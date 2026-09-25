# CloudConsoleWithAI

一个**开源、通用**的 AI 服务器与云管理助手。任何 Linux 服务器都能用：装了 1Panel、宝塔，或者什么面板都没装；云产品先支持腾讯云（EdgeOne、DNSPod、轻量、CVM）。先做成 Windows 本地 exe，之后做 Web 版。

- **一句话建站**：「把 blog.example.com 上线到我的服务器，走 EO，开 HTTPS」→ 自动完成 EO 站点、加速域名、DNSPod 解析、防火墙、证书和面板建站，并验证结果。
- **访问统计**：「今天下午流量为什么涨了？」→ 基于 EO 数据分析自动归因（Top IP / URL / UA / 地区）。
- **服务器状态**：CPU、内存、磁盘、带宽、到期时间一屏总览；「现在是什么在吃内存？」直接问。
- **AI 排障**：「网站打不开」→ 按 DNS → EO → 网络 → 主机 → 应用逐层取证，给出结论、证据和修复方案。
- **优化建议，同意后自动执行**：先用数据判断是否真有问题，再给出具体改法；你同意后自动执行，失败自动回滚，事后汇报效果。1Panel、宝塔上的修改走面板自己的 API，面板里看得到、不会被覆盖。
- **看不懂命令也能安全使用**：AI 只能提交计划；模板覆盖不到时可以开启受限的 AI 自由命令，由系统替你检查命令、试运行、做快照，并设置「5 分钟保险」自动恢复。

完整设计见 [docs/DESIGN.md](docs/DESIGN.md)。

## 服务器环境识别脚本

[`scripts/discover.sh`](scripts/discover.sh) 是一个只读脚本，用来识别服务器上跑了什么（1Panel / 宝塔、Nginx 站点、PHP-FPM、MySQL / Redis、Docker、WordPress、Java 等），输出一份脱敏后的精简报告。系统会通过 SSH 或腾讯云自动化助手（TAT）自动执行它，你也可以手动运行：

```bash
sudo bash scripts/discover.sh               # 全部段落
sudo bash scripts/discover.sh docker web    # 只看指定段落
```

也可以把脚本内容粘贴到腾讯云控制台「自动化助手 → 执行命令」里，以 Shell 类型执行。
