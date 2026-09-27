package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/textdiff"
)

// The 网站 page: the sites on each 1Panel server, and for one site its
// domains, HTTPS, reverse proxy rules, rewrite rules, config file and
// logs. Reading goes straight to 1Panel; every change the page offers
// becomes a checklist, like the AI's, so it is confirmed first, logged
// and can be undone.

// SiteView is one site in the list.
type SiteView struct {
	ID          uint   `json:"id"`
	Domain      string `json:"domain"`
	Alias       string `json:"alias"`
	Type        string `json:"type"`
	Running     bool   `json:"running"`
	HTTPS       bool   `json:"https"`
	Remark      string `json:"remark,omitempty"`
	SitePath    string `json:"sitePath,omitempty"`
	App         string `json:"app,omitempty"`     // the 1Panel app it runs, if any
	Runtime     string `json:"runtime,omitempty"` // its runtime environment, if any
	CertExpires string `json:"certExpires,omitempty"`
	CertDays    *int   `json:"certDays,omitempty"`
	Parent      string `json:"parent,omitempty"` // the site a subsite belongs to
	CreatedAt   string `json:"createdAt,omitempty"`
}

// OpenRestyView is the web server that serves a 1Panel server's sites.
type OpenRestyView struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Version   string `json:"version,omitempty"`
	HTTPPort  int    `json:"httpPort,omitempty"`
	HTTPSPort int    `json:"httpsPort,omitempty"`
}

// SiteServerView is one server's sites.
type SiteServerView struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	Host      string         `json:"host"`
	NoPanel   bool           `json:"noPanel,omitempty"` // 1Panel's API is not set up
	Error     string         `json:"error,omitempty"`
	OpenResty *OpenRestyView `json:"openresty,omitempty"`
	Sites     []SiteView     `json:"sites"`
}

// WebsitesView is the site list of every 1Panel server.
type WebsitesView struct {
	Servers []SiteServerView `json:"servers"`
	// Others are servers with another panel, which the page cannot manage yet.
	Others []string `json:"others,omitempty"`
}

// ---- a connection per server, kept while the page is used ----

const panelIdle = 5 * time.Minute

type panelConn struct {
	conn sshx.Conn
	used time.Time
	busy int
}

type panelPool struct {
	mu    sync.Mutex
	conns map[int64]*panelConn
	once  sync.Once
}

func (a *App) panelConnFor(ctx context.Context, id int64) (*panelConn, store.Server, error) {
	a.ppool.mu.Lock()
	if pc := a.ppool.conns[id]; pc != nil {
		pc.busy++
		pc.used = time.Now()
		a.ppool.mu.Unlock()
		sv, err := a.Store.GetServer(id)
		return pc, sv, err
	}
	a.ppool.mu.Unlock()
	sv, c, err := a.connect(ctx, id)
	if err != nil {
		return nil, sv, err
	}
	pc := &panelConn{conn: c, used: time.Now(), busy: 1}
	a.ppool.mu.Lock()
	if a.ppool.conns == nil {
		a.ppool.conns = map[int64]*panelConn{}
	}
	if old := a.ppool.conns[id]; old != nil { // opened meanwhile
		old.busy++
		a.ppool.mu.Unlock()
		c.Close()
		return old, sv, nil
	}
	a.ppool.conns[id] = pc
	a.ppool.mu.Unlock()
	a.ppool.once.Do(func() { go a.closeIdlePanels() })
	return pc, sv, nil
}

func (a *App) closeIdlePanels() {
	for range time.Tick(time.Minute) {
		a.ppool.mu.Lock()
		for id, pc := range a.ppool.conns {
			if pc.busy == 0 && time.Since(pc.used) > panelIdle {
				pc.conn.Close()
				delete(a.ppool.conns, id)
			}
		}
		a.ppool.mu.Unlock()
	}
}

func (a *App) releasePanel(id int64, pc *panelConn, drop bool) {
	a.ppool.mu.Lock()
	defer a.ppool.mu.Unlock()
	pc.busy--
	pc.used = time.Now()
	if drop && a.ppool.conns[id] == pc {
		delete(a.ppool.conns, id)
	}
	// A dropped connection closes once nobody uses it any more.
	if a.ppool.conns[id] != pc && pc.busy == 0 {
		pc.conn.Close()
	}
}

// errNoPanel says a server's 1Panel API is not set up.
var errNoPanel = userErr("这台服务器还没有配置 1Panel 接口，请在服务器页面填写 1Panel 的端口和 API 密钥")

// withPanel runs op with the server's 1Panel client, reconnecting once
// when the kept connection turns out to be gone.
func (a *App) withPanel(ctx context.Context, id int64, op func(sv store.Server, c sshx.Conn, p *onepanel.Client) error) error {
	for try := 0; ; try++ {
		pc, sv, err := a.panelConnFor(ctx, id)
		if err != nil {
			return err
		}
		p, err := a.onePanelClient(id, pc.conn)
		if err == nil && p == nil {
			err = errNoPanel
		}
		if err == nil {
			err = op(sv, pc.conn, p)
		}
		lost := err != nil && lostConn(err)
		a.releasePanel(id, pc, lost)
		if lost && try == 0 {
			continue
		}
		return err
	}
}

