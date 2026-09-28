#!/bin/sh
# Miao Panel（喵面板）Web 版安装脚本：装成 systemd 服务，开机自动启动，不需要 Docker。
#
#   sudo sh install.sh                  监听 18765 端口，浏览器直接访问 http://服务器IP:18765
#   sudo sh install.sh --port 8080      换一个端口
#   sudo sh install.sh --local          只让本机访问，由 1Panel、宝塔、Nginx、Caddy 反向代理并提供 HTTPS
#
# 再次运行就是升级：换上这个安装包里的程序并重启，数据、端口和设置保留。
# 程序在 /opt/miaopanel/bin，数据在 /opt/miaopanel/data，以 miaopanel 用户运行。
set -eu

NAME=miaopanel
BASE=/opt/miaopanel
BIN=$BASE/bin
DATA=$BASE/data
UNIT=/etc/systemd/system/$NAME.service
MARK=$BASE/.firewall # the port this script opened, to close it again

say() { printf '%s\n' "$*"; }
die() {
	printf '\n安装失败：%s\n' "$*" >&2
	exit 1
}

port=
host=
while [ $# -gt 0 ]; do
	case $1 in
	--port)
		[ $# -ge 2 ] || die "--port 后面要写端口号"
		port=$2
		shift 2
		;;
	--port=*)
		port=${1#--port=}
		shift
		;;
	--local)
		host=127.0.0.1
		shift
		;;
	--public)
		host=0.0.0.0
		shift
		;;
	-h | --help)
		sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*) die "不认识的参数 $1（可以用 --port 端口、--local）" ;;
	esac
done

[ "$(id -u)" = 0 ] || die "需要 root 权限，请运行：sudo sh install.sh"
here=$(cd "$(dirname "$0")" && pwd)
# From the unpacked package it installs its program; run as
# $BASE/install.sh it only changes the settings of the installed one.
src=
if [ -f "$here/$NAME" ]; then
	src=$here/$NAME
	chmod 755 "$src"
	new=$("$src" version 2>/dev/null) ||
		die "这个安装包和服务器的 CPU 不匹配（这台服务器是 $(uname -m)）：x86_64 用 linux-amd64，aarch64 用 linux-arm64"
elif [ "$here" = "$BASE" ] && [ -f "$BIN/$NAME" ]; then
	new=
else
	die "安装包不完整：没有找到 $here/$NAME，请重新解压"
fi
if ! command -v systemctl >/dev/null 2>&1 || [ ! -d /run/systemd/system ]; then
	die "这台服务器没有 systemd。可以直接运行程序：
  cd $here && ./$NAME serve --listen 0.0.0.0:18765 --data ./data
或者用 Docker 部署，见 https://github.com/bocmiao/CloudConsoleWithAI/blob/HEAD/docs/DEPLOY.md"
fi

# An earlier installation keeps its address, data and time zone.
tz=Asia/Shanghai
if [ -f "$UNIT" ]; then
	old=$(sed -n 's/^ExecStart=.* --listen \([^ ]*\).*/\1/p' "$UNIT")
	[ -n "$port" ] || port=${old##*:}
	[ -n "$host" ] || host=${old%:*}
	d=$(sed -n 's/^ExecStart=.* --data \([^ ]*\).*/\1/p' "$UNIT")
	[ -z "$d" ] || DATA=$d
	z=$(sed -n 's/^Environment=TZ=//p' "$UNIT")
	[ -z "$z" ] || tz=$z
fi
[ -n "$port" ] || port=18765
[ -n "$host" ] || host=0.0.0.0
case $port in '' | *[!0-9]*) die "端口 $port 不对，要是 1 到 65535 之间的数字" ;; esac
if [ "$port" -lt 1 ] || [ "$port" -gt 65535 ]; then die "端口 $port 不对，要是 1 到 65535 之间的数字"; fi

if [ -n "$src" ]; then say "正在安装 Miao Panel $new ……"; else say "正在修改 Miao Panel 的设置……"; fi

if ! id "$NAME" >/dev/null 2>&1; then
	nologin=$(command -v nologin || echo /sbin/nologin)
	if command -v useradd >/dev/null 2>&1; then
		useradd -r -U -d "$BASE" -M -s "$nologin" "$NAME"
	else
		adduser --system --group --home "$BASE" --no-create-home --shell "$nologin" "$NAME"
	fi
fi

# The program's directory belongs to the service, so that the settings
# page can update it; the rest stays root's.
mkdir -p "$BASE" "$BIN" "$DATA"
chown root:root "$BASE"
chmod 755 "$BASE"
if [ -n "$src" ]; then
	cp "$src" "$BIN/.$NAME.new"
	chmod 755 "$BIN/.$NAME.new"
	mv -f "$BIN/.$NAME.new" "$BIN/$NAME" # a running program is replaced, not overwritten
fi
for f in install.sh uninstall.sh README.txt; do
	if [ -f "$here/$f" ] && [ "$here" != "$BASE" ]; then
		cp "$here/$f" "$BASE/$f"
		chmod 644 "$BASE/$f"
	fi
