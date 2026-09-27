package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
)

// ---- Managing 1Panel websites ----
//
// Every change goes through 1Panel's own API, which writes the OpenResty
// config, tests it with nginx -t and reloads; a config that fails the test
// is put back by 1Panel before it answers. Each step records what it
// replaced so it can be put back later.

var (
	proxyNameRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	proxyTargetRe = regexp.MustCompile(`^https?://([A-Za-z0-9_.-]+|\[[0-9A-Fa-f:]+\])(:[0-9]{1,5})?(/[A-Za-z0-9._~/%-]*)?$`)
	proxyHostRe   = regexp.MustCompile(`^(\$host|\$proxy_host|\$http_host|[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?)$`)
	proxyPathRe   = regexp.MustCompile(`^/[A-Za-z0-9._~/%-]*$`)
)

// ConfHash identifies a config file's text, so a change prepared against
// one version is not written over a newer one.
func ConfHash(content string) string {
	sum := sha256.Sum256([]byte(normConf(content)))
	return hex.EncodeToString(sum[:8])
}

// normConf is how config text is compared and written: Unix line ends and
// one final newline.
func normConf(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, " \t\n")
	if s == "" {
		return ""
	}
	return s + "\n"
}

func checkProxyParams(v map[string]string) error {
	if !proxyNameRe.MatchString(v["name"]) {
		return fmt.Errorf("规则名称只能用字母、数字、_ 和 -（最多 32 个字符），%q 不行", v["name"])
	}
	if t, ok := v["target"]; ok && !proxyTargetRe.MatchString(t) {
		return fmt.Errorf("target 要是后端地址，例如 http://127.0.0.1:8080 或 https://api.example.com，%q 不是", t)
	}
	if p, ok := v["path"]; ok && !proxyPathRe.MatchString(p) {
		return fmt.Errorf("path 只能用字母、数字和 ._~/%%-，例如 /api，%q 不行", p)
	}
	if h, ok := v["host"]; ok && !proxyHostRe.MatchString(h) {
		return fmt.Errorf("host 填 $host（沿用访客访问的域名）、$proxy_host（后端地址里的域名）或者一个域名，%q 不行", h)
	}
	return nil
}

// panelSite finds a site by its primary domain.
func panelSite(ctx context.Context, c *onepanel.Client, domain string) (onepanel.Website, error) {
	sites, err := c.Websites(ctx)
	if err != nil {
		return onepanel.Website{}, fmt.Errorf("读取 1Panel 网站列表失败：%w", err)
	}
	for _, s := range sites {
		if strings.EqualFold(s.PrimaryDomain, domain) {
			return s, nil
		}
	}
	return onepanel.Website{}, fmt.Errorf("1Panel 里没有网站 %s", domain)
}

func siteIDOf(undo map[string]string) uint {
	id, _ := strconv.ParseUint(undo["website_id"], 10, 64)
	return uint(id)
}

// sameSite makes sure an undo still targets the site it changed: ids are
// reused after a site is deleted and another created.
func sameSite(ctx context.Context, c *onepanel.Client, undo map[string]string) (uint, error) {
	id := siteIDOf(undo)
	d, err := c.Website(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("找不到网站 %s：%w", undo["domain"], err)
	}
	if !strings.EqualFold(d.PrimaryDomain, undo["domain"]) {
		return 0, fmt.Errorf("网站 %s 已经不在了", undo["domain"])
	}
	return id, nil
}

func refused(out *Outcome, format string, args ...any) Outcome {
	out.Status = StatusRefused
	out.logf(format, args...)
	return *out
}

func (out *Outcome) remember(site onepanel.Website) {
	out.Undo["website_id"], out.Undo["domain"] = strconv.FormatUint(uint64(site.ID), 10), site.PrimaryDomain
}

// httpPort is the port OpenResty serves HTTP on.
func httpPort(ctx context.Context, c *onepanel.Client) int {
	if apps, err := c.InstalledApps(ctx); err == nil {
		for _, a := range apps {
			if a.AppKey == "openresty" && a.HTTPPort > 0 {
				return a.HTTPPort
			}
		}
	}
	return 80
}

// ---- start / stop ----

