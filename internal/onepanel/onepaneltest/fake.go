// Package onepaneltest is a stand-in for 1Panel's website API. It keeps
// sites, their domains, HTTPS, reverse proxy rules, rewrite rules and
// config files, checks requests the way 1Panel validates them, and, like
// 1Panel, puts a config back when "nginx -t" would fail.
package onepaneltest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BadDirective makes the fake's nginx -t fail when a config contains it.
const BadDirective = "not_a_directive"

// Proxy is a reverse proxy rule as the fake keeps it: its file's text.
type Proxy struct {
	Content string
	Enabled bool
}

// Site is one website.
type Site struct {
	ID       int
	Domain   string
	Alias    string
	Type     string
	Running  bool
	Proxy    string
	Domains  []Domain
	HTTPS    map[string]any // the last HTTPS request, when enabled
	Proxies  map[string]*Proxy
	Rewrite  string
	RwName   string
	Conf     string
	Created  time.Time
	Backups  int
	Deleted  bool
	sslID    int
	protocol string
}

// Domain is one of a site's domains.
type Domain struct {
	ID     int
	Domain string
	Port   int
	SSL    bool
}

// SSL is a certificate.
type SSL struct {
	ID      int
	Domain  string
	Others  []string
	Expires time.Time
	Status  string
}

// Fake is the 1Panel.
type Fake struct {
	mu       sync.Mutex
	Sites    []*Site
	SSLs     []*SSL
	Requests []string // "METHOD path" of every request
	nextID   int
	backups  []map[string]any
	// HTTPPort and HTTPSPort are OpenResty's.
	HTTPPort, HTTPSPort int
}

// New makes a 1Panel with OpenResty running and no sites.
func New() *Fake { return &Fake{nextID: 100, HTTPPort: 80, HTTPSPort: 443} }

func (f *Fake) id() int { f.nextID++; return f.nextID }

// AddSite adds a site answering to domain; a proxy site gets its root rule.
func (f *Fake) AddSite(domain, typ, backend string) *Site {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &Site{ID: f.id(), Domain: domain, Alias: domain, Type: typ, Running: true, Proxy: backend, Proxies: map[string]*Proxy{},
		RwName: "default", Created: time.Now(), protocol: "HTTP"}
	s.Domains = []Domain{{ID: f.id(), Domain: domain, Port: 80}}
	s.Conf = fmt.Sprintf("server {\n    listen 80;\n    server_name %s;\n    include /www/sites/%s/proxy/*.conf;\n    access_log /www/sites/%s/log/access.log main;\n}\n", domain, domain, domain)
	if typ == "proxy" {
		s.Proxies["root"] = &Proxy{Content: proxyText("^~", "/", backend, "$host"), Enabled: true}
	}
	f.Sites = append(f.Sites, s)
	return s
}

// AddSSL adds a ready certificate lasting days.
func (f *Fake) AddSSL(domain string, others []string, days int) *SSL {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := &SSL{ID: f.id(), Domain: domain, Others: others, Expires: time.Now().Add(time.Duration(days) * 24 * time.Hour), Status: "ready"}
	f.SSLs = append(f.SSLs, c)
	return c
}

// Site returns the site with a primary domain.
func (f *Fake) Site(domain string) *Site {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.Sites {
		if s.Domain == domain && !s.Deleted {
			return s
		}
	}
	return nil
}

// Lock and Unlock guard the fake's state for tests that read it.
func (f *Fake) Lock()   { f.mu.Lock() }
func (f *Fake) Unlock() { f.mu.Unlock() }

func proxyText(mod, match, pass, host string) string {
	return fmt.Sprintf("location %s %s {\n    proxy_pass %s;\n    proxy_set_header Host %s;\n    proxy_set_header X-Real-IP $remote_addr;\n}\n", mod, match, pass, host)
}

var (
	locRe  = regexp.MustCompile(`location\s+(\S+)\s+(\S+)\s*\{`)
	passRe = regexp.MustCompile(`proxy_pass\s+([^;]+);`)
	hostRe = regexp.MustCompile(`proxy_set_header\s+Host\s+([^;]+);`)
)

func (f *Fake) find(id int) *Site {
	for _, s := range f.Sites {
		if s.ID == id && !s.Deleted {
			return s
		}
	}
	return nil
}