// ---- the list ----

func certDays(t time.Time) (string, *int) {
	if t.IsZero() || t.Year() < 2000 {
		return "", nil
	}
	return t.Format(time.RFC3339), days(t)
}

func siteViewOf(s onepanel.SiteSummary) SiteView {
	v := SiteView{ID: s.ID, Domain: s.PrimaryDomain, Alias: s.Alias, Type: s.Type, Running: strings.EqualFold(s.Status, "running"),
		HTTPS: strings.EqualFold(s.Protocol, "https"), Remark: s.Remark, SitePath: s.SitePath, App: s.AppName, Runtime: s.RuntimeName, Parent: s.ParentSite}
	if v.HTTPS {
		v.CertExpires, v.CertDays = certDays(s.SSLExpireDate)
	}
	if !s.CreatedAt.IsZero() {
		v.CreatedAt = s.CreatedAt.Format(time.RFC3339)
	}
	return v
}

func (a *App) serverSites(ctx context.Context, sv store.Server) SiteServerView {
	out := SiteServerView{ID: sv.ID, Name: sv.Name, Host: sv.Host, Sites: []SiteView{}}
	if s, err := a.OnePanel(sv.ID); err == nil && (!s.HasKey || s.Port == 0) {
		out.NoPanel = true
		return out
	}
	err := a.withPanel(ctx, sv.ID, func(_ store.Server, _ sshx.Conn, p *onepanel.Client) error {
		var wg sync.WaitGroup
		var apps []onepanel.InstalledApp
		var appErr error
		wg.Add(1)
		go func() {
			defer wg.Done()
			apps, appErr = p.InstalledApps(ctx)
		}()
		sites, err := p.SearchWebsites(ctx)
		wg.Wait()
		if err != nil {
			return err
		}
		for _, s := range sites {
			out.Sites = append(out.Sites, siteViewOf(s))
		}
		if appErr == nil {
			or := &OpenRestyView{}
			for _, ap := range apps {
				if ap.AppKey == "openresty" {
					or = &OpenRestyView{Installed: true, Running: strings.EqualFold(ap.Status, "running"), Version: ap.Version, HTTPPort: ap.HTTPPort, HTTPSPort: ap.HTTPSPort}
				}
			}
			out.OpenResty = or
		}
		return nil
	})
	switch {
	case errors.Is(err, errNoPanel):
		out.NoPanel = true
	case err != nil:
		out.Error = err.Error()
	}
	return out
}

// Websites lists the sites of every 1Panel server, asking them all at once.
func (a *App) Websites(ctx context.Context) (WebsitesView, error) {
	servers, err := a.Store.ListServers()
	if err != nil {
		return WebsitesView{}, err
	}
	v := WebsitesView{Servers: []SiteServerView{}}
	var panels []store.Server
	for _, sv := range servers {
		switch sv.Adapter {
		case "1panel":
			panels = append(panels, sv)
		case "bt":
			v.Others = append(v.Others, sv.Name+"（宝塔）")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out := make([]SiteServerView, len(panels))
	var wg sync.WaitGroup
	for i, sv := range panels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = a.serverSites(ctx, sv)
		}()
	}
	wg.Wait()
	v.Servers = append(v.Servers, out...)
	return v, nil
}

// ---- one site ----

// SiteHTTPSView is a site's HTTPS setting and the certificate it uses.
type SiteHTTPSView struct {
	Enable     bool     `json:"enable"`
	Mode       string   `json:"mode,omitempty"` // HTTPAlso, HTTPToHTTPS, HTTPSOnly
	HSTS       bool     `json:"hsts"`
	HTTP3      bool     `json:"http3"`
	Cert       string   `json:"cert,omitempty"` // the certificate's primary domain
	CertNames  []string `json:"certNames,omitempty"`
	Expires    string   `json:"expires,omitempty"`
	Days       *int     `json:"days,omitempty"`
	AutoRenew  bool     `json:"autoRenew"`
	Provider   string   `json:"provider,omitempty"`
	Uncovered  []string `json:"uncovered,omitempty"` // site domains the certificate does not cover
	HTTPSPorts string   `json:"httpsPorts,omitempty"`
}

// CertOption is a certificate the site could use.
type CertOption struct {
	Domain    string   `json:"domain"`
	Names     []string `json:"names"`
	Expires   string   `json:"expires,omitempty"`
	Days      *int     `json:"days,omitempty"`
	AutoRenew bool     `json:"autoRenew"`
	Covers    int      `json:"covers"` // how many of the site's domains it covers
	Ready     bool     `json:"ready"`
}

