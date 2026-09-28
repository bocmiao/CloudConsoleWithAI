# Miao Panel 设计方案（v0.5）

> **Miao Panel**：一个**开源（GPL-3.0）、通用**的 AI 服务器与云管理助手：任何 Linux 服务器都能用（装了 1Panel、宝塔，或者什么面板都没装），云产品先支持腾讯云（EdgeOne、DNSPod、轻量、CVM）。先做成**本地 exe**，之后做 Web 版。
>
> 用自然语言管理：说出目标，AI 生成执行计划，你确认一次，系统自动完成跨产品的全部步骤并验证结果。可以直接问网站访问数据和服务器状态；出了问题，AI 逐层取证给出结论；觉得哪里不对劲（比如内存占用高），AI 先用数据判断是不是真有问题，再给出具体的优化方案，你同意后自动执行，并在执行后汇报效果。

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
- **开源、通用**：服务器这一层不绑定任何云厂商和面板；看不懂命令的人也能安全使用

**v1 不做**：替代控制台的全部功能、腾讯云以外的云产品（服务器本身不限云厂商）、自动购买/续费（只提示，不自动执行）。

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
- 服务器上的修改脚本来自代码里的**变更模板**，AI 只填参数。模板覆盖不到时，可以开启**受限的 AI 自由命令**（见 7.5）。

### 2.6 开源、通用：连接方式 × 环境适配器 × 云插件

为了「什么服务器都能用」，把能力拆成三个互相独立、都可以扩展的部分：

| 部分 | 解决什么 | 第一批支持 | 以后 |
|---|---|---|---|
| **连接方式** | 怎么在服务器上执行命令 | **SSH**（任何云、任何 VPS 都能用）、**腾讯云 TAT**（不需要开放 SSH） | 阿里云云助手等 |
| **环境适配器** | 同一个操作在不同环境里怎么做 | **1Panel v2**（走 1Panel API）、**纯 Linux**（直接改配置文件）、**宝塔**（走宝塔 API 或改宝塔管理的文件） | 其他面板 |
| **云插件** | 云厂商的产品 | **腾讯云**：EdgeOne、DNSPod、轻量、CVM、云监控 | 阿里云、Cloudflare 等 |

- 环境适配器由识别脚本（5.4）自动选择，不需要用户告诉系统装了什么；
- 计划里写的是**能力**（比如「设置 PHP-FPM 最大进程数」），由适配器决定具体怎么做：1Panel 上调 API，宝塔上改宝塔的配置文件，纯 Linux 上改系统的配置文件；
- 没有云插件的服务器（比如别家云的 VPS）照样能用：状态、诊断、优化、自由命令都只依赖 SSH；只是用不了 EO 建站、EO 统计这类云产品功能；
- 模板用 YAML 描述（见 8.10），社区贡献模板不需要写 Go 代码。

### 2.7 本地 exe 和 Web 版

- **同一套代码两种运行方式**：本地 exe 模式（双击运行，在本机启动服务并打开界面）和服务器模式（Web 版，`miaopanel serve`，部署到服务器上，登录后使用，见「Web 版」一节和 [DEPLOY.md](DEPLOY.md)）；
- **数据都在你自己电脑上**：配置和记录存本地 SQLite 文件；云 API 密钥、SSH 密钥、面板 API 密钥、AI 模型 Key 存 Windows 凭据管理器（系统钥匙串），不存明文；
- **本地界面的安全**：只监听 `127.0.0.1`、随机端口，并用一次性令牌校验每个请求，防止本机其他程序或网页冒充你操作；
- **电脑关机怎么办**：
  - 定时执行（比如「今晚 3 点重启 MySQL」）交给服务器自己：把带备份、校验、自动回滚的脚本登记成服务器上的一次性定时任务，电脑关机也会执行，结果在下次打开 exe 时同步；
  - 巡检、日报、24 小时复盘只在 exe 运行时进行（可以最小化到托盘常驻）。错过的复盘在下次打开时补做；需要 7×24 巡检就用 Web 版；
- **AI 模型**：用户自己填 API Key。默认支持 Claude；同时支持兼容 OpenAI 接口的模型（DeepSeek、通义千问、混元等），方便国内用户。

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

[R1] 1. EO 创建站点 example.com（DNSPod 托管接入，区域：中国大陆，已备案），绑定已有套餐
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
| ensureSite（装了 1Panel 时） | 1Panel `POST /websites/search` | 1Panel `POST /websites` 在源站创建网站（静态 / PHP / 反向代理），见 8.6 | — |
| ensureHttps | 域名证书配置 | `ModifyHostsCertificate(Mode=eofreecert)`；失败时改走 `ApplyFreeCertificate(dns_challenge)` → 写入验证记录 → `CheckFreeCertificateVerification` → `ModifyHostsCertificate(Mode=eofreecert_manual)` | 轮询证书状态 |
| verify | — | 多个公网 DoH 解析、HTTPS 探测、TLS 证书检查 | — |

### 3.4 回滚

每一步都有逆操作：删除新建的 CNAME 或恢复旧记录值、`DeleteAccelerationDomains`、删除新增的防火墙规则；站点只有在「本次新建」时才会被删除。界面上提供「撤销本次操作」按钮。

---

## 4. 网站访问统计（EO 数据分析）

只有经过 EO 的流量才有这些统计。没接 EO 的站点，退而通过 SSH 或 TAT 只读分析源站的 Nginx 访问日志，得出访问量、Top URL、状态码等核心数据。

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

**第二步：通过 SSH 或 TAT 执行只读识别脚本 [`scripts/discover.sh`](../scripts/discover.sh)**

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

### 6.4 在服务器上执行命令（SSH / TAT）

- 通用方式是 **SSH**（任何云、任何 VPS）；腾讯云服务器也可以用**自动化助手 TAT**（`RunCommand` + `DescribeInvocationTasks`），不需要开放 SSH 端口；
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
- 模板按服务器画像（5.4）匹配环境：同一种修改在直接安装、宝塔、Docker 下各有一套实现（配置文件路径、校验命令、生效方式都不同）；装了 1Panel 的服务器优先走 1Panel 的 API（见第 8 节）。

### 7.5 受限的 AI 自由命令（给看不懂命令的人用）

模板覆盖不到的情况（比如一个少见的软件要调参数），AI 可以现场写命令。**但如果你看不懂命令，「让你确认命令」起不到保护作用**，所以安全不能靠你读命令，要靠系统替你把关。这个功能默认关闭，在设置里开启时会先显示风险说明。

**1. 限定范围**

| 类别 | 例子 | 处理 |
|---|---|---|
| 允许 | 修改服务的配置文件、重载/重启服务、清理日志和缓存目录、查看类命令 | 按下面的流程执行 |
| 需要额外确认 | 安装/升级软件包、重启服务器 | 单独标红，需要输入服务器名确认 |
| 直接拒绝 | 格式化、分区、直接写磁盘设备；删除系统目录，或删除路径事先确定不了的文件；下载并直接执行网上的脚本（`curl … \| bash`）；修改账号、密码、SSH 配置和登录密钥；关闭或清空防火墙；停止 sshd、TAT、面板等关键服务；清除日志和操作记录；大范围改权限（`chmod -R 777`） | AI 需要换一种做法，或者等正式模板 |
| 面板管理的文件 | 1Panel、宝塔自己管理的配置文件 | 不允许自由命令直接改，必须走面板 API 或模板，否则会被面板覆盖 |

**2. 你看到的是「会发生什么」，不是命令**

```
AI 想在 blog-gz 上执行一段自定义操作（非标准模板）            风险：中

要做什么：给 Nginx 开启 gzip 压缩，减少网页传输大小

会改动：
  · 修改 1 个文件：/etc/nginx/nginx.conf
      + gzip on;
      + gzip_types text/css application/javascript;
  · 重新加载 Nginx（不中断访问）
不会：删除文件、安装软件、改动账号或防火墙

安全措施：
  ✓ 已在隔离环境里试运行，实际改动和上面一致
  ✓ 独立审查通过：说明和命令一致，没有发现危险操作
  ✓ 执行前自动备份上面的文件
  ✓ 5 分钟保险：执行后如果网站或连接异常，服务器会自动恢复原状
最坏情况：Nginx 配置出错导致网站打不开 → 自动恢复，最长约 5 分钟

[查看原始命令（高级）]                         [取消]   [我了解，执行]
```

**3. 执行前，系统替你检查命令**

1. **AI 必须先声明影响**：会改哪些文件、重启哪些服务、会不会装软件、会不会访问外网，以结构化的形式给出；
2. **逐条解析命令**：用 shell 语法解析器把命令拆开（不是简单的关键字匹配），找出所有会写入的文件和会执行的程序，与 AI 的声明对比。**有没声明的影响，或者写入位置在执行前确定不了（比如路径要运行时才算得出来），就拒绝执行**；
3. **对照上面的范围和禁止清单**；
4. **试运行**：在服务器上用一个隔离的文件层执行一遍，所有文件改动只落在临时层里，服务的重启、重载只记录不执行，由此得到**真实的修改前后对比**。做不了试运行的情况（系统不支持、需要联网安装软件）会明确标出，这时必须先做快照才能继续；
5. **独立审查**：另开一次 AI 调用，只给它看命令、声明和试运行结果（不给它看日志、网页等外部内容，避免被诱导），判断三者是否一致、有没有危险操作。审查不通过就不执行。

**4. 执行时的保护**

- **快照**：服务器支持快照时（1Panel 系统快照、腾讯云轻量或云硬盘快照等），执行自由命令前默认先做一次快照。这是最后一道防线；
- **自动备份**：解析出来的每个写入目标文件都先备份；
- **5 分钟保险**：执行前先在服务器上预约一个「5 分钟后自动恢复」的任务（`systemd-run --on-active=300`，没有 systemd 时用 `at` 或后台延时任务）。执行完成后，系统确认网站正常、连接正常，才取消这个预约。**如果命令把 SSH 或防火墙改坏了、系统连不上服务器，服务器会自己恢复。** 恢复脚本由系统生成，不由 AI 编写；
- **健康检查**：失败立即恢复备份，并重启受影响的服务；
- **使用限制**：只能立即执行，你必须在线；一次只对一台服务器执行；不能定时，不能无人值守；命令长度和执行时间都有上限。

**5. 执行之后**

- 命令、声明、试运行结果、审查意见、执行输出全部存档；
- 成功执行过的自由命令可以脱敏后导出，提交到开源项目的模板库，**由懂代码的维护者审核后变成正式模板**。看不懂命令的用户也能从社区的审核中受益。

**要说清楚的是**：这些措施能大幅降低风险，但做不到零风险。真正兜底的是快照和 5 分钟保险，所以建议开启自由命令之前，先确认服务器能做快照。

---

## 8. 环境适配（1Panel / 宝塔 / 纯 Linux）

每种环境一个适配器（见 2.6）。1Panel v2 是第一个完整支持的环境，8.1~8.7 的内容来自 1Panel 源码；宝塔和纯 Linux 见 8.8、8.9；模板格式见 8.10。

### 8.1 1Panel 的结构

- 安装目录记录在 `/usr/local/bin/1pctl` 的 `BASE_DIR`（默认 `/opt`），数据目录是 `<BASE_DIR>/1panel`；
- 应用商店装的应用（OpenResty、MySQL、Redis、WordPress 等）都以 **Docker Compose** 运行，目录 `<数据目录>/apps/<应用>/<名称>/`；
- 网站：配置在 `<数据目录>/www/conf.d/<网站>.conf`，文件在 `<数据目录>/www/sites/<网站>/`（`index`、`log`、`ssl`、`proxy` 等子目录）；
- PHP 运行环境在 `<数据目录>/runtime/php/<名称>/`；
- v2 的系统服务是 `1panel-core` 和 `1panel-agent`（v1 是 `1panel`）；
- 应用、网站、运行环境的参数记在 1Panel 自己的数据库里。

**关键结论：不要绕过 1Panel 直接改文件。** 直接改的内容可能在应用升级、重建或在面板里保存时被覆盖，面板显示的配置也会和实际不一致。所以在 1Panel 上，**能用 1Panel API 完成的修改一律走 API**：面板里看得到，也不会被覆盖。1Panel 管不到的系统级设置（如内核参数）才走 7.4 的 TAT 模板。

识别脚本（5.4）已适配 1Panel：会读出 1Panel 版本、安装时的端口、已装应用、网站、PHP 运行环境，以及应用目录下的 MySQL / Redis / PHP 配置。`1pctl` 里还存着初始用户名、密码和安全入口，脚本只按白名单读取 `BASE_DIR`、`ORIGINAL_VERSION`、`ORIGINAL_PORT`、`PANEL_EDITION` 这几个键。

### 8.2 优化项 → 1Panel 接口

接口路径都在 `/api/v2` 下。「生效方式」要在测试环境逐项确认后写进模板。

| 优化项 | 1Panel 接口 | 生效方式（预计） | 风险 |
|---|---|---|---|
| 添加 swap | `POST /toolbox/device/update/swap` | 立即生效 | R2 |
| PHP-FPM 进程数（`pm.max_children` 等） | `POST /runtimes/php/fpm/config` | 重启 PHP 运行环境 | R2 |
| php.ini（`memory_limit` 等） | `POST /runtimes/php/config` | 重启 PHP 运行环境 | R2 |
| MySQL 内存参数（`innodb_buffer_pool_size` 等） | `POST /databases/variables/update` | 可能需要重启 MySQL | R2 |
| Redis `maxmemory` / 淘汰策略 | `POST /databases/redis/conf/update` | 重启 Redis | R2 |
| 给应用容器设内存/CPU 上限 | `POST /apps/installed/params/update`（`advanced`、`memoryLimit`、`memoryUnit`、`cpuQuota`） | 重建该应用容器，短暂中断 | R2 |
| OpenResty 全局参数 | `POST /openresty/update` | reload | R2 |
| 网站反向代理缓存 | `POST /websites/proxy/config` | reload | R2 |
| 清理容器日志 | `POST /containers/clean/log` | 立即生效 | R2 |
| 清理垃圾文件、无用镜像 | `POST /toolbox/scan` 先扫描、列出可清理项，确认后 `POST /toolbox/clean` | 立即生效 | R2 |
| **改前备份** | 应用/网站/数据库：`POST /backups/backup`；大改动前做系统快照：`POST /settings/snapshot`（可用 `/settings/snapshot/rollback` 回滚） | — | R1 |

**只读数据**（用于状态、诊断和优化分析）：

| 数据 | 1Panel 接口 |
|---|---|
| 系统概况 | `GET /dashboard/base/os`、`/dashboard/base/all/all` |
| 监控历史（CPU/内存/负载/IO/网络） | `POST /hosts/monitor/search`，1Panel 自带监控，不依赖云监控 agent |
| 各容器资源占用 | `GET /containers/list/stats` |
| 已装应用 | `POST /apps/installed/search` |
| 网站 | `POST /websites/search` |
| PHP-FPM 实时状态 | `GET /runtimes/php/fpm/status/:id` |
| 监听端口 → 进程 | `POST /process/listening` |

### 8.3 安全地连上 1Panel API

- **认证**：请求头 `1Panel-Token` + `1Panel-Timestamp`。推荐 HMAC-SHA256：再加请求头 `1Panel-Signature-Version: hmac-sha256`，token = HMAC-SHA256(API 密钥, `"1panel:" + 时间戳`)。旧的 MD5 方式是 token = md5(`"1panel" + API 密钥 + 时间戳`)；
- **要先在 1Panel「面板设置」里开启 API 接口，并配置 IP 白名单**。白名单为空时所有 API 请求都会被拒绝；时间戳超出有效期也会被拒绝；
- 1Panel 的 API 密钥 = 整个面板的控制权（几乎等于 root），和云 API 密钥一样加密保存，不进入 AI 上下文；
- **不要把 1Panel 端口暴露到公网**。连接方式：

| 方式 | 做法 | 适用 |
|---|---|---|
| A. SSH 隧道（本地 exe 默认） | exe 通过 SSH 把服务器本机的面板端口转发到你的电脑上再调用 API，请求在服务器上来自 `127.0.0.1`，白名单只填 `127.0.0.1` | 面板端口完全不对外，推荐 |
| B. 通过 TAT 在服务器本机调用 | TAT 在服务器上执行 `curl http://127.0.0.1:<面板端口>/api/v2/...`，API 密钥用 TAT 的隐藏参数 `{{tat-hidden:key}}` 传入 | 腾讯云服务器、不开放 SSH 时；每次调用多几秒延迟，适合执行修改，不适合实时看板 |
| C. 与面板部署在同一台服务器 | 访问 `127.0.0.1:<面板端口>` | 以后的 Web 版 |
| D. 远程直连 | 防火墙只对你的 IP 放行面板端口，全程 HTTPS | 不推荐 |

### 8.4 官方 mcp-1panel

