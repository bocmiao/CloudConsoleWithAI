#!/bin/sh
# Miao Panel 网站访问统计脚本（只读）
#
# 作用：读取服务器上网站的访问日志，按网站、按天统计 PV、UV、独立 IP、
#       请求数、流量、爬虫、错误，以及受访页面、来源、访客 IP、状态码等排行。
#       日志只在服务器上统计，只把汇总结果传回 Miao Panel。
#
# 保证：只读，不修改任何文件；用 nice 降低优先级；单个日志超过 200MB 时只读最后 200MB。
#
# 用法：sh sitelogs.sh [天数]      # 默认 7 天，最多 31 天，1 表示只看今天
#
# 能找到的日志：
#   - 1Panel：OpenResty 容器挂载的 /www（默认 /opt/1panel/www，旧版本在
#     /opt/1panel/apps/openresty/openresty/www）下的 sites/<网站>/log/access.log，
#     以及 1Panel「切割网站日志」计划任务留下的 backup/log/website/<网站>/*.gz；
#   - 宝塔：/www/wwwlogs/<网站>.log；
#   - 都没有时：/var/log/nginx/ 下的访问日志（含 .1 和 .gz 轮转文件）。
#
# 日志格式按 Nginx 默认的 main / combined 解析：
#   $remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent
#   "$http_referer" "$http_user_agent" "$http_x_forwarded_for"
# 有 X-Forwarded-For 时用其中第一个 IP 作为访客 IP（经过 EdgeOne、CDN 时是真实访客）。
#
# 口径：
#   PV   = 爬虫以外、GET、状态 2xx 或 304、不是静态文件（css/js/图片/字体等）和 /api/ 的请求
#   UV   = PV 里不同的「IP + 浏览器标识」
#   IP   = PV 里不同的访客 IP
#   爬虫 = 浏览器标识里有 bot / spider / crawler，或是 curl、python 等程序，或为空
#
# 输出（制表符分隔）：
#   N  服务器今天的日期 天数 时区
#   E  找不到日志时的说明
#   F  网站 文件 大小                              读到的日志文件
#   D  网站 日期 请求 PV UV IP 爬虫 流量 4xx 5xx   每天
#   S  网站 请求 PV UV IP 爬虫 流量 4xx 5xx        整段时间（UV、IP 按整段去重）
#   H  网站 小时 请求 PV                           最近一天每小时
#   T  网站 类别 次数 值                           排行（page/referer/ip/status/bot/device，每类前 20）
#   V  网站 行数 带X-Forwarded-For的行数 无法解析的行数
# 网站为 * 的是所有网站合起来（UV、IP 在所有网站之间去重）。

export LC_ALL=C
export PATH="$PATH:/usr/sbin:/sbin:/usr/local/sbin:/usr/local/bin"
DAYS=${1:-7}
case $DAYS in '' | *[!0-9]*) DAYS=7 ;; esac
[ "$DAYS" -lt 1 ] && DAYS=1
[ "$DAYS" -gt 31 ] && DAYS=31
MAXBYTES=209715200

have() { command -v "$1" >/dev/null 2>&1; }

# The last DAYS days as they appear in $time_local, e.g. 26/Sep/2026.
now=$(date +%s)
DAYLIST=""
i=0
while [ $i -lt "$DAYS" ]; do
	d=$(date -d "@$((now - i * 86400))" +%d/%b/%Y 2>/dev/null) || break
	DAYLIST="$DAYLIST $d"
	i=$((i + 1))
done
[ -z "$DAYLIST" ] && DAYLIST=$(date +%d/%b/%Y)

# LIST holds one "SITE<tab>KIND<tab>FILE" line per log; KIND is plain, gz or
# tar (1Panel's log archives).
LIST=""
NL='
'
add() { [ -f "$3" ] && [ -r "$3" ] && LIST="$LIST$1	$2	$3$NL"; }
recent() { find "$@" -type f -mtime "-$((DAYS + 1))" 2>/dev/null; }

# 1Panel: where the OpenResty container keeps /www, plus the default places.
ROOTS=""
if have docker; then
	for c in $(docker ps --format '{{.Names}}' 2>/dev/null | grep -i openresty); do
		ROOTS="$ROOTS $(docker inspect -f '{{range .Mounts}}{{.Destination}} {{.Source}}{{"\n"}}{{end}}' "$c" 2>/dev/null | awk '$1 == "/www" {print $2}')"
	done