// ProxyView is one reverse proxy rule.
type ProxyView struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Modifier string `json:"modifier,omitempty"`
	Target   string `json:"target"`
	Host     string `json:"host"`
	Enabled  bool   `json:"enabled"`
	Cache    bool   `json:"cache,omitempty"`
	Cors     bool   `json:"cors,omitempty"`
	Content  string `json:"content"`
}

// SiteDetailView is everything the page shows about one site.
type SiteDetailView struct {
	ServerID      int64                 `json:"serverId"`
	ServerName    string                `json:"serverName"`
	Site          SiteView              `json:"site"`
	Proxy         string                `json:"proxy,omitempty"` // backend of a proxy site
	SiteDir       string                `json:"siteDir,omitempty"`
	AccessLog     bool                  `json:"accessLog"`
	ErrorLog      bool                  `json:"errorLog"`
	Domains       []onepanel.SiteDomain `json:"domains"`
	HTTPS         SiteHTTPSView         `json:"https"`
	Certs         []CertOption          `json:"certs"`
	Proxies       []ProxyView           `json:"proxies"`
	Rewrite       string                `json:"rewrite"`
	RewriteName   string                `json:"rewriteName,omitempty"`
	RewriteHash   string                `json:"rewriteHash"`
	Conf          string                `json:"conf"`
	ConfPath      string                `json:"confPath,omitempty"`
	ConfHash      string                `json:"confHash"`
	HTTPPort      int                   `json:"httpPort,omitempty"`
	EdgeOne       *SiteEOView           `json:"edgeone,omitempty"`  // the domains EdgeOne serves
	Problems      []string              `json:"problems,omitempty"` // parts that could not be read
	AccessLogPath string                `json:"-"`
	ErrorLogPath  string                `json:"-"`
}

func certCovers(names []string, host string) bool {
	host = strings.ToLower(host)
	for _, n := range names {
		n = strings.ToLower(n)
		if n == host || (strings.HasPrefix(n, "*.") && strings.HasSuffix(host, n[1:]) && !strings.Contains(strings.TrimSuffix(host, n[1:]), ".")) {
			return true
		}
	}
	return false
}

