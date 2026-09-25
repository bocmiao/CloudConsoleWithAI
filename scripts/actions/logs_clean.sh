# logs.clean: free disk space taken by old logs.  args: none
used_kb() { df -Pk / | awk 'NR == 2 {print $3}'; }

check() {
  info "会做三件事：系统日志（journal）只保留最近 200MB；删除 /var/log 下 7 天前的已归档日志；清空超过 100MB 的 Docker 容器日志"
}

apply() {
  before=$(used_kb)
  if have journalctl; then
    journalctl --vacuum-size=200M >/dev/null 2>&1 && info "系统日志已精简到 200MB 以内"
  fi
  n=$(find /var/log -xdev -type f \( -name '*.gz' -o -name '*.[0-9]' -o -name '*.old' \) -mtime +7 -print -delete 2>/dev/null | wc -l)
  info "删除了 $n 个 7 天前的归档日志"
  for f in /var/lib/docker/containers/*/*-json.log; do
    [ -f "$f" ] || continue
    kb=$(du -k "$f" | cut -f1)
    if [ "$kb" -gt 102400 ]; then
      : >"$f" && info "清空了一个 $((kb / 1024))MB 的容器日志"
    fi
  done
  freed=$(((before - $(used_kb)) / 1024))
  [ "$freed" -lt 0 ] && freed=0
  result freed_mb "$freed"
  info "完成：大约释放了 ${freed}MB"
}

case "$MODE" in
check) check ;;
apply) apply ;;
undo) refuse "删除的旧日志无法恢复" ;;
esac
