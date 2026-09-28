// Package btpaneltest is a stand-in for 宝塔面板's API, for tests. It
// checks requests the way the panel does (the API switch, the IP
// whitelist, request_token, the actions each module has, and the fields
// each action reads), keeps sites, domains, certificates, reverse
// proxies, files, backups and databases, and answers in the panel's
// shapes and words (宝塔 11.8). Like the panel it tests web server
// configs after writing them and puts the old file back when the test
// fails; a config containing BadDirective fails. Actions it does not
// implement answer as unknown ones do.
//
// Tests serve it with httptest and reach it through Transport, which
// plays the SSH tunnel:
//
//	f := btpaneltest.New()
//	srv := httptest.NewServer(f)
//	c := btpanel.New(btpaneltest.Transport(srv.URL), 8888, f.Key, "http")
package btpaneltest

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5" //nolint:gosec // the panel's token scheme
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BadDirective makes the fake's nginx -t fail when any config contains it.
const BadDirective = "not_a_directive"

// Site is a row of the panel's sites table.
type Site struct {
	ID          int
	Name        string
	Path        string
	Running     bool
	PS          string
	PHP         string // 00 for static, or a version such as 82
	ProjectType string
	TypeID      int
	AddTime     string
	EDate       string
}

// Domain is a row of the domain table.
type Domain struct {
	ID, SiteID int
	Name       string
	Port       int
	AddTime    string
}

// Cert is a site's certificate files (vhost/cert/<site>).
type Cert struct{ Key, Cert string }

// Proxy is an entry of data/proxyfile.json.
type Proxy struct {
	Name, Site, Dir, Target, Host    string
	Type, Cache, CacheTime, Advanced int
	SubFilter                        []map[string]string
}

// Backup is a row of the backup table.
type Backup struct {
	ID, Type, PID  int
	Name, Filename string
	Size           int64
	AddTime, PS    string
}

// Database is a row of the databases table.
type Database struct {
	ID, PID                                       int
	Name, Username, Password, Accept, Charset, PS string
	AddTime                                       string
	Restored                                      string // the backup file last imported
}

// Request is a request that passed the API check.
type Request struct {
	Action string     // module/action
	Form   url.Values // without the credentials and action
}

// Fake is the panel.
type Fake struct {
	mu           sync.Mutex
	Key          string   // the key 面板设置 → API 接口 shows
	APIEnabled   bool     // the API switch
	AllowedIPs   []string // the API whitelist
	Version      string
	PHPVersions  []string // installed PHP versions, such as 82
	PageSize     int      // the site list's page size, the user's choice (data/limit1.pl)
	NginxInit    bool     // /etc/init.d/nginx exists: SaveFileBody tests configs only then
	NginxRunning bool
	Sites        []*Site
	Domains      []*Domain
	SSL          map[string]*Cert // by site name
	Proxies      []*Proxy
	Files        map[string]string // path → content
	Backups      []*Backup
	Databases    []*Database
	Requests     []Request
	// Crashes says why the fake answered with its 404 page: the panel
	// reads some fields without checking, and turns the exception into
	// that page for API callers (error_500 → error_not_login).
	Crashes []string

	failures int
	nextID   int
	clock    int64
	mtimes   map[string]int64
	caCert   *x509.Certificate
	caKey    *ecdsa.PrivateKey
	caPEM    string
}

// Rewrite templates the fake ships (panel/rewrite/nginx).
var templates = map[string]string{
	"default":   "",
	"wordpress": "location /\n{\n\t try_files $uri $uri/ /index.php?$args;\n}\n\nrewrite /wp-admin$ $scheme://$host$uri/ permanent;",
	"thinkphp":  "location ~* (runtime|application)/{\n\treturn 403;\n}\nlocation / {\n\tif (!-e $request_filename){\n\t\trewrite  ^(.*)$  /index.php?s=$1  last;   break;\n\t}\n}",
	"laravel5":  "location / {\n    try_files $uri $uri/ /index.php?$query_string;\n}",
}

// New makes a panel with the API on for 127.0.0.1, nginx running and no
// sites.
func New() *Fake {
	f := &Fake{
		Key: "fakeBtApiKey0123456789abcdefGHIJ", APIEnabled: true, AllowedIPs: []string{"127.0.0.1"}, Version: "11.8.0",
		PHPVersions: []string{"74", "82"}, PageSize: 20, NginxInit: true, NginxRunning: true,
		SSL: map[string]*Cert{}, Files: map[string]string{}, mtimes: map[string]int64{}, clock: 1759000000,
	}
	for name, body := range templates {
		f.write("/www/server/panel/rewrite/nginx/"+name+".conf", body)
	}
	return f
}

// Lock and Unlock guard the fake's state for tests that read it.
func (f *Fake) Lock()   { f.mu.Lock() }
func (f *Fake) Unlock() { f.mu.Unlock() }

// AddSite adds a running PHP 8.2 site under /www/wwwroot, as AddSite would.
func (f *Fake) AddSite(name string) *Site {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addSite(name, "/www/wwwroot/"+name, 80, "82", "")
}

// AddDatabase adds a database with a user of the same name.
func (f *Fake) AddDatabase(name string) *Database {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := &Database{ID: f.id(), Name: name, Username: name, Password: "secret-" + name, Accept: "127.0.0.1", Charset: "utf8mb4", AddTime: now()}
	f.Databases = append(f.Databases, d)
	return d
}

// Site returns a site by name.
func (f *Fake) Site(name string) *Site {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.siteByName(name)
}

// WriteFile changes a file as someone else would, outside the API.
func (f *Fake) WriteFile(path, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.write(path, content)
}

// Transport sends every request, whatever its host, to the test server at
// serverURL, as the SSH tunnel sends 127.0.0.1:<port> to the panel. It
// accepts the test server's certificate, as the real transport accepts
// the panel's self-signed one.
func Transport(serverURL string) http.RoundTripper {
	u, err := url.Parse(serverURL)
	if err != nil {
		panic(err)
	}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, u.Host)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test server
	}
}

// NewCert makes a self-signed certificate and its key, in PEM.
func NewCert(domains ...string) (certPEM, keyPEM string) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: domains[0]}, DNSNames: domains,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	return pemText("CERTIFICATE", der), keyText(key)
}

func pemText(kind string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}))
}

func keyText(key *ecdsa.PrivateKey) string {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	return pemText("PRIVATE KEY", der)
}

func now() string { return time.Now().Format("2006-01-02 15:04:05") }

func md5hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // the panel's token scheme
	return hex.EncodeToString(sum[:])
}

func (f *Fake) id() int { f.nextID++; return f.nextID }

func (f *Fake) write(path, content string) {
	f.Files[path] = content
	f.clock++
	f.mtimes[path] = f.clock
}

func (f *Fake) mtime(path string) string {
	if t, ok := f.mtimes[path]; ok {
		return strconv.FormatInt(t, 10)
	}
	return "1700000000"
}

func vhostPath(site string) string   { return "/www/server/panel/vhost/nginx/" + site + ".conf" }
func rewritePath(site string) string { return "/www/server/panel/vhost/rewrite/" + site + ".conf" }

// sslMark is the line the panel edits a config's SSL block by.
const sslMark = "#error_page 404/404.html;"

