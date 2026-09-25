#!/usr/bin/env bash
# CloudConsoleWithAI 服务器环境识别脚本（只读）
#
# 作用：识别服务器上运行的软件栈（面板 / Web 服务 / PHP / 数据库 / Docker / 应用），
#       输出一份精简的「服务器画像」，供 AI 选择诊断方式和修改模板。
#
# 保证：
#   - 只读：不修改任何文件、不安装软件、不重启服务、不连接数据库，
#     Nginx 站点信息直接读配置文件（不执行 nginx -t / -T）；
#   - 脱敏：password / secret / token / key 等字段的值会替换成 ***；
#     wp-config.php 只提取几个开关常量，不输出数据库账号密码；
#   - 精简：每段限制行数，整体尽量控制在 24KB 以内（TAT 单次输出上限）。
#
# 用法（建议用 root 执行，否则部分信息看不到）：
#   bash discover.sh                 # 输出全部段落
#   bash discover.sh docker web php  # 只输出指定段落
#
# 段落：system panel ports services procs web php db docker apps cron security health
# 额外段落（只在显式指定时输出）：logs —— 近 24 小时的系统错误和网站错误日志末尾
#
# 支持识别宝塔、1Panel（v1/v2）。1Panel 的安装目录从 /usr/local/bin/1pctl 读取，
# 测试时可用环境变量 ONEPANEL_CTL 指向别的 1pctl 文件。
#
# 脚本只用 POSIX sh 语法，也可以直接粘贴到腾讯云「自动化助手」里以 Shell 类型执行。

export LC_ALL=C
export PATH="$PATH:/usr/sbin:/sbin:/usr/local/sbin:/usr/local/bin"
MAXL=40
SECTIONS=" $* "

have() { command -v "$1" >/dev/null 2>&1; }

# 1Panel：安装目录记录在 1pctl 的 BASE_DIR（默认 /opt），数据目录为 $BASE_DIR/1panel。
# 1pctl 里还有初始用户名、密码和安全入口，只按白名单读取需要的键。
OP_CTL=${ONEPANEL_CTL:-/usr/local/bin/1pctl}
op_conf() { sed -n "s/^$1=//p" "$OP_CTL" 2>/dev/null | head -n 1 | tr -d "\"' "; }
OP_BASE=$(op_conf BASE_DIR)
[ -z "$OP_BASE" ] && [ -d /opt/1panel ] && OP_BASE=/opt
OP_DIR=""
[ -n "$OP_BASE" ] && [ -d "$OP_BASE/1panel" ] && OP_DIR="$OP_BASE/1panel"

# 给可能卡住的命令加超时
t() { if have timeout; then timeout 15 "$@"; else "$@"; fi; }
cap() { head -n "${1:-$MAXL}"; }
sec() { printf '\n== %s ==\n' "$1"; }
want() { [ "$SECTIONS" = "  " ] && return 0; case "$SECTIONS" in *" $1 "*) return 0 ;; esac; return 1; }

# 脱敏：key=value / key: value 形式、--password xxx、mysql -pxxx、URL 中的密码、Bearer token
redact() {
  sed -E \
    -e 's/((pass(word|wd)?|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)[A-Za-z0-9_.-]*["'\'']?[[:space:]]*[=:][[:space:]]*)[^[:space:],;&]+/\1***/Ig' \
    -e 's/(--pass(word)?[= ])[^[:space:]]+/\1***/Ig' \
    -e 's/(mysql(dump|admin)?[^|;]*[[:space:]]-p)[^[:space:]]+/\1***/g' \
    -e 's#(://[^:/@[:space:]]+:)[^@[:space:]]+@#\1***@#g' \
    -e 's/(Bearer[[:space:]]+)[^[:space:]]+/\1***/Ig'
}

s_system() {
  sec system
  echo "os: $(sed -n 's/^PRETTY_NAME=//p' /etc/os-release 2>/dev/null | tr -d '"')"
  echo "kernel: $(uname -r) $(uname -m)"
  echo "cpu_cores: $(nproc 2>/dev/null)"
  echo "loadavg: $(cut -d' ' -f1-3 /proc/loadavg 2>/dev/null)"
  echo "uptime: $(uptime -p 2>/dev/null)"
  echo "run_as: $(id -un)"
  [ "$(id -u)" -eq 0 ] || echo "WARN: 非 root 运行，部分信息可能不完整"
  have free && free -m
  sw=$(tail -n +2 /proc/swaps 2>/dev/null)
  echo "swap_devices: ${sw:-none}"
  echo "-- disk --"
  df -hT -x tmpfs -x devtmpfs -x overlay -x squashfs 2>/dev/null | cap 15
}

