package actions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func cloudEnv(t *testing.T) (*Env, *tencenttest.Fake) {
	f := tencenttest.Start(t)
	return &Env{Cloud: f.Client(), PollInterval: 5 * time.Millisecond}, f
}

func mustResolve(t *testing.T, capability string, params map[string]any) Resolved {
	t.Helper()
	r, err := Resolve(capability, params, "-")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The whole "put blog.example.com behind EdgeOne with HTTPS" flow, then
// undone step by step in reverse.
func TestEdgeOneSiteFlowAndUndo(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()

	add := mustResolve(t, "eo.domain.add", map[string]any{"domain": "blog.example.com", "origin": "81.68.79.253"})
	out1 := Apply(ctx, env, add, nil)
	d := f.Domain("blog.example.com")
	if out1.Status != StatusDone || d == nil || d.Origin != "81.68.79.253" || d.Protocol != "HTTP" {
		t.Fatalf("eo.domain.add: %+v domain=%+v", out1, d)
	}
	if !strings.Contains(strings.Join(out1.Log, ""), d.Cname) || !strings.Contains(strings.Join(out1.Commands, "\n"), "teo CreateAccelerationDomain") {
		t.Fatalf("log/commands: %v / %v", out1.Log, out1.Commands)
	}

	// Before DNS points at EdgeOne, the free certificate cannot be issued.
	https := mustResolve(t, "eo.https.set", map[string]any{"domain": "blog.example.com"})
	if early := Apply(ctx, env, https, nil); early.Status != StatusRefused || f.Domain("blog.example.com").CertMode != "disable" {
		t.Fatalf("certificate before DNS: %+v", early)
	}

	dns := mustResolve(t, "dns.record.set", map[string]any{"domain": "example.com", "subdomain": "blog", "point_to": "eo"})
	out2 := Apply(ctx, env, dns, nil)
	recs := f.Lookup("example.com", "blog")
	if out2.Status != StatusDone || len(recs) != 1 || recs[0].Type != "CNAME" || recs[0].Value != d.Cname || recs[0].RecordID != 1 {
		t.Fatalf("dns.record.set: %+v records=%+v", out2, recs)
	}
	if again := Apply(ctx, env, dns, nil); again.Status != StatusDone || len(again.Undo) != 0 {
		t.Fatalf("setting the same value again should change nothing: %+v", again)
	}

	out3 := Apply(ctx, env, https, nil)
	if out3.Status != StatusDone || f.Domain("blog.example.com").CertMode != "eofreecert" || !strings.Contains(strings.Join(out3.Log, ""), "https://blog.example.com") {
		t.Fatalf("eo.https.set: %+v", out3)
	}

	if u := Undo(ctx, env, https, out3.Undo); u.Status != StatusUndone || f.Domain("blog.example.com").CertMode != "disable" {
		t.Fatalf("undo https: %+v", u)
	}
	if u := Undo(ctx, env, dns, out2.Undo); u.Status != StatusUndone {
		t.Fatalf("undo dns: %+v", u)
	}
	if recs := f.Lookup("example.com", "blog"); len(recs) != 1 || recs[0].Type != "A" || recs[0].Value != "1.2.3.4" {
		t.Fatalf("DNS not restored: %+v", recs)
	}
	if u := Undo(ctx, env, add, out1.Undo); u.Status != StatusUndone || f.Domain("blog.example.com") != nil {
		t.Fatalf("undo eo domain: %+v", u)
	}
}

func TestDNSRecordCases(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()

	// A new name gets a new record, and undo deletes it.
	r := mustResolve(t, "dns.record.set", map[string]any{"domain": "example.com", "subdomain": "www", "type": "A", "value": "5.6.7.8"})
	out := Apply(ctx, env, r, nil)
	if recs := f.Lookup("example.com", "www"); out.Status != StatusDone || len(recs) != 1 || recs[0].TTL != 600 {
		t.Fatalf("create: %+v %+v", out, recs)
	}
	if u := Undo(ctx, env, r, out.Undo); u.Status != StatusUndone || len(f.Lookup("example.com", "www")) != 0 {
		t.Fatalf("undo create: %+v", u)
	}

	// Several A records (load balancing) are left alone.
	f.Records["example.com"] = append(f.Records["example.com"],
		tencenttest.Record{RecordID: 2, Name: "api", Type: "A", Value: "1.1.1.1", Line: "默认", TTL: 600},
		tencenttest.Record{RecordID: 3, Name: "api", Type: "A", Value: "2.2.2.2", Line: "默认", TTL: 600})
	r = mustResolve(t, "dns.record.set", map[string]any{"domain": "example.com", "subdomain": "api", "type": "A", "value": "3.3.3.3"})
	if out := Apply(ctx, env, r, nil); out.Status != StatusRefused || !strings.Contains(strings.Join(out.Log, ""), "负载均衡") {
		t.Fatalf("load balanced: %+v", out)
	}
	// ...but switching them to a CNAME changes one and removes the other,
	// and undo brings both back.
	r = mustResolve(t, "dns.record.set", map[string]any{"domain": "example.com", "subdomain": "api", "type": "CNAME", "value": "api.example.net"})
	out = Apply(ctx, env, r, nil)
	if recs := f.Lookup("example.com", "api"); out.Status != StatusDone || len(recs) != 1 || recs[0].Type != "CNAME" {
		t.Fatalf("A to CNAME: %+v %+v", out, recs)
	}
	if u := Undo(ctx, env, r, out.Undo); u.Status != StatusUndone {
		t.Fatalf("undo A to CNAME: %+v", u)
	}
	if recs := f.Lookup("example.com", "api"); len(recs) != 2 || recs[0].Type != "A" || recs[1].Type != "A" || recs[0].Value == recs[1].Value {
		t.Fatalf("A records not restored: %+v", recs)
	}

	// Bad values and a missing EdgeOne domain are refused before any change.
	for _, params := range []map[string]any{
		{"domain": "example.com", "subdomain": "x", "type": "A", "value": "not-an-ip"},
		{"domain": "example.com", "subdomain": "x", "type": "CNAME", "value": "1.2.3.4 && rm"},
		{"domain": "example.com", "subdomain": "shop", "point_to": "eo"},
	} {
		if out := Apply(ctx, env, mustResolve(t, "dns.record.set", params), nil); out.Status != StatusRefused {
			t.Errorf("%v: %+v", params, out)
		}
	}
	for _, params := range []map[string]any{
		{"domain": "example.com", "subdomain": "x", "type": "A"},
		{"domain": "example.com", "subdomain": "x", "point_to": "eo", "type": "A", "value": "1.1.1.1"},
		{"domain": "exa mple.com", "subdomain": "x", "type": "A", "value": "1.1.1.1"},
		{"domain": "example.com", "subdomain": "x;y", "type": "A", "value": "1.1.1.1"},
	} {
		if _, err := Resolve("dns.record.set", params, "-"); err == nil {
			t.Errorf("accepted %v", params)
		}
	}
}

func TestEdgeOneWithoutSite(t *testing.T) {
	env, _ := cloudEnv(t)
	r := mustResolve(t, "eo.domain.add", map[string]any{"domain": "shop.other.org", "origin": "1.2.3.4"})
	out := Apply(context.Background(), env, r, nil)
	if out.Status != StatusRefused || !strings.Contains(strings.Join(out.Log, ""), "EdgeOne 控制台添加站点") {
		t.Fatalf("got %+v", out)
	}
	if out := Apply(context.Background(), &Env{}, r, nil); out.Status != StatusRefused || !strings.Contains(strings.Join(out.Log, ""), "腾讯云密钥") {
		t.Fatalf("without credentials: %+v", out)
	}
}