func vhost(s *Site, port int) string {
	return fmt.Sprintf(`server
{
    listen %d;
    server_name %s;
    index index.php index.html index.htm default.php default.htm default.html;
    root %s;

    #SSL-START SSL相关配置，请勿删除或修改下一行带注释的404规则
    %s
    #SSL-END

    #PHP-INFO-START  PHP引用配置，可以注释或修改
    include enable-php-%s.conf;
    #PHP-INFO-END

    #REWRITE-START URL重写规则引用,修改后将导致面板设置的伪静态规则失效
    include /www/server/panel/vhost/rewrite/%s.conf;
    #REWRITE-END

    access_log  /www/wwwlogs/%s.log;
    error_log  /www/wwwlogs/%s.error.log;
}
`, port, s.Name, s.Path, sslMark, s.PHP, s.Name, s.Name, s.Name)
}

func (f *Fake) addSite(name, path string, port int, php, ps string) *Site {
	s := &Site{ID: f.id(), Name: name, Path: path, Running: true, PS: ps, PHP: php, ProjectType: "PHP", AddTime: now(), EDate: "0000-00-00"}
	f.Sites = append(f.Sites, s)
	f.Domains = append(f.Domains, &Domain{ID: f.id(), SiteID: s.ID, Name: name, Port: port, AddTime: now()})
	f.write(vhostPath(name), vhost(s, port))
	f.write(rewritePath(name), "")
	f.write(path+"/index.html", "<h1>"+name+"</h1>\n")
	return s
}

func (f *Fake) siteByName(name string) *Site {
	for _, s := range f.Sites {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func (f *Fake) siteByID(id string) *Site {
	for _, s := range f.Sites {
		if strconv.Itoa(s.ID) == id {
			return s
		}
	}
	return nil
}

func (f *Fake) siteDomains(id int) []*Domain {
	var out []*Domain
	for _, d := range f.Domains {
		if d.SiteID == id {
			out = append(out, d)
		}
	}
	return out
}

var serverNameRe = regexp.MustCompile(`server_name\s+[^;]*;`)

// syncServerName rewrites server_name after the domains change, as the
// panel does.
func (f *Fake) syncServerName(s *Site) {
	var names []string
	for _, d := range f.siteDomains(s.ID) {
		names = append(names, d.Name)
	}
	f.write(vhostPath(s.Name), serverNameRe.ReplaceAllString(f.Files[vhostPath(s.Name)], "server_name "+strings.Join(names, " ")+";"))
}

// nginxT is the fake's nginx -t over the whole configuration, as the
// panel's checkWebConfig runs it.
func (f *Fake) nginxT() error {
	var paths []string
	for p := range f.Files {
		if strings.HasPrefix(p, "/www/server/panel/vhost/") && strings.HasSuffix(p, ".conf") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		if i := strings.Index(f.Files[p], BadDirective); i >= 0 {
			line := strings.Count(f.Files[p][:i], "\n") + 1
			return fmt.Errorf("nginx: [emerg] unknown directive \"%s\" in %s:%d\nnginx: configuration file /www/server/nginx/conf/nginx.conf test failed", BadDirective, p, line)
		}
	}
	return nil
}

func htmlLines(err error) string { return strings.ReplaceAll(err.Error(), "\n", "<br>") }

// args is a request's fields as the panel's actions see them.
type args struct {
	v       url.Values
	missing string
}

func (a *args) has(k string) bool   { return a.v.Has(k) }
func (a *args) get(k string) string { return a.v.Get(k) }

// need reports whether all keys are there, noting the first missing one:
// the panel reads these without checking, and raises.
func (a *args) need(keys ...string) bool {
	for _, k := range keys {
		if !a.v.Has(k) {
			a.missing = k
			return false
		}
	}
	return true
}

// int reads a field the panel passes to int(), which raises on anything else.
func (a *args) int(k string) (int, bool) {
	if !a.need(k) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(a.get(k)))
	if err != nil {
		a.missing = k + " (not a number)"
	}
	return n, err == nil
}

type reply = map[string]any

func msg(ok bool, text string) reply { return reply{"status": ok, "msg": text} }

// The panel's 404 page (public.error_404), sent for callers it does not
// accept and for actions that raise.
const page404 = "<html>\r\n<head><title>404 Not Found</title></head>\r\n<body>\r\n<center><h1>404 Not Found</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"

func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(page404))
}

type handler func(f *Fake, a *args) any

var handlers map[string]handler

func init() {
	handlers = map[string]handler{
		"system/GetSystemTotal":   (*Fake).systemTotal,
		"system/ServiceAdmin":     (*Fake).serviceAdmin,
		"data/getData":            (*Fake).getData,
		"data/getKey":             (*Fake).getKey,
		"site/AddSite":            (*Fake).siteAdd,
		"site/DeleteSite":         (*Fake).siteDelete,
		"site/SiteStart":          (*Fake).siteStart,
		"site/SiteStop":           (*Fake).siteStop,
		"site/AddDomain":          (*Fake).domainAdd,
		"site/DelDomain":          (*Fake).domainDel,
		"site/GetSSL":             (*Fake).getSSL,
		"site/SetSSL":             (*Fake).setSSL,
		"site/HttpToHttps":        (*Fake).httpToHTTPS,
		"site/CloseToHttps":       (*Fake).closeToHTTPS,
		"site/CloseSSLConf":       (*Fake).closeSSL,
		"acme/apply_cert_api":     (*Fake).applyCert,
		"site/SetPHPVersion":      (*Fake).phpSet,
		"site/GetProxyList":       (*Fake).proxyList,
		"site/CreateProxy":        (*Fake).proxyCreate,
		"site/ModifyProxy":        (*Fake).proxyModify,
		"site/RemoveProxy":        (*Fake).proxyRemove,
		"site/GetRewriteList":     (*Fake).rewriteList,
		"files/GetFileBody":       (*Fake).fileBody,
		"files/SaveFileBody":      (*Fake).fileSave,
		"site/GetSiteLogs":        func(f *Fake, a *args) any { return f.siteLog(a, ".log") },
		"site/get_site_errlog":    func(f *Fake, a *args) any { return f.siteLog(a, ".error.log") },
		"site/ToBackup":           (*Fake).siteBackup,
		"site/DelBackup":          (*Fake).siteDelBackup,
		"database/ToBackup":       (*Fake).dbBackup,
		"database/DelBackup":      (*Fake).dbDelBackup,
		"database/InputSql":       (*Fake).dbImport,
		"database/AddDatabase":    (*Fake).dbAdd,
		"database/DeleteDatabase": (*Fake).dbDelete,
	}
}

// The panel's modules (routes) the fake has; unknown actions of these get
// the dispatcher's answer, unknown routes the 404 page.
var modules = map[string]bool{"system": true, "data": true, "site": true, "files": true, "database": true, "acme": true}

