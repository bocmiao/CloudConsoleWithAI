# CloudConsoleWithAI 设计方案（v0.2）

> 用自然语言管理腾讯云：说出目标，AI 生成执行计划，你确认一次，系统自动完成跨产品的全部步骤并验证结果。可以直接问网站访问数据和服务器状态；出了问题，AI 逐层取证给出结论；觉得哪里不对劲（比如内存占用高），AI 先用数据判断是不是真有问题，再给出具体的优化方案，你同意后自动执行，并在执行后汇报效果。

---

## 1. 要解决的问题

**建站**：以「新建一个网站并接入 EdgeOne」为例，现在要在 3~4 个控制台之间来回切换：

1. **EO 控制台**：添加站点 → 选接入方式 → 选套餐 → 归属权验证（又要去 DNSPod 加 TXT 记录）
2. **EO 控制台**：添加加速域名，填写源站
3. 复制 EO 分配的 CNAME → **DNSPod 控制台**添加 CNAME 记录
4. 回到 EO：等待 CNAME 生效 → 配置 HTTPS 证书
5. **服务器控制台**：防火墙/安全组放行 80/443；登录服务器配置 Nginx `server_name`
6. 自己用浏览器、`dig` 验证是否生效

值要来回复制，中间有多次「等待生效」，容易漏步骤（最常见的是防火墙没放行），出错后也不知道卡在哪一步。

**看数据、做优化**：

- 访问统计要在 EO 控制台翻好几个页面，服务器监控又在另一个控制台，两边的数据对不上号；
- 发现流量突增、响应变慢，要自己去对比 Top IP、Top URL 才能找到原因；
- 觉得「内存占用太高」，但不知道是不是真有问题、是谁占的、该改哪个配置、改成多少；改了又怕把网站搞挂。

**目标**

- 一句话 → 一个计划 → 一次确认 → 自动完成 → 自动验证
- 统计和状态**直接问**，得到带数字的答案，而不是自己翻图表
- 排障和优化给出**结论 + 证据 + 具体改法**；你同意后自动执行，出问题自动回滚，事后汇报效果
- 安全：未经你确认，AI 不能做任何写操作

**v1 不做**：替代控制台的全部功能、多云、自动购买/续费（只提示，不自动执行）。

---

## 2. 核心设计决定

### 2.1 AI 负责理解和规划，确定性代码负责执行

如果让大模型直接逐个调用云 API，会有这些问题：步骤会漏、轮询等待不可靠、中途失败无法恢复、事后无法审计。所以能力分三层：

| 层 | 是什么 | 例子 |
|---|---|---|
| **原子工具** | 对腾讯云 API 的一对一、带类型的封装 | `dns.list_records`、`eo.create_domain`、`eo.traffic_top` |
| **剧本（Recipe）** | 用代码写死的多步流程，内置顺序、幂等检查、轮询等待、回滚 | `site.publish`、`site.diagnose`、`host.optimize_memory` |
| **AI（Agent）** | 识别意图、补全参数、选择剧本或组合原子工具、解释结果 | 「我那台广州的服务器」→ 从资源清单查到 `lhins-xxx / 1.2.3.4` |

**剧本是这个产品的核心资产**：它把「EO 接入的正确做法」「PHP-FPM 该怎么调」这类经验固化成代码。比如：

- 域名已托管在 DNSPod → `CreateZone` 使用 `Type=dnsPodAccess`（DNSPod 托管接入），不需要手工做归属权验证；
- 否则用 `partial`（CNAME 接入），剧本自动去 DNSPod 写 TXT 记录，再轮询 `VerifyOwnership`；
- 加速区域 `Area`：域名未备案时只能选 `overseas`（全球，不含中国大陆），已备案才能选 `mainland`/`global`；
- NS 接入模式下，新建的加速域名不会自动启用，需要调用 `ModifyDnsRecordsStatus`；
- 判断内存是否紧张要看 `available`，而不是 `used`，`buff/cache` 是可以回收的。

这些细节 AI 临场发挥很容易出错，写进代码就一劳永逸。

### 2.2 AI 没有直接的写权限

提供给大模型的工具只有两类：

- **只读工具（R0）**：直接执行，结果返回给 AI；
- **`propose_plan`**：AI 只能**提交计划**，计划进入「待确认」状态，由人确认后交给执行引擎。

也就是说，写操作只有一条路径：**AI 提交计划 → 人确认 → 执行引擎执行**。这道确认关卡由执行引擎强制执行，不依赖提示词。即使模型被日志、网页里的恶意文本注入了指令，也绕不过确认。

### 2.3 风险分级

| 级别 | 例子 | 策略 |
|---|---|---|
| **R0 只读** | 查询解析/实例/监控/访问统计，TAT 只读诊断命令，公网探测 | 自动执行 |
| **R1 新增类、可逆** | 新增解析记录、添加加速域名、部署免费证书、放行 80/443、创建快照 | 整个计划确认一次 |
| **R2 影响线上** | 修改/删除已有解析、切换源站、修改 EO 缓存/安全配置、修改服务器配置文件、重载/重启服务 | 逐项确认，并展示改动内容和回滚方案 |
| **R3 破坏性/花钱** | 删除站点、销毁实例、重装系统、购买套餐/升级规格、对 0.0.0.0/0 开放 22/3306 | 输入资源名二次确认；默认关闭，需在设置中开启 |

### 2.4 执行过程可恢复、可审计

