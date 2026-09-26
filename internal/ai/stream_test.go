package ai

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
)

// sseAPI answers each request with the next handler and records bodies.
type sseAPI struct {
	mu       sync.Mutex
	handlers []func(w http.ResponseWriter, body map[string]any)
	requests []map[string]any
}

func (f *sseAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.requests = append(f.requests, body)
	if len(f.handlers) == 0 {
		f.mu.Unlock()
		http.Error(w, `{"error":{"message":"no more responses"}}`, http.StatusInternalServerError)
		return
	}
	h := f.handlers[0]
	f.handlers = f.handlers[1:]
	f.mu.Unlock()
	h(w, body)
}

// events writes server-sent events, flushing after each.
func events(lines ...string) func(http.ResponseWriter, map[string]any) {
	return func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			_, _ = io.WriteString(w, l+"\n\n")
			w.(http.Flusher).Flush()
		}
	}
}

func record(evs *[]Event) func(Event) {
	return func(e Event) {
		if n := len(*evs); n > 0 && e.Type == (*evs)[n-1].Type && (e.Type == "text" || e.Type == "thinking") {
			(*evs)[n-1].Text += e.Text
			return
		}
		*evs = append(*evs, e)
	}
}

func kinds(evs []Event) string {
	var out []string
	for _, e := range evs {
		s := e.Type
		if e.Text != "" {
			s += ":" + e.Text
		}
		if e.Tool != "" {
			s += ":" + e.Tool
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

func TestOpenAIStreaming(t *testing.T) {
	api := &sseAPI{handlers: []func(http.ResponseWriter, map[string]any){
		// Some compatible APIs do not know stream_options.
		func(w http.ResponseWriter, body map[string]any) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"unknown field stream_options"}}`)
		},
		events(
			`: keep-alive`,
			`data: {"choices":[{"delta":{"role":"assistant","reasoning_content":"先看"}}]}`,
			`data: {"choices":[{"delta":{"reasoning_content":"看服务器"}}]}`,
			`data: {"choices":[{"delta":{"content":"我查"}}]}`,
			`data:{"choices":[{"delta":{"content":"一下。"}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"list_servers","arguments":"{"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"list_servers","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":50,"prompt_cache_hit_tokens":800}}`,
			`data: [DONE]`,
		),
		events(
			`data: {"choices":[{"delta":{"content":"你有"}}]}`,
			`data: {"choices":[{"delta":{"content":"一台服务器。"},"finish_reason":"stop","usage":{"prompt_tokens":1200,"completion_tokens":30}}]}`,
			`data: [DONE]`,
		),
	}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	tools := echoTool()
	sess, _ := NewSession(Config{Kind: KindOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k",
		Tools: []ToolDef{tools["list_servers"].Def}, EchoReasoning: true})
	var evs []Event
	reply, err := (&Agent{Session: sess, Tools: tools}).Ask(context.Background(), "我有几台服务器？", record(&evs))
	if err != nil {
		t.Fatal(err)
	}
	want := "thinking:先看看服务器 text:我查一下。 prepare:list_servers prepare:list_servers tool:list_servers tool_done:list_servers tool:list_servers tool_done:list_servers round text:你有一台服务器。"
	if got := kinds(evs); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	if reply.Text != "你有一台服务器。" || len(reply.Steps) != 2 || reply.Usage != (Usage{Input: 2200, CachedInput: 800, Output: 80}) {
		t.Fatalf("reply = %+v", reply)
	}
	if api.requests[0]["stream_options"] == nil || api.requests[1]["stream_options"] != nil || api.requests[1]["stream"] != true {
		t.Fatalf("requests = %v", api.requests[:2])
	}
	msgs := api.requests[2]["messages"].([]any)
	asst := msgs[2].(map[string]any)
	calls := asst["tool_calls"].([]any)
	first := calls[0].(map[string]any)["function"].(map[string]any)
	if asst["content"] != "我查一下。" || asst["reasoning_content"] != "先看看服务器" || len(calls) != 2 || first["arguments"] != "{}" {
		t.Fatalf("assistant message = %v", asst)
	}
}

func TestOpenAIStreamingFallsBackToWholeAnswer(t *testing.T) {
	api := &sseAPI{handlers: []func(http.ResponseWriter, map[string]any){
		func(w http.ResponseWriter, _ map[string]any) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"整段回答"}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
		},
		events(`data: {"error":{"message":"rate limited"}}`),
	}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	sess, _ := NewSession(Config{Kind: KindOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	var evs []Event
	reply, err := (&Agent{Session: sess}).Ask(context.Background(), "你好", record(&evs))
	if err != nil || reply.Text != "整段回答" || kinds(evs) != "text:整段回答" {
		t.Fatalf("reply = %+v %v events %s", reply, err, kinds(evs))
	}
	if _, err := (&Agent{Session: sess}).Ask(context.Background(), "再来", record(&evs)); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("error in the stream: %v", err)
	}
}

// A stopped answer keeps what was said, and the next question still
// follows an answer.
func TestStoppedAnswerKeepsHistoryInTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	api := &sseAPI{handlers: []func(http.ResponseWriter, map[string]any){
		func(w http.ResponseWriter, _ map[string]any) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"部分回答"}}]}`+"\n\n")
			w.(http.Flusher).Flush()
			<-ctx.Done()
		},
		events(`data: {"choices":[{"delta":{"content":"好的"}}]}`, `data: [DONE]`),
	}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	sess, _ := NewSession(Config{Kind: KindOpenAI, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	agent := &Agent{Session: sess}
	var evs []Event
	stopOnText := func(e Event) {
		if e.Type == "text" {
			cancel() // the user presses 停止 once the answer has begun
		}
	}
	reply, err := agent.Ask(ctx, "写一篇长文", stopOnText)
	if !errors.Is(err, context.Canceled) || reply.Text != "部分回答" {
		t.Fatalf("stopped: %+v %v", reply, err)
	}
	if _, err := agent.Ask(context.Background(), "继续", record(&evs)); err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range api.requests[1]["messages"].([]any) {
		m := m.(map[string]any)
		roles = append(roles, fmt.Sprint(m["role"]))
		if m["role"] == "assistant" && m["content"] != "部分回答\n"+interruptedNote {
			t.Fatalf("interrupted answer = %q", m["content"])
		}
	}
	if strings.Join(roles, ",") != "system,user,assistant,user" {
		t.Fatalf("roles = %v", roles)
	}
}

func claudeStream(model, stop string, blocks ...string) func(http.ResponseWriter, map[string]any) {
	lines := []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `","content":[],"stop_reason":null,"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":900}}}`,
	}
	lines = append(lines, blocks...)
	lines = append(lines,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"`+stop+`","stop_sequence":null},"usage":{"output_tokens":20}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
	)
	return func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < len(lines); i += 2 {
			_, _ = io.WriteString(w, lines[i]+"\n"+lines[i+1]+"\n\n")
			w.(http.Flusher).Flush()
		}
	}
}