fi
OP_BASE=$(sed -n 's/^BASE_DIR=//p' "${ONEPANEL_CTL:-/usr/local/bin/1pctl}" 2>/dev/null | head -n 1 | tr -d "\"' ")
[ -z "$OP_BASE" ] && OP_BASE=/opt
ROOTS="$ROOTS $OP_BASE/1panel/www $OP_BASE/1panel/apps/openresty/openresty/www"
seen=" "
for r in $ROOTS; do
	[ -d "$r/sites" ] || continue
	r=$(cd "$r" && pwd -P)
	case $seen in *" $r "*) continue ;; esac
	seen="$seen$r "
	for d in "$r"/sites/*/; do
		d=${d%/}
		site=$(basename "$d")
		add "$site" plain "$d/log/access.log"
		for f in $(recent "$OP_BASE/1panel/backup/log/website/$site" -name '*.gz'); do add "$site" tar "$f"; done
	done
done

# 宝塔
if [ -z "$LIST" ] && [ -d /www/wwwlogs ]; then
	for f in /www/wwwlogs/*.log; do
		case $f in *error*) continue ;; esac
		site=$(basename "$f" .log)
		[ "$site" = access ] && continue
		add "$site" plain "$f"
	done
fi

# Plain Nginx
if [ -z "$LIST" ] && [ -d /var/log/nginx ]; then
	for f in /var/log/nginx/*.log; do
		case $f in *error*) continue ;; esac
		site=$(basename "$f" .log)
		site=${site%.access}
		site=${site%_access}
		[ "$site" = access ] && site=nginx
		add "$site" plain "$f"
		add "$site" plain "$f.1"
		for g in $(recent /var/log/nginx -name "$(basename "$f").*.gz"); do add "$site" gz "$g"; done
	done
fi

printf 'N\t%s\t%s\t%s\n' "$(date +%Y-%m-%d)" "$DAYS" "$(date +%z)"
if [ -z "$LIST" ]; then
	echo "E	没有找到网站访问日志（找过 1Panel、宝塔和 /var/log/nginx）"
	exit 0
fi

printf '%s' "$LIST" | while IFS='	' read -r site kind f; do
	printf 'F\t%s\t%s\t%s\n' "$site" "$f" "$(wc -c <"$f" 2>/dev/null | tr -d ' ')"
done

# Every file's lines, each file preceded by a line naming its site.
emit() {
	printf '%s' "$LIST" | while IFS='	' read -r site kind f; do
		printf '@@MIAO_SITE %s\n' "$site"
		case $kind in
		tar) tar -xzOf "$f" access.log 2>/dev/null ;;
		gz) gzip -dc "$f" 2>/dev/null ;;
		*)
			size=$(wc -c <"$f" 2>/dev/null || echo 0)
			if [ "${size:-0}" -gt $MAXBYTES ]; then tail -c $MAXBYTES "$f"; else cat "$f"; fi
			;;
		esac
	done
}

PROG='
function bump(k,   vk, x) {
	R[k, day]++; RQ[k]++
	B[k, day] += bytes; BS[k] += bytes
	if (status >= 400 && status < 500) { E4[k, day]++; E4S[k]++ }
	if (status >= 500) { E5[k, day]++; E5S[k]++ }
	if (day == last) HR[k, hour]++
	TS[k, status]++
	if (bot) { BO[k, day]++; BOS[k]++; TB[k, botname]++; return }
	TI[k, ip]++
	if (!page) return
	P[k, day]++; PS[k]++
	if (day == last) HP[k, hour]++
	x = (k == "*") ? site path : path
	TP[k, x]++
	TR[k, refhost]++
	TD[k, device]++
	vk = ip "|" substr(ua, 1, 80)
	if (!((k, day, vk) in UD)) { UD[k, day, vk] = 1; U[k, day]++ }
	if (!((k, vk) in UR)) { UR[k, vk] = 1; US[k]++ }
	if (!((k, day, ip) in ID)) { ID[k, day, ip] = 1; I[k, day]++ }
	if (!((k, ip) in IR)) { IR[k, ip] = 1; IS[k]++ }
}
BEGIN {
	split("Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec", mn, " ")
	for (i = 1; i <= 12; i++) mon[mn[i]] = sprintf("%02d", i)
	n = split(days, dl, " ")
	for (i = 1; i <= n; i++) ok[dl[i]] = 1
	d = dl[1]
	last = substr(d, 8, 4) "-" mon[substr(d, 4, 3)] "-" substr(d, 1, 2)
}
/^@@MIAO_SITE / { site = substr($0, 13); sites[site] = 1; next }
{
	L[site]++
	b = index($0, "[")
	if (!b) { X[site]++; next }
	ts = substr($0, b + 1, 20)
	if (!(substr(ts, 1, 11) in ok)) next
	day = substr(ts, 8, 4) "-" mon[substr(ts, 4, 3)] "-" substr(ts, 1, 2)
	hour = substr(ts, 13, 2)
	nq = split($0, q, "\"")
	if (nq < 7) { X[site]++; next }
	split(q[1], a, " "); ip = a[1]
	split(q[2], rq, " "); method = rq[1]; path = rq[2]
	split(q[3], st, " "); status = st[1] + 0; bytes = st[2] + 0
	ref = q[4]; ua = q[6]
	if (nq >= 9 && q[8] != "-" && q[8] != "") {
		split(q[8], xf, ","); c = xf[1]; gsub(/ /, "", c)
		if (c != "") { ip = c; XF[site]++ }
	}
	sub(/\?.*/, "", path)
	if (path == "") path = "/"
	lua = tolower(ua)
	bot = (ua == "-" || ua == "" || lua ~ /bot|spider|crawl|slurp|curl|wget|python|go-http|java\/|httpclient|okhttp|scrapy|headless|scan|monitor|uptime|zgrab|nmap|masscan|libwww|axios|node-fetch|feed|preview/)
	botname = ""
	if (bot) {
		if (match(ua, /[A-Za-z0-9_.-]*([Bb]ot|[Ss]pider|[Cc]rawler)[A-Za-z0-9_.-]*/)) botname = substr(ua, RSTART, RLENGTH)
		else if (ua == "-" || ua == "") botname = "（没有浏览器标识）"
		else { botname = ua; sub(/[\/ ;(].*/, "", botname) }
	}
	lp = tolower(path)
	page = (!bot && method == "GET" && ((status >= 200 && status < 300) || status == 304) && lp !~ /\.(css|js|mjs|map|png|jpe?g|gif|svg|ico|webp|avif|bmp|woff2?|ttf|eot|otf|mp4|webm|mp3|wav|ogg|m4a|pdf|zip|gz|rar|7z|txt|xml|json|wasm|webmanifest)$/ && lp !~ /^\/api\//)
	refhost = "（直接访问）"
	if (ref != "-" && ref != "") {
		h = ref; sub(/^[a-zA-Z]+:\/\//, "", h); sub(/[\/?#:].*/, "", h)
		if (h == site || h == "") refhost = "（站内跳转）"; else refhost = h
	}
	device = (ua ~ /Mobile|Android|iPhone|iPad|HarmonyOS/) ? "手机/平板" : "电脑"
	bump(site); bump("*")
}
END {
	T = "\t"
	for (x in R) { split(x, kd, SUBSEP); print "D" T kd[1] T kd[2] T R[x] T P[x] + 0 T U[x] + 0 T I[x] + 0 T BO[x] + 0 T sprintf("%.0f", B[x]) T E4[x] + 0 T E5[x] + 0 }
	for (k in RQ) print "S" T k T RQ[k] T PS[k] + 0 T US[k] + 0 T IS[k] + 0 T BOS[k] + 0 T sprintf("%.0f", BS[k]) T E4S[k] + 0 T E5S[k] + 0
	for (x in HR) { split(x, kh, SUBSEP); print "H" T kh[1] T kh[2] T HR[x] T HP[x] + 0 }
	for (s in sites) print "V" T s T L[s] + 0 T XF[s] + 0 T X[s] + 0
	for (x in TP) { split(x, kv, SUBSEP); print "T" T kv[1] T "page" T TP[x] T kv[2] }
	for (x in TR) { split(x, kv, SUBSEP); print "T" T kv[1] T "referer" T TR[x] T kv[2] }
	for (x in TI) { split(x, kv, SUBSEP); print "T" T kv[1] T "ip" T TI[x] T kv[2] }
	for (x in TS) { split(x, kv, SUBSEP); print "T" T kv[1] T "status" T TS[x] T kv[2] }
	for (x in TB) { split(x, kv, SUBSEP); print "T" T kv[1] T "bot" T TB[x] T kv[2] }
	for (x in TD) { split(x, kv, SUBSEP); print "T" T kv[1] T "device" T TD[x] T kv[2] }
}'

emit | nice -n 10 awk -v days="$DAYLIST" "$PROG" |
	sort -t '	' -k1,1 -k2,2 -k3,3 -k4,4nr |
	awk -F '	' '$1 != "T" { print; next } { k = $2 FS $3; if (++n[k] <= 20) print }'
