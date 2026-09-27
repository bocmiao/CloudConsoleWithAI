package actions

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel/onepaneltest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
)

func siteEnv(t *testing.T, f *onepaneltest.Fake) (*Env, *stubConn) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	conn := &stubConn{run: func(cmd string) sshx.Result { return sshx.Result{Stdout: "200"} }}
	return &Env{SSH: conn, User: "root", PollInterval: 1,
		OnePanel: onepanel.New(func(network, _ string) (net.Conn, error) { return net.Dial(network, addr) }, 1, "k", "", "")}, conn
}

// run resolves and applies a step, then checks its status.
func run(t *testing.T, env *Env, capability string, params map[string]any, want string) (Resolved, Outcome) {
	t.Helper()
	r, err := Resolve(capability, params, "1panel")
	if err != nil {
		t.Fatalf("%s %v: %v", capability, params, err)
	}
	out := Apply(context.Background(), env, r, nil)
	if out.Status != want {
		t.Fatalf("%s %v: status %s, want %s\n%s", capability, params, out.Status, want, strings.Join(out.Log, "\n"))
	}
	return r, out
}

func undo(t *testing.T, env *Env, r Resolved, out Outcome) {
	t.Helper()
	if u := Undo(context.Background(), env, r, out.Undo); u.Status != StatusUndone {
		t.Fatalf("undo %s: %+v", r.Cap.Name, u)
	}
}

func TestSiteStatusAndDomains(t *testing.T) {
	f := onepaneltest.New()
	s := f.AddSite("blog.example.com", "proxy", "http://127.0.0.1:8090")
	env, _ := siteEnv(t, f)

	r, out := run(t, env, "site.status", map[string]any{"website": "blog.example.com", "action": "stop"}, StatusDone)
	if s.Running {
		t.Fatal("site still running")
	}
	undo(t, env, r, out)
	if !s.Running {
		t.Fatal("undo did not start the site again")
	}
	if _, again := run(t, env, "site.status", map[string]any{"website": "blog.example.com", "action": "start"}, StatusDone); len(again.Undo) != 0 {
		t.Fatalf("starting a running site should change nothing: %+v", again)
	}
	run(t, env, "site.status", map[string]any{"website": "nope.example.com", "action": "stop"}, StatusRefused)

	r, out = run(t, env, "site.domain.add", map[string]any{"website": "blog.example.com", "domain": "www.example.com"}, StatusDone)
	if len(s.Domains) != 2 || s.Domains[1].Domain != "www.example.com" || s.Domains[1].Port != 80 {
		t.Fatalf("domains %+v", s.Domains)
	}
	undo(t, env, r, out)
	if len(s.Domains) != 1 {
		t.Fatalf("undo left %+v", s.Domains)
	}

	run(t, env, "site.domain.add", map[string]any{"website": "blog.example.com", "domain": "www.example.com", "port": 8080}, StatusDone)
	run(t, env, "site.domain.remove", map[string]any{"website": "blog.example.com", "domain": "blog.example.com"}, StatusRefused)
	r, out = run(t, env, "site.domain.remove", map[string]any{"website": "blog.example.com", "domain": "www.example.com"}, StatusDone)
	if len(s.Domains) != 1 {
		t.Fatalf("remove left %+v", s.Domains)
	}
	undo(t, env, r, out)
	if len(s.Domains) != 2 || s.Domains[1].Port != 8080 {
		t.Fatalf("undo should put the domain back on its port: %+v", s.Domains)
	}
}