- 每个计划是一个**持久化的工作流**，步骤状态写入数据库；
- 异步步骤（归属权验证、CNAME 生效、证书签发）由引擎轮询等待，超时可以断点重试；
- 每一步都有 `check`（已完成则跳过，保证幂等）→ `apply` → `wait` → `undo`；
- 每次 API 调用记录 `Action`、脱敏后的参数、`RequestId`、**变更前快照**，用于审计和一键回滚。

### 2.5 优化建议也是计划

- **先证实问题，再给方案**：你说「内存太高」，AI 先看数据。如果只是 Linux 把空闲内存拿来做缓存，结论就是「不需要优化」，并解释原因。
- 每条建议都写清楚：**问题、数据依据、改什么（精确到文件 diff 或 API 参数）、预期效果、风险等级、会不会中断服务、怎么回滚**。
- 你勾选同意的建议会转换成一个**计划**，走和建站完全相同的执行引擎：同样的确认、审计、回滚。
- 常见问题由**规则**识别，参数由**公式**计算（比如 PHP-FPM 的 `max_children`）；AI 负责综合解释，以及处理规则覆盖不到的情况。
- 服务器上的修改脚本来自代码里的**变更模板**，AI 只填参数。模板以外的自由命令一律按 R2 处理，并额外标注「非标准变更」。

---

## 3. 一句话建站

> 用户：「把 blog.example.com 上线，源站是我那台广州的轻量服务器，走 EO，开 HTTPS」

### 3.1 预检（全部是 R0，并行执行）

| 检查项 | 调用 | 用来决定 |
|---|---|---|
| 根域名是否托管在 DNSPod | `dnspod.DescribeDomainList` | EO 接入方式：`dnsPodAccess` / `partial` / `full` |
| EO 是否已有该站点、是否已绑套餐 | `teo.DescribeZones` | 复用还是新建站点 |
| 子域名是否已有记录 | `dnspod.DescribeRecordList` | 已有 A 记录时，计划里标为「修改」（R2），并备份旧值 |
| 找到源站服务器 | `lighthouse.DescribeInstances` / `cvm.DescribeInstances` | 名称/地域 → 公网 IP，是否在运行 |
| 源站端口是否放行 | `lighthouse.DescribeFirewallRules` / `vpc.DescribeSecurityGroupPolicies` | 是否需要新增 80/443 规则 |
| 源站 Web 服务是否正常 | `tat.RunCommand`：`ss -lntp`、`curl -sI -H 'Host: blog.example.com' http://127.0.0.1` | 服务是否在监听、是否识别该 Host |
| 是否备案 | 读取用户配置（没有则询问） | `Area` 取值 |

### 3.2 生成的计划卡片

```
目标：上线 blog.example.com → 源站 1.2.3.4（lhins-xxxx，广州），EO 加速 + HTTPS

[R1] 1. EO 创建站点 example.com（DNSPod 托管接入，区域：全球不含中国大陆），绑定已有套餐
[R1] 2. EO 添加加速域名 blog.example.com，源站 1.2.3.4，回源 HTTP:80
[R1] 3. DNSPod 添加 CNAME：blog → <EO 分配的 CNAME>（若 EO 已自动添加则跳过）
[R1] 4. 轻量服务器防火墙放行 TCP 80、443
[R1] 5. 为 blog.example.com 部署 EO 免费证书
[R0] 6. 验证：公网解析、HTTPS 返回 200、证书有效、加速域名状态为 online

预计耗时 3~10 分钟，预计费用 0
                                   [确认执行]  [修改]  [取消]
```

### 3.3 剧本 `site.publish` 的步骤

| 步骤 | check（已完成就跳过） | apply | wait |
|---|---|---|---|
| ensureZone | `DescribeZones` | `CreateZone(ZoneName, Type=dnsPodAccess\|partial, Area)`；partial 模式下再用 DNSPod `CreateRecord` 写入 TXT 验证记录 | 轮询 `VerifyOwnership` |
| ensurePlan | 站点是否已绑套餐 | `BindZoneToPlan`（复用已有套餐）；需要新购时（`CreatePlanForZone`）单独列为 R3 步骤 | — |
| ensureDomain | `DescribeAccelerationDomains` | `CreateAccelerationDomain(ZoneId, DomainName, OriginInfo{OriginType: IP_DOMAIN, Origin}, OriginProtocol)` | 轮询 `DomainStatus` |
| ensureCname | DNSPod 是否已有指向 `AccelerationDomain.Cname` 的记录 | DNSPod `CreateRecord` / `ModifyRecord`（修改时先保存旧值） | 轮询 `CheckCnameStatus` |
| ensureOriginOpen | `DescribeFirewallRules` | `CreateFirewallRules(80, 443)` | — |
| ensureHttps | 域名证书配置 | `ModifyHostsCertificate(Mode=eofreecert)`；失败时改走 `ApplyFreeCertificate(dns_challenge)` → 写入验证记录 → `CheckFreeCertificateVerification` → `ModifyHostsCertificate(Mode=eofreecert_manual)` | 轮询证书状态 |
| verify | — | 多个公网 DoH 解析、HTTPS 探测、TLS 证书检查 | — |

### 3.4 回滚

每一步都有逆操作：删除新建的 CNAME 或恢复旧记录值、`DeleteAccelerationDomains`、删除新增的防火墙规则；站点只有在「本次新建」时才会被删除。界面上提供「撤销本次操作」按钮。

---

## 4. 网站访问统计（EO 数据分析）

只有经过 EO 的流量才有这些统计。没接 EO 的站点，退而通过 TAT 只读分析源站的 Nginx 访问日志，得出访问量、Top URL、状态码等核心数据。

