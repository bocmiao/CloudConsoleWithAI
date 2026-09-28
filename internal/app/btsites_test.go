package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/btpanel/btpaneltest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// btServer is a server whose 宝塔 is the fake, reached through the SSH
// connection's port forward like a real one.
func btServer(t *testing.T, a *App) (store.Server, *btpaneltest.Fake) {
	t.Helper()
	f := btpaneltest.New()
	web := httptest.NewServer(f)
	t.Cleanup(web.Close)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	if err := a.Store.SaveProfile(sv.ID, "", "bt"); err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(web.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	if _, err := a.SaveBT(sv.ID, BTSettings{Port: p}, f.Key); err != nil {
		t.Fatal(err)
	}
	sv, _ = a.Store.GetServer(sv.ID)
	return sv, f
}

func TestBTWebsites(t *testing.T) {
	a := newApp(t)
	sv, f := btServer(t, a)
	f.AddSite("blog.example.com")
	ctx := context.Background()

	if info, err := a.TestBT(ctx, sv.ID); err != nil || !strings.Contains(info, "宝塔 11.8.0") {
		t.Fatalf("test = %q, %v", info, err)
	}
	v, err := a.Websites(ctx, 0)
	if err != nil || len(v.Servers) != 1 {
		t.Fatalf("websites = %+v, %v", v, err)
	}
	got := v.Servers[0]
	if got.Panel != "bt" || got.Error != "" || got.NoPanel || len(got.Sites) != 1 || got.Sites[0].Domain != "blog.example.com" || got.Sites[0].Type != "php" || !got.Sites[0].Running {
		t.Fatalf("server = %+v", got)
	}
	id := got.Sites[0].ID
	d, err := a.Website(ctx, sv.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Panel != "bt" || d.Site.Domain != "blog.example.com" || len(d.Domains) != 1 || d.Conf == "" || d.ConfHash == "" || d.HTTPS.Enable || len(d.Problems) != 0 {
		t.Fatalf("detail = %+v", d)
	}

	run := func(req SiteRequest) PlanView {
		t.Helper()
		req.ServerID, req.Site = sv.ID, "blog.example.com"
		p, err := a.ProposeWebsite(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		if _, err := a.ExecutePlan(p.ID, []int{0}); err != nil {
			t.Fatal(err)
		}
		done := waitPlan(t, a, p.ID)
		if done.StepList[0].Status != actions.StatusDone {
			t.Fatalf("%s: %+v", req.Op, done.StepList[0])
		}
		_ = a.Store.SaveProfile(sv.ID, "", "bt") // the test server is no real 宝塔
		return done
	}
	p := run(SiteRequest{Op: "domain_add", Domain: "www.example.com", Port: 80})
	if strings.Contains(p.Reason, "1Panel") || strings.Contains(p.Title+p.StepList[0].Summary, "1Panel") {
		t.Errorf("a 宝塔 checklist talks of 1Panel: %s / %s", p.Title, p.Reason)
	}
	// Adding the domain changed the config: read it again, as the page does.
	if d, err = a.Website(ctx, sv.ID, id); err != nil {
		t.Fatal(err)
	}
	next := d.Conf + "\n# Miao Panel\n"
	p, err = a.ProposeWebsite(ctx, SiteRequest{ServerID: sv.ID, Site: "blog.example.com", Op: "conf", Content: next, BaseHash: d.ConfHash})
	if err != nil || len(p.StepList[0].Diffs) != 1 || !strings.Contains(p.StepList[0].Diffs[0].Diff, "+# Miao Panel") {
		t.Fatalf("conf plan = %+v, %v", p, err)
	}
	run(SiteRequest{Op: "backup"})
	if bk, err := a.SiteBackups(ctx, sv.ID, id); err != nil || len(bk.Backups) != 1 || !bk.Backups[0].Local {
		t.Fatalf("backups = %+v, %v", bk, err)
	} else if path, err := a.BackupPath(ctx, sv.ID, id, bk.Backups[0].ID); err != nil || !strings.HasPrefix(path, "/") {
		t.Fatalf("backup path = %q, %v", path, err)
	}
	if _, err := a.ProposeWebsite(ctx, SiteRequest{ServerID: sv.ID, Site: "blog.example.com", Op: "delete"}); err == nil {
		t.Error("deleting a 宝塔 site was proposed")
	}
	if log, err := a.WebsiteLog(ctx, sv.ID, id, "access", 50); err != nil || !log.Enabled {
		t.Fatalf("log = %+v, %v", log, err)
	}

	raw, _ := json.Marshal(map[string]any{"server_id": sv.ID})
	if text, err := a.toolPanelWebsites(ctx, raw); err != nil || !strings.Contains(text, "blog.example.com") || !strings.Contains(text, "宝塔") {
		t.Fatalf("tool list = %s, %v", text, err)
	}
	raw, _ = json.Marshal(map[string]any{"server_id": sv.ID, "website": "blog.example.com"})
	if text, err := a.toolPanelWebsite(ctx, raw); err != nil || !strings.Contains(text, "www.example.com:80") || !strings.Contains(text, "Nginx 配置文件") {
		t.Fatalf("tool detail = %s, %v", text, err)
	}
}
