# Miao Panel 设计方案（v0.5）

> **Miao Panel（喵面板）**：一个**开源（GPL-3.0）、通用**的 AI 服务器与云管理助手：任何 Linux 服务器都能用（装了 1Panel、宝塔，或者什么面板都没装），云产品先支持腾讯云（EdgeOne、DNSPod、轻量、CVM）。先做成**本地 exe**，之后做 Web 版。
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

### 2.7 先做本地 exe，之后做 Web 版

- **同一套代码两种运行方式**：本地 exe 模式（双击运行，在本机启动服务并自动打开浏览器界面）和服务器模式（以后的 Web 版，部署到服务器上，多用户登录）；
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

- 名称：**Miao Panel（喵面板）**；开源协议：**GPL-3.0**；
- 开源、通用：支持 1Panel、宝塔和没装面板的服务器；
- 先做本地 exe（Windows 优先），之后做 Web 版；
- 你的服务器是 1Panel v2；
- 开启受限的 AI 自由命令，按 7.5 的方式由系统把关；
- 域名在 DNSPod、已备案：EO 默认用 DNSPod 托管接入（`dnsPodAccess`），加速区域可以选中国大陆（`mainland`）或全球（`global`）；
- 默认 AI 模型：DeepSeek V4.1 Flash（见 11.1）。

**关于名称的一点提醒**：「Panel」容易让人以为这是又一个服务器面板，和 1Panel、宝塔是竞争关系；其实它是在这些面板之上工作的 AI 助手。建议在介绍里始终带上一句定位，比如「Miao Panel —— 帮你管服务器和云的 AI 助手，支持 1Panel、宝塔和纯 Linux」。

## 18. 当前进度（M0 + M1 第一步）

已完成（代码在 `cmd/`、`internal/`、`scripts/`）：

- 单文件程序：双击运行，在 `127.0.0.1` 启动网页界面并自动打开浏览器；一次性令牌登录 + Cookie + Host 校验；
- 添加服务器（SSH 密码或密钥文件），首次连接记录服务器指纹，指纹变化时拒绝连接；
- 密码和 API Key 存系统钥匙串（不可用时存数据目录下仅当前用户可读的文件）；
- 环境识别（通过 SSH 运行 `scripts/discover.sh`），服务器画像页面，自动发现问题（内存、磁盘、OOM、SSH 设置、失败的服务等）；
- AI 助手：只读工具（服务器列表、画像、重新识别、按项检查、近 24 小时错误日志），修改建议保存到「建议」页（风险等级由系统判定）；支持 DeepSeek 等 OpenAI 兼容接口和 Claude；每次回答显示花费，可设每月上限；
- 操作记录；新手引导；
- 自动化测试：进程内 SSH 服务器端到端测试、模拟模型 API 测试、HTTP 安全测试；GitHub Actions 自动测试并构建 Windows / macOS / Linux 程序。

- 对话记录保存在本地数据库：AI 助手页左侧是对话列表，打开程序时自动回到最近的对话；关掉程序后打开旧对话还能接着问（模型会收到之前的问题和回答，但之前工具查到的原始数据不会重发，需要时 AI 会重新查询；最多带最近 40 条消息）；删除对话不会删除里面生成的清单。

还没做（M0 剩余）：腾讯云 TAT 连接方式、新手向导里的 SSH 密钥和腾讯云子账号引导。

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

还没做：宝塔面板的 PHP / MySQL 接口、1Panel 应用的其他参数、受限的 AI 自由命令（第 7.5 节）、腾讯云 EO / DNSPod 相关操作。

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

还没做：在 EdgeOne 新建站点（要选套餐、涉及计费，暂由用户在控制台操作）、在面板里自动建站、EdgeOne 访问统计与缓存/安全优化。还没有在真实的腾讯云账号上验证过。

### Windows 桌面窗口（已完成）

Windows 版改成真正的桌面程序：双击后打开 Miao Panel 自己的窗口，不再打开浏览器，也没有命令行窗口。

- 窗口用系统自带的 WebView2（go-webview2，纯 Go，不需要 cgo），界面和浏览器版是同一套；程序仍然只在 127.0.0.1 上提供服务，窗口带一次性令牌登录，安全模型不变；
- 编译成 GUI 程序（`-H=windowsgui`）；资源文件带程序图标、版本信息和「每显示器 DPI 感知」声明，窗口大小按屏幕缩放计算，高分屏不发虚；
- 只允许运行一个：再次双击会把已经打开的窗口切到前面；
- 页面里在新窗口打开的链接交给系统浏览器；
- 缺少 WebView2 运行库时提示安装，并临时改用浏览器；`--browser` 参数也可以强制用浏览器。这两种情况下用一个提示框代替命令行窗口，点「确定」退出；
- 关闭窗口即退出；如果当时有清单正在执行，下次启动会把它标记为「中断」。