func textBlock(i int, parts ...string) []string {
	out := []string{`event: content_block_start`, fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, i)}
	for _, p := range parts {
		out = append(out, `event: content_block_delta`, fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%q}}`, i, p))
	}
	return append(out, `event: content_block_stop`, fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, i))
}

func TestClaudeStreaming(t *testing.T) {
	toolUse := []string{
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"list_servers","input":{}}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
	}
	api := &sseAPI{handlers: []func(http.ResponseWriter, map[string]any){
		claudeStream("claude-sonnet-5", "tool_use", append(textBlock(0, "我查", "一下。"), toolUse...)...),
		// Declined halfway, then served again by the fallback model.
		claudeStream("claude-sonnet-5", "refusal", textBlock(0, "这个")...),
		claudeStream("claude-opus-4-8", "end_turn", textBlock(0, "你有", "一台服务器。")...),
	}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	tools := echoTool()
	sess, _ := NewSession(Config{Kind: KindAnthropic, BaseURL: srv.URL, Model: "claude-sonnet-5", FallbackModel: "claude-opus-4-8",
		APIKey: "k", System: "sys", Tools: []ToolDef{tools["list_servers"].Def}})
	var evs []Event
	reply, err := (&Agent{Session: sess, Tools: tools}).Ask(context.Background(), "我有几台服务器？", record(&evs))
	if err != nil {
		t.Fatal(err)
	}
	want := "text:我查一下。 prepare:list_servers tool:list_servers tool_done:list_servers round text:这个 reset text:你有一台服务器。"
	if got := kinds(evs); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	if reply.Text != "你有一台服务器。" || len(reply.Steps) != 1 || reply.Usage != (Usage{Input: 2000, CachedInput: 1800, Output: 40}) {
		t.Fatalf("reply = %+v", reply)
	}
	if api.requests[0]["stream"] != true || api.requests[2]["model"] != "claude-opus-4-8" {
		t.Fatalf("requests = %v", api.requests)
	}
	msgs := api.requests[1]["messages"].([]any)
	asst := msgs[1].(map[string]any)["content"].([]any)
	if len(asst) != 2 || asst[1].(map[string]any)["type"] != "tool_use" || asst[1].(map[string]any)["id"] != "toolu_1" {
		t.Fatalf("assistant turn = %v", asst)
	}
}
