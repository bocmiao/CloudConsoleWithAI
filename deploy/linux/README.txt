Miao Panel（喵面板）Web 版 · Linux 安装包
===========================================

在一台 Linux 服务器上运行 Miao Panel，用浏览器从任何地方登录使用。不需要 Docker，
也不需要先装数据库：默认用内置数据库，也可以在安装向导里改用 MySQL / MariaDB。

一、安装（需要 root，系统要有 systemd：Ubuntu、Debian、CentOS、Rocky、AlmaLinux、OpenCloudOS 等都可以）

    tar -xzf MiaoPanel-linux-amd64.tar.gz      （ARM 服务器是 linux-arm64）
    cd miaopanel
    sudo sh install.sh

装好后屏幕上会显示访问地址和初始化码。在浏览器打开 http://服务器IP:18765 ，安装向导分三步：

    1. 输入初始化码（证明你能登录这台服务器；也在 /opt/miaopanel/data/setup-code 里）
    2. 选择数据库：
         内置数据库（推荐）——什么都不用准备，数据在 /opt/miaopanel/data
         MySQL / MariaDB ——填地址、端口、库名、用户名和密码，可以先「测试连接」。
                            需要 MySQL 5.7+ 或 MariaDB 10.3+，库要先建好（字符集 utf8mb4），
                            账号要能在这个库里建表。1Panel、宝塔的「数据库」页面都能建库。
    3. 创建管理员账号，然后就能用了。

云服务器还要在控制台的安全组 / 防火墙放行 TCP 18765 端口（这台服务器自己开着 ufw 或 firewalld 时，
安装脚本已经放行）。换端口：sudo sh install.sh --port 8080

二、加上 HTTPS（长期使用一定要做）

现在是 HTTP，登录密码会明文传输。Miao Panel 保存着你所有服务器的密码和云账号密钥，请：

    1. 在 1Panel、宝塔、Nginx 或 Caddy 里新建一个网站，把域名反向代理到 http://127.0.0.1:18765 ，申请 HTTPS 证书；
    2. 运行 sudo sh /opt/miaopanel/install.sh --local ，让 Miao Panel 只接受本机（反向代理）的连接；
    3. 登录后在「设置 → 账号与安全」开启两步验证。

反向代理要加的几行配置见：https://github.com/bocmiao/CloudConsoleWithAI/blob/HEAD/docs/DEPLOY.md

三、日常

    systemctl status miaopanel          查看状态
    journalctl -u miaopanel -f          查看日志
    sudo systemctl restart miaopanel    重启

    升级：在「设置 → 版本和诊断」点「更新到 vX」即可；也可以下载新的安装包，解压后再运行一次 sudo sh install.sh
    忘记密码：sudo -u miaopanel /opt/miaopanel/bin/miaopanel reset-password --data /opt/miaopanel/data
    备份：sudo systemctl stop miaopanel
          sudo sh -c 'umask 077; tar -C /opt/miaopanel/data -czf /root/miaopanel-backup.tar.gz .'
          sudo systemctl start miaopanel
          （数据目录里有服务器和云服务的密钥，备份要妥善保管；用 MySQL 时再用 mysqldump 备份那个库）
    卸载：sudo sh /opt/miaopanel/uninstall.sh            （保留数据）
          sudo sh /opt/miaopanel/uninstall.sh --purge    （连数据一起删除）

四、不想装成服务，只想先试试

    ./miaopanel serve --listen 0.0.0.0:18765 --data ./data

按 Ctrl+C 停止。数据在当前目录的 data 里。

项目主页和文档：https://github.com/bocmiao/CloudConsoleWithAI