// siteDetail reads one site with p, asking 1Panel for its parts at once.
func siteDetail(ctx context.Context, p *onepanel.Client, id uint) (SiteDetailView, error) {
	v := SiteDetailView{Domains: []onepanel.SiteDomain{}, Certs: []CertOption{}, Proxies: []ProxyView{}}
	d, err := p.Website(ctx, id)
	if err != nil {
		return v, err
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		domains []onepanel.SiteDomain
		https   onepanel.HTTPS
		ssls    []onepanel.SSL
		proxies []onepanel.Proxy
		rewrite string
		conf    onepanel.SiteConf
		apps    []onepanel.InstalledApp
	)
	fail := func(what string, err error) {
		if err != nil {
			mu.Lock()
			v.Problems = append(v.Problems, what+"："+err.Error())
			mu.Unlock()
		}
	}
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	run(func() { var e error; domains, e = p.WebsiteDomains(ctx, id); fail("读取域名失败", e) })
	run(func() { var e error; https, e = p.WebsiteHTTPS(ctx, id); fail("读取 HTTPS 设置失败", e) })
	run(func() { var e error; ssls, e = p.SSLs(ctx); fail("读取证书失败", e) })
	run(func() { var e error; apps, e = p.InstalledApps(ctx); fail("读取 OpenResty 失败", e) })
	if d.Type != "stream" {
		run(func() { var e error; proxies, e = p.Proxies(ctx, id); fail("读取反向代理失败", e) })
		run(func() { var e error; rewrite, e = p.Rewrite(ctx, id, "current"); fail("读取伪静态失败", e) })
	}
	run(func() { var e error; conf, e = p.WebsiteConf(ctx, id); fail("读取配置文件失败", e) })
	wg.Wait()
	sort.Strings(v.Problems)

	v.Site = SiteView{ID: d.ID, Domain: d.PrimaryDomain, Alias: d.Alias, Type: d.Type, Running: strings.EqualFold(d.Status, "running"),
		HTTPS: strings.EqualFold(d.Protocol, "https"), Remark: d.Remark, SitePath: d.SitePath, Runtime: d.RuntimeName}
	v.Proxy, v.SiteDir, v.AccessLog, v.ErrorLog = d.Proxy, d.SiteDir, d.AccessLog, d.ErrorLog
	v.AccessLogPath, v.ErrorLogPath = d.AccessLogPath, d.ErrorLogPath
	if domains != nil {
		v.Domains = domains
	}
	names := []string{d.PrimaryDomain}
	for _, dm := range v.Domains {
		if !strings.EqualFold(dm.Domain, d.PrimaryDomain) && !containsFold(names, dm.Domain) {
			names = append(names, dm.Domain)
		}
	}
	v.HTTPS = SiteHTTPSView{Enable: https.Enable, Mode: https.HTTPConfig, HSTS: https.Hsts, HTTP3: https.Http3}
	for _, s := range ssls {
		o := CertOption{Domain: s.PrimaryDomain, Names: s.Names(), AutoRenew: s.AutoRenew, Ready: s.Status == "ready"}
		o.Expires, o.Days = certDays(s.ExpireDate)
		for _, n := range names {
			if certCovers(o.Names, n) {
				o.Covers++
			}
		}
		if https.Enable && s.ID == https.SSL.ID {
			v.HTTPS.Cert, v.HTTPS.CertNames, v.HTTPS.Expires, v.HTTPS.Days = s.PrimaryDomain, o.Names, o.Expires, o.Days
			v.HTTPS.AutoRenew, v.HTTPS.Provider = s.AutoRenew, s.Provider
			for _, n := range names {
				if !certCovers(o.Names, n) {
					v.HTTPS.Uncovered = append(v.HTTPS.Uncovered, n)
				}
			}
		}
		if o.Covers > 0 {
			v.Certs = append(v.Certs, o)
		}
	}
	sort.SliceStable(v.Certs, func(i, j int) bool {
		if v.Certs[i].Covers != v.Certs[j].Covers {
			return v.Certs[i].Covers > v.Certs[j].Covers
		}
		return v.Certs[i].Expires > v.Certs[j].Expires
	})
	for _, pr := range proxies {
		v.Proxies = append(v.Proxies, ProxyView{Name: pr.Name, Path: pr.Match, Modifier: pr.Modifier, Target: pr.ProxyPass, Host: pr.ProxyHost,
			Enabled: pr.Enable, Cache: pr.Cache, Cors: pr.Cors, Content: pr.Content})
	}
	sort.SliceStable(v.Proxies, func(i, j int) bool { return v.Proxies[i].Path < v.Proxies[j].Path })
	v.Rewrite, v.RewriteName, v.RewriteHash = rewrite, d.Rewrite, actions.ConfHash(rewrite)
	v.Conf, v.ConfPath, v.ConfHash = conf.Content, conf.Path, actions.ConfHash(conf.Content)
	for _, ap := range apps {
		if ap.AppKey == "openresty" {
			v.HTTPPort = ap.HTTPPort
		}
	}
	return v, nil
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// Website reads one site of a server.
func (a *App) Website(ctx context.Context, serverID int64, siteID uint) (SiteDetailView, error) {
	var v SiteDetailView
	err := a.withPanel(ctx, serverID, func(sv store.Server, _ sshx.Conn, p *onepanel.Client) error {
		var err error
		if v, err = siteDetail(ctx, p, siteID); err != nil {
			return userErr("读取网站失败：%v", err)
		}
		v.ServerID, v.ServerName = sv.ID, sv.Name
		return nil
	})
	if err != nil {
		return v, err
	}
	names := []string{v.Site.Domain}
	for _, d := range v.Domains {
		if !containsFold(names, d.Domain) {
			names = append(names, d.Domain)
		}
	}
	eoCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	v.EdgeOne = a.siteEdgeOne(eoCtx, names)
	return v, nil
}

// SiteLog is the end of a site's access or error log.
type SiteLog struct {
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
	Lines   string `json:"lines"`
}

// WebsiteLog returns the last lines of a site's access or error log.
func (a *App) WebsiteLog(ctx context.Context, serverID int64, siteID uint, kind string, lines int) (SiteLog, error) {
	if kind != "access" && kind != "error" {
		return SiteLog{}, userErr("日志只有 access 和 error 两种")
	}
	lines = min(max(lines, 20), 2000)
	var out SiteLog
	err := a.withPanel(ctx, serverID, func(sv store.Server, c sshx.Conn, p *onepanel.Client) error {
		d, err := p.Website(ctx, siteID)
		if err != nil {
			return userErr("读取网站失败：%v", err)
		}
		out.Path, out.Enabled = d.AccessLogPath, d.AccessLog
		if kind == "error" {
			out.Path, out.Enabled = d.ErrorLogPath, d.ErrorLog
		}
		if out.Path == "" {
			out.Path = path.Join(d.SitePath, "log", kind+".log")
		}
		if !strings.HasPrefix(out.Path, "/") || strings.Contains(out.Path, "..") {
			return userErr("日志路径不对：%s", out.Path)
		}
		cmd := fmt.Sprintf("if [ -f %s ]; then tail -n %d -- %s; else echo MIAO_NOLOG; fi", shq(out.Path), lines, shq(out.Path))
		if sv.Username != "" && sv.Username != "root" {
			cmd = "sudo -n sh -c " + shq(cmd)
		}
		res, err := c.Run(ctx, cmd, "", 4<<20)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return userErr("读取日志失败：%s", strings.TrimSpace(res.Stderr))
		}
		if strings.TrimSpace(res.Stdout) != "MIAO_NOLOG" {
			out.Lines = res.Stdout
		}
		return nil
	})
	return out, err
}

// ---- changes ----

