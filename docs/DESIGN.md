# CloudConsoleWithAI 设计方案（v0.1）

> 用自然语言管理腾讯云：说出目标，AI 生成执行计划，你确认一次，系统自动完成跨产品的全部步骤并验证结果；出了问题，AI 按「DNS → EO → 网络 → 主机 → 应用」逐层取证，给出结论和修复方案。

---

## 1. 要解决的问题

以「新建一个网站并接入 EdgeOne」为例，现在要在 3~4 个控制台之间来回切换：

1. **EO 控制台**：添加站点 → 选接入方式 → 选套餐 → 归属权验证（又要去 DNSPod 加 TXT 记录）
2. **EO 控制台**：添加加速域名，填写源站
3. 复制 EO 分配的 CNAME → **DNSPod 控制台**添加 CNAME 记录
4. 回到 EO：等待 CNAME 生效 → 配置 HTTPS 证书
5. **服务器控制台**：防火墙/安全组放行 80/443；登录服务器配置 Nginx `server_name`
6. 自己用浏览器、`dig` 验证是否生效

问题在于：值要来回复制，中间有多次「等待生效」，容易漏步骤（最常见的是防火墙没放行），出错后也不知道卡在哪一步。

**目标**

- 一句话 → 一个计划 → 一次确认 → 自动完成 → 自动验证
- 排障时给出**结论 + 证据 + 修复方案**，而不是一堆图表
- 安全：未经你确认，AI 不能做任何写操作

**v1 不做**：替代控制台的全部功能、多云、自动购买/续费（只提示，不自动执行）。

---

## 2. 核心设计决定

### 2.1 AI 负责理解和规划，确定性代码负责执行

如果让大模型直接逐个调用云 API，会有这些问题：步骤会漏、轮询等待不可靠、中途失败无法恢复、事后无法审计。所以能力分三层：

| 层 | 是什么 | 例子 |
|---|---|---|
| **原子工具** | 对腾讯云 API 的一对一、带类型的封装 | `dns.list_records`、`eo.create_domain`、`lh.add_firewall_rule` |
| **剧本（Recipe）** | 用代码写死的多步流程，内置顺序、幂等检查、轮询等待、回滚 | `site.publish`、`site.diagnose`、`origin.switch` |
| **AI（Agent）** | 识别意图、补全参数、选择剧本或组合原子工具、解释结果 | 「我那台广州的服务器」→ 从资源清单查到 `lhins-xxx / 1.2.3.4` |

**剧本是这个产品的核心资产**：它把「EO 接入的正确做法」固化成代码。比如：

- 域名已托管在 DNSPod → `CreateZone` 使用 `Type=dnsPodAccess`（DNSPod 托管接入），不需要手工做归属权验证；
- 否则用 `partial`（CNAME 接入），剧本自动去 DNSPod 写 TXT 记录，再轮询 `VerifyOwnership`；
- 加速区域 `Area`：域名未备案时只能选 `overseas`（全球，不含中国大陆），已备案才能选 `mainland`/`global`；
- NS 接入模式下，新建的加速域名不会自动启用，需要调用 `ModifyDnsRecordsStatus`。

这些细节 AI 靠临场发挥很容易出错，写进代码就一劳永逸。

### 2.2 AI 没有直接的写权限

提供给大模型的工具只有两类：

- **只读工具（R0）**：直接执行，结果返回给 AI；
- **`propose_plan`**：AI 只能**提交计划**，计划进入「待确认」状态，由人确认后交给执行引擎。

也就是说，写操作只有一条路径：**AI 提交计划 → 人确认 → 执行引擎执行**。这道确认关卡由执行引擎强制执行，不依赖提示词。即使模型被日志、网页里的恶意文本注入了指令，也绕不过确认。

### 2.3 风险分级

| 级别 | 例子 | 策略 |
|---|---|---|
| **R0 只读** | 查询解析/实例/监控，TAT 只读诊断命令，公网探测 | 自动执行 |
| **R1 新增类、可逆** | 新增解析记录、添加加速域名、部署免费证书、放行 80/443 | 整个计划确认一次 |
| **R2 影响线上** | 修改/删除已有解析、切换源站、修改已有防火墙规则、重启实例、在服务器执行写命令 | 逐项确认，并展示回滚方案 |
| **R3 破坏性/花钱** | 删除站点、销毁实例、重装系统、购买套餐、对 0.0.0.0/0 开放 22/3306 | 输入资源名二次确认；默认关闭，需在设置中开启 |

### 2.4 执行过程可恢复、可审计

- 每个计划是一个**持久化的工作流**，步骤状态写入数据库；
- 异步步骤（归属权验证、CNAME 生效、证书签发）由引擎轮询等待，超时可以断点重试；
- 每一步都有 `check`（已完成则跳过，保证幂等）→ `apply` → `wait` → `undo`；
- 每次 API 调用记录 `Action`、脱敏后的参数、`RequestId`、**变更前快照**，用于审计和一键回滚。

