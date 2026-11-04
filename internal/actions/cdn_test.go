package actions

import (
	"context"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

func TestTencentCDN(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()
	f.CDN = []*tencent.CDNDomain{
		{Domain: "static.example.com", Cname: "static.example.com.cdn.dnsv1.com", Status: "online", ServiceType: "web", Area: "mainland", Origins: []string{"1.2.3.4"}, OriginType: "ip"},
		{Domain: "old.example.com", Status: "offline", ServiceType: "web", Area: "mainland", Origins: []string{"1.2.3.4"}, OriginType: "ip"},
	}
	run := func(capability string, params map[string]any, want string) Outcome {
		t.Helper()
		out := Apply(ctx, env, mustResolve(t, capability, params), nil)
		if out.Status != want {
			t.Fatalf("%s %v: %s %v", capability, params, out.Status, out.Log)
		}
		return out
	}
	run("cdn.cache.purge", map[string]any{"targets": "https://static.example.com/app.js\nhttps://static.example.com/css/site.css"}, StatusDone)
	run("cdn.cache.purge", map[string]any{"targets": "https://static.example.com/img", "type": "dir"}, StatusDone)
	run("cdn.cache.prefetch", map[string]any{"targets": "https://static.example.com/big.zip"}, StatusDone)
	want := []string{"purge-url https://static.example.com/app.js", "purge-url https://static.example.com/css/site.css",
		"purge-dir https://static.example.com/img/", "push https://static.example.com/big.zip"}
	if len(f.CDNTasks) != len(want) {
		t.Fatalf("tasks = %v", f.CDNTasks)
	}
	for i := range want {
		if f.CDNTasks[i] != want[i] {
			t.Errorf("task %d = %q, want %q", i, f.CDNTasks[i], want[i])
		}
	}
	// Not on this account, not a URL, or not serving: refused before asking.
	for _, bad := range []string{"https://www.other.com/a", "static.example.com/a", "https://old.example.com/a"} {
		run("cdn.cache.purge", map[string]any{"targets": bad}, StatusRefused)
	}

	r := mustResolve(t, "cdn.domain.status", map[string]any{"domain": "old.example.com", "status": "on"})
	out := Apply(ctx, env, r, nil)
	if out.Status != StatusDone || f.CDN[1].Status != "online" {
		t.Fatalf("start: %+v %s", out, f.CDN[1].Status)
	}
	if u := Undo(ctx, env, r, out.Undo); u.Status != StatusUndone || f.CDN[1].Status != "offline" {
		t.Fatalf("undo: %+v %s", u, f.CDN[1].Status)
	}
	if again := run("cdn.domain.status", map[string]any{"domain": "old.example.com", "status": "off"}, StatusDone); len(again.Undo) != 0 {
		t.Errorf("nothing to change, yet undo = %v", again.Undo)
	}
	run("cdn.domain.status", map[string]any{"domain": "nope.example.com", "status": "off"}, StatusRefused)
}

func TestAliyunCDNStatus(t *testing.T) {
	env, f := aliEnv(t)
	ctx := context.Background()
	if len(f.CDN) == 0 {
		f.CDN = append(f.CDN, &aliyuntest.CDNDomain{Name: "cdn.example.com", Cname: "cdn.example.com.w.kunlunsl.com", Status: "online", Origin: "1.2.3.4"})
	}
	name := f.CDN[0].Name
	r, out := aliRun(t, env, "aliyun.cdn.status", map[string]any{"domain": name, "status": "off"}, StatusDone)
	if f.CDN[0].Status != "offline" {
		t.Fatalf("stop: %s", f.CDN[0].Status)
	}
	if u := Undo(ctx, env, r, out.Undo); u.Status != StatusUndone || f.CDN[0].Status != "online" {
		t.Fatalf("undo: %+v %s", u, f.CDN[0].Status)
	}
	aliRun(t, env, "aliyun.cdn.status", map[string]any{"domain": "nope.example.com", "status": "off"}, StatusRefused)
}

// A free certificate, validated through DNSPod, put on the CDN domain of
// the same name; HTTPS on the CDN can be undone.
func TestFreeCertOnCDN(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()
	f.CDN = []*tencent.CDNDomain{{Domain: "static.example.com", Status: "online", ServiceType: "web", Area: "mainland", Origins: []string{"1.2.3.4"}, OriginType: "ip"}}

	r := mustResolve(t, "ssl.cert.apply", map[string]any{"domain": "static.example.com", "use_cdn": "yes"})
	out := Apply(ctx, env, r, nil)
	if out.Status != StatusDone || out.Result["cert"] == "" || !f.CDN[0].HTTPS || f.CDN[0].CertID != out.Result["cert"] {
		t.Fatalf("apply: %+v cdn=%+v", out, f.CDN[0])
	}
	if len(f.SSLApplied) != 1 || f.SSLApplied[0] != "static.example.com DNS_AUTO" {
		t.Errorf("applied = %v", f.SSLApplied)
	}

	// Off, then undone: back on the same certificate.
	cert := f.CDN[0].CertID
	off := mustResolve(t, "cdn.https.set", map[string]any{"domain": "static.example.com", "cert": "off"})
	o := Apply(ctx, env, off, nil)
	if o.Status != StatusDone || f.CDN[0].HTTPS {
		t.Fatalf("off: %+v", o)
	}
	if u := Undo(ctx, env, off, o.Undo); u.Status != StatusUndone || !f.CDN[0].HTTPS || f.CDN[0].CertID != cert {
		t.Fatalf("undo: %+v %+v", u, f.CDN[0])
	}

	// A certificate for another name, or none at all, is refused.
	for _, c := range []string{"ssl-abc", "ssl-nope"} {
		if o := Apply(ctx, env, mustResolve(t, "cdn.https.set", map[string]any{"domain": "static.example.com", "cert": c}), nil); o.Status != StatusRefused {
			t.Errorf("%s: %+v", c, o)
		}
	}
	// Not in DNSPod, or a wildcard: refused before applying.
	for _, d := range []string{"www.other.com", "*.example.com"} {
		p := map[string]any{"domain": d}
		if _, err := Resolve("ssl.cert.apply", p, "-"); err != nil {
			continue // the parameter itself is refused
		}
		if o := Apply(ctx, env, mustResolve(t, "ssl.cert.apply", p), nil); o.Status != StatusRefused {
			t.Errorf("%s: %+v", d, o)
		}
	}
	if len(f.SSLApplied) != 1 {
		t.Errorf("applied = %v", f.SSLApplied)
	}
	// Out of free certificates: the cloud's refusal is passed on.
	f.FreeCertsLeft = 0
	if o := Apply(ctx, env, mustResolve(t, "ssl.cert.apply", map[string]any{"domain": "blog.example.com"}), nil); o.Status != StatusRefused {
		t.Errorf("over the limit: %+v", o)
	}
}