// ServeHTTP answers one API request.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// A GET would render a page; the API is used with POST.
	if r.Method != http.MethodPost || r.ParseForm() != nil {
		notFound(w)
		return
	}
	// The API check (class/common.py get_sk).
	if !f.APIEnabled || !r.Form.Has("request_token") || !r.Form.Has("request_time") {
		notFound(w)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	switch {
	case f.failures >= 20:
		send(w, msg(false, "连续20次验证失败,禁止1小时"))
		return
	case !slices.Contains(f.AllowedIPs, ip):
		f.failures++
		send(w, msg(false, "IP校验失败,您的访问IP为["+ip+"]"))
		return
	case r.Form.Get("request_token") != md5hex(r.Form.Get("request_time")+md5hex(f.Key)):
		f.failures++
		send(w, msg(false, "密钥校验失败"))
		return
	}
	f.failures = 0
	module, action := strings.Trim(r.URL.Path, "/"), r.Form.Get("action")
	form := url.Values{}
	for k, v := range r.Form {
		if k != "request_token" && k != "request_time" && k != "action" {
			form[k] = v
		}
	}
	f.Requests = append(f.Requests, Request{Action: module + "/" + action, Form: form})
	h, ok := handlers[module+"/"+action]
	switch {
	case !modules[module]:
		notFound(w)
		return
	case !ok:
		// run_exec: an action not in the module's list.
		send(w, msg(false, "指定参数无效!"))
		return
	}
	a := &args{v: form}
	out := h(f, a)
	if a.missing != "" {
		f.Crashes = append(f.Crashes, module+"/"+action+": "+a.missing)
		notFound(w)
		return
	}
	send(w, out)
}

