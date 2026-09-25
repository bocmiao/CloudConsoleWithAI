package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/bocmiao/CloudConsoleWithAI/internal/ai"
	"github.com/bocmiao/CloudConsoleWithAI/internal/store"
)

// AISettings is the model configuration (the API key is stored separately).
type AISettings struct {
	ai.Preset
	PresetID      string  `json:"presetId"`
	MonthlyBudget float64 `json:"monthlyBudget"` // 0 = no limit, in Preset.Currency
	HasKey        bool    `json:"hasKey"`
}

const aiSettingsKey = "ai"

func aiKeyName(presetID string) string { return "ai/key/" + presetID }

// AISettings returns the saved model settings, defaulting to the
// recommended preset.
func (a *App) AISettings() (AISettings, error) {
	var s AISettings
	raw, err := a.Store.Setting(aiSettingsKey)
	if err != nil {
		return s, err
	}
	if raw == "" {
		p, _ := ai.PresetByID(ai.DefaultPresetID)
		s = AISettings{Preset: p, PresetID: p.ID}
	} else if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, err
	}
	_, err = a.Secrets.Get(aiKeyName(s.PresetID))
	s.HasKey = err == nil
	return s, nil
}

// SaveAISettings stores the model settings, and the API key if one is given.
func (a *App) SaveAISettings(s AISettings, apiKey string) (AISettings, error) {
	if _, ok := ai.PresetByID(s.PresetID); !ok {
		return s, userErr("未知的模型选项：%s", s.PresetID)
	}
	if s.Kind != ai.KindOpenAI && s.Kind != ai.KindAnthropic {
		return s, userErr("未知的接口类型：%s", s.Kind)
	}
	if strings.TrimSpace(s.Model) == "" || (s.Kind == ai.KindOpenAI && !validBaseURL(s.BaseURL)) {
		return s, userErr("请填写模型名，以及以 https:// 开头的接口地址（本机模型可以用 http://127.0.0.1 或 http://localhost）")
	}
	s.ID = s.PresetID
	s.HasKey = false
	data, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	if err := a.Store.SetSetting(aiSettingsKey, string(data)); err != nil {
		return s, err
	}
	if k := strings.TrimSpace(apiKey); k != "" {
		if err := a.Secrets.Set(aiKeyName(s.PresetID), k); err != nil {
			return s, fmt.Errorf("save API key: %w", err)
		}
	}
	a.mu.Lock()
	a.convs = map[string]*conversation{} // new settings apply to new conversations
	a.mu.Unlock()
	_ = a.Store.Audit("user", "settings.ai", s.PresetID, s.Model)
	return a.AISettings()
}

// validBaseURL requires HTTPS, except for models running on this machine.
func validBaseURL(u string) bool {
	return strings.HasPrefix(u, "https://") ||
		strings.HasPrefix(u, "http://127.0.0.1") || strings.HasPrefix(u, "http://localhost")
}

func (a *App) sessionConfig(tools []ai.ToolDef, system string) (ai.Config, AISettings, error) {
	s, err := a.AISettings()
	if err != nil {
		return ai.Config{}, s, err
	}
	key, err := a.Secrets.Get(aiKeyName(s.PresetID))
	if err != nil {
		return ai.Config{}, s, userErr("还没有填写 AI 模型的 API Key，请先到「设置」里填写")
	}
	return ai.Config{
		Kind: s.Kind, BaseURL: s.BaseURL, Model: s.Model, APIKey: key,
		System: system, Tools: tools, MaxTokens: 8000,
		FallbackModel: s.FallbackModel, EchoReasoning: s.EchoReasoning,
	}, s, nil
}

// TestAI sends a tiny request to check the key and endpoint.
func (a *App) TestAI(ctx context.Context) (string, error) {
	cfg, _, err := a.sessionConfig(nil, "你是连通性测试助手。")
	if err != nil {
		return "", err
	}
	cfg.MaxTokens = 200
	sess, err := ai.NewSession(cfg)
	if err != nil {
		return "", err
	}
	sess.AddUser("请只回复两个字：你好")
	turn, err := sess.Next(ctx)
	if err != nil {
		return "", userErr("连接 AI 模型失败：%v", err)
	}
	return strings.TrimSpace(turn.Text), nil
}

type conversation struct {
	mu    sync.Mutex
	agent *ai.Agent
}

// ChatReply is one answer in the AI 助手 tab.
type ChatReply struct {
	ConversationID string     `json:"conversationId"`
	Reply          ai.Reply   `json:"reply"`
	Cost           float64    `json:"cost"`
	Currency       string     `json:"currency"`
	Plans          []PlanView `json:"plans"` // checklists proposed in this answer
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Chat sends a user message in a conversation, creating it if needed.
// Conversations live in memory for now.
func (a *App) Chat(ctx context.Context, convID, text string) (ChatReply, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return ChatReply{}, userErr("请输入问题")
	}
	cfg, settings, err := a.sessionConfig(a.toolDefs(), systemPrompt)
	if err != nil {
		return ChatReply{}, err
	}
	if settings.MonthlyBudget > 0 {
		spent, err := a.Store.MonthCost()
		if err != nil {
			return ChatReply{}, err
		}
		if spent[settings.Currency] >= settings.MonthlyBudget {
			return ChatReply{}, userErr("本月 AI 花费已达到你设置的上限（%.2f %s），可以在「设置」里调整", settings.MonthlyBudget, settings.Currency)
		}
	}

	a.mu.Lock()
	conv, ok := a.convs[convID]
	if !ok {
		sess, err := ai.NewSession(cfg)
		if err != nil {
			a.mu.Unlock()
			return ChatReply{}, err
		}
		convID = newID()
		conv = &conversation{agent: &ai.Agent{
			Session: sess, Tools: a.tools(), MaxRounds: 10, MaxToolOutput: 12000,
		}}
		a.convs[convID] = conv
	}
	a.mu.Unlock()

	conv.mu.Lock()
	defer conv.mu.Unlock()
	collector := &planCollector{}
	reply, err := conv.agent.Ask(context.WithValue(ctx, planCollectorKey{}, collector), text)
	cost := settings.Cost(reply.Usage)
	if reply.Usage.Input+reply.Usage.Output > 0 {
		_ = a.Store.AddUsage(store.Usage{
			Model: settings.Model, InputTokens: reply.Usage.Input, CachedTokens: reply.Usage.CachedInput,
			OutputTokens: reply.Usage.Output, Cost: cost, Currency: settings.Currency,
		})
	}
	if err != nil {
		var ue *UserError
		if errors.As(err, &ue) || errors.Is(err, context.Canceled) {
			return ChatReply{}, err
		}
		return ChatReply{}, userErr("AI 回答失败：%v", err)
	}
	_ = a.Store.Audit("ai", "ai.chat", settings.Model, fmt.Sprintf("查询 %d 次", len(reply.Steps)))
	out := ChatReply{ConversationID: convID, Reply: reply, Cost: cost, Currency: settings.Currency, Plans: []PlanView{}}
	for _, id := range collector.ids {
		if v, err := a.Plan(id); err == nil {
			out.Plans = append(out.Plans, v)
		}
	}
	return out, nil
}
