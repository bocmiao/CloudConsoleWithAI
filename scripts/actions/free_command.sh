# free_command: run an AI-written command that Miao Panel has already
# checked (docs/DESIGN.md 7.5). The command itself is never trusted to
# protect anything: this script backs up every declared file, arms a
# restore that fires by itself after 5 minutes unless Miao Panel confirms
# the server is fine, checks the declared services afterwards, and restores
# at once when anything fails.
#
# args (each base64, "-" for empty):
#   <command> <files, one per line> <services, one per line> <check url> <expected file states>
# modes:
#   dryrun  run the command in a private mount namespace where the declared
#           files' directories are overlay layers and service reloads are
#           only recorded; print the before/after diff. Changes nothing.
#   apply   run it for real
#   undo    put the files back and reload the services

unb64() { [ "$1" = "-" ] || printf '%s' "$1" | base64 -d 2>/dev/null; }
have base64 || refuse "服务器缺少 base64 命令"
WORK=$(mktemp -d /tmp/miaopanel-free.XXXXXXXX) || refuse "无法创建临时目录"
trap 'rm -rf "$WORK"' EXIT
unb64 "$1" >"$WORK/cmd.sh"
unb64 "$2" >"$WORK/files"
unb64 "$3" >"$WORK/services"
CHECK_URL=$(unb64 "$4")
unb64 "$5" >"$WORK/expect"
# Lists end with a newline, so read sees their last line.
for l in files services expect; do echo >>"$WORK/$l"; done

key() { printf '%s' "$1" | tr '/' '_'; }
# state prints what a file is now: "<cksum> <size>", "link <target>" or "absent -".
state() {
  if [ -L "$1" ]; then
    printf 'link %s' "$(readlink "$1" | tr ' ' '_')"
  elif [ -e "$1" ]; then
    cksum <"$1" | awk '{printf "%s %s", $1, $2}'
  else
    printf 'absent -'
  fi
}
run_cmd() {
  if have timeout; then timeout "$1" sh "$WORK/cmd.sh"; else sh "$WORK/cmd.sh"; fi
}

dryrun() {
  if ! have unshare || ! grep -qw overlay /proc/filesystems 2>/dev/null; then
    echo "MIAO_DRY unsupported 这台服务器不支持隔离试运行（缺少 unshare 命令或 overlay 文件系统）"
    return
  fi
  mkdir -p "$WORK/orig" "$WORK/new" "$WORK/bin" "$WORK/layers"
  : >"$WORK/dirs.all"
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    echo "MIAO_HASH $(state "$f") $f"
    k=$(key "$f")
    if [ -L "$f" ]; then
      readlink "$f" >"$WORK/orig/$k.link"
    elif [ -e "$f" ]; then
      cp -p "$f" "$WORK/orig/$k"
    fi
    d=$(dirname "$f")
    while [ ! -d "$d" ]; do d=$(dirname "$d"); done
    if [ "$d" = / ]; then
      echo "MIAO_DRY unsupported $f 所在的目录还不存在，没法在隔离层里试运行"
      return
    fi
    echo "$d" >>"$WORK/dirs.all"
  done <"$WORK/files"
  # Layer only the outermost directories.
  sort -u "$WORK/dirs.all" | awk '{ for (p in kept) if (index($0, p "/") == 1) next; kept[$0] = 1; print }' >"$WORK/dirs"

  # Service control is recorded, never done; everything else runs for
  # real, so "nginx -t" checks the new configuration.
  for n in systemctl service nginx openresty docker apachectl apache2ctl; do
    cat >"$WORK/bin/$n" <<EOF
#!/bin/sh
record() { echo "$n \$*" >>"$WORK/services.log"; exit 0; }
case "$n" in
systemctl) case "\$1" in reload|restart|try-restart|reload-or-restart|try-reload-or-restart|start|stop) record "\$@";; esac ;;
service) case "\$2" in reload|restart|force-reload|start|stop) record "\$@";; esac ;;
nginx|openresty) case " \$* " in *" -s "*) record "\$@";; esac ;;
docker) case "\$1" in restart|start|stop) record "\$@";; esac ;;
apachectl|apache2ctl) case "\$1" in graceful|restart|start|stop|-k) record "\$@";; esac ;;
esac
PATH="$PATH" exec $n "\$@"
EOF
    chmod 755 "$WORK/bin/$n"
  done
  cat >"$WORK/inner.sh" <<'EOF'