func send(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func (f *Fake) systemTotal(*args) any {
	return reply{"memTotal": 1987, "memFree": 612, "memBuffers": 80, "memCached": 700, "memRealUsed": 595, "cpuNum": 2,
		"cpuRealUsed": 3.2, "time": "12天", "system": "Ubuntu 24.04.1 LTS x86_64(Py3.7.16)", "isuser": 0, "isport": false,
		"version": f.Version}
}

func (f *Fake) serviceAdmin(a *args) any {
	if !a.has("name") {
		return msg(false, "请传入name参数")
	}
	if !a.has("type") {
		return msg(false, "请传入type参数")
	}
	if a.get("name") == "nginx" {
		if err := f.nginxT(); err != nil {
			return msg(false, "Nginx配置规则错误: <br><a style='color:red;'>"+htmlLines(err)+"</a>")
		}
		switch a.get("type") {
		case "stop":
			f.NginxRunning = false
		default:
			// Whatever the type, the panel starts nginx if it is down.
			f.NginxRunning = true
		}
	}
	return msg(true, "执行成功!")
}

// pager is the page bar getData sends (page.py), with the row count.
func pager(p, count int) string {
	return fmt.Sprintf("<div><span class='Pcurrent'>%d</span><span class='Pcount'>共%d条</span></div>", p, count)
}

func pageOf[T any](rows []T, p, limit int) []T {
	start := (p - 1) * limit
	if start >= len(rows) {
		return []T{}
	}
	return rows[start:min(start+limit, len(rows))]
}

func (f *Fake) getData(a *args) any {
	if !a.need("table") {
		return nil
	}
	p, limit := 1, 20
	if a.has("p") {
		if p, _ = a.int("p"); p < 1 {
			p = 1
		}
	}
	if a.has("limit") {
		limit, _ = a.int("limit")
	}
	if a.missing != "" {
		return nil
	}
	switch a.get("table") {
	case "sites":
		// For this table the panel saves the page size it is given as the
		// user's own, and uses theirs when none is given.
		if a.has("limit") {
			f.PageSize = limit
		}
		limit = f.PageSize
		var rows []reply
		for i := len(f.Sites) - 1; i >= 0; i-- { // id desc
			rows = append(rows, f.siteRow(f.Sites[i]))
		}
		return reply{"data": pageOf(rows, p, limit), "page": pager(p, len(rows)), "where": "", "search_history": []any{}}
	case "domain":
		var rows []reply
		for _, d := range f.Domains {
			// GetWhere: pid=? OR name=?
			if strconv.Itoa(d.SiteID) == a.get("search") || d.Name == a.get("search") {
				rows = append(rows, reply{"id": d.ID, "pid": d.SiteID, "name": d.Name, "port": d.Port, "addtime": d.AddTime, "cn_name": d.Name})
			}
		}
		if a.has("list") {
			return rows
		}
		return reply{"data": pageOf(rows, p, limit), "page": pager(p, len(rows))}
	case "backup":
		if !a.has("search") {
			return reply{"data": []any{}, "page": pager(p, 0)}
		}
		typ, ok := a.int("type")
		if !ok {
			return nil
		}
		var rows []reply
		for i := len(f.Backups) - 1; i >= 0; i-- {
			b := f.Backups[i]
			if b.Type == typ && strconv.Itoa(b.PID) == a.get("search") {
				ps := b.PS
				if ps == "" {
					ps = "手动备份"
				}
				rows = append(rows, reply{"id": b.ID, "pid": b.PID, "name": b.Name, "filename": b.Filename, "addtime": b.AddTime,
					"size": b.Size, "ps": ps, "cron_id": 0, "local": b.Filename, "localexist": 1, "backup_status": 0})
			}
		}
		return reply{"data": pageOf(rows, p, limit), "page": pager(p, len(rows))}
	case "databases":
		var rows []reply
		for i := len(f.Databases) - 1; i >= 0; i-- {
			d := f.Databases[i]
			n := 0
			for _, b := range f.Backups {
				if b.Type == 1 && b.PID == d.ID {
					n++
				}
			}
			rows = append(rows, reply{"id": d.ID, "pid": d.PID, "sid": 0, "db_type": 0, "name": d.Name, "username": d.Username,
				"password": d.Password, "accept": d.Accept, "ps": d.PS, "addtime": d.AddTime, "type": "MySQL",
				"conn_config": reply{}, "backup_count": n})
		}
		return reply{"data": pageOf(rows, p, limit), "page": pager(p, len(rows))}
	}
	return reply{"data": []any{}, "page": pager(p, 0)}
}

func (f *Fake) siteRow(s *Site) reply {
	status, php := "0", "静态"
	if s.Running {
		status = "1"
	}
	if s.PHP != "00" {
		php = s.PHP[:1] + "." + s.PHP[1:]
	}
	var ssl any = -1
	if strings.Contains(f.Files[vhostPath(s.Name)], "ssl_certificate") && f.SSL[s.Name] != nil {
		ssl = certData(f.SSL[s.Name].Cert)
	}
	backups := 0
	for _, b := range f.Backups {
		if b.Type == 0 && b.PID == s.ID {
			backups++
		}
	}
	return reply{"id": s.ID, "name": s.Name, "path": s.Path, "status": status, "index": "", "ps": xss(s.PS),
		"addtime": s.AddTime, "edate": s.EDate, "type_id": s.TypeID, "project_type": s.ProjectType, "rname": "",
		"domain": len(f.siteDomains(s.ID)), "ssl": ssl, "php_version": php, "backup_count": backups,
		"proxy": f.proxiesOf(s.Name) != nil, "redirect": false}
}

// xss is how the panel stores and sends free text (xssencode2, xsssec2).
func xss(s string) string { return html.EscapeString(s) }

func (f *Fake) getKey(a *args) any {
	for _, k := range []string{"table", "key", "id"} {
		if !a.has(k) {
			return msg(false, "缺少参数! "+k)
		}
	}
	if a.get("table") == "config" && a.get("key") == "sites_path" && a.get("id") == "1" {
		return "/www/wwwroot"
	}
	return msg(false, "未获取到数据!")
}

var (
	domainRe = regexp.MustCompile(`^([\w\-*]+\.)+[\w\-]+$`)
	portRe   = regexp.MustCompile(`^\d+$`)
)

func (f *Fake) siteAdd(a *args) any {
	if err := f.nginxT(); err != nil {
		return msg(false, `ERROR: 检测到配置文件有错误,请先排除后再操作<br><br><a style="color:red;">`+htmlLines(err)+"</a>")
	}
	if !a.need("path") {
		return nil
	}
	path := strings.TrimRight(strings.ReplaceAll(a.get("path"), "//", "/"), "/")
	if strings.ContainsAny(path, " \n") {
		return msg(false, "目录中不能包含空格或换行符，请重新选择！")
	}
	if !a.need("webname") {
		return nil
	}
	var menu struct {
		Domain     string   `json:"domain"`
		DomainList []string `json:"domainlist"`
	}
	if json.Unmarshal([]byte(a.get("webname")), &menu) != nil {
		return msg(false, "webname参数格式不正确，应该是可被解析的JSON字符串")
	}
	name := strings.ToLower(strings.Split(strings.TrimSpace(menu.Domain), ":")[0])
	port, ok := a.int("port")
	if !ok {
		return nil
	}
	if port < 1 || port > 65535 {
		return msg(false, "端口范围不合法")
	}
	php := "00"
	if a.has("version") && a.get("version") != "" {
		php = a.get("version")
	}
	if php != "00" && !slices.Contains(f.PHPVersions, php) {
		return msg(false, "指定PHP版本不存在!")
	}
	if !domainRe.MatchString(name) {
		return msg(false, "主域名格式不正确")
	}
	if strings.Contains(name, "*") {
		return msg(false, "主域名不能为泛解析")
	}
	for _, d := range f.Domains {
		if d.Name == name && d.Port == port {
			return msg(false, "您添加的域名已存在")
		}
	}
	if !a.need("ps", "ftp", "sql") {
		return nil
	}
	siteName := name
	if f.siteByName(siteName) != nil {
		siteName += "_" + strconv.Itoa(port)
	}
	s := f.addSite(siteName, path, port, php, a.get("ps"))
	s.TypeID, _ = strconv.Atoi(a.get("type_id"))
	if siteName != name {
		f.Domains[len(f.Domains)-1].Name = name
		f.syncServerName(s)
	}
	for _, alias := range menu.DomainList {
		host, p := alias, 80
		if h, ps, ok := strings.Cut(alias, ":"); ok {
			host, p = h, atoi(ps)
		}
		if domainRe.MatchString(host) && !f.domainTaken(host, p) {
			f.Domains = append(f.Domains, &Domain{ID: f.id(), SiteID: s.ID, Name: strings.ToLower(host), Port: p, AddTime: now()})
		}
	}
	f.syncServerName(s)
	return reply{"siteStatus": true, "siteId": s.ID, "ftpStatus": false, "databaseStatus": false, "gitStatus": false}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func (f *Fake) domainTaken(name string, port int) bool {
	for _, d := range f.Domains {
		if d.Name == name && d.Port == port {
			return true
		}
	}
	return false
}

func (f *Fake) siteDelete(a *args) any {
	if !a.need("id") {
		return nil
	}
	s := f.siteByID(a.get("id"))
	if s == nil {
		return msg(false, "指定站点不存在!")
	}
	if !a.need("webname") {
		return nil
	}
	name := a.get("webname")
	f.Proxies = slices.DeleteFunc(f.Proxies, func(p *Proxy) bool { return p.Site == name })
	for p := range f.Files {
		if p == vhostPath(name) || p == rewritePath(name) || strings.HasPrefix(p, "/www/wwwlogs/"+name) ||
			strings.HasPrefix(p, "/www/server/panel/vhost/nginx/proxy/"+name+"/") ||
			a.get("path") == "1" && strings.HasPrefix(p, s.Path+"/") {
			delete(f.Files, p)
		}
	}
	delete(f.SSL, name)
	f.Sites = slices.DeleteFunc(f.Sites, func(x *Site) bool { return x == s })
	f.Domains = slices.DeleteFunc(f.Domains, func(d *Domain) bool { return d.SiteID == s.ID })
	if a.get("database") == "1" {
		f.Databases = slices.DeleteFunc(f.Databases, func(d *Database) bool { return d.PID == s.ID })
	}
	return msg(true, "站点删除成功!")
}

func (f *Fake) siteStart(a *args) any {
	if !a.has("id") || atoi(a.get("id")) == 0 && a.get("id") != "0" {
		return msg(false, "参数错误!")
	}
	s := f.siteByID(a.get("id"))
	if s == nil {
		a.missing = "id (no such site)"
		return nil
	}
	if !a.need("name") {
		return nil
	}
	s.Running = true
	return msg(true, "站点已启用")
}

func (f *Fake) siteStop(a *args) any {
	if !a.need("id") {
		return nil
	}
	s := f.siteByID(a.get("id"))
	if s == nil {
		a.missing = "id (no such site)"
		return nil
	}
	if !s.Running {
		return msg(true, "站点已停用")
	}
	if !a.need("name") {
		return nil
	}
	s.Running = false
	return msg(true, "站点已停用")
}

func (f *Fake) domainAdd(a *args) any {
	if !a.has("webname") {
		return msg(false, "网站选择错误")
	}
	if err := f.nginxT(); err != nil {
		return msg(false, `ERROR: 检测到配置文件有错误,请先排除后再操作<br><br><a style="color:red;">`+htmlLines(err)+"</a>")
	}
	if !a.has("domain") {
		return msg(false, "请填写域名!")
	}
	if len(a.get("domain")) < 3 {
		return msg(false, "域名不能为空!")
	}
	var res []reply
	for _, domain := range strings.Split(strings.ReplaceAll(strings.TrimSpace(a.get("domain")), " ", ""), ",") {
		if domain == "" {
			continue
		}
		name, port := domain, "80"
		if h, p, ok := strings.Cut(domain, ":"); ok {
			name, port = h, p
		}
		name = strings.ToLower(name)
		switch {
		case strings.Contains(name, "*") && !strings.HasPrefix(name, "*."), !domainRe.MatchString(name):
			res = append(res, reply{"name": name, "status": false, "msg": "域名格式不正确"})
			continue
		case !portRe.MatchString(port):
			res = append(res, reply{"name": name, "status": false, "msg": "端口不合法，应该为数字"})
			continue
		case slices.Contains([]string{"21", "25", "443", "888", "8888", "8443"}, port):
			res = append(res, reply{"name": name, "status": false, "msg": "端口不合法，请勿使用常用端口，例如：ssh的22端口等"})
			continue
		case atoi(port) < 1 || atoi(port) > 65535:
			res = append(res, reply{"name": name, "status": false, "msg": "端口范围不合法"})
			continue
		}
		taken := false
		for _, d := range f.Domains {
			if d.Name == name && d.Port == atoi(port) {
				owner := ""
				for _, s := range f.Sites {
					if s.ID == d.SiteID {
						owner = s.Name
					}
				}
				res = append(res, reply{"name": name, "status": false, "msg": fmt.Sprintf("指定域名[%s]已被网站[%s]绑定过了", name, owner)})
				taken = true
			}
		}
		if taken {
			continue
		}
		if !a.need("id") {
			return nil
		}
		s := f.siteByID(a.get("id"))
		if s == nil {
			a.missing = "id (no such site)"
			return nil
		}
		f.Domains = append(f.Domains, &Domain{ID: f.id(), SiteID: s.ID, Name: name, Port: atoi(port), AddTime: now()})
		f.syncServerName(s)
		res = append(res, reply{"name": name, "status": true, "msg": "添加成功"})
	}
	return reply{"domains": res}
}

func (f *Fake) domainDel(a *args) any {
	if !a.has("id") {
		return msg(false, "请选择域名")
	}
	if !a.has("port") {
		return msg(false, "请选择端口")
	}
	if !a.need("domain") {
		return nil
	}
	var found *Domain
	for _, d := range f.Domains {
		if strconv.Itoa(d.SiteID) == a.get("id") && d.Name == a.get("domain") && strconv.Itoa(d.Port) == a.get("port") {
			found = d
		}
	}
	if found == nil {
		return msg(false, "未查询到指定域名，无法删除")
	}
	if len(f.siteDomains(found.SiteID)) == 1 {
		return msg(false, "最后一个域名不能删除!")
	}
	if !a.need("webname") {
		return nil
	}
	f.Domains = slices.DeleteFunc(f.Domains, func(d *Domain) bool { return d == found })
	if s := f.siteByID(a.get("id")); s != nil {
		f.syncServerName(s)
	}
	return msg(true, "删除成功")
}

// certData is what the panel reads from a certificate (ssl_info.py).
func certData(certPEM string) reply {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return reply{"certificate": 0}
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return reply{"certificate": 0}
	}
	subject, dns := c.Subject.CommonName, c.DNSNames
	if len(dns) > 0 {
		subject = dns[0]
	} else if subject != "" {
		dns = []string{subject}
	}
	org := ""
	if len(c.Issuer.Organization) > 0 {
		org = c.Issuer.Organization[0]
	}
	return reply{"issuer": c.Issuer.CommonName, "issuer_O": org, "notAfter": c.NotAfter.Format("2006-01-02"),
		"notBefore": c.NotBefore.Format("2006-01-02"), "dns": dns, "subject": subject,
		"endtime": int(time.Until(c.NotAfter).Hours() / 24), "id": 0, "ps": ""}
}

func (f *Fake) getSSL(a *args) any {
	if !a.has("siteName") {
		return msg(false, "未指定网站!")
	}
	name := a.get("siteName")
	conf, ok := f.Files[vhostPath(name)]
	if !ok {
		return msg(false, "指定网站配置文件不存在!")
	}
	on := strings.Contains(conf, "ssl_certificate")
	typ, key, csr, data := -1, "", "", reply{}
	if c := f.SSL[name]; c != nil {
		key, csr, data = c.Key, c.Cert, certData(c.Cert)
		if on {
			typ = 0
			if data["issuer_O"] == "Let's Encrypt" {
				typ = 1
			}
		}
	}
	var domains []reply
	if s := f.siteByName(name); s != nil {
		for _, d := range f.siteDomains(s.ID) {
			domains = append(domains, reply{"name": d.Name})
		}
	}
	// Like the panel, the answer carries the private key.
	return reply{"status": on, "oid": -1, "domain": domains, "key": key, "csr": csr, "type": typ,
		"httpTohttps": strings.Contains(conf, "HTTP_TO_HTTPS_START"), "cert_data": data, "email": "",
		"index": "", "auth_type": "http", "tls_versions": []string{"TLSv1.2", "TLSv1.3"}, "push": reply{"status": false},
		"https_mode": false}
}

func parseKey(keyPEM string) crypto.Signer {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		s, _ := k.(crypto.Signer)
		return s
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k
	}
	return nil
}