func TestSiteHTTPS(t *testing.T) {
	f := onepaneltest.New()
	f.AddSite("blog.example.com", "static", "")
	env, conn := siteEnv(t, f)

	out := func() Outcome {
		_, o := run(t, env, "site.https.set", map[string]any{"website": "blog.example.com", "enabled": "on"}, StatusRefused)
		return o
	}()
	if !strings.Contains(strings.Join(out.Log, ""), "没有能用于") {
		t.Fatalf("no certificate: %v", out.Log)
	}
	f.AddSSL("other.example.com", nil, 60)
	wild := f.AddSSL("*.example.com", []string{"example.com"}, 40)
	longer := f.AddSSL("blog.example.com", nil, 80)

	r, o := run(t, env, "site.https.set", map[string]any{"website": "blog.example.com", "enabled": "on", "http_mode": "HTTPToHTTPS"}, StatusDone)
	s := f.Site("blog.example.com")
	if s.HTTPS["websiteSSLId"] != float64(longer.ID) || s.HTTPS["httpConfig"] != "HTTPToHTTPS" {
		t.Fatalf("should pick the covering certificate that lasts longest: %v", s.HTTPS)
	}
	if !strings.Contains(strings.Join(conn.cmds, "\n"), "https://blog.example.com:443/") {
		t.Fatalf("HTTPS not checked: %v", conn.cmds)
	}
	r2, o2 := run(t, env, "site.https.set", map[string]any{"website": "blog.example.com", "enabled": "on", "cert": "*.example.com", "hsts": "on"}, StatusDone)
	if s.HTTPS["websiteSSLId"] != float64(wild.ID) || s.HTTPS["hsts"] != true || s.HTTPS["httpConfig"] != "HTTPToHTTPS" {
		t.Fatalf("switching certificate should keep the mode: %v", s.HTTPS)
	}
	undo(t, env, r2, o2)
	if s.HTTPS["websiteSSLId"] != float64(longer.ID) || s.HTTPS["hsts"] != false {
		t.Fatalf("undo should restore the certificate: %v", s.HTTPS)
	}
	undo(t, env, r, o)
	if s.HTTPS != nil {
		t.Fatalf("undo should turn HTTPS off again: %v", s.HTTPS)
	}
	run(t, env, "site.https.set", map[string]any{"website": "blog.example.com", "enabled": "off"}, StatusDone)
	if _, err := Resolve("site.https.set", map[string]any{"website": "blog.example.com", "enabled": "off", "hsts": "on"}, "1panel"); err == nil {
		t.Fatal("turning HTTPS off takes no other settings")
	}
	if _, err := Resolve("site.https.set", map[string]any{"website": "blog.example.com", "enabled": "on"}, "bt"); err == nil {
		t.Fatal("宝塔 is not supported")
	}
}

func TestSiteProxies(t *testing.T) {
	f := onepaneltest.New()
	s := f.AddSite("blog.example.com", "proxy", "http://127.0.0.1:8090")
	env, _ := siteEnv(t, f)
	root := s.Proxies["root"].Content

	r, o := run(t, env, "site.proxy.set", map[string]any{"website": "blog.example.com", "name": "api", "path": "/api", "target": "http://127.0.0.1:9000"}, StatusDone)
	if px := s.Proxies["api"]; px == nil || !strings.Contains(px.Content, "proxy_pass http://127.0.0.1:9000;") || !strings.Contains(px.Content, "Host $host;") {
		t.Fatalf("new rule: %+v", s.Proxies)
	}
	run(t, env, "site.proxy.set", map[string]any{"website": "blog.example.com", "name": "api2", "path": "/api", "target": "http://127.0.0.1:9001"}, StatusRefused)
	undo(t, env, r, o)
	if _, ok := s.Proxies["api"]; ok {
		t.Fatal("undo should delete the new rule")
	}

	r, o = run(t, env, "site.proxy.set", map[string]any{"website": "blog.example.com", "name": "root", "target": "https://upstream.example.net", "host": "$proxy_host"}, StatusDone)
	if !strings.Contains(s.Proxies["root"].Content, "https://upstream.example.net") || !strings.Contains(s.Proxies["root"].Content, "$proxy_host") {
		t.Fatalf("edit: %s", s.Proxies["root"].Content)
	}
	undo(t, env, r, o)
	if s.Proxies["root"].Content != root {
		t.Fatalf("undo should restore the file exactly:\n%s", s.Proxies["root"].Content)
	}

	r, o = run(t, env, "site.proxy.status", map[string]any{"website": "blog.example.com", "name": "root", "enabled": "off"}, StatusDone)
	if s.Proxies["root"].Enabled {
		t.Fatal("rule still on")
	}
	run(t, env, "site.proxy.set", map[string]any{"website": "blog.example.com", "name": "root", "target": "http://127.0.0.1:1"}, StatusRefused)
	undo(t, env, r, o)

	r, o = run(t, env, "site.proxy.remove", map[string]any{"website": "blog.example.com", "name": "root"}, StatusDone)
	if _, ok := s.Proxies["root"]; ok || !strings.Contains(strings.Join(o.Log, ""), "主规则") {
		t.Fatalf("remove: %+v %v", s.Proxies, o.Log)
	}
	undo(t, env, r, o)
	if px := s.Proxies["root"]; px == nil || px.Content != root || !px.Enabled {
		t.Fatalf("undo should bring the rule back as it was: %+v", px)
	}

	for _, p := range []map[string]any{
		{"website": "blog.example.com", "name": "a.b", "target": "http://127.0.0.1:1"},
		{"website": "blog.example.com", "name": "api", "target": "http://127.0.0.1:1; include /etc/passwd"},
		{"website": "blog.example.com", "name": "api", "target": "ftp://x"},
		{"website": "blog.example.com", "name": "api", "target": "http://127.0.0.1:1", "host": "a b"},
		{"website": "blog.example.com", "name": "api", "path": "/a;b", "target": "http://127.0.0.1:1"},
	} {
		if _, err := Resolve("site.proxy.set", p, "1panel"); err == nil {
			t.Errorf("accepted %v", p)
		}
	}
}

