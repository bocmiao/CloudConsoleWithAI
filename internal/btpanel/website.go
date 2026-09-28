package btpanel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The 网站 page of the panel: sites, their domains, HTTPS, reverse
// proxies, rewrite rules, config files, logs and backups, and the MySQL
// databases on the 数据库 page. Most actions take the site's name (its
// main domain) and some its ID as well, so they take a Site.

// Site is one row of the panel's site list.
type Site struct {
	ID          int
	Name        string // the main domain; the key most actions use
	Title       string // the name the list shows (rname), the same unless renamed
	Path        string // the root directory
	Running     bool   // a stopped site serves the panel's "stopped" page
	Remark      string
	PHPVersion  string // such as 8.2; 静态 (Static on aaPanel) for static sites
	ProjectType string // PHP for ordinary sites; Node, Java, Go, Python, proxy... for projects
	TypeID      int    // the site's category
	Created     string // as the panel shows it, 2006-01-02 15:04:05
	Expires     string // 0000-00-00 means never
	DomainCount int
	BackupCount int
	Cert        *CertInfo // the certificate its config uses, nil without HTTPS
}

// CertInfo describes a certificate as the panel reads it (ssl_info.py).
type CertInfo struct {
	Subject   string
	Issuer    string // the issuer's common name, such as R11
	IssuerOrg string // such as Let's Encrypt
	NotBefore string
	NotAfter  string // 2006-01-02, with the time on panels that read it with openssl
	DaysLeft  int
	Domains   []string
}

type siteRow struct {
	ID          num             `json:"id"`
	Name        text            `json:"name"`
	RName       text            `json:"rname"`
	Path        text            `json:"path"`
	Status      text            `json:"status"`
	PS          text            `json:"ps"`
	AddTime     text            `json:"addtime"`
	EDate       text            `json:"edate"`
	TypeID      num             `json:"type_id"`
	ProjectType text            `json:"project_type"`
	PHPVersion  text            `json:"php_version"`
	Domains     num             `json:"domain"`
	BackupCount num             `json:"backup_count"`
	SSL         json.RawMessage `json:"ssl"` // -1 without a certificate
}

func (r siteRow) key() int { return int(r.ID) }

func (r siteRow) site() Site {
	s := Site{
		ID: int(r.ID), Name: string(r.Name), Title: string(r.RName), Path: string(r.Path), Running: r.Status == "1",
		// The panel stores remarks HTML-escaped.
		Remark:     html.UnescapeString(string(r.PS)),
		PHPVersion: string(r.PHPVersion), ProjectType: string(r.ProjectType), TypeID: int(r.TypeID),
		Created: string(r.AddTime), Expires: string(r.EDate), DomainCount: int(r.Domains), BackupCount: int(r.BackupCount),
		Cert: certFrom(r.SSL),
	}
	if s.Title == "" {
		s.Title = s.Name
	}
	return s
}

type certRow struct {
	Subject   text     `json:"subject"`
	Issuer    text     `json:"issuer"`
	IssuerOrg text     `json:"issuer_O"`
	NotBefore text     `json:"notBefore"`
	NotAfter  text     `json:"notAfter"`
	Endtime   num      `json:"endtime"`
	DNS       []string `json:"dns"`
}

func certFrom(raw json.RawMessage) *CertInfo {
	var r certRow
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &r) != nil || (r.NotAfter == "" && r.Subject == "") {
		return nil
	}
	return &CertInfo{Subject: string(r.Subject), Issuer: string(r.Issuer), IssuerOrg: string(r.IssuerOrg),
		NotBefore: string(r.NotBefore), NotAfter: string(r.NotAfter), DaysLeft: int(r.Endtime), Domains: r.DNS}
}

var pcountRe = regexp.MustCompile(`Pcount'>[^<0-9]*([0-9]+)`)

