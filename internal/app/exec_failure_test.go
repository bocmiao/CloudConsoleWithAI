package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

func TestFinishActionInvalidatesCachesOnLogFailure(t *testing.T) {
	for _, capability := range []string{"cloud.renew.set", "aliyun.renew.set"} {
		t.Run(capability, func(t *testing.T) {
			a := newApp(t)
			e, err := a.startChange(store.ExecLog{Kind: store.ExecChange, Capability: capability})
			if err != nil {
				t.Fatal(err)
			}
			before := a.pages.generation()
			if err := a.Store.Close(); err != nil {
				t.Fatal(err)
			}
			if err := a.finishAction(&e, actions.Outcome{Status: actions.StatusDone}); err == nil {
				t.Fatal("lost execution result was not reported")
			}
			if a.pages.generation() == before {
				t.Fatal("pages still show pre-change state")
			}
			if capability == "cloud.renew.set" && !a.cloud.stale.Load() {
				t.Fatal("Tencent list not invalidated")
			}
			if capability == "aliyun.renew.set" && !a.aliCloud.stale.Load() {
				t.Fatal("Aliyun list not invalidated")
			}
		})
	}
}

func TestStepRollbackAfterExecutionLogUpdateFails(t *testing.T) {
	// Reject only outcome updates. The actual operation and plan persistence
	// still work, reproducing a partial database failure rather than a closed DB.
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db, err := sql.Open("sqlite", filepath.Join(dir, store.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TRIGGER reject_change_outcome BEFORE UPDATE ON exec_logs
		WHEN OLD.kind = 'change' BEGIN SELECT RAISE(FAIL, 'injected outcome failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	a := New(st, secrets.OpenFile(t.TempDir()))
	t.Cleanup(actions.UseTestScripts(t.TempDir(), func(string) (string, error) { return fakeSwap, nil }))
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	ctx := context.Background()
	if _, _, err := a.Discover(ctx, sv.ID, nil); err != nil {
		t.Fatal(err)
	}
	step := map[string]any{"capability": "swap.set", "summary": "test", "params": map[string]any{"size_gb": 1}}
	args, _ := json.Marshal(map[string]any{"server_id": sv.ID, "title": "test", "reason": "test", "steps": []map[string]any{step, step}})
	collector := &planCollector{}
	if _, err := a.toolProposePlan(context.WithValue(ctx, planCollectorKey{}, collector), args); err != nil {
		t.Fatal(err)
	}
	id := collector.ids[0]
	if _, err := a.ExecutePlan(id, []int{0, 1}); err != nil {
		t.Fatal(err)
	}
	v := waitPlan(t, a, id)
	first := v.StepList[0]
	if v.Status != core.PlanPartial || first.Status != actions.StatusDone || first.LogID != 0 || first.RollbackFile == "" || first.BackupDir == "" || v.StepList[1].Status != "skipped" {
		t.Fatalf("incomplete fallback data or continued execution: %+v", v)
	}
	if _, err := os.Stat(first.RollbackFile); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UndoStep(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.RollbackFile + ".done"); err != nil {
		t.Fatalf("old rollback file not retired: %v", err)
	}
	if _, err := a.UndoStep(ctx, id, 0); err == nil {
		t.Fatal("step rolled back twice")
	}
}

func TestPrepareStepsRejectsSuppliedRecoveryPaths(t *testing.T) {
	steps := prepareSteps([]core.Step{{Capability: "swap.set", Params: map[string]any{"size_gb": 1}, BackupDir: "/untrusted", RollbackFile: "/untrusted/file"}}, "linux")
	if steps[0].BackupDir != "" || steps[0].RollbackFile != "" {
		t.Fatal("plan input supplied trusted recovery paths")
	}
}
