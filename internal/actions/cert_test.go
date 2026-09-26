package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx"
)

// fakeCerts is a 1Panel that issues certificates the way the real one
// does: in the background, reporting "applying" before "ready".
type fakeCerts struct {
	mu     sync.Mutex
	nextID int
	acme   []map[string]any
	dns    []map[string]any
	ssls   map[int]map[string]any
	looks  map[int]int
	https  map[string]any // website 5's HTTPS setting
	// renewals makes each renewed certificate last a day longer.
	renewals int
}

func newFakeCerts() *fakeCerts {
	return &fakeCerts{nextID: 10, ssls: map[int]map[string]any{}, looks: map[int]int{},
		https: map[string]any{"enable": false, "httpConfig": "", "SSL": map[string]any{"id": 0}}}
}

func (f *fakeCerts) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply := func(v any) {
		data, _ := json.Marshal(map[string]any{"code": 200, "data": v})
		_, _ = w.Write(data)
	}
	bad := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, fmt.Sprintf(`{"code":400,"message":%q}`, msg))
	}
	page := func(items any) map[string]any { return map[string]any{"total": 1, "items": items} }
	path := strings.TrimPrefix(r.URL.Path, "/api/v2")
	switch {
	case path == "/apps/installed/search":
		reply(page([]map[string]any{{"id": 2, "name": "openresty", "appKey": "openresty", "status": "Running", "httpPort": 80, "httpsPort": 443}}))
	case path == "/websites/list":
		reply([]map[string]any{{"id": 5, "primaryDomain": "blog.example.com", "type": "proxy"}})
	case path == "/websites/acme/search":
		reply(page(f.acme))
	case path == "/websites/acme":
		if body["type"] != "letsencrypt" || body["keyType"] != "EC256" {
			bad("bad acme account")
			return
		}
		f.acme = append(f.acme, map[string]any{"id": 1, "email": body["email"], "type": "letsencrypt"})
		reply(nil)
	case path == "/websites/dns/search":
		reply(page(f.dns))
	case path == "/websites/ssl/search":
		var items []map[string]any
		for _, s := range f.ssls {
			items = append(items, s)
		}
		reply(page(items))
	case path == "/websites/ssl" && r.Method == http.MethodPost:
		if body["acmeAccountId"] == float64(0) || body["autoRenew"] != true || body["keyType"] != "P256" {
			bad("bad ssl request")
			return
		}
		f.nextID++
		f.ssls[f.nextID] = map[string]any{"id": f.nextID, "primaryDomain": body["primaryDomain"], "domains": strings.ReplaceAll(body["otherDomains"].(string), "\n", ","),
			"provider": body["provider"], "status": "applying", "autoRenew": true, "acmeAccountId": body["acmeAccountId"], "keyType": "P256",
			"privateKey": "-----BEGIN EC PRIVATE KEY-----secret"}
		reply(map[string]any{"id": f.nextID})
	case path == "/websites/ssl/del":
		for _, id := range body["ids"].([]any) {
			delete(f.ssls, int(id.(float64)))
		}
		reply(nil)
	case path == "/websites/ssl/obtain":
		s := f.ssls[int(body["ID"].(float64))]
		s["status"] = "applying"
		f.looks[int(body["ID"].(float64))] = 0
		f.renewals++
		reply(nil)
	case path == "/websites/ssl/update":
		s := f.ssls[int(body["id"].(float64))]
		if body["primaryDomain"] != s["primaryDomain"] || body["provider"] != s["provider"] {
			bad("update must keep the certificate's fields")
			return
		}
		s["autoRenew"] = body["autoRenew"]
		reply(nil)
	case strings.HasPrefix(path, "/websites/ssl/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(path, "/websites/ssl/"))
		s := f.ssls[id]
		if s == nil {
			bad("ErrRecordNotFound")
			return
		}
		f.looks[id]++
		if s["status"] == "applying" && f.looks[id] >= 2 {
			if strings.HasPrefix(s["primaryDomain"].(string), "fail.") {
				s["status"], s["message"] = "applyError", "acme: error: 403 :: urn:ietf:params:acme:error:unauthorized :: Invalid response from http://fail.example.com/.well-known/acme-challenge/x: 404"
			} else {
				s["status"], s["organization"] = "ready", "Let's Encrypt"
				s["expireDate"] = time.Now().Add(time.Duration(90+f.renewals) * 24 * time.Hour).UTC().Format(time.RFC3339)
			}
		}
		reply(s)
	case path == "/websites/5/https" && r.Method == http.MethodGet:
		reply(f.https)
	case path == "/websites/5/https":
		if body["type"] != "existed" || (body["enable"] == true && (body["websiteSSLId"] == nil || body["algorithm"] == "")) {
			bad("bad https request")
			return
		}
		f.https = map[string]any{"enable": body["enable"], "httpConfig": body["httpConfig"], "SSL": map[string]any{"id": body["websiteSSLId"]}}
		reply(nil)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func certEnv(t *testing.T, f *fakeCerts) (*Env, *stubConn) {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	conn := &stubConn{run: func(string) sshx.Result { return sshx.Result{Stdout: "200"} }}
	return &Env{SSH: conn, User: "root", PollInterval: time.Millisecond,
		OnePanel: onepanel.New(func(network, _ string) (net.Conn, error) { return net.Dial(network, addr) }, 1, "k", "", "")}, conn
}

func TestCertIssueRenewAndUndo(t *testing.T) {
	f := newFakeCerts()
	env, conn := certEnv(t, f)
	ctx := context.Background()
	resolve := func(c string, p map[string]any) Resolved {
		t.Helper()
		r, err := Resolve(c, p, "1panel")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	issue := resolve("cert.issue", map[string]any{"domain": "blog.example.com", "other_domains": "www.example.com"})
	if o := Apply(ctx, env, issue, nil); o.Status != StatusRefused || !strings.Contains(strings.Join(o.Log, ""), "邮箱") {
		t.Fatalf("no ACME account and no email: %+v", o)
	}
	issue = resolve("cert.issue", map[string]any{"domain": "blog.example.com", "other_domains": "www.example.com", "email": "me@example.com"})
	out := Apply(ctx, env, issue, nil)
	log := strings.Join(out.Log, "\n")
	if out.Status != StatusDone || len(f.acme) != 1 || !strings.Contains(log, "已签发") || !strings.Contains(log, "返回 200") {
		t.Fatalf("issue: %+v", out)
	}
	if f.https["enable"] != true || f.https["httpConfig"] != "HTTPAlso" || !strings.Contains(conn.cmds[0], "--resolve 'blog.example.com:443:127.0.0.1'") {
		t.Fatalf("https = %v, cmds %v", f.https, conn.cmds)
	}
	if strings.Contains(strings.Join(out.Commands, "\n"), "PRIVATE KEY") {
		t.Fatal("private key in the log")
	}
	if again := Apply(ctx, env, issue, nil); again.Status != StatusDone || len(again.Undo) != 0 || len(f.ssls) != 1 {
		t.Fatalf("issuing again should reuse the certificate: %+v", again)
	}

	renew := resolve("cert.renew", map[string]any{"domain": "blog.example.com"})
	if o := Apply(ctx, env, renew, nil); o.Status != StatusDone || !strings.Contains(strings.Join(o.Log, ""), "新证书有效期到") {
		t.Fatalf("renew: %+v", o)
	}
	off := resolve("cert.autorenew.set", map[string]any{"domain": "blog.example.com", "enabled": "off"})
	o := Apply(ctx, env, off, nil)
	var id int
	for k := range f.ssls {
		id = k
	}
	if o.Status != StatusDone || f.ssls[id]["autoRenew"] != false {
		t.Fatalf("auto renew off: %+v", o)
	}
	if u := Undo(ctx, env, off, o.Undo); u.Status != StatusUndone || f.ssls[id]["autoRenew"] != true {
		t.Fatalf("undo auto renew: %+v", u)
	}

	if u := Undo(ctx, env, issue, out.Undo); u.Status != StatusUndone || f.https["enable"] != false || len(f.ssls) != 0 {
		t.Fatalf("undo issue: %+v https=%v ssls=%v", u, f.https, f.ssls)
	}

	failing := resolve("cert.issue", map[string]any{"domain": "fail.example.com"})
	if o := Apply(ctx, env, failing, nil); o.Status != StatusRefused || !strings.Contains(strings.Join(o.Log, ""), "unauthorized") || len(f.ssls) != 0 {
		t.Fatalf("failed validation: %+v %v", o, f.ssls)
	}
	dns := resolve("cert.issue", map[string]any{"domain": "*.example.com", "method": "dns"})
	if o := Apply(ctx, env, dns, nil); o.Status != StatusRefused || !strings.Contains(strings.Join(o.Log, ""), "DNS 账号") {
		t.Fatalf("dns without account: %+v", o)
	}
	for _, p := range []map[string]any{
		{"domain": "*.example.com"},
		{"domain": "blog.example.com", "other_domains": "*.example.com"},
		{"domain": "blog.example.com", "email": "not-an-email"},
		{"domain": "localhost"},
	} {
		if _, err := Resolve("cert.issue", p, "1panel"); err == nil {
			t.Errorf("accepted %v", p)
		}
	}
	if _, err := Resolve("cert.issue", map[string]any{"domain": "blog.example.com"}, "bt"); err == nil {
		t.Error("cert.issue should not run on 宝塔 yet")
	}
}
