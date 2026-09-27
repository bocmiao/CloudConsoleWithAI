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

### 备份与恢复 Web 版数据

数据目录同时保存 SQLite 数据库和服务器、云服务的密钥。升级前先做一次备份，备份文件也要按密钥保管。SQLite 使用 WAL 模式，**不要在服务运行时只复制 `miaopanel.db`**。

Docker 版在项目目录执行（短暂停机，生成仅所有者可读的压缩包）：

```bash
umask 077
mkdir -p backups
docker compose stop miaopanel
docker compose run --rm --no-deps --user 0 \
  -v "$PWD/backups:/backup" --entrypoint sh miaopanel \
  -c 'umask 077; tar -C /data -czf /backup/miaopanel-backup.tar.gz .'
docker compose start miaopanel
```

在新机器或**新的、空的数据卷**里恢复。把备份放在新项目目录的 `backups/` 下，先不要运行 `docker compose up`：

```bash
docker compose run --rm --no-deps --user 0 \
  -v "$PWD/backups:/backup:ro" --entrypoint sh miaopanel \
  -c 'test -z "$(ls -A /data)" || { echo "数据卷必须为空"; exit 1; }; tar -C /data -xzf /backup/miaopanel-backup.tar.gz'
docker compose up -d
```

systemd 版同样先停服务，再将整个数据目录打包：

```bash
sudo systemctl stop miaopanel
sudo sh -c 'umask 077; tar -C /var/lib/miaopanel -czf /root/miaopanel-backup.tar.gz .'
sudo systemctl start miaopanel
```

在新机器的空数据目录恢复时，先安装好服务但不要启动，解包后运行 `sudo chown -R miaopanel:miaopanel /var/lib/miaopanel`，然后启动服务。恢复演练要检查原账号能登录、服务器列表和密钥可用、执行日志仍在。不要把含密钥的备份提交到 GitHub。

## 方式二：直接运行程序（systemd）

