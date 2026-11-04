package onepanel

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// The 网站 page of 1Panel: each site's domains, HTTPS, reverse proxies,
// rewrite rules and its OpenResty config file. 1Panel checks every config
// change with nginx -t and puts the old file back when it fails.

// SiteSummary is one row of 1Panel's website list.
type SiteSummary struct {
	ID            uint      `json:"id"`
	PrimaryDomain string    `json:"primaryDomain"`
	Alias         string    `json:"alias"`
	Type          string    `json:"type"`     // static, proxy, deployment, runtime, subsite, stream
	Status        string    `json:"status"`   // Running, Stopped
	Protocol      string    `json:"protocol"` // HTTP, HTTPS
	Remark        string    `json:"remark"`
	SitePath      string    `json:"sitePath"`
	AppName       string    `json:"appName"`
	RuntimeName   string    `json:"runtimeName"`
	RuntimeType   string    `json:"runtimeType"`
	SSLExpireDate time.Time `json:"sslExpireDate"`
	SSLStatus     string    `json:"sslStatus"`
	ParentSite    string    `json:"parentSite"`
	CreatedAt     time.Time `json:"createdAt"`
	IPV6          bool      `json:"IPV6"`
}

// SearchWebsites lists every site with its HTTPS state, newest first.
func (c *Client) SearchWebsites(ctx context.Context) ([]SiteSummary, error) {
	var page struct {
		Items []SiteSummary `json:"items"`
	}
	err := c.do(ctx, http.MethodPost, "/websites/search",
		map[string]any{"page": 1, "pageSize": 500, "name": "", "orderBy": "created_at", "order": "descending"}, &page)
	return page.Items, err
}

// SiteDetail is one site as 1Panel keeps it.
type SiteDetail struct {
	ID            uint   `json:"id"`
	PrimaryDomain string `json:"primaryDomain"`
	Alias         string `json:"alias"`
	Type          string `json:"type"`
	Status        string `json:"status"`
	Protocol      string `json:"protocol"`
	HTTPConfig    string `json:"httpConfig"`
	Remark        string `json:"remark"`
	Proxy         string `json:"proxy"`
	SiteDir       string `json:"siteDir"`
	AccessLog     bool   `json:"accessLog"`
	ErrorLog      bool   `json:"errorLog"`
	DefaultServer bool   `json:"defaultServer"`
	IPV6          bool   `json:"IPV6"`
	Rewrite       string `json:"rewrite"` // the rewrite template in use
	SSLID         uint   `json:"webSiteSSLId"`
	RuntimeID     uint   `json:"runtimeID"`
	AppInstallID  uint   `json:"appInstallId"`
	ParentID      uint   `json:"parentWebsiteID"`
	SitePath      string `json:"sitePath"`
	AccessLogPath string `json:"accessLogPath"`
	ErrorLogPath  string `json:"errorLogPath"`
	RuntimeName   string `json:"runtimeName"`
	RuntimeType   string `json:"runtimeType"`
}

// Website returns one site.
func (c *Client) Website(ctx context.Context, id uint) (SiteDetail, error) {
	var d SiteDetail
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/websites/%d", id), nil, &d)
	return d, err
}

// SetWebsiteRunning starts a stopped site or stops a running one (1Panel
// then serves its "stopped" page instead).
func (c *Client) SetWebsiteRunning(ctx context.Context, id uint, run bool) error {
	op := "stop"
	if run {
		op = "start"
	}
	return c.do(ctx, http.MethodPost, "/websites/operate", map[string]any{"id": id, "operate": op}, nil)
}

// SiteDomain is one domain a site answers to.
type SiteDomain struct {
	ID        uint   `json:"id"`
	WebsiteID uint   `json:"websiteId"`
	Domain    string `json:"domain"`
	Port      int    `json:"port"`
	SSL       bool   `json:"ssl"`
}

// WebsiteDomains lists a site's domains.
func (c *Client) WebsiteDomains(ctx context.Context, id uint) ([]SiteDomain, error) {
	var list []SiteDomain
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/websites/domains/%d", id), nil, &list)
	return list, err
}

// AddWebsiteDomain adds a domain on a port; ssl also listens for HTTPS
// on it when the site has HTTPS on.
func (c *Client) AddWebsiteDomain(ctx context.Context, id uint, domain string, port int, ssl bool) error {
	return c.do(ctx, http.MethodPost, "/websites/domains", map[string]any{
		"websiteID": id, "domains": []map[string]any{{"domain": domain, "port": port, "ssl": ssl}},
	}, nil)
}

// DeleteWebsiteDomain removes one domain (by its own id).
func (c *Client) DeleteWebsiteDomain(ctx context.Context, domainID uint) error {
	return c.do(ctx, http.MethodPost, "/websites/domains/del", map[string]any{"id": domainID}, nil)
}

