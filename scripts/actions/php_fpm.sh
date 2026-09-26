# php_fpm.set: tune the PHP-FPM process manager (plain Linux installs).
# args: <max_children> [dynamic|ondemand|static]
MAX=$1
PM=${2:-dynamic}

find_pools() {
  for f in /etc/php/*/fpm/pool.d/www.conf /etc/php-fpm.d/www.conf /opt/remi/php*/root/etc/php-fpm.d/www.conf; do
    [ -f "$f" ] && echo "$f"
  done
}

locate() {
  n=$(find_pools | wc -l)
  [ "$n" -eq 1 ] || { [ "$n" -eq 0 ] && refuse "没有找到 PHP-FPM 的配置文件"; refuse "发现多个 PHP 版本，暂不支持自动选择"; }
  POOL=$(find_pools)
  case "$POOL" in
  /etc/php/*)
    VER=$(echo "$POOL" | cut -d/ -f4)
    BIN=php-fpm$VER
    SVC=php$VER-fpm ;;
  /opt/remi/*)
    # Remi's software collections: /opt/remi/php81/root/..., service php81-php-fpm.
    VER=$(echo "$POOL" | cut -d/ -f4)
    BIN=/opt/remi/$VER/root/usr/sbin/php-fpm
    SVC=$VER-php-fpm ;;
  *)
    BIN=php-fpm
    SVC=php-fpm ;;
  esac
  have "$BIN" || refuse "找不到 $BIN 命令"
}

check() {
  is_uint "$MAX" && [ "$MAX" -ge 2 ] && [ "$MAX" -le 500 ] || refuse "max_children 需要在 2 到 500 之间"
  case "$PM" in dynamic | ondemand | static) ;; *) refuse "pm 只能是 dynamic、ondemand 或 static" ;; esac
  locate
  "$BIN" -t >/dev/null 2>&1 || refuse "现有的 PHP-FPM 配置本身就检查不通过，为了安全先不修改"
}

# set_key replaces "key = value" (also a commented default) or appends it.
set_key() {
  re=$(printf '%s' "$1" | sed 's/\./\\./g')
  if grep -Eq "^[;[:space:]]*$re[[:space:]]*=" "$POOL"; then
    sed -i -E "s|^[;[:space:]]*$re[[:space:]]*=.*|$1 = $2|" "$POOL"
  else
    echo "$1 = $2" >>"$POOL"
  fi
}

# The master's process name starts with php-fpm; matching on the name (not
# the command line) avoids signalling some other process by mistake.
master_pid() { ps -eo pid=,comm=,args= | awk '$2 ~ /^php-fpm/ && /master process/ {print $1; exit}'; }

reload() {
  if has_systemd && systemctl cat "$SVC" >/dev/null 2>&1; then
    systemctl reload "$SVC" 2>/dev/null || systemctl restart "$SVC"
  else
    pid=$(master_pid)
    [ -n "$pid" ] && kill -USR2 "$pid"
  fi
}

healthy() {
  sleep 2
  if has_systemd && systemctl cat "$SVC" >/dev/null 2>&1; then
    systemctl is-active --quiet "$SVC"
  else
    [ -n "$(master_pid)" ]
  fi
}

apply() {
  backup "$POOL" || refuse "无法备份 $POOL"
  undo_data pool "$POOL"
  undo_data service "$SVC"
  undo_data bin "$BIN"
  rollback() {
    info "出错了，正在恢复原来的配置"
    restore "$POOL"
    reload
    healthy || exit 30
    exit 20
  }
  info "正在修改 $POOL：pm = $PM，pm.max_children = $MAX"
  set_key pm "$PM"
  set_key pm.max_children "$MAX"
  if [ "$PM" = dynamic ]; then
    min=$((MAX / 8)); [ "$min" -lt 1 ] && min=1
    maxs=$((MAX / 2)); [ "$maxs" -le "$min" ] && maxs=$((min + 1))
    start=$((MAX / 4)); [ "$start" -lt "$min" ] && start=$min; [ "$start" -gt "$maxs" ] && start=$maxs
    set_key pm.start_servers "$start"
    set_key pm.min_spare_servers "$min"
    set_key pm.max_spare_servers "$maxs"
  elif [ "$PM" = ondemand ]; then
    set_key pm.process_idle_timeout 10s
  fi
  "$BIN" -t >/dev/null 2>&1 || { info "新配置没有通过检查"; rollback; }
  reload || rollback
  healthy || { info "重载后 PHP-FPM 没有正常运行"; rollback; }
  info "完成：PHP-FPM 已平滑重载，新配置已生效"
}

undo() {
  POOL=${UNDO_pool:?}
  SVC=${UNDO_service:?}
  BIN=${UNDO_bin:?}
  eval "saved=\${UNDO_backup_$(printf '%s' "$POOL" | tr -c 'A-Za-z0-9\n' '_'):-}"
  [ -n "$saved" ] && [ -f "$saved" ] || refuse "找不到备份文件，无法撤销"
  cp -a "$saved" "$POOL" || exit 30
  "$BIN" -t >/dev/null 2>&1 || exit 30
  reload
  healthy || exit 30
  info "已撤销：恢复了原来的 PHP-FPM 配置"
}

case "$MODE" in
check) check; info "检查通过：将修改 $POOL" ;;
apply) check; apply ;;
undo) undo ;;
esac