### 4.1 看板

| 模块 | 指标 | 数据来源 |
|---|---|---|
| 概览 | 请求数、流量、峰值带宽、QPS、平均响应耗时、首字节耗时 | `DescribeTimingL7AnalysisData`：`l7Flow_request`、`l7Flow_flux`、`l7Flow_outBandwidth`、`l7Flow_requestRate`、`l7Flow_avgResponseTime`、`l7Flow_avgFirstByteResponseTime` |
| 访问来源 | Top 国家/地区、省份、客户端 IP、Referer | `DescribeTopL7AnalysisData`：`l7Flow_request_country` / `_province` / `_sip` / `_referers` |
| 内容 | Top URL、资源类型 | `l7Flow_request_url` / `_resourceType` |
| 终端 | 设备、浏览器、操作系统、UA | `l7Flow_request_ua_device` / `_ua_browser` / `_ua_os` / `_ua` |
| 质量 | 状态码分布，4xx/5xx 比例 | `l7Flow_request_statusCode` |
| 缓存 | 缓存命中率、未命中的 Top URL | `DescribeTimingL7CacheData`（按 `cacheType` = `hit` / `miss` / `dynamic` 过滤）、`DescribeTopL7CacheData` |
| 回源 | 回源请求数、回源流量（反映源站压力） | `DescribeTimingL7OriginPullData`：`l7Flow_request_hy`、`l7Flow_inFlux_hy` |
| 安全 | DDoS 攻击情况 | `DescribeDDoSAttackData`、`DescribeDDoSAttackTopData` |
| 原始日志 | 逐条请求明细（需开启离线日志） | `DownloadL7Logs` |

时间粒度（1 分钟 / 5 分钟 / 1 小时 / 1 天）按查询范围自动选择。查询结果短期缓存，避免触发接口限频。

### 4.2 直接问

- 「昨天网站访问量多少？和上周同一天比呢？」
- 「最近哪些页面 404 最多？」→ Top URL，按状态码 404 过滤
- 「今天下午流量为什么突然涨了？」→ **自动归因**：对比异常时段和正常时段的 Top IP / URL / UA / 国家，找出变化最大的维度
- 「缓存命中率多少？怎么提高？」→ 转入优化建议（见 7.3）

回答示例：

```
今天 14:00~15:00 的流量是平时的 6.3 倍，峰值带宽 48 Mbps

主要来源：
  · 客户端 IP 203.0.113.7 贡献了 71% 的请求，UA 为 python-requests/2.31
  · 请求集中在 /wp-content/uploads/*.jpg
判断：疑似爬虫在批量拉取图片，不是正常用户访问

建议（需确认）：
  [R2] 1. EO 安全策略：封禁 203.0.113.7，24 小时
  [R2] 2. /wp-content/uploads/ 开启防盗链，只允许本站 Referer
  [R2] 3. 单 IP 请求速率限制：10 秒内最多 60 次
```

### 4.3 日报 / 周报

每天早上推送：访问量和流量（附同比、环比）、Top 页面、错误率、缓存命中率、平均响应耗时、异常事件、服务器健康摘要（见第 5 节）。

---

## 5. 服务器状态

### 5.1 数据来源

| 来源 | 提供什么 | 说明 |
|---|---|---|
| 云监控 `GetMonitorData` | CPU、内存、磁盘、公网带宽等分钟级趋势 | 不需要登录服务器，有历史数据。轻量和 CVM 的命名空间不同，具体指标名用 `DescribeBaseMetrics` 查询 |
| TAT 实时快照（只读模板） | 进程级 CPU/内存排行、目录占用、监听端口、服务状态、OOM 记录、Docker 容器 | 能看到「是谁在占用」。需要实例上的自动化助手在线（`DescribeAutomationAgentStatus`） |
| 实例信息 | 运行状态、规格、到期时间、流量包用量 | `lighthouse.DescribeInstances`、`lighthouse.DescribeInstancesTrafficPackages`；CVM 用 `cvm.DescribeInstances` |

### 5.2 服务器总览

```
服务器          规格    CPU   可用内存   磁盘    带宽峰值   流量包   到期
blog-gz  广州   2核2G   12%   0.12G ⚠   61%     8 Mbps   43%     2027-03-01
api-sh   上海   4核8G   35%   5.10G     88% ⚠   22 Mbps  —       2026-10-08 ⚠
```

健康规则（可配置）：

- 内存看**可用内存（available）**：低于总量 10%，或近 7 天出现过 OOM
- 磁盘使用率 > 85%
- CPU 持续 15 分钟 > 80%；负载 > 核数 × 1.5
- 到期时间 < 15 天；流量包用量 > 80%

### 5.3 直接问

- 「哪台服务器最危险？」
- 「blog 那台这周内存趋势怎么样？」
- 「现在是什么在吃 CPU？」

### 5.4 自动识别服务器上跑的是什么（服务器画像）

不需要你告诉系统服务器上跑了什么，连接服务器时会自动识别。

**第一步：不登录服务器，先从云 API 推测**

轻量服务器的镜像信息（`DescribeInstances` 返回 `BlueprintId` → `DescribeBlueprints`）：`BlueprintType` 为 `APP_OS` 的是应用镜像，镜像名称通常直接说明装了什么（如 WordPress、宝塔面板、Docker）。不过这只反映「开机时装了什么」，之后自己装的东西要靠第二步。

**第二步：通过 TAT 执行只读识别脚本 [`scripts/discover.sh`](../scripts/discover.sh)**

