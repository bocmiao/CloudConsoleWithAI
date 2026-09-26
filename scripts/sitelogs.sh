#!/bin/sh
# Miao Panel 网站访问统计脚本（只读）
#
# 作用：读取服务器上网站的访问日志，一次统计出「今天、7 天、30 天」三段时间：
#       每个网站和全部网站的 PV、UV、独立 IP、请求数、流量、爬虫、错误，
#       受访页面、访问目录、来源、访客 IP、状态码、爬虫、设备、错误页面排行，
#       最近 30 天每天和今天每小时的数字，以及值得注意的 IP 做了什么
#       （请求频率、访问的页面和网站、扫描、注入、登录爆破等迹象）。
#       日志只在服务器上统计，只把汇总结果传回 Miao Panel。
#
# 保证：只读，不修改任何文件；用 nice 降低优先级；单个日志超过 200MB 时只读最后
#       200MB；超过 300 万行后不再统计 IP 明细（总数照常统计）。
#
# 用法：sh sitelogs.sh
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
# 口径（和 Miao Panel 统计 EdgeOne 日志的规则相同，见 internal/visits/rules.go，
# 下面每条规则都标了「rule 名字」，测试会核对两边一致）：
#   PV   = 爬虫以外、GET、状态 2xx 或 304、不是静态文件和 /api/ 的请求
#   UV   = PV 里不同的「IP + 浏览器标识」；IP = PV 里不同的访客 IP
#   爬虫 = 浏览器标识里有 bot / spider / crawler 等，或是 curl、python 等程序，或为空
#
# 输出（制表符分隔；R 是 1、7 或 30 天）：
#   N  服务器今天的日期 30 时区
#   E  找不到日志时的说明
#   F  网站 文件 大小                                 读到的日志文件
#   V  网站 行数 带X-Forwarded-For的行数 无法解析的行数
#   D  网站 日期 请求 PV UV IP 爬虫 流量 4xx 5xx      最近 30 天每天
#   H  网站 小时 请求 PV                              今天每小时
#   S  R 网站 请求 PV UV IP 爬虫 流量 4xx 5xx         整段时间（UV、IP 按整段去重）
#   T  R 网站 类别 次数 值                            排行（page dir referer ip status bot
#                                                     device errpage dead leak，每类前 20）
#   I  R IP 请求 PV 4xx 5xx POST 不同地址 敏感探测 注入 登录失败 每分钟峰值 最早 最晚 爬虫名 直连次数（没经过代理） 浏览器标识
#   P  R IP path|site 次数 值                         这个 IP 最常访问的地址和网站（前 5）
#   A  R 网站 visitor PV 访客IP                       有浏览的访客（每个网站前 3000，用来统计地区）
#   X  说明                                           统计被截断等
# 网站为 * 的是所有网站合起来（UV、IP 在所有网站之间去重）。

export LC_ALL=C
export PATH="$PATH:/usr/sbin:/sbin:/usr/local/sbin:/usr/local/bin"
DAYS=30
MAXBYTES=209715200
TAB=$(printf '\t')

have() { command -v "$1" >/dev/null 2>&1; }

# The last 30 days as they appear in $time_local, today first: 26/Sep/2026 ...
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
add() { [ -f "$3" ] && [ -r "$3" ] && LIST="$LIST$1$TAB$2$TAB$3$NL"; }
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
	echo "E${TAB}没有找到网站访问日志（找过 1Panel、宝塔和 /var/log/nginx）"
	exit 0
fi

printf '%s' "$LIST" | while IFS="$TAB" read -r site kind f; do
	printf 'F\t%s\t%s\t%s\n' "$site" "$f" "$(wc -c <"$f" 2>/dev/null | tr -d ' ')"
done