func num(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

func str(v any) string { s, _ := v.(string); return s }

func (f *Fake) sslByID(id int) *SSL {
	for _, c := range f.SSLs {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (c *SSL) json() map[string]any {
	return map[string]any{"id": c.ID, "primaryDomain": c.Domain, "domains": strings.Join(c.Others, ","), "status": c.Status,
		"expireDate": c.Expires.Format(time.RFC3339), "provider": "http", "autoRenew": true}
}

func (s *Site) summary(f *Fake) map[string]any {
	m := map[string]any{"id": s.ID, "primaryDomain": s.Domain, "alias": s.Alias, "type": s.Type, "status": map[bool]string{true: "Running", false: "Stopped"}[s.Running],
		"protocol": s.protocol, "sitePath": "/opt/1panel/www/sites/" + s.Alias, "createdAt": s.Created.Format(time.RFC3339), "remark": ""}
	if c := f.sslByID(s.sslID); c != nil && s.protocol == "HTTPS" {
		m["sslExpireDate"] = c.Expires.Format(time.RFC3339)
		m["sslStatus"] = "success"
	}
	return m
}

// nginxT is the fake's config test.
func nginxT(texts ...string) error {
	for _, t := range texts {
		if strings.Contains(t, BadDirective) {
			return fmt.Errorf("nginx: [emerg] unknown directive \"%s\"", BadDirective)
		}
	}
	return nil
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, r.Method+" "+r.URL.Path)
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	reply := func(v any) {
		data, _ := json.Marshal(map[string]any{"code": 200, "message": "", "data": v})
		_, _ = w.Write(data)
	}
	bad := func(format string, args ...any) {
		w.WriteHeader(http.StatusBadRequest)
		data, _ := json.Marshal(map[string]any{"code": 400, "message": fmt.Sprintf(format, args...)})
		_, _ = w.Write(data)
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v2")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	site := func(id int) *Site {
		s := f.find(id)
		if s == nil {
			bad("ErrRecordNotFound")
		}
		return s
	}
	switch {
	case p == "/apps/installed/search":
		reply(map[string]any{"total": 1, "items": []map[string]any{{"id": 2, "name": "openresty", "appKey": "openresty", "status": "Running",
			"version": "1.27.1.2", "httpPort": f.HTTPPort, "httpsPort": f.HTTPSPort}}})
	case p == "/websites/list":
		var out []map[string]any
		for _, s := range f.Sites {
			if !s.Deleted {
				out = append(out, map[string]any{"id": s.ID, "primaryDomain": s.Domain, "alias": s.Alias, "type": s.Type,
					"status": map[bool]string{true: "Running", false: "Stopped"}[s.Running], "proxy": s.Proxy, "sitePath": "/opt/1panel/www/sites/" + s.Alias})
			}
		}
		reply(out)
	case p == "/websites/search":
		if body["orderBy"] == nil || body["order"] == nil {
			bad("Key: 'WebsiteSearch.OrderBy' Error:Field validation for 'OrderBy' failed on the 'required' tag")
			return
		}
		var out []map[string]any
		for _, s := range f.Sites {
			if !s.Deleted {
				out = append(out, s.summary(f))
			}
		}
		reply(map[string]any{"total": len(out), "items": out})
	case p == "/websites/operate":
		s := site(num(body["id"]))
		if s == nil {
			return
		}
		switch body["operate"] {
		case "start":
			s.Running = true
		case "stop":
			s.Running = false
		default:
			bad("bad operate")
			return
		}
		reply(nil)
	case len(parts) == 3 && parts[0] == "websites" && parts[1] == "domains" && r.Method == http.MethodGet:
		s := site(num(parts[2]))
		if s == nil {
			return
		}
		var out []map[string]any
		for _, d := range s.Domains {
			out = append(out, map[string]any{"id": d.ID, "websiteId": s.ID, "domain": d.Domain, "port": d.Port, "ssl": d.SSL})
		}
		reply(out)
	case p == "/websites/domains":
		s := site(num(body["websiteID"]))
		if s == nil {
			return
		}
		list, _ := body["domains"].([]any)
		if len(list) == 0 {
			bad("Key: 'WebsiteDomainCreate.Domains' Error:Field validation for 'Domains' failed on the 'required' tag")
			return
		}
		for _, x := range list {
			d, _ := x.(map[string]any)
			for _, o := range f.Sites {
				for _, od := range o.Domains {
					if !o.Deleted && od.Domain == str(d["domain"]) && od.Port == num(d["port"]) {
						bad("ErrDomainIsExist")
						return
					}
				}
			}
			s.Domains = append(s.Domains, Domain{ID: f.id(), Domain: str(d["domain"]), Port: num(d["port"]), SSL: d["ssl"] == true})
		}
		reply(nil)
	case p == "/websites/domains/del":
		id := num(body["id"])
		for _, s := range f.Sites {
			for i, d := range s.Domains {
				if d.ID == id {
					if len(s.Domains) == 1 {
						bad("ErrDomainOnly")
						return
					}
					s.Domains = append(s.Domains[:i:i], s.Domains[i+1:]...)
					reply(nil)
					return
				}
			}
		}
		bad("ErrRecordNotFound")
	case len(parts) == 3 && parts[0] == "websites" && parts[2] == "https":
		s := site(num(parts[1]))
		if s == nil {
			return
		}
		if r.Method == http.MethodGet {
			out := map[string]any{"enable": s.protocol == "HTTPS", "httpsPort": "443"}
			if s.HTTPS != nil && s.protocol == "HTTPS" {
				out["httpConfig"], out["hsts"], out["http3"] = s.HTTPS["httpConfig"], s.HTTPS["hsts"], s.HTTPS["http3"]
				out["SSLProtocol"], out["algorithm"] = s.HTTPS["SSLProtocol"], s.HTTPS["algorithm"]
				if c := f.sslByID(s.sslID); c != nil {
					out["SSL"] = c.json()
				}
			}
			reply(out)
			return
		}
		mode := str(body["httpConfig"])
		if mode != "HTTPSOnly" && mode != "HTTPAlso" && mode != "HTTPToHTTPS" {
			bad("Key: 'WebsiteHTTPSOp.HttpConfig' Error:Field validation for 'HttpConfig' failed on the 'oneof' tag")
			return
		}
		if t := str(body["type"]); t != "existed" && t != "auto" && t != "manual" {
			bad("Key: 'WebsiteHTTPSOp.Type' Error:Field validation for 'Type' failed on the 'oneof' tag")
			return
		}
		if body["enable"] != true {
			s.protocol, s.sslID, s.HTTPS = "HTTP", 0, nil
			reply(nil)
			return
		}
		c := f.sslByID(num(body["websiteSSLId"]))
		if c == nil || c.Status != "ready" {
			bad("ErrSSLValid")
			return
		}
		s.protocol, s.sslID, s.HTTPS = "HTTPS", c.ID, body
		reply(nil)
	case p == "/websites/ssl/search":
		var out []map[string]any
		for _, c := range f.SSLs {
			out = append(out, c.json())
		}
		reply(map[string]any{"total": len(out), "items": out})
	case len(parts) == 3 && parts[0] == "websites" && parts[1] == "ssl" && r.Method == http.MethodGet:
		c := f.sslByID(num(parts[2]))
		if c == nil {
			bad("ErrRecordNotFound")
			return
		}
		reply(c.json())
	case p == "/websites/proxies":
		s := site(num(body["id"]))
		if s == nil {
			return
		}
		out := []map[string]any{}
		names := make([]string, 0, len(s.Proxies))
		for n := range s.Proxies {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			px := s.Proxies[n]
			m := map[string]any{"id": s.ID, "name": n, "enable": px.Enabled, "content": px.Content, "filePath": "/opt/1panel/www/sites/" + s.Alias + "/proxy/" + n + ".conf"}
			if l := locRe.FindStringSubmatch(px.Content); l != nil {
				m["modifier"], m["match"] = l[1], l[2]
			}
			if l := passRe.FindStringSubmatch(px.Content); l != nil {
				m["proxyPass"] = l[1]
			}
			if l := hostRe.FindStringSubmatch(px.Content); l != nil {
				m["proxyHost"] = l[1]
			}
			out = append(out, m)
		}
		reply(out)
	case p == "/websites/proxies/update":
		s := site(num(body["id"]))
		if s == nil {
			return
		}
		for _, k := range []string{"operate", "name", "match", "proxyPass", "proxyHost"} {
			if str(body[k]) == "" {
				bad("Key: 'WebsiteProxyConfig.%s' Error:Field validation for '%s' failed on the 'required' tag", k, k)
				return
			}
		}
		name := str(body["name"])
		text := proxyText(str(body["modifier"]), str(body["match"]), str(body["proxyPass"]), str(body["proxyHost"]))
		switch body["operate"] {
		case "create":
			if _, ok := s.Proxies[name]; ok {
				bad("ErrNameIsExist")
				return
			}
			s.Proxies[name] = &Proxy{Content: text, Enabled: true}
		case "edit":
			px, ok := s.Proxies[name]
			if !ok || !px.Enabled {
				bad("open %s.conf: no such file or directory", name)
				return
			}
			px.Content = text
		default:
			bad("unknown operate")
			return
		}
		reply(nil)
	case p == "/websites/proxies/delete":
		s := site(num(body["id"]))
		if s == nil {
			return
		}
		delete(s.Proxies, str(body["name"]))
		reply(nil)
	case p == "/websites/proxies/status":
		s := site(num(body["id"]))
		if s == nil {
			return
		}
		if px, ok := s.Proxies[str(body["name"])]; ok {
			px.Enabled = body["status"] == "enable"
		}
		reply(nil)
	case p == "/websites/proxies/file":
		s := site(num(body["websiteID"]))
		if s == nil {
			return
		}
		px, ok := s.Proxies[str(body["name"])]
		if !ok || !px.Enabled {
			bad("open %s.conf: no such file or directory", str(body["name"]))
			return
		}
		if err := nginxT(str(body["content"])); err != nil {
			bad("%v", err)
			return
		}
		px.Content = str(body["content"])
		reply(nil)
	case p == "/websites/rewrite":
		s := site(num(body["websiteId"]))
		if s == nil {
			return
		}
		if body["name"] == "current" {
			reply(map[string]any{"content": s.Rewrite})
			return
		}
		reply(map[string]any{"content": "location / {\n    try_files $uri $uri/ /index.php?$args;\n}\n"})
	case p == "/websites/rewrite/update":
		s := site(num(body["websiteId"]))
		if s == nil {
			return
		}
		if str(body["name"]) == "" {
			bad("Key: 'NginxRewriteUpdate.Name' Error:Field validation for 'Name' failed on the 'required' tag")
			return
		}
		if err := nginxT(str(body["content"])); err != nil {
			bad("%v", err)
			return
		}
		s.Rewrite, s.RwName = str(body["content"]), str(body["name"])
		reply(nil)
	case len(parts) == 4 && parts[0] == "websites" && parts[2] == "config" && parts[3] == "openresty":
		s := site(num(parts[1]))
		if s == nil {
			return
		}
		reply(map[string]any{"path": "/opt/1panel/apps/openresty/openresty/conf/conf.d/" + s.Alias + ".conf", "content": s.Conf, "name": s.Alias + ".conf"})
	case p == "/websites/nginx/update":
		s := site(num(body["id"]))
		if s == nil {
			return
		}
		if str(body["content"]) == "" {
			bad("Key: 'WebsiteNginxUpdate.Content' Error:Field validation for 'Content' failed on the 'required' tag")
			return
		}
		if err := nginxT(str(body["content"])); err != nil {
			bad("%v", err) // the old file is kept
			return
		}
		s.Conf = str(body["content"])
		reply(nil)
	case len(parts) == 2 && parts[0] == "websites" && r.Method == http.MethodGet:
		s := site(num(parts[1]))
		if s == nil {
			return
		}
		m := s.summary(f)
		m["proxy"], m["rewrite"], m["accessLog"], m["errorLog"] = s.Proxy, s.RwName, true, true
		m["webSiteSSLId"], m["httpConfig"] = s.sslID, ""
		m["accessLogPath"] = "/opt/1panel/www/sites/" + s.Alias + "/log/access.log"
		m["errorLogPath"] = "/opt/1panel/www/sites/" + s.Alias + "/log/error.log"
		reply(m)
	case p == "/backups/backup":
		if body["type"] != "website" {
			bad("unsupported backup type")
			return
		}
		for _, s := range f.Sites {
			if s.Alias == str(body["detailName"]) && !s.Deleted {
				s.Backups++
				f.backups = append(f.backups, map[string]any{"taskID": body["taskID"], "status": "Success", "fileDir": "website/" + s.Alias,
					"fileName": s.Alias + "_20260927.tar.gz", "name": s.Alias, "detailName": s.Alias})
				reply(nil)
				return
			}
		}
		bad("ErrRecordNotFound")
	case p == "/backups/record/search":
		var out []map[string]any
		for _, b := range f.backups {
			if b["name"] == body["name"] && b["detailName"] == body["detailName"] {
				out = append(out, b)
			}
		}
		reply(map[string]any{"total": len(out), "items": out})
	case p == "/websites/del":
		s := site(num(body["id"]))
		if s == nil {
			return
		}
		if body["deleteApp"] != false || body["deleteDB"] != false || body["deleteBackup"] != false {
			bad("would delete more than the site")
			return
		}
		s.Deleted = true
		reply(nil)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":404,"message":"not found"}`)
	}
}