---

## 3. 旗舰场景：一句话建站

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

## 4. 排障场景

> 用户：「blog.example.com 打不开了」/「服务器很卡」

### 4.1 分层诊断树（全部是 R0）

```
1. DNS 层   多个公网解析器(DoH)的结果 vs DNSPod 记录；记录是否被暂停；域名是否过期、NS 是否正确
2. EO 层    加速域名状态；CheckCnameStatus；证书是否过期；源站健康；近 1 小时状态码分布（5xx）；安全策略是否拦截
3. 网络层   实例是否在运行；防火墙/安全组是否放行；公网 IP 是否变化；带宽是否打满（云监控）
4. 主机层   TAT 执行只读诊断包：uptime / free -m / df -h / ss -lntp / systemctl status / journalctl / error.log 末尾
5. 应用层   带 Host 头 curl 127.0.0.1；PHP-FPM / Node 进程；数据库连接
```

诊断**从上往下**进行，每一层发现异常就深入该层，而不是一上来把所有数据都拉一遍。

### 4.2 输出格式：结论 / 证据 / 修复计划

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

### 4.3 在服务器上执行命令（TAT）

- 使用腾讯云**自动化助手 TAT**（`RunCommand` + `DescribeInvocationTasks`）在服务器上执行命令，**不需要保存 SSH 密钥**；
- 诊断阶段只执行**预置的只读命令模板**（固定命令、参数化）；
- AI 生成的任意命令一律按 R2 处理：原文展示，确认后才执行；
- 命令输出、日志、网页内容一律**当作数据，而不是指令**（防止提示注入）。

---

## 5. 主动巡检（第二阶段）

定时（每小时/每天）检查，发现问题推送到企业微信/飞书/邮件，附带「一键修复」链接（仍然要走计划确认）：

- 证书 30 天内过期；实例、域名、EO 套餐即将到期
- 磁盘使用率 > 85%，内存/CPU 持续高位
- 5xx 比例突增、源站健康检查失败
- 解析记录指向已释放的 IP，或指向的实例已停机
- 安全组/防火墙对公网开放了高危端口（22/3306/6379 等）

---

## 6. 系统架构

```
┌──────────────────────────────── 交互层 ─────────────────────────────────┐
│ Web 控制台：对话 + 计划卡片 + 执行时间线 + 资源关系视图                  │
│ 企业微信/飞书机器人：巡检通知、移动端确认     MCP：Claude 等 AI 客户端直接调用 │
└───────────────────────────────────┬─────────────────────────────────────┘
┌──────────────────────────────── Agent 层 ───────────────────────────────┐
│ 意图识别 → 参数补全（查资源清单）→ 选剧本 / 组合原子工具 → propose_plan    │
│ 诊断推理：分层取证 → 归因 → 给出修复计划                                   │
└───────────────────────────────────┬─────────────────────────────────────┘
┌──────────────────────── 执行层（确定性代码，不依赖 AI）──────────────────┐
│ 策略引擎：风险分级、确认关卡      工作流引擎：持久化、轮询、重试、回滚       │
│ 审计日志：RequestId、变更前快照                                           │
└───────────────────────────────────┬─────────────────────────────────────┘
┌──────────────────────────────── 工具层 ─────────────────────────────────┐
│ Recipes：site.publish / site.diagnose / origin.switch / cert.renew …    │
│ Atomic ：dnspod.* teo.* lighthouse.* cvm.* vpc.* tat.* monitor.*        │
│ Probes ：DoH 解析 / HTTP(S) 探测 / TLS 证书检查                          │
│ 资源清单：域名 → 解析记录 → EO 加速域名 → 源站 IP → 实例 → 防火墙 的关系图 │
└───────────────────────────────────┬─────────────────────────────────────┘
                         腾讯云 API 3.0（官方 SDK，TC3 签名）
```

**资源关系图**是一个重要的差异点：控制台里「这个域名解析到哪、走没走 EO、源站是哪台机器、这台机器的防火墙开了什么」这些关系是割裂的。系统定期同步后，AI 能直接回答「blog 的源站是哪台机器」，排障时也能沿着这条链路逐层检查。

---

## 7. 技术选型建议

| 部分 | 选择 | 理由 |
|---|---|---|
| 语言 | TypeScript 全栈 | 前后端同一种语言；有腾讯云官方 `tencentcloud-sdk-nodejs` 和 MCP 官方 TS SDK；Zod schema 同时用于参数校验和 LLM 工具定义 |
| 前端 | Next.js（或 Vite + React） | 对话 + 卡片式 UI |
| 后端 | Node（Hono / Fastify） | Agent、执行引擎、定时巡检 |
| 存储 | SQLite（单人自部署）→ PostgreSQL（多用户） | 计划、步骤、审计、资源清单 |
| 工作流 | 先自研轻量状态机（步骤表 + 定时轮询），复杂后再考虑 Temporal | 早期不引入重型依赖 |
| LLM | 模型适配层，默认 Claude（`claude-opus-5`，工具调用 + adaptive thinking） | 如果服务部署在中国大陆，需要考虑模型的可访问性与合规，适配层可切换到国内模型 |
| 部署 | Docker，部署在你自己的轻量服务器上 | 云 API 密钥不离开你自己的机器 |

