# CloudConsoleWithAI

用 AI 管理你的腾讯云产品（EdgeOne、DNSPod、轻量应用服务器、CVM 等）。

- **一句话建站**：「把 blog.example.com 上线到我的轻量服务器，走 EO，开 HTTPS」→ 自动完成 EO 站点、加速域名、DNSPod 解析、防火墙、证书，并验证结果。
- **AI 排障**：「网站打不开」→ 按 DNS → EO → 网络 → 主机 → 应用逐层取证，给出结论、证据和修复方案。
- **安全第一**：AI 只能提交计划，所有写操作都要你确认后才由执行引擎执行，可审计、可回滚。

完整设计见 [docs/DESIGN.md](docs/DESIGN.md)。
