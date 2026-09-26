package visits

import (
	"regexp"
	"strings"
)

// These rules decide what a request is. scripts/sitelogs.sh applies the
// same patterns on the server (each regex there is marked "rule <name>");
// a test keeps the two in step, so EdgeOne logs counted here and server
// logs counted there mean the same thing.
const (
	// A crawler, monitor or script rather than a person (lower-cased UA).
	botPattern = `bot|spider|crawl|slurp|curl|wget|python|go-http|java/|httpclient|okhttp|scrapy|headless|scan|monitor|uptime|zgrab|nmap|masscan|libwww|axios|node-fetch|feed|preview|sqlmap|nikto|nuclei|dirbuster|gobuster|ffuf|fuzz|hydra|acunetix|nessus|openvas|burp`
	// The crawler's own name in its UA (leftmost-longest match).
	botNamePattern = `[A-Za-z0-9_.-]*([Bb]ot|[Ss]pider|[Cc]rawler)[A-Za-z0-9_.-]*`
	// Files that are not a page view (lower-cased path).
	staticPattern = `\.(css|js|mjs|map|png|jpe?g|gif|svg|ico|webp|avif|bmp|woff2?|ttf|eot|otf|mp4|webm|mp3|wav|ogg|m4a|pdf|zip|gz|rar|7z|txt|xml|json|wasm|webmanifest)$`
	mobilePattern = `Mobile|Android|iPhone|iPad|HarmonyOS`
	// What scanners look for (lower-cased path); counted when the server
	// answers 4xx, i.e. the probe did not find it.
	sensitivePattern = `wp-login\.php|xmlrpc\.php|/wp-admin|/\.env|/\.git|/\.svn|/\.ds_store|/\.aws|/\.vscode|/\.idea|phpmyadmin|/pma/|/adminer|/manager/html|/cgi-bin/|/vendor/phpunit|/actuator|/solr/|/hnap1|/boaform|/phpinfo|/server-status|/druid/|/nacos|/_ignition|/owa/|/autodiscover|/id_rsa|/web\.config|/wp-config|/config\.php|/shell|/eval-stdin|\.sql$|\.bak$|\.swp$`
	// Files that must never be served; a 200 means they leaked.
	secretPattern = `/\.env|/\.git/|/\.svn/|/id_rsa|\.sql$|\.bak$|/\.aws/|/wp-config\.php.|/phpinfo`
	// Attack payloads in the request target (lower-cased, with query).
	injectPattern = `\.\./|\.\.%2f|%2e%2e|union(\+|%20| )+(all(\+|%20| )+)?select|<script|%3cscript|/etc/passwd|etc%2fpasswd|\$\{jndi|%24%7bjndi|base64_decode|eval\(|eval%28|/bin/(ba)?sh|wget(\+|%20)http|curl(\+|%20)http|information_schema|sleep\([0-9]|sleep%28[0-9]|benchmark\(`
	// Login forms (lower-cased path); a POST counts as a failed attempt
	// when refused (401/403/429) or on WordPress, which always answers 200.
	loginPattern     = `login|signin|sign-in|logon|wp-login\.php|xmlrpc\.php|passport|/auth/`
	wordpressPattern = `wp-login\.php|xmlrpc\.php`
)

var (
	botRe       = regexp.MustCompile(botPattern)
	botNameRe   = longest(botNamePattern)
	staticRe    = regexp.MustCompile(staticPattern)
	mobileRe    = regexp.MustCompile(mobilePattern)
	sensitiveRe = regexp.MustCompile(sensitivePattern)
	secretRe    = regexp.MustCompile(secretPattern)
	injectRe    = regexp.MustCompile(injectPattern)
	loginRe     = regexp.MustCompile(loginPattern)
	wordpressRe = regexp.MustCompile(wordpressPattern)
	schemeRe    = regexp.MustCompile(`^[a-zA-Z]+://`)
)

// longest matches the way awk does: the leftmost, then longest, match.
func longest(p string) *regexp.Regexp {
	re := regexp.MustCompile(p)
	re.Longest()
	return re
}

// lower lower-cases ASCII only, as awk's tolower does.
func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// botWord names the program behind a UA: the first word the bot rule
// matches, without its version ("Mozilla/5.0 zgrab/0.x" is zgrab), else
// the first word.
func botWord(ua string) string {
	for _, w := range strings.FieldsFunc(ua, func(c rune) bool { return c == ' ' || c == ';' || c == '(' || c == ')' }) {
		if botRe.MatchString(lower(w)) {
			name, _, _ := strings.Cut(w, "/")
			return name
		}
	}
	name := ua
	if i := strings.IndexAny(ua, "/ ;("); i >= 0 {
		name = ua[:i]
	}
	return name
}

// request is one log line, classified.
type request struct {
	site, ip, method, path, target, ref, ua string
	status                                  int
	bytes                                   int64
	bot, page, sensitive, inject, login     bool
	leak                                    bool
	botName, refHost, device, dir           string
}

// classify applies the rules to a request. path has no query; target is
// what was requested, query included.
func classify(site, method, target string, status int, ref, ua string) request {
	r := request{site: site, method: method, target: target, status: status, ref: ref, ua: ua}
	r.path = target
	if i := strings.IndexByte(r.path, '?'); i >= 0 {
		r.path = r.path[:i]
	}
	if r.path == "" {
		r.path = "/"
	}
	lua := lower(ua)
	r.bot = ua == "-" || ua == "" || botRe.MatchString(lua)
	if r.bot {
		switch {
		case botNameRe.MatchString(ua):
			r.botName = botNameRe.FindString(ua)
		case ua == "-" || ua == "":
			r.botName = "（没有浏览器标识）"
		default:
			r.botName = botWord(ua)
		}
	}
	lp := lower(r.path)
	r.page = !r.bot && method == "GET" && ((status >= 200 && status < 300) || status == 304) &&
		!staticRe.MatchString(lp) && !strings.HasPrefix(lp, "/api/")
	r.refHost = "（直接访问）"
	if ref != "-" && ref != "" {
		h := schemeRe.ReplaceAllString(ref, "")
		if i := strings.IndexAny(h, "/?#:"); i >= 0 {
			h = h[:i]
		}
		if h == site || h == "" {
			r.refHost = "（站内跳转）"
		} else {
			r.refHost = h
		}
	}
	r.device = "电脑"
	if mobileRe.MatchString(ua) {
		r.device = "手机/平板"
	}
	// The first directory: /posts/x -> /posts/, /about -> /.
	r.dir = "/"
	if rest := strings.TrimPrefix(r.path, "/"); strings.Contains(rest, "/") {
		r.dir = "/" + rest[:strings.IndexByte(rest, '/')] + "/"
	}
	r.login = method == "POST" && loginRe.MatchString(lp) && (status == 401 || status == 403 || status == 429 || wordpressRe.MatchString(lp))
	// A refused login is counted as one, not also as a probe.
	r.sensitive = !r.login && status >= 400 && status < 500 && sensitiveRe.MatchString(lp)
	r.leak = status == 200 && secretRe.MatchString(lp)
	r.inject = injectRe.MatchString(lower(target))
	return r
}
