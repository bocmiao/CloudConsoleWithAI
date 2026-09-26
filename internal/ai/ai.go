// Package ai talks to language models behind one small interface, so the
// agent loop works the same with Claude and with OpenAI-compatible APIs
// (DeepSeek, Qwen, Kimi, GLM, ...).
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ToolDef describes a tool the model may call. Schema is a JSON Schema
// object with "properties" and optionally "required".
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any
}

// ToolCall is the model asking to run a tool.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// ToolResult answers one ToolCall.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Usage counts tokens for one or more model calls. Input includes cached
// input tokens; CachedInput is the part served from the provider's cache.
type Usage struct {
	Input       int `json:"input"`
	CachedInput int `json:"cachedInput"`
	Output      int `json:"output"`
}

// Add accumulates another call's usage.
func (u *Usage) Add(o Usage) {
	u.Input += o.Input
	u.CachedInput += o.CachedInput
	u.Output += o.Output
}

// Turn is one model response.
type Turn struct {
	Text      string
	ToolCalls []ToolCall
	Usage     Usage
}

// Session is one conversation. It keeps history in the provider's native
// format so nothing is lost converting back and forth.
type Session interface {
	AddUser(text string)
	// AddAssistant records an earlier answer, to continue a saved
	// conversation in a new session.
	AddAssistant(text string)
	AddToolResults(results []ToolResult)
	// Next sends everything added since the last call and returns the
	// model's turn, which is appended to the history. With onDelta the
	// turn is streamed and onDelta sees it as it is generated.
	Next(ctx context.Context, onDelta func(Delta)) (Turn, error)
}

// Delta is a piece of a turn as the model generates it.
type Delta struct {
	Text      string // more of the answer
	Reasoning string // more of the model's thinking, for models that show it
	Tool      string // a tool call to this tool has begun
	// Reset: forget the text so far, the turn is being generated again
	// (a declined turn re-served by the fallback model).
	Reset bool
}

// Provider kinds.
const (
	KindOpenAI    = "openai" // any OpenAI-compatible chat completions API
	KindAnthropic = "anthropic"
)

// Config selects and configures a model.
type Config struct {
	Kind      string
	BaseURL   string
	Model     string
	APIKey    string
	System    string
	Tools     []ToolDef
	MaxTokens int
	// FallbackModel re-serves a turn the primary model declined (Claude only).
	FallbackModel string
	// EchoReasoning sends reasoning_content back in later turns
	// (OpenAI-compatible APIs that require it, such as DeepSeek).
	EchoReasoning bool
}

// ErrNoAPIKey is returned when no key is configured.
var ErrNoAPIKey = errors.New("AI model API key is not set")

// NewSession starts a conversation.
func NewSession(cfg Config) (Session, error) {
	if cfg.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 8000
	}
	switch cfg.Kind {
	case KindOpenAI:
		return newOpenAISession(cfg), nil
	case KindAnthropic:
		return newClaudeSession(cfg), nil
	default:
		return nil, fmt.Errorf("unknown model provider %q", cfg.Kind)
	}
}
