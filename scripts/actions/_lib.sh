# Shared helpers for Miao Panel action scripts. The runner prepends this
# file to each action and invokes it as: sh action.sh <check|apply|undo> [args]
#
# Output protocol, one message per line on stdout:
#   MIAO_INFO <text>          progress shown to the user
#   MIAO_UNDO <key>=<value>   data the runner stores to undo this change;
#                             passed back as UNDO_<key> environment variables
#   MIAO_RESULT <key>=<value> facts about the outcome
# Exit codes:
#   0  success
#   10 precondition not met, nothing was changed
#   20 failed, all changes were rolled back
#   30 failed, rollback incomplete: needs a person to look
export LC_ALL=C
export PATH="$PATH:/usr/sbin:/sbin:/usr/local/sbin:/usr/local/bin"
MODE=${1:-check}
[ $# -gt 0 ] && shift
BACKUP_DIR=${MIAO_BACKUP_DIR:-/var/backups/miaopanel/manual}

info() { printf 'MIAO_INFO %s\n' "$*"; }
undo_data() { printf 'MIAO_UNDO %s=%s\n' "$1" "$2"; }
result() { printf 'MIAO_RESULT %s=%s\n' "$1" "$2"; }
refuse() { info "$*"; exit 10; }
have() { command -v "$1" >/dev/null 2>&1; }
is_uint() { case "$1" in '' | *[!0-9]*) return 1 ;; esac; return 0; }
has_systemd() { have systemctl && [ -d /run/systemd/system ]; }
backup_name() { printf '%s/%s' "$BACKUP_DIR" "$(printf '%s' "$1" | tr '/' '_')"; }
# backup copies a file into this run's backup directory and records it.
backup() {
  mkdir -p "$BACKUP_DIR" && cp -a "$1" "$(backup_name "$1")" || return 1
  undo_data "backup:$1" "$(backup_name "$1")"
}
restore() { cp -a "$(backup_name "$1")" "$1"; }

[ "$(id -u)" -eq 0 ] || refuse "需要 root 权限（或免密 sudo）才能修改这台服务器"