| 段落 | 识别内容 |
|---|---|
| system | 系统版本、CPU/内存/swap/磁盘 |
| panel | 宝塔（面板端口、已装软件、PHP 版本）、1Panel |
| ports | 监听端口 → 进程 |
| services | 运行中 / 失败的 systemd 服务 |
| procs | 按内存、CPU 排行的进程 |
| web | Nginx/Apache/Caddy 版本；每个站点的域名、端口、网站目录、反向代理和 PHP 后端 |
| php | PHP-FPM 版本、每个进程池的进程数和平均内存、`pm` 配置、`memory_limit` |
| db | MySQL/MariaDB/PostgreSQL/Redis/MongoDB 进程；MySQL 内存参数；Redis `maxmemory` |
| docker | 容器、compose 项目目录、内存/CPU、内存上限、日志配置、磁盘占用 |
| apps | WordPress（目录、版本、关键开关）、Java（堆参数、jar、所属 systemd 服务）、Node/Python |
| cron | 定时任务 |
| security | SSH 端口/密码登录/root 登录、系统防火墙、腾讯云 agent（TAT、云监控、主机安全） |
| health | 近 7 天 OOM、大目录、inode 使用率 |

脚本的约束：

- **只读**：不改文件、不装软件、不重启服务、不连数据库；Nginx 站点信息直接读配置文件，不执行 `nginx -t`。已用 strace 验证，运行过程中没有以写方式打开过任何文件（`/dev/null` 除外）；
- **脱敏**：密码、密钥、token 类的值替换成 `***`；`wp-config.php` 只提取几个开关常量，不输出数据库账号密码；
- **精简**：TAT 单次输出上限 24KB，典型输出约 5KB；超出时按段落分多次执行（`discover.sh docker web`）；
- **兼容**：只用 POSIX sh 语法，bash 和 dash 下都能运行。

**第三步：生成服务器画像，接入资源关系图**

- 脚本负责采集事实，AI 负责解释：陌生进程是什么、这台机器是「宝塔 + WordPress」还是「Docker 跑的 Java 服务」、哪个站点对应哪个进程；
- 画像接入资源关系图：`blog.example.com → EO → 源站 1.2.3.4:80 → Nginx 站点 → /var/www/blog → WordPress → PHP-FPM 池 www`，排障和优化时沿着这条链路定位；
- 按画像启用对应环境的修改模板；识别出来但还没有模板的软件，只给文字建议。

**什么时候重新识别**：首次连接、每天一次、每次执行修改之前（作为预检的一部分，环境变了就中止）。

**前提**：服务器上的 TAT 自动化助手在线（`DescribeAutomationAgentStatus`）。不在线时只能用第一步的镜像信息，并提示你安装。要在云监控里看到内存等指标，还需要云监控 agent 在运行，识别脚本会一并检查。

---

## 6. 排障

> 用户：「blog.example.com 打不开了」/「服务器很卡」

### 6.1 网站打不开：分层诊断树（全部是 R0）

```
1. DNS 层   多个公网解析器(DoH)的结果 vs DNSPod 记录；记录是否被暂停；域名是否过期、NS 是否正确
2. EO 层    加速域名状态；CheckCnameStatus；证书是否过期；源站健康；近 1 小时状态码分布（5xx）；安全策略是否拦截
3. 网络层   实例是否在运行；防火墙/安全组是否放行；公网 IP 是否变化；带宽是否打满（云监控）
4. 主机层   TAT 执行只读诊断包：uptime / free -m / df -h / ss -lntp / systemctl status / journalctl / error.log 末尾
5. 应用层   带 Host 头 curl 127.0.0.1；PHP-FPM / Node 进程；数据库连接
```

诊断**从上往下**进行，某一层发现异常就深入这一层，而不是一上来把所有数据都拉一遍。

### 6.2 服务器很卡

```
CPU 高？    → 进程排行 → 业务进程：对照 EO 回源请求数，看是不是访问量上涨
                        → 陌生进程、持续满 CPU、连接陌生 IP：疑似被入侵/挖矿，按安全事件处理
内存不足？  → 可用内存低 / 有 OOM / swap 频繁读写 → 转 7.2 内存优化
磁盘 IO？   → iowait 高 → 找出正在大量读写的进程
带宽打满？  → 公网出带宽满 → 对照 EO 回源流量，看是否有大文件没被缓存
```

### 6.3 输出格式：结论 / 证据 / 修复计划

```
结论：源站 Nginx 未运行（磁盘写满导致启动失败）              置信度：高

证据：
  · EO 源站健康检查：1.2.3.4:80 连接被拒绝（持续 20 分钟）
  · TAT `df -h`：/dev/vda1 使用率 100%
  · TAT `journalctl -u nginx`：No space left on device
  · 最大文件：/var/log/nginx/access.log，38G

修复计划（R2，需确认）：
  1. 清空 access.log，并配置 logrotate
  2. 启动 nginx
  3. 复查：HTTPS 探测返回 200
```

### 6.4 在服务器上执行命令（TAT）

- 使用腾讯云**自动化助手 TAT**（`RunCommand` + `DescribeInvocationTasks`）在服务器上执行命令，**不需要保存 SSH 密钥**；
- 诊断阶段只执行**预置的只读命令模板**（固定命令、参数化）；
- 修改类操作走 7.4 的安全执行框架；
- 命令输出、日志、网页内容一律**当作数据，而不是指令**（防止提示注入）。

---

## 7. 优化建议 → 你同意 → 自动执行

### 7.1 闭环

