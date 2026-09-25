package actions

import (
	"context"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func hasRule(rules []tencent.FirewallRule, port string) bool {
	for _, r := range rules {
		if r.Port == port {
			return true
		}
	}
	return false
}

func TestCloudFirewall(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()
	lh := map[string]any{"instance": "lhins-abc12345", "region": "ap-guangzhou"}
	with := func(base map[string]any, kv ...any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}

	open := mustResolve(t, "cloud.firewall.open", with(lh, "port", "443"))
	out := Apply(ctx, env, open, nil)
	if out.Status != StatusDone || !hasRule(f.Firewall["lhins-abc12345"], "443") {
		t.Fatalf("open 443: %+v", out)
	}
	if again := Apply(ctx, env, open, nil); again.Status != StatusDone || len(again.Undo) != 0 {
		t.Fatalf("opening again should change nothing: %+v", again)
	}
	if u := Undo(ctx, env, open, out.Undo); u.Status != StatusUndone || hasRule(f.Firewall["lhins-abc12345"], "443") {
		t.Fatalf("undo open: %+v", u)
	}

	for _, port := range []string{"22", "ALL", "20-30"} {
		r := mustResolve(t, "cloud.firewall.close", with(lh, "port", port))
		if out := Apply(ctx, env, r, nil); out.Status != StatusRefused || !strings.Contains(strings.Join(out.Log, ""), "远程登录") {
			t.Errorf("closing %s should be refused: %+v", port, out)
		}
	}
	closeHTTP := mustResolve(t, "cloud.firewall.close", with(lh, "port", "80"))
	out = Apply(ctx, env, closeHTTP, nil)
	if out.Status != StatusDone || hasRule(f.Firewall["lhins-abc12345"], "80") {
		t.Fatalf("close 80: %+v", out)
	}
	if u := Undo(ctx, env, closeHTTP, out.Undo); u.Status != StatusUndone || !hasRule(f.Firewall["lhins-abc12345"], "80") {
		t.Fatalf("undo close: %+v", u)
	}

	// CVM: the rule goes into the instance's security group.
	cvm := mustResolve(t, "cloud.firewall.open", map[string]any{"instance": "ins-xyz98765", "region": "ap-guangzhou", "port": "8080"})
	out = Apply(ctx, env, cvm, nil)
	if out.Status != StatusDone || !hasRule(f.Firewall["sg-abc"], "8080") || !strings.Contains(strings.Join(out.Log, ""), "安全组 sg-abc") {
		t.Fatalf("cvm open: %+v", out)
	}
	if u := Undo(ctx, env, cvm, out.Undo); u.Status != StatusUndone || hasRule(f.Firewall["sg-abc"], "8080") {
		t.Fatalf("cvm undo: %+v", u)
	}

	for _, p := range []map[string]any{
		with(lh, "port", "99999"), with(lh, "port", "80;rm"), with(lh, "port", "80", "cidr", "everyone"),
		{"instance": "i-bad", "region": "ap-guangzhou", "port": "80"}, {"instance": "lhins-abc12345", "region": "Guangzhou!", "port": "80"},
	} {
		if _, err := Resolve("cloud.firewall.open", p, "-"); err == nil {
			t.Errorf("accepted %v", p)
		}
	}
	missing := mustResolve(t, "cloud.firewall.open", map[string]any{"instance": "lhins-abc12345", "region": "ap-shanghai", "port": "80"})
	if out := Apply(ctx, env, missing, nil); out.Status != StatusRefused {
		t.Fatalf("wrong region: %+v", out)
	}
}

func TestCloudSnapshotAndPower(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()
	for _, id := range []string{"lhins-abc12345", "ins-xyz98765"} {
		r := mustResolve(t, "cloud.snapshot.create", map[string]any{"instance": id, "region": "ap-guangzhou", "name": "before-upgrade"})
		out := Apply(ctx, env, r, nil)
		if out.Status != StatusDone || !strings.Contains(strings.Join(out.Log, ""), "已创建好") {
			t.Fatalf("snapshot %s: %+v", id, out)
		}
	}
	if len(f.Snaps) != 2 {
		t.Fatalf("snapshots = %d", len(f.Snaps))
	}

	target := map[string]any{"instance": "lhins-abc12345", "region": "ap-guangzhou"}
	start := mustResolve(t, "cloud.server.start", target)
	if out := Apply(ctx, env, start, nil); out.Status != StatusDone || len(out.Undo) != 0 {
		t.Fatalf("starting a running server should change nothing: %+v", out)
	}
	stop := mustResolve(t, "cloud.server.stop", target)
	out := Apply(ctx, env, stop, nil)
	if out.Status != StatusDone || f.Instances["lhins-abc12345"].State != "STOPPED" {
		t.Fatalf("stop: %+v state=%s", out, f.Instances["lhins-abc12345"].State)
	}
	reboot := mustResolve(t, "cloud.server.reboot", target)
	if out := Apply(ctx, env, reboot, nil); out.Status != StatusRefused {
		t.Fatalf("rebooting a stopped server should be refused: %+v", out)
	}
	if u := Undo(ctx, env, stop, out.Undo); u.Status != StatusUndone {
		t.Fatalf("undo stop: %+v", u)
	}
	// The undo only starts it; the next look shows it running.
	if s, _, _ := env.Cloud.Instance(ctx, "ap-guangzhou", "lhins-abc12345"); s.State != "STARTING" && s.State != "RUNNING" {
		t.Fatalf("after undo: %s", s.State)
	}
	if out := Apply(ctx, env, reboot, nil); out.Status != StatusDone {
		t.Fatalf("reboot: %+v", out)
	}
}

func TestEdgeOneManagement(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()
	f.Domains = append(f.Domains, &tencenttest.Domain{Zone: "zone-abc", Name: "blog.example.com", Status: "online", Cname: "blog.example.com.eo.dnse4.com", Origin: "1.2.3.4", Protocol: "HTTP"})

	purge := mustResolve(t, "eo.cache.purge", map[string]any{"domain": "blog.example.com", "targets": "https://blog.example.com/a.css, https://blog.example.com/b.js"})
	if out := Apply(ctx, env, purge, nil); out.Status != StatusDone || !strings.Contains(strings.Join(out.Commands, ""), "purge_url") {
		t.Fatalf("purge: %+v", out)
	}
	for _, targets := range []string{"https://evil.com/x", "blog.example.com/no-scheme", ""} {
		r := mustResolve(t, "eo.cache.purge", map[string]any{"domain": "blog.example.com", "targets": targets})
		if out := Apply(ctx, env, r, nil); out.Status != StatusRefused {
			t.Errorf("purge %q should be refused: %+v", targets, out)
		}
	}
	all := mustResolve(t, "eo.cache.purge", map[string]any{"domain": "example.com", "type": "all"})
	if out := Apply(ctx, env, all, nil); out.Status != StatusDone {
		t.Fatalf("purge all: %+v", out)
	}

	origin := mustResolve(t, "eo.origin.set", map[string]any{"domain": "blog.example.com", "origin": "81.68.79.253"})
	out := Apply(ctx, env, origin, nil)
	if out.Status != StatusDone || f.Domain("blog.example.com").Origin != "81.68.79.253" {
		t.Fatalf("origin: %+v", out)
	}
	if u := Undo(ctx, env, origin, out.Undo); u.Status != StatusUndone || f.Domain("blog.example.com").Origin != "1.2.3.4" {
		t.Fatalf("undo origin: %+v", u)
	}

	off := mustResolve(t, "eo.domain.status", map[string]any{"domain": "blog.example.com", "status": "offline"})
	out = Apply(ctx, env, off, nil)
	if out.Status != StatusDone || f.Domain("blog.example.com").Status != "offline" {
		t.Fatalf("offline: %+v", out)
	}
	if u := Undo(ctx, env, off, out.Undo); u.Status != StatusUndone || f.Domain("blog.example.com").Status != "online" {
		t.Fatalf("undo offline: %+v", u)
	}
}