// SiteRequest is a change asked for on the 网站 page.
type SiteRequest struct {
	ServerID int64  `json:"serverId"`
	Site     string `json:"site"` // the site's primary domain
	Op       string `json:"op"`
	// For domains.
	Domain string `json:"domain,omitempty"`
	Port   int    `json:"port,omitempty"`
	// For HTTPS.
	Enabled  *bool  `json:"enabled,omitempty"`
	Cert     string `json:"cert,omitempty"`
	HTTPMode string `json:"httpMode,omitempty"`
	HSTS     *bool  `json:"hsts,omitempty"`
	HTTP3    *bool  `json:"http3,omitempty"`
	// For reverse proxy rules.
	Name   string `json:"name,omitempty"`
	Path   string `json:"path,omitempty"`
	Target string `json:"target,omitempty"`
	Host   string `json:"host,omitempty"`
	// For the config file and rewrite rules.
	Content  string `json:"content,omitempty"`
	Template string `json:"template,omitempty"`
	BaseHash string `json:"baseHash,omitempty"`
	// For new sites.
	Type  string `json:"type,omitempty"` // static or proxy
	Proxy string `json:"proxy,omitempty"`
	// For certificates.
	OtherDomains []string `json:"otherDomains,omitempty"`
	Email        string   `json:"email,omitempty"`
}

func switchOf(b *bool) string {
	if b == nil {
		return ""
	}
	if *b {
		return "on"
	}
	return "off"
}

var httpModeName = map[string]string{
	"HTTPAlso":    "HTTP 和 HTTPS 都能访问",
	"HTTPToHTTPS": "HTTP 自动跳转到 HTTPS",
	"HTTPSOnly":   "只能用 HTTPS 访问",
}

