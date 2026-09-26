# nginx.realip: make Nginx take the visitor's IP from the request header
# EdgeOne adds on its way to this server, so access logs and websites see
# real visitors instead of EdgeOne's nodes. The header has a random name
# only EdgeOne knows (eo.clientip.header sets it there), so a request that
# reaches the server directly cannot pretend to be someone else; requests
# without the header keep their own address.
# args: <header>
HEADER=$1
NAME=00-miaopanel-realip.conf

# ngx runs Nginx: inside 1Panel's OpenResty container, or on the server.
ngx() {
  if [ -n "$CONTAINER" ]; then
    docker exec "$CONTAINER" "$BIN" "$@"
  else
    # shellcheck disable=SC2086 # CONF_ARGS is "-c <file>" or empty
    "$BIN" $CONF_ARGS "$@"
  fi
}

# locate finds Nginx and a folder it reads at the http level, where the
# websites' own "server { }" files are.
locate() {
  CONTAINER="" BIN="" DIR="" CONF_ARGS="" KIND=""
  if [ -n "${MIAO_NGINX_CONF:-}" ]; then # tests: a separate Nginx
    BIN=nginx CONF_ARGS="-c $MIAO_NGINX_CONF" DIR=$(dirname "$MIAO_NGINX_CONF")/conf.d KIND=nginx
    return
  fi
  # 1Panel: OpenResty runs in Docker; websites are in $OP_DIR/www/conf.d
  # (v2) or in the OpenResty app's conf/conf.d (v1).
  base=$(sed -n 's/^BASE_DIR=//p' /usr/local/bin/1pctl 2>/dev/null | head -n 1 | tr -d "\"' ")
  [ -z "$base" ] && [ -d /opt/1panel ] && base=/opt
  op=${base:+$base/1panel}
  if [ -n "$op" ] && [ -d "$op" ]; then
    if [ -d "$op/www/conf.d" ]; then
      DIR=$op/www/conf.d
    else
      n=$(ls -d "$op"/apps/openresty/*/conf/conf.d 2>/dev/null | wc -l)
      [ "$n" -gt 1 ] && refuse "发现多个 1Panel OpenResty，暂不支持自动选择"
      [ "$n" -eq 1 ] && DIR=$(ls -d "$op"/apps/openresty/*/conf/conf.d)
    fi
    if [ -n "$DIR" ]; then
      have docker || refuse "找不到 docker 命令，没法检查 1Panel 的 OpenResty"
      CONTAINER=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -i openresty | head -n 1)
      [ -n "$CONTAINER" ] || refuse "1Panel 的 OpenResty 没有在运行"
      BIN=nginx KIND=1panel
      docker exec "$CONTAINER" sh -c 'command -v nginx' >/dev/null 2>&1 || BIN=openresty
      return
    fi
  fi
  if [ -x /www/server/nginx/sbin/nginx ] && [ -d /www/server/panel/vhost/nginx ]; then
    BIN=/www/server/nginx/sbin/nginx DIR=/www/server/panel/vhost/nginx KIND=bt
    return
  fi
  have nginx || refuse "没有找到 Nginx（支持 1Panel 的 OpenResty、宝塔和系统安装的 Nginx）"
  BIN=nginx KIND=nginx
  for d in /etc/nginx/conf.d /usr/local/nginx/conf/conf.d; do
    [ -d "$d" ] && { DIR=$d; break; }
  done
  [ -n "$DIR" ] || refuse "没有找到 Nginx 的 conf.d 目录"
}

# others prints real_ip_header lines Nginx already has, other than ours.
others() {
  ngx -T 2>/dev/null | awk -v me="$NAME" '
    /^# configuration file / { f = $4; next }
    index(f, me) == 0 && /^[ \t]*real_ip_header[ \t]/ { sub(/:$/, "", f); print f ": " $0 }' | head -n 3
}

running() {
  if [ "$KIND" = 1panel ]; then return 0; fi
  ps -eo comm= 2>/dev/null | grep -qx nginx
}

reload() {
  RELOADED=
  if ! running; then
    info "Nginx 现在没有在运行，新配置会在它下次启动时生效"
    return 0
  fi
  RELOADED=1
  if [ -z "$CONTAINER" ] && [ -z "$CONF_ARGS" ] && [ "$KIND" = nginx ] && has_systemd && systemctl is-active --quiet nginx; then
    systemctl reload nginx
  else
    ngx -s reload >/dev/null 2>&1
  fi
}

