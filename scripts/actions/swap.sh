# swap.set: add a swap file.  args: <size_gb>
SIZE_GB=$1
SWAPFILE=/swapfile
# Named to sort after 99-sysctl.conf (the link to /etc/sysctl.conf), which
# on many cloud images sets swappiness itself and would otherwise win at
# boot. The old name is still removed on undo.
SYSCTL_FILE=/etc/sysctl.d/zz-miaopanel-swap.conf
OLD_SYSCTL_FILE=/etc/sysctl.d/99-miaopanel-swap.conf

check() {
  is_uint "$SIZE_GB" && [ "$SIZE_GB" -ge 1 ] && [ "$SIZE_GB" -le 16 ] || refuse "swap 大小需要在 1 到 16 GB 之间"
  if [ "$(awk 'NR > 1' /proc/swaps | wc -l)" -gt 0 ]; then refuse "服务器已经有 swap 了，不需要再添加"; fi
  [ -e "$SWAPFILE" ] && refuse "$SWAPFILE 已经存在，为了安全不会覆盖"
  have mkswap && have swapon || refuse "系统缺少 mkswap 或 swapon 命令"
  fstype=$(df -PT / | awk 'NR == 2 {print $2}')
  case "$fstype" in btrfs | zfs) refuse "系统盘是 $fstype 文件系统，暂不支持自动创建 swap 文件" ;; esac
  avail_kb=$(df -Pk / | awk 'NR == 2 {print $4}')
  [ "$avail_kb" -ge $(((SIZE_GB + 1) * 1024 * 1024)) ] || refuse "系统盘剩余空间不够（至少需要 $((SIZE_GB + 1))GB）"
}

apply() {
  old_swappiness=$(cat /proc/sys/vm/swappiness)
  undo_data swapfile "$SWAPFILE"
  undo_data swappiness "$old_swappiness"
  backup /etc/fstab || refuse "无法备份 /etc/fstab"
  rollback() {
    info "出错了，正在恢复原状"
    swapoff "$SWAPFILE" 2>/dev/null
    rm -f "$SWAPFILE" "$SYSCTL_FILE"
    restore /etc/fstab
    sysctl -q -w vm.swappiness="$old_swappiness" 2>/dev/null
    exit 20
  }
  info "正在创建 ${SIZE_GB}GB 的 swap 文件"
  if ! fallocate -l "${SIZE_GB}G" "$SWAPFILE" 2>/dev/null; then
    dd if=/dev/zero of="$SWAPFILE" bs=1M count=$((SIZE_GB * 1024)) 2>/dev/null || rollback
  fi
  chmod 600 "$SWAPFILE" || rollback
  mkswap "$SWAPFILE" >/dev/null 2>&1 || rollback
  swapon "$SWAPFILE" || rollback
  grep -q "^$SWAPFILE " /etc/fstab || echo "$SWAPFILE none swap sw 0 0" >>/etc/fstab || rollback
  echo "vm.swappiness=10" >"$SYSCTL_FILE" || rollback
  sysctl -q -w vm.swappiness=10 || rollback
  grep -q "^$SWAPFILE " /proc/swaps || rollback
  result swap_total_mb "$(awk '/^SwapTotal/ {print int($2 / 1024)}' /proc/meminfo)"
  info "完成：已添加 ${SIZE_GB}GB swap，开机后也会自动启用；swappiness 调为 10（尽量先用内存）"
}

undo() {
  f=${UNDO_swapfile:-$SWAPFILE}
  info "正在移除 $f"
  if grep -q "^$f " /proc/swaps; then
    swapoff "$f" || { info "swap 正在被大量使用，内存不够把它收回，暂时不能撤销"; exit 10; }
  fi
  sed -i "\#^$f #d" /etc/fstab
  rm -f "$f" "$SYSCTL_FILE" "$OLD_SYSCTL_FILE"
  sysctl -q -w vm.swappiness="${UNDO_swappiness:-60}" 2>/dev/null
  info "已撤销：swap 已移除"
}

case "$MODE" in
check) check; info "检查通过：可以添加 ${SIZE_GB}GB swap" ;;
apply) check; apply ;;
undo) undo ;;
esac
