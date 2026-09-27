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

Docker Compose 在 Linux 服务器上使用主机网络，程序只监听本机的 `127.0.0.1:18765`，需要再配一个带 HTTPS 的反向代理（见下文）才能从外面访问。
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
sha256sum backups/miaopanel-backup.tar.gz > backups/miaopanel-backup.tar.gz.sha256
docker compose start miaopanel
```

在新机器或**新的、空的数据卷**里恢复。把备份放在新项目目录的 `backups/` 下，先不要运行 `docker compose up`：

```bash
sha256sum -c backups/miaopanel-backup.tar.gz.sha256
docker compose run --rm --no-deps --user 0 \
  -v "$PWD/backups:/backup:ro" --entrypoint sh miaopanel \
  -c 'test -z "$(ls -A /data)" || { echo "数据卷必须为空"; exit 1; }; tar -C /data -xzf /backup/miaopanel-backup.tar.gz'
docker compose up -d
```

systemd 版同样先停服务，再将整个数据目录打包：

```bash
sudo systemctl stop miaopanel
sudo sh -c 'umask 077; tar -C /var/lib/miaopanel -czf /root/miaopanel-backup.tar.gz .'
sudo sh -c 'cd /root && sha256sum miaopanel-backup.tar.gz > miaopanel-backup.tar.gz.sha256'
sudo systemctl start miaopanel
```

在新机器的空数据目录恢复时，先安装好服务但不要启动，先用 `sha256sum -c` 检查备份，再解包，运行 `sudo chown -R miaopanel:miaopanel /var/lib/miaopanel`，然后启动服务。恢复演练要检查原账号和两步验证能登录、服务器列表和密钥可用、执行日志及回滚信息仍在。至少保留一份不在原服务器磁盘上的备份。不要把含密钥的备份提交到 GitHub。

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
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
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

默认只信任来自本机回环地址的反向代理，Docker Compose 也通过主机网络保持这个边界。如果反向代理在另一台机器，设置 `MIAO_TRUSTED_PROXIES` 为代理的准确 IP（或尽量小的 CIDR），多个地址用逗号分隔；例如 `MIAO_TRUSTED_PROXIES=172.20.0.5`。代理必须覆盖客户端传来的 `X-Real-IP` 和 `X-Forwarded-Proto`，并覆盖或追加 `X-Forwarded-For`；不要把这些请求头原样转发。

直接运行程序且不想用反向代理时，也可以让 Miao Panel 自己提供 HTTPS：`miaopanel serve --listen 0.0.0.0:443 --tls-cert 证书.pem --tls-key 私钥.pem`。Docker 部署请保持默认的本机监听和 HTTPS 反向代理，不要直接发布应用端口。

## 上线验收

仓库 CI 会在 Docker 中自动检查：精确到单个 IP 的可信反向代理、HTTPS 登录和安全 Cookie、开启并再次使用两步验证登录，以及停机备份到空卷后恢复账号、两步验证、服务器记录和密钥文件。单元测试还会用本地模拟 SSH 检查执行日志、服务器端 `rollback.sh` 和一键回滚。它们不能代替下面的真实环境验收。

首次上线或更换代理、数据目录、运行用户后，在一台可丢弃的测试服务器上完成以下检查：

| 项目 | 真实环境通过标准 |
|---|---|
| 反向代理与 HTTPS | 公网 HTTP 会跳转到 HTTPS，浏览器证书链有效；`/api/auth/state` 返回 `"https":true`。从外网伪造 `X-Real-IP`、`X-Forwarded-For`、`X-Forwarded-Proto` 后故意登录失败，操作记录仍显示测试机的真实公网 IP，且 Cookie 带 `Secure`、`HttpOnly`、`SameSite=Strict`。 |
| 登录与两步验证 | 开启两步验证前必须再次输入当前密码；退出后用密码加 TOTP 能登录，错误 TOTP 不能登录；服务器上的 `reset-password` 能恢复账号并退出已有会话。 |
| SSH 与权限 | 用计划中的实际账号（包括非 root 加 `sudo -n`）测试连接、识别环境；确认主机密钥后重连成功。需要 AI 自由命令时，另测 `systemd-run` 和五分钟保险。 |
| 执行日志与回滚 | 在可丢弃服务器上执行一个标明可回滚的脚本操作。执行日志应先出现“运行中”再给出完整命令和结果；服务器备份目录应有 `rollback.sh`。点回滚后确认状态确实恢复、日志互相引用，文件改名为 `rollback.sh.done`，再次回滚被拒绝。 |
| 备份与恢复 | 先产生一条可回滚执行日志并保存一个可用的 SSH 密钥，再按上文停机备份。恢复到另一台机器或新空卷后，用原账号和 TOTP 登录，测试 SSH 密钥、服务器列表、执行日志和回滚信息；校验和与 `secrets.json` 的 `0600` 权限必须保持。 |
| 面板与云资源 | 如实际使用 1Panel、宝塔或腾讯云，在专用测试站点/实例上各执行一次可逆变更并回滚；核对面板/云控制台的最终状态、Miao Panel 执行日志和审计记录一致。不要用生产资源做首次演练。 |

把日期、版本或提交号、代理地址、测试资源、日志 ID、备份校验和和结果记入上线记录。任何一项未通过，都不要把该版本当作已完成生产验收。

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
