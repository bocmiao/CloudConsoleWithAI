package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/actions"
	"github.com/bocmiao/CloudConsoleWithAI/internal/core"
	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
	"github.com/bocmiao/CloudConsoleWithAI/internal/tencent/tencenttest"
)

func newApp(t *testing.T) *App {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, secrets.OpenFile(t.TempDir()))
}

func addTestServer(t *testing.T, a *App, srv *sshtest.Server, password string) store.Server {
	t.Helper()
	sv, err := a.AddServer(AddServerRequest{
		Name: "blog", Host: srv.Host, Port: srv.Port, Username: "root", AuthKind: "password", Password: password,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sv
}

func TestServerLifecycle(t *testing.T) {
	a := newApp(t)
	srv := sshtest.Start(t, "root", "s3cret")
	sv := addTestServer(t, a, srv, "s3cret")
	ctx := context.Background()

	// The password lives in the secret store, not in the database.
	if got, _ := a.Secrets.Get(secretKey(sv.ID, "password")); got != "s3cret" {
		t.Fatalf("stored password = %q", got)
	}

	res, err := a.TestConnection(ctx, sv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.HostKey != srv.HostKey || res.Output == "" {
		t.Fatalf("test result = %+v", res)
	}
	got, _ := a.Store.GetServer(sv.ID)
	if got.HostKey != srv.HostKey {
		t.Fatalf("host key not pinned: %q", got.HostKey)
	}

	raw, prof, err := a.Discover(ctx, sv.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "== system ==") || prof.Adapter == "" {
		t.Fatalf("discover: adapter=%q raw=%q", prof.Adapter, raw[:min(len(raw), 200)])
	}
	view, err := a.Profile(sv.ID)
	if err != nil || view.Profile == nil || view.CollectedAt == "" {
		t.Fatalf("profile view = %+v, err = %v", view, err)
	}

	if _, _, err := a.Discover(ctx, sv.ID, []string{"rm -rf /"}); err == nil {
		t.Fatal("invalid section accepted")
	}

	if err := a.DeleteServer(sv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Secrets.Get(secretKey(sv.ID, "password")); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("password not deleted: %v", err)
	}
}

func TestHostKeyChangeIsRefused(t *testing.T) {
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	if err := a.Store.SetHostKey(sv.ID, "SHA256:recorded-earlier"); err != nil {
		t.Fatal(err)
	}
	_, err := a.TestConnection(context.Background(), sv.ID)
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "指纹") {
		t.Fatalf("err = %v, want host key warning", err)
	}
}

func TestAddServerValidation(t *testing.T) {
	a := newApp(t)
	for _, req := range []AddServerRequest{
		{Host: "", AuthKind: "password", Password: "x"},
		{Host: "1.2.3.4; rm -rf /", AuthKind: "password", Password: "x"},
		{Host: "1.2.3.4", Port: 70000, AuthKind: "password", Password: "x"},
		{Host: "1.2.3.4", AuthKind: "password"},
		{Host: "1.2.3.4", AuthKind: "magic"},
	} {
		if _, err := a.AddServer(req); err == nil {
			t.Errorf("accepted invalid request %+v", req)
		}
	}
}

// fakeModel is an OpenAI-compatible endpoint that asks for list_servers,
// then run_check, then answers.
type fakeModel struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, _ = io.Copy(io.Discard, r.Body)
	f.calls++
	var body string
	switch f.calls {
	case 1:
		body = `{"choices":[{"message":{"content":"","tool_calls":[{"id":"a","type":"function","function":{"name":"list_servers","arguments":"{}"}}]}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`
	case 2:
		body = `{"choices":[{"message":{"content":"","tool_calls":[{"id":"b","type":"function","function":{"name":"run_check","arguments":"{\"server_id\":1,\"checks\":[\"system\"]}"}}]}}],"usage":{"prompt_tokens":200,"completion_tokens":10}}`
	default:
		body = `{"choices":[{"message":{"content":"结论：内存正常。"}}],"usage":{"prompt_tokens":300,"completion_tokens":20}}`
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

func TestChatUsesToolsAndRecordsCost(t *testing.T) {
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	addTestServer(t, a, srv, "pw")
	model := httptest.NewServer(&fakeModel{})
	defer model.Close()

	if _, err := a.Chat(context.Background(), "", "你好"); err == nil {
		t.Fatal("chat without an API key should fail")
	}
	s, err := a.AISettings()
	if err != nil {
		t.Fatal(err)
	}
	s.BaseURL = "http://example.com/v1"
	if _, err := a.SaveAISettings(s, "sk-test"); err == nil {
		t.Fatal("plain http to a remote host should be rejected")
	}
	s.BaseURL = model.URL // http://127.0.0.1:port is allowed for local models
	if _, err := a.SaveAISettings(s, "sk-test"); err != nil {
		t.Fatal(err)
	}

	r, err := a.Chat(context.Background(), "", "我的服务器内存怎么样？")
	if err != nil {
		t.Fatal(err)
	}
	if r.Reply.Text != "结论：内存正常。" || len(r.Reply.Steps) != 2 {
		t.Fatalf("reply = %+v", r.Reply)
	}
	if !strings.Contains(r.Reply.Steps[1].Output, "== system ==") {
		t.Fatalf("run_check output = %q", r.Reply.Steps[1].Output)
	}
	if r.Cost <= 0 || r.Currency != "CNY" || r.ConversationID == "" {
		t.Fatalf("cost/currency/conv = %v %q %q", r.Cost, r.Currency, r.ConversationID)
	}
	spent, _ := a.Store.MonthCost()
	if spent["CNY"] <= 0 {
		t.Fatalf("usage not recorded: %v", spent)
	}

	s.MonthlyBudget = 0.000001
	data, _ := json.Marshal(s)
	_ = a.Store.SetSetting(aiSettingsKey, string(data))
	if _, err := a.Chat(context.Background(), "", "再问一次"); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("budget not enforced: %v", err)
	}
}

func TestProposePlanAssignsPolicyRisk(t *testing.T) {
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	args, _ := json.Marshal(map[string]any{
		"server_id": sv.ID, "title": "加 swap", "reason": "可用内存只有 150MB",
		"steps": []map[string]any{
			{"capability": "swap.set", "summary": "添加 1G swap", "risk": "R0"},
			{"capability": "server.reboot", "summary": "重启"},
		},
	})
	if _, err := a.toolProposePlan(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	plans, _ := a.Store.ListPlans(10)
	if len(plans) != 1 || !strings.Contains(plans[0].Steps, `"risk":"R2"`) || !strings.Contains(plans[0].Steps, `"risk":"R3"`) {
		t.Fatalf("plans = %+v", plans)
	}
}

func waitPlan(t *testing.T, a *App, id int64) PlanView {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		v, err := a.Plan(id)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status != core.PlanRunning {
			return v
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("plan did not finish")
	return PlanView{}
}

func TestExecutePlanFlow(t *testing.T) {
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	if _, _, err := a.Discover(context.Background(), sv.ID, nil); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"server_id": sv.ID, "title": "清理磁盘", "reason": "磁盘快满了",
		"steps": []map[string]any{
			{"capability": "logs.clean", "summary": "清理旧日志"},
			{"capability": "mysql.vars.set", "summary": "改 MySQL", "params": map[string]any{"max_connections": 200}},
			{"capability": "swap.set", "summary": "加 swap", "params": map[string]any{"size_gb": 99}},
		},
	})
	collector := &planCollector{}
	msg, err := a.toolProposePlan(context.WithValue(context.Background(), planCollectorKey{}, collector), args)
	if err != nil {
		t.Fatal(err)
	}
	if len(collector.ids) != 1 || !strings.Contains(msg, "1. logs.clean：可以自动执行") || !strings.Contains(msg, "不能自动执行") {
		t.Fatalf("propose result: %s", msg)
	}
	v, _ := a.Plan(collector.ids[0])
	if !v.StepList[0].Executable || v.StepList[1].Executable || v.StepList[2].Executable {
		t.Fatalf("executable flags: %+v", v.StepList)
	}
	if v.StepList[2].Blocked == "" || v.StepList[0].Via != "系统脚本" {
		t.Fatalf("step details: %+v", v.StepList)
	}

	for _, bad := range [][]int{{}, {7}, {0, 0}, {1}} {
		if _, err := a.ExecutePlan(v.ID, bad); err == nil {
			t.Errorf("ExecutePlan(%v) should fail", bad)
		}
	}

	if _, err := a.ExecutePlan(v.ID, []int{0}); err != nil {
		t.Fatal(err)
	}
	done := waitPlan(t, a, v.ID)
	st := done.StepList[0]
	// As root the logs are cleaned; as an ordinary user the script refuses
	// and changes nothing. Either way the run must finish cleanly.
	if st.Status != "done" && st.Status != "refused" {
		t.Fatalf("step status = %q, log = %v", st.Status, st.Log)
	}
	if len(st.Log) == 0 || done.Before == nil || done.After == nil {
		t.Fatalf("missing log or snapshots: %+v", done)
	}
	if done.StepList[1].Status != "" || done.StepList[2].Status != "" {
		t.Fatalf("unselected steps were touched: %+v", done.StepList)
	}
	if _, err := a.UndoStep(context.Background(), v.ID, 0); err == nil {
		t.Fatal("log cleaning cannot be undone")
	}
	entries, _ := a.Store.ListAudit(50)
	var sawExecute bool
	for _, e := range entries {
		sawExecute = sawExecute || e.Action == "plan.execute"
	}
	if !sawExecute {
		t.Fatal("execution was not audited")
	}
}

// fakeSwap stands in for swap.sh so tests change nothing on the machine.
const fakeSwap = `
case "$1" in
apply) echo "MIAO_INFO 已添加 swap"; echo "MIAO_UNDO swapfile=/swapfile"; exit 0 ;;
undo) echo "MIAO_INFO 已撤销：swap 已移除 $UNDO_swapfile"; exit 0 ;;
esac
`

func TestExecLogAndRollback(t *testing.T) {
	t.Cleanup(actions.UseTestScripts(t.TempDir(), func(string) (string, error) { return fakeSwap, nil }))
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	ctx := context.Background()
	if _, _, err := a.Discover(ctx, sv.ID, nil); err != nil {
		t.Fatal(err)
	}
	// A check the AI runs is logged as the AI's, with the exact command.
	if _, _, err := a.Discover(withOrigin(ctx, OriginAI), sv.ID, []string{"system"}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"server_id": sv.ID, "title": "加 swap", "reason": "内存不够",
		"steps": []map[string]any{{"capability": "swap.set", "summary": "添加 1G swap", "params": map[string]any{"size_gb": 1}}},
	})
	collector := &planCollector{}
	if _, err := a.toolProposePlan(context.WithValue(ctx, planCollectorKey{}, collector), args); err != nil {
		t.Fatal(err)
	}
	planID := collector.ids[0]
	if _, err := a.ExecutePlan(planID, []int{0}); err != nil {
		t.Fatal(err)
	}
	v := waitPlan(t, a, planID)
	st := v.StepList[0]
	if st.Status != actions.StatusDone || st.LogID == 0 {
		t.Fatalf("step = %+v", st)
	}

	logs, err := a.ExecLogs(false)
	if err != nil {
		t.Fatal(err)
	}
	var sawAICheck, sawBefore bool
	for _, e := range logs {
		sawAICheck = sawAICheck || (e.Origin == OriginAI && e.Kind == store.ExecRead && e.Title == "只读检查：system")
		sawBefore = sawBefore || (e.Origin == OriginPlan && strings.HasPrefix(e.Title, "执行前识别"))
	}
	if !sawAICheck || !sawBefore {
		t.Fatalf("read-only runs missing from the log: %+v", logs)
	}
	e, err := a.ExecEntry(st.LogID)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != store.ExecChange || e.Title != "添加 swap（size_gb=1）" || e.Note != "添加 1G swap" || !e.CanRollback ||
		e.RollbackHow == "" || e.Script != fakeSwap || !strings.Contains(e.Commands, "actions.log") || !strings.Contains(e.Output, "已添加 swap") {
		t.Fatalf("change entry = %+v", e)
	}
	if e.RollbackFile == "" {
		t.Fatal("no rollback file on the server")
	}

	rolled, err := a.Rollback(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rolled.UndoneBy == 0 || rolled.CanRollback {
		t.Fatalf("after rollback = %+v", rolled)
	}
	rb, _ := a.ExecEntry(rolled.UndoneBy)
	if rb.Kind != store.ExecRollback || rb.UndoOf != e.ID || rb.Status != actions.StatusUndone || !strings.Contains(rb.Commands, "rollback.sh.done") {
		t.Fatalf("rollback entry = %+v", rb)
	}
	if p, _ := a.Plan(planID); p.StepList[0].Status != actions.StatusUndone {
		t.Fatalf("plan step not marked undone: %+v", p.StepList[0])
	}
	if _, err := a.Rollback(ctx, e.ID); err == nil {
		t.Fatal("rolled back twice")
	}
	if _, err := a.UndoPlan(ctx, planID); err == nil {
		t.Fatal("nothing left to undo, UndoPlan should say so")
	}
	if changes, _ := a.ExecLogs(true); len(changes) != 2 {
		t.Fatalf("changes only = %+v", changes)
	}
}

func TestUndoPlanRevertsEverything(t *testing.T) {
	t.Cleanup(actions.UseTestScripts(t.TempDir(), func(string) (string, error) { return fakeSwap, nil }))
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	args, _ := json.Marshal(map[string]any{
		"server_id": sv.ID, "title": "加 swap", "reason": "内存不够",
		"steps": []map[string]any{{"capability": "swap.set", "summary": "添加 swap", "params": map[string]any{"size_gb": 2}}},
	})
	collector := &planCollector{}
	if _, err := a.toolProposePlan(context.WithValue(context.Background(), planCollectorKey{}, collector), args); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExecutePlan(collector.ids[0], []int{0}); err != nil {
		t.Fatal(err)
	}
	waitPlan(t, a, collector.ids[0])
	v, err := a.UndoPlan(context.Background(), collector.ids[0])
	if err != nil || v.StepList[0].Status != actions.StatusUndone {
		t.Fatalf("undo plan: %+v %v", v.StepList, err)
	}
}

func TestInterruptedRunsAreFlagged(t *testing.T) {
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e, _ := st.AddExec(store.ExecLog{ServerID: 1, ServerName: "blog", Origin: OriginPlan, Kind: store.ExecChange, Title: "x", Status: store.ExecRunning})
	steps, _ := json.Marshal([]core.Step{{Capability: "swap.set", Status: "running"}, {Capability: "logs.clean", Status: "queued"}})
	p, _ := st.AddPlan(store.Plan{ServerID: 1, Title: "t", Steps: string(steps), Status: core.PlanRunning})

	a := New(st, secrets.OpenFile(t.TempDir()))
	if got, _ := st.GetExec(e.ID); got.Status != store.ExecInterrupted {
		t.Fatalf("exec status = %q", got.Status)
	}
	v, _ := a.Plan(p.ID)
	if v.Status != core.PlanPartial || v.StepList[0].Status != store.ExecInterrupted || v.StepList[1].Status != store.ExecInterrupted {
		t.Fatalf("plan = %+v", v)
	}
}

// A checklist saved by an older version, when a step could not run yet,
// shows what this version can do.
func TestOldPlansAreRechecked(t *testing.T) {
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	sv := addTestServer(t, a, srv, "pw")
	steps, _ := json.Marshal([]core.Step{
		{Capability: "swap.set", Summary: "加 swap", Params: map[string]any{"size_gb": float64(2)}, Blocked: "这类操作还不能自动执行"},
		{Capability: "swap.set", Summary: "参数写错了", Params: map[string]any{"size": "2G"}, Blocked: "这类操作还不能自动执行"},
		{Capability: "logs.clean", Summary: "已经执行过", Status: "done", Blocked: "旧的结果"},
	})
	p, err := a.Store.AddPlan(store.Plan{ServerID: sv.ID, Title: "旧清单", Steps: string(steps), Status: core.PlanProposed})
	if err != nil {
		t.Fatal(err)
	}
	v, err := a.Plan(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !v.StepList[0].Executable || v.StepList[0].Blocked != "" {
		t.Fatalf("step 1 = %+v", v.StepList[0])
	}
	if v.StepList[1].Executable || !strings.Contains(v.StepList[1].Blocked, "重新生成") {
		t.Fatalf("step 2 = %+v", v.StepList[1])
	}
	if v.StepList[2].Status != "done" || v.StepList[2].Blocked != "旧的结果" {
		t.Fatalf("executed step was touched: %+v", v.StepList[2])
	}
	if list, _ := a.Plans(10); !list[0].StepList[0].Executable {
		t.Fatalf("Plans did not recheck: %+v", list[0].StepList[0])
	}
}

// echoModel answers every question and remembers what it was sent; it
// fails when the question contains "坏".
type echoModel struct {
	mu     sync.Mutex
	bodies []string
}

func (f *echoModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.bodies = append(f.bodies, string(body))
	if strings.Contains(string(body), "坏") {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"server busy"}}`)
		return
	}
	_, _ = io.WriteString(w, fmt.Sprintf(`{"choices":[{"message":{"content":"第 %d 个回答"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`, len(f.bodies)))
}

func TestChatIsSavedAndRestored(t *testing.T) {
	a := newApp(t)
	model := &echoModel{}
	srv := httptest.NewServer(model)
	defer srv.Close()
	s, _ := a.AISettings()
	s.BaseURL = srv.URL
	if _, err := a.SaveAISettings(s, "sk-test"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r, err := a.Chat(ctx, "", "服务器内存怎么样？\n顺便看看磁盘")
	if err != nil || r.Error != "" || r.ConversationID == "" {
		t.Fatalf("first answer: %+v %v", r, err)
	}

	// Miao Panel restarts: a new App over the same database.
	b := New(a.Store, a.Secrets)
	r2, err := b.Chat(ctx, r.ConversationID, "那要不要加 swap？")
	if err != nil || r2.ConversationID != r.ConversationID || r2.Reply.Text != "第 2 个回答" {
		t.Fatalf("continued answer: %+v %v", r2, err)
	}
	last := model.bodies[1]
	for _, want := range []string{"服务器内存怎么样", "第 1 个回答", "之前工具查到的原始数据没有保留", "那要不要加 swap"} {
		if !strings.Contains(last, want) {
			t.Fatalf("restored history is missing %q: %s", want, last)
		}
	}

	// A failed answer is reported, and the question is still saved.
	r3, err := b.Chat(ctx, r.ConversationID, "这个问题会坏掉")
	if err != nil || !strings.Contains(r3.Error, "server busy") {
		t.Fatalf("failed answer: %+v %v", r3, err)
	}

	list, _ := b.Conversations()
	if len(list) != 1 || list[0].Title != "服务器内存怎么样？" {
		t.Fatalf("conversations = %+v", list)
	}
	v, err := b.Conversation(r.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range v.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,user,assistant,user,error" {
		t.Fatalf("roles = %v", roles)
	}
	if m := v.Messages[1]; m.Text != "第 1 个回答" || m.Usage.Input != 10 || m.Cost <= 0 || m.Currency == "" {
		t.Fatalf("saved answer = %+v", m)
	}
	if v.Messages[2].Text != "那要不要加 swap？" {
		t.Fatalf("the note for the model leaked into the saved question: %q", v.Messages[2].Text)
	}

	if err := b.DeleteConversation(r.ConversationID); err != nil {
		t.Fatal(err)
	}
	if list, _ := b.Conversations(); len(list) != 0 {
		t.Fatalf("not deleted: %+v", list)
	}
	if msgs, _ := b.Store.ChatMessages(r.ConversationID); len(msgs) != 0 {
		t.Fatalf("messages left behind: %d", len(msgs))
	}
}

func TestTencentCloudPlanWithoutServer(t *testing.T) {
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	a.PollInterval = 10 * time.Millisecond
	ctx := context.Background()

	if _, err := a.TestTencent(ctx); err == nil {
		t.Fatal("test without credentials should fail")
	}
	if _, err := a.SaveTencent("not-a-key", "x"); err == nil {
		t.Fatal("malformed SecretId accepted")
	}
	s, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey)
	if err != nil || !s.Configured || s.SecretID == tencenttest.SecretID || !strings.HasPrefix(s.SecretID, "AKIDfa") {
		t.Fatalf("save: %+v %v", s, err)
	}
	if info, err := a.TestTencent(ctx); err != nil || !strings.Contains(info, "DNSPod：1 个域名") || !strings.Contains(info, "EdgeOne：1 个站点") {
		t.Fatalf("test: %q %v", info, err)
	}

	// The AI looks around; each query is logged as a read.
	out, err := a.toolTencentEO(withOrigin(ctx, OriginAI), json.RawMessage(`{"domain":"blog.example.com"}`))
	if err != nil || !strings.Contains(out, "CNAME 接入") || !strings.Contains(out, "加速域名：没有 blog.example.com") {
		t.Fatalf("tencent_eo: %q %v", out, err)
	}
	if out, err := a.toolTencentDNS(ctx, json.RawMessage(`{"domain":"example.com","subdomain":"blog"}`)); err != nil || !strings.Contains(out, "blog A 1.2.3.4") {
		t.Fatalf("tencent_dns: %q %v", out, err)
	}

	// A checklist that only touches Tencent Cloud needs no server.
	args, _ := json.Marshal(map[string]any{
		"server_id": 0, "title": "blog 接入 EdgeOne", "reason": "加速并开启 HTTPS",
		"steps": []map[string]any{
			{"capability": "eo.domain.add", "summary": "添加加速域名", "params": map[string]any{"domain": "blog.example.com", "origin": "81.68.79.253"}},
			{"capability": "dns.record.set", "summary": "解析到 EdgeOne", "params": map[string]any{"domain": "example.com", "subdomain": "blog", "point_to": "eo"}},
			{"capability": "eo.https.set", "summary": "免费证书", "params": map[string]any{"domain": "blog.example.com"}},
			{"capability": "swap.set", "summary": "这一步要服务器", "params": map[string]any{"size_gb": 1}},
		},
	})
	collector := &planCollector{}
	if _, err := a.toolProposePlan(context.WithValue(ctx, planCollectorKey{}, collector), args); err != nil {
		t.Fatal(err)
	}
	v, _ := a.Plan(collector.ids[0])
	if !v.StepList[0].Executable || !v.StepList[2].Executable || v.StepList[3].Executable || !strings.Contains(v.StepList[3].Blocked, "没有指定服务器") {
		t.Fatalf("steps = %+v", v.StepList)
	}
	if _, err := a.ExecutePlan(v.ID, []int{0, 1, 2}); err != nil {
		t.Fatal(err)
	}
	done := waitPlan(t, a, v.ID)
	for i := 0; i < 3; i++ {
		if done.StepList[i].Status != actions.StatusDone {
			t.Fatalf("step %d = %+v", i+1, done.StepList[i])
		}
	}
	if recs := f.Lookup("example.com", "blog"); len(recs) != 1 || recs[0].Type != "CNAME" {
		t.Fatalf("DNS = %+v", recs)
	}
	e, _ := a.ExecEntry(done.StepList[1].LogID)
	if e.ServerName != "腾讯云" || !e.CanRollback || !strings.Contains(e.Commands, "dnspod ModifyRecord") || strings.Contains(e.Commands, tencenttest.SecretKey) {
		t.Fatalf("log entry = %+v", e)
	}

	// Undo the whole checklist: certificate, then DNS, then the domain.
	if _, err := a.UndoPlan(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if recs := f.Lookup("example.com", "blog"); len(recs) != 1 || recs[0].Type != "A" || f.Domain("blog.example.com") != nil {
		t.Fatalf("after undo: records=%+v domain=%+v", recs, f.Domain("blog.example.com"))
	}
	logs, _ := a.ExecLogs(false)
	var reads, rollbacks int
	for _, l := range logs {
		if l.Kind == store.ExecRead && l.ServerName == "腾讯云" {
			reads++
		}
		if l.Kind == store.ExecRollback {
			rollbacks++
		}
	}
	if reads < 2 || rollbacks != 3 {
		t.Fatalf("reads=%d rollbacks=%d", reads, rollbacks)
	}
	if s := a.ClearTencent(); s.Configured {
		t.Fatal("credentials not cleared")
	}
}

func TestTencentServersAndAnalyticsTools(t *testing.T) {
	a := newApp(t)
	f := tencenttest.Start(t)
	a.TencentEndpoint = f.Endpoint
	ctx := withOrigin(context.Background(), OriginAI)
	if _, err := a.SaveTencent(tencenttest.SecretID, tencenttest.SecretKey); err != nil {
		t.Fatal(err)
	}
	blog, _ := a.Store.AddServer(store.Server{Name: "博客", Host: "81.68.79.253", Port: 22, Username: "root", AuthKind: "password"})

	out, err := a.toolTencentServers(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"轻量应用服务器 blog id=lhins-abc12345 region=ap-guangzhou", "云服务器 CVM shop id=ins-xyz98765",
		"本月流量包=已用900.0GB/1000.0GB（90%）", "还剩", "安全组=sg-abc", fmt.Sprintf("对应 Miao Panel 服务器 id=%d", blog.ID)} {
		if !strings.Contains(out, want) {
			t.Fatalf("tencent_servers is missing %q:\n%s", want, out)
		}
	}
	calls := len(f.Calls)
	if _, err := a.toolTencentServers(ctx, nil); err != nil || len(f.Calls) != calls {
		t.Fatalf("second listing should come from the cache (%d → %d calls)", calls, len(f.Calls))
	}
	if cs, err := a.ServerCloud(ctx, blog.ID); err != nil || cs == nil || cs.ID != "lhins-abc12345" {
		t.Fatalf("server cloud = %+v, %v", cs, err)
	}

	out, err = a.toolTencentServerDetail(ctx, json.RawMessage(`{"instance":"lhins-abc12345","region":"ap-guangzhou","hours":1}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"防火墙规则", "TCP 22", "系统盘快照：没有", "CPU 使用率：平均", "公网出带宽", "走势"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tencent_server_detail is missing %q:\n%s", want, out)
		}
	}

	out, err = a.toolTencentEOAnalytics(ctx, json.RawMessage(`{"domain":"blog.example.com","hours":24}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"站点 example.com", "请求数", "缓存命中率 50.0%", "请求最多的时段", "走势"} {
		if !strings.Contains(out, want) {
			t.Fatalf("overview is missing %q:\n%s", want, out)
		}
	}
	out, err = a.toolTencentEOAnalytics(ctx, json.RawMessage(`{"domain":"example.com","view":"top","dimension":"url","filters":[{"key":"statusCode","operator":"equals","value":["200"]}]}`))
	if err != nil || !strings.Contains(out, "1. /wp-login.php  4200") || !strings.Contains(out, "%）") {
		t.Fatalf("top: %q %v", out, err)
	}
	for _, bad := range []string{`{"domain":"example.com","view":"top","dimension":"password"}`,
		`{"domain":"example.com","filters":[{"key":"a b","operator":"equals","value":["x"]}]}`,
		`{"domain":"example.com","start":"yesterday","end":"today"}`} {
		if _, err := a.toolTencentEOAnalytics(ctx, json.RawMessage(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}

	r, err := a.EOAnalytics(context.Background(), "blog.example.com", 168, false)
	if err != nil || r.Requests == 0 || r.Interval != "hour" || len(r.Series) == 0 || len(r.Tops["url"]) != 3 || r.Tops["url"][0].Share <= 0 {
		t.Fatalf("EOAnalytics = %+v, %v", r, err)
	}
	calls = len(f.Calls)
	if again, err := a.EOAnalytics(context.Background(), "blog.example.com", 168, false); err != nil || len(f.Calls) != calls || again.Requests != r.Requests {
		t.Fatalf("a second look within seconds should come from the cache (%d → %d calls)", calls, len(f.Calls))
	}
	if _, err := a.EOAnalytics(context.Background(), "blog.example.com", 168, true); err != nil || len(f.Calls) == calls {
		t.Fatal("refresh should ask EdgeOne again")
	}
	if live, err := a.EOAnalytics(context.Background(), "blog.example.com", 1, false); err != nil || live.Interval != "min" {
		t.Fatalf("last hour = %+v, %v", live, err)
	}
	// The page's extras: more curves, the period before, and the site's
	// report ranking its domains.
	if len(r.Bandwidth) == 0 || len(r.Resp) == 0 || r.PrevRequests <= 0 || r.CheckedAt == "" || len(r.Tops["referer"]) == 0 || r.Tops["domain"] != nil {
		t.Fatalf("domain report extras = %+v", r)
	}
	site, err := a.EOAnalytics(context.Background(), "example.com", 24, false)
	if err != nil || len(site.Tops["domain"]) == 0 {
		t.Fatalf("site report = %+v, %v", site, err)
	}
	if got, ok := a.LatestEOAnalytics("Example.com", 24); !ok || got.CheckedAt != site.CheckedAt {
		t.Fatalf("latest = %+v %v", got, ok)
	}
	if _, ok := a.LatestEOAnalytics("example.com", 720); ok {
		t.Fatal("no 30-day report was made")
	}
	if eoLabel("status", "404") != "找不到" || eoLabel("country", "us") != "美国" || eoLabel("device", "Mobile") != "手机" || eoLabel("url", "/") != "" {
		t.Fatal("labels")
	}
	if sites, err := a.EOSites(context.Background()); err != nil || len(sites) == 0 || sites[0] != "example.com" {
		t.Fatalf("sites = %v, %v", sites, err)
	}
}

func TestPastedKey(t *testing.T) {
	a := newApp(t)
	srv := sshtest.Start(t, "root", "pw")
	ctx := context.Background()
	req := AddServerRequest{Name: "web", Host: srv.Host, Port: srv.Port, Username: "root", AuthKind: "key"}
	for _, c := range []struct{ text, want string }{
		{"", "请粘贴私钥"},
		{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB user@pc", "私钥格式不对"},
	} {
		req.KeyText = c.text
		if _, err := a.AddServer(req); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%q: %v", c.text, err)
		}
	}
	req.KeyText = "  " + srv.ClientKey + "\n\n"
	sv, err := a.AddServer(req)
	if err != nil {
		t.Fatal(err)
	}
	if sv.KeyPath != "" {
		t.Fatalf("key path = %q", sv.KeyPath)
	}
	if got, _ := a.Secrets.Get(secretKey(sv.ID, "key")); !strings.Contains(got, "OPENSSH PRIVATE KEY") {
		t.Fatal("key not in the secret store")
	}
	if res, err := a.TestConnection(ctx, sv.ID); err != nil || res.Output == "" {
		t.Fatalf("connect with pasted key: %+v %v", res, err)
	}
	if err := a.DeleteServer(sv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Secrets.Get(secretKey(sv.ID, "key")); err == nil {
		t.Fatal("key left behind")
	}
}