s_panel() {
  sec panel
  if [ -d /www/server/panel ]; then
    echo "bt_panel: yes"
    [ -f /www/server/panel/data/port.pl ] && echo "bt_panel_port: $(cat /www/server/panel/data/port.pl)"
    echo "bt_software: $(ls /www/server 2>/dev/null | tr '\n' ' ')"
    echo "bt_php_versions: $(ls /www/server/php 2>/dev/null | tr '\n' ' ')"
  else
    echo "bt_panel: no"
  fi
  if [ -z "$OP_DIR" ]; then
    echo "1panel: no"
    return
  fi
  echo "1panel: yes data_dir=$OP_DIR"
  for k in ORIGINAL_VERSION ORIGINAL_PORT PANEL_EDITION; do
    v=$(op_conf "$k")
    [ -n "$v" ] && echo "1panel_$k: $v"
  done
  # v2 为 1panel-core + 1panel-agent，v1 为 1panel
  if have systemctl; then
    for u in 1panel-core 1panel-agent 1panel; do
      [ "$(systemctl is-active "$u" 2>/dev/null)" = active ] && echo "1panel_service: $u active"
    done
  fi
  # 应用商店安装的应用：$OP_DIR/apps/<应用>/<名称>/docker-compose.yml
  echo "1panel_apps: $(for f in "$OP_DIR"/apps/*/*/docker-compose.yml; do
    [ -f "$f" ] && echo "$f"; done | sed -E "s#^$OP_DIR/apps/##; s#/docker-compose.yml\$##" | cap 30 | tr '\n' ' ')"
  # 网站：v2 为 $OP_DIR/www/conf.d，v1 在 OpenResty 应用目录下
  echo "1panel_websites: $(ls "$OP_DIR"/www/conf.d "$OP_DIR"/apps/openresty/*/conf/conf.d 2>/dev/null \
    | grep '\.conf$' | sed 's/\.conf$//' | sort -u | cap 30 | tr '\n' ' ')"
  echo "1panel_php_runtimes: $(ls "$OP_DIR"/runtime/php 2>/dev/null | tr '\n' ' ')"
}

s_ports() {
  sec ports
  if have ss; then
    t ss -Hlntup 2>/dev/null | awk '{print $1, $5, $7}' \
      | sed -E 's/users:\(\("([^"]+)",pid=([0-9]+)[^)]*\).*/\1 pid=\2/' | cap
  elif have netstat; then
    t netstat -lntup 2>/dev/null | cap
  else
    echo "ports: ss/netstat not available"
  fi
}

s_services() {
  sec services
  have systemctl || { echo "systemctl: no"; return; }
  echo "running: $(systemctl list-units --type=service --state=running --plain --no-legend --no-pager 2>/dev/null \
    | awk '{print $1}' | sed 's/\.service$//' | tr '\n' ' ')"
  echo "failed: $(systemctl list-units --state=failed --plain --no-legend --no-pager 2>/dev/null | awk '{print $1}' | tr '\n' ' ')"
}

# 进程排行：去掉本脚本自己（及其子进程）和内核线程（RSS 为 0）
s_procs() {
  sec procs_by_mem
  ps -eo pid,ppid,user,rss,pcpu,etimes,args --sort=-rss 2>/dev/null \
    | awk -v me=$$ 'NR == 1 || ($1 != me && $2 != me && $4 > 0)' | redact | cut -c1-160 | head -n 16
  sec procs_by_cpu
  ps -eo pid,ppid,user,pcpu,rss,args --sort=-pcpu 2>/dev/null \
    | awk -v me=$$ 'NR == 1 || ($1 != me && $2 != me && $5 > 0)' | redact | cut -c1-160 | head -n 11
}