1Panel 官方有 MCP Server（[1Panel-dev/mcp-1panel](https://github.com/1Panel-dev/mcp-1panel)），默认只读，能查询仪表盘、网站、证书、已装应用、数据库，也能建站、申请证书、建库、安装 OpenResty/MySQL，**但没有调优类接口**。我们的程序直接调用 1Panel API（查询和调优都覆盖），mcp-1panel 可以作为接口用法的参考。

### 8.5 1Panel 下的内存优化示例

```
内存分析：blog-gz（2核2G，1Panel v2）
当前可用内存 0.15G；近 7 天 OOM 2 次（被杀的是 mysqld）

容器内存排行（/containers/list/stats）：
  1Panel-mysql-xxxx        690M   没有内存上限，innodb_buffer_pool_size = 1G
  1Panel-php8-xxxx         420M   pm.max_children = 30
  1panel-core / agent      120M
  1Panel-openresty-xxxx     60M

优化建议：
 [✓] 1. 添加 1G swap（1Panel 工具箱）                         R2  不中断
 [✓] 2. PHP 运行环境：pm.max_children 30 → 10                  R2  重启 PHP 运行环境，约几秒
 [ ] 3. MySQL 性能调整：innodb_buffer_pool_size → 256M         R2  可能需要重启 MySQL，建议 03:00 执行
 [ ] 4. MySQL 容器内存上限 800M（防止它拖垮整台机器）          R2  重建容器，约 10 秒中断

执行前：通过 1Panel 备份 MySQL 应用和数据库
执行后：在 1Panel 面板里能看到同样的修改；24 小时后自动复盘
```

### 8.6 建站流程也能用上 1Panel

第 3 节的一句话建站可以多做一步：在 1Panel 上创建网站（`POST /websites`，静态、PHP 或反向代理），这样「EO + DNSPod + 源站网站」全部一次完成，不用再进 1Panel 手动建站。

### 8.7 1Panel 首批模板

1. 添加 swap
2. PHP 运行环境的 FPM 参数和 `php.ini`
3. MySQL 性能参数
4. 应用容器内存/CPU 上限
5. Redis `maxmemory` 和淘汰策略
6. 容器日志清理、垃圾文件清理（先扫描再清理）
7. OpenResty 参数、网站反向代理缓存
8. 改前备份和系统快照
9. 建站：在 1Panel 创建网站（配合 `site.publish`）

### 8.8 宝塔

- 目录：软件在 `/www/server/` 下（Nginx 在 `/www/server/nginx`，PHP 在 `/www/server/php/<版本>`，MySQL 配置是 `/etc/my.cnf`），网站配置在 `/www/server/panel/vhost/nginx/`，日志在 `/www/wwwlogs/`；
- 宝塔也有官方 API（面板设置 → API 接口），签名方式是 `request_token = md5(request_time + md5(api_sk))`，同样要配置 IP 白名单，连接方式同 8.3；
- 策略和 1Panel 一样：宝塔 API 能做的走 API；API 覆盖不到的调优项改宝塔管理的配置文件，并提示「在宝塔面板里再次保存对应设置，可能会覆盖本次修改」。宝塔 API 对各个调优项的覆盖范围还需要逐项核实。

### 8.9 纯 Linux 服务器（没装面板）

- 直接修改系统的配置文件，走 7.4 的六步安全框架（预检、备份、修改、校验、生效、健康检查）；
- 按发行版区分路径和命令，比如 Debian/Ubuntu 是 `/etc/php/<版本>/fpm/pool.d/` 和 `systemctl reload php<版本>-fpm`，RHEL/Rocky/OpenCloudOS 是 `/etc/php-fpm.d/` 和 `systemctl reload php-fpm`；
- 用 Docker Compose 部署的应用：修改 compose 文件或挂载进容器的配置文件，用 `docker compose up -d` 生效（直接改容器里的文件，容器重建后就丢了）。

### 8.10 模板格式（社区可贡献）

模板是 YAML 文件，一个模板描述一种能力，按环境给出不同实现。示意：

```yaml
id: php-fpm.max-children
title: 调整 PHP-FPM 最大进程数
risk: R2
params:
  max_children: { type: int, min: 2, max: 500 }   # 由规则按「可用内存 ÷ 单进程内存」计算
implementations:
  1panel:                                  # 走 1Panel API
    api: POST /api/v2/runtimes/php/fpm/config
    body: { id: "{{runtime_id}}", params: { pm.max_children: "{{max_children}}" } }
  bt:                                      # 宝塔：改宝塔管理的配置文件
    file: /www/server/php/{{ver}}/etc/php-fpm.conf
    set: { pm.max_children: "{{max_children}}" }
    validate: /www/server/php/{{ver}}/sbin/php-fpm -t
    reload: /etc/init.d/php-fpm-{{ver}} reload
  linux-debian:
    file: /etc/php/{{ver}}/fpm/pool.d/www.conf
    set: { pm.max_children: "{{max_children}}" }
    validate: php-fpm{{ver}} -t
    reload: systemctl reload php{{ver}}-fpm
verify:
  - service_active: php-fpm
  - http_probe: { expect_status: 200 }
```

- 预检、备份、健康检查、回滚由执行引擎统一处理，模板作者只需要描述「改什么、怎么校验、怎么生效」；
- 每个模板在 CI 里对多种环境自动测试：Ubuntu、Debian、Rocky、OpenCloudOS 的 Docker 镜像，以及装了 1Panel、宝塔的测试机；
- 社区贡献模板只需要提交 YAML 和测试用例，维护者审核通过后发布。

---

## 9. 主动巡检

定时（每小时/每天）检查。发现问题先自动做一次诊断（R0），再把「结论 + 建议」推送到企业微信/飞书/邮件；点开后仍然走计划确认。

- 证书 30 天内过期；实例、域名、EO 套餐即将到期；流量包即将用完
- 可用内存持续偏低或出现 OOM，磁盘使用率 > 85%，CPU/负载持续偏高
- 请求量、带宽、5xx 比例与历史同期相比突增；源站健康检查失败；缓存命中率明显下降
- 解析记录指向已释放的 IP，或指向的实例已停机
- 安全组/防火墙对公网开放了高危端口（22/3306/6379 等）

每天的日报见 4.3。

本地 exe 模式下，巡检和推送只在 exe 运行时进行（可以最小化到托盘常驻，见 2.7）；需要 7×24 巡检就用 Web 版。

---

## 10. 系统架构

```
┌──────────────────────────────── 界面 ─────────────────────────────────┐
│ 本地 exe：浏览器界面（只监听 127.0.0.1）+ 托盘     以后：Web 版（多用户） │
│ 对话 / 服务器总览 / 访问统计 / 计划与变更记录       可选：MCP 模式         │
└───────────────────────────────────┬───────────────────────────────────┘
┌──────────────────────────────── Agent ────────────────────────────────┐
│ 模型适配：Claude / OpenAI 兼容接口（DeepSeek、通义千问、混元等）           │
│ 意图识别 → 参数补全（查资源清单）→ 选剧本 / 能力 / 自由命令 → propose_plan │
│ 分析引擎：规则库 + 异常检测 + AI 归因；自由命令的独立审查                  │
└───────────────────────────────────┬───────────────────────────────────┘
┌──────────────────────── 执行层（确定性代码，不依赖 AI）─────────────────┐
│ 策略引擎：风险分级、确认关卡          工作流引擎：持久化、轮询、重试、回滚   │
│ 自由命令关卡：解析、禁止清单、试运行、快照、5 分钟保险                     │
│ 调度器：巡检、复盘（定时执行交给服务器）  审计日志                          │
└───────────────────────────────────┬───────────────────────────────────┘
┌──────────────────────────────── 能力与适配 ───────────────────────────┐
│ 剧本：site.publish / site.diagnose / host.optimize_memory …            │
│ 模板（YAML）：能力 → 按环境的实现                                         │
│ 环境适配器：1Panel（API）/ 宝塔（API + 文件）/ 纯 Linux（文件）            │
│ 云插件：腾讯云（EO、DNSPod、轻量、CVM、云监控），以后更多                  │
│ 资源清单与服务器画像；探测：DoH 解析 / HTTP(S) / TLS                      │
└───────────────────────────────────┬───────────────────────────────────┘
┌──────────────────────────────── 连接方式 ─────────────────────────────┐
│ SSH（任何服务器；也用来建立到面板 API 的隧道）   腾讯云 TAT   云厂商 API  │
└───────────────────────────────────────────────────────────────────────┘
```

**资源关系图**是一个重要的差异点：控制台里「这个域名解析到哪、走没走 EO、源站是哪台机器、这台机器的防火墙开了什么」这些关系是割裂的。系统定期同步后，AI 能直接回答「blog 的源站是哪台机器」，也能把 EO 的访问数据和源站服务器的负载放在一起分析（比如「回源请求翻倍」和「源站 CPU 打满」同时出现）。

---

## 11. 技术选型

为了打包成单个 exe、以后又能直接变成 Web 版，后端改用 **Go**。之前建议的 TypeScript 打包成 exe 体积大，SSH 等系统能力也不如 Go 成熟。

| 部分 | 选择 | 理由 |
|---|---|---|
| 后端 | Go | 编译成单个 exe，不需要安装任何运行环境；能交叉编译出 Windows、macOS、Linux 版本；SSH、并发、系统操作成熟；1Panel 也是 Go 写的 |
| 前端 | Vue 3，构建后嵌进 exe（`go:embed`） | 本地版和 Web 版共用同一套界面 |
| 数据库 | SQLite（纯 Go 驱动 `modernc.org/sqlite`，不依赖 cgo） | 本地一个文件；Web 版可以换 PostgreSQL |
| 密钥存储 | 系统钥匙串（`go-keyring`：Windows 凭据管理器 / macOS 钥匙串 / Linux Secret Service） | 不存明文 |
| SSH | `golang.org/x/crypto/ssh` | 执行命令，以及到面板 API 的端口转发 |
| 腾讯云 | `tencentcloud-sdk-go`（官方） | |
| 命令解析 | `mvdan.cc/sh`（shfmt 使用的 shell 解析器） | 自由命令的逐条解析（7.5） |
| AI | OpenAI 兼容接口（DeepSeek、通义千问、Kimi、GLM、豆包、混元、MiniMax 等）+ Claude 官方 Go SDK | 用户自带 Key；默认 DeepSeek V4.1 Flash，见 11.1 |
| MCP（可选） | MCP 官方 Go SDK | 同一套工具也能给 Claude Code 等客户端使用 |
| 模板 | YAML 文件 | 社区贡献不需要写代码（8.10） |
| 构建发布 | GitHub Actions 自动构建各平台的单文件程序并发布到 Releases（Windows 优先） | |

### 11.1 AI 模型选择（2026-09 调研）

目标是「性价比最高」：工具调用能力要强（查数据、写计划、生成命令），价格要低，并且在中国大陆能直接使用。

| 用途 | 推荐 | 价格（元 / 百万 tokens：输入未命中 / 缓存命中 / 输出） | 理由 |
|---|---|---|---|
| **默认（主力）** | **DeepSeek V4.1 Flash**（`deepseek-flash`，`https://api.deepseek.com`） | 2 / 0.04 / 8（工作日 9–12 点、14–18 点为高峰价，其余时段半价） | 便宜档里公开的终端/智能体评测成绩最高；1M 上下文；缓存命中价极低，适合反复发送同一套系统提示和工具定义的智能体 |
| 简单总结 | 通义千问 Qwen3.8-Flash | 0.8 / 约 0.16 / 2.7 | 很便宜；和审查模型同一个阿里云 Key |
| 独立审查（M1 自由命令） | 通义千问 Qwen3.8-Max | 12 / 1.5 / 36 | 和主力模型不同厂商，减少「同一种错误」；一次审查只有几千 tokens，约 0.1 元 |
| 备选 | Qwen3.7-Plus、腾讯混元 Hy3、MiniMax M3、Kimi K2.6、GLM-5.3、豆包 Seed 2.1、免费的 GLM-4.7-Flash | 见程序内设置 | |
| 海外用户 | Claude Sonnet 5 / Opus 5 / Haiku 4.5 | 美元 2/0.2/10、5/0.5/25、1/0.1/5 | Claude API 不向中国大陆提供服务 |

估算：一次典型任务约 4 万输入 tokens（一半命中缓存）+ 3 千输出 tokens，**DeepSeek V4.1 Flash 做 100 次约 6.5 元（高峰）/ 3.2 元（非高峰）**。

注意：

- 除 Claude 外，各厂商价格来自 GitHub 上维护的价格表和新闻报道（调研环境无法直接打开厂商官网），模型和价格变化很快（DeepSeek 六周内调价三次），所以程序里的价格只是默认值，用户可以在设置里修改；
- DeepSeek 的思考模式在多步工具调用中需要把 `reasoning_content` 回传，程序已处理；
- 选 Claude Opus 5 时，如果模型拒绝某个请求，程序会自动用 Claude Opus 4.8 重试一次。

**目录结构**

```
cmd/miaopanel/          程序入口：本地模式（默认）/ server 模式 / mcp 模式
internal/core/           计划、步骤、风险等级、审计
internal/agent/          AI 对话、模型适配、独立审查
internal/connect/        连接方式：ssh、tat
internal/adapters/       环境适配器：linux、onepanel、bt
internal/cloud/tencent/  腾讯云插件：EO、DNSPod、轻量、CVM、云监控
internal/freecmd/        自由命令：解析、策略、试运行、5 分钟保险
internal/store/          SQLite、系统钥匙串
templates/               模板（YAML）
scripts/discover.sh      环境识别脚本
web/                     前端（Vue 3）
```

---

## 12. 安全设计

1. **最小权限**（腾讯云）：使用 CAM 子用户，只授予需要的产品权限；绝不使用主账号密钥。也支持 CAM 角色 + STS 临时凭证。
2. **密钥不进入 LLM 上下文**：密钥加密存储，只在执行层使用；工具返回给 AI 的结果先脱敏。
3. **AI 无直接写权限**：见 2.2，写操作只能走「计划 → 确认 → 执行」。
4. **SSH / TAT 等同于 root 权限**：只读模板和变更模板以外的命令都是自由命令，必须经过 7.5 的全部检查；自由命令默认关闭。
5. **面板（1Panel、宝塔）API 密钥等同于面板的完全控制权**：存系统钥匙串，不进入 AI 上下文；面板端口不对公网开放，通过 SSH 隧道或 TAT 在服务器本机调用；面板的 IP 白名单只放 `127.0.0.1`（见 8.3）。
6. **服务器变更可回滚**：先备份、先校验、健康检查失败自动回滚（见 7.4）。
7. **提示注入防护**：日志、网页、命令输出都视为不可信数据；风险等级由策略引擎按操作类型硬编码，不采信 AI 的判断。
8. **审计**：每次 API 调用记录操作人、时间、Action、脱敏参数、RequestId、结果。
9. **本地 exe**：界面只监听 `127.0.0.1`，每个请求都校验一次性令牌；所有密钥存系统钥匙串（见 2.7）。
10. **SSH**：推荐用密钥登录，也支持密码（同样存系统钥匙串）；首次连接记录服务器指纹，指纹变化时拒绝连接并提醒，防止中间人攻击。

腾讯云插件的 CAM 策略示例（上线前按实际用到的接口再收紧）：

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

## 13. 数据模型（核心表）

```
credentials      id, kind(tencentcloud|ssh|1panel|bt|llm), name, key_id, secret_ref, endpoint, default_region
                 -- secret_ref 指向系统钥匙串里的条目，数据库不存密钥本身
servers          id, name, connect_kind(ssh|tat), host, port, ssh_user, host_key_fingerprint,
                 cloud_resource_id, adapter(1panel|bt|linux), panel_credential_id
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
freecmd_runs     id, plan_id, server_id, script, declared_effects, parsed_effects, dryrun_diff,
                 review, snapshot_ref, guard_status, output, result   -- 自由命令全过程存档（7.5）
audit_logs       id, actor, action, params_redacted, request_id, result, created_at
conversations    id, ...;  messages  id, conversation_id, role, content, plan_id?
```

---

## 14. MVP 工具清单

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
| `host.discover` | SSH / TAT 执行 `scripts/discover.sh`（只读识别） | R0 |
| `host.snapshot` | SSH / TAT 执行只读快照模板 | R0 |
| `host.diagnose` | SSH / TAT 执行只读诊断模板 | R0 |
| `host.access_log_stats` | SSH / TAT 分析 Nginx 访问日志（用于没接 EO 的站点） | R0 |
| `host.apply_change` | SSH / TAT 执行变更模板（含备份、校验、健康检查、自动回滚） | R2 |
| `host.exec_free` | 自由命令，经过 7.5 的全部检查 | R2（安装软件、重启服务器为 R3） |
| `probe.resolve` / `probe.http` / `probe.tls` | 本地实现 | R0 |

**1Panel**（接口见 8.2，均在 `/api/v2` 下）

| 工具 | 底层接口 | 风险 |
|---|---|---|
| `op.overview` | `GET /dashboard/base/os`、`/dashboard/base/all/all` | R0 |
| `op.monitor` | `POST /hosts/monitor/search` | R0 |
| `op.container_stats` | `GET /containers/list/stats` | R0 |
| `op.list_apps` / `op.list_websites` | `POST /apps/installed/search`、`POST /websites/search` | R0 |
| `op.fpm_status` | `GET /runtimes/php/fpm/status/:id` | R0 |
| `op.backup` / `op.snapshot` | `POST /backups/backup`、`POST /settings/snapshot` | R1 |
| `op.create_website` | `POST /websites` | R1 |
| `op.set_swap` | `POST /toolbox/device/update/swap` | R2 |
| `op.set_fpm` / `op.set_php_ini` | `POST /runtimes/php/fpm/config`、`POST /runtimes/php/config` | R2 |
| `op.set_mysql_vars` | `POST /databases/variables/update` | R2 |
| `op.set_redis_conf` | `POST /databases/redis/conf/update` | R2 |
| `op.set_app_limits` | `POST /apps/installed/params/update` | R2 |
| `op.clean` | `POST /toolbox/scan` → `POST /toolbox/clean`、`POST /containers/clean/log` | R2 |

---

## 15. 路线图

| 阶段 | 内容 | 目的 |
|---|---|---|
| **M0 本地 exe 基础** | Go 程序骨架 + 内嵌网页界面；添加服务器（SSH、腾讯云 TAT）；环境识别；服务器状态；AI 对话（只读诊断）；计划—确认—审计框架；密钥存系统钥匙串；AI 用量和花费；新手向导 | 能装、能连、能看、能问 |
| **M1 修改能力** | 环境适配器（纯 Linux、1Panel v2 优先，宝塔随后）+ 首批 YAML 模板；7.4 安全执行框架；受限自由命令（解析、禁止清单、独立审查、快照、5 分钟保险；试运行先做技术验证）；执行后复盘 | 能安全地改 |
| **M2 腾讯云** | 一句话建站（EO + DNSPod + 面板建站）、EO 访问统计与归因、EO 缓存/安全优化 | 解决最初的建站痛点 |
| **M3 Web 版** | 同一程序以 server 模式部署：多用户、7×24 巡检、日报推送、移动端确认 | 常驻运行 |
| **M4 生态** | 更多云厂商（阿里云、Cloudflare 等）和面板、模板社区、MCP 模式 | 扩展 |

---

## 16. 可以添加的功能

下面是在已有设计之外，结合你的使用场景值得加的功能，按价值排了优先级，并放进对应阶段：

| 功能 | 做什么 | 阶段 |
|---|---|---|
| **新手向导** | 一步一步带你：配置 SSH 密钥登录；创建腾讯云子账号和最小权限策略（自动生成策略，告诉你在控制台点哪里）；开启 1Panel/宝塔 API 并设置白名单。全程不需要看懂命令 | M0 |
| **AI 用量和花费** | 每次对话显示花了多少钱，可以设每月上限；简单问题自动用便宜模型 | M0 |
| **安全体检** | 检查 SSH 是否允许密码/root 登录、有没有被暴力破解的迹象、面板端口是否暴露在公网、面板是否开了安全入口和两步验证、高危端口是否对公网开放、系统和软件多久没更新、证书是否快过期；给出修复计划（1Panel 上可以一键开启 Fail2ban 防爆破） | M1 |
| **备份体检和一键异地备份** | 检查网站和数据库有没有定期备份、备份是不是只存在同一台服务器上（服务器坏了会一起丢）；一键配置备份到腾讯云 COS；定期做「恢复演练」，确认备份真的能用 | M1 |
| **操作时间线** | 所有 AI 和人工的修改按时间排好。出了问题先看「最近改了什么」，并能从这里一键回滚 | M1 |
| **运维备忘** | 每台服务器一份备忘，比如「blog 是 WordPress」「白天不要重启 MySQL」「这台 5 月到期不续费」。AI 做计划时会遵守 | M1 |
| **日志 AI 摘要** | 读 Nginx 错误日志、PHP 慢日志、MySQL 慢查询，总结「最近 24 小时最主要的 3 个问题」和建议 | M1 |
| **费用和到期管家** | 腾讯云账单按产品汇总、和上月对比；实例、域名、证书、EO 套餐到期统一提醒；找出闲置资源（停机没释放的实例、没挂载的云硬盘、旧快照）；流量包快用完时预警 | M2 |
| **网站可用性监控** | 定时访问你的网站，打不开就提醒并自动开始诊断（exe 运行时进行，Web 版 7×24） | M2 |
| **应用更新提醒** | 1Panel 应用有新版本、WordPress 核心和插件更新、系统安全更新；更新前自动备份，更新后自动检查 | M2 |
| **通知渠道** | 企业微信、飞书、钉钉、邮件、Bark | M3 |
| **一键搬家** | 把网站从一台服务器迁到另一台：备份 → 在新机器恢复 → 切换 EO 源站 → 验证 | M4 |

---

## 17. 决定记录与待确认

**已确认**

- 名称：**Miao Panel**（不叫「喵面板」）；开源协议：**GPL-3.0**；
- 开源、通用：支持 1Panel、宝塔和没装面板的服务器；
- 先做本地 exe（Windows 优先），之后做 Web 版；
- 你的服务器是 1Panel v2；
- 开启受限的 AI 自由命令，按 7.5 的方式由系统把关；
- 域名在 DNSPod、已备案：EO 默认用 DNSPod 托管接入（`dnsPodAccess`），加速区域可以选中国大陆（`mainland`）或全球（`global`）；
- 默认 AI 模型：DeepSeek V4.1 Flash（见 11.1）。

**关于名称的一点提醒**：「Panel」容易让人以为这是又一个服务器面板，和 1Panel、宝塔是竞争关系；其实它是在这些面板之上工作的 AI 助手。建议在介绍里始终带上一句定位，比如「Miao Panel —— 帮你管服务器和云的 AI 助手，支持 1Panel、宝塔和纯 Linux」。

## 18. 当前进度

已完成（代码在 `cmd/`、`internal/`、`scripts/`）：

- 单文件程序：双击运行，在 `127.0.0.1` 启动网页界面并自动打开浏览器；一次性令牌登录 + Cookie + Host 校验；
- 添加服务器（SSH 密码或密钥文件），首次连接记录服务器指纹，指纹变化时拒绝连接；
- 密码和 API Key 存系统钥匙串（不可用时存数据目录下仅当前用户可读的文件）；
- 环境识别（通过 SSH 运行 `scripts/discover.sh`），服务器画像页面，自动发现问题（内存、磁盘、OOM、SSH 设置、失败的服务等）；
- AI 助手：只读工具（服务器列表、画像、重新识别、按项检查、近 24 小时错误日志），修改建议保存到「建议」页（风险等级由系统判定）；支持 DeepSeek 等 OpenAI 兼容接口和 Claude；每次回答显示花费，可设每月上限；
- 操作记录；新手引导；
- 自动化测试：进程内 SSH 服务器端到端测试、模拟模型 API 测试、HTTP 安全测试；GitHub Actions 自动测试并构建 Windows / macOS / Linux 程序。

- 对话记录保存在本地数据库：AI 助手页左侧是对话列表，打开程序时自动回到最近的对话；关掉程序后打开旧对话还能接着问（模型会收到之前的问题和回答，但之前工具查到的原始数据不会重发，需要时 AI 会重新查询；最多带最近 40 条消息）；删除对话不会删除里面生成的清单。

还没做（M0 剩余）：新手向导里的 SSH 密钥和腾讯云子账号引导（TAT 连接方式已在 M2 第三步完成）。

### M1 第一步：清单 + 一键执行（已完成）

完整流程「说需求 → AI 检查 → 生成清单 → 点一下执行 → 看结果 / 撤销」已经打通：

- AI 在对话里直接给出清单卡片（`propose_plan`），每一项显示风险等级、执行方式（系统脚本 / 1Panel 接口）、对网站的影响、能否撤销；当前环境不支持的项会标明原因并不能勾选；
- 勾选后二次确认，后台按顺序执行，前一项失败就停下，界面实时显示每一步的日志；
- 执行前后各做一次环境识别，显示「执行前后对比」（可用内存、swap、根分区、发现的问题数）；
- 能撤销的项目保存了撤销数据（原配置备份在服务器 `/var/backups/miaopanel/`），一键撤销；
- 同一台服务器同一时间只执行一个清单；每次执行和撤销都写入操作记录；
- 1Panel 服务器可以在服务器页填写 1Panel API 密钥，修改走 1Panel 自己的接口（通过 SSH 隧道访问，不需要把面板端口暴露到公网）。

目前能自动执行的操作（`internal/actions`，脚本在 `scripts/actions/`）：

| 操作 | 说明 | 支持环境 | 可撤销 |
|---|---|---|---|
| `swap.set` | 添加 swap 文件并设置 swappiness=10 | 全部 | 是 |
| `logs.clean` | 清理 journal、7 天前的归档日志、超过 100MB 的 Docker 日志 | 全部 | 否 |
| `service.restart` | 重启 systemd 服务（ssh、docker、面板等关键服务禁止） | 全部（需 systemd） | 否 |
| `php_fpm.set` | 调整 PHP-FPM 进程数 / 进程管理方式 | 纯 Linux（脚本）、1Panel（接口） | 是 |
| `mysql.vars.set` | 调整 innodb_buffer_pool_size、max_connections | 1Panel（接口） | 是 |
| `app.limits.set` | 设置 1Panel 应用（容器）的内存上限；低于当前占用 1.2 倍时拒绝；改完核对端口开放方式没变 | 1Panel（接口） | 是 |
| `java.heap.set` | 固定 1Panel 上 Java 应用（如 Halo）的最大堆：在应用的 docker-compose 里设置 `JAVA_TOOL_OPTIONS=-Xmx…`；启动命令里已有堆参数时拒绝（命令行优先）；重建后确认容器持续运行，否则恢复 | 1Panel（接口） | 是 |
| `backup.create` | 让 1Panel 备份应用，或备份 MySQL/MariaDB 里的数据库，等备份文件写完才算完成 | 1Panel（接口） | 不需要 |
| `container.restart` | 重启 Docker 容器并等它恢复运行（有健康检查时等到 healthy） | 全部 | 不需要 |

脚本约定：必须 root 运行；先检查再修改，修改前备份；失败自动回滚；退出码 0 完成、10 拒绝（未做修改）、20 已回滚、30 失败。脚本以后台方式运行，SSH 断开会自动重连继续读取进度。

### 执行日志与回滚（已完成）

「日志 → 执行日志」记录 Miao Panel 在服务器上执行的每一件事，包括 AI 回答问题时做的只读检查：

- 每条记录：时间、服务器、谁发起的（AI 检查 / 你操作的 / 清单）、类型（只读 / 修改 / 回滚）、结果和完整输出；
- 原样记录实际执行的命令（1Panel 操作记录请求的方法、路径和内容，不记录密钥），修改类操作保存脚本全文；
- 修改类记录写明能不能回滚：能回滚的说明回滚会做什么，并提供「回滚」按钮；不能回滚的写明原因（比如删掉的日志无法恢复、重启不需要回滚）；
- 回滚本身也是一条记录，和原记录互相链接；清单卡片上的每一步可以跳到对应记录，还可以「撤销全部」（按相反顺序逐项回滚，遇到失败就停）；
- 服务器上也留有记录：每次脚本执行在 `/var/log/miaopanel/actions.log` 记一行；能回滚的脚本修改会在备份目录里生成独立的 `rollback.sh`，就算电脑上的 Miao Panel 丢了，也能在服务器上用 root 执行它回滚；从 Miao Panel 回滚后它会改名为 `rollback.sh.done`；1Panel 接口的操作在 1Panel 自己的「日志审计 → 操作日志」里也能查到；
- Miao Panel 在执行中被关闭时，下次启动会把没结束的记录和清单步骤标记为「中断」，提示重新识别服务器确认结果。

还没做：宝塔面板的 PHP / MySQL 接口、1Panel 应用的其他参数。受限的 AI 自由命令和腾讯云操作见后面的小节。

### M2 第一步：腾讯云 DNSPod + EdgeOne（已完成）

「设置 → 腾讯云」填入子账号的 SecretId / SecretKey（存系统钥匙串；页面上有创建子账号、只授权 DNSPod 和 EdgeOne 的步骤）。之后：

- AI 能查：DNSPod 域名和解析记录（`tencent_dns`）、EdgeOne 站点、加速域名、分配的 CNAME、回源、证书，以及 DNS 是否已经解析到 EdgeOne（`tencent_eo`）。每次查询都记在执行日志里（服务器一栏显示「腾讯云」）；
- 一句话上线：比如「把 blog.example.com 接入 EO 并开 HTTPS，源站是我的服务器」，清单一般是下面三步，全部可以一键执行、单独回滚或整份撤销：

| 操作 | 说明 | 回滚 |
|---|---|---|
| `eo.domain.add` | 在域名所在的 EdgeOne 站点添加加速域名，回源到服务器（默认 HTTP 80）；已存在则跳过；站点没验证归属时给出要加的 TXT 记录 | 停用并删除这个加速域名 |
| `dns.record.set` | 设置 DNSPod 解析；`point_to=eo` 时执行时自动查询 EdgeOne 分配的 CNAME。A 改 CNAME 是原地修改，名字一直能解析；有多条冲突记录时先删多余的；多条同类型 A 记录（负载均衡）拒绝修改；值和现在一样时不做任何修改 | 新增的删除、改过的改回、删掉的加回 |
| `eo.https.set` | 申请并部署 EdgeOne 免费证书（自动续签）。CNAME 接入的站点会先确认 DNS 已经解析到 EdgeOne，否则拒绝（证书验证不了）；等证书部署好才算完成，申请失败自动恢复原设置 | 恢复原来的证书设置 |

- 只涉及腾讯云的清单不需要服务器（不连 SSH），和服务器步骤混在一起时按需连接；
- 接口签名（TC3-HMAC-SHA256）和官方 Go SDK 逐字节比对过；请求字段按官方 SDK 的定义编写；测试用一个会校验签名、按 DNSPod/EdgeOne 规则保存状态的模拟服务。

还没做：在 EdgeOne 新建站点（要选套餐、涉及计费，暂由用户在控制台操作）、在面板里自动建站。EdgeOne 统计和缓存管理见下面的 M2 第二步。

### Windows 桌面窗口（已完成）

Windows 版改成真正的桌面程序：双击后打开 Miao Panel 自己的窗口，不再打开浏览器，也没有命令行窗口。

- 窗口用系统自带的 WebView2（go-webview2，纯 Go，不需要 cgo），界面和浏览器版是同一套；程序仍然只在 127.0.0.1 上提供服务，窗口带一次性令牌登录，安全模型不变；
- 编译成 GUI 程序（`-H=windowsgui`）；资源文件带程序图标、版本信息和「每显示器 DPI 感知」声明，窗口大小按屏幕缩放计算，高分屏不发虚；
- 只允许运行一个：再次双击会把已经打开的窗口切到前面；
- 页面里在新窗口打开的链接交给系统浏览器；
- 缺少 WebView2 运行库时提示安装，并临时改用浏览器；`--browser` 参数也可以强制用浏览器。这两种情况下用一个提示框代替命令行窗口，点「确定」退出；
- 关闭窗口即退出；如果当时有清单正在执行，下次启动会把它标记为「中断」。

### M2 第二步：腾讯云服务器 + EdgeOne 统计与管理（已完成）

需要的子账号权限在「设置 → 腾讯云」的说明里：除了 DNSPod、EdgeOne，还要轻量应用服务器、云服务器 CVM、私有网络（安全组）、云硬盘（快照）和云监控（只读）。用不到的产品可以不授权，只想让 AI 查看的话选 ReadOnly 策略。

**服务器（轻量应用服务器 + CVM）**

- AI 能查：所有地域的实例列表（并发查询，结果缓存 5 分钟），包括状态、配置、公网/内网 IP、系统、计费方式、到期时间、自动续费、轻量服务器的流量包用量（`tencent_servers`）；单台实例的防火墙或安全组规则、快照、最近的 CPU/内存/带宽监控（`tencent_server_detail`，监控指标名先用 DescribeBaseMetrics 确认，数据按时间段取合适的粒度并压缩成摘要）。按公网 IP 和 Miao Panel 里的服务器自动对应；
- 系统提示词要求：做大改动前建议先建快照；到期不到 15 天或流量包用了 80% 以上时主动提醒；
- 界面：添加服务器时可以「从腾讯云选择」，自动填好 IP 和用户名；服务器页多一组「腾讯云」信息（实例、地域和配置、到期时间、流量包用量），快到期或流量快用完时标红。

| 操作 | 说明 | 回滚 |
|---|---|---|
| `cloud.firewall.open` | 轻量服务器改防火墙，CVM 改第一个安全组（提示同组的其他服务器也会放行）；同样的规则已存在就不修改 | 删除这条规则 |
| `cloud.firewall.close` | 删除匹配的放行规则；覆盖 22 或 3389 的端口一律拒绝（关掉会连不上服务器）；删到一半失败会把已删的加回去 | 把删掉的规则加回去 |
| `cloud.snapshot.create` | 轻量服务器建实例快照，CVM 给系统盘建快照；等快照可用才算完成 | 不需要（新增的备份） |
| `cloud.server.start` / `stop` / `reboot` | 开机、关机、重启；已经是目标状态就不操作，没在运行的机器不能关机或重启；等状态稳定才算完成 | 开机↔关机互为回滚；重启不需要回滚 |

**EdgeOne 统计**

- AI 能查（`tencent_eo_analytics`）：请求数、流量、峰值带宽、平均响应时间、缓存命中率的总量和时间曲线，以及按网址、IP、国家/省份、状态码、Referer、UA/浏览器/系统/设备、资源类型、域名的排行；可以按其中任一维度过滤；时间范围是最近 N 小时或指定起止时间（最长 31 天），粒度按范围自动选（2 小时内按分钟、2 天内按 5 分钟、7 天内按小时、更长按天）；
- 分析方法写在系统提示词里：先看总览和曲线找到异常时段，再在这个时段按各维度看排行，必要时和前一个时段对比；结论引用具体数字，给出可以执行的建议（清缓存、预热、改回源等）；封 IP、限流等安全策略暂时只给出控制台操作步骤；
- 界面：侧边栏新增「网站统计」页，选站点和时间范围（1 小时 / 24 小时 / 7 天 / 30 天；1 小时按分钟统计）。页面打开过一次就一直保留，切回来立即显示上次的数据；打开期间每分钟（1 小时视图每 30 秒）在后台自动更新，不会清空页面；后端把一份报告要用的 7 次 EdgeOne 查询同时发出，并缓存 20 秒，显示 5 个指标、请求数曲线（悬停看每个时间点，可以切换成表格）和 4 个排行（网址、国家、状态码、IP）；「让 AI 分析」把当前站点和时间范围带到 AI 助手里。

**EdgeOne 管理**

| 操作 | 说明 | 回滚 |
|---|---|---|
| `eo.cache.purge` | 按网址、目录、域名或整个站点清除缓存；网址必须属于这个站点 | 不需要 |
| `eo.cache.prefetch` | 预热指定网址 | 不需要 |
| `eo.domain.status` | 启用或停用加速域名；已经是目标状态就不操作 | 恢复原来的状态 |
| `eo.origin.set` | 修改回源地址、回源协议和端口，没填的保持原样 | 改回原来的设置 |

这些操作和 DNSPod、EdgeOne 的第一批操作一样：只涉及腾讯云时不连服务器，每一次查询和修改都写进执行日志，能回滚的可以在日志页或清单里一键回滚。测试用的模拟服务同样校验签名，并模拟实例开关机的中间状态、快照创建过程、监控数据和 EdgeOne 统计数据。

EdgeOne 安全策略、新建站点、自动化助手连接和 1Panel 建站见下面的 M2 第三步。还没有在真实的腾讯云账号上验证过。

### M2 第三步：EdgeOne 安全防护、新建站点、自动化助手连接、1Panel 建站（已完成）

**EdgeOne 安全防护**：AI 用 `tencent_eo_security` 查看站点级安全策略（自定义规则、速率限制、CC 防护、托管规则开关），再结合访问数据给出清单。规则写成 EdgeOne 的表达式（如 `${http.request.ip} in ['1.2.3.4']`）。EdgeOne 修改规则时整个列表一起替换，所以每次都先读出现有规则，只改 Miao Panel 自己的那一条，其余原样带回；回滚也是先读最新列表再改，不会覆盖期间别人做的修改。

| 操作 | 说明 | 回滚 |
|---|---|---|
| `eo.ip.block` / `eo.ip.unblock` | 维护一条名为「Miao Panel 封禁 IP」的基础访问管控规则（拦截），对整个站点生效；IP 和网段会校验，拒绝 /8 以上的大网段；最多 500 个 | 恢复修改前的 IP 列表 |
| `eo.ratelimit.set` | 按客户端 IP 限速：可以只统计某个域名、某个路径（如 /wp-login.php）；超过阈值后 JavaScript 挑战、拦截或只记录，持续一段时间；同一范围再次设置会修改原规则 | 新建的删除，修改的改回 |
| `eo.ratelimit.remove` | 按名称删除一条速率限制规则 | 按原设置加回 |
| `eo.cc.set` | 开关自适应频控（CC 防护），设置灵敏度和处置方式；HTTP DDoS 防护的其他子项原样保留 | 恢复原来的设置 |

**新建 EdgeOne 站点**（`eo.zone.create`）：CNAME 接入，绑定账号里还能绑定站点、服务区域合适的套餐（`tencent_eo` 会列出套餐；购买套餐涉及计费，仍由用户在控制台操作）。域名在同一账号的 DNSPod 里时，自动添加 EdgeOne 要求的 TXT 验证记录并触发归属验证；否则告诉用户要加的记录。站点已存在但没验证时，再执行一次只做验证。回滚：站点里没有加速域名时停用并删除站点，删掉自动添加的验证记录。

**腾讯云自动化助手（TAT）连接**：添加服务器时「从腾讯云选择」一台实例，登录方式默认是「自动化助手」：不需要密码，也不用开放 SSH 端口，命令以 root 运行（腾讯云控制台的执行记录里也看得到）。

- 新的 `tatx` 包实现了和 SSH 相同的「执行命令」接口，识别、只读检查、执行清单、回滚都不用区分连接方式；
- TAT 没有标准输入、输出最多 24KB，所以每条命令包在一个小脚本里：解出命令和输入、执行、把退出码和输出打包成 gzip+base64；超过一次能取回的大小时，留在服务器上的私有临时目录里分段读取，读完删除；
- 1Panel 接口没法走 SSH 隧道时，改为在服务器上用 curl 调用本机的 1Panel 端口；
- 需要子账号有 `QcloudTATFullAccess`。

**1Panel 自动建站**（`site.create`）：新建反向代理网站（指向 1Panel 里某个应用的对外端口，或直接填后端地址）或静态网站。先确认 OpenResty 已安装并在运行、这个域名还没有网站；建好后在服务器本机用 curl 访问一次，报告返回的状态码（502 说明后端没起来）。回滚：删除这个网站（不删应用和数据库）。AI 用 `panel_websites` 查看已有网站和可以代理的应用。宝塔和纯 Linux 还不能自动建站。

有了这几项，「把 blog.example.com 上线到我的服务器，走 EO，开 HTTPS」的完整清单是：新建 EdgeOne 站点（需要时）→ 1Panel 建站 → 添加加速域名 → DNS 解析到 EdgeOne → 申请免费证书，每一步都能单独回滚。

测试：模拟的腾讯云服务按 EdgeOne 的规则保存安全策略（列表整体替换、规则 ID、只读字段）、套餐和站点验证；模拟的自动化助手用本机 sh 执行命令，测试覆盖大输出分段读取、退出码、代理不在线；1Panel 建站用模拟的 1Panel 按其源码里的校验规则检查请求。都还没有在真实的腾讯云账号和 1Panel 上验证过。

还没做：宝塔的建站和 PHP / MySQL 接口、EdgeOne 地区封禁和 Bot 管理、在 1Panel 里申请证书（走 EdgeOne 时不需要）。

### M1 第二步：受限的 AI 自由命令（已完成）

按 7.5 实现，默认关闭，在「设置 → AI 自由命令」阅读风险说明后开启。AI 用 `free_command` 提交：要做什么（goal）、命令（script）、会写入/新建/删除的文件（files）、会重载或重启的服务（services），可选一个本机检查地址（check_url）。

**1. 静态检查**（`internal/freecmd`，用 shfmt 的 shell 解析器 `mvdan.cc/sh` 解析，不是关键字匹配）：

- 只允许列出的程序：查看类命令，`sed -i`（只允许 s/d/p/a/i/c 命令，拒绝会执行命令的 `e` 和会写文件的 `w`，不许带备份后缀）、`tee`、`cp`、`mv`、`rm`（单个文件）、`mkdir`、`touch`、`chmod`/`chown`（不能 -R，不能设成所有人可写）、`ln -s`、`systemctl reload/restart/start`、`service`、`nginx -t / -s reload`、`php-fpm -t`、`apachectl configtest/graceful`、`docker restart`；
- 明确拒绝并说明原因：磁盘和挂载、网络下载、安装软件（归为「需要额外确认」，这个版本还不支持）、账号和 sudo、防火墙、重启服务器、kill、解释器和嵌套执行（sh -c、python、awk、eval、xargs……）、定时任务、sysctl；
- 不能有变量、命令替换、算术展开、通配符、循环、函数、后台执行；写配置文件的 here-doc 结束符必须加引号，这样内容里的 `$host` 之类原样写入；
- 写入位置必须是写明的绝对路径，在 /etc、/usr/local/etc、/opt、/srv、/var/www、/www/wwwroot、/home、/root、/data 下，并且不能是 SSH、账号、sudo、PAM、开机和 systemd 服务定义、定时任务、网络、防火墙、软件源、logrotate、shell 启动文件、1Panel（/opt/1panel）和宝塔（/www/server）自己管理的文件、TAT、Miao Panel 自己的日志和备份；
- 关键服务（sshd、tat_agent、1Panel、宝塔、docker、containerd、网络、防火墙、systemd-* 等）不能重启；
- **命令实际写入的文件和重载的服务必须和声明完全一致**，多一个少一个都拒绝；
- 执行时会重新做一遍静态检查。

**2. 隔离试运行**（`scripts/actions/free_command.sh dryrun`）：在一个私有的挂载命名空间（`unshare -m`）里，把声明文件所在的目录各挂一层 overlay，命令对文件的改动只落在临时层；`systemctl`、`service`、`nginx -s`、`docker restart` 等换成只记录不执行的替身，`nginx -t` 这类检查照常执行，所以检查的是**改动后的配置**。结束后输出每个文件真实的修改前后对比、记录下来的服务操作、命令输出，以及临时层里出现的**没有声明的改动**（有就拒绝）；同时记下每个文件修改前的校验值。服务器缺少 unshare 或 overlay 时明确标出「不能隔离试运行」，这时清单里必须在它前面有 `cloud.snapshot.create`，而且执行时要一起执行。

**3. 独立审查**：另开一次模型调用，不带工具、不带对话历史，只给它目的、声明、命令和试运行结果，按「实际改动是否和目的一致、有没有降低安全性或植入后门、改动是否合理」判断，输出 JSON；材料里如果有要求放行之类的文字直接判不通过。审查不通过就不能执行。目前审查用的是同一个模型的新会话；以后可以在设置里另配一个不同厂商的审查模型（11.1 的建议）。

**4. 执行**（`free_command.sh apply`，经常规的上传—后台执行—轮询流程）：

- 先核对声明文件的校验值和试运行时一致，被改过就拒绝，要求重新生成；
- 备份每个声明文件（不存在的记为「原本没有」），生成由系统编写（不是 AI 编写）的 `restore.sh`；
- **5 分钟保险**：用 `systemd-run --on-active=300` 预约执行 `restore.sh guard`（没有 systemd 时用 setsid 后台延时）；Miao Panel 收到执行结果后，再单独连一次服务器写入取消标记。连不上服务器（比如命令把网络搞坏了）就不会取消，服务器 5 分钟后自己恢复；
- 命令限时 120 秒；失败、声明的服务没有在运行、或者 check_url 返回 5xx/连不上，立即恢复并重新加载服务；
- 回滚（清单或执行日志里）执行同一个 `restore.sh`；它也留在服务器的备份目录里，可以手动执行。

**界面**：清单里的自由命令显示「会发生什么」——要做什么（审查给出的大白话说明）、会修改/新建/删除哪些文件（可以展开看修改前后对比）、会重新加载哪些服务、不会做什么、通过了哪些检查、最坏情况；原始命令收在「查看原始命令（高级）」里。确认执行时会提示这段命令是 AI 现场编写的，按钮是「我了解，执行」。试运行、审查、执行的全过程都在执行日志里。

和 7.5 的差别：能隔离试运行时不强制先做快照（快照每次要几分钟，还有数量限制），只有不能试运行时才强制；「需要额外确认」的一类（安装软件、重启服务器）还没有做，现在直接拒绝；把成功的自由命令导出成模板还没有做。

测试：`internal/freecmd` 覆盖允许和拒绝的各种写法；`free_command.sh` 在有 root 和 overlay 的环境里通过 SSH 端到端测试（试运行不改文件、执行后确认并取消保险、失败自动恢复、试运行后文件被改过就拒绝、回滚还原）；应用层测试覆盖默认关闭、AI 不能伪造检查结果、审查不通过、关闭后不能执行、不能试运行时必须带快照。还没有在真实服务器上执行过。

### 证书管理：总览、申请、自动续签（已完成）

侧边栏新增「证书」页，AI 有对应的只读工具 `certificates`。

**总览**把几处的证书放在一起，按问题严重程度排序：

- EdgeOne 每个加速域名的边缘证书：免费证书（EdgeOne 自动续签）、自有证书，或者还没开 HTTPS；
- 腾讯云 SSL 证书服务里的服务器证书（需要 `QcloudSSLReadOnlyAccess`，没有权限时跳过并说明），托管的标为自动续期；
- 每台配置了 1Panel 接口的服务器上 1Panel 管理的证书：到期时间、是否自动续签、用在哪些网站、申请或续签失败的原因；
- 从这台电脑实际访问每个网站（EdgeOne 域名、1Panel 证书正在使用的网站）拿到的证书：签发者、到期时间，是否受信任、是否包含这个域名。

**合并显示**：同一张证书经常放在几个地方（例如上传到腾讯云 SSL 给 EdgeOne 用，同时装在 1Panel 里），所以页面按域名分组（`*.miao.club`、`miao.club`、`www.miao.club` 归到一组），组里每张证书只显示一次（域名相同、到期日相同就是同一张），列出所有存放位置，以及在用的地方：1Panel 网站、引用它的 EdgeOne 域名（按腾讯云证书编号对上）、实际访问时看到的就是它的域名。哪里都没发现在用的证书不报警，已过期的提示可以删除（放在「没发现在用的证书」里）；EdgeOne 用的是腾讯云里的副本、而只有 1Panel 会续签时，快到期会提醒续签后要重新上传。

**筛选和排序**：卡片上方一行筛选：「显示」全部 / 需要处理（带数量）/ 不会自动续签；搜索框可以搜域名、服务器、签发机构或状态文字（粘贴 `https://blog.miao.club:443/...` 这样的网址也行，`blog.miao.club` 能搜到覆盖它的 `*.miao.club`），搜服务器名时「实际访问到的证书」里也只留下它的网站；「排序」最紧急在前 / 最先到期 / 按域名（按主域名归在一起，`miao.club` 在它的子域名前面），「实际访问到的证书」的 网址 / 到期 / 状态 表头也可以点击排序，和排序选项是同一个设置。需要处理的始终排在最上面，卡片不会在输入时从一个分组跳到另一个分组；组里有被筛掉的证书时，卡片底部可以展开。只记住排序方式，筛选不保存，这样重启后不会因为忘了关筛选而漏看新问题；筛选把需要处理的问题藏起来时，会提示「还有 N 个需要处理的问题被筛选隐藏了」。没有到期日的证书（申请中、申请失败）排在同一级别的最后；已过期的显示「已过期」而不是「剩 -3 天」。

判断规则：已过期或不足 7 天为严重；会自动续签但不足 15 天（说明续签可能失败了）或者不会自动续签且不足 30 天为警告。最近一次的检查结果保存在本地数据库里：打开「证书」页（包括重新打开 Miao Panel 之后）立即显示上次的结果，并标明是什么时候检查的；超过 5 分钟就在后台重新检查，查完自动替换。后台检查不会因为切走页面而中断，几个页面或 AI 同时要数据时只查一次；执行清单或回滚后自动重新检查（正在进行的检查如果开始得比改动早，会再查一次）。每一行都有对应的「让 AI 处理 / 开启 HTTPS / 开启自动续签」按钮，把问题带到 AI 助手里。

**申请和自动续签**：续签要 7×24 进行，而 Miao Panel 是桌面程序，关着就续不了，所以交给一直在运行的一方：

| 操作 | 说明 | 回滚 |
|---|---|---|
| `eo.https.set`（已有） | 经过 EdgeOne 的网站用 EdgeOne 免费证书，EdgeOne 自动续签；EdgeOne 用 HTTP 回源时源站不需要证书 | 恢复原来的证书设置 |
| `cert.issue` | 1Panel 服务器：让 1Panel 向 Let's Encrypt 申请证书并开启自动续签（1Panel 在服务器上自动续签）。默认 HTTP 验证（域名要能访问到服务器，经过 EdgeOne 也可以）；泛域名必须用 DNS 验证，需要 1Panel 里有 DNS 账号；1Panel 还没有 Let's Encrypt 账号时用给出的邮箱注册。申请下来后给同名网站开启 HTTPS（默认 HTTP 和 HTTPS 都能访问，避免 EdgeOne HTTP 回源时循环跳转），并在服务器本机用 curl 验证。同一张证书已经有了就直接使用 | 恢复网站原来的 HTTPS 设置，删除新申请的证书 |
| `cert.renew` | 立即续签 1Panel 里的一张证书，等到新证书签发下来才算完成 | 不需要 |
| `cert.autorenew.set` | 开关 1Panel 证书的自动续签 | 改回原来的设置 |

- 1Panel 的证书接口会返回私钥。经 SSH 隧道调用时私钥只在内存里；经自动化助手调用时，服务器上先用 sed 把返回里的私钥字段抹掉，私钥不会出现在腾讯云的执行记录里；Miao Panel 自己也从不保存私钥；
- 宝塔和纯 Linux 服务器暂时不能自动申请证书（AI 会建议在面板里申请，或者接入 EdgeOne 用免费证书）。以后可以考虑由 Miao Panel 自己用 ACME + DNSPod 验证签发，但续签只能在 Miao Panel 运行时进行，要讲清楚这个限制。

测试：模拟的 1Panel 按其源码的行为异步签发证书（先 applying 再 ready）、校验请求字段；覆盖没有账号时要邮箱、签发并开启 HTTPS、重复申请直接复用、续签、开关自动续签、验证失败自动删除、回滚；证书总览用模拟的腾讯云（EdgeOne 免费证书、未开 HTTPS、将到期的 SSL 证书）测试，实际访问的检查在测试里替换掉。还没有在真实的 1Panel 和腾讯云账号上验证过。

### AI 实时回复（已完成）

以前要等 AI 整个回答完才一次性显示，中间只有一个转圈。现在边生成边显示：

- **文字逐字出现**，Markdown 边写边排版；
- **AI 在查什么实时列出来**，比如「分析网站访问数据 · ui.miao.club」，查完打勾，出错标红并显示原因；AI 在两次查询之间说的话（「我先查一下……」）按顺序显示在查询旁边；
- 会思考的模型（DeepSeek 等）在思考时显示「正在思考」和思考内容的最后两行；
- 回答时发送按钮变成 **■ 停止**，随时中断。已经说出来的部分和查过的数据会保存在对话记录里，下次接着问时模型也知道自己说到哪里被打断了。

实现：

- `ai.Session.Next` 可以传一个回调，流式接收回答。兼容 OpenAI 的接口用 `stream: true` 和 `stream_options.include_usage`（不认识 `stream_options` 的服务商自动去掉重试；忽略 `stream`、直接返回整段 JSON 的也能用），逐段拼出文字、思考内容（`reasoning_content`，DeepSeek 要回传）和工具调用（按 `index` 拼接参数）；Claude 用官方 Go SDK 的 `Messages.NewStreaming` 并用 `Message.Accumulate` 拼出完整消息，被拒答时改用备用模型并通知界面清掉已经显示的文字。工具要等整个回答收完才执行，所以没有开启工具参数的逐字流式（`eager_input_streaming`）；
- `ai.Agent.Ask` 把过程变成事件：`text`、`thinking`、`reset`、`round`（又一轮查询开始，之前的文字只是过程中的说明）、`prepare`（模型正在写某个工具调用）、`tool`、`tool_done`；出错或被停止时返回已经说出的文字，并在对话历史里补一句「这次回答被中断了」，保持一问一答的顺序（有的模型不接受连续两条提问）；
- 新接口 `POST /api/chat/stream` 返回逐行 JSON（NDJSON）：先是 `start`（带对话编号），然后是上面的事件，最后是 `done`（完整回答、清单、费用）。和其他接口一样要求登录 Cookie 和 `X-Miao` 请求头，所以用 fetch 读流而不是 WebSocket。`POST /api/chat/stop` 取消这个对话正在进行的回答，服务端照常保存已有内容并发出 `done`；万一停不下来，页面 5 秒后直接断开连接，服务端也会随之取消。

测试：模拟的流式接口覆盖思考内容、分段文字、分块的工具调用参数、拒绝 `stream_options` 后重试、整段 JSON 回退、流中的错误、Claude 的流式事件和拒答后换备用模型，以及回答被中断后对话历史仍然正确；API 测试通过真实的 HTTP 连接读取事件流，并在回答中途调用停止接口，检查已说的部分和「已停止回答」被保存。还在浏览器里用慢速的模拟模型检查了浅色、深色和手机宽度下的显示。

### 网站访问统计：服务器访问日志（已完成）

EdgeOne 的统计接口只有请求数、流量、带宽这类总量和排行，没有「独立 IP / UV」这种去重的数字；而且只有经过 EdgeOne 的网站才有。所以另外从服务器上的访问日志统计，「网站统计」页顶部可以在「访问日志」和「EdgeOne」之间切换。

- **只读脚本 `scripts/sitelogs.sh`**（POSIX sh + awk，mawk 也能跑）在服务器上读日志、统计，只把汇总结果传回来，日志本身不离开服务器。找日志的顺序：1Panel（OpenResty 容器挂载的 `/www`，默认 `/opt/1panel/www`，旧版本 `/opt/1panel/apps/openresty/openresty/www`，下面的 `sites/<网站>/log/access.log`，以及「切割网站日志」计划任务留在 `backup/log/website/<网站>/` 的压缩包）→ 宝塔（`/www/wwwlogs/<网站>.log`）→ `/var/log/nginx/`（含轮转文件）。单个日志超过 200MB 只读最后 200MB，用 `nice` 降低优先级；
- 按 Nginx 默认的 main / combined 格式解析；有 X-Forwarded-For 时用第一个 IP 作为访客 IP（经过 EdgeOne 或 CDN 时才是真实访客），日志里完全没有这个字段时页面会提醒 IP、UV 可能偏少；
- **口径**：PV = 爬虫以外、GET、状态 2xx 或 304、不是静态文件（css/js/图片/字体等）也不是 `/api/` 的请求；UV = PV 里不同的「IP + 浏览器标识」；IP = PV 里不同的访客 IP；爬虫 = 浏览器标识里有 bot/spider/crawler，或是 curl、python 等程序，或为空。每天、每个网站分别去重，整段时间和「全部网站」也各自去重（同一个人访问两个网站只算一个 UV）；
- 结果：每个网站和全部网站的每天数据、最近一天每小时的请求和 PV、整段时间合计，以及受访页面、来源（直接访问、站内跳转、外部域名）、访客 IP、状态码、爬虫、设备（手机/平板、电脑）的前 20 名；
- 和证书页一样，最近一次的结果按数据来源保存在本地数据库里，打开页面立即显示，超过 5 分钟就在后台重新统计；每次在服务器上运行都记在执行日志里（只读脚本）；
- AI 工具 `site_visits`（source、server_id、days、site、refresh），「我的网站这周访问量怎么样」「哪些页面最多人看」直接回答；
- 经过 EdgeOne 的网站，被 EdgeOne 缓存的请求（主要是静态文件）不会到服务器，所以请求数和流量以 EdgeOne 为准；网页一般不缓存，PV、UV、IP 以访问日志为准。

测试：Go 测试用真实的 `sitelogs.sh` 统计一个模拟的 1Panel 目录（两个网站、当前日志加一个切割压缩包、爬虫、静态文件、接口、4xx/5xx、40 天前的旧日志和坏行），逐项核对 PV、UV、IP、每天数据和排行；另外在 dash 和 bash 下对比输出一致。浏览器里用三个网站、30 天的模拟日志检查了浅色、深色和手机宽度下的显示。

### 网站统计第二版：EdgeOne 离线日志、IP 画像、AI 研判和封禁（已完成）

第一版的页面把所有数字堆在一起，用户看不出重点；服务器日志里的「访客 IP」其实大多是 EdgeOne 节点（43.140.x、43.157.x 等），UV、IP 都不准。第二版重做了数据和页面。

**页面**：「网站统计」分「访问分析」和「EdgeOne 实时」两个视图，结构相同：顶部一行筛选（数据来源或站点、网站、时间范围、更新时间、刷新、让 AI 分析），下面按「概览 / 访客 / 内容 / 安全」分组，每组是一张张卡片。概览先列需要处理的问题（敏感文件被成功下载、还没封禁的高风险 IP、5xx 多、爬虫占比高），再是关键数字（整天的 PV、请求数和再往前同样天数比；「今天」显示昨天全天作参考）、走势（今天按小时，否则按天；没过完的最后一段画成虚线）、热门页面 / 来源 / 地区前 5 名和各网站。时间范围、分组、数据来源记在本机，下次打开还是那样。

**数据来源**
- EdgeOne 的统计接口只有总量和排行，没有 UV、独立 IP 这类去重的数字，也看不到每个 IP 做了什么，所以「访问分析」按请求日志统计，两个来源：
  - **EdgeOne 离线日志**（有 EdgeOne 时默认用它）：`DownloadL7Logs` 逐页（每页 300 个）列出最近 30 天每小时的日志包，下载到 `<数据目录>/cache/eologs/<站点>/`（6 个并发，已下载的不再下载，列表里没有了的删除），最多读 3GB 日志，超出时只统计最近的并提示。日志是 JSON Lines，用到 RequestTime、ClientIP、RequestHost、RequestUrl、RequestUrlQueryString、RequestMethod、EdgeResponseStatusCode、EdgeResponseBytes、RequestReferer、RequestUA。每个请求都在（包括被缓存的），访客 IP 真实，延迟十几分钟到一小时；
  - **服务器访问日志**：上一节的 `scripts/sitelogs.sh`，实时，但被 EdgeOne 缓存的请求不在里面。统计后用 `DescribeIPRegion` 查访问最多的 IP 是不是 EdgeOne 节点（每次最多 100 个，结果保存 24 小时），大多是的话提醒这个网站的 UV、IP、地区都不准，应该看 EdgeOne 日志，或者让服务器日志记下真实 IP。
- 两个来源用同一套规则：Go 的 `visits.Counter` 统计 EdgeOne 日志，awk 脚本在服务器上统计，逻辑一一对应。脚本里每个正则都标了 `# rule <名字>`，测试检查它们和 Go 里的常量一字不差，并用同一份模拟日志对比两边的完整结果（JSON 逐项相同）。

**统计内容**
- 一次统计同时算出今天、7 天、30 天三个时间范围，以及 30 天每天、今天每小时的数据，切换时间范围不用重新统计；
- 排行：受访页面、访问目录（第一级）、来源、访客 IP、状态码、出错的地址、爬虫和程序（用暴露它的那个词命名，比如 `Mozilla/5.0 zgrab/0.x` 是 zgrab）、设备，以及敏感文件被成功下载（`/.env`、`.git`、`.sql`、`.bak` 等返回 200）；地区和运营商按独立 IP 统计；
- 死链：不是爬虫、GET、返回 404 或 410、带着来源（Referer）、又不是探测或攻击的请求，也就是有人点了一个链接却打不开。站内的链接记下链接所在的页面（`/posts/gone ← /about`），别的网站上的链接记下那个网站（`/old-guide ← www.zhihu.com`），页面上可以让 AI 建议改链接、做 301 跳转还是恢复页面；
- IP 画像：每个时间范围里被标出风险的和请求最多的前 80 个 IP：请求、PV、4xx、5xx、POST、不同地址数、一分钟最多几次、第一次和最后一次访问、常访问的地址和网站、浏览器标识；
- IP 所在地：内置 [ip2region](https://github.com/lionsoul2014/ip2region) 的 IPv4 和 IPv6 数据库（Apache-2.0 或 MIT；IPv4 用 gzip 压缩约 4.3MB，IPv6 原本 37MB，用 bzip2 压到 5.5MB，第一次遇到 IPv6 访客时才解压，约 1.4 秒），不联网；内网、本机、链路本地地址跳过。国内显示到省和城市（IPv6 也一样，例如 240e 开头的电信、2408 开头的联通），国外显示国家。

**风险评分**（`visits.Assess`）

| 迹象 | 分数 |
|---|---|
| 请求里带攻击代码（目录穿越、SQL 注入、XSS、JNDI 等） | 60 |
| 探测后台、备份和密钥文件（返回 4xx 的 `/.env`、`/wp-login.php`、phpmyadmin 等）3 次以上 / 1~2 次 | 50 / 15 |
| 登录失败（POST 登录地址被 401/403/429 拒绝，或 WordPress 登录）20 次以上 / 5 次以上 | 50 / 30 |
| 一分钟最多 300 / 120 / 60 次以上请求 | 50 / 30 / 15 |
| 30 次以上请求、一半以上是 404/403 | 30 |
| 访问了 200 个以上不同地址，PV 不到十分之一 | 20 |
| 自称搜索引擎，但反查域名不是它的（冒充爬虫） | 40 |
| 程序大量访问（200 次以上） | 10 |

60 分以上高风险，30 分以上中风险，10 分以上低风险。一次被拒绝的登录只算登录失败，不再同时算探测。EdgeOne 节点和内网地址不打分；自称 Googlebot、Bingbot、百度、Yandex、搜狗、Applebot 的 IP 按搜索引擎公布的方法验证（反查域名属于它们，再正查回到同一个 IP），验证通过的最多 5 分，DNS 查不了的不下结论。

**AI 研判**：「安全」里的「AI 研判」把风险 IP（或勾选的 IP，最多 25 个）的画像一行一个发给 AI，要求只回答 JSON：每个 IP 是 block（建议封禁）、watch（继续观察）还是 ignore（不用管），加一句理由。AI 建议封禁 EdgeOne 节点、已验证的爬虫或内网地址时改成 ignore。结果显示在表格和 IP 详情里，费用计入本月花费。

**封禁**：勾选 IP（「选中建议封禁的」按 AI 研判或高风险自动勾选）或在 IP 详情里点「封禁」，生成普通清单：每个 EdgeOne 站点一步 `eo.ip.block`（R2，可撤销），说明里写清每个 IP 为什么被封；EdgeOne 节点、内网地址、已验证的爬虫、只访问了没经过 EdgeOne 的网站的 IP 不会进清单。「已在 EdgeOne 封禁」列出 Miao Panel 封禁的 IP，点 × 生成解封清单（`eo.ip.unblock`）。

**自动封禁**（默认关闭，在「安全」里开启）：规则是封哪些 IP（高风险，或高风险和中风险）、要不要 AI 也同意（每个 IP 每天最多问一次，结论保存 24 小时）、封多久（1 小时 / 24 小时 / 7 天 / 30 天 / 一直）和永远不封的 IP 或网段。每次后台统计完（启动时和每 20 分钟），先把到期的解封（只解还在封禁列表里的，用户自己解封的就不管了），再从每个数据来源今天的统计里挑出符合规则的 IP，最多 20 个，生成清单并直接执行（审计记录里执行者是 auto），完成后记下每个 IP 的站点和到期时间。到期解封的 IP 记 7 天，只有它在解封之后又出现时才会再封，免得一解封就马上封回去。用户手动封禁或解封某个 IP 时，它就归用户管，不会再被自动解封。EdgeOne 节点、内网地址、已验证的搜索引擎爬虫照样不会被封。页面上显示规则、上次检查做了什么、现在自动封禁的 IP 和到期时间，以及最近 30 条记录；「立即检查一次」不用等下一次统计。

**预加载**：`App.KeepWarm` 在程序启动时和之后每 20 分钟在后台统计所有来源（执行日志里来源是「后台自动更新」）、重新检查过期的证书、读取每个 EdgeOne 站点最近 24 小时的实时统计。页面打开时立即显示最近一次结果（程序重启前的也行），旧了就在后台更新。

**EdgeOne 实时**：一份报告同时发出曲线（请求、流量、带宽、响应时间）、缓存命中、前一段同样长时间的总量，以及网址、国家/地区、状态码、IP、来源、设备、浏览器的排行（看整个站点时再加各域名）；国家代码和状态码显示成中文（US → 美国，404 → 找不到）。页面打开后依次预取其他时间范围，切换时立即显示；服务器保留每个站点和时间范围最近的一份报告（`GET /api/eo/analytics?cached=1`），第一次打开也是先显示再刷新。

**接口**：`GET /api/visits/sources`、`GET /api/visits?source=`（`wait=1` 等后台统计完、`refresh=1` 重新统计）、`GET /api/visits/blocked`、`POST /api/visits/block`、`POST /api/visits/unblock`、`POST /api/visits/judge`。

测试：Go 测试覆盖 awk 和 Go 两种统计完全一致、三个时间范围、地区和运营商、各项风险评分、敏感文件泄露、爬虫验证（模拟 DNS）、EdgeOne 节点提醒、EdgeOne 日志的下载缓存（第二次统计不再下载）和坏行、后台预加载、AI 研判（包括不许封禁的 IP 被改成 ignore）、封禁和解封的完整清单。浏览器里用一个模拟的腾讯云（30 天、4 个网站、6 万多条日志，包括扫描器、SQL 注入、猜密码、冒充 Googlebot、采集程序）和一台模拟服务器，走了一遍 AI 研判 → 勾选 → 封禁 → 执行 → 解封列表，检查了浅色、深色和手机宽度下的显示。

### 日报和提醒（已完成）

侧边栏「通知」页保存 Miao Panel 主动告诉用户的消息，最多 100 条，打开页面即标为已读，侧边栏显示未读数。

- **日报**（默认开启，9:00）：后台每 20 分钟一次的统计更新之后，如果已经过了设定时间而今天还没生成，就生成前一天的日报。每个数据来源一段：PV、UV、IP、请求、流量、错误率，PV 比前一天和比上周同一天的变化，各网站的 PV，近 7 天的热门页面和访客地区，死链、敏感文件被下载，从那一天起还有活动的高风险 IP 数；最后是证书（在用的有问题的逐条列出，30 天内到期但会自动续签的也提一句）。Miao Panel 没开着就等下次打开，也可以在页面上「现在生成一份日报」。
- **提醒**（每项可以关掉）：每次统计更新之后检查，有新情况就合成一条提醒：新的高风险 IP（没被封禁的，同一个 IP 一天只提醒一次；自动封禁在提醒之前运行，已经封掉的就不提了）、敏感文件被成功下载、证书快到期（不会自动续签）或已过期、申请失败、访问时证书出错（同一个问题一周只提醒一次）、自动封禁和到期解封的记录。
- **推送**：推送地址和加签密钥和其他密钥一样存在本机的密钥文件里，页面上只显示打了码的地址。按地址认出服务：企业微信群机器人（markdown，按 4096 字节截断）、钉钉群机器人（markdown，可选加签：时间戳和密钥做 HmacSHA256）、飞书群机器人（消息卡片里放 markdown，可选签名）、Server酱（表单 title / desp，推送到微信），其他地址按通用 Webhook 发 JSON（title、text、source）。机器人返回 200 但带错误码时当作失败，原因记在这条消息下面。有「发送测试」。

测试：模拟的服务器检查五种推送的请求格式、钉钉和飞书的签名、机器人返回错误码时报错；提醒和日报用模拟的腾讯云日志（扫描器、泄露的 .env、昨天的访问）检查提醒内容、不重复提醒、到时间才生成日报且一天只一次、日报里的数字、推送次数和已读。浏览器里配了一个本地的假机器人，检查了测试消息和日报的推送内容，以及浅色、深色和手机宽度下的页面。

### 终端（已完成）

侧边栏「终端」页，或者服务器页的「终端」按钮，打开服务器的交互式命令行。

- 后端：`sshx.Client.Shell` 在 SSH 连接上申请伪终端（`xterm-256color`）并启动登录 shell；`app` 为每个终端保存最近 256KB 输出，页面断开再连上（刷新页面、切换页面）时先重放这部分，再继续实时输出；10 分钟没有页面连着就自动关闭（合上笔记本、手机锁屏一会儿再回来还在）；
- 保活：终端的 SSH 连接每 30 秒问一次服务器还在不在（`keepalive@openssh.com`），免得路上的防火墙、NAT 把空闲连接悄悄断掉；连续 3 次没有回答就关闭连接，页面上显示「连接已断开」，而不是一直卡着；
- 接口：`POST /api/servers/{id}/terminal` 打开，`GET /api/terminals/{id}/output?from=位置` 以 server-sent events 持续输出（每段输出 base64 编码，事件 id 是这段之后的字节位置；从页面要求的位置开始，做不到时先发 `reset` 让页面清屏再重放；没有输出时每 15 秒发一行注释当心跳，免得 Nginx、EdgeOne 把空闲的连接断掉；shell 结束时发 `end`；响应头 `X-Accel-Buffering: no` 让 Nginx 不缓冲，输出立刻到浏览器），`POST …/input` 输入，`POST …/resize` 调整大小，`DELETE` 关闭，`GET /api/terminals` 列出（页面刷新后重新接上）。和其他接口一样要求登录 Cookie 和 `X-Miao` 请求头，所以用 fetch 读流、用 POST 发送输入，而不用 WebSocket（浏览器的 WebSocket 不能带自定义请求头）；
- 页面：xterm.js（MIT，放在 `internal/webui/static/xterm/`，打开终端时才加载），多个标签页；Ctrl+C 在有选中文字时复制，Ctrl+V 粘贴；输出流断了（代理超时、网络切换、电脑休眠醒来）会自动从断开的位置接着连，不清屏、不重复，重连期间敲的字照样发到服务器，40 秒收不到心跳也当作断了；会话真正结束后可以一键重新连接；
- **安全边界**：终端是用户自己在操作服务器，AI 不能使用；命令直接执行，不经过清单、备份和检查，页面上有提示；审计日志只记录打开和关闭（不记录输入内容，里面可能有密码）；
- 用腾讯云自动化助手连接的服务器没有 SSH，不能打开终端，会提示改用 SSH 或腾讯云控制台的 OrcaTerm。

测试：测试用的 SSH 服务器支持 pty-req、window-change 和 shell 请求；测试覆盖输入输出、调整大小、第二个页面接上时拿到之前的输出、从某个位置接着输出（不重复）、心跳、SSH 保活发现死连接（一个会突然不再转发的 TCP 代理）、shell 退出后结束、没有页面连着时自动关闭、手动关闭和审计记录，以及通过 HTTP 接口的完整流程。浏览器里检查了输入中文和彩色输出、多个标签页、切换页面和刷新页面后重新接上；通过一个会掐断输出流的代理检查了自动重连（原来的 shell、输出不重复、重连时输入不丢）和 28KB 的粘贴。

### 文件管理（已完成）

侧边栏「文件」页，或者服务器页的「文件」按钮，像桌面上的文件管理器一样管理服务器上的文件。

- **连接**：在服务器的 SSH 连接上打开 SFTP 子系统（`github.com/pkg/sftp`，BSD-2），每台服务器一个连接，5 分钟不用就关闭，连接断了自动重连一次。浏览、读写文件、上传、下载走 SFTP；复制、移动、删除、压缩、解压、改权限用服务器自己的命令（`cp -a`、`mv`、`rm -rf`、`tar`、`zip`/`unzip`、`7z`、`unrar`、`chmod`、`chown`），路径一律用单引号转义并放在 `--` 之后。服务器上没有 zip、unzip、7z、unrar 时提示安装命令；
- **编辑**：5MB 以内的 UTF-8 文本可以直接编辑（含 NUL 字节或不是 UTF-8 的当作二进制，只能下载；管道、设备等特殊文件不能打开）。保存时先写到同目录的临时文件，保留原来的权限和所有者，再改名覆盖，写一半失败不会损坏原文件；编辑符号链接时写到它指向的文件，链接本身不变。页面带上打开时的修改时间，文件在这之后被别人改过就先提醒，确认后才覆盖。Windows 换行（CRLF）的文件保存后还是 CRLF；
- **所有者**：新上传的文件、新建的文件和文件夹、压缩出来的文件，自动改成所在文件夹的所有者（比如 1Panel 网站目录的 1000），免得网站读写不了 root 的文件；覆盖和编辑已有文件保持原来的所有者；复制用 `cp -a` 保留原来的；只有 root 登录时才能改所有者，其他账号不变；
- **上传和下载**：上传用 multipart 流式写入（不整个读进内存），一次一个文件并显示进度；已有同名文件时先问覆盖还是跳过，接口默认不覆盖（`?overwrite=1` 才覆盖）。下载需要浏览器直接保存，普通链接带不了 `X-Miao` 请求头，所以页面先 `POST …/files/link` 拿一个一次性地址（2 分钟有效、只能用一次，而且仍然要登录 Cookie）；文件夹用 `tar -czf -` 打包成 .tar.gz 边打包边下载；
- **复制、移动**：目标位置已有同名的，接口返回 `code: "conflict"`，页面问覆盖（先删掉同名的）、都保留（新的一份改名为 `name (2).ext`）还是跳过；在同一个文件夹里粘贴复制的文件自动「都保留」；不能把文件夹移动到它自己里面；
- **保护**：根目录和系统目录（`/etc`、`/usr`、`/var`、`/boot`、`/proc` 等）、账号和登录相关的文件（`/etc/passwd`、`/etc/shadow`、`/etc/sudoers`、`/etc/ssh`、`/root/.ssh`、`/home/*/.ssh/authorized_keys` 等）、1Panel 和宝塔自己的目录不能删除、移动、改名，也不能被覆盖；这些目录不能递归改权限，不能改所有者；
- **安全边界**：文件管理和终端一样是用户自己在操作服务器，AI 不能使用，不经过清单和备份，页面上有提示；每次修改（新建、编辑、上传、复制、移动、改名、删除、压缩、解压、改权限）都记在审计日志里，写明服务器、路径和大小；
- **页面**：路径栏（点一下可以直接输入路径）、常用位置（主目录、根目录、1Panel 的 `/opt/1panel/www/sites`、宝塔的 `/www/wwwroot` 等，按服务器类型给出）、可排序的列表（文件夹在前）、筛选、多选（Ctrl / Shift）、右键菜单、快捷键（Ctrl+A / C / X / V、Delete、F2、F5、Enter、Backspace、方向键）、拖放上传、编辑器（Tab 按文件原来的缩进输入、自动换行开关、行列号），改动正在进行时关闭页面会提醒。每台服务器记住上次打开的文件夹。腾讯云自动化助手连接的服务器没有 SSH，不能使用。

测试：测试用的 SSH 服务器提供 SFTP 子系统；测试覆盖新建、编辑（含冲突和强制保存、符号链接）、新文件跟随文件夹的所有者、二进制文件、上传（含不覆盖同名文件）、列表、复制和移动（同名冲突、自动改名、不能移动到自己里面）、改名、tar.gz 和 zip 的压缩解压、改权限、下载文件和文件夹、保护目录和审计日志，以及通过 HTTP 接口的上传、冲突代码和一次性下载地址。浏览器里在 1Panel 目录结构上检查了编辑保存（所有者不变）、新建文件夹、复制粘贴、同名处理、剪切、重命名、上传覆盖、压缩、解压、改权限、下载、删除和系统目录的保护，以及浅色、深色和手机宽度下的页面。

### 服务器日志记录真实访客 IP（已完成）

网站经过 EdgeOne 时，服务器看到的连接来自 EdgeOne 的节点，访问日志、网站程序（WordPress 的登录限制、评论）和 1Panel 看到的都是节点的地址。

- **为什么不按 IP 段信任**：常见做法是 Nginx 的 realip 模块只信任 EdgeOne 回源节点的 IP 段。但 EdgeOne 的公开 IP 段查询接口（`api.edgeone.ai/ips`）已经在 2026-08-31 下线，替代它的「源站防护」接口据说免费版不能用。只写 `real_ip_header X-Forwarded-For` 而信任所有来源又不行：任何人直接访问服务器都能在请求头里冒充别人的 IP；
- **做法**：每个账号生成一次随机的请求头名字（`X-Miao-IP-` 加 24 个十六进制字符，存在本机密钥里）。清单两步：
  1. `nginx.realip`（系统脚本 `nginx_realip.sh`）：在 Nginx 读取的 http 级目录里加 `00-miaopanel-realip.conf`：`set_real_ip_from 0.0.0.0/0; set_real_ip_from ::/0; real_ip_header <名字>;`。带这个请求头的请求用里面的 IP 作为 `$remote_addr`，其他请求不变；因为名字只有 EdgeOne 知道，直接访问服务器的请求没法冒充。位置：1Panel v2 的 `/opt/1panel/www/conf.d`、v1 的 OpenResty 应用 `conf/conf.d`（在 OpenResty 容器里执行 `nginx -t`、`-s reload`），宝塔的 `/www/server/panel/vhost/nginx`，系统 Nginx 的 `/etc/nginx/conf.d`。先确认有 realip 模块、现有配置能通过 `nginx -t`、没有别的 `real_ip_header`；写入后再 `nginx -t`，并用 `nginx -T` 确认这个文件真的被读取了，然后平滑重载，任何一步失败都恢复原样。撤销删除这个文件（或恢复被替换的旧文件）并重载；
  2. `eo.clientip.header`（腾讯云接口）：对回源到这台服务器的每个 EdgeOne 站点，用 `ModifyL7AccSetting` 打开「回源携带客户端 IP 头部」（`ZoneConfig.ClientIPHeader`），名字就是上面那个；站点已经用别的名字打开时不改（可能有程序在用），说明原因。撤销恢复成修改前的设置；
- 先改服务器再改 EdgeOne：服务器不支持（没有 realip 模块、已有别的设置）就不会动 EdgeOne；两步都只是多一个请求头或多认一个请求头，任何一步单独存在都没有副作用；
- 请求头名字只由 Miao Panel 填进清单（AI 提出的清单里的这个参数也会被覆盖），不出现在给 AI 的内容里，清单页面也不显示；服务器识别脚本列 1Panel 网站时跳过这个配置文件；
- 「网站统计」按服务器日志统计时，如果访客 IP 大多是 EdgeOne 节点，提示里有「让服务器记录真实 IP」按钮，一键生成清单（找出回源地址是这台服务器的加速域名）；执行过之后提示改成「已经让服务器记录真实访客 IP（某时起），之前的日志还不准」。

测试：用本机单独启动的 Nginx 实际执行脚本：带正确请求头时 `$remote_addr` 变成访客 IP，伪造 `X-Forwarded-For`、`EO-Connecting-IP` 无效，重复执行不重复修改，撤销后恢复，已有别的 `real_ip_header` 时拒绝，名字不合规时拒绝；手动检查了 Nginx 没有读取这个目录时自动恢复。模拟的腾讯云检查清单内容（只包括回源到这台服务器的域名）、名字只生成一次且 AI 不能指定、EdgeOne 设置的打开和撤销、已有别的名字时不修改。浏览器里从统计页的提示生成清单并实际执行（服务器一步在本机的系统 Nginx 目录，EdgeOne 一步在模拟接口），再撤销，以及手机宽度下的清单。

### 封禁的安全检查：EdgeOne 节点、搜索引擎爬虫、直连服务器（已完成）

- **搜索引擎爬虫的核实**：先看 Google 和 Bing 公布的爬虫 IP 段（googlebot.json、bingbot.json，2026-09-26 取自 github.com/lord-alfred/ipranges 的同步，内置在程序里），在里面就是真的，不用查 DNS。不在里面再按搜索引擎说的方法查反向 DNS 和正向解析。在国内 googlebot.com 的解析常被污染，名字对得上但解析回来的 IP 对不上时，结果是「不确定」而不是「冒充」；IP 库显示这个地址属于搜索引擎自己的公司（Google、Microsoft、百度……）时也不算冒充。`googleusercontent.com` 不再当作 Googlebot 的域名：任何人租一台 Google 云服务器都有这样的反向解析名字；
- **封禁前再核对一次**：生成封禁清单（手动或自动封禁）时，不用统计报告里旧的结论，而是现查：向 EdgeOne（`DescribeIPRegion`）确认这些 IP 不是它的节点，查不了就一个都不封；自称搜索引擎爬虫的 IP，只有确认是冒充的才封，确认不了真假的也不封，免得影响收录；
- **直连服务器**：服务器日志里没有 `X-Forwarded-For` 的请求是直接连到服务器 IP 的（没经过 EdgeOne 或别的代理），统计脚本和本地计数器都按 IP 记下次数（`direct`）。安全页给这些 IP 标「直连服务器」并说明：在 EdgeOne 封禁只能挡住经过 EdgeOne 的访问，挡不住它们直接访问服务器；封禁清单的说明里也会写；
- 统计时没能向 EdgeOne 核对节点，页面会提示原因；安全页说明封禁只在 EdgeOne 上生效，不影响 SSH、1Panel 和 Miao Panel 管理服务器，封错了可以解封。

测试：爬虫核实覆盖公布 IP 段（IPv4、IPv6、Bingbot）、被污染的 DNS 回答、属于 Google 网络但不在爬虫段里的地址（不确定）、Google 云服务器冒充 Googlebot（冒充）；封禁时 EdgeOne 查询失败不封、EdgeOne 节点不封；统计脚本和本地计数器的直连次数一致。

### Web 版（已完成）

同一个程序，`miaopanel serve` 以服务器模式运行，用浏览器从任何地方登录使用，功能和桌面版一样。部署方法见 [DEPLOY.md](DEPLOY.md)。

- **运行**：`serve` 监听 `--listen`（默认 `127.0.0.1:18765`，前面放 HTTPS 反向代理），也可以 `--tls-cert/--tls-key` 自己提供 HTTPS；参数都能用环境变量（`MIAO_LISTEN`、`MIAO_DATA`、`MIAO_TLS_*`）。密钥固定存数据目录的 `secrets.json`（0600，服务器上没有桌面钥匙串）。后台统计、日报、自动封禁照常运行；收到 SIGTERM 平滑退出。程序内置时区数据，最小的容器里日报也按 `TZ` 生成；
- **账号**（`internal/auth`）：一个管理员账号。密码至少 10 个字符，bcrypt（cost 12）保存；用户名不存在时也做一次 bcrypt 比较，回答时间一样。可选两步验证（TOTP，RFC 6238：SHA-1、6 位、30 秒，前后各容许一个时间片，同一个验证码只能用一次）：先生成新密钥和二维码（`rsc.io/qr`，BSD），输入一次正确的验证码才开启；密钥和其他密钥一样存 secrets.json。关闭两步验证、修改密码都要输入密码；修改密码后其他登录全部退出；
- **第一个账号**：没有账号时，启动时生成一次性初始化码（80 位随机数），打印在日志里并写到数据目录的 `setup-code`（0600）。创建账号必须填对它，用过就删除，所以谁先打开页面都抢不走；服务器命令行的 `miaopanel reset-password` 也能创建第一个账号，或者重设密码（同时关闭两步验证、退出所有登录），这是被锁在外面时的出路；
- **会话**：登录后发一个 32 字节的随机 Cookie（HttpOnly、SameSite=Strict，HTTPS 下加 Secure），数据库里只存它的 SHA-256；3 天没用或者满 30 天失效，每小时清理一次。「设置 → 账号与安全」列出登录的设备（浏览器、IP、最近使用时间），可以让某个设备退出；
- **防猜密码**：同一个 IP 15 分钟内输错 5 次，这个 IP 锁 15 分钟；一个账号 15 分钟内被输错 20 次（来自任何地方），这个账号锁 15 分钟。访客 IP 取连接地址；只有来自本机或内网的连接（反向代理）才采信 `X-Real-IP` 或 `X-Forwarded-For` 最后一跳，所以从外网直接连过来的人不能靠伪造请求头绕过限制。登录、登录失败、修改密码都写进操作记录；
- **请求的保护**：所有接口（包括登录）都要求 `X-Miao: 1` 请求头，别的网站的页面加不了这个头（跨站请求要先预检，我们不回应），加上 SameSite=Strict，防跨站请求伪造；桌面版的 Host 检查（防 DNS 重绑定）在 Web 版不需要：登录 Cookie 只发给面板自己的域名。HTTPS 下加 HSTS；所有页面带 `Referrer-Policy: no-referrer`、`X-Frame-Options: DENY` 和内容安全策略；
- **页面**：没登录时整页是登录页（第一次是「创建管理员账号」），通过 HTTP 从别的机器访问时提示密码会明文传输；会话失效时任何接口返回 `code: login`，页面回到登录页。侧边栏显示当前账号和「退出」；有些说明按运行方式改变措辞（比如密钥保存在哪里）。手机上（宽度 720px 以下）侧边栏收进左上角的菜单按钮，点开从左边滑出，选了页面、点外面或按 Esc 就收起，未读通知数显示在按钮上；各页面的标题栏滚动时停在顶部，按钮多的一行可以左右滑。添加服务器时可以直接粘贴私钥内容（Web 版读不到你电脑上的密钥文件；桌面版两种都行），私钥存 secrets.json；
- **部署**：`Dockerfile`（多阶段构建，运行在 alpine 上，uid 10001，数据卷 `/data`，自带健康检查）、`docker-compose.yml`（只映射到 127.0.0.1）、`deploy/miaopanel.service`（systemd，专用用户和一组加固选项）、`deploy/nginx.conf` 和 `deploy/Caddyfile`（关闭缓冲让 AI 回答和终端实时显示、上传不限大小、长连接超时）。CI 另外构建 Linux ARM64，并实际构建镜像、启动容器、检查登录页和初始化码。

测试：账号服务的初始化码（错误、大小写和横线、用过作废）、弱密码、登录、会话只存哈希、按设备退出、3 天和 30 天过期、IP 锁定和账号锁定、命令行重设、两步验证（开启前要验证、需要验证码、验证码只能用一次、时间容差、关闭要密码）、修改密码让其他登录退出，以及 RFC 6238 的标准测试向量；接口层的未登录、X-Miao 头、HTTPS 反向代理下的 Secure Cookie 和 HSTS、外网伪造 X-Forwarded-For 不能绕过锁定、下载要登录、退出登录；桌面版没有账号且仍检查 Host；粘贴私钥用测试 SSH 服务器实际登录。浏览器里走了初始化、登录、开启两步验证（用真实算出的验证码）、退出、带验证码登录、输错被锁和 HTTP 提示，看了浅色、深色和手机宽度；用示例 Nginx 配置实际代理了一次登录（HTTP/2、Secure Cookie、HSTS、HTTP 跳转 HTTPS）；容器实际运行了一遍（初始化码在日志里、数据卷属于 uid 10001、`docker exec … reset-password`、正常退出）。

### 域名解析页和一键解析（已完成）

侧边栏「解析」页管理 DNSPod 解析，和 EdgeOne 联动。所有修改都走清单：先生成、再确认执行，写进执行日志，能撤销；AI 用的也是同样的操作。

| 操作 | 说明 | 回滚 |
|---|---|---|
| `dns.record.add` | 添加一条记录（A、AAAA、CNAME、MX、TXT、NS、CAA、SRV，可选线路、TTL、MX 优先级、备注）；已有一模一样的就跳过；同一线路上 CNAME 和 A/AAAA/CNAME 冲突时先说明，不去碰已有的记录 | 删除这条记录 |
| `dns.record.modify` | 按记录编号修改；暂停状态和权重保持不变，没填的线路、TTL、备注保持原样 | 改回原来的样子 |
| `dns.record.delete` | 按记录编号删除 | 把记录加回来（编号会变） |
| `dns.record.status` | 暂停或启用 | 恢复原来的状态 |

- DNSPod 自带的 NS 记录（`DefaultNS`）不能修改、暂停或删除；记录值按类型检查（CAA 要像 `0 issue "letsencrypt.org"`，SRV 要是「优先级 权重 端口 目标」），线路名只允许中文、字母、数字和少数符号；
- `ModifyRecord` 带上原来的 `Status` 和 `Weight`（不带的话 DNSPod 会把暂停的记录重新启用、清掉权重）；线路列表来自 `DescribeRecordLineList`（按域名的套餐），读不到时只提供「默认」；
- 页面上每条记录标出 EdgeOne 的情况：名字是 EdgeOne 加速域名、而且默认线路 CNAME 到它分配的地址，就是「经过 EdgeOne」（显示回源地址和证书）；其他线路直接解析到别处的，标「这条线路的访客不经过 EdgeOne」；EdgeOne 里有加速域名、但没有解析过去的，列在页面顶部，可以一键「解析到 EdgeOne」（`dns.record.set point_to=eo`）；
- **一键解析**（`ProposeDNS`，op=quick）：名字 + 指向（我的服务器用它的地址，内网地址拒绝；IP 用 A/AAAA；另一个域名用 CNAME）。不经过 EdgeOne 就是一步 `dns.record.set`；经过 EdgeOne 时按现状组合：域名还没接入就先 `eo.zone.create`（要有能绑定站点的套餐，没有就直接说明，不生成清单；包含中国大陆的区域提示要备案），加速域名不存在就 `eo.domain.add`、已存在但回源不同就 `eo.origin.set`，然后 `dns.record.set point_to=eo`，勾了 HTTPS 再加 `eo.https.set`。NS 方式接入 EdgeOne 的域名不在 DNSPod 管理解析，直接说明；
- 「不走 EdgeOne」（op=eo_off）把名字直接解析回 EdgeOne 里记的源站，加速域名保留，清单里说明缓存、防护和证书不再起作用、服务器 IP 会公开；普通 A/CNAME 记录可以「开启 EdgeOne」（用它现在的值当源站走一键解析）；
- 清单弹窗关掉时如果还在执行，页面继续跟进，执行完自动刷新记录；撤销后也会刷新。

测试：四个新操作在模拟的 DNSPod 上添加 MX、重复添加跳过、CNAME 冲突被拒、暂停后修改仍是暂停、撤销修改和暂停、删除后加回、撤销已经不存在的新增记录、自带 NS 记录拒绝、各类型记录值和线路名的检查；一键解析直接解析、经过 EdgeOne（三步真实执行）、回源不同改用 `eo.origin.set`、「不走 EdgeOne」、未解析的加速域名提示和「解析到 EdgeOne」、单条记录的增改停删和撤销、内网地址和错误值被拒、没接入的域名先接入、没有可绑定套餐时说明、NS 接入时说明。浏览器里在演示数据上走了一遍：提醒、解析到 EdgeOne、一键解析（经过和不经过 EdgeOne）、错误提示、添加 TXT、修改、启用、删除再撤销、「不走 EdgeOne」，看了浅色、深色和手机宽度。还没有在真实的 DNSPod 和 EdgeOne 账号上执行过。

### 存储：腾讯云 COS（已完成）

侧边栏「存储」页管理 COS 存储桶。COS 用自己的 XML 接口和签名（不是云 API 3.0）：`internal/tencent/cos.go` 按官方文档实现 HMAC-SHA1 签名，和官方 Go SDK（cos-go-sdk-v5 v0.7.75）逐字节比对过；请求发到 `{桶}.cos.{地域}.myqcloud.com`，列桶发到 `service.cos.myqcloud.com`。

两类操作分开处理：

- **文件**（上传、新建文件夹、改名、删除、下载和分享链接）直接执行，写进操作记录（`cos.upload`、`cos.mkdir`、`cos.rename`、`cos.delete`、`cos.link`）。上传是浏览器把文件原样 PUT 给 Miao Panel、再流式转给 COS，单个最大 5 GB；同名文件默认拒绝，确认后带 `overwrite=1` 再传。改名是复制再删除（文件夹最多 1000 个文件），删除文件夹一次最多 10000 个。下载和分享用带签名的临时链接（最长 7 天），浏览器直接从 COS 下载；
- **设置**走清单（页面上的表单就是确认，保存时 `run=true` 直接执行），写进执行日志，能撤销：

| 操作 | 说明 | 回滚 |
|---|---|---|
| `cos.bucket.create` | 新建存储桶，只能私有或公有读 | 桶还是空的就删除 |
| `cos.bucket.delete` | 删除存储桶，有文件时拒绝 | 不能撤销（R3） |
| `cos.acl.set` | 访问权限（私有、公有读、公有读写），单独授权给其他账号的保留 | 改回原来的权限 |
| `cos.referer.set` | 防盗链：白名单或黑名单、域名、是否允许空 Referer | 改回原来的设置 |
| `cos.cors.set` | 跨域规则（来源、方法、请求头、暴露头、缓存时间）；没有规则就删除 | 改回原来的规则 |
| `cos.lifecycle.set` | 生命周期：按前缀设置多少天后转低频、归档、深度归档、删除，删除历史版本，清理上传碎片；在别处建的规则按 ID 原样保留 | 恢复原来的整份 XML |
| `cos.versioning.set` | 开启或暂停版本控制 | 恢复原状态（从没开过的只能暂停） |
| `cos.encryption.set` | 服务端加密 SSE-COS | 恢复原状态 |
| `cos.website.set` | 静态网站：首页、出错页、强制 HTTPS | 恢复原设置 |
| `cos.policy.set` | 存储桶策略（JSON），空的就删除 | 恢复原策略 |

- 安全检查（`cosFindings`）：公有读写（严重）；公有读但没开防盗链；存储桶策略允许任何人（`qcs::cam::anyone`）读或写，能一键只去掉这些规则、保留其他的；公开可读的桶里有像备份或密钥的文件（`.sql`、`.env`、`.pem`、`backup/` 下的文件，名字像站点或数据库备份的压缩包）；跨域允许任何来源写入；没开版本控制；
- 用量来自腾讯云监控：运行时用 `DescribeBaseMetrics` 找 `QCE/COS` 下的标准存储量和外网下行流量指标，按桶查最近 48 小时；最近 24 小时超过 1 GB 且是前 24 小时 3 倍以上时提醒可能被盗链或刷流量。读不到时显示原因，不影响其他功能；
- AI 用 `tencent_cos` 工具看存储桶、设置、问题和文件列表，能生成修改设置的清单，不能改动文件。

测试：签名和官方 SDK 的结果比对；在模拟的 COS 上走通列桶、各项设置的读写、文件上传下载复制删除、分页列表；每个设置操作的执行和撤销、错误参数被拒；安全检查、一键修复、保留别处建的生命周期规则；文件的上传冲突、覆盖、非法路径、文件夹改名和删除、操作记录；用量和流量突增。浏览器里在演示数据上走了一遍：浏览文件夹、上传和覆盖、新建文件夹、改名、分享链接、删除，防盗链保存再撤销，用模板加生命周期，跨域的错误提示，安全检查和一键改成私有，新建和删除存储桶，看了浅色、深色和手机宽度。还没有在真实的 COS 账号上执行过，监控指标的名字是按运行时查到的为准。

### Web 版：邮箱、手机号登录（已完成）

Web 版除了「用户名 + 密码」，可以用邮箱或手机收到的验证码登录，每种方式在「设置 → 账号与安全」里单独开关。

- **发送通道**：邮件走用户自己配置的 SMTP（`internal/sender`：SSL、STARTTLS 或本机不加密，PLAIN 登录，标题和正文按 UTF-8 编码，常见邮箱的预设）；短信可以选腾讯云短信 `SendSms`（2021-01-11，用已保存的腾讯云密钥，签名、模板 ID、模板变量个数 1 或 2 可配，按号码返回的结果码换成中文说明）或阿里云短信 `SendSms`（dysmsapi 2017-05-25，RPC 风格的 HMAC-SHA1 签名，`internal/aliyun`，和阿里云官方 openapi-util 的签名代码逐字节比对过，包括它自带的测试向量；用单独的 RAM 用户 AccessKey，Secret 存在密钥存储里；模板 CODE、验证码变量名和可选的分钟数变量名可配；中国大陆号码去掉 +86，其他地区带国家代码；网络错误不带请求地址，免得把 AccessKey ID 和手机号显示出来）。SMTP 授权码和阿里云 AccessKey Secret 存在密钥存储里，页面上只显示「已保存」。修改或清除发送设置要输入账号密码：谁能改发信服务器，谁就能收到登录验证码。短信每天有总上限（默认 30 条，可改），防止被刷费用。
- **绑定**：输入账号密码后，往新邮箱或手机号发一个验证码，输对才绑定；邮箱和手机号在数据库里各自唯一（`users.email`、`users.phone`，部分唯一索引），手机号统一成 `+86…` 的形式。解绑也要密码，并关掉对应的验证码登录。
- **验证码**：6 位随机数，只在内存里保存它的 HMAC（进程启动时生成的随机密钥），10 分钟有效、用一次就作废、输错 5 次作废；发送限制：同一个邮箱或手机号同一用途 1 分钟一次、一天 10 次，同一个 IP 一小时 10 次。没有绑定的邮箱或手机号得到和绑定了的完全一样的回答，也不发送；发送失败时，页面只说「没发出去」，原因写进服务器运行日志和操作记录，不告诉陌生人这个地址是否绑定了。
- **只给管理员邮箱和手机号发送**（设置 `auth.send_scope`，默认开启）：验证码只发到账号绑定的邮箱和手机号，登录、测试邮件和短信都检查，其他地址拒发并写进操作记录；开启时不能更换绑定（第一次绑定可以，还没有绑定时测试也可以发到别处，便于第一次设置）；关闭要账号密码，开启不用。登录页对拒发的地址仍然回答「如果绑定了账号就已发出」，不暴露管理员的地址。
- **和其他保护的关系**：验证码代替的是密码，开了两步验证的账号用验证码登录后还要输入身份验证器的验证码（这时邮件验证码继续有效）；登录尝试和密码登录共用按 IP、按账号的次数限制；密码登录也接受绑定的邮箱或手机号作为账号。
- **不被锁在外面**：至少要留一种登录方式；只有验证码登录能用时才能关掉密码登录；发送通道没配置好时，已开启的验证码登录不显示，密码登录自动恢复；`miaopanel reset-password` 会重新开启密码登录。

测试：SMTP 在测试服务器上走通 SSL、STARTTLS、不加密三种方式，错误的授权码、端口和加密方式不匹配、地址里的换行注入、连不上；腾讯云短信的发送、签名和变量个数不对；阿里云短信在一个会校验签名的模拟服务上发送，签名、模板、变量名、Secret 不对时的中文说明，保存时不填 Secret 沿用已保存的，清除设置；绑定（错误密码、错误验证码）、邮箱和短信验证码登录、一次性、5 次作废、过期、没有绑定的地址、两步验证、关闭密码登录后的拒绝、发送通道坏了时密码自动恢复、命令行重设、同一个邮箱不能绑两个账号、发送失败不泄露是否绑定、各种发送限制；HTTP 上走通短信绑定和登录。浏览器里用真实的 `miaopanel serve` 和本地的测试邮件服务器走了一遍：设置发信邮箱（测试邮件、错误的账号密码被拒）、阿里云短信的设置（切换服务商、保存后 Secret 不回显）、绑定邮箱、开启邮箱登录、退出后用邮件验证码登录、关闭密码登录后登录页只剩验证码，看了浅色、深色和手机宽度。还没有用真实的邮箱服务商和腾讯云短信发送过。

### 网站管理：1Panel 的网站、HTTPS、反向代理、Nginx 配置（已完成）

侧边栏「网站」页列出每台 1Panel 服务器上的网站（同时向各台服务器查询，读不到的服务器单独显示原因；还没填 1Panel API 密钥的服务器提示去填），显示类型、运行状态、HTTPS 和证书剩余天数，以及 OpenResty 是否在运行。点进一个网站分成几块：概览、域名、HTTPS、反向代理、伪静态、配置文件、日志；网站经过 EdgeOne 时多一块「EdgeOne 缓存」。

读取直接调用 1Panel v2 的接口（`internal/onepanel/website.go`，按 1Panel 源码 `agent/app/api/v2/website*.go` 和对应的请求结构核对过）：`/websites/search`、`/websites/{id}`、`/websites/domains/{id}`、`/websites/{id}/https`、`/websites/ssl/search`、`/websites/proxies`、`/websites/rewrite`、`/websites/{id}/config/openresty`。一个网站的这几项同时查询；页面在用时，每台服务器保留一条 SSH 连接（空闲 5 分钟关闭，断了自动重连一次），切换网站不用重新登录。日志在服务器上用 `tail` 读网站日志文件的最后几行（非 root 用户走 `sudo -n`）。

所有修改都生成清单，确认后执行，写进执行日志，能撤销：

| 操作 | 说明 | 回滚 |
|---|---|---|
| `site.status` | 启动或停止网站（停止后访客看到 1Panel 的「网站已停止」页，文件和配置保留） | 恢复原来的状态 |
| `site.domain.add` | 添加域名和端口；网站开着 HTTPS 而证书不包含新域名时提醒 | 删除这个域名 |
| `site.domain.remove` | 删除域名（不能删主域名和最后一个域名），可以只删某个端口 | 按原端口加回来 |
| `site.https.set` | 用 1Panel 里的证书开关 HTTPS、换证书、设置访问方式（都能访问 / 跳转 / 只能 HTTPS）、HSTS、HTTP/3；不指定证书时选包含网站域名最多、最晚到期的一张；只改给了的项 | 恢复原来的 HTTPS 设置 |
| `site.proxy.set` | 新建或修改反向代理规则（路径、后端地址、发给后端的 Host）；同一路径已有别的规则时拒绝；停用的规则要先启用 | 新建的删除，修改的把规则文件原样写回 |
| `site.proxy.remove` | 删除规则 | 按原设置新建，再把规则文件原样写回，原来停用的仍停用 |
| `site.proxy.status` | 启用或停用规则 | 恢复原状态 |
| `site.rewrite.set` | 伪静态规则，可以从 1Panel 自带的模板（wordpress、thinkphp、laravel5 等）开始改；留空表示清空 | 写回原来的规则和模板名 |
| `site.conf.set` | 整个网站的 Nginx 配置文件 | 写回修改前的内容 |
| `site.delete` | 删除网站：先用 1Panel 备份网站目录和配置，等备份成功才删，应用和数据库保留 | 不能撤销（R3），备份文件在 1Panel 的备份目录 |

申请证书沿用 `cert.issue`（让 1Panel 向 Let's Encrypt 申请并开启 HTTPS），页面上可以勾选证书包含网站的哪些域名。

- **配置改动先看清楚再执行**：`site.conf.set` 和 `site.rewrite.set` 生成清单时，Miao Panel 读出现在的文件，算出改动（`internal/textdiff`，统一 diff 格式，三行上下文），清单里直接显示增删了哪几行；这份 diff 只由 Miao Panel 计算，AI 传来的会被丢掉。清单记下文件内容的哈希（`base_hash`），执行时文件已经被别处改过（比如在 1Panel 里）就拒绝，不覆盖别人的改动；页面打开之后文件变了，生成清单时就会提示重新打开；
- **1Panel 负责检查**：写配置、伪静态和反向代理规则时，1Panel 先用 `nginx -t` 检查，不通过就恢复原文件并返回错误，这一步显示为「没有执行」；
- **撤销核对网站**：撤销前确认这个编号的网站还是原来的域名（网站删掉重建后编号会被复用）；
- **AI**：`panel_websites` 列出网站时带上 HTTPS 和证书到期；新增 `panel_website` 读出一个网站的全部设置、可用的证书、完整的 Nginx 配置和最近的日志。系统说明里要求优先用具体的操作，只有它们做不到时才改整个配置文件，并且在原文基础上只改需要的几行；1Panel 网站的配置不用自由命令改。

**EdgeOne 缓存清除和预热**：`eo.cache.purge`、`eo.cache.prefetch` 以前只有 AI 能用，现在网站页（网站的域名经过 EdgeOne 时，按腾讯云的加速域名列表判断）和「网站统计 → EdgeOne 实时」都有表单：清除可以选指定网址、目录、整个域名或整个站点，方式选标记过期或直接删除；预热填网址。可以只写路径，自动补成 `https://域名/路径`，目录自动补结尾的 `/`，一次最多 1000 个。同样先生成清单，确认后提交；这两个操作本身不需要回滚。

宝塔和纯 Linux 服务器的网站暂时不能这样管理。

测试：一个按 1Panel 行为写的模拟服务（`internal/onepanel/onepaneltest`：校验必填字段和取值范围，配置里有不认识的指令时像 `nginx -t` 一样拒绝并保留原文件，反向代理规则按文件内容解析）上，每个操作的执行、重复执行不改动、撤销、错误参数（规则名里的点、路径里的分号、后端地址里夹带的指令、非 http 的地址、控制字符）；通过 SSH 端口转发访问模拟 1Panel 的完整流程：网站列表（包括没配置 API 的服务器）、详情、HTTPS 清单执行和撤销、配置 diff 和过期的 base_hash、AI 读网站和提交配置修改（伪造的 diff 被丢掉）、日志；EdgeOne 缓存清单的地址补全、执行和错误输入。浏览器里在演示数据上走了一遍：列表、各个分块、HTTPS 改成跳转（经过 EdgeOne 时的循环提醒）、添加反向代理、改配置文件看 diff、套用伪静态模板、日志、EdgeOne 缓存清单、新建网站，看了浅色、深色和手机宽度。还没有在真实的 1Panel 上执行过这些操作。

### 界面重构第一步：总览、分组侧边栏、待处理、AI 侧栏（已完成）

按通过的设计稿（名字 Miao Panel、用户提供的猫耳 M 图标、苹果系统风格、颜色从简）改了界面：

- **颜色**：只有一个主色蓝（主按钮 `#0071E3`、链接、选中项）；绿、橙、红只用作小圆点和短字，标签、胶囊、提示条都改成灰底，提示条靠图标的颜色区分轻重；图标是灰色线条，选中时变蓝；去掉登录页的渐变背景。深色模式背景纯黑、卡片 `#1C1C1E`。颜色都是 `style.css` 开头的变量；
- **图标**：侧边栏、登录页、欢迎页和浏览器标签页用猫耳 M 标志（`MiaoLogo` 组件），浅色白底深色标志，深色模式深底白色标志；
- **侧边栏分组**：总览、AI 助手、待处理（数字是等你确认的清单加未读提醒）；服务器（每台前面的圆点来自最近一次识别：正常、注意、严重、还没识别，下面一行是面板类型和最要紧的一句话），终端和文件放在服务器组里；网站（网站管理、访问统计、安全、证书，安全和证书后面是要处理的数量）；腾讯云（EdgeOne、解析、存储）；底部是记录（原来的日志）和设置。「安全」直接打开访问统计的安全分块，「EdgeOne」打开 EdgeOne 实时；
- **总览**（新首页，`GET /api/overview`）：只用 Miao Panel 已经有的数据拼出来——各服务器最近一次识别的 CPU（负载除以核数）、内存、磁盘和发现的问题，保存着的访问统计（今天的 PV、每小时走势、和昨天同一时段比），保存着的证书总览，最近 24 小时的高风险 IP 和 EdgeOne 里的封禁列表（查不到就不说是否已封禁），等你确认的清单，未读提醒，最近 5 次变更。需要联网的部分最多等 4 秒，读不到的卡片显示「没有数据」。「需要你处理」按严重、清单、注意排序，每条都能一键跳到对应的页面或让 AI 看看；
- **待处理**：原来的「建议」和「通知」合在一起，分「等你确认」（还没执行的清单）、「提醒和日报」、「全部清单」；打开旧的建议、通知入口会跳到对应的分块；
- **AI 侧栏**：在任何页面按 Ctrl+J / ⌘J 或右下角的「问 AI」打开，和 AI 助手页是同一个对话；从侧栏发出的问题会带上当前页面（例如「网站 blog.example.com（服务器 blog）· HTTPS」），模型收到「我现在在 Miao Panel 的某某页面」，保存的消息还是原话；页面名称只保留一行、最多 120 个字。Esc 关闭，「打开整页」切到 AI 助手页。窗口够宽（1100 像素以上）时页面向左让出侧栏的位置，不被挡住；总览的卡片按页面自己的宽度（容器查询）排成四列、两列或一列。终端和文件铺满整个窗口，右下角的按钮会挡住内容，所以这两页的「问 AI」放在标题栏右边；
- **总览的数字和各页一致**：证书卡片按证书页的算法计数——「在用的证书」和「需要处理」（在用或没有在用证书的域名上有问题的证书，加上访问时拿到坏证书、又不是这些证书造成的网站）；「最近的变更」显示每一步的说明（例如「给网站 shop.example.com 添加域名 www.shop.example.com（端口 80）」），不显示参数。

- **搜索和跳转**：Ctrl+K / ⌘K 或侧边栏的「搜索」打开，搜服务器、网站（各台 1Panel 的网站列表，一分钟内打开过就不重新读）和页面；输入「终端 blog」「文件 blog」直接打开那台服务器的终端或文件，「新建网站」打开新建网站；都搜不到的话，最后一项是把这句话交给 AI。在终端里 Ctrl+K 留给 shell。

- **服务器工作台**：服务器页分成「概览 · 网站 · 应用 · 执行记录 · 连接设置」五个分页签，名字下面一行是系统、面板、配置、位置、IP 和到期（快到期的标出来）。
  - 概览：CPU（负载除以核数）、内存、磁盘和流量包（腾讯云轻量）四个圆环，超过 80% 变橙、90% 变红，内存没有 swap 又用了七成以上也标橙；体检结果每一条旁边是对应的按钮（「加 swap」「优化内存」「清理磁盘」「加固 SSH」「排查」），点了把问题交给 AI，由 AI 先查看再给清单；这台服务器的网站（1Panel 的实时读，其他面板显示识别时看到的）、占内存最多的程序和最近五次修改；
  - 网站：就是网站管理页，只列这台服务器（`GET /api/websites?server=`），没有服务器筛选；
  - 应用：识别时占内存最多的程序（按程序名合并进程，「php-fpm: pool www」算 php-fpm，「node server.js」按脚本名）、Docker 容器（镜像、状态、内存、CPU）、系统服务（启动失败的可以让 AI 排查）、面板应用和运行环境；
  - 执行记录：记录页只看这台服务器（`GET /api/exec?server=`）；
  - 连接设置：地址、登录方式、指纹、测试连接和删除服务器，腾讯云实例（链接到云服务器页），系统和存储，1Panel 接口，原始识别结果。
  - 终端和文件还是单独的页面（会话和上传要在切走以后继续），从右上角的按钮打开，所以没有放进分页签。
- **腾讯云 › 云服务器**：账号里所有地域的轻量应用服务器和 CVM 一张表：状态、配置、公网 IP、到期（没开自动续费、15 天内到期标出来）、流量包用量、在 Miao Panel 里对应哪台服务器（按自动化助手的实例 ID 或公网 IP 对上），没加的点「添加」打开添加服务器并填好实例信息。点一台看详情（`GET /api/tencent/servers/{region}/{instance}`，各部分同时读，读不到的部分单独说原因）：最近 24 小时的 CPU、内存、公网出入带宽曲线（当前、平均、最高），防火墙（CVM 是第一个安全组，并提醒同组服务器共用规则），系统盘快照。开机、关机、重启、创建快照、放行端口（可以只允许一个 IP）、关闭端口都生成一步的清单（`POST /api/tencent/servers/plan`，能力就是 AI 用的 `cloud.*`），22 和 3389 在生成清单时就拒绝关闭。手机上每台实例是一张小卡片。

界面重构到这里全部完成。

### 监控和告警（已完成）

以前服务器的 CPU、内存、磁盘只是上次「识别」时的快照，网站打不开要用户自己发现。现在 Miao Panel 自己一直在看：

- **网站**：每分钟打开一次（`GET`，15 秒超时，最多跟 5 次跳转，证书要通过校验：证书错了访客也打不开）。要看的地址每 30 分钟重新整理一次（识别服务器、保存 1Panel 接口后马上整理；一个都没有时 5 分钟后再试）：1Panel 上运行中的网站（开了 HTTPS 的用 https），其他面板或纯 Linux 服务器识别时在 Nginx 配置里看到的域名，加上手动添加的地址，减去忽略的地址。5xx、连不上、超时、证书问题算打不开，原因写成大白话（「域名解析失败」「连接被拒绝」「证书已过期」「服务器返回 502」）。
- **服务器**：每两分钟一次，通过一直保持的 SSH 连接运行一个只读的小脚本（`/proc/loadavg`、`/proc/stat`、`free`、`df /`、`/proc/net/dev`），CPU 用两次采样的计数差算，网络算每秒字节数。连接断了下次重连，不会每两分钟登录一次把登录日志刷满。用腾讯云自动化助手连接的服务器不采样（太慢），它们看云服务器页的云监控。
- **保存**：`uptime_checks`、`server_metrics`、`incidents` 三张表，保留 7 天，每小时清理一次。
- **故障和提醒**：网站连续两次打不开、服务器连续三次连不上，开一条故障记录并提醒；恢复时结束记录并说「中断了多久」。磁盘超过提醒线（默认 90%）、内存连续 6 分钟超过 95%、CPU 连续 10 分钟超过 95% 也会提醒一次，回落后记录结束（不再提醒）。提醒走原来的通知：进「待处理」，有推送地址就推送；监控设置里填了邮箱、并且设置过发信邮箱（SMTP）的话同时发邮件。短信需要审核过的模板，不能发任意内容，所以不支持。
- **页面**：侧边栏「监控」（有网站或服务器挂了会显示红色数字）：网站表（现在的状态和原因、响应时间、24 小时可用率、最近一小时每次检查的小柱子，打不开的可以「让 AI 排查」，点「历史」看 24 小时或 7 天的响应时间和可用性条）、服务器（最新的 CPU、内存、磁盘和网络，点进服务器页）、最近 7 天的故障。总览的「需要你处理」会列出正在发生的故障，服务器卡片用最近 10 分钟的采样。服务器工作台的圆环在有新采样时用实时数据，并多一张「最近 24 小时」的 CPU、内存、网络曲线。
- **AI**：新工具 `monitor_status`，用户问「网站现在正常吗」「昨晚是不是宕机了」时直接查监控记录。


### 网站备份和恢复（已完成）

备份用 1Panel 自己的：网站目录和 Nginx 配置打成一个包，放在 1Panel 的备份目录或在 1Panel 里添加的备份账号（COS、OSS、S3 等）。Miao Panel 不自己打包，这样在 1Panel 里也看得到、能恢复，Miao Panel 不在时也能用。

- **立即备份**（`site.backup`，R1）：可以写备注（「改版前」）；等 1Panel 写完文件才算完成。不改动网站，所以没有撤销。
- **定时备份**（`site.backup.schedule`，R1，可撤销）：在 1Panel 里建一个名为「Miao Panel 备份 <域名>」的计划任务（类型 website，每天某个时间，保留几份，放在哪个备份账号），所以由 1Panel 在服务器上跑，Miao Panel 关着也会备份，超过份数的旧备份由 1Panel 删掉。再次设置是修改这个任务；撤销时新建的删除、修改的恢复原来的时间、份数和位置。`site.backup.unschedule` 取消（已有的备份保留，撤销时按原设置重建）。1Panel 里用户自己建的、也覆盖这个网站的备份任务会列出来，不去动它。
- **恢复**（`site.restore`，R3，不能撤销）：先把网站现在的样子备份一份（失败就不恢复），再让 1Panel 取出选中的备份（远程账号的会先下载到服务器）并恢复，等 1Panel 的任务结束。1Panel 恢复时自己也会先另存一份，失败自动退回；成功后告诉用户恢复前那份备份的文件名，想退回去就用它再恢复一次。网站备份不含数据库，页面上写明。
- **页面**：网站详情多一个「备份」：立即备份、定时备份（时间、保留 3/7/14/30 份、放在服务器本机或备份账号；选本机时提醒「服务器坏了会一起丢」）、备份列表（时间、备注、位置、文件名，下载和恢复）。下载时先让 1Panel 把文件准备在服务器上，再走文件管理的下载。
- **AI**：新工具 `panel_backups` 看一个网站的备份和定时备份；改动前建议先 `site.backup`，只在用户明确要求时才 `site.restore`。
- 纯 Linux 和宝塔服务器上的备份（tar 加 mysqldump 的定时任务）还没有做。

### 阿里云：服务器（已完成）

「设置 → 阿里云」保存一个 RAM 用户的 AccessKey（和腾讯云的一样放在密钥存储里，页面上列出要勾的策略：`AliyunECSFullAccess`、`AliyunSWASFullAccess`、`AliyunCloudMonitorReadOnlyAccess`、`AliyunDNSFullAccess`、`AliyunCDNFullAccess`），「测试」会分别说服务器、云解析、CDN 能不能用，缺哪个权限。

- **客户端**（`internal/aliyun`）：RPC 签名（HMAC-SHA1），ECS、轻量应用服务器（SWAS）、云监控、云解析、CDN 的接口；`aliyuntest` 是一个严格的假阿里云（检查签名、nonce、时间、版本、必填参数、地域和接入点是否对得上），测试全部跑在它上面。
- **云服务器页**：侧边栏的「腾讯云」改叫「云服务」。云服务器页同时读两家（`/api/tencent/servers`、`/api/aliyun/servers`），一家读不到只在那家报错；两家都配了时每台前面标出是哪家。阿里云的详情（`GET /api/aliyun/servers/{region}/{instance}`）和腾讯云同一个样子：24 小时 CPU、内存、公网出入带宽（带宽换算成 Mbps），防火墙（ECS 是第一个安全组），系统盘快照。
- **清单**：`aliyun.server.start / stop / reboot`、`aliyun.snapshot.create`、`aliyun.firewall.open / close`，和 `cloud.*` 一样的规则（22、3389 不允许关，数据库端口对所有人放行是 R3）；阿里云一条规则只能是一个端口或一段范围。按量付费的 ECS 关机用「继续计费」模式，保证公网 IP 不变、一定能再开机，清单里写明。没有对应 Miao Panel 服务器的清单，记录里记在「阿里云」名下。
- **添加服务器**：从阿里云实例「添加」时带上名称和公网 IP，用 SSH 连接（阿里云没有腾讯云的自动化助手）。
- **AI**：`aliyun_servers`，不带参数列出全部，带实例看详情。

### 阿里云：云解析和 CDN（已完成）

- **解析页**：域名列表同时读 DNSPod 和阿里云云解析（`DNSDomains` 各自出错只报那一家；同一个域名两边都有时列 DNSPod 的）。记录、线路和清单都带 `provider`；阿里云的记录编号可能超过 JavaScript 能精确表示的整数，所以单独用字符串 `rid` 传。线路在页面上用中文名（默认、电信……），执行时换成阿里云的代码（default、telecom……）。EdgeOne 相关的按钮和选项对阿里云的域名不显示。
- **清单**：`aliyun.dns.record.add / modify / delete / status` 和 `aliyun.dns.record.set`（一键解析：替换这个名字默认线路的 A、AAAA、CNAME，失败时把删掉的加回去），都能撤销。
- **CDN**：`aliyun.cdn.purge`（网址或目录）和 `aliyun.cdn.prefetch`，执行前核对每个网址都是这个账号的加速域名，提交后等任务完成或报出失败的网址。
- **AI**：`aliyun_dns`、`aliyun_cdn`。

### 宝塔的网站管理（已完成）

- **客户端**（`internal/btpanel`）：宝塔 11.x 和 aaPanel 的接口（`request_token` 签名），网站、域名、HTTPS、Let's Encrypt、反向代理、配置文件、伪静态、日志、备份和数据库；`btpaneltest` 是按宝塔源码行为写的假面板（接口关闭或参数不对时像真的一样回 404 页面，改配置会跑 nginx -t 并还原）。
- **连接**：和 1Panel 一样从服务器本机访问：SSH 隧道到 127.0.0.1:面板端口，或用自动化助手在服务器上运行 curl（1Panel 的 curl 传输改成可以给别的面板用，并在服务器上把回答里的私钥抹掉）。「连接设置」里填端口（识别时从 `port.pl` 读到）和接口密钥，测试时自动认出面板用的是 HTTP 还是 HTTPS。
- **网站页**：宝塔服务器和 1Panel 服务器一起列出，详情用同样的页面（`panel` 字段说是哪个面板）：域名、HTTPS、反向代理、伪静态（套用宝塔自己的模板）、配置文件、日志、备份。宝塔做不到或者接口没有的（HSTS、HTTP/3、选已有证书、新建和删除网站、定时备份、恢复网站备份）不显示。
- **清单**：用同一批操作名（`site.status`、`site.domain.*`、`site.https.set`、`cert.issue`、`site.proxy.*`、`site.conf.set`、`site.rewrite.set`、`site.backup`），宝塔服务器上走 `bt_*` 的实现。改配置文件和伪静态时宝塔自己先 nginx -t，Miao Panel 再整体测一次，不通过就写回原来的内容；申请证书只支持 HTTP 验证，原来就开着 HTTPS 的网站申请后不能一键撤销（接口读不到原证书的私钥）。清单和记录里的文字说宝塔和 Nginx，不说 1Panel。
- **AI**：`panel_websites`、`panel_website` 对宝塔服务器同样能用，提示词说明了宝塔能做和不能做的。

### 应用标签：重启和数据库（已完成）

- 服务器的「应用」标签不再只读：每个 Docker 容器旁边有「重启」，系统服务可以选一个重启（`container.restart`、`service.restart`，SSH、防火墙、面板这类关键服务仍然不允许），都先生成清单（`POST /api/servers/{id}/apps/plan`）。
- 1Panel 服务器多一个「数据库」：列出每个 MySQL/MariaDB 应用里的数据库（用户、谁能连接、备注），可以新建（数据库和同名用户，utf8mb4，密码随机生成、不经过 Miao Panel 也不记下来，在 1Panel 里查看；撤销时先备份再删除）、备份（`backup.create` 加 `database`）和删除（`mysql.db.delete`，R3，先备份再删，不能撤销）。
- AI 也能用 `mysql.db.create`、`mysql.db.delete`；提示词告诉它看不到也不要问数据库密码。

### 检查更新、一键更新和诊断包（已完成）

- **检查**（`internal/update`）：问 GitHub 最新的发布（`releases/latest`，只是一个 GET，不带任何数据），发布版（`vX.Y.Z`）启动一分钟后查一次，之后每天一次；「设置 → 版本和诊断」可以关掉（设置项 `update_check`），也能手动「检查更新」。有新版本时总览的「需要你处理」多一条，设置里显示发布说明的前几行。自己编译的开发版不检查。
- **一键更新**：下载这个系统的程序（`MiaoPanel-<系统>-<架构>`），和发布里的 `SHA256SUMS.txt` 核对，不对就丢掉；新文件先写在程序旁边，再改名换上（Windows 上正在运行的程序只能改名，旧的变成 `.old`，下次启动删掉）。然后回答页面，1.5 秒后重启进新程序：Linux 和 macOS 直接 `exec`，Windows 启动新程序再退出（新程序等旧窗口关掉再拿单实例锁）。Web 版页面一直问版本，变了就刷新。
- **不能一键更新的**：Docker 版（在项目目录 `git pull && docker compose up -d --build`）、程序目录不能写的（systemd 服务文件用了 `ProtectSystem=strict`，按部署文档用 `sudo install` 替换）、开发版。页面上写明原因。
- **诊断包**：「下载诊断包」生成一个 zip：`miaopanel.json`（版本、系统、哪些功能配置了——只有是否配置，服务器列表的地址只留前两段）、`recent-activity.json`（最近 200 条查看和修改记录的标题和结果，失败的附上最后 1500 个字符的输出，IPv4 地址部分隐藏）、`incidents.json`（7 天的监控故障）和说明。不含密钥、密码、对话内容和访问日志。下载走一次性链接（`POST /api/diagnostics/link`）。