```
采集数据(R0) → 判断是否真有问题 → 生成建议 → 你勾选同意 → 安全执行 → 立即验证 → 观察期复盘 → 保留或回滚
```

建议的优先级：不影响服务的调整 > 平滑重载即可生效 > 需要重启服务 > 花钱升级。

### 7.2 例子：「我觉得服务器内存占用太高」

**第 1 步：采集（R0）**

- 云监控近 7 天的内存曲线：判断是一直很高、周期性高峰，还是持续线性上涨（疑似内存泄漏）；
- TAT 执行只读快照模板：

```bash
free -m; swapon --show
ps -eo pid,user,rss,etime,cmd --sort=-rss | head -20
journalctl -k --since "7 days ago" | grep -iE "out of memory|killed process" | tail -20
docker stats --no-stream 2>/dev/null
# 常见服务的内存相关配置
grep -rhE "innodb_buffer_pool_size" /etc/mysql /etc/my.cnf* 2>/dev/null
grep -rhE "^pm(\.|\s)" /etc/php/*/fpm/pool.d/ 2>/dev/null
```

**第 2 步：判断（规则库）**

| 现象 | 判断 | 建议 |
|---|---|---|
| used 高，但 available 充足，大部分是 buff/cache | **正常**：Linux 用空闲内存做缓存，需要时会释放 | 不需要处理；告警改为看可用内存 |
| 小内存机器没有 swap，且出现过 OOM | 应对峰值的余量不足 | 添加 1~2G swap，`vm.swappiness=10` |
| MySQL `innodb_buffer_pool_size` 占总内存比例过大 | 配置超出机器规格 | 按机器内存和同机其他服务重新计算（需重启 MySQL） |
| PHP-FPM 子进程数 × 单进程内存 过大 | `pm.max_children` 设得太高 | `max_children ≈ 可分配给 PHP 的内存 ÷ 单进程平均内存`，改用 `pm = ondemand` |
| Java 进程没设 `-Xmx`，或设得过大 | 堆内存没有上限 | 设置合理的 `-Xmx` |
| 某进程内存 7 天持续线性增长 | 疑似内存泄漏 | 短期：用 systemd `MemoryMax` 限制，或定时重启；长期：修复代码 |
| 运行着用不到的服务/容器 | 浪费内存 | 停用并取消开机自启（先确认用途） |
| 以上都优化后仍不够 | 机器规格偏小 | 升级规格（R3，要花钱） |

**第 3 步：给出建议（勾选后执行）**

```
内存分析：blog-gz（lhins-xxxx，2核2G）
当前：已用 1.78G，可用 0.12G；近 7 天发生 3 次 OOM（mysqld 被杀 2 次）
结论：内存确实紧张，主要原因是 MySQL 和 PHP-FPM 的配置超出了 2G 机器的承受能力

内存占用排行：
  mysqld          820M   （innodb_buffer_pool_size = 1G）
  php-fpm × 24    610M   （pm.max_children = 50，单进程约 25M）
  nginx            40M

优化建议：
 [✓] 1. 添加 2G swap，swappiness=10            R2  防止再次 OOM          不中断服务
 [✓] 2. PHP-FPM：ondemand，max_children=12     R2  预计节省约 300M       平滑重载，不中断
 [ ] 3. MySQL：innodb_buffer_pool_size=256M    R2  预计节省约 550M       需重启 MySQL（约 5 秒中断），建议今晚 03:00 执行
 [ ] 4. 升级到 4G 规格                          R3  费用见报价

                        [执行选中项]   [定时执行]   [查看每项详情]
```

**第 4 步：每项详情（以第 2 项为例）**

```
修改文件：/etc/php/8.2/fpm/pool.d/www.conf
- pm = dynamic
- pm.max_children = 50
+ pm = ondemand
+ pm.max_children = 12
+ pm.process_idle_timeout = 10s

依据：可分配给 PHP 的内存约 300M ÷ 单进程平均 25M ≈ 12
生效：平滑重载（reload），不中断访问
回滚：恢复备份文件并 reload
```

**第 5 步：执行** —— 按 7.4 的安全执行框架执行。

**第 6 步：验证和复盘**

- 立即验证：服务状态正常、本机和公网 HTTP 探测返回 200、错误日志没有新增报错；
- 24 小时后自动复盘，对比执行前后的数据：

```
优化效果报告（执行后 24 小时）
  可用内存：    0.12G  →  0.71G
  OOM 次数：    3 次/周 →  0
  平均响应耗时：312ms  →  298ms（没有变慢）
  PHP-FPM 告警：无 "server reached pm.max_children"
结论：建议保留本次修改                                   [回滚]
```

如果复盘发现副作用（比如 `max_children` 调得太低，出现 502 或 PHP-FPM 进程数打满的告警），AI 会指出来，并建议回滚或调整参数，调整同样要你确认。

### 7.3 例子：EO 缓存命中率低

```
缓存命中率 23%，77% 的请求回源；未命中的 Top URL 是 /static/*.js、/uploads/*.jpg，都是静态资源

建议：
 [R2] 1. 规则引擎：js/css/图片/字体 缓存 30 天（CreateL7AccRules）
 [R2] 2. 开启 Brotli/Gzip 压缩（ModifyL7AccSetting）
 [R2] 3. 开启 HTTP/3（ModifyL7AccSetting）
预期：命中率提升到 80% 以上，回源流量和源站 CPU 明显下降
验证：执行后对比 24 小时的命中率和回源请求数（DescribeTimingL7OriginPullData）
回滚：删除新增规则、恢复原配置（执行前已用 DescribeL7AccSetting 保存）
```