func (f *Fake) setSSL(a *args) any {
	if !a.need("key", "csr", "siteName") {
		return nil
	}
	key, cert, name := strings.TrimSpace(a.get("key")), strings.TrimSpace(a.get("csr")), a.get("siteName")
	if !strings.Contains(key, "KEY") {
		return msg(false, "秘钥错误，请检查!")
	}
	if !strings.Contains(cert, "CERTIFICATE") {
		return msg(false, "证书错误，请检查!")
	}
	block, _ := pem.Decode([]byte(cert))
	if block == nil {
		return msg(false, "证书错误,请粘贴正确的PEM格式证书!")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return msg(false, "证书错误,请粘贴正确的PEM格式证书!")
	}
	signer := parseKey(key)
	if signer == nil {
		return msg(false, "密钥错误，请检查是否为正确的PEM格式私钥")
	}
	if pub, ok := signer.Public().(interface{ Equal(crypto.PublicKey) bool }); !ok || !pub.Equal(c.PublicKey) {
		return msg(false, "密钥和证书不匹配。")
	}
	conf, ok := f.Files[vhostPath(name)]
	if !ok {
		return msg(false, "指定网站配置文件不存在!")
	}
	old := f.SSL[name]
	f.SSL[name] = &Cert{Key: key, Cert: cert}
	if !strings.Contains(conf, "ssl_certificate") {
		lines := fmt.Sprintf("%s\n    ssl_certificate    /www/server/panel/vhost/cert/%s/fullchain.pem;\n    ssl_certificate_key    /www/server/panel/vhost/cert/%s/privkey.pem;", sslMark, name, name)
		conf = strings.Replace(strings.Replace(conf, sslMark, lines, 1), "    server_name", "    listen 443 ssl;\n    server_name", 1)
		f.write(vhostPath(name), conf)
	}
	if err := f.nginxT(); err != nil {
		// The panel puts the old certificate back (the config stays).
		if old != nil {
			f.SSL[name] = old
		} else {
			delete(f.SSL, name)
		}
		return msg(false, `ERROR: <br><a style="color:red;">`+htmlLines(err)+"</a>")
	}
	return msg(true, "证书已保存!")
}

const toHTTPS = sslMark + `
    #HTTP_TO_HTTPS_START
    set $isRedcert 1;
    if ($server_port != 443) {
        set $isRedcert 2;
    }
    if ( $uri ~ /\.well-known/ ) {
        set $isRedcert 1;
    }
    if ($isRedcert != 1) {
        rewrite ^(/.*)$ https://$host$1 permanent;
    }
    #HTTP_TO_HTTPS_END`

var (
	toHTTPSRe   = regexp.MustCompile(`(?s)\n\s*#HTTP_TO_HTTPS_START.{1,900}?#HTTP_TO_HTTPS_END`)
	sslLinesRe  = regexp.MustCompile(`\s+ssl_certificate(_key)?\s+.+;[^\n]*`)
	listen443Re = regexp.MustCompile(`\s+listen\s+(.*:)?443.*;[^\n]*`)
)

func (f *Fake) httpToHTTPS(a *args) any {
	if !a.need("siteName") {
		return nil
	}
	path := vhostPath(a.get("siteName"))
	conf := f.Files[path]
	if conf != "" {
		if !strings.Contains(conf, "ssl_certificate") {
			return msg(false, "当前未开启SSL")
		}
		if !strings.Contains(conf, "HTTP_TO_HTTPS_START") {
			f.write(path, strings.Replace(conf, sslMark, toHTTPS, 1))
		}
	}
	return msg(true, "设置成功")
}

func (f *Fake) closeToHTTPS(a *args) any {
	if !a.need("siteName") {
		return nil
	}
	path := vhostPath(a.get("siteName"))
	if conf, ok := f.Files[path]; ok {
		f.write(path, toHTTPSRe.ReplaceAllString(conf, ""))
	}
	return msg(true, "设置成功")
}