// pageTotal reads the row count from the pager HTML getData sends
// (page.py: 共N条, or Total N on aaPanel).
func pageTotal(page string) (int, bool) {
	m := pcountRe.FindStringSubmatch(page)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

type keyed interface{ key() int }

// allRows walks the pages of data/getData (data.py GetSql) until it has
// every row, and stops early if the panel repeats a page.
func allRows[R keyed](ctx context.Context, c *Client, form url.Values) ([]R, error) {
	var all []R
	seen := map[int]bool{}
	for p := 1; p <= 1000; p++ {
		form.Set("p", strconv.Itoa(p))
		var page struct {
			Data []R  `json:"data"`
			Page text `json:"page"`
		}
		if err := c.do(ctx, "data", "getData", form, &page); err != nil {
			return nil, err
		}
		fresh := 0
		for _, r := range page.Data {
			if !seen[r.key()] {
				seen[r.key()] = true
				all = append(all, r)
				fresh++
			}
		}
		if total, ok := pageTotal(string(page.Page)); fresh == 0 || ok && len(all) >= total {
			break
		}
	}
	return all, nil
}

// Sites lists every site, page by page.
func (c *Client) Sites(ctx context.Context) ([]Site, error) {
	// No limit: for this table the panel saves the page size it is given
	// as the user's own (data.py writes data/limit1.pl), so use theirs.
	rows, err := allRows[siteRow](ctx, c, url.Values{"table": {"sites"}})
	sites := make([]Site, 0, len(rows))
	for _, r := range rows {
		sites = append(sites, r.site())
	}
	return sites, err
}

// Site returns the site with a name (its main domain).
func (c *Client) Site(ctx context.Context, name string) (Site, error) {
	sites, err := c.Sites(ctx)
	if err != nil {
		return Site{}, err
	}
	for _, s := range sites {
		if s.Name == name {
			return s, nil
		}
	}
	for _, s := range sites {
		if strings.EqualFold(s.Name, name) || strings.EqualFold(s.Title, name) {
			return s, nil
		}
	}
	return Site{}, notFound("宝塔面板里没有网站 " + name)
}

func (c *Client) siteByID(ctx context.Context, id int) (Site, error) {
	sites, err := c.Sites(ctx)
	if err != nil {
		return Site{}, err
	}
	for _, s := range sites {
		if s.ID == id {
			return s, nil
		}
	}
	return Site{}, notFound(fmt.Sprintf("宝塔面板里没有 ID 为 %d 的网站", id))
}

// SetSiteRunning starts a stopped site or stops a running one; a stopped
// site's config points at the panel's stop page instead of its directory.
func (c *Client) SetSiteRunning(ctx context.Context, s Site, run bool) error {
	action := "SiteStop"
	if run {
		action = "SiteStart"
	}
	_, err := c.call(ctx, "site", action, url.Values{"id": {strconv.Itoa(s.ID)}, "name": {s.Name}})
	return err
}

// NewSite is what creating a site needs.
type NewSite struct {
	Domain  string   // the main domain, with :port for a port other than 80
	Aliases []string // more domains, each optionally with :port
	Path    string   // the root directory; the panel's site folder/<domain> when empty
	PHP     string   // a PHP version the panel has, such as 82; empty or 00 for a static site
	Remark  string
	TypeID  int // the category; 0 is the default one
}

// CreateSite adds a site without FTP account or database. The panel names
// it after the main domain, adding _<port> when that name is taken.
func (c *Client) CreateSite(ctx context.Context, n NewSite) (Site, error) {
	host, port := n.Domain, "80"
	if h, p, ok := strings.Cut(n.Domain, ":"); ok && p != "" {
		host, port = h, p
	}
	path := n.Path
	if path == "" {
		root, err := c.sitesPath(ctx)
		if err != nil {
			return Site{}, err
		}
		path = root + "/" + host
	}
	php := n.PHP
	if php == "" {
		php = "00"
	}
	// domainlist must be a list: the panel loops over it.
	aliases := append([]string{}, n.Aliases...)
	webname, _ := json.Marshal(map[string]any{"domain": host, "domainlist": aliases, "count": len(aliases)})
	var r struct {
		SiteID num `json:"siteId"`
	}
	err := c.do(ctx, "site", "AddSite", url.Values{
		"webname": {string(webname)}, "path": {path}, "port": {port}, "ps": {n.Remark},
		"type": {"PHP"}, "type_id": {strconv.Itoa(n.TypeID)}, "version": {php},
		"ftp": {"false"}, "sql": {"false"},
	}, &r)
	if err != nil {
		return Site{}, err
	}
	if r.SiteID == 0 {
		return Site{}, &Error{Status: http.StatusOK, Action: "site/AddSite", Reason: ReasonBadAnswer, Message: "没有返回新网站的 ID"}
	}
	return c.siteByID(ctx, int(r.SiteID))
}

// sitesPath is where the panel puts new sites (面板设置 → 默认建站目录).
func (c *Client) sitesPath(ctx context.Context) (string, error) {
	var p text
	err := c.do(ctx, "data", "getKey", url.Values{"table": {"config"}, "key": {"sites_path"}, "id": {"1"}}, &p)
	var pe *Error
	if errors.As(err, &pe) && pe.Reason != ReasonBadKey && pe.Reason != ReasonIPNotAllowed && pe.Reason != ReasonLockedOut {
		err = nil // an older panel: use its default
	}
	if err != nil {
		return "", err
	}
	if s := strings.TrimRight(string(p), "/"); strings.HasPrefix(s, "/") {
		return s, nil
	}
	return "/www/wwwroot", nil
}

// DeleteSiteOptions says what goes with a site besides its configs, logs,
// domains and reverse proxies, which the panel always removes. The zero
// value keeps the rest.
type DeleteSiteOptions struct {
	Files    bool // the root directory (kept anyway when another site uses it)
	Database bool // the database created with the site
	FTP      bool // the FTP account created with the site
}

// DeleteSite removes a site. Its backups stay.
func (c *Client) DeleteSite(ctx context.Context, s Site, o DeleteSiteOptions) error {
	form := url.Values{"id": {strconv.Itoa(s.ID)}, "webname": {s.Name}}
	// The panel deletes each only when its field is "1".
	for field, on := range map[string]bool{"path": o.Files, "database": o.Database, "ftp": o.FTP} {
		if on {
			form.Set(field, "1")
		}
	}
	_, err := c.call(ctx, "site", "DeleteSite", form)
	return err
}

// Domain is one domain a site answers to.
type Domain struct {
	ID      int
	SiteID  int
	Name    string
	Port    int
	Created string
}

type domainRow struct {
	ID      num  `json:"id"`
	PID     num  `json:"pid"`
	Name    text `json:"name"`
	Port    num  `json:"port"`
	AddTime text `json:"addtime"`
}

// Domains lists a site's domains.
func (c *Client) Domains(ctx context.Context, s Site) ([]Domain, error) {
	var rows []domainRow
	// list asks for every row at once, as the panel's own page does.
	err := c.do(ctx, "data", "getData", url.Values{"table": {"domain"}, "list": {"True"}, "search": {strconv.Itoa(s.ID)}}, &rows)
	var out []Domain
	for _, r := range rows {
		// search also matches a domain named like the ID.
		if int(r.PID) == s.ID {
			out = append(out, Domain{ID: int(r.ID), SiteID: int(r.PID), Name: string(r.Name), Port: int(r.Port), Created: string(r.AddTime)})
		}
	}
	return out, err
}

// AddDomains binds more domains to a site, each optionally with :port.
// The panel adds the ones it can; the error names the others.
func (c *Client) AddDomains(ctx context.Context, s Site, domains ...string) error {
	raw, err := c.call(ctx, "site", "AddDomain", url.Values{
		"id": {strconv.Itoa(s.ID)}, "webname": {s.Name}, "domain": {strings.Join(domains, ",")},
	})
	if err != nil {
		return err
	}
	// 宝塔 answers per domain; aaPanel with a plain status and message.
	var r struct {
		Domains []struct {
			Name   text `json:"name"`
			Status bool `json:"status"`
			Msg    text `json:"msg"`
		} `json:"domains"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	var failed []string
	for _, d := range r.Domains {
		if !d.Status {
			failed = append(failed, string(d.Name)+"："+plain(string(d.Msg)))
		}
	}
	if len(failed) > 0 {
		return &Error{Status: http.StatusOK, Action: "site/AddDomain", Reason: ReasonRefused, Message: strings.Join(failed, "；")}
	}
	return nil
}

// DeleteDomain unbinds a domain; the panel keeps a site's last one.
func (c *Client) DeleteDomain(ctx context.Context, s Site, d Domain) error {
	_, err := c.call(ctx, "site", "DelDomain", url.Values{
		"id": {strconv.Itoa(s.ID)}, "webname": {s.Name}, "domain": {d.Name}, "port": {strconv.Itoa(d.Port)},
	})
	return err
}

// SSL is a site's HTTPS state, as its SSL page shows it.
type SSL struct {
	Enabled    bool      // the site's config has a certificate
	ForceHTTPS bool      // plain HTTP is redirected to HTTPS
	Type       int       // 0 own certificate, 1 Let's Encrypt, 2 and 3 bought through the panel; -1 when off
	Cert       *CertInfo // the certificate file's details; nil when there is none
	Domains    []string  // the site's domains
}

// SSL reads a site's HTTPS state. The panel's answer also carries the
// private key, which is dropped here.
func (c *Client) SSL(ctx context.Context, s Site) (SSL, error) {
	var r struct {
		Status      bool            `json:"status"`
		Type        num             `json:"type"`
		HTTPToHTTPS bool            `json:"httpTohttps"`
		CertData    json.RawMessage `json:"cert_data"`
		Domain      []struct {
			Name text `json:"name"`
		} `json:"domain"`
	}
	if err := c.do(ctx, "site", "GetSSL", url.Values{"siteName": {s.Name}}, &r); err != nil {
		return SSL{}, err
	}
	out := SSL{Enabled: r.Status, ForceHTTPS: r.HTTPToHTTPS, Type: int(r.Type), Cert: certFrom(r.CertData)}
	for _, d := range r.Domain {
		out.Domains = append(out.Domains, string(d.Name))
	}
	return out, nil
}

// SetSSL installs a certificate (PEM, with its chain) and its private key
// and turns HTTPS on. The panel checks that they match and that nginx
// accepts the result, and keeps the previous certificate otherwise.
func (c *Client) SetSSL(ctx context.Context, s Site, keyPEM, certPEM string) error {
	// type 1 is what the SSL page sends; SetSSL itself does not read it.
	_, err := c.call(ctx, "site", "SetSSL", url.Values{"type": {"1"}, "siteName": {s.Name}, "key": {keyPEM}, "csr": {certPEM}})
	return err
}

// SetForceHTTPS switches redirecting plain HTTP to HTTPS (the panel keeps
// /.well-known reachable over HTTP for certificate renewals). It needs
// HTTPS on.
func (c *Client) SetForceHTTPS(ctx context.Context, s Site, on bool) error {
	action := "CloseToHttps"
	if on {
		action = "HttpToHttps"
	}
	_, err := c.call(ctx, "site", action, url.Values{"siteName": {s.Name}})
	return err
}

// CloseSSL turns HTTPS off, forced HTTPS with it. The certificate files
// stay on the server.
func (c *Client) CloseSSL(ctx context.Context, s Site) error {
	_, err := c.call(ctx, "site", "CloseSSLConf", url.Values{"siteName": {s.Name}})
	return err
}

// ApplyLetsEncrypt gets a Let's Encrypt certificate for domains (all the
// site's domains when none are given) by file validation and installs
// it, the two steps the SSL page takes: acme/apply_cert_api runs the
// whole order inside the call and answers with the certificate and key,
// then SetSSL puts them on the site. Validation needs the site running
// and the domains resolving to this server; wildcard domains need DNS
// validation, which is not offered here.
func (c *Client) ApplyLetsEncrypt(ctx context.Context, s Site, domains []string) (SSL, error) {
	if len(domains) == 0 {
		list, err := c.Domains(ctx, s)
		if err != nil {
			return SSL{}, err
		}
		for _, d := range list {
			domains = append(domains, d.Name)
		}
	}
	names, _ := json.Marshal(domains)
	id := strconv.Itoa(s.ID)
	var cert struct {
		Cert       text `json:"cert"`
		Root       text `json:"root"`
		PrivateKey text `json:"private_key"`
	}
	// auth_to may be the site's ID: the panel turns it into the directory
	// the challenge files go in.
	err := c.do(ctx, "acme", "apply_cert_api", url.Values{
		"id": {id}, "domains": {string(names)}, "auth_type": {"http"}, "auth_to": {id}, "auto_wildcard": {"0"},
	}, &cert)
	if err != nil {
		return SSL{}, err
	}
	if cert.Cert == "" || cert.PrivateKey == "" {
		return SSL{}, &Error{Status: http.StatusOK, Action: "acme/apply_cert_api", Reason: ReasonBadAnswer, Message: "没有返回证书"}
	}
	if err := c.SetSSL(ctx, s, string(cert.PrivateKey), string(cert.Cert+cert.Root)); err != nil {
		return SSL{}, err
	}
	return c.SSL(ctx, s)
}

// Proxy is one reverse proxy of a site (网站 → 反向代理). The panel keeps
// them in data/proxyfile.json and writes each to its own config file.
type Proxy struct {
	Name         string // 3 to 40 bytes, unique within the site
	Site         string // the site's name
	Dir          string // the path proxied; "/" (the default) proxies the whole site
	Target       string // where requests go, such as http://127.0.0.1:8080
	Host         string // the Host header sent to Target; $host (the default) passes the visitor's
	Enabled      bool   // a paused proxy keeps its settings but is not served
	Cache        bool   // nginx caches the target's answers
	CacheMinutes int
	Replace      []Replace // text replaced in the target's answers (sub_filter)
}

// Replace is one text replacement of a reverse proxy.
type Replace struct{ From, To string }

type proxyRow struct {
	ProxyName text `json:"proxyname"`
	SiteName  text `json:"sitename"`
	ProxyDir  text `json:"proxydir"`
	ProxySite text `json:"proxysite"`
	ToDomain  text `json:"todomain"`
	Type      num  `json:"type"`
	Cache     num  `json:"cache"`
	CacheTime num  `json:"cachetime"`
	SubFilter []struct {
		Sub1 text `json:"sub1"`
		Sub2 text `json:"sub2"`
	} `json:"subfilter"`
}

// Proxies lists a site's reverse proxies. The panel first turns a proxy
// written by very old versions into the "旧代理" entry.
func (c *Client) Proxies(ctx context.Context, s Site) ([]Proxy, error) {
	var rows []proxyRow
	if err := c.do(ctx, "site", "GetProxyList", url.Values{"sitename": {s.Name}}, &rows); err != nil {
		return nil, err
	}
	var out []Proxy
	for _, r := range rows {
		p := Proxy{Name: string(r.ProxyName), Site: string(r.SiteName), Dir: string(r.ProxyDir), Target: string(r.ProxySite),
			Host: string(r.ToDomain), Enabled: r.Type == 1, Cache: r.Cache == 1, CacheMinutes: int(r.CacheTime)}
		for _, f := range r.SubFilter {
			if f.Sub1 != "" {
				p.Replace = append(p.Replace, Replace{From: string(f.Sub1), To: string(f.Sub2)})
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func (p Proxy) form(s Site, enabled bool) url.Values {
	dir, host, minutes := p.Dir, p.Host, p.CacheMinutes
	if dir == "" {
		dir = "/"
	}
	if host == "" {
		host = "$host"
	}
	if minutes <= 0 {
		minutes = 1
	}
	subs := []map[string]string{}
	for _, r := range p.Replace {
		subs = append(subs, map[string]string{"sub1": r.From, "sub2": r.To})
	}
	sub, _ := json.Marshal(subs)
	// advanced marks a directory proxy; the panel won't mix those with one
	// for the whole site.
	advanced := "0"
	if dir != "/" {
		advanced = "1"
	}
	return url.Values{
		"proxyname": {p.Name}, "sitename": {s.Name}, "proxydir": {dir}, "proxysite": {p.Target}, "todomain": {host},
		"type": {bit(enabled)}, "cache": {bit(p.Cache)}, "cachetime": {strconv.Itoa(minutes)},
		"advanced": {advanced}, "subfilter": {string(sub)},
	}
}

func bit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// CreateProxy adds a reverse proxy, switched on. Proxying "/" also
// switches the site's PHP off (the panel sets it to static).
func (c *Client) CreateProxy(ctx context.Context, s Site, p Proxy) error {
	_, err := c.call(ctx, "site", "CreateProxy", p.form(s, true))
	return err
}

// ModifyProxy rewrites a reverse proxy, found by its name, from p; a p
// that is not Enabled pauses it.
func (c *Client) ModifyProxy(ctx context.Context, s Site, p Proxy) error {
	raw, err := c.call(ctx, "site", "ModifyProxy", p.form(s, p.Enabled))
	if err == nil && string(raw) == "null" {
		// The panel answers nothing when no proxy has that name.
		return notFound("网站 " + s.Name + " 没有名为 " + p.Name + " 的反向代理")
	}
	return err
}

// RemoveProxy deletes a reverse proxy and its config file.
func (c *Client) RemoveProxy(ctx context.Context, s Site, name string) error {
	raw, err := c.call(ctx, "site", "RemoveProxy", url.Values{"sitename": {s.Name}, "proxyname": {name}})
	if err == nil && string(raw) == "null" {
		return notFound("网站 " + s.Name + " 没有名为 " + name + " 的反向代理")
	}
	return err
}

// File is a text file read through the panel's file manager.
type File struct {
	Path    string
	Content string
	// ModTime is the file's modification time as the panel gives it. Pass
	// it back when saving and the panel refuses to overwrite a file that
	// changed in between.
	ModTime string
	// ReadOnly is set for files over 3 MB, of which the panel sends only
	// the end, and for files under tamper protection.
	ReadOnly bool
}

func (c *Client) readFile(ctx context.Context, path string) (File, error) {
	var r struct {
		Data     text `json:"data"`
		OnlyRead bool `json:"only_read"`
		MTime    text `json:"st_mtime"`
	}
	err := c.do(ctx, "files", "GetFileBody", url.Values{"path": {path}}, &r)
	return File{Path: path, Content: string(r.Data), ModTime: string(r.MTime), ReadOnly: r.OnlyRead}, err
}

// writeFile saves a file with files.SaveFileBody. For a path containing
// nginx, apache or rewrite the panel copies the old file aside, writes,
// runs the web server's config test and copies the old file back when
// it fails; it skips the test when neither /etc/init.d/nginx nor
// /etc/init.d/httpd exists.
func (c *Client) writeFile(ctx context.Context, path, content, modTime string) error {
	form := url.Values{"path": {path}, "data": {content}, "encoding": {"utf-8"}}
	if modTime != "" {
		form.Set("st_mtime", modTime)
	}
	_, err := c.call(ctx, "files", "SaveFileBody", form)
	var pe *Error
	if errors.As(err, &pe) && pe.Reason == ReasonRefused && strings.HasPrefix(pe.Message, "ERROR") {
		// aaPanel reports the failed test as ERROR: <nginx's output>,
		// without 宝塔's conf_check flag.
		pe.Reason = ReasonConfigRejected
		pe.Message = strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(pe.Message, "ERROR"), ":："))
	}
	return err
}

// confPrefix is how the panel prefixes a site's config files by project
// type (panelSite.py): nothing for PHP sites and reverse proxy projects,
// node_, java_, go_, python_, other_, net_, html_ or ai_ for the others.
// WordPress sites (WP, WP2) start as PHP sites and keep their names.
func confPrefix(projectType string) string {
	switch t := strings.ToLower(projectType); t {
	case "", "php", "proxy", "wp", "wp2":
		return ""
	default:
		return t + "_"
	}
}

// NginxConfPath is where the panel keeps a site's nginx config.
func NginxConfPath(s Site) string {
	return "/www/server/panel/vhost/nginx/" + confPrefix(s.ProjectType) + s.Name + ".conf"
}

// NginxConf reads a site's nginx config.
func (c *Client) NginxConf(ctx context.Context, s Site) (File, error) {
	return c.readFile(ctx, NginxConfPath(s))
}

// SetNginxConf replaces a site's nginx config; modTime, when not empty,
// is File.ModTime from NginxConf. The panel tests the whole configuration
// with nginx -t and puts the old file back when it fails
// (ReasonConfigRejected). It skips the test when nginx has no
// /etc/init.d/nginx, which the panel's own nginx always has; call
// TestNginx afterwards to be sure, and write the old content back if it
// fails. The panel also refuses a config whose SSL block has lost its
// "#error_page 404/404.html;" marker line, which it edits by.
func (c *Client) SetNginxConf(ctx context.Context, s Site, content, modTime string) error {
	return c.writeFile(ctx, NginxConfPath(s), content, modTime)
}

// TestNginx has the panel run nginx -t over the whole configuration
// (system.ServiceAdmin). Side effects to know: the panel starts nginx
// afterwards if it is not running, and when nginx's complaint mentions
// "proxy" or "perserver" it repairs the config its own way, restarts
// nginx and reports success, so that case is tested once more. An nginx
// installed from the system's packages (sys_install.pl) is not tested.
func (c *Client) TestNginx(ctx context.Context) error {
	for try := 0; try < 2; try++ {
		raw, err := c.call(ctx, "system", "ServiceAdmin", url.Values{"name": {"nginx"}, "type": {"test"}})
		var pe *Error
		if errors.As(err, &pe) && pe.Reason == ReasonRefused && (strings.Contains(pe.Message, "配置规则错误") || strings.Contains(pe.Message, "[emerg]")) {
			pe.Reason = ReasonConfigInvalid
			if _, rest, ok := strings.Cut(pe.Message, "错误:"); ok {
				pe.Message = strings.TrimSpace(rest)
			}
		}
		if err != nil {
			return err
		}
		var r struct {
			Msg text `json:"msg"`
		}
		if json.Unmarshal(raw, &r) != nil || !strings.Contains(string(r.Msg), "已修正") {
			return nil
		}
	}
	return &Error{Status: http.StatusOK, Action: "system/ServiceAdmin", Reason: ReasonConfigInvalid,
		Message: "宝塔面板两次自动修正了 Nginx 配置，仍不能确认配置正确"}
}

type rewriteList struct {
	Rewrite  []string `json:"rewrite"`
	SitePath text     `json:"sitePath"` // only for Apache and OpenLiteSpeed
}

func (c *Client) rewriteList(ctx context.Context, s Site) (rewriteList, error) {
	var r rewriteList
	err := c.do(ctx, "site", "GetRewriteList", url.Values{"siteName": {s.Name}}, &r)
	return r, err
}

// RewriteTemplates lists the panel's rewrite rule templates for the
// site's web server, such as wordpress, thinkphp and laravel5.
func (c *Client) RewriteTemplates(ctx context.Context, s Site) ([]string, error) {
	r, err := c.rewriteList(ctx, s)
	var out []string
	for _, name := range r.Rewrite {
		// "0.当前" (0.Current) stands for the site's own rules.
		if !strings.HasPrefix(name, "0.") {
			out = append(out, name)
		}
	}
	return out, err
}

// RewriteTemplate returns the text of one of the RewriteTemplates.
func (c *Client) RewriteTemplate(ctx context.Context, s Site, name string) (string, error) {
	r, err := c.rewriteList(ctx, s)
	if err != nil {
		return "", err
	}
	found := false
	for _, n := range r.Rewrite {
		found = found || n == name && !strings.HasPrefix(n, "0.")
	}
	if !found {
		// Reading a missing path with "rewrite" in it would create it.
		return "", notFound("宝塔面板没有名为 " + name + " 的伪静态模板")
	}
	ws := "nginx"
	if r.SitePath != "" {
		ws = "apache" // OpenLiteSpeed uses the Apache templates too
	}
	f, err := c.readFile(ctx, "/www/server/panel/rewrite/"+ws+"/"+name+".conf")
	return f.Content, err
}

// rewritePath is the site's own rewrite rules file: an include of its
// nginx config, or .htaccess in its run directory under Apache.
func (c *Client) rewritePath(ctx context.Context, s Site) (string, error) {
	r, err := c.rewriteList(ctx, s)
	if err != nil {
		return "", err
	}
	if r.SitePath != "" {
		return strings.TrimRight(string(r.SitePath), "/") + "/.htaccess", nil
	}
	return "/www/server/panel/vhost/rewrite/" + confPrefix(s.ProjectType) + s.Name + ".conf", nil
}

// Rewrite reads a site's rewrite rules (伪静态). The panel creates the
// file empty if it is missing.
func (c *Client) Rewrite(ctx context.Context, s Site) (File, error) {
	path, err := c.rewritePath(ctx, s)
	if err != nil {
		return File{}, err
	}
	return c.readFile(ctx, path)
}

// SetRewrite replaces a site's rewrite rules; modTime, when not empty, is
// File.ModTime from Rewrite. The panel tests and restores them like
// SetNginxConf does.
func (c *Client) SetRewrite(ctx context.Context, s Site, content, modTime string) error {
	path, err := c.rewritePath(ctx, s)
	if err != nil {
		return err
	}
	return c.writeFile(ctx, path, content, modTime)
}

// logUnescape undoes what the panel does to log text for its web page
// (panelSite.xsssec): <>'" become full-width, then HTML escaping.
var logUnescape = strings.NewReplacer("＜", "<", "＞", ">", "＇", "'", "＂", `"`)

// SiteLog returns the last lines of a site's access log (at most the
// 1000 the panel sends; all of them when lines is 0).
func (c *Client) SiteLog(ctx context.Context, s Site, lines int) (string, error) {
	return c.siteLog(ctx, s, lines, "GetSiteLogs", "")
}

// SiteErrorLog returns the last lines of a site's error log.
func (c *Client) SiteErrorLog(ctx context.Context, s Site, lines int) (string, error) {
	// aaPanel names this action get_site_err_log.
	return c.siteLog(ctx, s, lines, "get_site_errlog", "get_site_err_log")
}

func (c *Client) siteLog(ctx context.Context, s Site, lines int, action, fallback string) (string, error) {
	form := url.Values{"siteName": {s.Name}}
	var r struct {
		Msg text `json:"msg"`
	}
	err := c.do(ctx, "site", action, form, &r)
	var pe *Error
	if errors.As(err, &pe) && pe.Reason == ReasonUnknownAction && fallback != "" {
		err = c.do(ctx, "site", fallback, form, &r)
	}
	if errors.As(err, &pe) && (pe.Message == "日志为空" || pe.Message == "Log is empty") {
		return "", nil // no log file yet
	}
	if err != nil {
		return "", err
	}
	out := strings.TrimRight(logUnescape.Replace(html.UnescapeString(string(r.Msg))), "\n")
	if all := strings.Split(out, "\n"); lines > 0 && len(all) > lines {
		out = strings.Join(all[len(all)-lines:], "\n")
	}
	return out, nil
}

// Backup is one backup the panel recorded, made by hand or by a 计划任务.
type Backup struct {
	ID      int
	Type    int    // 0 a site's files, 1 a database, as the panel numbers them
	OwnerID int    // the site's or database's ID
	Name    string // the archive's file name
	File    string // its path on the server; path|storage|name for cloud copies
	Size    int64
	Created string
	Remark  string
	Running bool // still being written
	Missing bool // no local file (gone, or only in cloud storage)
}

type backupRow struct {
	ID           num  `json:"id"`
	PID          num  `json:"pid"`
	Name         text `json:"name"`
	Filename     text `json:"filename"`
	Size         num  `json:"size"`
	AddTime      text `json:"addtime"`
	PS           text `json:"ps"`
	LocalExist   *num `json:"localexist"`
	BackupStatus num  `json:"backup_status"`
}

func (r backupRow) key() int { return int(r.ID) }

func (c *Client) backups(ctx context.Context, typ, owner int) ([]Backup, error) {
	rows, err := allRows[backupRow](ctx, c, url.Values{
		"table": {"backup"}, "type": {strconv.Itoa(typ)}, "search": {strconv.Itoa(owner)}, "limit": {"100"},
	})
	var out []Backup
	for _, r := range rows {
		out = append(out, Backup{ID: int(r.ID), Type: typ, OwnerID: int(r.PID), Name: string(r.Name), File: string(r.Filename),
			Size: int64(r.Size), Created: string(r.AddTime), Remark: string(r.PS), Running: r.BackupStatus == 1,
			Missing: r.LocalExist != nil && *r.LocalExist == 0})
	}
	return out, err
}

// SiteBackups lists a site's backups, newest first.
func (c *Client) SiteBackups(ctx context.Context, s Site) ([]Backup, error) {
	return c.backups(ctx, 0, s.ID)
}

// DatabaseBackups lists a database's backups, newest first.
func (c *Client) DatabaseBackups(ctx context.Context, d Database) ([]Backup, error) {
	return c.backups(ctx, 1, d.ID)
}

// newBackup runs a backup action and finds the record it added, which the
// panel does not return.
func (c *Client) newBackup(ctx context.Context, module string, list func() ([]Backup, error), form url.Values) (Backup, error) {
	before, err := list()
	if err != nil {
		return Backup{}, err
	}
	last := 0
	for _, b := range before {
		last = max(last, b.ID)
	}
	if _, err := c.call(ctx, module, "ToBackup", form); err != nil {
		return Backup{}, err
	}
	after, err := list()
	if err != nil {
		return Backup{}, err
	}
	var made *Backup
	for i, b := range after {
		if b.ID > last && (made == nil || b.ID > made.ID) {
			made = &after[i]
		}
	}
	switch {
	case made == nil:
		return Backup{}, &Error{Status: http.StatusOK, Action: module + "/ToBackup", Reason: ReasonBadAnswer, Message: "面板说备份成功，但备份列表里没有新的备份"}
	case made.Size == 0 && !made.Running:
		// The panel records a site backup without checking tar's result.
		return *made, &Error{Status: http.StatusOK, Action: module + "/ToBackup", Reason: ReasonRefused, Message: "备份文件是空的：" + made.File}
	}
	return *made, nil
}

// BackupSite archives a site's directory (tar.gz under the panel's
// backup folder) and returns the new backup. The panel does the work
// inside the call.
func (c *Client) BackupSite(ctx context.Context, s Site) (Backup, error) {
	return c.newBackup(ctx, "site", func() ([]Backup, error) { return c.SiteBackups(ctx, s) },
		url.Values{"id": {strconv.Itoa(s.ID)}})
}

// BackupDatabase dumps a database and returns the new backup.
func (c *Client) BackupDatabase(ctx context.Context, d Database) (Backup, error) {
	return c.newBackup(ctx, "database", func() ([]Backup, error) { return c.DatabaseBackups(ctx, d) },
		url.Values{"id": {strconv.Itoa(d.ID)}})
}

// DeleteBackup deletes a backup's record and its local file.
func (c *Client) DeleteBackup(ctx context.Context, b Backup) error {
	module := "site"
	if b.Type == 1 {
		module = "database"
	}
	_, err := c.call(ctx, module, "DelBackup", url.Values{"id": {strconv.Itoa(b.ID)}})
	return err
}

// RestoreDatabase imports one of a database's backups into it,
// overwriting what is there. Site backups have no restore action in
// 宝塔 (aaPanel's files/restore_website is not in 宝塔 11), so none is
// offered for them.
func (c *Client) RestoreDatabase(ctx context.Context, d Database, b Backup) error {
	_, err := c.call(ctx, "database", "InputSql", url.Values{"name": {d.Name}, "file": {b.File}})
	return err
}

// Database is a MySQL database the panel manages. Its password, which
// the panel's list includes, is left out.
type Database struct {
	ID          int
	Name        string
	User        string
	Access      string // who may connect: 127.0.0.1, % for anyone, or listed addresses
	Remark      string
	SiteID      int // the site it was created with, 0 if none
	ServerID    int // 0 for this server's MySQL, else a remote server the panel manages
	Created     string
	BackupCount int
}

type dbRow struct {
	ID          num  `json:"id"`
	PID         num  `json:"pid"`
	SID         num  `json:"sid"`
	Name        text `json:"name"`
	Username    text `json:"username"`
	Accept      text `json:"accept"`
	PS          text `json:"ps"`
	AddTime     text `json:"addtime"`
	BackupCount num  `json:"backup_count"`
}

func (r dbRow) key() int { return int(r.ID) }

// Databases lists the MySQL databases.
func (c *Client) Databases(ctx context.Context) ([]Database, error) {
	rows, err := allRows[dbRow](ctx, c, url.Values{"table": {"databases"}, "limit": {"100"}})
	var out []Database
	for _, r := range rows {
		out = append(out, Database{ID: int(r.ID), Name: string(r.Name), User: string(r.Username), Access: string(r.Accept),
			Remark: html.UnescapeString(string(r.PS)), SiteID: int(r.PID), ServerID: int(r.SID), Created: string(r.AddTime),
			BackupCount: int(r.BackupCount)})
	}
	return out, err
}

// NewDatabase is what creating a database needs.
type NewDatabase struct {
	Name     string
	User     string // the database's name when empty
	Password string
	Access   string // 127.0.0.1 when empty; % lets any address in
	Charset  string // utf8mb4 when empty; also utf8, gbk or big5
	Remark   string
	SiteID   int // the site it belongs to, if any
}

// CreateDatabase creates a database on this server's MySQL with a user
// that has all privileges on it.
func (c *Client) CreateDatabase(ctx context.Context, n NewDatabase) (Database, error) {
	if n.Password == "" {
		// The panel would make one up and not say what it is.
		return Database{}, errors.New("需要设置数据库密码")
	}
	user, access, charset := n.User, n.Access, n.Charset
	if user == "" {
		user = n.Name
	}
	if access == "" {
		access = "127.0.0.1"
	}
	if charset == "" {
		charset = "utf8mb4"
	}
	form := url.Values{
		"name": {n.Name}, "db_user": {user}, "password": {n.Password}, "codeing": {charset},
		// The page sends the access choice as dataAccess and the addresses
		// as address; the panel reads only address.
		"dataAccess": {access}, "address": {access}, "ps": {n.Remark}, "sid": {"0"},
	}
	if n.SiteID > 0 {
		form.Set("pid", strconv.Itoa(n.SiteID))
	}
	if _, err := c.call(ctx, "database", "AddDatabase", form); err != nil {
		return Database{}, err
	}
	dbs, err := c.Databases(ctx)
	if err != nil {
		return Database{}, err
	}
	for _, d := range dbs {
		if strings.EqualFold(d.Name, n.Name) {
			return d, nil
		}
	}
	return Database{}, notFound("宝塔面板说数据库已创建，但列表里没有 " + n.Name)
}

// DeleteDatabase drops a database and its user. When the panel's
// database recycle bin is on, it moves the database there instead.
func (c *Client) DeleteDatabase(ctx context.Context, d Database) error {
	_, err := c.call(ctx, "database", "DeleteDatabase", url.Values{"id": {strconv.Itoa(d.ID)}, "name": {d.Name}})
	return err
}
