package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel/onepaneltest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

// panelServer is a server whose 1Panel is the fake, reached through the
// SSH connection's port forward like a real one.
func panelServer(t *testing.T, a *App) (store.Server, *onepaneltest.Fake) {
	t.Helper()
	f := onepaneltest.New()
	web := httptest.NewServer(f)
	t.Cleanup(web.Close)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	if err := a.Store.SaveProfile(sv.ID, "", "1panel"); err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(web.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	if _, err := a.SaveOnePanel(sv.ID, OnePanelSettings{Port: p}, "key"); err != nil {
		t.Fatal(err)
	}
	sv, _ = a.Store.GetServer(sv.ID)
	return sv, f
}

func TestWebsitesPage(t *testing.T) {
	a := newApp(t)
	sv, f := panelServer(t, a)
	blog := f.AddSite("blog.example.com", "proxy", "http://127.0.0.1:8090")
	f.AddSite("shop.example.com", "static", "")
	f.AddSSL("blog.example.com", []string{"www.example.com"}, 50)
	ctx := context.Background()

	// A second server without 1Panel's API shows up as needing it.
	srv2 := sshtest.Start(t, "root", "pw")
	other := addTestServer(t, a, srv2, "pw")
	_ = a.Store.SaveProfile(other.ID, "", "1panel")

	v, err := a.Websites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Servers) != 2 {
		t.Fatalf("servers %+v", v.Servers)
	}
	var got SiteServerView
	for _, s := range v.Servers {
		if s.ID == sv.ID {
			got = s
		} else if !s.NoPanel {
			t.Fatalf("server without the API: %+v", s)
		}
	}
	if got.Error != "" || len(got.Sites) != 2 || got.OpenResty == nil || !got.OpenResty.Running || got.Sites[0].Domain != "blog.example.com" || !got.Sites[0].Running {
		t.Fatalf("sites %+v", got)
	}

	d, err := a.Website(ctx, sv.ID, uint(blog.ID))
	if err != nil {
		t.Fatal(err)
	}
	if d.Site.Domain != "blog.example.com" || len(d.Domains) != 1 || len(d.Proxies) != 1 || d.Proxies[0].Name != "root" ||
		d.Proxies[0].Target != "http://127.0.0.1:8090" || d.Conf == "" || d.ConfHash == "" || len(d.Certs) != 1 || d.HTTPS.Enable || len(d.Problems) != 0 {
		t.Fatalf("detail %+v", d)
	}

	// HTTPS through a checklist, run and undone.
	on := true
	p, err := a.ProposeWebsite(ctx, SiteRequest{ServerID: sv.ID, Site: "blog.example.com", Op: "https", Enabled: &on, HTTPMode: "HTTPToHTTPS"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.StepList[0].Executable || p.StepList[0].Capability != "site.https.set" || !strings.Contains(p.Reason, "EdgeOne") {
		t.Fatalf("plan %+v", p)
	}
	if _, err := a.ExecutePlan(p.ID, []int{0}); err != nil {
		t.Fatal(err)
	}
	done := waitPlan(t, a, p.ID)
	if done.StepList[0].Status != actions.StatusDone || blog.HTTPS == nil || blog.HTTPS["httpConfig"] != "HTTPToHTTPS" {
		t.Fatalf("run: %+v %v", done.StepList[0], blog.HTTPS)
	}
	_ = a.Store.SaveProfile(sv.ID, "", "1panel") // the run re-discovered this machine
	if _, err := a.UndoStep(ctx, p.ID, 0); err != nil {
		t.Fatal(err)
	}
	if blog.HTTPS != nil {
		t.Fatalf("undo left HTTPS on: %v", blog.HTTPS)
	}
	_ = a.Store.SaveProfile(sv.ID, "", "1panel")

	// A config change shows its diff, and one made against an older file
	// is refused.
	next := strings.Replace(d.Conf, "listen 80;", "listen 80;\n    gzip on;", 1)
	p, err = a.ProposeWebsite(ctx, SiteRequest{ServerID: sv.ID, Site: "blog.example.com", Op: "conf", Content: next, BaseHash: d.ConfHash})
	if err != nil {
		t.Fatal(err)
	}
	st := p.StepList[0]
	if len(st.Diffs) != 1 || !strings.Contains(st.Diffs[0].Diff, "+    gzip on;") || !strings.Contains(st.Summary, "新增 1 行，删除 0 行") || st.Params["base_hash"] != d.ConfHash {
		t.Fatalf("conf step %+v", st)
	}
	if _, err := a.ProposeWebsite(ctx, SiteRequest{ServerID: sv.ID, Site: "blog.example.com", Op: "conf", Content: next, BaseHash: "0000000000000000"}); err == nil {
		t.Fatal("a change against an older file should be refused")
	}
	if _, err := a.ProposeWebsite(ctx, SiteRequest{ServerID: sv.ID, Site: "blog.example.com", Op: "conf", Content: d.Conf, BaseHash: d.ConfHash}); err == nil {
		t.Fatal("no change should be refused")
	}

	// The AI can read a site and propose the same kinds of change; the
	// diff is worked out by Miao Panel, not taken from the AI.
	out, err := a.toolPanelWebsite(ctx, json.RawMessage(`{"server_id":`+strconv.FormatInt(sv.ID, 10)+`,"website":"blog.example.com"}`))
	if err != nil || !strings.Contains(out, "反向代理规则 root") || !strings.Contains(out, "server_name blog.example.com") {
		t.Fatalf("panel_website: %v\n%s", err, out)
	}
	args, _ := json.Marshal(map[string]any{"server_id": sv.ID, "title": "开 gzip", "reason": "省流量", "steps": []map[string]any{{
		"capability": "site.conf.set", "summary": "开 gzip", "params": map[string]any{"website": "blog.example.com", "content": next},
		"diffs": []map[string]any{{"path": "/etc/passwd", "diff": "fake"}},
	}}})
	collector := &planCollector{}
	if _, err := a.toolProposePlan(context.WithValue(ctx, planCollectorKey{}, collector), args); err != nil {
		t.Fatal(err)
	}
	ai, _ := a.Plan(collector.ids[0])
	if len(ai.StepList[0].Diffs) != 1 || ai.StepList[0].Diffs[0].Path == "/etc/passwd" || !ai.StepList[0].Executable {
		t.Fatalf("AI step %+v", ai.StepList[0])
	}

	// Logs come from the server itself.
	if _, err := a.WebsiteLog(ctx, sv.ID, uint(blog.ID), "nope", 50); err == nil {
		t.Fatal("unknown log kind accepted")
	}
	lg, err := a.WebsiteLog(ctx, sv.ID, uint(blog.ID), "access", 50)
	if err != nil || lg.Path != "/opt/1panel/www/sites/blog.example.com/log/access.log" {
		t.Fatalf("log %+v %v", lg, err)
	}

	for _, bad := range []SiteRequest{
		{ServerID: sv.ID, Site: "blog.example.com", Op: "proxy_set", Name: "a b", Target: "http://127.0.0.1:1"},
		{ServerID: sv.ID, Site: "blog.example.com", Op: "domain_add", Domain: "not a domain"},
		{ServerID: sv.ID, Site: "blog.example.com", Op: "explode"},
		{ServerID: sv.ID, Op: "start"},
	} {
		if _, err := a.ProposeWebsite(ctx, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestEOCachePlans(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	if _, err := a.ProposeEOCache(ctx, EOCacheRequest{Domain: "blog.example.com", Op: "purge"}); err == nil {
		t.Fatal("needs Tencent Cloud")
	}
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	a.PollInterval = 5 * time.Millisecond
	f.Domains = append(f.Domains, &tencenttest.Domain{Zone: "zone-abc", Name: "blog.example.com", Status: "online", Cname: "x", Origin: "1.2.3.4"})
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}

	p, err := a.ProposeEOCache(ctx, EOCacheRequest{Domain: "blog.example.com", Op: "purge", Type: "url", Targets: []string{"/css/app.css", "blog.example.com/js/app.js"}})
	if err != nil {
		t.Fatal(err)
	}
	st := p.StepList[0]
	if !st.Executable || st.Params["targets"] != "https://blog.example.com/css/app.css\nhttps://blog.example.com/js/app.js" || st.Reversible {
		t.Fatalf("purge step %+v", st)
	}
	if _, err := a.ExecutePlan(p.ID, []int{0}); err != nil {
		t.Fatal(err)
	}
	if done := waitPlan(t, a, p.ID); done.StepList[0].Status != actions.StatusDone {
		t.Fatalf("purge run %+v", done.StepList[0])
	}
	p, err = a.ProposeEOCache(ctx, EOCacheRequest{Domain: "blog.example.com", Op: "purge", Type: "prefix", Targets: []string{"/static"}})
	if err != nil || p.StepList[0].Params["targets"] != "https://blog.example.com/static/" {
		t.Fatalf("prefix %+v %v", p, err)
	}
	p, err = a.ProposeEOCache(ctx, EOCacheRequest{Domain: "blog.example.com", Op: "prefetch", Targets: []string{"https://blog.example.com/big.zip"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExecutePlan(p.ID, []int{0}); err != nil {
		t.Fatal(err)
	}
	waitPlan(t, a, p.ID)
	if len(f.CacheTasks) != 2 || f.CacheTasks[0]["Type"] != "purge_url" || f.CacheTasks[1]["Targets"] == nil {
		t.Fatalf("tasks %v", f.CacheTasks)
	}
	for _, bad := range []EOCacheRequest{
		{Domain: "blog.example.com", Op: "purge", Type: "url"},
		{Domain: "blog.example.com", Op: "prefetch"},
		{Domain: "blog.example.com", Op: "purge", Type: "everything"},
		{Domain: "blog.example.com", Op: "purge", Type: "url", Targets: []string{"ftp://x/y"}},
		{Op: "purge", Type: "all"},
	} {
		if _, err := a.ProposeEOCache(ctx, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if eo := a.siteEdgeOne(ctx, []string{"blog.example.com", "www.example.com"}); eo == nil || eo.Zone != "example.com" || len(eo.Domains) != 1 {
		t.Fatalf("site on EdgeOne %+v", eo)
	}
	if eo := a.siteEdgeOne(ctx, []string{"other.org"}); eo != nil {
		t.Fatalf("not on EdgeOne %+v", eo)
	}
}
