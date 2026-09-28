package actions

import (
	"context"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

func TestFirewallTightenAndUndo(t *testing.T) {
	env, f := cloudEnv(t)
	f.Firewall["sg-abc"] = []tencent.FirewallRule{
		{Protocol: "TCP", Port: "443", CidrBlock: "0.0.0.0/0", Action: "DROP"},
		{Protocol: "ALL", Port: "ALL", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"},
		{Protocol: "TCP", Port: "22", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"},
		{Protocol: "TCP", Port: "3306", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"},
	}
	r := mustResolve(t, "cloud.firewall.tighten", map[string]any{"instance": "ins-xyz98765", "region": "ap-guangzhou", "group": "sg-abc", "admin_cidr": "203.0.113.4/32", "ssh_port": 22})
	out := Apply(context.Background(), env, r, nil)
	if out.Status != StatusDone {
		t.Fatalf("apply: %+v", out)
	}
	if first := f.Firewall["sg-abc"][0]; first.Port != "443" || first.Action != "DROP" {
		t.Fatalf("existing deny lost priority: %+v", f.Firewall["sg-abc"])
	}
	for _, rule := range f.Firewall["sg-abc"] {
		if publicSensitiveRule(rule, 22) {
			t.Fatalf("sensitive port remains public: %+v", rule)
		}
	}
	if u := Undo(context.Background(), env, r, out.Undo); u.Status != StatusUndone {
		t.Fatalf("undo: %+v", u)
	}
	if len(f.Firewall["sg-abc"]) != 4 || f.Firewall["sg-abc"][0].Action != "DROP" || f.Firewall["sg-abc"][1].Port != "ALL" {
		t.Fatalf("undo did not restore original rules: %+v", f.Firewall["sg-abc"])
	}
}

func TestFirewallTightenPreservesTopRulePosition(t *testing.T) {
	env, f := cloudEnv(t)
	f.Firewall["sg-abc"] = []tencent.FirewallRule{
		{Protocol: "ALL", Port: "ALL", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"},
		{Protocol: "TCP", Port: "80", CidrBlock: "0.0.0.0/0", Action: "DROP"},
	}
	r := mustResolve(t, "cloud.firewall.tighten", map[string]any{"instance": "ins-xyz98765", "region": "ap-guangzhou", "group": "sg-abc", "admin_cidr": "203.0.113.4/32", "ssh_port": 22})
	if out := Apply(context.Background(), env, r, nil); out.Status != StatusDone {
		t.Fatalf("apply: %+v", out)
	}
	for i, rule := range f.Firewall["sg-abc"] {
		if rule.Port == "80" && rule.Action == "ACCEPT" {
			for _, earlier := range f.Firewall["sg-abc"][:i] {
				if earlier.Port == "80" && earlier.Action == "DROP" {
					t.Fatalf("new 80 rule appended after deny: %+v", f.Firewall["sg-abc"])
				}
			}
			return
		}
	}
	t.Fatal("new 80 rule missing")
}

func TestFirewallTightenRefusesOtherPublicSources(t *testing.T) {
	base := tencent.FirewallRule{Protocol: "ALL", Port: "ALL", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"}
	for _, source := range []string{"0.0.0.0/1", "128.0.0.0/1", "203.0.113.9/32"} {
		rules := []tencent.FirewallRule{base, {Protocol: "TCP", Port: "3306", CidrBlock: source, Action: "ACCEPT"}}
		if _, err := FirewallTightenRules(rules, 22, "203.0.113.4/32"); err == nil {
			t.Errorf("public MySQL source %s accepted", source)
		}
	}
	rules := []tencent.FirewallRule{base, {Protocol: "TCP", Port: "3306", CidrBlock: "10.0.0.0/8", Action: "ACCEPT"}}
	if _, err := FirewallTightenRules(rules, 22, "203.0.113.4/32"); err != nil {
		t.Fatalf("private MySQL source rejected: %v", err)
	}
}

func TestSnapshotRequiresReadyState(t *testing.T) {
	env, f := cloudEnv(t)
	f.FailAction = "cbs DescribeSnapshots"
	r := mustResolve(t, "cloud.snapshot.create", map[string]any{"instance": "ins-xyz98765", "region": "ap-guangzhou"})
	if out := Apply(context.Background(), env, r, nil); out.Status != StatusFailed {
		t.Fatalf("unverified snapshot marked successful: %+v", out)
	}
}

func TestFirewallTightenRefusesWideExtraRule(t *testing.T) {
	env, f := cloudEnv(t)
	f.Firewall["sg-abc"] = []tencent.FirewallRule{
		{Protocol: "ALL", Port: "ALL", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"},
		{Protocol: "TCP", Port: "3000-4000", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"},
	}
	r := mustResolve(t, "cloud.firewall.tighten", map[string]any{"instance": "ins-xyz98765", "region": "ap-guangzhou", "group": "sg-abc", "admin_cidr": "203.0.113.4/32", "ssh_port": 22})
	if out := Apply(context.Background(), env, r, nil); out.Status != StatusRefused {
		t.Fatalf("expected refusal: %+v", out)
	}
	if len(f.Firewall["sg-abc"]) != 2 {
		t.Fatalf("rules changed: %+v", f.Firewall["sg-abc"])
	}
}

func TestSSHHardenRequiresKeyConnection(t *testing.T) {
	r := mustResolve(t, "ssh.harden", map[string]any{"login_user": "admin"})
	for _, env := range []*Env{{User: "root", AuthKind: "key"}, {User: "admin", AuthKind: "password"}, {User: "other", AuthKind: "key"}} {
		if out := Apply(context.Background(), env, r, nil); out.Status != StatusRefused {
			t.Fatalf("unsafe SSH execution accepted: %+v", out)
		}
	}
}
