package actions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun"
	"github.com/bocmiao/CloudConsoleWithAI/internal/aliyun/aliyuntest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent"
)

// A running server is shut down, rolled back and started again; one that
// was off stays off.
func TestCloudRollback(t *testing.T) {
	env, f := cloudEnv(t)
	ctx := context.Background()
	cvm := f.Instances["ins-xyz98765"]
	f.Snaps["lhsnap-old1"] = &tencent.Snapshot{ID: "lhsnap-old1", Name: "升级前|lhins-abc12345", State: "NORMAL"}
	f.Snaps["snap-old2"] = &tencent.Snapshot{ID: "snap-old2", Name: "上线前|" + cvm.DiskID, State: "NORMAL"}

	r := mustResolve(t, "cloud.snapshot.rollback", map[string]any{"instance": "lhins-abc12345", "region": "ap-guangzhou", "snapshot": "lhsnap-old1"})
	out := Apply(ctx, env, r, nil)
	if out.Status != StatusDone || f.Instances["lhins-abc12345"].State != "RUNNING" || len(f.Rollbacks) != 1 || f.Rollbacks[0] != "lhins-abc12345 lhsnap-old1" {
		t.Fatalf("lighthouse: %+v %s %v", out, f.Instances["lhins-abc12345"].State, f.Rollbacks)
	}
	if log := strings.Join(out.Log, "\n"); !strings.Contains(log, "正在关机") || !strings.Contains(log, "已重新开机") || !strings.Contains(log, "升级前（lhsnap-old1）") {
		t.Errorf("log:\n%s", log)
	}

	// A CVM that is off: rolled back, left off.
	cvm.State = "STOPPED"
	r = mustResolve(t, "cloud.snapshot.rollback", map[string]any{"instance": "ins-xyz98765", "region": "ap-guangzhou", "snapshot": "snap-old2"})
	if out := Apply(ctx, env, r, nil); out.Status != StatusDone || cvm.State != "STOPPED" || f.Rollbacks[1] != "ins-xyz98765 snap-old2" {
		t.Fatalf("cvm: %+v %s %v", out, cvm.State, f.Rollbacks)
	}

	// Another server's snapshot, or one still being made, is refused.
	for _, c := range []struct{ inst, snap string }{{"lhins-abc12345", "snap-old2"}, {"lhins-abc12345", "lhsnap-nope"}} {
		r := mustResolve(t, "cloud.snapshot.rollback", map[string]any{"instance": c.inst, "region": "ap-guangzhou", "snapshot": c.snap})
		if out := Apply(ctx, env, r, nil); out.Status != StatusRefused {
			t.Errorf("%v: %+v", c, out)
		}
	}
	f.Snaps["lhsnap-new"] = &tencent.Snapshot{ID: "lhsnap-new", Name: "x|lhins-abc12345", State: "CREATING"}
	r = mustResolve(t, "cloud.snapshot.rollback", map[string]any{"instance": "lhins-abc12345", "region": "ap-guangzhou", "snapshot": "lhsnap-new"})
	if out := Apply(ctx, env, r, nil); out.Status != StatusRefused || len(f.Rollbacks) != 2 {
		t.Errorf("unfinished snapshot: %+v", out)
	}
	if r.Cap.Reversible || r.Cap.NoUndo == "" {
		t.Error("a rollback cannot be undone and should say so")
	}
}

func TestAliyunRollback(t *testing.T) {
	env, f := aliEnv(t)
	ctx := context.Background()
	shop := f.ECS[aliECS]
	f.Snapshots = append(f.Snapshots,
		&aliyuntest.Snap{ID: "s-bp1before", Name: "before", DiskID: shop.DiskID, Instance: aliECS, Region: "cn-hangzhou", Kind: "ecs", Created: time.Now(), SizeGB: 40},
		&aliyuntest.Snap{ID: "s-bp1swasold", Name: "old", DiskID: f.SWAS[aliSWAS].DiskID, Instance: aliSWAS, Region: "cn-hangzhou", Kind: "swas", Created: time.Now()})
	// Still being made the first time it is looked at: refused.
	aliRun(t, env, "aliyun.snapshot.rollback", map[string]any{"instance": aliECS, "region": "cn-hangzhou", "snapshot": "s-bp1before"}, StatusRefused)

	_, out := aliRun(t, env, "aliyun.snapshot.rollback", map[string]any{"instance": aliECS, "region": "cn-hangzhou", "snapshot": "s-bp1before"}, StatusDone)
	if shop.Status != "Running" || len(f.Resets) != 1 || f.Resets[0] != aliECS+" s-bp1before" {
		t.Fatalf("ecs: %s %v\n%s", shop.Status, f.Resets, strings.Join(out.Log, "\n"))
	}
	var sw aliyun.Server
	if s, ok, err := env.Aliyun.Instance(ctx, "cn-hangzhou", aliSWAS); err != nil || !ok {
		t.Fatal(err)
	} else {
		sw = s
	}
	if _, err := env.Aliyun.Snapshots(ctx, sw); err != nil {
		t.Fatal(err)
	}
	aliRun(t, env, "aliyun.snapshot.rollback", map[string]any{"instance": aliSWAS, "region": "cn-hangzhou", "snapshot": "s-bp1swasold"}, StatusDone)
	if len(f.Resets) != 2 || f.SWAS[aliSWAS].Status != "Running" {
		t.Fatalf("swas: %v %s", f.Resets, f.SWAS[aliSWAS].Status)
	}
	// Another disk's snapshot.
	aliRun(t, env, "aliyun.snapshot.rollback", map[string]any{"instance": aliECS, "region": "cn-hangzhou", "snapshot": "s-bp1swasold"}, StatusRefused)
}