func (f *Fake) closeSSL(a *args) any {
	if !a.need("siteName") {
		return nil
	}
	path := vhostPath(a.get("siteName"))
	if conf, ok := f.Files[path]; ok {
		for _, re := range []*regexp.Regexp{toHTTPSRe, sslLinesRe, listen443Re} {
			conf = re.ReplaceAllString(conf, "")
		}
		f.write(path, conf)
	}
	return msg(true, "SSL已关闭!")
}

// issue signs a certificate for domains with the fake's own "Let's
// Encrypt" CA.
func (f *Fake) issue(domains []string) (certPEM, rootPEM, keyPEM string) {
	if f.caCert == nil {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(err)
		}
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "R11", Organization: []string{"Let's Encrypt"}},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
			IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			panic(err)
		}
		f.caCert, _ = x509.ParseCertificate(der)
		f.caKey, f.caPEM = key, pemText("CERTIFICATE", der)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: domains[0]}, DNSNames: domains,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, f.caCert, &key.PublicKey, f.caKey)
	if err != nil {
		panic(err)
	}
	return pemText("CERTIFICATE", der), f.caPEM, keyText(key)
}

func (f *Fake) applyCert(a *args) any {
	if !a.has("id") {
		return msg(false, "网站id不能为空!")
	}
	s := f.siteByID(a.get("id"))
	if s == nil {
		return msg(false, "网站丢失，无法继续申请证书")
	}
	if !a.need("auth_type") {
		return nil
	}
	file := a.get("auth_type") == "http" || a.get("auth_type") == "tls"
	if file && !s.Running {
		return msg(false, "当前网站未开启不能使用文件验证")
	}
	if !a.need("auth_to", "domains") {
		return nil
	}
	var domains []string
	if json.Unmarshal([]byte(a.get("domains")), &domains) != nil || len(domains) == 0 {
		a.missing = "domains (not a JSON list)"
		return nil
	}
	for _, d := range domains {
		if strings.Contains(d, "*.") && file {
			return msg(false, "泛域名不能使用【文件验证】的方式申请证书!")
		}
	}
	for _, d := range domains {
		bound := false
		for _, x := range f.siteDomains(s.ID) {
			bound = bound || x.Name == d
		}
		if !bound {
			// What the CA says when the challenge file is not served, in the
			// panel's [summary, ACME object] form.
			return reply{"status": false, "index": "fake-order", "msg": []any{
				"验证失败,域名解析错误或验证URL无法被访问! ",
				reply{"identifier": reply{"type": "dns", "value": d}, "status": "invalid", "challenges": []any{reply{"type": "http-01",
					"error": reply{"type": "urn:ietf:params:acme:error:unauthorized", "detail": "Invalid response from http://" + d + "/.well-known/acme-challenge/x: 404"}}}},
			}}
		}
	}
	cert, root, key := f.issue(domains)
	return reply{"status": true, "msg": "申请成功!", "cert": cert, "root": root, "private_key": key, "domains": domains,
		"cert_timeout": time.Now().Add(90 * 24 * time.Hour).Unix(), "save_path": "/www/server/panel/vhost/ssl/" + domains[0]}
}

func (f *Fake) proxiesOf(site string) []*Proxy {
	var out []*Proxy
	for _, p := range f.Proxies {
		if p.Site == site {
			out = append(out, p)
		}
	}
	return out
}

func proxyPath(p *Proxy) string {
	return fmt.Sprintf("/www/server/panel/vhost/nginx/proxy/%s/%s_%s.conf", p.Site, md5hex(p.Name), p.Site)
}

func proxyConf(p *Proxy) string {
	return fmt.Sprintf("#PROXY-START%s\n\nlocation ^~ %s\n{\n    proxy_pass %s;\n    proxy_set_header Host %s;\n}\n\n#PROXY-END%s\n", p.Dir, p.Dir, p.Target, p.Host, p.Dir)
}

func (f *Fake) proxyList(a *args) any {
	if !a.need("sitename") {
		return nil
	}
	out := []reply{}
	for _, p := range f.proxiesOf(a.get("sitename")) {
		out = append(out, reply{"proxyname": p.Name, "sitename": p.Site, "proxydir": p.Dir, "proxysite": p.Target, "todomain": p.Host,
			"type": p.Type, "cache": p.Cache, "subfilter": p.SubFilter, "advanced": p.Advanced, "cachetime": p.CacheTime})
	}
	return out
}

var (
	proxyDirBad  = regexp.MustCompile("[?=\\[\\])(*&^%$#@!~`{}><,',\";]+")
	proxyHostBad = regexp.MustCompile(`[}{#;"']+`)
	proxyURLRe   = regexp.MustCompile(`^http(s)?://`)
)

// proxyFields reads what CreateProxy and ModifyProxy read, and runs the
// panel's checks (__CheckStart); create is true for CreateProxy.
func (f *Fake) proxyFields(a *args, create bool) (*Proxy, any) {
	if !a.need("proxyname", "sitename", "proxydir", "todomain", "type", "cache", "subfilter", "advanced", "cachetime") {
		return nil, nil
	}
	p := &Proxy{Name: a.get("proxyname"), Site: a.get("sitename"), Dir: a.get("proxydir"), Target: a.get("proxysite"), Host: a.get("todomain")}
	var ok [3]bool
	p.Type, ok[0] = a.int("type")
	p.Cache, ok[1] = a.int("cache")
	p.Advanced, ok[2] = a.int("advanced")
	if json.Unmarshal([]byte(a.get("subfilter")), &p.SubFilter) != nil {
		a.missing = "subfilter (not JSON)"
		return nil, nil
	}
	if create && (len(p.Name) < 3 || len(p.Name) > 40) {
		return nil, msg(false, "名称必须大于3小于40个字符串")
	}
	others := f.proxiesOf(p.Site)
	for _, o := range others {
		if create && (o.Dir == p.Dir || o.Name == p.Name) || !create && o.Name != p.Name && o.Dir == p.Dir {
			return nil, msg(false, "指定反向代理名称或代理文件夹已存在")
		}
	}
	if create || len(others) != 1 {
		for _, o := range others {
			if o.Advanced != p.Advanced {
				return nil, msg(false, "不能同时设置目录代理和全局代理")
			}
		}
	}
	// The panel checks this one itself.
	n, err := strconv.Atoi(strings.TrimSpace(a.get("cachetime")))
	if err != nil {
		return nil, msg(false, "缓存时间不能为空")
	}
	p.CacheTime = n
	if !ok[0] || !ok[1] || !ok[2] {
		return nil, nil
	}
	if proxyDirBad.MatchString(p.Dir) {
		return nil, msg(false, "代理目录不能有以下特殊符号 ?,=,[,],),(,*,&,^,%,$,#,@,!,~,`,{,},>,<,\\,',\";]")
	}
	if p.Host != "" && proxyHostBad.MatchString(p.Host) {
		return nil, msg(false, "发送域名格式错误:"+p.Host+"<br>不能存在以下特殊字符【 }  { # ; \" ' 】 ")
	}
	if p.Host == "" {
		p.Host = "$host"
	}
	if !proxyURLRe.MatchString(p.Target) {
		return nil, msg(false, "域名格式错误 "+p.Target)
	}
	if proxyDirBad.MatchString(strings.TrimPrefix(strings.TrimPrefix(p.Target, "https://"), "http://")) {
		return nil, msg(false, "目标URL不能有以下特殊符号 ?,=,[,],),(,*,&,^,%,$,#,@,!,~,`,{,},>,<,\\,',\"]")
	}
	for _, s := range p.SubFilter {
		if s["sub1"] == "" && s["sub2"] != "" {
			return nil, msg(false, "请输入被替换的内容")
		}
		if s["sub1"] != "" && s["sub1"] == s["sub2"] {
			return nil, msg(false, "替换内容与被替换内容不能一致")
		}
	}
	// CheckLocation: the path must not already be a location of the
	// site's config or rewrite rules.
	loc := regexp.MustCompile(`location\s+(\^~\s+)?` + regexp.QuoteMeta(p.Dir) + `/\s*{`)
	if loc.MatchString(f.Files[vhostPath(p.Site)]) || loc.MatchString(f.Files[rewritePath(p.Site)]) {
		return nil, msg(false, "伪静态/网站配置文件已经存在路径【"+p.Dir+"】的反向代理")
	}
	return p, nil
}