// SiteConf is a site's OpenResty config file.
type SiteConf struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WebsiteConf reads a site's OpenResty config file.
func (c *Client) WebsiteConf(ctx context.Context, id uint) (SiteConf, error) {
	var f SiteConf
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/websites/%d/config/openresty", id), nil, &f)
	return f, err
}

// UpdateWebsiteConf replaces a site's config file; 1Panel tests it and
// reloads OpenResty, and puts the old file back when the test fails.
func (c *Client) UpdateWebsiteConf(ctx context.Context, id uint, content string) error {
	return c.do(ctx, http.MethodPost, "/websites/nginx/update", map[string]any{"id": id, "content": content}, nil)
}

// Proxy is one reverse proxy rule of a site (a location block kept in
// its own file under the site's proxy folder).
type Proxy struct {
	ID              uint              `json:"id"` // the website
	Operate         string            `json:"operate"`
	Enable          bool              `json:"enable"`
	Cache           bool              `json:"cache"`
	CacheTime       int               `json:"cacheTime"`
	CacheUnit       string            `json:"cacheUnit"`
	ServerCacheTime int               `json:"serverCacheTime"`
	ServerCacheUnit string            `json:"serverCacheUnit"`
	Name            string            `json:"name"`
	Modifier        string            `json:"modifier"` // ^~, =, ~, ~* or empty
	Match           string            `json:"match"`    // the path, such as /api
	ProxyPass       string            `json:"proxyPass"`
	ProxyHost       string            `json:"proxyHost"` // the Host header sent to the backend
	Content         string            `json:"content"`
	FilePath        string            `json:"filePath"`
	Replaces        map[string]string `json:"replaces"`
	SNI             bool              `json:"sni"`
	ProxySSLName    string            `json:"proxySSLName"`
	SSLVerify       bool              `json:"sslVerify"`
	Cors            bool              `json:"cors"`
	AllowOrigins    string            `json:"allowOrigins"`
	AllowMethods    string            `json:"allowMethods"`
	AllowHeaders    string            `json:"allowHeaders"`
	AllowCreds      bool              `json:"allowCredentials"`
	Preflight       bool              `json:"preflight"`
}

// Proxies lists a site's reverse proxy rules with their files' text.
func (c *Client) Proxies(ctx context.Context, id uint) ([]Proxy, error) {
	var list []Proxy
	err := c.do(ctx, http.MethodPost, "/websites/proxies", map[string]any{"id": id}, &list)
	return list, err
}

// SaveProxy creates a rule (operate "create") or rewrites one from its
// settings (operate "edit"); settings not in p are cleared.
func (c *Client) SaveProxy(ctx context.Context, p Proxy, operate string) error {
	p.Operate = operate
	if p.Replaces == nil {
		p.Replaces = map[string]string{}
	}
	return c.do(ctx, http.MethodPost, "/websites/proxies/update", p, nil)
}

// DeleteProxy removes a rule.
func (c *Client) DeleteProxy(ctx context.Context, id uint, name string) error {
	return c.do(ctx, http.MethodPost, "/websites/proxies/delete", map[string]any{"id": id, "name": name}, nil)
}

// SetProxyEnabled switches a rule on or off, keeping its file.
func (c *Client) SetProxyEnabled(ctx context.Context, id uint, name string, on bool) error {
	status := "disable"
	if on {
		status = "enable"
	}
	return c.do(ctx, http.MethodPost, "/websites/proxies/status", map[string]any{"id": id, "name": name, "status": status}, nil)
}

// WriteProxyFile replaces the text of an enabled rule's file.
func (c *Client) WriteProxyFile(ctx context.Context, id uint, name, content string) error {
	return c.do(ctx, http.MethodPost, "/websites/proxies/file", map[string]any{"websiteID": id, "name": name, "content": content}, nil)
}

// Rewrite reads the site's rewrite rules ("current"), or the text of a
// template such as wordpress, thinkphp or laravel5.
func (c *Client) Rewrite(ctx context.Context, id uint, name string) (string, error) {
	var out struct {
		Content string `json:"content"`
	}
	err := c.do(ctx, http.MethodPost, "/websites/rewrite", map[string]any{"websiteId": id, "name": name}, &out)
	return out.Content, err
}

// UpdateRewrite writes the site's rewrite rules; name records which
// template they came from ("custom" when written by hand).
func (c *Client) UpdateRewrite(ctx context.Context, id uint, name, content string) error {
	return c.do(ctx, http.MethodPost, "/websites/rewrite/update", map[string]any{"websiteId": id, "name": name, "content": content}, nil)
}
