package actions

import (
	"context"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func policy(t *testing.T, f *tencenttest.Fake, zone string) tencent.SecurityPolicy {
	t.Helper()
	p, err := f.Client().SecurityPolicy(context.Background(), zone)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func rule(rules []map[string]any, prefix string) map[string]any {
	for _, r := range rules {
		if strings.HasPrefix(tencent.RuleString(r, "Name"), prefix) {
			return r
		}
	}
	return nil
}

func TestEdgeOneIPBlock(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()

	block := mustResolve(t, "eo.ip.block", map[string]any{"domain": "blog.example.com", "ips": "1.2.3.4, 5.6.7.0/24 1.2.3.4"})
	if block.Values["ips"] != "1.2.3.4,5.6.7.0/24" {
		t.Fatalf("ips not normalized: %q", block.Values["ips"])
	}
	out := Apply(ctx, env, block, nil)
	p := policy(t, f, "zone-abc")
	mine := rule(p.CustomRules, BlockRuleName)
	if out.Status != StatusDone || mine == nil || tencent.RuleString(mine, "Condition") != "${http.request.ip} in ['1.2.3.4','5.6.7.0/24']" {
		t.Fatalf("block: %+v %v", out, p.CustomRules)
	}
	if rule(p.CustomRules, "屏蔽海外") == nil || tencent.RuleString(rule(p.CustomRules, "屏蔽海外"), "Id") != "rule-1" {
		t.Fatalf("the user's own rule was not kept: %v", p.CustomRules)
	}
	if again := Apply(ctx, env, block, nil); again.Status != StatusDone || len(again.Undo) != 0 {
		t.Fatalf("blocking again should change nothing: %+v", again)
	}

	more := mustResolve(t, "eo.ip.block", map[string]any{"domain": "example.com", "ips": "9.9.9.9"})
	out2 := Apply(ctx, env, more, nil)
	mine2 := rule(policy(t, f, "zone-abc").CustomRules, BlockRuleName)
	if out2.Status != StatusDone || !strings.Contains(tencent.RuleString(mine2, "Condition"), "'9.9.9.9'") || mine2["Id"] != mine["Id"] {
		t.Fatalf("second block should extend the same rule: %+v %v", out2, mine2)
	}

	unblock := mustResolve(t, "eo.ip.unblock", map[string]any{"domain": "example.com", "ips": "1.2.3.4"})
	out3 := Apply(ctx, env, unblock, nil)
	cond := tencent.RuleString(rule(policy(t, f, "zone-abc").CustomRules, BlockRuleName), "Condition")
	if out3.Status != StatusDone || strings.Contains(cond, "1.2.3.4") || !strings.Contains(cond, "9.9.9.9") {
		t.Fatalf("unblock: %+v %s", out3, cond)
	}

	// Undo in reverse brings back each earlier list, and finally removes
	// the rule, leaving the user's rule alone.
	for _, step := range []struct {
		r   Resolved
		out Outcome
	}{{unblock, out3}, {more, out2}, {block, out}} {
		if u := Undo(ctx, env, step.r, step.out.Undo); u.Status != StatusUndone {
			t.Fatalf("undo %s: %+v", step.r.Cap.Name, u)
		}
	}
	p = policy(t, f, "zone-abc")
	if rule(p.CustomRules, BlockRuleName) != nil || rule(p.CustomRules, "屏蔽海外") == nil {
		t.Fatalf("after undo: %v", p.CustomRules)
	}

	// Undoing an earlier block leaves a later one alone.
	a := mustResolve(t, "eo.ip.block", map[string]any{"domain": "example.com", "ips": "1.1.1.1"})
	outA := Apply(ctx, env, a, nil)
	b := mustResolve(t, "eo.ip.block", map[string]any{"domain": "example.com", "ips": "2.2.2.2"})
	Apply(ctx, env, b, nil)
	if u := Undo(ctx, env, a, outA.Undo); u.Status != StatusUndone {
		t.Fatalf("undo A: %+v", u)
	}
	cond = tencent.RuleString(rule(policy(t, f, "zone-abc").CustomRules, BlockRuleName), "Condition")
	if strings.Contains(cond, "1.1.1.1") || !strings.Contains(cond, "2.2.2.2") {
		t.Fatalf("undo of an earlier block touched a later one: %s", cond)
	}

	for _, ips := range []string{"0.0.0.0/0", "10.0.0.0/4", "not-an-ip", "1.2.3.4'] or ['x"} {
		if _, err := Resolve("eo.ip.block", map[string]any{"domain": "example.com", "ips": ips}, "-"); err == nil {
			t.Errorf("accepted ips %q", ips)
		}
	}
}

func TestEdgeOneRateLimitAndCC(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()

	rl := mustResolve(t, "eo.ratelimit.set", map[string]any{"domain": "blog.example.com", "path": "/wp-login.php", "threshold": 20})
	out := Apply(ctx, env, rl, nil)
	r := rule(policy(t, f, "zone-abc").RateLimitingRules, "Miao Panel 限速")
	if out.Status != StatusDone || r == nil ||
		tencent.RuleString(r, "Condition") != "${http.request.host} in ['blog.example.com'] and ${http.request.uri.path} contain ['/wp-login.php']" ||
		tencent.RuleString(r, "CountingPeriod") != "1m" || tencent.ActionName(r) != "JSChallenge" || r["MaxRequestThreshold"] != float64(20) {
		t.Fatalf("rate limit: %+v %v", out, r)
	}
	stricter := mustResolve(t, "eo.ratelimit.set", map[string]any{"domain": "blog.example.com", "path": "/wp-login.php", "threshold": 5, "action": "deny"})
	out2 := Apply(ctx, env, stricter, nil)
	rules := policy(t, f, "zone-abc").RateLimitingRules
	if out2.Status != StatusDone || len(rules) != 1 || rules[0]["MaxRequestThreshold"] != float64(5) || rules[0]["Id"] != r["Id"] {
		t.Fatalf("same scope should update the rule: %v", rules)
	}
	if u := Undo(ctx, env, stricter, out2.Undo); u.Status != StatusUndone ||
		rule(policy(t, f, "zone-abc").RateLimitingRules, "Miao Panel")["MaxRequestThreshold"] != float64(20) {
		t.Fatalf("undo update: %+v", u)
	}
	name := tencent.RuleString(r, "Name")
	rm := mustResolve(t, "eo.ratelimit.remove", map[string]any{"domain": "example.com", "name": name})
	out3 := Apply(ctx, env, rm, nil)
	if out3.Status != StatusDone || len(policy(t, f, "zone-abc").RateLimitingRules) != 0 {
		t.Fatalf("remove: %+v", out3)
	}
	if u := Undo(ctx, env, rm, out3.Undo); u.Status != StatusUndone || rule(policy(t, f, "zone-abc").RateLimitingRules, name) == nil {
		t.Fatalf("undo remove: %+v", u)
	}
	for _, p := range []map[string]any{
		{"domain": "example.com", "threshold": 10, "duration": "500m"},
		{"domain": "example.com", "threshold": 10, "path": "/a' or 1"},
		{"domain": "example.com", "threshold": 10, "period": "3m"},
	} {
		if _, err := Resolve("eo.ratelimit.set", p, "-"); err == nil {
			t.Errorf("accepted %v", p)
		}
	}

	cc := mustResolve(t, "eo.cc.set", map[string]any{"domain": "example.com", "enabled": "on"})
	out4 := Apply(ctx, env, cc, nil)
	ddos := policy(t, f, "zone-abc").HTTPDDoS
	afc, _ := ddos["AdaptiveFrequencyControl"].(map[string]any)
	cf, _ := ddos["ClientFiltering"].(map[string]any)
	if out4.Status != StatusDone || afc["Enabled"] != "on" || afc["Sensitivity"] != "Moderate" || cf["Enabled"] != "on" {
		t.Fatalf("cc on: %+v %v", out4, ddos)
	}
	if again := Apply(ctx, env, cc, nil); len(again.Undo) != 0 {
		t.Fatalf("same setting again should change nothing: %+v", again)
	}
	if u := Undo(ctx, env, cc, out4.Undo); u.Status != StatusUndone {
		t.Fatalf("undo cc: %+v", u)
	}
	afc, _ = policy(t, f, "zone-abc").HTTPDDoS["AdaptiveFrequencyControl"].(map[string]any)
	if afc["Enabled"] != "off" {
		t.Fatalf("cc not restored: %v", afc)
	}
}

func TestEdgeOneZoneCreate(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()
	f.Records["example.org"] = nil // on DNSPod, with no records yet
	create := mustResolve(t, "eo.zone.create", map[string]any{"domain": "example.org", "area": "mainland"})
	out := Apply(ctx, env, create, nil)
	log := strings.Join(out.Log, "\n")
	if out.Status != StatusDone || !strings.Contains(log, "edgeone-free2") || !strings.Contains(log, "验证通过") {
		t.Fatalf("create: %+v", out)
	}
	var z tencent.Zone
	for _, x := range f.Zones {
		if x.ZoneName == "example.org" {
			z = x
		}
	}
	if z.Status != "active" || z.Area != "mainland" || out.Undo["txt_record_id"] == "" {
		t.Fatalf("zone %+v undo %v", z, out.Undo)
	}
	if again := Apply(ctx, env, create, nil); again.Status != StatusDone || len(again.Undo) != 0 {
		t.Fatalf("creating again should change nothing: %+v", again)
	}
	// No plan left that can take another site.
	other := mustResolve(t, "eo.zone.create", map[string]any{"domain": "example.net", "area": "overseas"})
	if o := Apply(ctx, env, other, nil); o.Status != StatusRefused || !strings.Contains(strings.Join(o.Log, ""), "套餐") {
		t.Fatalf("without a plan: %+v", o)
	}

	// Undo is refused while the site has acceleration domains.
	add := mustResolve(t, "eo.domain.add", map[string]any{"domain": "www.example.org", "origin": "81.68.79.253"})
	added := Apply(ctx, env, add, nil)
	if u := Undo(ctx, env, create, out.Undo); u.Status != StatusFailed || !strings.Contains(strings.Join(u.Log, ""), "www.example.org") {
		t.Fatalf("undo with domains should fail: %+v", u)
	}
	if u := Undo(ctx, env, add, added.Undo); u.Status != StatusUndone {
		t.Fatal(u)
	}
	if u := Undo(ctx, env, create, out.Undo); u.Status != StatusUndone {
		t.Fatalf("undo create: %+v", u)
	}
	for _, x := range f.Zones {
		if x.ZoneName == "example.org" {
			t.Fatal("zone still there")
		}
	}
	if len(f.Records["example.org"]) != 0 {
		t.Fatalf("verification record left: %v", f.Records["example.org"])
	}
}