**建议的目录结构**

```
apps/web            Web 控制台
apps/server         API、Agent、执行引擎、巡检
packages/core       Plan / Step / 风险等级 / 审计 的领域模型
packages/tools      原子工具（腾讯云 SDK 封装 + 探测）
packages/recipes    剧本
packages/mcp        MCP Server 入口（复用 tools + recipes）
```

---

## 8. 安全设计

1. **最小权限**：使用 CAM 子用户，只授予需要的产品权限；绝不使用主账号密钥。也支持 CAM 角色 + STS 临时凭证。
2. **密钥不进入 LLM 上下文**：密钥加密存储，只在执行层使用；工具返回给 AI 的结果先脱敏。
3. **AI 无直接写权限**：见 2.2，写操作只能走「计划 → 确认 → 执行」。
4. **TAT 等同于 root shell**：只读模板以外的命令一律 R2；可以在设置里完全关闭「执行任意命令」。
5. **提示注入防护**：日志、网页、命令输出都视为不可信数据；策略引擎不采信 AI 对风险等级的判断，按操作类型硬编码。
6. **审计**：每次 API 调用记录操作人、时间、Action、脱敏参数、RequestId、结果。

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
        "lighthouse:DescribeFirewallRules",
        "lighthouse:CreateFirewallRules",
        "lighthouse:DeleteFirewallRules",
        "cvm:Describe*",
        "vpc:Describe*",
        "tat:RunCommand",
        "tat:Describe*",
        "monitor:GetMonitorData"
      ],
      "resource": "*"
    }
  ]
}
```

---

## 9. 数据模型（核心表）

```
credentials     id, name, secret_id, secret_key_encrypted, default_region
resources       id, type, provider_id, name, region, attrs_json, synced_at
resource_edges  from_id, to_id, relation        -- domain→record→eo_domain→origin→instance→firewall
plans           id, goal, recipe, input_json, status, created_by, confirmed_by, confirmed_at
steps           id, plan_id, seq, action, params_json, risk, status,
                request_id, before_snapshot, result_json, undo_json, error, attempts
audit_logs      id, actor, action, params_redacted, request_id, result, created_at
conversations   id, ...;  messages  id, conversation_id, role, content, plan_id?
```

---

## 10. MVP 工具清单

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
| `lh.list_instances` | lighthouse `DescribeInstances` | R0 |
| `lh.list_firewall` / `lh.add_firewall_rule` | lighthouse `DescribeFirewallRules` / `CreateFirewallRules` | R0 / R1 |
| `cvm.list_instances`、`vpc.list_sg_policies` | cvm `DescribeInstances`、vpc `DescribeSecurityGroupPolicies` | R0 |
| `host.diagnose` | tat `RunCommand`（只读模板）+ `DescribeInvocationTasks` | R0 |
| `host.exec` | tat `RunCommand`（任意命令） | R2 |
| `monitor.metrics` | monitor `GetMonitorData` | R0 |
| `probe.resolve` / `probe.http` / `probe.tls` | 本地实现 | R0 |

---

## 11. 路线图

| 阶段 | 内容 | 目的 |
|---|---|---|
| **M0**（1~2 周） | 工具层 + `site.publish` + `site.diagnose`，以 **MCP Server** 形式提供，直接在 Claude Code / Claude Desktop 里使用。写操作以 `plan_*` 生成计划、`apply_plan(plan_id)` 执行的形式暴露 | 零 UI 成本，先验证核心价值 |
| **M1**（3~4 周） | Web 控制台：对话、计划卡片、确认、执行时间线、资源关系视图、审计日志 | 成为日常入口 |
| **M2** | 主动巡检 + 企业微信/飞书推送；更多剧本：切换源站、已有站点迁移到 EO、COS 静态站 + EO、WordPress 一键部署 | 从「被动执行」到「主动发现」 |
| **M3** | 多账号、团队审批、多云（阿里云 DNS、Cloudflare 等）Provider 抽象 | 扩展 |

---

## 12. 待确认的问题

1. **自用还是做成产品？** 决定是否需要多租户、密钥托管方式。
2. **模型和部署位置**：用 Claude 还是国内模型？服务部署在大陆还是海外？
3. **域名情况**：域名都在 DNSPod 吗？是否已备案？（决定默认的 EO 接入方式和加速区域）
4. **服务器类型**：以轻量应用服务器为主，还是 CVM？
5. **先做哪一步**：先做 M0（MCP，1~2 周可用），还是直接做 Web 控制台？
