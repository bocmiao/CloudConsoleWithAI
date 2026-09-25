package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPI replays canned JSON responses and records request bodies.
type fakeAPI struct {
	mu        sync.Mutex
	responses []string
	requests  []map[string]any
	paths     []string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	f.requests = append(f.requests, m)
	f.paths = append(f.paths, r.URL.Path)
	if len(f.responses) == 0 {
		http.Error(w, `{"error":{"message":"no more responses"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, f.responses[0])
	f.responses = f.responses[1:]
}

func echoTool() map[string]Tool {
	return map[string]Tool{"list_servers": {
		Def: ToolDef{Name: "list_servers", Description: "list", Schema: map[string]any{"type": "object", "properties": map[string]any{}}},
		Run: func(_ context.Context, _ json.RawMessage) (string, error) { return "id=1 名称=blog", nil },
	}}
}

func TestOpenAIAgentLoop(t *testing.T) {
	api := &fakeAPI{responses: []string{
		`{"choices":[{"message":{"content":"","reasoning_content":"先看看服务器","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_servers","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
		  "usage":{"prompt_tokens":1000,"completion_tokens":50,"prompt_cache_hit_tokens":800}}`,
		`{"choices":[{"message":{"content":"你有一台服务器：blog。"},"finish_reason":"stop"}],
		  "usage":{"prompt_tokens":1200,"completion_tokens":30,"prompt_cache_hit_tokens":1000}}`,
	}}
	srv := httptest.NewServer(api)
	defer srv.Close()

	tools := echoTool()
	sess, err := NewSession(Config{Kind: KindOpenAI, BaseURL: srv.URL + "/v1", Model: "deepseek-flash", APIKey: "k",
		System: "sys", Tools: []ToolDef{tools["list_servers"].Def}, EchoReasoning: true})
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{Session: sess, Tools: tools}
	reply, err := agent.Ask(context.Background(), "我有几台服务器？")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "你有一台服务器：blog。" || len(reply.Steps) != 1 || reply.Steps[0].Output != "id=1 名称=blog" {
		t.Fatalf("reply = %+v", reply)
	}
	if reply.Usage != (Usage{Input: 2200, CachedInput: 1800, Output: 80}) {
		t.Fatalf("usage = %+v", reply.Usage)
	}
	if api.paths[0] != "/v1/chat/completions" {
		t.Fatalf("path = %s", api.paths[0])
	}

	// The second request must carry the assistant tool call, its reasoning,
	// and the tool result.
	msgs := api.requests[1]["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("second request has %d messages, want 4: %v", len(msgs), msgs)
	}
	asst := msgs[2].(map[string]any)
	if asst["reasoning_content"] != "先看看服务器" || asst["tool_calls"] == nil {
		t.Fatalf("assistant message = %v", asst)
	}
	tool := msgs[3].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != "id=1 名称=blog" {
		t.Fatalf("tool message = %v", tool)
	}

	p, _ := PresetByID("deepseek-flash")
	want := (400*2 + 1800*0.04 + 80*8) / 1e6
	if got := p.Cost(reply.Usage); got < want*0.999 || got > want*1.001 {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestOpenAIErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Authentication Fails"}}`)
	}))
	defer srv.Close()
	sess, _ := NewSession(Config{Kind: KindOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "bad"})
	sess.AddUser("hi")
	_, err := sess.Next(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Authentication Fails") {
		t.Fatalf("err = %v", err)
	}
}

func TestClaudeAgentLoop(t *testing.T) {
	api := &fakeAPI{responses: []string{
		`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5",
		  "content":[{"type":"text","text":"我查一下。"},{"type":"tool_use","id":"toolu_1","name":"list_servers","input":{}}],
		  "stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":900}}`,
		`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-5",
		  "content":[{"type":"text","text":"你有一台服务器：blog。"}],
		  "stop_reason":"end_turn","usage":{"input_tokens":50,"output_tokens":10}}`,
	}}
	srv := httptest.NewServer(api)
	defer srv.Close()

	tools := echoTool()
	sess, err := NewSession(Config{Kind: KindAnthropic, BaseURL: srv.URL, Model: "claude-sonnet-5", APIKey: "k",
		System: "sys", Tools: []ToolDef{tools["list_servers"].Def}})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := (&Agent{Session: sess, Tools: tools}).Ask(context.Background(), "我有几台服务器？")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "你有一台服务器：blog。" || len(reply.Steps) != 1 {
		t.Fatalf("reply = %+v", reply)
	}
	if reply.Usage != (Usage{Input: 1050, CachedInput: 900, Output: 30}) {
		t.Fatalf("usage = %+v", reply.Usage)
	}
	if api.paths[0] != "/v1/messages" {
		t.Fatalf("path = %s", api.paths[0])
	}
	msgs := api.requests[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("second request has %d messages, want 3", len(msgs))
	}
	last := msgs[2].(map[string]any)
	block := last["content"].([]any)[0].(map[string]any)
	if last["role"] != "user" || block["type"] != "tool_result" || block["tool_use_id"] != "toolu_1" {
		t.Fatalf("tool result message = %v", last)
	}
}

func TestAgentStopsAtMaxRounds(t *testing.T) {
	call := `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c","type":"function","function":{"name":"list_servers","arguments":"{}"}}]}}],"usage":{}}`
	api := &fakeAPI{responses: []string{call, call, call}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	tools := echoTool()
	sess, _ := NewSession(Config{Kind: KindOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k", Tools: []ToolDef{tools["list_servers"].Def}})
	reply, err := (&Agent{Session: sess, Tools: tools, MaxRounds: 2}).Ask(context.Background(), "loop")
	if err != nil {
		t.Fatal(err)
	}
	if len(api.requests) != 2 || !strings.Contains(reply.Text, "查询上限") {
		t.Fatalf("requests=%d reply=%q", len(api.requests), reply.Text)
	}
}

func TestClaudeRestoredHistory(t *testing.T) {
	api := &fakeAPI{responses: []string{
		`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5",
		  "content":[{"type":"text","text":"好的。"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`,
	}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	sess, err := NewSession(Config{Kind: KindAnthropic, BaseURL: srv.URL, Model: "claude-sonnet-5", APIKey: "k", System: "sys"})
	if err != nil {
		t.Fatal(err)
	}
	// A saved conversation: a question whose answer failed, then one that
	// was answered, then an empty answer that must be skipped.
	sess.AddUser("第一个问题")
	sess.AddUser("第二个问题")
	sess.AddAssistant("第二个回答")
	sess.AddAssistant("")
	if _, err := (&Agent{Session: sess}).Ask(context.Background(), "第三个问题"); err != nil {
		t.Fatal(err)
	}
	msgs := api.requests[0]["messages"].([]any)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "user,assistant,user" {
		t.Fatalf("roles = %v", roles)
	}
	if n := len(msgs[0].(map[string]any)["content"].([]any)); n != 2 {
		t.Fatalf("the two unanswered questions should share one turn, got %d blocks", n)
	}
}
