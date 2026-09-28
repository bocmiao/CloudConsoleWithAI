package actions

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/btpanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/btpanel/btpaneltest"
)

func btEnv(t *testing.T) (*Env, *btpaneltest.Fake) {
	t.Helper()
	f := btpaneltest.New()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &Env{BT: btpanel.New(btpaneltest.Transport(srv.URL), 8888, f.Key, "http"), PollInterval: 1}, f
}

func btRun(t *testing.T, env *Env, capability string, params map[string]any, want string) (Resolved, Outcome) {
	t.Helper()
	r, err := Resolve(capability, params, "bt")
	if err != nil {
		t.Fatalf("%s %v: %v", capability, params, err)
	}
	if !strings.HasPrefix(r.Impl.Panel, "bt_") {
		t.Fatalf("%s runs as %q on 宝塔", capability, r.Impl.Panel)
	}
	out := Apply(context.Background(), env, r, nil)
	if out.Status != want {
		t.Fatalf("%s %v: status %s, want %s\n%s", capability, params, out.Status, want, strings.Join(out.Log, "\n"))
	}
	return r, out
}

func btUndo(t *testing.T, env *Env, r Resolved, out Outcome) {
	t.Helper()
	if u := Undo(context.Background(), env, r, out.Undo); u.Status != StatusUndone {
		t.Fatalf("undo %s: %+v", r.Cap.Name, u)
	}
}

