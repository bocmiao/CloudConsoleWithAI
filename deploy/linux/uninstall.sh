#!/bin/sh
# 卸载 Miao Panel（喵面板）Web 版：停止并删除服务和程序。
#
#   sudo sh /opt/miaopanel/uninstall.sh            保留数据目录，重新安装后继续使用
#   sudo sh /opt/miaopanel/uninstall.sh --purge    连数据（数据库、密钥）和 miaopanel 用户一起删除，不能恢复
set -eu

NAME=miaopanel
BASE=/opt/miaopanel
DATA=$BASE/data
UNIT=/etc/systemd/system/$NAME.service
MARK=$BASE/.firewall

die() {
	printf '卸载失败：%s\n' "$*" >&2
	exit 1
}

purge=
case ${1:-} in
'') ;;
--purge) purge=1 ;;
-h | --help)
	sed -n '2,5p' "$0" | sed 's/^# \{0,1\}//'
	exit 0
	;;
*) die "不认识的参数 $1（只有 --purge）" ;;
esac
[ "$(id -u)" = 0 ] || die "需要 root 权限，请运行：sudo sh $0"

if [ -f "$UNIT" ]; then
	d=$(sed -n 's/^ExecStart=.* --data \([^ ]*\).*/\1/p' "$UNIT")
	[ -z "$d" ] || DATA=$d
	systemctl disable --now -q "$NAME" 2>/dev/null || true
	rm -f "$UNIT"
	rm -rf "/etc/systemd/system/$NAME.service.d"
	systemctl daemon-reload
fi

if [ -f "$MARK" ]; then
	read -r fw p <"$MARK" || true
	case $fw in
	ufw) ufw delete allow "$p/tcp" >/dev/null 2>&1 || true ;;
	firewalld) { firewall-cmd --permanent --remove-port="$p/tcp" && firewall-cmd --reload; } >/dev/null 2>&1 || true ;;
	esac
	rm -f "$MARK"
fi

mysql=
[ -f "$DATA/database.json" ] && grep -q '"kind": *"mysql"' "$DATA/database.json" && mysql=1
rm -rf "${BASE:?}/bin" "$BASE/install.sh" "$BASE/README.txt"
if [ -n "$purge" ]; then
	rm -rf "$DATA"
	userdel "$NAME" 2>/dev/null || true
	rm -f "$BASE/uninstall.sh"
	rmdir "$BASE" 2>/dev/null || true
	echo "Miao Panel 已经卸载，数据已删除。"
	[ -z "$mysql" ] || echo "MySQL 里的表没有删除，不需要了请在 MySQL 里自己删除那个库。"
else
	echo "Miao Panel 已经卸载。数据保留在 $DATA（数据库和密钥），重新安装会继续使用；"
	echo "不需要了可以运行：sudo sh $BASE/uninstall.sh --purge"
fi