s_web() {
  sec web
  for b in nginx /www/server/nginx/sbin/nginx /usr/local/nginx/sbin/nginx /usr/local/openresty/nginx/sbin/nginx openresty; do
    if have "$b"; then echo "nginx: $("$b" -v 2>&1 | head -1) bin=$(command -v "$b")"; break; fi
  done
  for b in apache2 httpd; do have "$b" && echo "apache: $("$b" -v 2>/dev/null | head -1)"; done
  have caddy && echo "caddy: $(caddy version 2>/dev/null | head -1)"
  echo "-- nginx sites (file:directive) --"
  for d in /etc/nginx /www/server/panel/vhost/nginx /www/server/nginx/conf /usr/local/nginx/conf /usr/local/openresty/nginx/conf \
      ${OP_DIR:+"$OP_DIR"/www/conf.d "$OP_DIR"/apps/openresty/*/conf}; do
    [ -d "$d" ] || continue
    grep -RHsE --exclude-dir=sites-available --exclude='*.bak' --exclude='*.default' \
      '(^|[{;])[[:space:]]*(server_name|listen|root|proxy_pass|fastcgi_pass|ssl_certificate)[[:space:]]' "$d"
  done | sed -E 's/[[:space:]]+/ /g; s/;[[:space:]]*$//' | awk '!seen[$0]++' | cap 80
  for b in apache2ctl apachectl; do
    if have "$b"; then echo "-- apache vhosts --"; t "$b" -S 2>/dev/null | cap 30; break; fi
  done
}

s_php() {
  sec php
  have php && echo "php_cli: $(php -v 2>/dev/null | head -1)"
  echo "-- php-fpm masters --"
  ps -eo pid,args 2>/dev/null | grep '[p]hp-fpm: master' | cap 10
  echo "-- php-fpm workers per pool --"
  ps -eo rss,args 2>/dev/null | awk '/[p]hp-fpm: pool/ {p=$NF; n[p]++; r[p]+=$1}
    END {for (p in n) printf "pool=%s workers=%d avg_rss_mb=%.1f total_mb=%.0f\n", p, n[p], r[p]/n[p]/1024, r[p]/1024}'
  echo "-- pool settings --"
  # 1Panel 的 PHP 运行环境在 $OP_DIR/runtime/php/<名称>/ 下
  { for f in /etc/php/*/fpm/pool.d/*.conf /etc/php-fpm.d/*.conf /www/server/php/*/etc/php-fpm.conf /opt/remi/php*/root/etc/php-fpm.d/*.conf; do
      echo "$f"; done
    [ -n "$OP_DIR" ] && t find "$OP_DIR/runtime/php" -maxdepth 4 -name '*.conf' -type f 2>/dev/null
  } | while read -r f; do
    [ -f "$f" ] || continue
    out=$(grep -hE '^[[:space:]]*pm(\.[a-z_]+)?[[:space:]]*=' "$f" | tr -d ' ' | tr '\n' ' ')
    [ -n "$out" ] && echo "$f: $out"
  done | cap 20
  echo "-- memory_limit --"
  { for f in /etc/php/*/fpm/php.ini /etc/php.ini /www/server/php/*/etc/php.ini; do echo "$f"; done
    [ -n "$OP_DIR" ] && t find "$OP_DIR/runtime/php" -maxdepth 4 -name 'php.ini' -type f 2>/dev/null
  } | while read -r f; do
    [ -f "$f" ] || continue
    echo "$f: $(grep -hE '^[[:space:]]*memory_limit' "$f" | tr -d ' ')"
  done | cap 10
}

s_db() {
  sec db
  for b in mysqld mariadbd /www/server/mysql/bin/mysqld; do
    have "$b" && echo "$b: $("$b" --version 2>/dev/null | head -1)"
  done
  echo "-- db processes --"
  # 按进程名（comm）精确匹配，排除本脚本自己的子进程
  ps -eo pid,ppid,rss,comm,args 2>/dev/null \
    | awk -v me=$$ '$2 != me && $4 ~ /^(mysqld|mariadbd|postgres|redis-server|mongod)$/ {
        printf "pid=%s rss_mb=%d ", $1, $3/1024; $1 = $2 = $3 = $4 = ""; print }' | redact | cut -c1-160 | cap 10
  echo "-- mysql config --"
  # 1Panel 的 MySQL/MariaDB 应用配置在 $OP_DIR/apps/<应用>/<名称>/ 下
  { for f in /etc/my.cnf /etc/my.cnf.d/*.cnf /etc/mysql/my.cnf /etc/mysql/*.cnf /etc/mysql/conf.d/*.cnf /etc/mysql/mysql.conf.d/*.cnf /etc/mysql/mariadb.conf.d/*.cnf; do
      echo "$f"; done
    [ -n "$OP_DIR" ] && t find "$OP_DIR/apps" -maxdepth 4 -name '*.cnf' -type f 2>/dev/null
  } | while read -r f; do
    [ -f "$f" ] || continue
    out=$(grep -hE '^[[:space:]]*(innodb_buffer_pool_size|max_connections|key_buffer_size|query_cache_size|performance_schema|tmp_table_size|table_open_cache)[[:space:]]*=' "$f" | tr -d ' ' | tr '\n' ' ')
    [ -n "$out" ] && echo "$f: $out"
  done | cap 15
  echo "-- redis config --"
  { for f in /etc/redis/redis.conf /etc/redis.conf /www/server/redis/redis.conf; do echo "$f"; done
    [ -n "$OP_DIR" ] && t find "$OP_DIR/apps" -maxdepth 4 -name 'redis.conf' -type f 2>/dev/null
  } | while read -r f; do
    [ -f "$f" ] || continue
    out=$(grep -hE '^[[:space:]]*maxmemory(-policy)?[[:space:]]' "$f" | tr '\n' ' ')
    echo "$f: ${out:-maxmemory unset (no limit)}"
  done
}

s_docker() {
  sec docker
  have docker || { echo "docker: no"; return; }
  v=$(t docker version --format '{{.Server.Version}}' 2>/dev/null)
  echo "docker: ${v:-installed, daemon not reachable}"
  [ -n "$v" ] || return
  echo "-- containers (name|image|status|ports) --"
  t docker ps -a --format '{{.Names}}|{{.Image}}|{{.Status}}|{{.Ports}}' 2>/dev/null | cap
  echo "-- compose projects (project|working_dir|config_files) --"
  t docker ps -a --format '{{.Label "com.docker.compose.project"}}|{{.Label "com.docker.compose.project.working_dir"}}|{{.Label "com.docker.compose.project.config_files"}}' 2>/dev/null \
    | grep -v '^||$' | sort -u | cap 20
  echo "-- stats --"
  t docker stats --no-stream --format '{{.Name}}|mem={{.MemUsage}}|cpu={{.CPUPerc}}' 2>/dev/null | cap
  ids=$(t docker ps -q 2>/dev/null)
  if [ -n "$ids" ]; then
    echo "-- limits / restart / logging --"
    # shellcheck disable=SC2086
    t docker inspect -f '{{.Name}}|mem_limit={{.HostConfig.Memory}}|restart={{.HostConfig.RestartPolicy.Name}}|log={{.HostConfig.LogConfig.Type}}{{range $k, $v := .HostConfig.LogConfig.Config}} {{$k}}={{$v}}{{end}}' $ids 2>/dev/null | cap
  fi
  echo "-- disk --"
  t docker system df 2>/dev/null
  echo "-- largest container logs --"
  t du -sh /var/lib/docker/containers/*/*-json.log 2>/dev/null | sort -rh | head -n 5
}

s_apps() {
  sec apps
  echo "-- wordpress --"
  for d in /var/www /www/wwwroot /home /srv /opt /usr/share/nginx /data ${OP_DIR:+"$OP_DIR"/www/sites "$OP_DIR"/apps}; do
    [ -d "$d" ] && t find "$d" -maxdepth 4 -name wp-config.php -type f 2>/dev/null
  done | awk '!seen[$0]++' | cap 10 | while read -r f; do
    dir=$(dirname "$f")
    ver=$(grep -oE "wp_version = '[^']+'" "$dir/wp-includes/version.php" 2>/dev/null | cut -d"'" -f2)
    flags=$(grep -oE "define\([[:space:]]*'(DISABLE_WP_CRON|WP_MEMORY_LIMIT|WP_MAX_MEMORY_LIMIT|WP_CACHE|WP_DEBUG)'[[:space:]]*,[[:space:]]*[^)]+\)" "$f" 2>/dev/null | tr -d ' ' | tr '\n' ' ')
    echo "wordpress: dir=$dir version=${ver:-?} $flags"
  done
  echo "-- java --"
  ps -eo pid,rss,args 2>/dev/null | awk '$3 ~ /(^|\/)java$/' | cap 10 | while read -r pid rss rest; do
    heap=$(echo "$rest" | grep -oE -- '-Xm[sx][0-9]+[kKmMgG]?|-XX:(Max|Initial)RAMPercentage=[0-9.]+' | tr '\n' ' ')
    main=$(echo "$rest" | grep -oE -- '-jar [^ ]+|org\.apache\.catalina\.startup\.Bootstrap' | head -n 1)
    unit=$(ps -o unit= -p "$pid" 2>/dev/null)
    echo "java: pid=$pid rss_mb=$((rss / 1024)) heap_opts=[$heap] main=${main:-?} unit=${unit:-?}"
  done
  echo "-- node / python --"
  ps -eo pid,ppid,rss,comm,args 2>/dev/null \
    | awk -v me=$$ '$2 != me && ($4 == "node" || $4 ~ /^(gunicorn|uwsgi|uvicorn|PM2)/) {
        printf "pid=%s rss_mb=%d ", $1, $3/1024; $1 = $2 = $3 = $4 = ""; print }' | redact | cut -c1-160 | cap 10
}