func TestBTSiteBasics(t *testing.T) {
	env, f := btEnv(t)
	f.AddSite("blog.example.com")
	ctx := context.Background()
	site := func() btpanel.Site {
		s, err := env.BT.Site(ctx, "blog.example.com")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	r, out := btRun(t, env, "site.status", map[string]any{"website": "blog.example.com", "action": "stop"}, StatusDone)
	if site().Running {
		t.Fatal("still running")
	}
	btUndo(t, env, r, out)
	if !site().Running {
		t.Fatal("undo did not start it")
	}

	r, out = btRun(t, env, "site.domain.add", map[string]any{"website": "blog.example.com", "domain": "www.example.com", "port": 8080}, StatusDone)
	has := func(name string, port int) bool {
		ds, err := env.BT.Domains(ctx, site())
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range ds {
			if d.Name == name && d.Port == port {
				return true
			}
		}
		return false
	}
	if !has("www.example.com", 8080) {
		t.Fatal("domain not added")
	}
	btUndo(t, env, r, out)
	if has("www.example.com", 8080) {
		t.Fatal("undo left the domain")
	}
	btRun(t, env, "site.domain.add", map[string]any{"website": "blog.example.com", "domain": "www.example.com"}, StatusDone)
	r, out = btRun(t, env, "site.domain.remove", map[string]any{"website": "blog.example.com", "domain": "www.example.com"}, StatusDone)
	if has("www.example.com", 80) {
		t.Fatal("domain not removed")
	}
	btUndo(t, env, r, out)
	if !has("www.example.com", 80) {
		t.Fatal("undo did not add it back")
	}

	_, out = btRun(t, env, "site.backup", map[string]any{"website": "blog.example.com"}, StatusDone)
	if out.Result["backup"] == "" {
		t.Errorf("backup result = %v", out.Result)
	}
	btRun(t, env, "site.status", map[string]any{"website": "nothere.example.com", "action": "stop"}, StatusRefused)
	for _, c := range []string{"site.delete", "site.create", "site.restore", "site.backup.schedule"} {
		if _, err := Resolve(c, map[string]any{"website": "blog.example.com", "domain": "x.example.com", "type": "static", "backup": "a.tar.gz"}, "bt"); err == nil {
			t.Errorf("%s runs on 宝塔", c)
		}
	}
}

func TestBTSiteHTTPSAndProxies(t *testing.T) {
	env, f := btEnv(t)
	f.AddSite("blog.example.com")
	ctx := context.Background()

	btRun(t, env, "site.https.set", map[string]any{"website": "blog.example.com", "enabled": "on", "http_mode": "HTTPToHTTPS"}, StatusRefused) // no HTTPS yet
	r, out := btRun(t, env, "cert.issue", map[string]any{"domain": "blog.example.com", "http_mode": "HTTPToHTTPS"}, StatusDone)
	s, _ := env.BT.Site(ctx, "blog.example.com")
	ssl, err := env.BT.SSL(ctx, s)
	if err != nil || !ssl.Enabled || !ssl.ForceHTTPS {
		t.Fatalf("ssl = %+v, %v", ssl, err)
	}
	r2, out2 := btRun(t, env, "site.https.set", map[string]any{"website": "blog.example.com", "enabled": "on", "http_mode": "HTTPAlso"}, StatusDone)
	if ssl, _ = env.BT.SSL(ctx, s); ssl.ForceHTTPS {
		t.Fatal("still forced")
	}
	btUndo(t, env, r2, out2)
	if ssl, _ = env.BT.SSL(ctx, s); !ssl.ForceHTTPS {
		t.Fatal("undo did not force HTTPS again")
	}
	btRun(t, env, "site.https.set", map[string]any{"website": "blog.example.com", "enabled": "on", "hsts": "on"}, StatusRefused)
	btUndo(t, env, r, out) // HTTPS was off before: off again
	if ssl, _ = env.BT.SSL(ctx, s); ssl.Enabled {
		t.Fatal("undo left HTTPS on")
	}

	r, out = btRun(t, env, "site.proxy.set", map[string]any{"website": "blog.example.com", "name": "api", "path": "/api", "target": "http://127.0.0.1:9000"}, StatusDone)
	list, _ := env.BT.Proxies(ctx, s)
	if len(list) != 1 || list[0].Target != "http://127.0.0.1:9000" || !list[0].Enabled {
		t.Fatalf("proxies = %+v", list)
	}
	r2, out2 = btRun(t, env, "site.proxy.status", map[string]any{"website": "blog.example.com", "name": "api", "enabled": "off"}, StatusDone)
	if list, _ = env.BT.Proxies(ctx, s); list[0].Enabled {
		t.Fatal("still on")
	}
	btUndo(t, env, r2, out2)
	r3, out3 := btRun(t, env, "site.proxy.remove", map[string]any{"website": "blog.example.com", "name": "api"}, StatusDone)
	if list, _ = env.BT.Proxies(ctx, s); len(list) != 0 {
		t.Fatal("not removed")
	}
	btUndo(t, env, r3, out3)
	btUndo(t, env, r, out) // removes the rule it created
	if list, _ = env.BT.Proxies(ctx, s); len(list) != 0 {
		t.Fatalf("proxies after undo = %+v", list)
	}
}

func TestBTConfAndRewrite(t *testing.T) {
	env, f := btEnv(t)
	f.AddSite("blog.example.com")
	ctx := context.Background()
	s, _ := env.BT.Site(ctx, "blog.example.com")
	conf, err := env.BT.NginxConf(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	next := strings.Replace(conf.Content, "server\n{", "server\n{\n    client_max_body_size 64m;", 1)
	if next == conf.Content {
		next = conf.Content + "\n# Miao Panel\n"
	}
	r, out := btRun(t, env, "site.conf.set", map[string]any{"website": "blog.example.com", "content": next, "base_hash": ConfHash(conf.Content)}, StatusDone)
	if now, _ := env.BT.NginxConf(ctx, s); normConf(now.Content) != normConf(next) {
		t.Fatal("not written")
	}
	btUndo(t, env, r, out)
	if now, _ := env.BT.NginxConf(ctx, s); normConf(now.Content) != normConf(conf.Content) {
		t.Fatal("undo did not restore it")
	}
	btRun(t, env, "site.conf.set", map[string]any{"website": "blog.example.com", "content": next, "base_hash": "0000000000000000"}, StatusRefused)
	bad := conf.Content + "\n" + btpaneltest.BadDirective + " on;\n"
	btRun(t, env, "site.conf.set", map[string]any{"website": "blog.example.com", "content": bad}, StatusRefused)

	rw := "location / {\n    try_files $uri $uri/ /index.php?$args;\n}\n"
	r, out = btRun(t, env, "site.rewrite.set", map[string]any{"website": "blog.example.com", "content": rw}, StatusDone)
	if now, _ := env.BT.Rewrite(ctx, s); normConf(now.Content) != normConf(rw) {
		t.Fatalf("rewrite = %q", now.Content)
	}
	btUndo(t, env, r, out)
}

func TestBTUndoCorners(t *testing.T) {
	env, f := btEnv(t)
	f.AddSite("blog.example.com")
	ctx := context.Background()
	site := func() btpanel.Site {
		s, err := env.BT.Site(ctx, "blog.example.com")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	proxy := func(name string) *btpanel.Proxy {
		list, err := env.BT.Proxies(ctx, site())
		if err != nil {
			t.Fatal(err)
		}
		for i := range list {
			if list[i].Name == name {
				return &list[i]
			}
		}
		return nil
	}

	// A whole-site proxy turns PHP off; undo turns it back on.
	r, out := btRun(t, env, "site.proxy.set", map[string]any{"website": "blog.example.com", "name": "app", "path": "/", "target": "http://127.0.0.1:3000"}, StatusDone)
	if site().PHPVersion != "静态" {
		t.Fatalf("php = %q", site().PHPVersion)
	}
	btUndo(t, env, r, out)
	if proxy("app") != nil || site().PHPVersion != "8.2" {
		t.Fatalf("after undo: proxy %+v php %q", proxy("app"), site().PHPVersion)
	}

	// A paused proxy stays paused through undoing a removal, and gets its
	// old target back through undoing a change.
	btRun(t, env, "site.proxy.set", map[string]any{"website": "blog.example.com", "name": "api", "path": "/api", "target": "http://127.0.0.1:9000"}, StatusDone)
	btRun(t, env, "site.proxy.status", map[string]any{"website": "blog.example.com", "name": "api", "enabled": "off"}, StatusDone)
	r, out = btRun(t, env, "site.proxy.remove", map[string]any{"website": "blog.example.com", "name": "api"}, StatusDone)
	btUndo(t, env, r, out)
	if p := proxy("api"); p == nil || p.Enabled || p.Target != "http://127.0.0.1:9000" {
		t.Fatalf("after undoing the removal: %+v", p)
	}
	r, out = btRun(t, env, "site.proxy.set", map[string]any{"website": "blog.example.com", "name": "api", "path": "/api", "target": "http://127.0.0.1:7777"}, StatusDone)
	btUndo(t, env, r, out)
	if p := proxy("api"); p == nil || p.Enabled || p.Target != "http://127.0.0.1:9000" {
		t.Fatalf("after undoing the change: %+v", p)
	}

	// The last domain is not removed.
	btRun(t, env, "site.domain.remove", map[string]any{"website": "blog.example.com", "domain": "blog.example.com"}, StatusRefused)

	// Certificates: what 宝塔 cannot do is refused before ordering one.
	for _, p := range []map[string]any{
		{"domain": "blog.example.com", "http_mode": "HTTPSOnly"},
		{"domain": "blog.example.com", "apply": "no"},
	} {
		btRun(t, env, "cert.issue", p, StatusRefused)
	}
	env.BT.KeysHidden = true
	btRun(t, env, "cert.issue", map[string]any{"domain": "blog.example.com"}, StatusRefused)
	env.BT.KeysHidden = false
	btRun(t, env, "cert.issue", map[string]any{"domain": "blog.example.com"}, StatusDone)
	// A second order replaces a certificate undo cannot bring back, and says so.
	r, out = btRun(t, env, "cert.issue", map[string]any{"domain": "blog.example.com"}, StatusDone)
	if u := Undo(ctx, env, r, out.Undo); u.Status != StatusFailed || !strings.Contains(strings.Join(u.Log, ""), "证书") {
		t.Fatalf("undo of a replaced certificate: %+v", u)
	}
}
