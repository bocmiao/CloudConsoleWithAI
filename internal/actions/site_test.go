package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
)

// fakeSites is a 1Panel with OpenResty and Halo installed that keeps the
// websites it is asked to create, checking requests the way 1Panel does.
type fakeSites struct {
	mu        sync.Mutex
	openresty string
	sites     []map[string]any
	nextID    int
}

func (f *fakeSites) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply := func(v any) {
		data, _ := json.Marshal(map[string]any{"code": 200, "message": "", "data": v})
		_, _ = w.Write(data)
	}
	bad := func(msg string) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, fmt.Sprintf(`{"code":500,"message":%q}`, msg))
	}
	switch r.URL.Path {
	case "/api/v2/apps/installed/search":
		items := []map[string]any{{"id": 7, "name": "halo", "appKey": "halo", "status": "Running", "httpPort": 8090}}
		if f.openresty != "" {
			items = append(items, map[string]any{"id": 2, "name": "openresty", "appKey": "openresty", "status": f.openresty, "httpPort": 80})
		}
		reply(map[string]any{"total": len(items), "items": items})
	case "/api/v2/websites/list":
		reply(f.sites)
	case "/api/v2/groups/search":
		if body["type"] != "website" {
			bad("bad group type")
			return
		}
		reply([]map[string]any{{"id": 3, "name": "默认", "type": "website", "isDefault": true}})
	case "/api/v2/websites":
		domains, _ := body["domains"].([]any)
		if body["appType"] != "installed" && body["appType"] != "new" {
			bad("Key: 'WebsiteCreate.AppType' Error:Field validation for 'AppType' failed on the 'oneof' tag")
			return
		}
		if body["webSiteGroupID"] != float64(3) || body["alias"] == "" || len(domains) != 1 {
			bad("bad create request")
			return
		}
		d := domains[0].(map[string]any)
		if d["port"] != float64(80) {
			bad("domain port missing")
			return
		}
		if body["type"] == "proxy" && body["proxy"] != "http://127.0.0.1:8090" {
			bad("bad proxy")
			return
		}
		f.nextID++
		f.sites = append(f.sites, map[string]any{"id": f.nextID, "primaryDomain": d["domain"], "alias": body["alias"], "type": body["type"],
			"status": "Running", "proxy": body["proxy"], "sitePath": "/opt/1panel/www/sites/" + body["alias"].(string)})
		reply(nil)
	case "/api/v2/websites/del":
		for i, s := range f.sites {
			if float64(s["id"].(int)) == body["id"] {
				if body["deleteApp"] != false || body["deleteDB"] != false {
					bad("would delete the app")
					return
				}
				f.sites = append(f.sites[:i:i], f.sites[i+1:]...)
				reply(nil)
				return
			}
		}
		bad("ErrRecordNotFound")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// stubConn answers commands on the "server" with a canned function.
type stubConn struct {
	run  func(cmd string) sshx.Result
	cmds []string
}

func (s *stubConn) Run(_ context.Context, cmd, _ string, _ int) (sshx.Result, error) {
	s.cmds = append(s.cmds, cmd)
	return s.run(cmd), nil
}
func (s *stubConn) RunScript(ctx context.Context, _, script string, _ []string, n int) (sshx.Result, error) {
	return s.Run(ctx, script, "", n)
}
func (s *stubConn) Close() error { return nil }

func TestSiteCreate(t *testing.T) {
	f := &fakeSites{openresty: "Running"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	conn := &stubConn{run: func(cmd string) sshx.Result { return sshx.Result{Stdout: "200"} }}
	env := &Env{SSH: conn, User: "root",
		OnePanel: onepanel.New(func(network, _ string) (net.Conn, error) { return net.Dial(network, addr) }, 1, "k", "", "")}
	ctx := context.Background()

	r, err := Resolve("site.create", map[string]any{"domain": "Blog.Example.com", "app": "halo"}, "1panel")
	if err != nil {
		t.Fatal(err)
	}
	out := Apply(ctx, env, r, nil)
	log := strings.Join(out.Log, "\n")
	if out.Status != StatusDone || len(f.sites) != 1 || f.sites[0]["primaryDomain"] != "blog.example.com" || !strings.Contains(log, "返回 200") {
		t.Fatalf("create: %+v %v", out, f.sites)
	}
	if len(conn.cmds) != 1 || !strings.Contains(conn.cmds[0], "'Host: blog.example.com'") {
		t.Fatalf("check command: %v", conn.cmds)
	}
	if again := Apply(ctx, env, r, nil); again.Status != StatusDone || len(again.Undo) != 0 || len(f.sites) != 1 {
		t.Fatalf("creating again should change nothing: %+v", again)
	}
	if u := Undo(ctx, env, r, out.Undo); u.Status != StatusUndone || len(f.sites) != 0 {
		t.Fatalf("undo: %+v %v", u, f.sites)
	}

	static, _ := Resolve("site.create", map[string]any{"domain": "www.example.com", "type": "static"}, "1panel")
	if o := Apply(ctx, env, static, nil); o.Status != StatusDone || f.sites[0]["type"] != "static" {
		t.Fatalf("static: %+v", o)
	}

	f.openresty = ""
	other, _ := Resolve("site.create", map[string]any{"domain": "shop.example.com", "proxy": "http://127.0.0.1:8090"}, "1panel")
	if o := Apply(ctx, env, other, nil); o.Status != StatusRefused || !strings.Contains(strings.Join(o.Log, ""), "OpenResty") {
		t.Fatalf("without OpenResty: %+v", o)
	}

	for _, p := range []map[string]any{
		{"domain": "a.example.com"}, // proxy needs app or proxy
		{"domain": "a.example.com", "app": "halo", "proxy": "http://127.0.0.1:1"}, // not both
		{"domain": "a.example.com", "type": "static", "app": "halo"},
		{"domain": "a.example.com", "proxy": "http://127.0.0.1:80; rm -rf /"},
	} {
		if _, err := Resolve("site.create", p, "1panel"); err == nil {
			t.Errorf("accepted %v", p)
		}
	}
	if _, err := Resolve("site.create", map[string]any{"domain": "a.example.com", "app": "halo"}, "bt"); err == nil {
		t.Error("site.create should not run on 宝塔 yet")
	}
}