缓存规则默认只针对静态文件扩展名，**不会缓存 HTML 等动态页面**，以免把登录状态之类的内容缓存给别人。

### 7.4 服务器变更的安全执行框架

每一次在服务器上的修改，都按固定的 6 步执行：

```
① 预检     目标文件/服务存在；当前值与计划中的「修改前」一致，不一致就中止（防止基于过期数据修改）
② 备份     配置文件复制到 /var/backups/cloudconsole/<变更ID>/；系统级改动可先创建实例快照
③ 修改     按变更模板修改
④ 校验     nginx -t / php-fpm -t / mysqld --validate-config 等
⑤ 生效     能 reload 就不 restart；需要重启的改动可以放到维护窗口定时执行
⑥ 健康检查 服务状态 + 本机 HTTP 探测 + 公网探测；失败则自动恢复②的备份，并通知你
```

由模板生成的脚本示例（执行前原样展示给你）：

```bash
set -euo pipefail
CHANGE_ID=chg_20260925_001
CONF=/etc/php/8.2/fpm/pool.d/www.conf
BACKUP=/var/backups/cloudconsole/$CHANGE_ID
rollback() { cp -a "$BACKUP/www.conf" "$CONF"; systemctl reload php8.2-fpm; }

# ① 预检
grep -q '^pm.max_children = 50$' "$CONF" || { echo "当前配置已变化，中止"; exit 9; }
# ② 备份
mkdir -p "$BACKUP" && cp -a "$CONF" "$BACKUP/"
# ③ 修改
sed -i -e 's/^pm = .*/pm = ondemand/' -e 's/^pm.max_children = .*/pm.max_children = 12/' "$CONF"
grep -q '^pm.process_idle_timeout' "$CONF" || echo 'pm.process_idle_timeout = 10s' >> "$CONF"
# ④ 校验
php-fpm8.2 -t || { cp -a "$BACKUP/www.conf" "$CONF"; exit 10; }
# ⑤ 生效
systemctl reload php8.2-fpm || { rollback; exit 11; }
# ⑥ 健康检查
sleep 3
if ! systemctl is-active --quiet php8.2-fpm \
   || ! curl -fsS -o /dev/null -H 'Host: blog.example.com' http://127.0.0.1/; then
  rollback; exit 20
fi
```

补充规则：

- 同一台服务器上的变更**串行执行**（加锁），避免两个计划同时修改同一台机器；
- **定时执行**：你事先确认，到时间自动执行；执行时重新做一次①预检，状态变了就中止并通知你；
- 首批变更模板：swap、PHP-FPM、MySQL 内存参数、Nginx worker/缓冲区、logrotate、停用服务、systemd `MemoryMax`；
- 模板按服务器画像（5.4）匹配环境：同一种修改在直接安装、宝塔、Docker 下各有一套实现（配置文件路径、校验命令、生效方式都不同）。

---

## 8. 主动巡检

定时（每小时/每天）检查。发现问题先自动做一次诊断（R0），再把「结论 + 建议」推送到企业微信/飞书/邮件；点开后仍然走计划确认。

- 证书 30 天内过期；实例、域名、EO 套餐即将到期；流量包即将用完
- 可用内存持续偏低或出现 OOM，磁盘使用率 > 85%，CPU/负载持续偏高
- 请求量、带宽、5xx 比例与历史同期相比突增；源站健康检查失败；缓存命中率明显下降
- 解析记录指向已释放的 IP，或指向的实例已停机
- 安全组/防火墙对公网开放了高危端口（22/3306/6379 等）

每天的日报见 4.3。

---

## 9. 系统架构

```
┌──────────────────────────────── 交互层 ─────────────────────────────────┐
│ Web 控制台：对话 / 访问统计看板 / 服务器总览 / 计划与变更记录              │
│ 企业微信/飞书机器人：日报、告警、移动端确认    MCP：Claude 等 AI 客户端直接调用 │
└───────────────────────────────────┬─────────────────────────────────────┘
┌──────────────────────────────── Agent 层 ───────────────────────────────┐
│ 意图识别 → 参数补全（查资源清单）→ 选剧本 / 组合工具 → propose_plan        │
│ 分析引擎：规则库 + 异常检测（与历史同期对比）+ AI 归因与解释               │
└───────────────────────────────────┬─────────────────────────────────────┘
┌──────────────────────── 执行层（确定性代码，不依赖 AI）──────────────────┐
│ 策略引擎：风险分级、确认关卡      工作流引擎：持久化、轮询、重试、回滚       │
│ 调度器：巡检、定时执行、24 小时复盘  审计日志：RequestId、变更前快照         │
└───────────────────────────────────┬─────────────────────────────────────┘
┌──────────────────────────────── 工具层 ─────────────────────────────────┐
│ Recipes：site.publish / site.diagnose / host.optimize_memory / eo.tune_cache … │
│ Atomic ：dnspod.* teo.* lighthouse.* cvm.* vpc.* tat.* monitor.*        │
│ 变更模板：swap / php-fpm / mysql / nginx / logrotate …                  │
│ Probes ：DoH 解析 / HTTP(S) 探测 / TLS 证书检查                          │
│ 资源清单：域名 → 解析记录 → EO 加速域名 → 源站 IP → 实例 → 防火墙 的关系图 │
│ 指标缓存：EO 统计 / 云监控数据短期缓存，减少接口调用                        │
└───────────────────────────────────┬─────────────────────────────────────┘
                         腾讯云 API 3.0（官方 SDK，TC3 签名）
```

