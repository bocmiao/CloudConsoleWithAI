# Miao Panel（喵面板）

一个**开源、通用**的 AI 服务器与云管理助手，支持 1Panel、宝塔和纯 Linux。任何 Linux 服务器都能用：装了 1Panel、宝塔，或者什么面板都没装；云产品先支持腾讯云（EdgeOne、DNSPod、轻量、CVM）。先做成 Windows 本地 exe，之后做 Web 版。

- **一句话建站**：「把 blog.example.com 上线到我的服务器，走 EO，开 HTTPS」→ 自动完成 EO 站点、加速域名、DNSPod 解析、防火墙、证书和面板建站，并验证结果。
- **访问统计**：「今天下午流量为什么涨了？」→ 基于 EO 数据分析自动归因（Top IP / URL / UA / 地区）。
- **服务器状态**：CPU、内存、磁盘、带宽、到期时间一屏总览；「现在是什么在吃内存？」直接问。
- **AI 排障**：「网站打不开」→ 按 DNS → EO → 网络 → 主机 → 应用逐层取证，给出结论、证据和修复方案。
- **优化建议，同意后自动执行**：先用数据判断是否真有问题，再给出具体改法；你同意后自动执行，失败自动回滚，事后汇报效果。1Panel、宝塔上的修改走面板自己的 API，面板里看得到、不会被覆盖。
- **看不懂命令也能安全使用**：AI 只能提交计划；模板覆盖不到时可以开启受限的 AI 自由命令，由系统替你检查命令、试运行、做快照，并设置「5 分钟保险」自动恢复。

完整设计见 [docs/DESIGN.md](docs/DESIGN.md)。

## 怎么用（Windows）

1. 在仓库的 Actions 或 Releases 页面下载 `MiaoPanel-windows-amd64.exe`；
2. 双击运行，会自动打开浏览器（地址是 `http://127.0.0.1:18765`，只有你自己的电脑能访问）；
3. 按页面上的三步走：**设置 AI 模型**（推荐 DeepSeek V4.1 Flash，在 DeepSeek 开放平台创建 API Key）→ **添加服务器**（IP、用户名、密码）→ **识别环境**；
4. 到「AI 助手」里用大白话说你想干什么，比如「服务器内存是不是太高了？帮我优化一下」；
5. AI 检查完会给出一份**清单**，勾选想做的项目，点「执行」并确认，就会自动完成，并显示执行前后的对比；能撤销的项目可以一键撤销。

没点「执行」之前，程序只**查看**服务器，不做任何修改；清单也保存在「建议」页。目前能自动执行的操作：添加 swap、清理旧日志、重启服务、重启容器，以及 1Panel 上的调整 PHP-FPM 进程数、调整 MySQL 内存参数、设置应用内存上限、固定 Java 应用的最大堆、备份应用或数据库，详见 [设计文档第 18 节](docs/DESIGN.md)。

**腾讯云（DNSPod + EdgeOne）**：在「设置 → 腾讯云」填入一个子账号的 SecretId 和 SecretKey（页面上有创建步骤，只给它 DNSPod 和 EdgeOne 权限，不要用主账号）。然后在 AI 助手里说「把 blog.example.com 接入 EO 并开 HTTPS，源站是我的服务器」，AI 会查清楚现状并给出清单：添加 EdgeOne 加速域名 → 解析切到 EdgeOne → 申请免费证书，每一步都能回滚。EdgeOne 里还没有这个站点时，需要先在控制台添加站点并选好套餐（涉及计费）。

**对话记录**：和 AI 的对话会自动保存，关掉程序再打开也在，还能接着问；在 AI 助手页左侧的「对话记录」里切换或删除。

**日志和回滚**：「日志 → 执行日志」里能查到 Miao Panel 在服务器上执行过的每一条命令（包括 AI 的只读检查）和结果；改过的东西可以在这里一键回滚。能回滚的修改还会在服务器的备份目录里留一个 `rollback.sh`，就算电脑上的 Miao Panel 不在了，也能在服务器上用 root 执行它恢复。

**1Panel 用户**：在 1Panel「面板设置 → API 接口」开启 API，IP 白名单填 `127.0.0.1`，把密钥粘贴到 Miao Panel 的服务器页「1Panel 接口」里并点「测试」。这样 PHP、MySQL 的修改会走 1Panel 自己的接口，面板里看得到、不会被覆盖。

密码和 API Key 保存在 Windows 凭据管理器里。关闭命令行窗口即退出。

## 从源码构建

需要 Go（版本见 `go.mod`），不需要 Node.js（界面用的是 Vue 3 浏览器版，已经放在仓库里）：

```bash
go test ./...                                                   # 运行测试
go build -o miaopanel ./cmd/miaopanel                           # 当前系统
GOOS=windows GOARCH=amd64 go build -o MiaoPanel.exe ./cmd/miaopanel  # Windows exe
```

运行参数：`--port`（默认 18765，被占用时自动换一个）、`--data`（数据目录，默认是用户配置目录下的 `MiaoPanel`）、`--no-browser`。

## 服务器环境识别脚本

[`scripts/discover.sh`](scripts/discover.sh) 是一个只读脚本，用来识别服务器上跑了什么（1Panel / 宝塔、Nginx 站点、PHP-FPM、MySQL / Redis、Docker、WordPress、Java 等），输出一份脱敏后的精简报告。系统会通过 SSH 或腾讯云自动化助手（TAT）自动执行它，你也可以手动运行：

```bash
sudo bash scripts/discover.sh               # 全部段落
sudo bash scripts/discover.sh docker web    # 只看指定段落
```

也可以把脚本内容粘贴到腾讯云控制台「自动化助手 → 执行命令」里，以 Shell 类型执行。

## 开源协议

[GPL-3.0](LICENSE)