W=$1
i=0
while IFS= read -r d; do
  i=$((i + 1))
  mkdir -p "$W/layers/u$i" "$W/layers/w$i"
  mount -t overlay overlay -o "lowerdir=$d,upperdir=$W/layers/u$i,workdir=$W/layers/w$i" "$d" || exit 97
  echo "$i $d" >>"$W/layers/map"
done <"$W/dirs"
PATH="$W/bin:$PATH"
export PATH
if command -v timeout >/dev/null 2>&1; then timeout 60 sh "$W/cmd.sh" >"$W/out" 2>&1; else sh "$W/cmd.sh" >"$W/out" 2>&1; fi
echo $? >"$W/rc"
while IFS= read -r f; do
  [ -n "$f" ] || continue
  k=$(printf '%s' "$f" | tr '/' '_')
  if [ -L "$f" ]; then readlink "$f" >"$W/new/$k.link"; elif [ -e "$f" ]; then cp -p "$f" "$W/new/$k"; fi
done <"$W/files"
EOF
  unshare -m --propagation private sh "$WORK/inner.sh" "$WORK" >"$WORK/inner.log" 2>&1
  code=$?
  if [ "$code" -ne 0 ] || [ ! -f "$WORK/rc" ]; then
    echo "MIAO_DRY unsupported 没能建立隔离的文件层（$(tr '\n' ' ' <"$WORK/inner.log" | cut -c1-200)）"
    return
  fi
  echo "MIAO_DRY ok"
  echo "MIAO_DRY_RC $(cat "$WORK/rc")"
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    k=$(key "$f")
    echo "MIAO_FILE $f"
    if [ -f "$WORK/orig/$k.link" ] || [ -f "$WORK/new/$k.link" ]; then
      echo "MIAO_D 软链接：$(cat "$WORK/orig/$k.link" 2>/dev/null || echo 无) → $(cat "$WORK/new/$k.link" 2>/dev/null || echo 已删除)"
      continue
    fi
    old=$WORK/orig/$k
    new=$WORK/new/$k
    [ -e "$old" ] || old=/dev/null
    [ -e "$new" ] || new=/dev/null
    if have diff; then
      diff -u -L "修改前" -L "修改后" "$old" "$new" | head -400 | sed 's/^/MIAO_D /'
    elif ! cmp -s "$old" "$new"; then
      echo "MIAO_D （服务器上没有 diff 命令，只能确认内容有变化）"
    fi
  done <"$WORK/files"
  [ -f "$WORK/services.log" ] && sed 's/^/MIAO_SVC /' "$WORK/services.log"
  # Anything written in the layers that was not declared.
  while read -r i d; do
    (cd "$WORK/layers/u$i" && find . -mindepth 1 ! -type d) | while IFS= read -r rel; do
      p="$d/${rel#./}"
      grep -qxF "$p" "$WORK/files" || echo "MIAO_EXTRA $p"
    done
  done <"$WORK/layers/map"
  head -200 "$WORK/out" | sed 's/^/MIAO_O /'
}

# cancel_guard <guard> <backup dir>: the flag file is what counts; stopping
# the timer or the waiting process just tidies up.
cancel_guard() {
  [ -n "$2" ] && touch "$2/guard.cancelled"
  case "$1" in
  unit:*) systemctl stop "${1#unit:}.timer" >/dev/null 2>&1 ;;
  pid:*) kill "${1#pid:}" 2>/dev/null ;;
  esac
}

restore_now() {
  info "$1，正在恢复修改前的文件"
  sh "$BACKUP_DIR/restore.sh" && info "已恢复原状" || info "恢复时有问题，备份在 $BACKUP_DIR"
  cancel_guard "$guard" "$BACKUP_DIR"
  exit 20
}

