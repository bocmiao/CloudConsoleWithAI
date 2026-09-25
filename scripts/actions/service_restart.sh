# service.restart: restart a systemd service.  args: <name>
NAME=$1

check() {
  case "$NAME" in '' | *[!A-Za-z0-9@._-]*) refuse "服务名不对：$NAME" ;; esac
  has_systemd || refuse "这台服务器没有使用 systemd，暂不支持重启服务"
  case "${NAME%.service}" in
  ssh | sshd | tat_agent | 1panel* | systemd-* | dbus | docker | containerd)
    refuse "$NAME 是关键服务（重启可能断开连接或影响所有容器），不允许在这里重启" ;;
  esac
  systemctl cat "$NAME" >/dev/null 2>&1 || refuse "找不到服务 $NAME"
}

apply() {
  info "正在重启 $NAME"
  if systemctl restart "$NAME" && sleep 3 && systemctl is-active --quiet "$NAME"; then
    info "完成：$NAME 已重启并正常运行"
    return
  fi
  info "重启后 $NAME 没有正常运行，最近的日志："
  journalctl -u "$NAME" -n 15 --no-pager 2>/dev/null | cut -c1-200 | sed 's/^/MIAO_INFO   /'
  exit 30
}

case "$MODE" in
check) check; info "检查通过：可以重启 $NAME" ;;
apply) check; apply ;;
undo) refuse "重启服务不需要撤销" ;;
esac
