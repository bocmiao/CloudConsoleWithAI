# ssh.harden: only called through a verified non-root key connection.
# The script itself refuses unsafe SSH configurations and arms a five-minute
# restore before modifying or reloading sshd.
LOGIN_USER=$1
FILE=/etc/ssh/sshd_config.d/00-miaopanel-hardening.conf

sshd_bin() {
  for b in /usr/sbin/sshd /usr/local/sbin/sshd; do [ -x "$b" ] && { echo "$b"; return; }; done
  command -v sshd
}

service_name() {
  if has_systemd; then
    for s in ssh sshd; do systemctl is-active --quiet "$s" && { echo "$s"; return; }; done
  else
    for s in ssh sshd; do service "$s" status >/dev/null 2>&1 && { echo "$s"; return; }; done
  fi
}

reload_ssh() {
  if has_systemd; then systemctl reload "$SERVICE"; else service "$SERVICE" reload; fi
}

cancel_guard() {
  touch "$BACKUP_DIR/guard.cancelled"
  case "$1" in
    unit:*) systemctl stop "${1#unit:}.timer" >/dev/null 2>&1 || : ;;
    pid:*) kill "${1#pid:}" 2>/dev/null || : ;;
  esac
}

check() {
  printf '%s' "$LOGIN_USER" | grep -Eq '^[A-Za-z_][A-Za-z0-9_-]{0,63}$' || refuse "SSH 用户名不合法"
  [ "$LOGIN_USER" != root ] || refuse "必须先用非 root 密钥连接"
  [ -f /etc/ssh/sshd_config ] || refuse "找不到 OpenSSH 服务器配置"
  [ ! -L "$FILE" ] || refuse "$FILE 是软链接，拒绝修改"
  grep -Eq '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config.d/\*\.conf([[:space:]]|$)' /etc/ssh/sshd_config || refuse "主配置未包含 /etc/ssh/sshd_config.d/*.conf，不能安全地添加配置"
  SSHD=$(sshd_bin); [ -n "$SSHD" ] || refuse "找不到 sshd"
  "$SSHD" -t || refuse "现有 SSH 配置检查失败"
  SERVICE=$(service_name); [ -n "$SERVICE" ] || refuse "找不到正在运行的 SSH 服务"
}

restore_now() {
  info "$1，正在恢复 SSH 配置"
  if sh "$BACKUP_DIR/restore.sh"; then
    cancel_guard "$GUARD"
    info "已恢复原状"
    exit 20
  fi
  info "自动恢复失败，请用云控制台登录并运行 sh $BACKUP_DIR/restore.sh"
  exit 30
}

apply() {
  check
  mkdir -p "$BACKUP_DIR" || refuse "无法创建备份目录"
  if [ -e "$FILE" ]; then
    cp -a "$FILE" "$BACKUP_DIR/original" || refuse "无法备份已有配置"
    undo_data "backup:$FILE" "$BACKUP_DIR/original"
  fi
  cat >"$BACKUP_DIR/restore.sh" <<EOF
#!/bin/sh
B=$BACKUP_DIR
if [ "\${1:-}" = guard ] && [ -e "\$B/guard.cancelled" ]; then exit 0; fi
if [ -e "\$B/original" ]; then cp -a "\$B/original" "$FILE" || exit 1; else rm -f "$FILE" || exit 1; fi
"$SSHD" -t || exit 1
if [ -d /run/systemd/system ]; then systemctl reload "$SERVICE" || exit 1; else service "$SERVICE" reload || exit 1; fi
echo "\$(date '+%Y-%m-%d %H:%M:%S') restored" >>"\$B/restored"
EOF
  chmod 700 "$BACKUP_DIR/restore.sh"
  undo_data restore "$BACKUP_DIR/restore.sh"
  UNIT=miaopanel-guard-$(basename "$BACKUP_DIR" | tr -cd 'A-Za-z0-9-')
  if has_systemd && have systemd-run && systemd-run --unit="$UNIT" --on-active=300 /bin/sh "$BACKUP_DIR/restore.sh" guard >/dev/null 2>&1; then
    GUARD=unit:$UNIT
  elif have nohup; then
    nohup sh -c "sleep 300; sh '$BACKUP_DIR/restore.sh' guard" >/dev/null 2>&1 </dev/null &
    GUARD=pid:$!
  else
    refuse "无法设置 5 分钟自动恢复保险，配置未修改"
  fi
  undo_data guard "$GUARD"
  info "已备份配置并启动 5 分钟自动恢复保险"
  mkdir -p /etc/ssh/sshd_config.d || restore_now "无法创建配置目录"
  cat >"$FILE" <<'EOF'
# Managed by Miao Panel. Restore with the saved restore.sh if needed.
PasswordAuthentication no
KbdInteractiveAuthentication no
ChallengeResponseAuthentication no
PermitRootLogin no
EOF
  chmod 600 "$FILE"
  "$SSHD" -t || restore_now "SSH 配置语法检查失败"
  ADMIN_CONFIG=$("$SSHD" -T -C "user=$LOGIN_USER,host=localhost,addr=127.0.0.1") || restore_now "无法检查非 root 用户的有效配置"
  ROOT_CONFIG=$("$SSHD" -T -C "user=root,host=localhost,addr=127.0.0.1") || restore_now "无法检查 root 用户的有效配置"
  printf '%s\n' "$ADMIN_CONFIG" | grep -qx 'passwordauthentication no' || restore_now "密码登录仍然生效"
  printf '%s\n' "$ADMIN_CONFIG" | grep -qx 'kbdinteractiveauthentication no' || restore_now "交互式密码登录仍然生效"
  printf '%s\n' "$ADMIN_CONFIG" | grep -qx 'pubkeyauthentication yes' || restore_now "密钥登录没有启用"
  printf '%s\n' "$ROOT_CONFIG" | grep -qx 'permitrootlogin no' || restore_now "root 直连仍然生效"
  reload_ssh || restore_now "SSH 服务重载失败"
  info "SSH 配置已生效；Miao Panel 将用非 root 密钥重新连接，成功后才取消保险"
}

undo() {
  [ -f "${UNDO_restore:-}" ] || refuse "找不到 SSH 配置备份"
  BACKUP_DIR=$(dirname "$UNDO_restore")
  cancel_guard "${UNDO_guard:-}"
  sh "$UNDO_restore" || exit 30
  info "已恢复修改前的 SSH 配置"
}

case "$MODE" in
  apply) apply ;;
  undo) undo ;;
  *) refuse "不支持的操作 $MODE" ;;
esac
