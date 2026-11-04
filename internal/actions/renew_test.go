package actions

import (
	"context"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

func TestCloudRenew(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()
	for _, id := range []string{"lhins-abc12345", "ins-xyz98765"} {
		on := mustResolve(t, "cloud.renew.set", map[string]any{"instance": id, "region": "ap-guangzhou", "auto": "on"})
		out := Apply(ctx, env, on, nil)
		if out.Status != StatusDone || f.Instances[id].RenewFlag != tencent.RenewAuto {
			t.Fatalf("%s on: %+v flag=%s", id, out, f.Instances[id].RenewFlag)
		}
		// Asked again, nothing to change.
		if again := Apply(ctx, env, on, nil); again.Status != StatusDone || len(again.Undo) != 0 {
			t.Errorf("%s again: %+v", id, again)
		}
		if u := Undo(ctx, env, on, out.Undo); u.Status != StatusUndone || f.Instances[id].RenewFlag != tencent.RenewManual {
			t.Fatalf("%s undo: %+v flag=%s", id, u, f.Instances[id].RenewFlag)
		}
	}
	if _, err := Resolve("cloud.renew.set", map[string]any{"instance": "lhins-abc12345", "region": "ap-guangzhou", "auto": "maybe"}, ""); err == nil {
		t.Error("a wrong auto was accepted")
	}
}

func TestAliyunRenew(t *testing.T) {
	env, f := aliEnv(t)
	ctx := context.Background()
	r, out := aliRun(t, env, "aliyun.renew.set", map[string]any{"instance": aliECS, "region": "cn-hangzhou", "auto": "off"}, StatusDone)
	if f.ECS[aliECS].AutoRenew {
		t.Fatal("still renews")
	}
	if u := Undo(ctx, env, r, out.Undo); u.Status != StatusUndone || !f.ECS[aliECS].AutoRenew {
		t.Fatalf("undo: %+v renew=%v", u, f.ECS[aliECS].AutoRenew)
	}
	// Pay-as-you-go servers and Simple Application Servers are refused.
	aliRun(t, env, "aliyun.renew.set", map[string]any{"instance": "i-2zedb000000000000002", "region": "cn-beijing", "auto": "on"}, StatusRefused)
	aliRun(t, env, "aliyun.renew.set", map[string]any{"instance": aliSWAS, "region": "cn-hangzhou", "auto": "on"}, StatusRefused)
}
