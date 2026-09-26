package api

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// streamModel streams an answer in two pieces, then, for the next
// question, starts answering and waits until it is stopped.
type streamModel struct {
	mu    sync.Mutex
	calls int
}

func (m *streamModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(s string) {
		_, _ = io.WriteString(w, "data: "+s+"\n\n")
		w.(http.Flusher).Flush()
	}
	if n == 1 {
		send(`{"choices":[{"delta":{"content":"你好"}}]}`)
		send(`{"choices":[{"delta":{"content":"，世界"}}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`)
		send(`[DONE]`)
		return
	}
	send(`{"choices":[{"delta":{"content":"开始"}}]}`)
	<-r.Context().Done()
}

func post(t *testing.T, base, path, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
	req.Host = "127.0.0.1:18765"
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "tok"})
	req.Header.Set("X-Miao", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

type line struct {
	Type           string `json:"type"`
	Text           string `json:"text"`
	ConversationID string `json:"conversationId"`
	Reply          struct {
		Error string `json:"error"`
		Reply struct {
			Text string `json:"text"`
		} `json:"reply"`
	} `json:"reply"`
}

func TestChatStreamAndStop(t *testing.T) {
	s := newServer(t)
	model := httptest.NewServer(&streamModel{})
	defer model.Close()
	settings, _ := s.app.AISettings()
	settings.BaseURL = model.URL
	if _, err := s.app.SaveAISettings(settings, "sk-test"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	defer srv.Close()

	resp := post(t, srv.URL, "/api/chat/stream", `{"message":"你好"}`)
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != 200 || !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Fatalf("status %d, type %q", resp.StatusCode, ct)
	}
	var lines []line
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("bad line %q", sc.Text())
		}
		lines = append(lines, l)
	}
	resp.Body.Close()
	var text string
	var types []string
	for _, l := range lines {
		types = append(types, l.Type)
		if l.Type == "text" {
			text += l.Text
		}
	}
	conv := lines[0].ConversationID
	last := lines[len(lines)-1]
	if strings.Join(types, ",") != "start,text,text,done" || conv == "" || text != "你好，世界" || last.Reply.Reply.Text != "你好，世界" {
		t.Fatalf("lines = %+v", lines)
	}

	// 停止: what was said stays, in the reply and in the saved conversation.
	resp = post(t, srv.URL, "/api/chat/stream", `{"conversationId":"`+conv+`","message":"写长一点"}`)
	defer resp.Body.Close()
	rd := bufio.NewScanner(resp.Body)
	var done line
	for rd.Scan() {
		var l line
		_ = json.Unmarshal(rd.Bytes(), &l)
		if l.Type == "text" {
			stop := post(t, srv.URL, "/api/chat/stop", `{"conversationId":"`+conv+`"}`)
			stop.Body.Close()
		}
		if l.Type == "done" {
			done = l
		}
	}
	if done.Reply.Error != "已停止回答" || done.Reply.Reply.Text != "开始" {
		t.Fatalf("stopped reply = %+v", done)
	}
	v, err := s.app.Conversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range v.Messages {
		got = append(got, m.Role+":"+m.Text)
	}
	if strings.Join(got, "|") != "user:你好|assistant:你好，世界|user:写长一点|assistant:开始|error:已停止回答" {
		t.Fatalf("saved = %v", got)
	}
	if w := do(s, "POST", "/api/chat/stream", "127.0.0.1:18765", `{"message":" "}`, true); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "请输入问题") {
		t.Fatalf("empty question: %d %s", w.Code, w.Body.String())
	}
}