func (f *Fake) proxyCreate(a *args) any {
	if a.get("proxysite") == "" {
		return msg(false, "目标URL不能为空")
	}
	p, refusal := f.proxyFields(a, true)
	if p == nil {
		return refusal
	}
	if strings.TrimSpace(p.Target[strings.LastIndex(p.Target, "//")+2:]) == "" {
		return msg(false, "目标URL不能为[http://或https://],请填写完整URL，如：https://www.bt.cn")
	}
	f.Proxies = append(f.Proxies, p)
	f.write(proxyPath(p), proxyConf(p))
	if p.Dir == "/" {
		// A proxy for the whole site turns PHP off.
		if s := f.siteByName(p.Site); s != nil {
			s.PHP = "00"
		}
	}
	return msg(true, "添加成功")
}

// phpSet is SetPHPVersion: the site's PHP, 00 for none.
func (f *Fake) phpSet(a *args) any {
	if !a.need("siteName", "version") {
		return nil
	}
	s := f.siteByName(a.get("siteName"))
	if s == nil {
		a.missing = "siteName (no such site)"
		return nil
	}
	v := a.get("version")
	if v != "00" && !slices.Contains(f.PHPVersions, v) {
		return msg(false, "指定PHP版本不存在!")
	}
	f.write(vhostPath(s.Name), strings.Replace(f.Files[vhostPath(s.Name)], "enable-php-"+s.PHP+".conf", "enable-php-"+v+".conf", 1))
	s.PHP = v
	return msg(true, "切换成功")
}

func (f *Fake) proxyModify(a *args) any {
	if a.get("proxysite") == "" {
		return msg(false, "目标URL不能为空")
	}
	p, refusal := f.proxyFields(a, false)
	if p == nil {
		return refusal
	}
	for _, o := range f.proxiesOf(p.Site) {
		if o.Name != p.Name {
			continue
		}
		if p.Type != 1 {
			if o.Type != 1 {
				return msg(false, "请先开启反向代理后再编辑！")
			}
			o.Type = p.Type
			f.Files[proxyPath(o)+"_bak"] = f.Files[proxyPath(o)]
			delete(f.Files, proxyPath(o))
			return msg(true, "修改成功")
		}
		*o = *p
		f.write(proxyPath(o), proxyConf(o))
		delete(f.Files, proxyPath(o)+"_bak")
		return msg(true, "修改成功")
	}
	return nil // the panel answers null when no proxy has the name
}

func (f *Fake) proxyRemove(a *args) any {
	if !a.need("sitename", "proxyname") {
		return nil
	}
	for i, p := range f.Proxies {
		if p.Site == a.get("sitename") && p.Name == a.get("proxyname") {
			delete(f.Files, proxyPath(p))
			delete(f.Files, proxyPath(p)+"_bak")
			f.Proxies = slices.Delete(f.Proxies, i, i+1)
			return msg(true, "删除成功")
		}
	}
	return nil
}

func (f *Fake) rewriteList(a *args) any {
	if !a.need("siteName") {
		return nil
	}
	list := []string{"0.当前"}
	for p := range f.Files {
		if name, ok := strings.CutPrefix(p, "/www/server/panel/rewrite/nginx/"); ok {
			list = append(list, strings.TrimSuffix(name, ".conf"))
		}
	}
	sort.Strings(list)
	return reply{"rewrite": list, "default_list": []string{"0.当前", "default", "laravel5", "thinkphp", "wordpress"}}
}

func (f *Fake) fileBody(a *args) any {
	if !a.has("path") {
		return msg(false, "缺少参数! path")
	}
	path := a.get("path")
	body, ok := f.Files[path]
	if !ok {
		if !strings.Contains(path, "rewrite") {
			return msg(false, "指定文件不存在!")
		}
		f.write(path, "") // the panel creates missing rewrite files
	}
	out := reply{"status": true, "only_read": false, "size": len(body), "encoding": "utf-8", "historys": []any{},
		"auto_save": nil, "st_mtime": f.mtime(path)}
	if len(body) > 3145928 {
		lines := strings.Split(body, "\n")
		body = strings.Join(lines[max(0, len(lines)-1000):], "\n")
		out["only_read"], out["next"] = true, true
	}
	out["data"] = body
	return out
}

func (f *Fake) fileSave(a *args) any {
	if !a.has("path") {
		return msg(false, "path参数不能为空!")
	}
	path := a.get("path")
	old, exists := f.Files[path]
	if !exists && !strings.Contains(path, ".htaccess") {
		return msg(false, "指定文件不存在!")
	}
	if strings.Contains(path, "/www/server/panel/vhost/nginx/") {
		if !a.need("data") {
			return nil
		}
		d := a.get("data")
		if strings.Contains(d, "#SSL-START") && strings.Contains(d, "#SSL-END") && !strings.Contains(d, sslMark) {
			return msg(false, `配置文件保存失败：<p style="color:red;">请勿修改SSL相关配置中注释的404规则</p><p>要修改404配置，找到以下配置位置：</p><pre>#ERROR-PAGE-START  错误页配置</pre>`)
		}
	}
	if a.has("st_mtime") && a.get("force") != "1" && a.get("st_mtime") != f.mtime(path) {
		return msg(false, "保存失败，"+path+"文件发生改变，可能是该文件已经被其他人修改，请刷新内容后重新修改.")
	}
	// Inside the panel's try block, where a missing field is reported.
	for _, k := range []string{"data", "encoding"} {
		if !a.has(k) {
			return msg(false, "FILE_SAVE_ERR'dict_obj' object has no attribute '"+k+"'")
		}
	}
	test := f.NginxInit && !slices.Contains([]string{"1", "true", "True"}, a.get("skip_conf_check")) &&
		(strings.Contains(path, "nginx") || strings.Contains(path, "apache") || strings.Contains(path, "rewrite"))
	oldTime, hadTime := f.mtimes[path]
	f.write(path, strings.ReplaceAll(a.get("data"), "\r\n", "\n"))
	if test {
		if err := f.nginxT(); err != nil {
			// cp -a the backup back: content and time.
			if exists {
				f.Files[path] = old
			} else {
				delete(f.Files, path)
			}
			if hadTime {
				f.mtimes[path] = oldTime
			} else {
				delete(f.mtimes, path)
			}
			out := msg(false, `保存失败，因为检测到被修改的配置文件存在错误:<br><pre style="color:red;white-space: pre-line;">`+err.Error()+"</pre>")
			out["conf_check"] = 1
			return out
		}
	}
	return reply{"status": true, "msg": "文件已保存!", "historys": []any{}, "st_mtime": f.mtime(path)}
}