s_cron() {
  sec cron
  echo "-- root crontab --"
  crontab -l 2>/dev/null | grep -vE '^[[:space:]]*(#|$)' | redact | cut -c1-160 | cap 20
  echo "cron.d: $(ls /etc/cron.d 2>/dev/null | tr '\n' ' ')"
  have systemctl && echo "timers: $(systemctl list-timers --all --plain --no-legend --no-pager 2>/dev/null \
    | grep -oE '[^[:space:]]+\.timer' | tr '\n' ' ')"
}

s_security() {
  sec security
  cfg="/etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf"
  # shellcheck disable=SC2086
  echo "sshd_port: $(grep -hE '^[[:space:]]*Port[[:space:]]' $cfg 2>/dev/null | awk '{print $2}' | tr '\n' ' ')"
  # shellcheck disable=SC2086
  echo "sshd_password_auth: $(grep -hE '^[[:space:]]*PasswordAuthentication[[:space:]]' $cfg 2>/dev/null | awk '{print $2}' | head -n 1)"
  # shellcheck disable=SC2086
  echo "sshd_root_login: $(grep -hE '^[[:space:]]*PermitRootLogin[[:space:]]' $cfg 2>/dev/null | awk '{print $2}' | head -n 1)"
  have ufw && echo "ufw: $(ufw status 2>/dev/null | head -n 1)"
  have firewall-cmd && echo "firewalld: $(firewall-cmd --state 2>/dev/null)"
  # 腾讯云相关 agent：TAT（远程执行）、barad（云监控内存等指标依赖它）、YDService（主机安全）
  for p in tat_agent barad_agent YDService; do
    if pgrep -f "$p" >/dev/null 2>&1; then echo "$p: running"; else echo "$p: not-found"; fi
  done
}

