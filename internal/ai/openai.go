package ai

import (
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

type oaResponse struct {
	Choices []struct {
		Message struct {
			Content          *string      `json:"content"`
			ReasoningContent string       `json:"reasoning_content"`
			ToolCalls        []oaToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens         int `json:"prompt_tokens"`
		CompletionTokens     int `json:"completion_tokens"`
		PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"` // DeepSeek
		PromptTokensDetails  struct {
			CachedTokens int `json:"cached_tokens"` // OpenAI style (Qwen, Kimi, ...)
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *openAISession) Next(ctx context.Context) (Turn, error) {
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
	data, err := json.Marshal(body)
	if err != nil {
		return Turn{}, err
	}
	url := strings.TrimRight(s.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return Turn{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	resp, err := s.client.Do(req)
	if err != nil {
		return Turn{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Turn{}, err
	}
	var out oaResponse
	if err := json.Unmarshal(raw, &out); err != nil || resp.StatusCode != http.StatusOK {
		msg := string(raw)
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return Turn{}, fmt.Errorf("model API returned HTTP %d: %s", resp.StatusCode, msg)
	}
	if len(out.Choices) == 0 {
		return Turn{}, fmt.Errorf("model API returned no choices")
	}
	m := out.Choices[0].Message
	turn := Turn{Usage: Usage{
		Input:       out.Usage.PromptTokens,
		CachedInput: max(out.Usage.PromptCacheHitTokens, out.Usage.PromptTokensDetails.CachedTokens),
		Output:      out.Usage.CompletionTokens,
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