// logEscape is panelSite.xsssec: <>'" full-width, then HTML escaping.
var logEscape = strings.NewReplacer("<", "＜", ">", "＞", "'", "＇", `"`, "＂")

func (f *Fake) siteLog(a *args, suffix string) any {
	if !a.need("siteName") {
		return nil
	}
	if f.siteByName(a.get("siteName")) == nil {
		a.missing = "siteName (no such site)"
		return nil
	}
	body, ok := f.Files["/www/wwwlogs/"+a.get("siteName")+suffix]
	if !ok {
		return msg(false, "日志为空")
	}
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	body = strings.Join(lines[max(0, len(lines)-1000):], "\n")
	return msg(true, html.EscapeString(logEscape.Replace(body)))
}

func (f *Fake) siteBackup(a *args) any {
	if !a.has("id") {
		return msg(false, "参数错误")
	}
	s := f.siteByID(a.get("id"))
	if s == nil {
		return msg(false, "未找到对应网站")
	}
	size := int64(1024)
	for p, body := range f.Files {
		if strings.HasPrefix(p, s.Path+"/") {
			size += int64(len(body))
		}
	}
	name := s.Name + "_" + time.Now().Format("20060102_150405") + ".tar.gz"
	f.Backups = append(f.Backups, &Backup{ID: f.id(), Type: 0, PID: s.ID, Name: name, Filename: "/www/backup/site/" + s.Name + "/" + name,
		Size: size, AddTime: now()})
	return msg(true, "备份成功!")
}

func (f *Fake) delBackup(id string) bool {
	for i, b := range f.Backups {
		if strconv.Itoa(b.ID) == id {
			f.Backups = slices.Delete(f.Backups, i, i+1)
			return true
		}
	}
	return false
}

func (f *Fake) siteDelBackup(a *args) any {
	if !a.need("id") {
		return nil
	}
	if !f.delBackup(a.get("id")) {
		return msg(true, "未查询到备份文件")
	}
	return msg(true, "删除成功")
}

func (f *Fake) dbByID(id string) *Database {
	for _, d := range f.Databases {
		if strconv.Itoa(d.ID) == id {
			return d
		}
	}
	return nil
}

func (f *Fake) dbBackup(a *args) any {
	if !a.has("id") {
		return msg(false, "缺少参数！id")
	}
	d := f.dbByID(a.get("id"))
	if d == nil {
		return msg(false, "数据库不存在！"+a.get("id"))
	}
	name := d.Name + "_" + time.Now().Format("2006-01-02_15-04-05") + "_mysql_data_Ab3xY.sql.zip"
	f.Backups = append(f.Backups, &Backup{ID: f.id(), Type: 1, PID: d.ID, Name: name,
		Filename: "/www/backup/database/mysql/" + d.Name + "/" + name, Size: 4096, AddTime: now()})
	return msg(true, "备份成功!")
}

func (f *Fake) dbDelBackup(a *args) any {
	if !a.has("id") {
		return msg(false, "缺少参数！id")
	}
	if !f.delBackup(a.get("id")) {
		return msg(false, "备份已删除！")
	}
	return msg(true, "删除成功")
}

func (f *Fake) dbImport(a *args) any {
	if !a.has("name") {
		return msg(false, "缺少参数！name")
	}
	if !a.has("file") {
		return msg(false, "缺少参数！file")
	}
	file := a.get("file")
	exists := false
	for _, b := range f.Backups {
		exists = exists || b.Filename == file
	}
	if _, ok := f.Files[file]; !exists && !ok {
		return msg(false, "导入路径不存在!")
	}
	if !slices.ContainsFunc([]string{".sql", ".tar.gz", ".gz", ".zip", ".tgz"}, func(ext string) bool { return strings.HasSuffix(strings.ToLower(file), ext) }) {
		return msg(false, "请选择sql、tar.gz、gz、zip文件格式!")
	}
	for _, d := range f.Databases {
		if d.Name == a.get("name") {
			d.Restored = file
			return msg(true, "导入数据库成功!")
		}
	}
	a.missing = "name (no such database)"
	return nil
}

var dbNameRe = regexp.MustCompile(`^[\w.-]+$`)

func (f *Fake) dbAdd(a *args) any {
	if !a.need("name") {
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(a.get("name")))
	if name == "" {
		return msg(false, "数据库名称不能为空")
	}
	if len(name) > 64 {
		return msg(false, "数据库名不能大于64位!")
	}
	if !a.need("db_user") {
		return nil
	}
	user := strings.TrimSpace(a.get("db_user"))
	switch {
	case user == "":
		return msg(false, "数据库用户名不能为空")
	case len(user) > 32:
		return msg(false, "Mysql不支持超过32位的用户名！")
	case !dbNameRe.MatchString(name):
		return msg(false, "数据库名称不能带有特殊符号!")
	case !dbNameRe.MatchString(user):
		return msg(false, "用户名不能带特殊字符！")
	case slices.Contains([]string{"root", "mysql", "test", "sys", "panel_logs"}, user):
		return msg(false, "数据库用户名不合法!请使用其他用户名!")
	case slices.Contains([]string{"root", "mysql", "test", "sys", "panel_logs"}, name):
		return msg(false, "数据库名称不合法!请使用其他数据库名称!")
	}
	if !a.need("password") {
		return nil
	}
	password := a.get("password")
	if password == "" {
		password = md5hex(strconv.FormatInt(time.Now().UnixNano(), 10))[:16]
	}
	for _, d := range f.Databases {
		if d.Username == user {
			return msg(false, "数据库用户已存在，请使用其他数据库用户名称！")
		}
	}
	if !a.need("address") {
		return nil
	}
	address := strings.TrimSpace(a.get("address"))
	if address == "" || address == "ip" {
		return msg(false, "访问权限为【指定IP】时，需要填写IP地址!")
	}
	if !a.need("codeing") || !slices.Contains([]string{"utf8", "utf8mb4", "gbk", "big5"}, a.get("codeing")) {
		a.missing = "codeing"
		return nil
	}
	for _, d := range f.Databases {
		if strings.EqualFold(d.Name, name) {
			return msg(false, "指定数据库已在MySQL中存在，请换个名称!")
		}
	}
	if !a.need("ps") {
		return nil
	}
	ps := a.get("ps")
	if ps == "" {
		ps = "填写备注"
	}
	f.Databases = append(f.Databases, &Database{ID: f.id(), PID: atoi(a.get("pid")), Name: name, Username: user, Password: password,
		Accept: address, Charset: a.get("codeing"), PS: xss(ps), AddTime: now()})
	return msg(true, "添加成功")
}

func (f *Fake) dbDelete(a *args) any {
	if !a.has("id") {
		return msg(false, "缺少参数！id")
	}
	if !a.has("name") {
		return msg(false, "缺少参数！name")
	}
	for i, d := range f.Databases {
		if strconv.Itoa(d.ID) == a.get("id") && d.Name == a.get("name") {
			f.Databases = slices.Delete(f.Databases, i, i+1)
			return msg(true, "删除成功")
		}
	}
	return msg(false, "数据库["+a.get("name")+"]不存在")
}