s_health() {
  sec health
  echo "-- oom (7d) --"
  if have journalctl; then
    t journalctl -k --since '7 days ago' --no-pager 2>/dev/null | grep -iE 'out of memory|oom-kill|killed process' | tail -n 10
  else
    dmesg 2>/dev/null | grep -iE 'out of memory|killed process' | tail -n 10
  fi
  echo "-- big dirs --"
  for d in /var/log /www/wwwlogs /var/lib/docker /var/lib/mysql /www/server/data /tmp; do
    [ -d "$d" ] && t du -sh "$d" 2>/dev/null
  done
  echo "-- inodes > 50% --"
  df -i -x tmpfs -x devtmpfs -x overlay -x squashfs 2>/dev/null | awk 'NR > 1 && $5 + 0 > 50'
}

s_logs() {
  sec logs
  echo "-- system errors (24h) --"
  if have journalctl; then
    t journalctl -p err --since '24 hours ago' --no-pager 2>/dev/null | tail -n 30 | redact | cut -c1-200
  fi
  echo "-- web error logs (tail) --"
  for f in /var/log/nginx/error.log /www/wwwlogs/*error*.log ${OP_DIR:+"$OP_DIR"/www/sites/*/log/error.log}; do
    [ -f "$f" ] || continue
    echo "# $f"
    tail -n 15 "$f" 2>/dev/null | redact | cut -c1-200
  done | cap 80
}

echo "# discover.sh v1 $(date -u +%Y-%m-%dT%H:%M:%SZ) host=$(hostname 2>/dev/null)"
for s in system panel ports services procs web php db docker apps cron security health; do
  want "$s" && "s_$s"
done
case "$SECTIONS" in *" logs "*) s_logs ;; esac
# 各段落内部的失败不影响整体结果；TAT 会把非 0 退出码记为执行失败
exit 0