# Every file's lines, each file preceded by a line naming its site.
emit() {
	printf '%s' "$LIST" | while IFS="$TAB" read -r site kind f; do
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
function isbot(s) {
	return s ~ /bot|spider|crawl|slurp|curl|wget|python|go-http|java\/|httpclient|okhttp|scrapy|headless|scan|monitor|uptime|zgrab|nmap|masscan|libwww|axios|node-fetch|feed|preview|sqlmap|nikto|nuclei|dirbuster|gobuster|ffuf|fuzz|hydra|acunetix|nessus|openvas|burp/ # rule bot
}
# botword names the program behind a UA: the first word the bot rule
# matches, without its version, else the first word.
function botword(s,   n, w, i, name) {
	n = split(s, w, /[ ;()]+/)
	for (i = 1; i <= n; i++) {
		if (w[i] != "" && isbot(tolower(w[i]))) {
			name = w[i]
			sub(/\/.*/, "", name)
			return name
		}
	}
	name = s
	sub(/[\/ ;(].*/, "", name)
	return name
}
function classify(   lua, lp, h, rest, i) {
	path = target
	sub(/\?.*/, "", path)
	if (path == "") path = "/"
	lua = tolower(ua)
	bot = (ua == "-" || ua == "" || isbot(lua))
	botname = ""
	if (bot) {
		if (match(ua, /[A-Za-z0-9_.-]*([Bb]ot|[Ss]pider|[Cc]rawler)[A-Za-z0-9_.-]*/)) botname = substr(ua, RSTART, RLENGTH) # rule botName
		else if (ua == "-" || ua == "") botname = "（没有浏览器标识）"
		else botname = botword(ua)
	}
	lp = tolower(path)
	page = (!bot && method == "GET" && ((status >= 200 && status < 300) || status == 304) && lp !~ /\.(css|js|mjs|map|png|jpe?g|gif|svg|ico|webp|avif|bmp|woff2?|ttf|eot|otf|mp4|webm|mp3|wav|ogg|m4a|pdf|zip|gz|rar|7z|txt|xml|json|wasm|webmanifest)$/ && substr(lp, 1, 5) != "/api/") # rule static
	refhost = "（直接访问）"
	if (ref != "-" && ref != "") {
		h = ref
		sub(/^[a-zA-Z]+:\/\//, "", h)
		sub(/[\/?#:].*/, "", h)
		if (h == site || h == "") refhost = "（站内跳转）"; else refhost = h
	}
	device = (ua ~ /Mobile|Android|iPhone|iPad|HarmonyOS/) ? "手机/平板" : "电脑" # rule mobile
	rest = path
	if (substr(rest, 1, 1) == "/") rest = substr(rest, 2)
	i = index(rest, "/")
	dir = (i > 0) ? "/" substr(rest, 1, i - 1) "/" : "/"
	login = (method == "POST" && lp ~ /login|signin|sign-in|logon|wp-login\.php|xmlrpc\.php|passport|\/auth\// && (status == 401 || status == 403 || status == 429 || lp ~ /wp-login\.php|xmlrpc\.php/)) # rule login wordpress
	sensitive = (!login && status >= 400 && status < 500 && lp ~ /wp-login\.php|xmlrpc\.php|\/wp-admin|\/\.env|\/\.git|\/\.svn|\/\.ds_store|\/\.aws|\/\.vscode|\/\.idea|phpmyadmin|\/pma\/|\/adminer|\/manager\/html|\/cgi-bin\/|\/vendor\/phpunit|\/actuator|\/solr\/|\/hnap1|\/boaform|\/phpinfo|\/server-status|\/druid\/|\/nacos|\/_ignition|\/owa\/|\/autodiscover|\/id_rsa|\/web\.config|\/wp-config|\/config\.php|\/shell|\/eval-stdin|\.sql$|\.bak$|\.swp$/) # rule sensitive
	leak = (status == 200 && lp ~ /\/\.env|\/\.git\/|\/\.svn\/|\/id_rsa|\.sql$|\.bak$|\/\.aws\/|\/wp-config\.php.|\/phpinfo/) # rule secret
	inject = (tolower(target) ~ /\.\.\/|\.\.%2f|%2e%2e|union(\+|%20| )+(all(\+|%20| )+)?select|<script|%3cscript|\/etc\/passwd|etc%2fpasswd|\$\{jndi|%24%7bjndi|base64_decode|eval\(|eval%28|\/bin\/(ba)?sh|wget(\+|%20)http|curl(\+|%20)http|information_schema|sleep\([0-9]|sleep%28[0-9]|benchmark\(/) # rule inject
	dead = (!bot && method == "GET" && (status == 404 || status == 410) && !sensitive && !inject && ref != "-" && ref != "")
	deadfrom = refhost
	if (dead && refhost == "（站内跳转）") {
		deadfrom = ref
		sub(/^[a-zA-Z]+:\/\//, "", deadfrom)
		sub(/^[^\/?#]*/, "", deadfrom)
		sub(/[?#].*/, "", deadfrom)
		if (deadfrom == "") deadfrom = "/"
	}
}
function dayc(k,   key, u) {
	key = k SUBSEP day
	DR[key]++; DB[key] += bytes
	if (status >= 400 && status < 500) DE4[key]++
	if (status >= 500) DE5[key]++
	if (a == 0) { HR[k, hour]++; if (page) HP[k, hour]++ }
	if (bot) { DBO[key]++; return }
	if (!page) return
	DP[key]++
	u = key SUBSEP ip "|" substr(ua, 1, 80)
	if (!(u in DUS)) { DUS[u] = 1; DU[key]++ }
	u = key SUBSEP ip
	if (!(u in DIS)) { DIS[u] = 1; DI[key]++ }
}
function rangec(r, k,   key, pre, u) {
	key = r SUBSEP k
	RR[key]++; RB[key] += bytes
	if (status >= 400 && status < 500) RE4[key]++
	if (status >= 500) RE5[key]++
	pre = (k == "*") ? site : ""
	T[key, "status", status]++
	T[key, "dir", pre dir]++
	if (status >= 400) T[key, "errpage", status " " pre path]++
	if (leak) T[key, "leak", status " " pre path]++
	if (dead) T[key, "dead", pre path " ← " deadfrom]++
	if (bot) { RBO[key]++; T[key, "bot", botname]++; return }
	T[key, "ip", ip]++
	if (!page) return
	RP[key]++
	T[key, "page", pre path]++
	T[key, "referer", refhost]++
	T[key, "device", device]++
	u = key SUBSEP ip "|" substr(ua, 1, 80)
	if (!(u in RUS)) { RUS[u] = 1; RU[key]++ }
	u = key SUBSEP ip
	if (!(u in RIS)) { RIS[u] = 1; RI[key]++ }
}
function profile(r,   key, pk) {
	key = r SUBSEP ip
	IQ[key]++
	if (page) { IPV[key]++; VIS[r, site, ip]++ }
	if (status >= 400 && status < 500) IE4[key]++
	if (status >= 500) IE5[key]++
	if (method == "POST") IPO[key]++
	if (sensitive) ISE[key]++
	if (inject) IIN[key]++
	if (login) ILO[key]++
	if (!fwd) IDI[key]++
	pk = key SUBSEP path
	if (!(pk in IPP)) IPN[key]++
	IPP[pk]++
	ISI[key, site]++
	if (IM[key] == minute) IMC[key]++; else { IM[key] = minute; IMC[key] = 1 }
	if (IMC[key] > IPK[key]) IPK[key] = IMC[key]
	if (!(key in IFI) || stamp < IFI[key]) IFI[key] = stamp
	if (stamp >= ILA[key]) { ILA[key] = stamp; IUA[key] = ua; IBN[key] = botname }
}
BEGIN {
	split("Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec", mn, " ")
	for (i = 1; i <= 12; i++) mon[mn[i]] = sprintf("%02d", i)
	n = split(days, dl, " ")
	for (i = 1; i <= n; i++) age[dl[i]] = i - 1
	maxlines = 3000000
}
/^@@MIAO_SITE / { site = substr($0, 13); sites[site] = 1; next }
{
	L[site]++
	lines++
	b = index($0, "[")
	if (!b) { X[site]++; next }
	ts = substr($0, b + 1, 20)
	if (!(substr(ts, 1, 11) in age)) next
	a = age[substr(ts, 1, 11)]
	nq = split($0, q, "\"")
	if (nq < 7) { X[site]++; next }
	day = substr(ts, 8, 4) "-" mon[substr(ts, 4, 3)] "-" substr(ts, 1, 2)
	hour = substr(ts, 13, 2) + 0
	minute = day " " substr(ts, 13, 5)
	stamp = day " " substr(ts, 13, 8)
	split(q[1], f, " "); ip = f[1]
	split(q[2], rq, " "); method = rq[1]; target = rq[2]
	split(q[3], st, " "); status = st[1] + 0; bytes = st[2] + 0
	ref = q[4]; ua = q[6]
	fwd = 0
	if (nq >= 9 && q[8] != "-" && q[8] != "") {
		split(q[8], xf, ","); c = xf[1]; gsub(/ /, "", c)
		if (c != "") { ip = c; XF[site]++; fwd = 1 }
	}
	classify()
	dayc(site); dayc("*")
	detail = (lines <= maxlines)
	if (!detail) trunc = 1
	if (a < 1) { rangec(1, site); rangec(1, "*"); if (detail) profile(1) }
	if (a < 7) { rangec(7, site); rangec(7, "*"); if (detail) profile(7) }
	rangec(30, site); rangec(30, "*"); if (detail) profile(30)
}
END {
	t = "\t"
	for (x in DR) { split(x, k, SUBSEP); print "D" t k[1] t k[2] t DR[x] t DP[x] + 0 t DU[x] + 0 t DI[x] + 0 t DBO[x] + 0 t sprintf("%.0f", DB[x]) t DE4[x] + 0 t DE5[x] + 0 }
	for (x in HR) { split(x, k, SUBSEP); print "H" t k[1] t k[2] t HR[x] t HP[x] + 0 }
	for (x in RR) { split(x, k, SUBSEP); print "S" t k[1] t k[2] t RR[x] t RP[x] + 0 t RU[x] + 0 t RI[x] + 0 t RBO[x] + 0 t sprintf("%.0f", RB[x]) t RE4[x] + 0 t RE5[x] + 0 }
	for (x in T) { split(x, k, SUBSEP); print "T" t k[1] t k[2] t k[3] t T[x] t k[4] }
	for (s in sites) print "V" t s t L[s] + 0 t XF[s] + 0 t X[s] + 0
	# The IPs worth describing, per range: flagged ones first, then the busiest.
	for (x in IQ) {
		split(x, k, SUBSEP); r = k[1]
		flag = (ISE[x] > 0 || IIN[x] > 0 || ILO[x] >= 5 || (IQ[x] >= 30 && IE4[x] * 2 >= IQ[x]) || IPK[x] >= 60)
		score = (flag ? 1000000000000 : 0) + IQ[x]
		n = NS[r] + 0
		if (n < 80 || score > SS[r, n]) {
			if (n < 80) n++
			i = n
			while (i > 1 && SS[r, i - 1] < score) { SS[r, i] = SS[r, i - 1]; SK[r, i] = SK[r, i - 1]; i-- }
			SS[r, i] = score; SK[r, i] = x
			NS[r] = n
		}
	}
	for (r in NS) for (i = 1; i <= NS[r]; i++) {
		x = SK[r, i]; SEL[x] = 1; split(x, k, SUBSEP)
		print "I" t k[1] t k[2] t IQ[x] t IPV[x] + 0 t IE4[x] + 0 t IE5[x] + 0 t IPO[x] + 0 t IPN[x] + 0 t ISE[x] + 0 t IIN[x] + 0 t ILO[x] + 0 t IPK[x] + 0 t IFI[x] t ILA[x] t IBN[x] t IDI[x] + 0 t IUA[x]
	}
	for (x in IPP) { split(x, k, SUBSEP); if ((k[1] SUBSEP k[2]) in SEL) print "P" t k[1] t k[2] t "path" t IPP[x] t k[3] }
	for (x in ISI) { split(x, k, SUBSEP); if ((k[1] SUBSEP k[2]) in SEL) print "P" t k[1] t k[2] t "site" t ISI[x] t k[3] }
	for (x in VIS) { split(x, k, SUBSEP); print "A" t k[1] t k[2] t "visitor" t VIS[x] t k[3] }
	if (trunc) print "X" t "日志超过 300 万行，IP 明细和地区只统计了前 300 万行"
}'

# Rankings are sorted here and cut to size: 20 per ranking, 5 per IP,
# 3000 visitors per site.
emit | nice -n 10 awk -v days="$DAYLIST" "$PROG" |
	sort -t "$TAB" -k1,1 -k2,2 -k3,3 -k4,4 -k5,5nr |
	awk -F '\t' '$1 == "T" || $1 == "P" || $1 == "A" { k = $1 FS $2 FS $3 FS $4; lim = ($1 == "A") ? 3000 : ($1 == "P" ? 5 : 20); if (++n[k] <= lim) print; next } { print }'
