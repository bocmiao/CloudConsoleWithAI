package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// claudeSession uses the official Anthropic Go SDK.
type claudeSession struct {
	cfg     Config
	client  anthropic.Client
	tools   []anthropic.ToolUnionParam
	msgs    []anthropic.MessageParam
	pending []anthropic.ContentBlockParamUnion // next user turn: tool results first, then text
}

func newClaudeSession(cfg Config) *claudeSession {
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	s := &claudeSession{cfg: cfg, client: anthropic.NewClient(opts...)}
	for _, t := range cfg.Tools {
		tp := anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: t.Schema["properties"],
				Required:   stringSlice(t.Schema["required"]),
			},
		}
		s.tools = append(s.tools, anthropic.ToolUnionParam{OfTool: &tp})
	}
	return s
}

func stringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (s *claudeSession) AddUser(text string) {
	s.pending = append(s.pending, anthropic.NewTextBlock(text))
}

func (s *claudeSession) AddAssistant(text string) {
	if text == "" {
		return // the API rejects empty text blocks
	}
	if len(s.pending) > 0 {
		s.msgs = append(s.msgs, anthropic.NewUserMessage(s.pending...))
		s.pending = nil
	}
	s.msgs = append(s.msgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(text)))
}

func (s *claudeSession) AddToolResults(results []ToolResult) {
	for _, r := range results {
		s.pending = append(s.pending, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
	}
}

func (s *claudeSession) call(ctx context.Context, model string, msgs []anthropic.MessageParam) (*anthropic.Message, error) {
	return s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: int64(s.cfg.MaxTokens),
		System: []anthropic.TextBlockParam{{
			Text:         s.cfg.System,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Messages: msgs,
		Tools:    s.tools,
	})
}

func (s *claudeSession) Next(ctx context.Context) (Turn, error) {
	msgs := s.msgs
	if len(s.pending) > 0 {
		msgs = append(msgs, anthropic.NewUserMessage(s.pending...))
	}
	resp, err := s.call(ctx, s.cfg.Model, msgs)
	if err == nil && resp.StopReason == anthropic.StopReasonRefusal && s.cfg.FallbackModel != "" {
		resp, err = s.call(ctx, s.cfg.FallbackModel, msgs)
	}
	if err != nil {
		return Turn{}, err
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return Turn{}, fmt.Errorf("the model declined this request (%s)", resp.StopDetails.Category)
	}
	s.msgs = append(msgs, resp.ToParam())
	s.pending = nil

	turn := Turn{Usage: Usage{
		Input:       int(resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens),
		CachedInput: int(resp.Usage.CacheReadInputTokens),
		Output:      int(resp.Usage.OutputTokens),
	}}
	for _, block := range resp.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			turn.Text += v.Text
		case anthropic.ToolUseBlock:
			turn.ToolCalls = append(turn.ToolCalls, ToolCall{
				ID: v.ID, Name: v.Name, Args: json.RawMessage(v.JSON.Input.Raw()),
			})
		}
	}
	return turn, nil
}
