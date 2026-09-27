package actions

import (
	"context"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

func TestFirewallTightenAndUndo(t *testing.T) {
	env, f := cloudEnv(t)
	f.Firewall["sg-abc"] = []tencent.FirewallRule{
		{Protocol: "ALL", Port: "ALL", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"},
		{Protocol: "TCP", Port: "22", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"},
		{Protocol: "TCP", Port: "3306", CidrBlock: "0.0.0.0/0", Action: "ACCEPT"},
	}
	r := mustResolve(t, "cloud.firewall.tighten", map[string]any{"instance": "ins-xyz98765", "region": "ap-guangzhou", "group": "sg-abc", "admin_cidr": "203.0.113.4/32", "ssh_port": 22})
	out := Apply(context.Background(), env, r, nil)
	if out.Status != StatusDone {
		t.Fatalf("apply: %+v", out)
	}
	for _, rule := range f.Firewall["sg-abc"] {
		if publicSensitiveRule(rule, 22) {
			t.Fatalf("sensitive port remains public: %+v", rule)
		}
	}
	if u := Undo(context.Background(), env, r, out.Undo); u.Status != StatusUndone {
		t.Fatalf("undo: %+v", u)
	}
	if len(f.Firewall["sg-abc"]) != 3 {
		t.Fatalf("undo did not restore original rules: %+v", f.Firewall["sg-abc"])
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
