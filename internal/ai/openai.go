package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// openAISession speaks the OpenAI-compatible chat completions API that
// DeepSeek, Qwen (DashScope compatible mode), Kimi, GLM and others offer.
type openAISession struct {
	cfg    Config
	client *http.Client
	msgs   []map[string]any
}

func newOpenAISession(cfg Config) *openAISession {
	return &openAISession{cfg: cfg, client: &http.Client{Timeout: 5 * time.Minute}}
}

func (s *openAISession) AddUser(text string) {
	s.msgs = append(s.msgs, map[string]any{"role": "user", "content": text})
}

func (s *openAISession) AddAssistant(text string) {
	s.msgs = append(s.msgs, map[string]any{"role": "assistant", "content": text})
}

func (s *openAISession) AddToolResults(results []ToolResult) {
	for _, r := range results {
		content := r.Content
		if r.IsError {
			content = "错误: " + content
		}
		s.msgs = append(s.msgs, map[string]any{"role": "tool", "tool_call_id": r.CallID, "content": content})
	}
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaUsage struct {
	PromptTokens         int `json:"prompt_tokens"`
	CompletionTokens     int `json:"completion_tokens"`
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"` // DeepSeek
	PromptTokensDetails  struct {
		CachedTokens int `json:"cached_tokens"` // OpenAI style (Qwen, Kimi, ...)
	} `json:"prompt_tokens_details"`
}

type oaMessage struct {
	Content          *string      `json:"content"`
	ReasoningContent string       `json:"reasoning_content"`
	ToolCalls        []oaToolCall `json:"tool_calls"`
}

type oaError struct {
	Message string `json:"message"`
}

type oaResponse struct {
	Choices []struct {
		Message      oaMessage `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Usage oaUsage  `json:"usage"`
	Error *oaError `json:"error"`
}

// oaChunk is one server-sent event of a streamed answer.
type oaChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		Usage *oaUsage `json:"usage"` // Kimi reports it here
	} `json:"choices"`
	Usage *oaUsage `json:"usage"`
	Error *oaError `json:"error"`
}

func (s *openAISession) body() map[string]any {
	msgs := append([]map[string]any{{"role": "system", "content": s.cfg.System}}, s.msgs...)
	body := map[string]any{
		"model":      s.cfg.Model,
		"messages":   msgs,
		"max_tokens": s.cfg.MaxTokens,
	}
	if len(s.cfg.Tools) > 0 {
		tools := make([]map[string]any, 0, len(s.cfg.Tools))
		for _, t := range s.cfg.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": t.Name, "description": t.Description, "parameters": t.Schema,
				},
			})
		}
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	return body
}

func (s *openAISession) post(ctx context.Context, body map[string]any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(s.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	return s.client.Do(req)
}

// apiError describes a failed request from its body.
func apiError(status int, raw []byte) error {
	msg := string(raw)
	var out struct {
		Error *oaError `json:"error"`
	}
	if json.Unmarshal(raw, &out) == nil && out.Error != nil && out.Error.Message != "" {
		msg = out.Error.Message
	}
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return fmt.Errorf("model API returned HTTP %d: %s", status, msg)
}

// decode reads a whole (not streamed) answer.
func decode(resp *http.Response) (oaMessage, oaUsage, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return oaMessage{}, oaUsage{}, err
	}
	var out oaResponse
	if err := json.Unmarshal(raw, &out); err != nil || resp.StatusCode != http.StatusOK {
		return oaMessage{}, oaUsage{}, apiError(resp.StatusCode, raw)
	}
	if len(out.Choices) == 0 {
		return oaMessage{}, oaUsage{}, fmt.Errorf("model API returned no choices")
	}
	return out.Choices[0].Message, out.Usage, nil
}

func (s *openAISession) Next(ctx context.Context, onDelta func(Delta)) (Turn, error) {
	body := s.body()
	var (
		m     oaMessage
		usage oaUsage
		err   error
	)
	if onDelta == nil {
		var resp *http.Response
		if resp, err = s.post(ctx, body); err != nil {
			return Turn{}, err
		}
		defer resp.Body.Close()
		m, usage, err = decode(resp)
	} else {
		m, usage, err = s.stream(ctx, body, onDelta)
	}
	if err != nil {
		return Turn{}, err
	}
	turn := Turn{Usage: Usage{
		Input:       usage.PromptTokens,
		CachedInput: max(usage.PromptCacheHitTokens, usage.PromptTokensDetails.CachedTokens),
		Output:      usage.CompletionTokens,
	}}
	if m.Content != nil {
		turn.Text = *m.Content
	}
	assistant := map[string]any{"role": "assistant", "content": turn.Text}
	if s.cfg.EchoReasoning && m.ReasoningContent != "" {
		// DeepSeek thinking mode needs its reasoning sent back on later
		// turns, or multi-step tool calls fail.
		assistant["reasoning_content"] = m.ReasoningContent
	}
	if len(m.ToolCalls) > 0 {
		assistant["tool_calls"] = m.ToolCalls
		for _, c := range m.ToolCalls {
			args := json.RawMessage(c.Function.Arguments)
			if !json.Valid(args) {
				args = json.RawMessage(`{}`)
			}
			turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: c.ID, Name: c.Function.Name, Args: args})
		}
	}
	s.msgs = append(s.msgs, assistant)
	return turn, nil
}

// stream asks for the answer as server-sent events and puts it together.
// A server that ignores "stream" and answers in one piece works too.
func (s *openAISession) stream(ctx context.Context, body map[string]any, onDelta func(Delta)) (oaMessage, oaUsage, error) {
	body["stream"] = true
	body["stream_options"] = map[string]any{"include_usage": true}
	resp, err := s.post(ctx, body)
	if err != nil {
		return oaMessage{}, oaUsage{}, err
	}
	if resp.StatusCode == http.StatusBadRequest {
		// Some compatible APIs reject stream_options; usage then comes
		// without asking, or not at all.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if !strings.Contains(string(raw), "stream_options") {
			return oaMessage{}, oaUsage{}, apiError(resp.StatusCode, raw)
		}
		delete(body, "stream_options")
		if resp, err = s.post(ctx, body); err != nil {
			return oaMessage{}, oaUsage{}, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		m, usage, err := decode(resp)
		if err == nil && m.Content != nil && *m.Content != "" {
			onDelta(Delta{Text: *m.Content})
		}
		return m, usage, err
	}

	var (
		text, reasoning strings.Builder
		calls           []oaToolCall
		slot            = map[int]int{} // stream index -> position in calls
		usage           oaUsage
		sawText         bool
	)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // blank separators, comments, "event:" lines
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var c oaChunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			continue
		}
		if c.Error != nil {
			return oaMessage{}, oaUsage{}, fmt.Errorf("model API: %s", c.Error.Message)
		}
		if c.Usage != nil {
			usage = *c.Usage
		}
		for _, ch := range c.Choices {
			if ch.Usage != nil && c.Usage == nil {
				usage = *ch.Usage
			}
			d := ch.Delta
			if d.ReasoningContent != "" {
				reasoning.WriteString(d.ReasoningContent)
				onDelta(Delta{Reasoning: d.ReasoningContent})
			}
			if d.Content != "" {
				text.WriteString(d.Content)
				sawText = true
				onDelta(Delta{Text: d.Content})
			}
			for _, tc := range d.ToolCalls {
				i, ok := slot[tc.Index]
				if !ok || (tc.ID != "" && calls[i].ID != "" && tc.ID != calls[i].ID) {
					calls = append(calls, oaToolCall{Type: "function"})
					i = len(calls) - 1
					slot[tc.Index] = i
				}
				call := &calls[i]
				if tc.ID != "" {
					call.ID = tc.ID
				}
				if tc.Function.Name != "" && call.Function.Name == "" {
					call.Function.Name = tc.Function.Name
					onDelta(Delta{Tool: tc.Function.Name})
				}
				call.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil {
		return oaMessage{}, oaUsage{}, err
	}
	m := oaMessage{ReasoningContent: reasoning.String(), ToolCalls: calls}
	if sawText || len(calls) == 0 {
		t := text.String()
		m.Content = &t
	}
	return m, usage, nil
}