done
chown -R "$NAME:$NAME" "$BIN" "$DATA"
chmod 755 "$BIN"
chmod 700 "$DATA"

cat >"$UNIT" <<EOF
# 由 $BASE 的 install.sh 生成，重新运行 install.sh 时会重写。
# 要加自己的设置（比如 Environment=MIAO_TRUSTED_PROXIES=...），请用 sudo systemctl edit $NAME。
[Unit]
Description=Miao Panel（喵面板）Web 版
After=network-online.target
Wants=network-online.target

[Service]
User=$NAME
Group=$NAME
ExecStart=$BIN/$NAME serve --data $DATA --listen $host:$port
Environment=TZ=$tz
UMask=0077
Restart=on-failure
RestartSec=3
# Miao Panel 只需要网络、自己的数据目录，和为了在设置里一键更新而写入的程序目录。
ReadWritePaths=$BIN $DATA
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes

[Install]
WantedBy=multi-user.target
EOF

# The firewall on the server itself: open the port when others are to
# reach it, close what an earlier run opened when that has changed.
close_port() {
	[ -f "$MARK" ] || return 0
	read -r fw p <"$MARK" || true
	case $fw in
	ufw) ufw delete allow "$p/tcp" >/dev/null 2>&1 || true ;;
	firewalld) { firewall-cmd --permanent --remove-port="$p/tcp" && firewall-cmd --reload; } >/dev/null 2>&1 || true ;;
	esac
	rm -f "$MARK"
}
opened=
if [ -f "$MARK" ] && { [ "$host" = 127.0.0.1 ] || [ "$(cut -d' ' -f2 "$MARK")" != "$port" ]; }; then
	close_port
fi
if [ "$host" != 127.0.0.1 ] && [ ! -f "$MARK" ]; then
	if command -v ufw >/dev/null 2>&1 && LC_ALL=C ufw status 2>/dev/null | grep -q '^Status: active'; then
		ufw allow "$port/tcp" >/dev/null && echo "ufw $port" >"$MARK" && opened=ufw
	elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
		firewall-cmd --permanent --add-port="$port/tcp" >/dev/null && firewall-cmd --reload >/dev/null &&
			echo "firewalld $port" >"$MARK" && opened=firewalld
	fi
fi

systemctl daemon-reload
systemctl enable -q "$NAME"
systemctl restart "$NAME"

# Wait for it: a new installation writes its setup code once it listens.
code=
i=0
while [ $i -lt 20 ]; do
	sleep 1
	i=$((i + 1))
	if ! systemctl is-active --quiet "$NAME"; then
		continue
	fi
	if [ -f "$DATA/setup-code" ]; then
		code=$(cat "$DATA/setup-code")
		break
	fi
	if [ $i -ge 3 ] && { [ -f "$DATA/database.json" ] || [ -f "$DATA/miaopanel.db" ]; }; then
		break # installed before, with its administrator
	fi
done
if ! systemctl is-active --quiet "$NAME"; then
	journalctl -u "$NAME" -n 30 --no-pager 2>/dev/null || true
	die "服务没有启动起来，上面是它的日志（也可以运行 journalctl -u $NAME 查看）"
fi

ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')
[ -n "$ip" ] || ip=$(hostname -I 2>/dev/null | cut -d' ' -f1)
[ -n "$ip" ] || ip=服务器IP
[ "$host" = 127.0.0.1 ] && ip=127.0.0.1

say ""
say "==== Miao Panel ${new:+$new }已经安装并启动 ===="
say ""
say "  访问地址：http://$ip:$port"
if [ "$host" != 127.0.0.1 ]; then
	say "  （云服务器请换成控制台里的公网 IP，并在安全组 / 防火墙里放行 TCP $port 端口）"
	[ -z "$opened" ] || say "  已在这台服务器的防火墙（$opened）放行 $port 端口。"
else
	say "  （只有本机能访问：请在 1Panel、宝塔、Nginx 或 Caddy 里把一个域名反向代理到 http://127.0.0.1:$port 并开启 HTTPS）"
fi
if [ -n "$code" ]; then
	say ""
	say "  初始化码：$code"
	say ""
	say "  在浏览器打开上面的地址，按安装向导输入初始化码、选择数据库（内置或 MySQL）、创建管理员账号。"
else
	say "  已经安装过，用原来的账号登录即可。"
fi
if [ "$host" != 127.0.0.1 ]; then
	say ""
	say "  注意：现在是 HTTP，密码会明文传输。长期使用请配一个带 HTTPS 的域名反向代理，"
	say "  然后运行 sudo sh $BASE/install.sh --local 只让本机访问（见 $BASE/README.txt）。"
fi
say ""
say "  常用命令："
printf '    %-38s%s\n' "systemctl status $NAME" 查看状态 "journalctl -u $NAME -f" 查看日志 \
	"sudo systemctl restart $NAME" 重启 "sudo sh $BASE/uninstall.sh" 卸载（保留数据）
say "  数据目录：$DATA（数据库、服务器和云服务的密钥都在这里，请定期备份）"
