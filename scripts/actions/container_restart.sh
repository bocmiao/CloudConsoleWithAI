# container.restart: restart a Docker container.  args: <name>
NAME=$1

check() {
  case "$NAME" in '' | *[!A-Za-z0-9_.-]*) refuse "容器名不对：$NAME" ;; esac
  have docker || refuse "这台服务器没有安装 Docker"
  docker inspect --type container "$NAME" >/dev/null 2>&1 || refuse "找不到容器 $NAME"
}

state() { docker inspect -f '{{.State.Status}}/{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$NAME" 2>/dev/null; }

apply() {
  info "正在重启容器 $NAME"
  docker restart -t 30 "$NAME" >/dev/null 2>&1 || { info "重启命令执行失败"; exit 30; }
  i=0
  while [ "$i" -lt 30 ]; do
    case "$(state)" in
    running/ | running/healthy) info "完成：$NAME 已重启并正常运行"; return ;;
    esac
    sleep 2
    i=$((i + 1))
  done
  if [ "$(state)" = running/starting ]; then
    info "完成：$NAME 已重启，还在启动中（健康检查未结束）"
    return
  fi
  info "重启后 $NAME 没有正常运行（状态：$(state)），最近的日志："
  docker logs --tail 15 "$NAME" 2>&1 | cut -c1-200 | sed 's/^/MIAO_INFO   /'
  exit 30
}

case "$MODE" in
check) check; info "检查通过：可以重启容器 $NAME" ;;
apply) check; apply ;;
undo) refuse "重启容器不需要撤销" ;;
esac