**资源关系图**是一个重要的差异点：控制台里「这个域名解析到哪、走没走 EO、源站是哪台机器、这台机器的防火墙开了什么」这些关系是割裂的。系统定期同步后，AI 能直接回答「blog 的源站是哪台机器」，也能把 EO 的访问数据和源站服务器的负载放在一起分析（比如「回源请求翻倍」和「源站 CPU 打满」同时出现）。

---

## 10. 技术选型建议

| 部分 | 选择 | 理由 |
|---|---|---|
| 语言 | TypeScript 全栈 | 前后端同一种语言；有腾讯云官方 `tencentcloud-sdk-nodejs` 和 MCP 官方 TS SDK；Zod schema 同时用于参数校验和 LLM 工具定义 |
| 前端 | Next.js（或 Vite + React）+ ECharts | 对话、卡片式 UI、统计图表 |
| 后端 | Node（Hono / Fastify） | Agent、执行引擎、调度器 |
| 存储 | SQLite（单人自部署）→ PostgreSQL（多用户） | 计划、步骤、审计、资源清单、指标缓存 |
| 工作流 | 先自研轻量状态机（步骤表 + 定时轮询），复杂后再考虑 Temporal | 早期不引入重型依赖 |
| LLM | 模型适配层，默认 Claude（`claude-opus-5`，工具调用 + adaptive thinking） | 如果服务部署在中国大陆，需要考虑模型的可访问性与合规，适配层可切换到国内模型 |
| 部署 | Docker，部署在你自己的轻量服务器上 | 云 API 密钥不离开你自己的机器 |

**建议的目录结构**

```
apps/web              Web 控制台
apps/server           API、Agent、执行引擎、调度器
packages/core         Plan / Step / 风险等级 / 审计 的领域模型
packages/tools        原子工具（腾讯云 SDK 封装 + 探测）
packages/recipes      剧本
packages/analyzers    规则库、异常检测、优化建议生成
packages/templates    服务器变更模板
packages/mcp          MCP Server 入口（复用 tools + recipes）
```

---

## 11. 安全设计

1. **最小权限**：使用 CAM 子用户，只授予需要的产品权限；绝不使用主账号密钥。也支持 CAM 角色 + STS 临时凭证。
2. **密钥不进入 LLM 上下文**：密钥加密存储，只在执行层使用；工具返回给 AI 的结果先脱敏。
3. **AI 无直接写权限**：见 2.2，写操作只能走「计划 → 确认 → 执行」。
4. **TAT 等同于 root shell**：只读模板和变更模板以外的命令一律 R2；可以在设置里完全关闭「执行任意命令」，只允许模板。
5. **服务器变更可回滚**：先备份、先校验、健康检查失败自动回滚（见 7.4）。
6. **提示注入防护**：日志、网页、命令输出都视为不可信数据；风险等级由策略引擎按操作类型硬编码，不采信 AI 的判断。
7. **审计**：每次 API 调用记录操作人、时间、Action、脱敏参数、RequestId、结果。

MVP 阶段的 CAM 策略示例（上线前按实际用到的接口再收紧）：

```json
{
  "version": "2.0",
  "statement": [
    { "effect": "allow", "action": ["teo:*", "dnspod:*"], "resource": "*" },
    {
      "effect": "allow",
      "action": [
        "lighthouse:DescribeInstances",
        "lighthouse:DescribeInstancesTrafficPackages",
        "lighthouse:DescribeFirewallRules",
        "lighthouse:CreateFirewallRules",
        "lighthouse:DeleteFirewallRules",
        "lighthouse:CreateInstanceSnapshot",
        "cvm:Describe*",
        "vpc:Describe*",
        "tat:RunCommand",
        "tat:Describe*",
        "monitor:GetMonitorData",
        "monitor:DescribeBaseMetrics"
      ],
      "resource": "*"
    }
  ]
}
```

---

## 12. 数据模型（核心表）

```
credentials      id, name, secret_id, secret_key_encrypted, default_region
resources        id, type, provider_id, name, region, attrs_json, synced_at
resource_edges   from_id, to_id, relation        -- domain→record→eo_domain→origin→instance→site→app
host_profiles    resource_id, stack_json, raw_output_redacted, collected_at   -- 服务器画像（5.4）
plans            id, goal, recipe, input_json, status, created_by, confirmed_by, confirmed_at, scheduled_at
steps            id, plan_id, seq, action, params_json, risk, status,
                 request_id, before_snapshot, result_json, undo_json, error, attempts
recommendations  id, resource_id, category(memory|cpu|disk|cache|security|...), finding_json,
                 evidence_json, status(open|accepted|dismissed|done), plan_id
change_reviews   id, plan_id, metric, before_value, after_value, window, verdict(keep|rollback)
schedules        id, kind(inspection|report|plan_execution|review), cron_or_time, target, enabled
metric_cache     resource_id, source, metric, interval, ts, value
audit_logs       id, actor, action, params_redacted, request_id, result, created_at
conversations    id, ...;  messages  id, conversation_id, role, content, plan_id?
```

---

## 13. MVP 工具清单

**建站与解析**