从 [Releases](https://github.com/bocmiao/CloudConsoleWithAI/releases/latest) 下载 Linux 版程序（ARM 服务器把下面的 `amd64` 换成 `arm64`），然后：

```bash
curl -fLO https://github.com/bocmiao/CloudConsoleWithAI/releases/latest/download/MiaoPanel-linux-amd64
sudo useradd --system --home /var/lib/miaopanel --shell /usr/sbin/nologin miaopanel
sudo install -m 755 MiaoPanel-linux-amd64 /usr/local/bin/miaopanel
sudo curl -o /etc/systemd/system/miaopanel.service \
  https://raw.githubusercontent.com/bocmiao/CloudConsoleWithAI/HEAD/deploy/miaopanel.service
sudo systemctl daemon-reload && sudo systemctl enable --now miaopanel
sudo journalctl -u miaopanel   # 找到「初始化码」
```

服务文件在 [`deploy/miaopanel.service`](../deploy/miaopanel.service)：以 `miaopanel` 用户运行，数据在 `/var/lib/miaopanel`，只监听 `127.0.0.1:18765`。
升级：下载新版本，`sudo install -m 755 MiaoPanel-linux-amd64 /usr/local/bin/miaopanel` 替换后 `sudo systemctl restart miaopanel`。

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

直接运行程序时，默认只信任来自本机回环地址的反向代理。Docker Compose 的端口只绑定主机回环地址，但容器里看到的主机代理连接来自 Docker 网桥，因此 `docker-compose.yml` 已信任常见的 Docker 网桥网段。如果你的网桥不在该网段，或反向代理从另一台机器连接，设置 `MIAO_TRUSTED_PROXIES` 为代理的 IP（或尽量小的 CIDR），多个地址用逗号分隔；例如 `MIAO_TRUSTED_PROXIES=172.20.0.5`。如果把 Docker 端口改成对公网开放，必须移除 Compose 里的网桥信任配置，并用程序自带的 HTTPS。不要让代理原样转发客户端提供的 `X-Real-IP` 和 `X-Forwarded-Proto`。

不想用反向代理，也可以让 Miao Panel 自己提供 HTTPS：`miaopanel serve --listen 0.0.0.0:443 --tls-cert 证书.pem --tls-key 私钥.pem`。

## 第一次登录

打开你的域名，填日志里的初始化码、用户名和密码（至少 10 个字符），创建管理员账号。初始化码只能用来创建第一个账号，用过就失效，
所以就算别人先打开了这个页面，没有服务器的登录权限也拿不到初始化码。

登录后建议马上在「设置 → 账号与安全」开启两步验证（Google Authenticator、Microsoft Authenticator、腾讯身份验证器等都可以），
这里也能修改密码、查看哪些设备登录着、让某个设备退出。

登录的保护：同一个 IP 连续输错 5 次要等 15 分钟；一个账号 15 分钟内被输错 20 次，也要等 15 分钟。
3 天没使用或者登录满 30 天会自动退出。所有登录、登录失败、修改密码都记在「日志 → 操作记录」里。

## 邮箱、手机号登录

除了用户名加密码，还可以用邮箱或手机收到的验证码登录，在「设置 → 账号与安全」里自己配置：

1. **设置从哪里发验证码**（保存时要输入账号密码，因为它决定了验证码发到谁手里）：
   - **发信邮箱（SMTP）**：选邮箱服务商（QQ 邮箱、163、腾讯企业邮箱、阿里企业邮箱、Gmail、Outlook）或者自己填 SMTP 服务器、端口和加密方式。
     QQ、163 邮箱的「密码」要填授权码（在邮箱网页版的设置里开启 SMTP 服务后生成），Gmail 填应用专用密码。可以先发一封测试邮件再保存。
     建议用一个单独的邮箱发信，不要用收验证码的那个；
   - **短信**，腾讯云和阿里云二选一，都可以设每天最多发多少条，防止被人刷短信费：
     - **腾讯云短信**：用「设置 → 腾讯云」里的密钥发送（子账号要有 `QcloudSMSFullAccess` 权限）。先在腾讯云短信控制台创建应用、申请签名和「验证码」类正文模板，
       审核通过后填 SDK AppID、签名、模板 ID 和模板里变量的个数（例如「您的验证码为：{1}，{2}分钟内有效」是 2 个）；
     - **阿里云短信**：用一个 RAM 用户的 AccessKey 发送（只给它 `AliyunDysmsFullAccess` 权限，不要用主账号的），AccessKey Secret 和其他密钥一样只保存在服务器的数据目录。
       先在阿里云短信服务控制台申请签名和「验证码」类模板，审核通过后填签名、模板 CODE（`SMS_` 开头）和模板里验证码的变量名（模板是「您的验证码为：${code}」就填 `code`；
       有「${min} 分钟内有效」这样的变量再填分钟数的变量名）。
2. **绑定邮箱、手机号**：输入账号密码，再输入发到新邮箱或手机上的验证码；
3. **开启登录方式**：邮箱验证码、短信验证码各自开关。至少有一种验证码登录能用时，也可以关掉密码登录。

规则：
- 验证码 6 位，10 分钟内有效、只能用一次，输错 5 次作废；同一个邮箱或手机号 1 分钟内只能发一次、一天最多 10 次，同一个 IP 一小时最多要 10 次；
- **只给管理员邮箱和手机号发送**（默认开启）：验证码只发到账号绑定的邮箱和手机号，登录验证码、测试邮件、测试短信都一样，其他地址一律拒发，拒发会记在「日志 → 操作记录」里。
  开启时不能更换绑定的邮箱或手机号（第一次绑定可以）；要更换，先输入账号密码关闭它，换好再开启；
- 登录页对「管理员的邮箱或手机号」和「其他的」回答一样（其他的并没有发送），别人没法用它试出你绑定的是哪个；
- 开了两步验证的账号，用验证码登录后还要输入身份验证器 App 里的验证码（验证码代替的是密码，不是两步验证）；
- 绑定过的邮箱、手机号也可以代替用户名，和密码一起登录；
- 发信或短信设置被清除时，密码登录会自动恢复，不会把你锁在外面。发信设置填错收不到验证码、又关了密码登录时，在服务器上运行 `miaopanel reset-password`，会重新开启密码登录。

## 忘记密码、丢了两步验证的手机

在服务器上重设密码（同时关闭两步验证、重新开启密码登录、退出所有登录）：

```bash
docker exec -it miaopanel miaopanel reset-password                                   # Docker
sudo -u miaopanel miaopanel reset-password --data /var/lib/miaopanel                  # systemd
```

## 设置参考

| 参数 | 环境变量 | 默认 | 说明 |
|---|---|---|---|
| `--listen` | `MIAO_LISTEN` | `127.0.0.1:18765`（Docker 里是 `0.0.0.0:18765`） | 监听地址 |
| `--trusted-proxies` | `MIAO_TRUSTED_PROXIES` | 空（仍信任本机回环地址） | 额外可信反向代理的 IP 或 CIDR，逗号分隔 |
| `--data` | `MIAO_DATA` | 当前用户的配置目录（Docker 里是 `/data`） | 数据库和密钥的位置 |
| `--tls-cert` / `--tls-key` | `MIAO_TLS_CERT` / `MIAO_TLS_KEY` | 无 | 由 Miao Panel 自己提供 HTTPS |
| | `TZ` | `Asia/Shanghai`（Docker） | 日报按这个时区生成 |

## 和桌面版的区别

- 服务器和密钥要在 Web 版里重新添加：桌面版的密钥存在 Windows 凭据管理器里，不能搬过去；
- 用密钥登录服务器时，把私钥内容粘贴进去（Web 版读不到你电脑上的密钥文件），私钥和密码一样只保存在运行 Miao Panel 的服务器上，不会发给 AI；
- 密钥保存在数据目录的 `secrets.json`（只有运行 Miao Panel 的用户可读）。备份数据目录就是备份了全部配置和密钥，请把备份放在安全的地方；
- 目前只有一个管理员账号。
