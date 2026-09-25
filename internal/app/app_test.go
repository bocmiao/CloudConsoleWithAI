package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bocmiao/CloudConsoleWithAI/internal/secrets"
	"github.com/bocmiao/CloudConsoleWithAI/internal/sshx/sshtest"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
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