check() {
  printf '%s' "$HEADER" | grep -Eq '^X-Miao-IP-[A-Za-z0-9]{16,40}$' || refuse "请求头名字不对"
  locate
  ngx -V 2>&1 | grep -q http_realip_module || refuse "这个 Nginx 没有 realip 模块，没法改用真实 IP"
  ngx -t >/dev/null 2>&1 || refuse "现有的 Nginx 配置本身就检查不通过，为了安全先不修改"
  o=$(others)
  [ -z "$o" ] || refuse "Nginx 里已经设置了 real_ip_header，为了不冲突没有修改：$o"
}

apply() {
  F=$DIR/$NAME
  if [ -f "$F" ] && grep -q "real_ip_header $HEADER;" "$F"; then
    info "已经设置好了：$F"
    exit 0
  fi
  undo_data file "$F"
  undo_data kind "$KIND"
  undo_data bin "$BIN"
  undo_data container "$CONTAINER"
  undo_data conf_args "$CONF_ARGS"
  if [ -e "$F" ]; then backup "$F" || refuse "无法备份 $F"; fi
  rollback() {
    info "出错了，正在恢复原来的配置"
    if [ -e "$(backup_name "$F")" ]; then restore "$F"; else rm -f "$F"; fi
    ngx -t >/dev/null 2>&1 || exit 30
    reload >/dev/null 2>&1 || exit 30
    exit 20
  }
  info "正在添加 $F：来自 EdgeOne 的请求用它带来的访客 IP"
  cat >"$F.miao-tmp" <<EOF || rollback
# Miao Panel：经过 EdgeOne 的请求，用 EdgeOne 回源时带上的访客 IP 作为访客地址，
# 访问日志和网站程序看到的就是真实访客，而不是 EdgeOne 的节点。
# 请求头的名字是随机的，只有 EdgeOne 知道；直接访问服务器的请求没有这个头，地址不变。
# 在 Miao Panel 里撤销，或者删除这个文件后重新加载 Nginx，就恢复原样。
set_real_ip_from 0.0.0.0/0;
set_real_ip_from ::/0;
real_ip_header $HEADER;
EOF
  chmod 644 "$F.miao-tmp" && mv -f "$F.miao-tmp" "$F" || { rm -f "$F.miao-tmp"; rollback; }
  if ! ngx -t >/dev/null 2>&1; then
    info "新配置没有通过 Nginx 检查：$(ngx -t 2>&1 | grep -iE 'emerg|error' | head -n 2 | tr '\n' ' ')"
    rollback
  fi
  ngx -T 2>/dev/null | grep -q "real_ip_header $HEADER;" || { info "Nginx 没有读取 $DIR 里的配置"; rollback; }
  reload || { info "重新加载 Nginx 失败"; rollback; }
  result file "$F"
  if [ -n "$RELOADED" ]; then
    info "完成：Nginx 已重新加载，经过 EdgeOne 的请求会记录真实访客 IP"
  else
    info "完成：配置已添加，Nginx 启动后经过 EdgeOne 的请求会记录真实访客 IP"
  fi
}

undo() {
  F=${UNDO_file:-}
  [ -n "$F" ] || { info "这一步当时没有做任何修改，不需要撤销"; exit 0; }
  KIND=${UNDO_kind:?} BIN=${UNDO_bin:?} CONTAINER=${UNDO_container:-} CONF_ARGS=${UNDO_conf_args:-}
  eval "saved=\${UNDO_backup_$(printf '%s' "$F" | tr -c 'A-Za-z0-9\n' '_'):-}"
  if [ -n "$saved" ]; then
    [ -f "$saved" ] || refuse "找不到备份文件，无法撤销"
    cp -a "$saved" "$F" || exit 30
    what="恢复了原来的 $F"
  else
    rm -f "$F" || exit 30
    what="删除了 $F"
  fi
  ngx -t >/dev/null 2>&1 || exit 30
  reload || exit 30
  if [ -n "$RELOADED" ]; then info "已撤销：$what，Nginx 已重新加载"; else info "已撤销：$what"; fi
}

case "$MODE" in
check) check; info "检查通过：将在 $DIR 添加 $NAME" ;;
apply) check; apply ;;
undo) undo ;;
esac