| 工具 | 底层 API | 风险 |
|---|---|---|
| `dns.list_domains` | dnspod `DescribeDomainList` | R0 |
| `dns.list_records` | dnspod `DescribeRecordList` | R0 |
| `dns.upsert_record` | dnspod `CreateRecord` / `ModifyRecord` | R1（新增）/ R2（修改） |
| `dns.delete_record` | dnspod `DeleteRecord` | R2 |
| `eo.list_zones` | teo `DescribeZones` | R0 |
| `eo.create_zone` | teo `CreateZone` | R1 |
| `eo.verify_ownership` | teo `DescribeIdentifications` / `VerifyOwnership` | R0 |
| `eo.list_domains` | teo `DescribeAccelerationDomains` | R0 |
| `eo.create_domain` | teo `CreateAccelerationDomain` | R1 |
| `eo.check_cname` | teo `CheckCnameStatus` | R0 |
| `eo.set_certificate` | teo `ModifyHostsCertificate` | R1 |
| `eo.purge_cache` | teo `CreatePurgeTask` | R1 |

**访问统计与 EO 优化**

| 工具 | 底层 API | 风险 |
|---|---|---|
| `eo.traffic_timeseries` | teo `DescribeTimingL7AnalysisData` | R0 |
| `eo.traffic_top` | teo `DescribeTopL7AnalysisData` | R0 |
| `eo.cache_stats` | teo `DescribeTimingL7CacheData` / `DescribeTopL7CacheData` | R0 |
| `eo.origin_stats` | teo `DescribeTimingL7OriginPullData` | R0 |
| `eo.ddos_stats` | teo `DescribeDDoSAttackData` / `DescribeDDoSAttackTopData` | R0 |
| `eo.get_acc_setting` / `eo.set_acc_setting` | teo `DescribeL7AccSetting` / `ModifyL7AccSetting` | R0 / R2 |
| `eo.list_rules` / `eo.add_rules` | teo `DescribeL7AccRules` / `CreateL7AccRules` | R0 / R2 |
| `eo.block_ips` | teo `CreateSecurityIPGroup` + `ModifySecurityPolicy` | R2 |

**服务器**

| 工具 | 底层 API | 风险 |
|---|---|---|
| `lh.list_instances` | lighthouse `DescribeInstances` | R0 |
| `lh.traffic_packages` | lighthouse `DescribeInstancesTrafficPackages` | R0 |
| `lh.list_firewall` / `lh.add_firewall_rule` | lighthouse `DescribeFirewallRules` / `CreateFirewallRules` | R0 / R1 |
| `lh.create_snapshot` | lighthouse `CreateInstanceSnapshot` | R1 |
| `cvm.list_instances`、`vpc.list_sg_policies` | cvm `DescribeInstances`、vpc `DescribeSecurityGroupPolicies` | R0 |
| `monitor.list_metrics` | monitor `DescribeBaseMetrics` | R0 |
| `monitor.metrics` | monitor `GetMonitorData` | R0 |
| `lh.blueprint` | lighthouse `DescribeBlueprints`（推测镜像预装了什么） | R0 |
| `tat.agent_status` | tat `DescribeAutomationAgentStatus` | R0 |
| `host.discover` | tat `RunCommand`（`scripts/discover.sh`，只读识别） | R0 |
| `host.snapshot` | tat `RunCommand`（只读快照模板）+ `DescribeInvocationTasks` | R0 |
| `host.diagnose` | tat `RunCommand`（只读诊断模板） | R0 |
| `host.access_log_stats` | tat `RunCommand`（分析 Nginx 访问日志，用于没接 EO 的站点） | R0 |
| `host.apply_change` | tat `RunCommand`（变更模板，含备份、校验、健康检查、自动回滚） | R2 |
| `host.exec` | tat `RunCommand`（任意命令） | R2 |
| `probe.resolve` / `probe.http` / `probe.tls` | 本地实现 | R0 |

---

## 14. 路线图

| 阶段 | 内容 | 目的 |
|---|---|---|
| **M0**（1~2 周） | 工具层 + `site.publish` + `site.diagnose` + **只读的访问统计问答、服务器状态和环境识别**，以 **MCP Server** 形式提供，直接在 Claude Code / Claude Desktop 里使用。写操作以 `plan_*` 生成计划、`apply_plan(plan_id)` 执行的形式提供 | 零 UI 成本先验证价值；统计和状态都是 R0，风险最低、见效最快 |
| **M1**（3~4 周） | Web 控制台：对话、计划卡片、执行时间线、访问统计看板、服务器总览、资源关系视图、审计日志 | 成为日常入口 |
| **M2** | 优化闭环：规则库 + 首批变更模板（swap、PHP-FPM、MySQL、Nginx、logrotate、EO 缓存/压缩、IP 封禁）+ 定时执行 + 24 小时复盘；巡检 + 日报推送；更多剧本（切换源站、已有站点迁移到 EO、COS 静态站 + EO、WordPress 一键部署） | 从「帮我做」到「主动发现、给出方案」 |
| **M3** | 多账号、团队审批、多云（阿里云 DNS、Cloudflare 等）Provider 抽象 | 扩展 |

---

## 15. 待确认的问题

1. **自用还是做成产品？** 决定是否需要多租户、密钥托管方式。
2. **模型和部署位置**：用 Claude 还是国内模型？服务部署在大陆还是海外？
3. **域名情况**：域名都在 DNSPod 吗？是否已备案？（决定默认的 EO 接入方式和加速区域）
4. **服务器上跑的东西**：系统上线后会自动识别（见 5.4）。开发时先写哪几套模板，可以先在你的服务器上运行 `scripts/discover.sh`，看结果再决定。
5. **自动执行的边界**：是否只允许执行模板内的修改，完全禁止 AI 自由编写的命令？
6. **先做哪一步**：先做 M0（MCP，1~2 周可用），还是直接做 Web 控制台？