// ProposeWebsite turns a change on the 网站 page into a checklist.
func (a *App) ProposeWebsite(ctx context.Context, req SiteRequest) (PlanView, error) {
	sv, err := a.Store.GetServer(req.ServerID)
	if err != nil {
		return PlanView{}, userErr("找不到这台服务器（编号 %d）", req.ServerID)
	}
	site := strings.ToLower(strings.TrimSpace(req.Site))
	if site == "" && req.Op != "create" {
		return PlanView{}, userErr("请选择网站")
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	params := map[string]any{"website": site}
	var title, reason, summary, capability string
	switch req.Op {
	case "start", "stop":
		capability, params["action"] = "site.status", req.Op
		if req.Op == "start" {
			title, summary = "启动网站："+site, "启动网站 "+site
			reason = "启动后访客可以正常访问 " + site + "。"
		} else {
			title, summary = "停止网站："+site, "停止网站 "+site
			reason = "停止后访问 " + site + " 会看到 1Panel 的「网站已停止」页面；网站文件、配置、证书都保留，随时可以启动。"
		}
	case "domain_add":
		if req.Port == 0 {
			req.Port = 80
		}
		capability, params["domain"], params["port"] = "site.domain.add", req.Domain, req.Port
		title, summary = "添加域名："+req.Domain, fmt.Sprintf("给网站 %s 添加域名 %s（端口 %d）", site, req.Domain, req.Port)
		reason = fmt.Sprintf("让网站 %s 也响应 %s。域名还要解析到这台服务器（或 EdgeOne）才能访问；网站开着 HTTPS 时，证书也要包含这个域名。", site, req.Domain)
	case "domain_remove":
		capability, params["domain"] = "site.domain.remove", req.Domain
		if req.Port > 0 {
			params["port"] = req.Port
		}
		title, summary = "删除域名："+req.Domain, fmt.Sprintf("删除网站 %s 的域名 %s", site, req.Domain)
		reason = fmt.Sprintf("删除后访问 %s 不会再到网站 %s。DNS 解析不会改动，可以一键加回来。", req.Domain, site)
	case "https":
		capability = "site.https.set"
		if req.Enabled != nil && !*req.Enabled {
			params["enabled"] = "off"
			title, summary = "关闭 HTTPS："+site, "关闭网站 "+site+" 的 HTTPS"
			reason = "关闭后只能用 http:// 访问 " + site + "，用 https:// 访问会失败。如果开过 HSTS，访问过的浏览器会继续用 HTTPS，打不开网站。可以一键恢复。"
			break
		}
		params["enabled"] = "on"
		var parts []string
		if req.Cert != "" {
			params["cert"] = strings.ToLower(req.Cert)
			parts = append(parts, "使用 "+req.Cert+" 的证书")
		}
		if req.HTTPMode != "" {
			params["http_mode"] = req.HTTPMode
			parts = append(parts, httpModeName[req.HTTPMode])
		}
		if s := switchOf(req.HSTS); s != "" {
			params["hsts"] = s
			parts = append(parts, map[string]string{"on": "开启 HSTS", "off": "关闭 HSTS"}[s])
		}
		if s := switchOf(req.HTTP3); s != "" {
			params["http3"] = s
			parts = append(parts, map[string]string{"on": "开启 HTTP/3", "off": "关闭 HTTP/3"}[s])
		}
		title, summary = "设置 HTTPS："+site, "设置网站 "+site+" 的 HTTPS："+strings.Join(parts, "，")
		reason = "修改网站 " + site + " 的 HTTPS 设置：" + strings.Join(parts, "，") + "。不中断访问，可以一键恢复原来的设置。"
		if req.HTTPMode == "HTTPToHTTPS" || req.HTTPMode == "HTTPSOnly" {
			reason += "\n注意：如果这个网站在 EdgeOne 后面并且用 HTTP 回源，跳转到 HTTPS 会让访问陷入循环，这时请选「HTTP 和 HTTPS 都能访问」。"
		}
		if switchOf(req.HSTS) == "on" {
			reason += "\n注意：HSTS 会让浏览器在一年内只用 HTTPS 访问这个网站，撤销也不能让已经访问过的浏览器忘掉。"
		}
	case "cert":
		capability = "cert.issue"
		domain := req.Domain
		if domain == "" {
			domain = site
		}
		params["domain"], params["website"], params["method"], params["apply"] = domain, site, "http", "yes"
		if len(req.OtherDomains) > 0 {
			params["other_domains"] = strings.Join(req.OtherDomains, ",")
		}
		if req.Email != "" {
			params["email"] = req.Email
		}
		mode := req.HTTPMode
		if mode == "" {
			mode = "HTTPAlso"
		}
		params["http_mode"] = mode
		all := append([]string{domain}, req.OtherDomains...)
		title, summary = "申请证书："+domain, fmt.Sprintf("让 1Panel 申请 %s 的免费证书（Let's Encrypt，自动续签），并给网站 %s 开启 HTTPS（%s）", strings.Join(all, "、"), site, httpModeName[mode])
		reason = "Let's Encrypt 会访问 http://" + domain + "/.well-known/acme-challenge/ 来验证，所以域名要已经解析到这台服务器（经过 EdgeOne 也可以），80 端口要能访问。签发一般 1 分钟左右，之后 1Panel 会在到期前自动续签。可以一键撤销（恢复原来的 HTTPS 设置并删除新证书）。"
	case "proxy_set":
		path := strings.TrimSpace(req.Path)
		if path == "" {
			path = "/"
		}
		capability = "site.proxy.set"
		params["name"], params["path"], params["target"] = strings.TrimSpace(req.Name), path, strings.TrimSpace(req.Target)
		if h := strings.TrimSpace(req.Host); h != "" {
			params["host"] = h
		}
		title, summary = "反向代理："+site+path, fmt.Sprintf("网站 %s 的 %s 反向代理到 %s（规则 %s）", site, path, req.Target, req.Name)
		reason = fmt.Sprintf("访问 %s%s 时由 OpenResty 转发到 %s。1Panel 先检查配置，通过才生效，可以一键恢复。", site, path, req.Target)
	case "proxy_remove":
		capability, params["name"] = "site.proxy.remove", req.Name
		title, summary = "删除反向代理："+req.Name, fmt.Sprintf("删除网站 %s 的反向代理规则 %s", site, req.Name)
		reason = "删除后这个路径不再转发到后端。可以一键加回来。"
	case "proxy_on", "proxy_off":
		on := req.Op == "proxy_on"
		capability, params["name"], params["enabled"] = "site.proxy.status", req.Name, map[bool]string{true: "on", false: "off"}[on]
		verb := map[bool]string{true: "启用", false: "停用"}[on]
		title, summary = verb+"反向代理："+req.Name, fmt.Sprintf("%s网站 %s 的反向代理规则 %s", verb, site, req.Name)
		reason = verb + "后" + map[bool]string{true: "这个路径重新转发到后端。", false: "这个路径不再转发到后端，规则保留，随时可以启用。"}[on]
	case "conf":
		capability, params["content"], params["base_hash"] = "site.conf.set", req.Content, req.BaseHash
		title, summary = "修改配置文件："+site, "修改网站 "+site+" 的 Nginx 配置文件"
		reason = "1Panel 会先用 nginx -t 检查新配置，通过后才重新加载 OpenResty（不中断访问）；检查不通过会自动恢复原文件。可以一键恢复修改前的内容。"
	case "rewrite":
		capability, params["content"], params["base_hash"] = "site.rewrite.set", req.Content, req.BaseHash
		if req.Template != "" {
			params["template"] = req.Template
		}
		title, summary = "修改伪静态："+site, "修改网站 "+site+" 的伪静态规则"
		if strings.TrimSpace(req.Content) == "" {
			summary = "清空网站 " + site + " 的伪静态规则"
		}
		reason = "1Panel 检查通过后重新加载 OpenResty（不中断访问）；检查不通过会自动恢复。可以一键恢复原来的规则。"
	case "delete":
		capability = "site.delete"
		title, summary = "删除网站："+site, "删除网站 "+site+"（先备份）"
		reason = "删除后 " + site + " 立即打不开，网站目录和配置会被删除。删除前会先在 1Panel 里打包备份；网站用的应用和数据库保留。这一步不能一键撤销。"
	case "create":
		capability = "site.create"
		delete(params, "website")
		params["domain"], params["type"] = req.Domain, req.Type
		if req.Type == "proxy" {
			params["proxy"] = strings.TrimSpace(req.Proxy)
			title, summary = "新建网站："+req.Domain, fmt.Sprintf("在 1Panel 新建网站 %s，反向代理到 %s", req.Domain, req.Proxy)
		} else {
			title, summary = "新建网站："+req.Domain, "在 1Panel 新建静态网站 "+req.Domain
		}
		reason = "由 1Panel 的 OpenResty 提供服务。域名要解析到这台服务器（或 EdgeOne）才能访问；建好后可以再申请证书开启 HTTPS。可以一键撤销（删除这个网站）。"
	default:
		return PlanView{}, userErr("不支持的操作 %q", req.Op)
	}
	steps := []core.Step{{Capability: capability, Summary: summary, Params: params}}
	// A step that cannot run is reported now rather than shown blocked.
	if _, err := actions.Resolve(capability, params, sv.Adapter); err != nil {
		return PlanView{}, userErr("%v", err)
	}
	p, _, err := a.proposePlan(ctx, "user", sv.ID, title, reason, steps)
	if err != nil {
		return PlanView{}, err
	}
	return a.Plan(p.ID)
}

// fillSiteDiffs works out how the config steps of a checklist change the
// files, so the diff is shown before anything runs. A step prepared
// against an older version of the file is refused now.
func (a *App) fillSiteDiffs(ctx context.Context, sv store.Server, steps []core.Step) error {
	for i := range steps {
		s := &steps[i]
		s.Diffs = nil // only Miao Panel works these out
		if s.Capability != "site.conf.set" && s.Capability != "site.rewrite.set" {
			continue
		}
		site, _ := s.Params["website"].(string)
		content, _ := s.Params["content"].(string)
		if site == "" || sv.Adapter != "1panel" {
			continue
		}
		var cur, name string
		err := a.withPanel(ctx, sv.ID, func(_ store.Server, _ sshx.Conn, p *onepanel.Client) error {
			w, err := findPanelSite(ctx, p, site)
			if err != nil {
				return err
			}
			if s.Capability == "site.conf.set" {
				f, err := p.WebsiteConf(ctx, w.ID)
				cur, name = f.Content, f.Path
				return err
			}
			name = "伪静态规则"
			cur, err = p.Rewrite(ctx, w.ID, "current")
			return err
		})
		if err != nil {
			continue // the step still runs; it just shows no diff
		}
		if h, _ := s.Params["base_hash"].(string); h != "" && h != actions.ConfHash(cur) {
			return userErr("%s在你打开之后被改过了（可能是在 1Panel 里改的），请重新打开再改", map[bool]string{true: "配置文件", false: "伪静态规则"}[s.Capability == "site.conf.set"])
		}
		if s.Params["base_hash"] == nil || s.Params["base_hash"] == "" {
			s.Params["base_hash"] = actions.ConfHash(cur)
		}
		diff := textdiff.Unified(normText(cur), normText(content))
		if diff == "" {
			return userErr("内容没有变化")
		}
		s.Diffs = []core.FileDiff{{Path: name, Diff: diff}}
		add, del := textdiff.Count(diff)
		s.Summary = strings.TrimSpace(s.Summary) + fmt.Sprintf("（新增 %d 行，删除 %d 行）", add, del)
	}
	return nil
}

func normText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, " \t\n")
	if s == "" {
		return ""
	}
	return s + "\n"
}