apply() {
  while read -r a b f; do
    [ -n "$f" ] || continue
    [ "$(state "$f")" = "$a $b" ] || refuse "$f 在试运行之后被改动过，为了安全不执行，请让 AI 重新生成这一步"
  done <"$WORK/expect"
  mkdir -p "$BACKUP_DIR/files" || refuse "无法创建备份目录"
  : >"$BACKUP_DIR/manifest"
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    k=$(key "$f")
    if [ -e "$f" ] || [ -L "$f" ]; then
      cp -a "$f" "$BACKUP_DIR/files/$k" || refuse "无法备份 $f"
      echo "present $f" >>"$BACKUP_DIR/manifest"
    else
      echo "absent $f" >>"$BACKUP_DIR/manifest"
    fi
  done <"$WORK/files"
  cp "$WORK/services" "$BACKUP_DIR/services"
  # The restore script is written by Miao Panel, not by the AI.
  cat >"$BACKUP_DIR/restore.sh" <<EOF
#!/bin/sh
# Miao Panel：把一次 AI 自由命令改动的文件恢复原样，并重新加载相关服务。
# 用 root 执行：sh $BACKUP_DIR/restore.sh
B=$BACKUP_DIR
# The 5-minute guard runs this with "guard"; Miao Panel cancels it by
# leaving guard.cancelled once it has confirmed the server is fine.
if [ "\${1:-}" = guard ] && [ -e "\$B/guard.cancelled" ]; then exit 0; fi
while read -r what f; do
  [ -n "\$f" ] || continue
  k=\$(printf '%s' "\$f" | tr '/' '_')
  rm -f "\$f"
  if [ "\$what" = present ]; then cp -a "\$B/files/\$k" "\$f"; fi
done <"\$B/manifest"
while IFS= read -r s; do
  [ -n "\$s" ] || continue
  case "\$s" in
  docker:*) docker restart "\${s#docker:}" >/dev/null 2>&1 ;;
  *)
    if command -v systemctl >/dev/null 2>&1 && systemctl cat "\$s" >/dev/null 2>&1; then
      systemctl reload-or-restart "\$s"
    elif [ "\$s" = nginx ] && command -v nginx >/dev/null 2>&1; then
      nginx -s reload
    else
      service "\$s" restart
    fi >/dev/null 2>&1 ;;
  esac
done <"\$B/services"
echo "\$(date '+%Y-%m-%d %H:%M:%S') 已恢复" >>"\$B/restored"
EOF
  undo_data restore "$BACKUP_DIR/restore.sh"
  guard=""
  unit=miaopanel-guard-$(basename "$BACKUP_DIR" | tr -cd 'A-Za-z0-9-')
  if has_systemd && have systemd-run &&
    systemd-run --unit="$unit" --on-active=300 --description="Miao Panel 5 分钟保险" /bin/sh "$BACKUP_DIR/restore.sh" guard >/dev/null 2>&1; then
    guard="unit:$unit"
  else
    setsid sh -c "echo \$\$ >'$BACKUP_DIR/guard.pid'; sleep 300; sh '$BACKUP_DIR/restore.sh' guard" >/dev/null 2>&1 </dev/null &
    sleep 1
    guard="pid:$(cat "$BACKUP_DIR/guard.pid" 2>/dev/null)"
  fi
  undo_data guard "$guard"
  info "已备份要修改的文件，并设好 5 分钟保险：如果之后 Miao Panel 连不上服务器，服务器会自己恢复原状"

  info "正在执行"
  run_cmd 120 >"$WORK/out" 2>&1
  rc=$?
  head -100 "$WORK/out" | sed 's/^/  /'
  [ "$rc" -eq 0 ] || restore_now "命令执行失败（退出码 $rc）"

  while IFS= read -r s; do
    [ -n "$s" ] || continue
    case "$s" in
    docker:*)
      [ "$(docker inspect -f '{{.State.Running}}' "${s#docker:}" 2>/dev/null)" = true ] || restore_now "容器 ${s#docker:} 没有在运行"
      ;;
    *)
      if has_systemd && systemctl cat "$s" >/dev/null 2>&1; then
        systemctl is-active --quiet "$s" || restore_now "服务 $s 没有正常运行"
      fi
      ;;
    esac
  done <"$WORK/services"
  if [ -n "$CHECK_URL" ] && have curl; then
    code=$(curl -s -o /dev/null -m 10 --noproxy '*' -w '%{http_code}' "$CHECK_URL")
    case "$code" in 000 | 5*) restore_now "访问 $CHECK_URL 失败（$code）" ;; esac
    info "访问 $CHECK_URL 正常（$code）"
  fi
  info "执行成功，检查通过"
}

undo() {
  r=${UNDO_restore:-}
  [ -n "$r" ] && [ -f "$r" ] || refuse "找不到修改前的备份，无法回滚"
  cancel_guard "${UNDO_guard:-}" "$(dirname "$r")"
  sh "$r" || exit 30
  info "已撤销：文件恢复成修改前的样子，并重新加载了相关服务"
}

case "$MODE" in
dryrun) dryrun ;;
apply) apply ;;
undo) undo ;;
*) refuse "不支持的操作 $MODE" ;;
esac
