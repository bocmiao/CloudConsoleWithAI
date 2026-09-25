# CloudConsoleWithAI

用 AI 管理你的腾讯云产品（EdgeOne、DNSPod、轻量应用服务器、CVM 等）。

- **一句话建站**：「把 blog.example.com 上线到我的轻量服务器，走 EO，开 HTTPS」→ 自动完成 EO 站点、加速域名、DNSPod 解析、防火墙、证书，并验证结果。
- **访问统计**：「今天下午流量为什么涨了？」→ 基于 EO 数据分析自动归因（Top IP / URL / UA / 地区），每天推送日报。
- **服务器状态**：CPU、内存、磁盘、带宽、到期时间一屏总览；「现在是什么在吃内存？」直接问。
- **AI 排障**：「网站打不开」→ 按 DNS → EO → 网络 → 主机 → 应用逐层取证，给出结论、证据和修复方案。
- **优化建议，同意后自动执行**：「我觉得内存占用太高」→ 先用数据判断是否真有问题，再给出具体改法（精确到配置 diff）；你勾选同意后自动执行，失败自动回滚，24 小时后汇报效果。
- **安全第一**：AI 只能提交计划，所有写操作都要你确认后才由执行引擎执行，可审计、可回滚。

完整设计见 [docs/DESIGN.md](docs/DESIGN.md)。首个支持的服务器环境是 **1Panel**：优化类修改通过 1Panel 自己的 API 执行，面板里看得到、不会被覆盖（设计文档第 8 节）。

## 服务器环境识别脚本

[`scripts/discover.sh`](scripts/discover.sh) 是一个只读脚本，用来识别服务器上跑了什么（1Panel / 宝塔、Nginx 站点、PHP-FPM、MySQL / Redis、Docker、WordPress、Java 等），输出一份脱敏后的精简报告。系统会通过腾讯云自动化助手（TAT）自动执行它，你也可以手动运行：

```bash
sudo bash scripts/discover.sh               # 全部段落
sudo bash scripts/discover.sh docker web    # 只看指定段落
```

也可以把脚本内容粘贴到腾讯云控制台「自动化助手 → 执行命令」里，以 Shell 类型执行。