func findPanelSite(ctx context.Context, p *onepanel.Client, domain string) (onepanel.Website, error) {
	sites, err := p.Websites(ctx)
	if err != nil {
		return onepanel.Website{}, err
	}
	for _, s := range sites {
		if strings.EqualFold(s.PrimaryDomain, domain) {
			return s, nil
		}
	}
	return onepanel.Website{}, fmt.Errorf("1Panel 里没有网站 %s", domain)
}

// toolPanelWebsite shows the AI one site in full, config file included.
func (a *App) toolPanelWebsite(ctx context.Context, raw json.RawMessage) (string, error) {
	var arg struct {
		ServerID int64  `json:"server_id"`
		Website  string `json:"website"`
		LogLines int    `json:"log_lines"`
	}
	if err := parseArgs(raw, &arg); err != nil {
		return "", err
	}
	if arg.LogLines <= 0 {
		arg.LogLines = 30
	}
	arg.LogLines = min(arg.LogLines, 200)
	sv, err := a.Store.GetServer(arg.ServerID)
	if err != nil {
		return "", fmt.Errorf("找不到服务器 %d", arg.ServerID)
	}
	e := a.startExec(store.ExecLog{ServerID: sv.ID, ServerName: sv.Name, Adapter: sv.Adapter, Origin: originOf(ctx),
		Kind: store.ExecRead, Title: "查看 1Panel 网站 " + arg.Website, Via: "1Panel 接口"})
	var b strings.Builder
	err = a.withPanel(ctx, sv.ID, func(_ store.Server, c sshx.Conn, p *onepanel.Client) error {
		w, err := findPanelSite(ctx, p, strings.TrimSpace(arg.Website))
		if err != nil {
			return err
		}
		v, err := siteDetail(ctx, p, w.ID)
		if err != nil {
			return err
		}
		st := map[bool]string{true: "运行中", false: "已停止"}[v.Site.Running]
		fmt.Fprintf(&b, "网站 %s（1Panel 编号 %d）类型=%s 状态=%s 目录=%s\n", v.Site.Domain, v.Site.ID, v.Site.Type, st, orDash(v.Site.SitePath))
		if v.Proxy != "" {
			fmt.Fprintf(&b, "反向代理网站的后端：%s\n", v.Proxy)
		}
		var ds []string
		for _, d := range v.Domains {
			ds = append(ds, fmt.Sprintf("%s:%d", d.Domain, d.Port))
		}
		fmt.Fprintf(&b, "域名：%s\n", orDash(strings.Join(ds, "、")))
		if v.HTTPS.Enable {
			fmt.Fprintf(&b, "HTTPS：开（%s；HSTS=%s；HTTP/3=%s）证书=%s 包含=%s 到期=%s 自动续签=%s\n", httpModeName[v.HTTPS.Mode], yesNoText(v.HTTPS.HSTS), yesNoText(v.HTTPS.HTTP3),
				v.HTTPS.Cert, strings.Join(v.HTTPS.CertNames, "、"), orDash(v.HTTPS.Expires), yesNoText(v.HTTPS.AutoRenew))
			if len(v.HTTPS.Uncovered) > 0 {
				fmt.Fprintf(&b, "注意：证书不包含 %s\n", strings.Join(v.HTTPS.Uncovered, "、"))
			}
		} else {
			b.WriteString("HTTPS：关\n")
		}
		if len(v.Certs) > 0 {
			var cs []string
			for _, c := range v.Certs {
				cs = append(cs, fmt.Sprintf("%s（包含 %s，到期 %s）", c.Domain, strings.Join(c.Names, "、"), orDash(c.Expires)))
			}
			fmt.Fprintf(&b, "1Panel 里能用于这个网站的证书：%s\n", strings.Join(cs, "；"))
		}
		if len(v.Proxies) == 0 {
			b.WriteString("反向代理规则：没有\n")
		}
		for _, pr := range v.Proxies {
			fmt.Fprintf(&b, "反向代理规则 %s：%s %s → %s Host=%s %s\n", pr.Name, pr.Modifier, pr.Path, pr.Target, pr.Host, map[bool]string{true: "启用", false: "停用"}[pr.Enabled])
		}
		if strings.TrimSpace(v.Rewrite) == "" {
			b.WriteString("伪静态规则：没有\n")
		} else {
			fmt.Fprintf(&b, "伪静态规则（模板 %s）：\n%s\n", orDash(v.RewriteName), clipText(v.Rewrite, 4000))
		}
		fmt.Fprintf(&b, "Nginx 配置文件 %s：\n%s\n", v.ConfPath, clipText(v.Conf, 16000))
		for _, p := range v.Problems {
			fmt.Fprintf(&b, "（%s）\n", p)
		}
		for _, kind := range []string{"access", "error"} {
			path := map[string]string{"access": v.AccessLogPath, "error": v.ErrorLogPath}[kind]
			if path == "" || !strings.HasPrefix(path, "/") {
				continue
			}
			cmd := fmt.Sprintf("tail -n %d -- %s 2>/dev/null", arg.LogLines, shq(path))
			if sv.Username != "" && sv.Username != "root" {
				cmd = "sudo -n sh -c " + shq(cmd)
			}
			if res, err := c.Run(ctx, cmd, "", 256<<10); err == nil {
				fmt.Fprintf(&b, "%s 最后 %d 行：\n%s\n", map[string]string{"access": "访问日志", "error": "错误日志"}[kind], arg.LogLines, orDash(clipText(res.Stdout, 12000)))
			}
		}
		return nil
	})
	e.Commands = "# 在服务器本机调用 1Panel 接口（只读），并读取网站日志的最后几行"
	if err != nil {
		a.finishExec(&e, actions.StatusFailed, err.Error())
		return "", err
	}
	a.finishExec(&e, actions.StatusDone, b.String())
	return b.String(), nil
}

func yesNoText(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

var rewriteNameRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// WebsiteRewriteTemplate returns the text of one of 1Panel's rewrite
// templates (wordpress, thinkphp…), to start from on the page.
func (a *App) WebsiteRewriteTemplate(ctx context.Context, serverID int64, siteID uint, name string) (string, error) {
	if !rewriteNameRe.MatchString(name) {
		return "", userErr("模板名称不对")
	}
	var out string
	err := a.withPanel(ctx, serverID, func(_ store.Server, _ sshx.Conn, p *onepanel.Client) error {
		var err error
		if out, err = p.Rewrite(ctx, siteID, name); err != nil {
			return userErr("读取模板失败：%v", err)
		}
		return nil
	})
	return out, err
}
