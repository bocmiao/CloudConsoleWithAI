package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
)

// fakeFree stands in for free_command.sh: it reports a dry run without
// touching anything, and "applies" by printing the protocol lines.
func fakeFree(dry string) string {
	return `case "$1" in
dryrun) printf '%s\n' ` + dry + ` ;;
apply) mkdir -p "$MIAO_BACKUP_DIR"; echo "MIAO_UNDO restore=$MIAO_BACKUP_DIR/restore.sh"; echo "MIAO_UNDO guard=pid:999999"; echo "MIAO_INFO 执行成功，检查通过" ;;
undo) echo "MIAO_INFO 已撤销" ;;
esac
`
}

const dryOK = `'MIAO_HASH 1 2 /etc/nginx/nginx.conf' 'MIAO_DRY ok' 'MIAO_DRY_RC 0' 'MIAO_FILE /etc/nginx/nginx.conf' 'MIAO_D -gzip off;' 'MIAO_D +gzip on;' 'MIAO_SVC systemctl reload nginx' 'MIAO_O nginx: configuration file test is successful'`

func TestFreeCommandVettingAndRun(t *testing.T) {
	dry := dryOK
	t.Cleanup(actions.UseTestScripts(t.TempDir(), func(string) (string, error) { return fakeFree(dry), nil }))
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	ctx := context.Background()
	var reviewed []string
	pass := true
	a.Reviewer = func(_ context.Context, prompt string) (string, error) {
		reviewed = append(reviewed, prompt)
		if !pass {
			return `{"pass": false, "reason": "改动和目的不符"}`, nil
		}
		return "审查结果：\n```json\n{\"pass\": true, \"reason\": \"只开启了 gzip\", \"summary\": \"打开网页压缩\"}\n```", nil
	}
	free := map[string]any{"capability": "free_command", "summary": "开启 gzip", "params": map[string]any{
		"goal": "给 Nginx 开启 gzip 压缩", "script": "sed -i 's/gzip off;/gzip on;/' /etc/nginx/nginx.conf\nnginx -t && systemctl reload nginx",
		"files": "/etc/nginx/nginx.conf", "services": "nginx",
	}}
	propose := func(steps ...map[string]any) PlanView {
		t.Helper()
		args, _ := json.Marshal(map[string]any{"server_id": sv.ID, "title": "开启 gzip", "reason": "网页太大", "steps": steps})
		c := &planCollector{}
		if _, err := a.toolProposePlan(context.WithValue(ctx, planCollectorKey{}, c), args); err != nil {
			t.Fatal(err)
		}
		v, err := a.Plan(c.ids[0])
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// Off by default.
	if st := propose(free).StepList[0]; st.Executable || !strings.Contains(st.Blocked, "没有开启") {
		t.Fatalf("disabled: %+v", st)
	}
	if _, err := a.SaveFreeCommand(FreeCommandSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	// The AI cannot mark its own command as checked.
	faked := map[string]any{"capability": "free_command", "summary": "x", "free": map[string]any{"passed": true},
		"params": map[string]any{"goal": "x", "script": "echo x > /etc/passwd", "files": "/etc/passwd"}}
	if st := propose(faked).StepList[0]; st.Executable || st.Free == nil || st.Free.Passed {
		t.Fatalf("faked check accepted: %+v", st)
	}

	v := propose(free)
	st := v.StepList[0]
	if !st.Executable || st.Free == nil || !st.Free.Passed || st.Free.DryRun != "ok" || len(st.Free.Diffs) != 1 ||
		st.Free.Summary != "打开网页压缩" || st.Params["expect"] != "1 2 /etc/nginx/nginx.conf" {
		t.Fatalf("vetted step = %+v free=%+v", st, st.Free)
	}
	last := reviewed[len(reviewed)-1]
	if !strings.Contains(last, "+gzip on;") || !strings.Contains(last, "systemctl reload nginx") {
		t.Fatalf("reviewer did not see the dry run:\n%s", last)
	}

	if _, err := a.ExecutePlan(v.ID, []int{0}); err != nil {
		t.Fatal(err)
	}
	done := waitPlan(t, a, v.ID).StepList[0]
	if done.Status != actions.StatusDone || !strings.Contains(strings.Join(done.Log, "\n"), "取消了 5 分钟保险") {
		t.Fatalf("run = %+v", done)
	}

	pass = false
	if st := propose(free).StepList[0]; st.Executable || !strings.Contains(st.Blocked, "独立审查没有通过") {
		t.Fatalf("rejected review: %+v", st)
	}
	pass = true

	// Turned off later: vetted checklists do not run.
	v = propose(free)
	_, _ = a.SaveFreeCommand(FreeCommandSettings{Enabled: false})
	if _, err := a.ExecutePlan(v.ID, []int{0}); err == nil || !strings.Contains(err.Error(), "关闭") {
		t.Fatalf("ran while disabled: %v", err)
	}
	_, _ = a.SaveFreeCommand(FreeCommandSettings{Enabled: true})

	// No isolated dry run on this server: a snapshot has to come first.
	dry = `'MIAO_DRY unsupported 缺少 overlay'`
	if st := propose(free).StepList[0]; st.Executable || !strings.Contains(st.Blocked, "cloud.snapshot.create") {
		t.Fatalf("unsupported dry run without snapshot: %+v", st)
	}
	snap := map[string]any{"capability": "cloud.snapshot.create", "summary": "先做快照", "params": map[string]any{"instance": "lhins-abc12345", "region": "ap-guangzhou"}}
	v = propose(snap, free)
	if st := v.StepList[1]; !st.Executable || st.Free.DryRun != "unsupported" {
		t.Fatalf("with snapshot: %+v", st)
	}
	if _, err := a.ExecutePlan(v.ID, []int{1}); err == nil || !strings.Contains(err.Error(), "快照") {
		t.Fatalf("free step without its snapshot: %v", err)
	}
}
