package actions

import (
	"context"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
)

const (
	aliECS  = "i-bp1shop000000000001"
	aliSWAS = "2ad1ae67295445f598017499dc000001"
)

func aliEnv(t *testing.T) (*Env, *aliyuntest.Cloud) {
	f := aliyuntest.StartCloud(t)
	return &Env{Aliyun: f.Client(), PollInterval: 1}, f
}

func aliRun(t *testing.T, env *Env, capability string, params map[string]any, want string) (Resolved, Outcome) {
	t.Helper()
	r, err := Resolve(capability, params, "")
	if err != nil {
		t.Fatalf("%s %v: %v", capability, params, err)
	}
	out := Apply(context.Background(), env, r, nil)
	if out.Status != want {
		t.Fatalf("%s %v: status %s, want %s\n%s", capability, params, out.Status, want, strings.Join(out.Log, "\n"))
	}
	return r, out
}

func TestAliyunPower(t *testing.T) {
	env, _ := aliEnv(t)
	ctx := context.Background()
	on := map[string]any{"instance": aliECS, "region": "cn-hangzhou"}

	r, out := aliRun(t, env, "aliyun.server.stop", on, StatusDone)
	if s, _, _ := env.Aliyun.Instance(ctx, "cn-hangzhou", aliECS); s.State != "STOPPED" {
		t.Fatalf("state after stop = %s", s.State)
	}
	if len(out.Commands) < 2 || !strings.Contains(strings.Join(out.Commands, "\n"), "StopInstance") {
		t.Errorf("commands = %v", out.Commands)
	}
	if u := Undo(ctx, env, r, out.Undo); u.Status != StatusUndone {
		t.Fatalf("undo stop: %+v", u)
	}
	aliRun(t, env, "aliyun.server.start", on, StatusDone) // already running again: nothing to do
	aliRun(t, env, "aliyun.server.reboot", map[string]any{"instance": aliSWAS, "region": "cn-hangzhou"}, StatusDone)
	aliRun(t, env, "aliyun.server.start", map[string]any{"instance": "i-2zedb000000000000002", "region": "cn-beijing"}, StatusDone)
	aliRun(t, env, "aliyun.server.stop", map[string]any{"instance": "i-nothere000000000", "region": "cn-hangzhou"}, StatusRefused)

	if _, err := Resolve("aliyun.server.stop", map[string]any{"instance": "lhins-abc12345", "region": "cn-hangzhou"}, ""); err == nil {
		t.Error("a Tencent instance id was accepted")
	}
	env.Aliyun = nil
	aliRun(t, env, "aliyun.server.stop", on, StatusRefused)
}

func TestAliyunSnapshot(t *testing.T) {
	env, _ := aliEnv(t)
	for _, id := range []string{aliECS, aliSWAS} {
		_, out := aliRun(t, env, "aliyun.snapshot.create", map[string]any{"instance": id, "region": "cn-hangzhou", "name": "before-upgrade"}, StatusDone)
		if !strings.Contains(strings.Join(out.Log, "\n"), "before-upgrade") {
			t.Errorf("%s log: %v", id, out.Log)
		}
	}
}

func TestAliyunFirewall(t *testing.T) {
	env, _ := aliEnv(t)
	ctx := context.Background()
	for _, id := range []string{aliECS, aliSWAS} {
		p := map[string]any{"instance": id, "region": "cn-hangzhou", "port": "8080"}
		r, out := aliRun(t, env, "aliyun.firewall.open", p, StatusDone)
		s, _, _ := env.Aliyun.Instance(ctx, "cn-hangzhou", id)
		has := func() bool {
			rules, err := env.Aliyun.Firewall(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			for _, x := range rules {
				if x.Port == "8080" && x.Source == "0.0.0.0/0" {
					return true
				}
			}
			return false
		}
		if !has() {
			t.Fatalf("%s: 8080 not open", id)
		}
		if _, again := aliRun(t, env, "aliyun.firewall.open", p, StatusDone); len(again.Undo) != 0 {
			t.Errorf("%s: opening twice changed something", id)
		}
		if u := Undo(ctx, env, r, out.Undo); u.Status != StatusUndone || has() {
			t.Fatalf("%s: undo open: %+v", id, u)
		}

		aliRun(t, env, "aliyun.firewall.open", p, StatusDone)
		r, out = aliRun(t, env, "aliyun.firewall.close", p, StatusDone)
		if has() {
			t.Fatalf("%s: 8080 still open", id)
		}
		if u := Undo(ctx, env, r, out.Undo); u.Status != StatusUndone || !has() {
			t.Fatalf("%s: undo close: %+v", id, u)
		}
		aliRun(t, env, "aliyun.firewall.close", map[string]any{"instance": id, "region": "cn-hangzhou", "port": "22"}, StatusRefused)
	}
	if _, err := Resolve("aliyun.firewall.open", map[string]any{"instance": aliECS, "region": "cn-hangzhou", "port": "80,443"}, ""); err == nil {
		t.Error("a port list was accepted")
	}
	r, _ := Resolve("aliyun.firewall.open", map[string]any{"instance": aliECS, "region": "cn-hangzhou", "port": "3306"}, "")
	if r.Cap.RiskFor(r.Values) != core.R3 {
		t.Error("opening MySQL to everyone is not high risk")
	}
}