func applySiteStatus(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	run := v["action"] == "start"
	running := strings.EqualFold(site.Status, "running")
	if run == running {
		report("网站 %s 已经是%s状态，不需要修改", site.PrimaryDomain, map[bool]string{true: "运行", false: "停止"}[run])
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	report("正在%s网站 %s", map[bool]string{true: "启动", false: "停止"}[run], site.PrimaryDomain)
	if err := c.SetWebsiteRunning(ctx, site.ID, run); err != nil {
		return refused(out, "操作失败：%v", err)
	}
	out.remember(site)
	out.Undo["running"] = strconv.FormatBool(running)
	if run {
		report("完成：%s 恢复访问", site.PrimaryDomain)
	} else {
		report("完成：访问 %s 会看到 1Panel 的「网站已停止」页面，网站文件和配置都还在", site.PrimaryDomain)
	}
	out.Status = StatusDone
	return *out
}

func undoSiteStatus(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	id, err := sameSite(ctx, c, undo)
	if err != nil {
		return err
	}
	return c.SetWebsiteRunning(ctx, id, undo["running"] == "true")
}

// ---- domains ----

func applySiteDomainAdd(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	port, _ := strconv.Atoi(v["port"])
	domains, err := c.WebsiteDomains(ctx, site.ID)
	if err != nil {
		return refused(out, "读取网站的域名失败：%v", err)
	}
	for _, d := range domains {
		if strings.EqualFold(d.Domain, v["domain"]) && d.Port == port {
			report("网站 %s 已经有域名 %s:%d 了", site.PrimaryDomain, d.Domain, port)
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
	}
	report("正在给网站 %s 添加域名 %s（端口 %d）", site.PrimaryDomain, v["domain"], port)
	if err := c.AddWebsiteDomain(ctx, site.ID, v["domain"], port, false); err != nil {
		return refused(out, "添加失败：%v", err)
	}
	out.remember(site)
	out.Undo["added"], out.Undo["port"] = v["domain"], strconv.Itoa(port)
	if https, err := c.WebsiteHTTPS(ctx, site.ID); err == nil && https.Enable {
		if s, err := c.SSL(ctx, https.SSL.ID); err == nil && !covers(s.Names(), v["domain"]) {
			report("注意：网站开着 HTTPS，但现在的证书不包含 %s，用 https:// 访问这个域名时浏览器会报证书错误。可以重新申请一张包含它的证书", v["domain"])
		}
	}
	checkSite(ctx, env, v["domain"], port, report)
	report("完成：%s 现在也由这个网站提供服务（域名还要解析到这台服务器或 EdgeOne）", v["domain"])
	out.Status = StatusDone
	return *out
}

// covers says whether a certificate for names is valid for host.
func covers(names []string, host string) bool {
	host = strings.ToLower(host)
	for _, n := range names {
		n = strings.ToLower(n)
		if n == host || (strings.HasPrefix(n, "*.") && strings.HasSuffix(host, n[1:]) && !strings.Contains(strings.TrimSuffix(host, n[1:]), ".")) {
			return true
		}
	}
	return false
}

func undoSiteDomainAdd(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	id, err := sameSite(ctx, c, undo)
	if err != nil {
		return err
	}
	domains, err := c.WebsiteDomains(ctx, id)
	if err != nil {
		return err
	}
	port, _ := strconv.Atoi(undo["port"])
	for _, d := range domains {
		if strings.EqualFold(d.Domain, undo["added"]) && d.Port == port {
			return c.DeleteWebsiteDomain(ctx, d.ID)
		}
	}
	return nil // already gone
}

func applySiteDomainRemove(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	if strings.EqualFold(v["domain"], site.PrimaryDomain) {
		return refused(out, "%s 是网站的主域名，不能删除。可以先添加新域名，或者删除整个网站", v["domain"])
	}
	domains, err := c.WebsiteDomains(ctx, site.ID)
	if err != nil {
		return refused(out, "读取网站的域名失败：%v", err)
	}
	port, _ := strconv.Atoi(v["port"])
	var hit []onepanel.SiteDomain
	for _, d := range domains {
		if strings.EqualFold(d.Domain, v["domain"]) && (port == 0 || d.Port == port) {
			hit = append(hit, d)
		}
	}
	if len(hit) == 0 {
		report("网站 %s 没有域名 %s，不需要删除", site.PrimaryDomain, v["domain"])
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	if len(hit) == len(domains) {
		return refused(out, "这是网站仅有的域名，不能删除")
	}
	out.remember(site)
	var removed []onepanel.SiteDomain
	for _, d := range hit {
		report("正在删除网站 %s 的域名 %s（端口 %d）", site.PrimaryDomain, d.Domain, d.Port)
		if err := c.DeleteWebsiteDomain(ctx, d.ID); err != nil {
			if len(removed) == 0 {
				return refused(out, "删除失败：%v", err)
			}
			out.Status = StatusFailed
			out.logf("删除 %s:%d 失败：%v。前面删掉的可以撤销这一步加回来", d.Domain, d.Port, err)
			break
		}
		removed = append(removed, d)
	}
	data, _ := json.Marshal(removed)
	out.Undo["removed"] = string(data)
	if out.Status == StatusFailed {
		return *out
	}
	report("完成：访问 %s 不会再到这个网站（DNS 解析没有改动）", v["domain"])
	out.Status = StatusDone
	return *out
}

func undoSiteDomainRemove(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	id, err := sameSite(ctx, c, undo)
	if err != nil {
		return err
	}
	var removed []onepanel.SiteDomain
	if err := json.Unmarshal([]byte(undo["removed"]), &removed); err != nil {
		return err
	}
	for _, d := range removed {
		if err := c.AddWebsiteDomain(ctx, id, d.Domain, d.Port, d.SSL); err != nil {
			return fmt.Errorf("加回 %s:%d 失败：%w", d.Domain, d.Port, err)
		}
	}
	return nil
}

// ---- HTTPS ----

// pickCert chooses the certificate to use for a site: the named one, or
// the ready certificate covering the most of its domains that lasts
// longest.
func pickCert(ctx context.Context, c *onepanel.Client, name string, domains []string) (onepanel.SSL, error) {
	list, err := c.SSLs(ctx)
	if err != nil {
		return onepanel.SSL{}, fmt.Errorf("读取 1Panel 证书失败：%w", err)
	}
	if name != "" {
		s, ok := findSSL(list, name)
		switch {
		case !ok:
			return s, fmt.Errorf("1Panel 里没有 %s 的证书，可以先申请一张", name)
		case s.Status != "ready":
			return s, fmt.Errorf("%s 的证书还不能用（状态 %s）", name, s.Status)
		}
		return s, nil
	}
	var best onepanel.SSL
	bestN := 0
	for _, s := range list {
		if s.Status != "ready" || s.ExpireDate.Before(time.Now()) {
			continue
		}
		n := 0
		for _, d := range domains {
			if covers(s.Names(), d) {
				n++
			}
		}
		if n == 0 || !covers(s.Names(), domains[0]) {
			continue
		}
		if n > bestN || (n == bestN && s.ExpireDate.After(best.ExpireDate)) {
			best, bestN = s, n
		}
	}
	if best.ID == 0 {
		return best, fmt.Errorf("1Panel 里没有能用于 %s 的证书，可以先申请一张（申请证书会顺便开启 HTTPS）", domains[0])
	}
	return best, nil
}

func applySiteHTTPS(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	prev, err := c.WebsiteHTTPS(ctx, site.ID)
	if err != nil {
		return refused(out, "读取网站 %s 的 HTTPS 设置失败：%v", site.PrimaryDomain, err)
	}
	next := prev
	if v["enabled"] == "off" {
		if !prev.Enable {
			report("网站 %s 本来就没有开启 HTTPS", site.PrimaryDomain)
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
		next = onepanel.HTTPS{Enable: false, HTTPConfig: "HTTPAlso"}
		report("正在关闭网站 %s 的 HTTPS（之后只能用 http:// 访问）", site.PrimaryDomain)
	} else {
		domains := []string{site.PrimaryDomain}
		if list, err := c.WebsiteDomains(ctx, site.ID); err == nil {
			for _, d := range list {
				if !strings.EqualFold(d.Domain, site.PrimaryDomain) {
					domains = append(domains, d.Domain)
				}
			}
		}
		cert := onepanel.SSL{ID: prev.SSL.ID}
		if v["cert"] != "" || !prev.Enable {
			if cert, err = pickCert(ctx, c, v["cert"], domains); err != nil {
				return refused(out, "%v", err)
			}
		}
		next.Enable = true
		next.SSL.ID = cert.ID
		if v["http_mode"] != "" {
			next.HTTPConfig = v["http_mode"]
		} else if !prev.Enable || next.HTTPConfig == "" {
			next.HTTPConfig = "HTTPAlso"
		}
		if v["hsts"] != "" {
			next.Hsts = v["hsts"] == "on"
		}
		if v["http3"] != "" {
			next.Http3 = v["http3"] == "on"
		}
		if prev.Enable && prev.SSL.ID == next.SSL.ID && prev.HTTPConfig == next.HTTPConfig && prev.Hsts == next.Hsts && prev.Http3 == next.Http3 {
			report("网站 %s 的 HTTPS 已经是这样设置的，不需要修改", site.PrimaryDomain)
			out.Status, out.Undo = StatusDone, map[string]string{}
			return *out
		}
		if cert.PrimaryDomain != "" {
			report("使用 1Panel 里 %s 的证书（有效期到 %s）", strings.Join(cert.Names(), "、"), cert.ExpireDate.Format("2006-01-02"))
			for _, d := range domains {
				if !covers(cert.Names(), d) {
					report("注意：这张证书不包含 %s，用 https:// 访问它时浏览器会报证书错误", d)
				}
			}
		}
		extra := ""
		if next.Hsts {
			extra += "，开启 HSTS"
		}
		if next.Http3 {
			extra += "，开启 HTTP/3"
		}
		report("正在给网站 %s 设置 HTTPS：%s%s", site.PrimaryDomain, httpModeText[next.HTTPConfig], extra)
	}
	if err := c.SetWebsiteHTTPS(ctx, site.ID, next); err != nil {
		return refused(out, "设置失败：%v", err)
	}
	data, _ := json.Marshal(prev)
	out.remember(site)
	out.Undo["https"] = string(data)
	if next.Enable {
		port := 443
		if apps, err := c.InstalledApps(ctx); err == nil {
			for _, a := range apps {
				if a.AppKey == "openresty" && a.HTTPSPort > 0 {
					port = a.HTTPSPort
				}
			}
		}
		checkHTTPS(ctx, env, site.PrimaryDomain, port, report)
	}
	report("完成")
	out.Status = StatusDone
	return *out
}

func undoSiteHTTPS(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	id, err := sameSite(ctx, c, undo)
	if err != nil {
		return err
	}
	var prev onepanel.HTTPS
	if err := json.Unmarshal([]byte(undo["https"]), &prev); err != nil {
		return err
	}
	if !prev.Enable {
		prev.HTTPConfig = "HTTPAlso"
	}
	return c.SetWebsiteHTTPS(ctx, id, prev)
}

// ---- reverse proxy rules ----

func findProxy(list []onepanel.Proxy, name string) (onepanel.Proxy, bool) {
	for _, p := range list {
		if p.Name == name {
			return p, true
		}
	}
	return onepanel.Proxy{}, false
}

func applySiteProxySet(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	list, err := c.Proxies(ctx, site.ID)
	if err != nil {
		return refused(out, "读取反向代理规则失败：%v", err)
	}
	for _, p := range list {
		if p.Name != v["name"] && p.Match == v["path"] && p.Enable {
			return refused(out, "路径 %s 已经由规则 %s 代理到 %s 了，请修改那条规则，或者换一个路径", v["path"], p.Name, p.ProxyPass)
		}
	}
	old, exists := findProxy(list, v["name"])
	p := old
	if !exists {
		p = onepanel.Proxy{ID: site.ID, Name: v["name"], Enable: true, Modifier: "^~", CacheUnit: "m", ServerCacheUnit: "m"}
	}
	if exists && !old.Enable {
		return refused(out, "规则 %s 现在是停用的，请先启用它再修改", v["name"])
	}
	p.ID, p.Match, p.ProxyPass = site.ID, v["path"], v["target"]
	p.ProxyHost = v["host"]
	if p.ProxyHost == "" {
		p.ProxyHost = "$host"
	}
	if p.Modifier == "" {
		p.Modifier = "^~"
	}
	if exists && old.Match == p.Match && old.ProxyPass == p.ProxyPass && old.ProxyHost == p.ProxyHost {
		report("规则 %s 已经是 %s → %s，不需要修改", p.Name, p.Match, p.ProxyPass)
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	op := "create"
	if exists {
		op = "edit"
		report("正在修改网站 %s 的反向代理规则 %s：%s → %s（原来 %s → %s）", site.PrimaryDomain, p.Name, p.Match, p.ProxyPass, old.Match, old.ProxyPass)
	} else {
		report("正在给网站 %s 添加反向代理规则 %s：%s → %s", site.PrimaryDomain, p.Name, p.Match, p.ProxyPass)
	}
	if err := c.SaveProxy(ctx, p, op); err != nil {
		return refused(out, "设置失败（1Panel 没有保存这次改动）：%v", err)
	}
	out.remember(site)
	out.Undo["name"] = p.Name
	if exists {
		out.Undo["content"] = old.Content
	} else {
		out.Undo["created"] = "1"
	}
	checkSite(ctx, env, site.PrimaryDomain, httpPort(ctx, c), report)
	report("完成：访问 %s%s 会转到 %s", site.PrimaryDomain, p.Match, p.ProxyPass)
	out.Status = StatusDone
	return *out
}

func undoSiteProxySet(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	id, err := sameSite(ctx, c, undo)
	if err != nil {
		return err
	}
	if undo["created"] == "1" {
		list, err := c.Proxies(ctx, id)
		if err != nil {
			return err
		}
		if _, ok := findProxy(list, undo["name"]); !ok {
			return nil // already gone
		}
		return c.DeleteProxy(ctx, id, undo["name"])
	}
	return c.WriteProxyFile(ctx, id, undo["name"], undo["content"])
}

func applySiteProxyRemove(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	list, err := c.Proxies(ctx, site.ID)
	if err != nil {
		return refused(out, "读取反向代理规则失败：%v", err)
	}
	old, ok := findProxy(list, v["name"])
	if !ok {
		report("网站 %s 没有叫 %s 的反向代理规则，不需要删除", site.PrimaryDomain, v["name"])
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	report("正在删除网站 %s 的反向代理规则 %s（%s → %s）", site.PrimaryDomain, old.Name, old.Match, old.ProxyPass)
	if err := c.DeleteProxy(ctx, site.ID, old.Name); err != nil {
		return refused(out, "删除失败：%v", err)
	}
	data, _ := json.Marshal(old)
	out.remember(site)
	out.Undo["proxy"] = string(data)
	if site.Type == "proxy" && old.Match == "/" {
		report("注意：这是反向代理网站的主规则，删除后网站首页会变成 404")
	}
	report("完成")
	out.Status = StatusDone
	return *out
}

func undoSiteProxyRemove(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	id, err := sameSite(ctx, c, undo)
	if err != nil {
		return err
	}
	var p onepanel.Proxy
	if err := json.Unmarshal([]byte(undo["proxy"]), &p); err != nil {
		return err
	}
	p.ID = id
	if err := c.SaveProxy(ctx, p, "create"); err != nil {
		return err
	}
	// The rule's own text, exactly as it was.
	if p.Content != "" {
		if err := c.WriteProxyFile(ctx, id, p.Name, p.Content); err != nil {
			return err
		}
	}
	if !p.Enable {
		return c.SetProxyEnabled(ctx, id, p.Name, false)
	}
	return nil
}

func applySiteProxyStatus(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	list, err := c.Proxies(ctx, site.ID)
	if err != nil {
		return refused(out, "读取反向代理规则失败：%v", err)
	}
	p, ok := findProxy(list, v["name"])
	if !ok {
		return refused(out, "网站 %s 没有叫 %s 的反向代理规则", site.PrimaryDomain, v["name"])
	}
	on := v["enabled"] == "on"
	if p.Enable == on {
		report("规则 %s 已经是%s的", p.Name, map[bool]string{true: "启用", false: "停用"}[on])
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	report("正在%s网站 %s 的反向代理规则 %s（%s → %s）", map[bool]string{true: "启用", false: "停用"}[on], site.PrimaryDomain, p.Name, p.Match, p.ProxyPass)
	if err := c.SetProxyEnabled(ctx, site.ID, p.Name, on); err != nil {
		return refused(out, "操作失败：%v", err)
	}
	out.remember(site)
	out.Undo["name"], out.Undo["enabled"] = p.Name, strconv.FormatBool(p.Enable)
	report("完成")
	out.Status = StatusDone
	return *out
}

func undoSiteProxyStatus(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	id, err := sameSite(ctx, c, undo)
	if err != nil {
		return err
	}
	return c.SetProxyEnabled(ctx, id, undo["name"], undo["enabled"] == "true")
}

// ---- the config file and rewrite rules ----

func applySiteConf(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	cur, err := c.WebsiteConf(ctx, site.ID)
	if err != nil {
		return refused(out, "读取配置文件失败：%v", err)
	}
	next := normConf(v["content"])
	if normConf(cur.Content) == next {
		report("配置文件和现在的一样，不需要修改")
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	if h := v["base_hash"]; h != "" && h != ConfHash(cur.Content) {
		return refused(out, "配置文件在这份清单生成之后被改过了（可能是在 1Panel 里改的），为了不覆盖那些改动，这一步没有执行。请重新打开配置文件再改")
	}
	report("正在写入网站 %s 的配置文件 %s（1Panel 会先用 nginx -t 检查，通过后才重新加载 OpenResty）", site.PrimaryDomain, cur.Path)
	if err := c.UpdateWebsiteConf(ctx, site.ID, next); err != nil {
		return refused(out, "新配置没有通过检查，1Panel 已经恢复了原来的文件：%v", err)
	}
	out.remember(site)
	out.Undo["content"] = cur.Content
	checkSite(ctx, env, site.PrimaryDomain, httpPort(ctx, c), report)
	report("完成：新配置已生效")
	out.Status = StatusDone
	return *out
}

func undoSiteConf(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	id, err := sameSite(ctx, c, undo)
	if err != nil {
		return err
	}
	return c.UpdateWebsiteConf(ctx, id, undo["content"])
}

func applySiteRewrite(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	d, err := c.Website(ctx, site.ID)
	if err != nil {
		return refused(out, "读取网站信息失败：%v", err)
	}
	cur, err := c.Rewrite(ctx, site.ID, "current")
	if err != nil {
		return refused(out, "读取伪静态规则失败：%v", err)
	}
	next := normConf(v["content"])
	name := v["template"]
	if name == "" {
		name = d.Rewrite
	}
	if name == "" {
		name = "default"
	}
	if normConf(cur) == next && name == d.Rewrite {
		report("伪静态规则和现在的一样，不需要修改")
		out.Status, out.Undo = StatusDone, map[string]string{}
		return *out
	}
	if h := v["base_hash"]; h != "" && h != ConfHash(cur) {
		return refused(out, "伪静态规则在这份清单生成之后被改过了，为了不覆盖那些改动，这一步没有执行。请重新打开再改")
	}
	if next == "" {
		report("正在清空网站 %s 的伪静态规则", site.PrimaryDomain)
	} else {
		report("正在写入网站 %s 的伪静态规则（1Panel 会先检查，通过后才重新加载 OpenResty）", site.PrimaryDomain)
	}
	if err := c.UpdateRewrite(ctx, site.ID, name, next); err != nil {
		return refused(out, "新规则没有通过检查，1Panel 已经恢复了原来的规则：%v", err)
	}
	out.remember(site)
	out.Undo["content"], out.Undo["template"] = cur, d.Rewrite
	checkSite(ctx, env, site.PrimaryDomain, httpPort(ctx, c), report)
	report("完成：新规则已生效")
	out.Status = StatusDone
	return *out
}

func undoSiteRewrite(ctx context.Context, c *onepanel.Client, undo map[string]string) error {
	id, err := sameSite(ctx, c, undo)
	if err != nil {
		return err
	}
	name := undo["template"]
	if name == "" {
		name = "default"
	}
	return c.UpdateRewrite(ctx, id, name, undo["content"])
}

// ---- deleting a site ----

func applySiteDelete(ctx context.Context, env *Env, v map[string]string, out *Outcome, report func(string, ...any)) Outcome {
	c := env.OnePanel
	site, err := panelSite(ctx, c, v["website"])
	if err != nil {
		return refused(out, "%v", err)
	}
	if site.Alias == "" {
		return refused(out, "读不到网站 %s 的目录名，没有删除", site.PrimaryDomain)
	}
	task := newTaskID()
	report("先备份网站 %s（网站目录和配置）……", site.PrimaryDomain)
	if err := c.Backup(ctx, "website", site.Alias, site.Alias, task); err != nil {
		return refused(out, "备份失败，没有删除网站：%v", err)
	}
	deadline := time.Now().Add(backupTimeout)
	for {
		rec, found, err := c.FindBackup(ctx, "website", site.Alias, site.Alias, task)
		if err == nil && found && rec.Status == "Success" {
			report("已备份到服务器上的 1Panel 备份目录：%s/%s", rec.FileDir, rec.FileName)
			out.Result = map[string]string{"backup": rec.FileDir + "/" + rec.FileName}
			break
		}
		if err == nil && found && rec.Status == "Failed" {
			return refused(out, "备份失败，没有删除网站：%s", rec.Message)
		}
		if time.Now().After(deadline) {
			return refused(out, "等了 %d 分钟备份还没完成，没有删除网站", int(backupTimeout.Minutes()))
		}
		select {
		case <-ctx.Done():
			return refused(out, "等待备份时超时了，没有删除网站")
		case <-time.After(pollEvery(env)):
		}
	}
	report("正在删除网站 %s（网站用的应用和数据库不删除）", site.PrimaryDomain)
	if err := c.DeleteWebsite(ctx, site.ID); err != nil {
		out.Status = StatusFailed
		out.logf("删除失败：%v", err)
		return *out
	}
	report("完成：网站已删除。备份文件还在服务器上，需要时可以在 1Panel 新建同名网站后从备份恢复")
	out.Status, out.Undo = StatusDone, map[string]string{}
	return *out
}

func init() {
	site := Param{Name: "website", Kind: "host", Required: true, Desc: "1Panel 网站的主域名（panel_websites 返回的域名）"}
	panel := func(op, downtime, undo string) map[string]Impl {
		return map[string]Impl{"1panel": {Via: "1Panel 接口", Panel: op, Downtime: downtime, Undo: undo}}
	}
	register(&Capability{
		Name: "site.status", Title: "启动或停止网站", Risk: core.R2, Reversible: true,
		Params: []Param{site, {Name: "action", Kind: "enum", Enum: []string{"start", "stop"}, Required: true, Desc: "start 启动；stop 停止（访客会看到「网站已停止」页面，文件和配置都保留）"}},
		Impls:  panel("site_status", "停止期间网站打不开；1Panel 重新加载 OpenResty，不影响其他网站", "恢复成原来的运行或停止状态"),
	})
	register(&Capability{
		Name: "site.domain.add", Title: "给网站添加域名", Risk: core.R1, Reversible: true,
		Params: []Param{site,
			{Name: "domain", Kind: "host", Required: true, Desc: "要添加的域名，例如 www.example.com"},
			{Name: "port", Kind: "int", Min: 1, Max: 65535, Default: "80", Desc: "监听端口，默认 80"}},
		Impls: panel("site_domain_add", "不影响现有访问；1Panel 重新加载 OpenResty", "删除这个域名"),
	})
	register(&Capability{
		Name: "site.domain.remove", Title: "删除网站的域名", Risk: core.R2, Reversible: true,
		Params: []Param{site,
			{Name: "domain", Kind: "host", Required: true, Desc: "要删除的域名（不能是主域名）"},
			{Name: "port", Kind: "int", Min: 1, Max: 65535, Desc: "只删除这个端口上的；不填删除这个域名的所有端口"}},
		Impls: panel("site_domain_remove", "访问这个域名不会再到这个网站；其他域名不受影响", "把删掉的域名和端口加回来"),
	})
	register(&Capability{
		Name: "site.https.set", Title: "设置网站 HTTPS", Risk: core.R2, Reversible: true,
		Params: []Param{site,
			{Name: "enabled", Kind: "enum", Enum: []string{"on", "off"}, Required: true, Desc: "on 开启或修改 HTTPS；off 关闭"},
			{Name: "cert", Kind: "certhost", Desc: "用 1Panel 里哪张证书（证书的主域名）；不填自动选一张包含网站域名、最晚到期的证书。没有证书时用 cert.issue 申请"},
			{Name: "http_mode", Kind: "enum", Enum: []string{"HTTPAlso", "HTTPToHTTPS", "HTTPSOnly"},
				Desc: "HTTPAlso：HTTP 和 HTTPS 都能访问（EdgeOne 用 HTTP 回源时必须选这个）；HTTPToHTTPS：HTTP 跳转到 HTTPS；HTTPSOnly：只能 HTTPS。不填保持原样，第一次开启时是 HTTPAlso"},
			{Name: "hsts", Kind: "enum", Enum: []string{"on", "off"}, Desc: "HSTS：浏览器记住以后只用 HTTPS 访问（开启后即使撤销，访问过的浏览器也会记住一段时间）；不填保持原样"},
			{Name: "http3", Kind: "enum", Enum: []string{"on", "off"}, Desc: "HTTP/3（需要放行 UDP 443）；不填保持原样"}},
		Impls: panel("site_https", "不中断访问；1Panel 重新加载 OpenResty", "恢复网站原来的 HTTPS 设置"),
		Check: func(v map[string]string) error {
			if v["enabled"] == "off" && (v["cert"] != "" || v["http_mode"] != "" || v["hsts"] != "" || v["http3"] != "") {
				return fmt.Errorf("关闭 HTTPS 时不用填 cert、http_mode、hsts 和 http3")
			}
			return nil
		},
	})
	register(&Capability{
		Name: "site.proxy.set", Title: "设置网站反向代理", Risk: core.R2, Reversible: true,
		Params: []Param{site,
			{Name: "name", Kind: "text", Required: true, Desc: "规则名称（字母、数字、_、-）；已有同名规则时修改它，没有就新建。反向代理网站的主规则叫 root"},
			{Name: "path", Kind: "path", Default: "/", Desc: "代理哪个路径，例如 /api；/ 表示整个网站"},
			{Name: "target", Kind: "text", Required: true, Desc: "后端地址，例如 http://127.0.0.1:8080"},
			{Name: "host", Kind: "text", Default: "$host", Desc: "发给后端的 Host：$host 沿用访客访问的域名（默认）；$proxy_host 用后端地址里的域名（代理到别人的网站时用）"}},
		Impls: panel("site_proxy_set", "改动的路径会转到新的后端；1Panel 检查配置后重新加载 OpenResty，不中断其他访问", "新建的规则删除；修改的规则恢复原来的内容"),
		Check: checkProxyParams,
	})
	register(&Capability{
		Name: "site.proxy.remove", Title: "删除网站反向代理", Risk: core.R2, Reversible: true,
		Params: []Param{site, {Name: "name", Kind: "text", Required: true, Desc: "规则名称"}},
		Impls:  panel("site_proxy_remove", "这个路径不再转发到后端", "按原来的内容把规则加回来"),
		Check:  checkProxyParams,
	})
	register(&Capability{
		Name: "site.proxy.status", Title: "启用或停用反向代理规则", Risk: core.R2, Reversible: true,
		Params: []Param{site,
			{Name: "name", Kind: "text", Required: true, Desc: "规则名称"},
			{Name: "enabled", Kind: "enum", Enum: []string{"on", "off"}, Required: true, Desc: "on 启用；off 停用（规则保留，可以再启用）"}},
		Impls: panel("site_proxy_status", "停用后这个路径不再转发到后端", "恢复成原来的启用或停用状态"),
		Check: checkProxyParams,
	})
	register(&Capability{
		Name: "site.conf.set", Title: "修改网站 Nginx 配置文件", Risk: core.R2, Reversible: true,
		Params: []Param{site,
			{Name: "content", Kind: "conf", Required: true, Desc: "完整的新配置文件内容（先用 panel_website 读出现在的内容，在它的基础上修改）"},
			{Name: "base_hash", Kind: "name", Hidden: true}},
		Impls: panel("site_conf", "1Panel 先用 nginx -t 检查，通过后重新加载 OpenResty，不中断访问；检查不通过会自动恢复原文件", "把配置文件恢复成修改前的内容"),
	})
	register(&Capability{
		Name: "site.rewrite.set", Title: "设置网站伪静态规则", Risk: core.R2, Reversible: true,
		Params: []Param{site,
			{Name: "content", Kind: "conf", Desc: "完整的伪静态规则（Nginx 的 location、rewrite 等）；留空表示清空"},
			{Name: "template", Kind: "name", Desc: "规则来自 1Panel 的哪个模板，例如 wordpress、thinkphp、laravel5；手写的不用填"},
			{Name: "base_hash", Kind: "name", Hidden: true}},
		Impls: panel("site_rewrite", "1Panel 检查通过后重新加载 OpenResty，不中断访问；检查不通过会自动恢复原来的规则", "恢复原来的伪静态规则"),
	})
	register(&Capability{
		Name: "site.delete", Title: "删除网站", Risk: core.R3,
		NoUndo: "删除前会先在 1Panel 里把网站目录和配置打包备份（在服务器的 1Panel 备份目录里），需要时可以新建同名网站后从备份恢复；网站用的应用和数据库不会删除",
		Params: []Param{site},
		Impls:  panel("site_delete", "网站立即打不开，目录和配置被删除（先备份）", ""),
	})
}