func TestSiteConfAndRewrite(t *testing.T) {
	f := onepaneltest.New()
	s := f.AddSite("blog.example.com", "static", "")
	env, _ := siteEnv(t, f)
	orig := s.Conf
	next := strings.Replace(orig, "listen 80;", "listen 80;\n    client_max_body_size 50m;", 1)

	run(t, env, "site.conf.set", map[string]any{"website": "blog.example.com", "content": next, "base_hash": "0123456789abcdef"}, StatusRefused)
	r, o := run(t, env, "site.conf.set", map[string]any{"website": "blog.example.com", "content": next, "base_hash": ConfHash(orig)}, StatusDone)
	if s.Conf != next {
		t.Fatalf("conf:\n%s", s.Conf)
	}
	undo(t, env, r, o)
	if s.Conf != orig {
		t.Fatalf("undo:\n%s", s.Conf)
	}
	_, bad := run(t, env, "site.conf.set", map[string]any{"website": "blog.example.com", "content": orig + onepaneltest.BadDirective + " on;\n"}, StatusRefused)
	if s.Conf != orig || !strings.Contains(strings.Join(bad.Log, ""), "没有通过检查") {
		t.Fatalf("a config failing nginx -t must not stay: %v", bad.Log)
	}
	if _, same := run(t, env, "site.conf.set", map[string]any{"website": "blog.example.com", "content": orig}, StatusDone); len(same.Undo) != 0 {
		t.Fatal("writing the same text should change nothing")
	}

	rules := "location / {\n    try_files $uri $uri/ /index.php?$args;\n}"
	r, o = run(t, env, "site.rewrite.set", map[string]any{"website": "blog.example.com", "content": rules, "template": "wordpress"}, StatusDone)
	if s.Rewrite != rules+"\n" || s.RwName != "wordpress" {
		t.Fatalf("rewrite %q %s", s.Rewrite, s.RwName)
	}
	undo(t, env, r, o)
	if s.Rewrite != "" || s.RwName != "default" {
		t.Fatalf("undo rewrite %q %s", s.Rewrite, s.RwName)
	}
	if _, err := Resolve("site.conf.set", map[string]any{"website": "blog.example.com", "content": "a\x00b"}, "1panel"); err == nil {
		t.Fatal("control characters accepted")
	}
}

func TestSiteDelete(t *testing.T) {
	f := onepaneltest.New()
	s := f.AddSite("blog.example.com", "static", "")
	env, _ := siteEnv(t, f)
	r, o := run(t, env, "site.delete", map[string]any{"website": "blog.example.com"}, StatusDone)
	if !s.Deleted || s.Backups != 1 || o.Result["backup"] == "" {
		t.Fatalf("delete: %+v %+v", s, o)
	}
	if r.Cap.Reversible {
		t.Fatal("deleting a site cannot be undone")
	}
	run(t, env, "site.delete", map[string]any{"website": "blog.example.com"}, StatusRefused)
}
