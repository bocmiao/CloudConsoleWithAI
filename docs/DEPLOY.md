# 部署 Miao Panel Web 版

Web 版和桌面版是同一个程序：`miaopanel serve` 在服务器上运行，用浏览器从任何地方登录使用，功能和桌面版一样。
放在服务器上 7×24 运行，日报、提醒、自动封禁这些后台任务不用等你开电脑。

> **先想清楚放在哪台机器上。** Miao Panel 保存着你所有服务器的 SSH 密码或私钥、腾讯云密钥和 AI Key，谁能登录它，谁就能管理你所有的服务器。
> 建议放在一台只有你能登录的服务器上，只通过 HTTPS 访问，并开启两步验证。

## 方式一：Docker（推荐）

需要 Docker 和 Docker Compose（1Panel、宝塔都自带）。

```bash
git clone https://github.com/bocmiao/CloudConsoleWithAI.git miaopanel
cd miaopanel
docker compose up -d --build
# 在国内构建慢或失败时，换 Go 模块代理：
# docker compose build --build-arg GOPROXY=https://goproxy.cn,direct && docker compose up -d
docker logs miaopanel          # 找到「初始化码」
```

默认只在本机的 `127.0.0.1:18765` 上监听，需要再配一个带 HTTPS 的反向代理（见下文）才能从外面访问。
数据（数据库和密钥）在 Docker 卷 `miaopanel-data` 里，删除容器不会丢；**删除这个卷就全没了**。

升级：`git pull && docker compose up -d --build`。

## 方式二：直接运行程序（systemd）

从 [Releases](https://github.com/bocmiao/CloudConsoleWithAI/releases)（还没有发布版本时，用 [Actions](https://github.com/bocmiao/CloudConsoleWithAI/actions) 里最新一次构建的产物，下载后先解压）下载 `MiaoPanel-linux-amd64`（ARM 服务器用 `linux-arm64`），然后：

```bash
sudo useradd --system --home /var/lib/miaopanel --shell /usr/sbin/nologin miaopanel
sudo install -m 755 MiaoPanel-linux-amd64 /usr/local/bin/miaopanel
sudo curl -o /etc/systemd/system/miaopanel.service \
  https://raw.githubusercontent.com/bocmiao/CloudConsoleWithAI/HEAD/deploy/miaopanel.service
sudo systemctl daemon-reload && sudo systemctl enable --now miaopanel
sudo journalctl -u miaopanel   # 找到「初始化码」
```

服务文件在 [`deploy/miaopanel.service`](../deploy/miaopanel.service)：以 `miaopanel` 用户运行，数据在 `/var/lib/miaopanel`，只监听 `127.0.0.1:18765`。
升级：替换 `/usr/local/bin/miaopanel` 后 `sudo systemctl restart miaopanel`。

## 配置 HTTPS 反向代理

Miao Panel 本身只说 HTTP，由前面的网站服务提供 HTTPS 证书。登录 Cookie 只在 HTTPS 下标为安全，没有 HTTPS 时登录页会提示密码会明文传输。

**1Panel**：「网站 → 创建网站 → 反向代理」，填一个解析到这台服务器的域名，代理地址填 `http://127.0.0.1:18765`；
创建后在网站设置里申请 HTTPS 证书，并在反向代理的配置里确认有下面几行（没有就加上）：

```nginx
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-Proto $scheme;
proxy_buffering off;
proxy_request_buffering off;
proxy_read_timeout 3600s;
client_max_body_size 0;
```

**宝塔**：「网站 → 添加站点」，在站点设置的「反向代理」里目标 URL 填 `http://127.0.0.1:18765`，「SSL」里申请证书，配置文件同样核对上面几行。

**Nginx**：参考 [`deploy/nginx.conf`](../deploy/nginx.conf)。**Caddy**（自动申请证书）：参考 [`deploy/Caddyfile`](../deploy/Caddyfile)。

这几行的作用：`X-Real-IP` 让登录失败按访客 IP 限制；`X-Forwarded-Proto` 让 Miao Panel 知道是 HTTPS；
关闭缓冲让 AI 的回答和终端输出实时显示；超时时间让终端能长时间开着；`client_max_body_size 0` 让「文件」和「存储」页能上传大文件。

不想用反向代理，也可以让 Miao Panel 自己提供 HTTPS：`miaopanel serve --listen 0.0.0.0:443 --tls-cert 证书.pem --tls-key 私钥.pem`。

## 第一次登录

打开你的域名，填日志里的初始化码、用户名和密码（至少 10 个字符），创建管理员账号。初始化码只能用来创建第一个账号，用过就失效，
所以就算别人先打开了这个页面，没有服务器的登录权限也拿不到初始化码。

登录后建议马上在「设置 → 账号与安全」开启两步验证（Google Authenticator、Microsoft Authenticator、腾讯身份验证器等都可以），
这里也能修改密码、查看哪些设备登录着、让某个设备退出。

登录的保护：同一个 IP 连续输错 5 次要等 15 分钟；一个账号 15 分钟内被输错 20 次，也要等 15 分钟。
3 天没使用或者登录满 30 天会自动退出。所有登录、登录失败、修改密码都记在「日志 → 操作记录」里。

## 忘记密码、丢了两步验证的手机

在服务器上重设密码（同时关闭两步验证、退出所有登录）：

```bash
docker exec -it miaopanel miaopanel reset-password                                   # Docker
sudo -u miaopanel miaopanel reset-password --data /var/lib/miaopanel                  # systemd
```

## 设置参考

| 参数 | 环境变量 | 默认 | 说明 |
|---|---|---|---|
| `--listen` | `MIAO_LISTEN` | `127.0.0.1:18765`（Docker 里是 `0.0.0.0:18765`） | 监听地址 |
| `--data` | `MIAO_DATA` | 当前用户的配置目录（Docker 里是 `/data`） | 数据库和密钥的位置 |
| `--tls-cert` / `--tls-key` | `MIAO_TLS_CERT` / `MIAO_TLS_KEY` | 无 | 由 Miao Panel 自己提供 HTTPS |
| | `TZ` | `Asia/Shanghai`（Docker） | 日报按这个时区生成 |

## 和桌面版的区别

- 服务器和密钥要在 Web 版里重新添加：桌面版的密钥存在 Windows 凭据管理器里，不能搬过去；
- 用密钥登录服务器时，把私钥内容粘贴进去（Web 版读不到你电脑上的密钥文件），私钥和密码一样只保存在运行 Miao Panel 的服务器上，不会发给 AI；
- 密钥保存在数据目录的 `secrets.json`（只有运行 Miao Panel 的用户可读）。备份数据目录就是备份了全部配置和密钥，请把备份放在安全的地方；
- 目前只有一个管理员账号。
