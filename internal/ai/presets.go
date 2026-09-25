package ai

// Preset is a ready-made model configuration. Prices are per million
// tokens and were collected from public sources in 2026-09; providers
// change them often, so users can override them in settings.
type Preset struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Note          string  `json:"note"`
	Kind          string  `json:"kind"`
	BaseURL       string  `json:"baseUrl"`
	Model         string  `json:"model"`
	InputPrice    float64 `json:"inputPrice"`  // cache miss
	CachedPrice   float64 `json:"cachedPrice"` // cache hit
	OutputPrice   float64 `json:"outputPrice"`
	Currency      string  `json:"currency"` // CNY | USD
	EchoReasoning bool    `json:"echoReasoning"`
	FallbackModel string  `json:"fallbackModel,omitempty"`
}

// DefaultPresetID is the recommended default: the best price/performance
// for tool-heavy agent work that is reachable from mainland China.
const DefaultPresetID = "deepseek-flash"

// Presets lists the built-in model choices, recommended first.
var Presets = []Preset{
	{ID: "deepseek-flash", Name: "DeepSeek V4.1 Flash（推荐）", Note: "性价比最高：便宜、工具调用能力强。价格按工作日高峰时段估算，其余时段半价",
		Kind: KindOpenAI, BaseURL: "https://api.deepseek.com", Model: "deepseek-flash",
		InputPrice: 2, CachedPrice: 0.04, OutputPrice: 8, Currency: "CNY", EchoReasoning: true},
	{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", Note: "更强但更贵",
		Kind: KindOpenAI, BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro",
		InputPrice: 9, CachedPrice: 0.3, OutputPrice: 27, Currency: "CNY", EchoReasoning: true},
	{ID: "qwen3.7-plus", Name: "通义千问 Qwen3.7-Plus", Note: "阿里云百炼，和 DeepSeek Flash 价格接近",
		Kind: KindOpenAI, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen3.7-plus",
		InputPrice: 2, CachedPrice: 0.4, OutputPrice: 8, Currency: "CNY"},
	{ID: "qwen3.8-max", Name: "通义千问 Qwen3.8-Max", Note: "旗舰，适合做独立审查",
		Kind: KindOpenAI, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen3.8-max",
		InputPrice: 12, CachedPrice: 1.5, OutputPrice: 36, Currency: "CNY"},
	{ID: "qwen3.8-flash", Name: "通义千问 Qwen3.8-Flash", Note: "很便宜，适合简单总结",
		Kind: KindOpenAI, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen3.8-flash",
		InputPrice: 0.8, CachedPrice: 0.16, OutputPrice: 2.7, Currency: "CNY"},
	{ID: "kimi-k2.6", Name: "Kimi K2.6", Kind: KindOpenAI, BaseURL: "https://api.moonshot.cn/v1", Model: "kimi-k2.6",
		InputPrice: 6.5, CachedPrice: 1.1, OutputPrice: 27, Currency: "CNY"},
	{ID: "glm-5.3", Name: "智谱 GLM-5.3", Kind: KindOpenAI, BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-5.3",
		InputPrice: 8, CachedPrice: 2, OutputPrice: 28, Currency: "CNY"},
	{ID: "glm-4.7-flash", Name: "智谱 GLM-4.7-Flash（免费）", Note: "免费，能力较弱",
		Kind: KindOpenAI, BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4.7-flash", Currency: "CNY"},
	{ID: "doubao-seed-2.1-turbo", Name: "豆包 Seed 2.1 Turbo", Kind: KindOpenAI, BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
		Model: "doubao-seed-2-1-turbo-260628", InputPrice: 3, CachedPrice: 0.6, OutputPrice: 15, Currency: "CNY"},
	{ID: "hy3", Name: "腾讯混元 Hy3", Note: "需要腾讯云 TokenHub 的 API Key（不是 SecretId/SecretKey）",
		Kind: KindOpenAI, BaseURL: "https://tokenhub.tencentmaas.com/v1", Model: "hy3",
		InputPrice: 1, CachedPrice: 0.25, OutputPrice: 4, Currency: "CNY"},
	{ID: "minimax-m3", Name: "MiniMax M3", Kind: KindOpenAI, BaseURL: "https://api.minimaxi.com/v1", Model: "MiniMax-M3",
		InputPrice: 2.1, CachedPrice: 0.42, OutputPrice: 8.4, Currency: "CNY"},
	{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", Note: "Claude API 不向中国大陆提供服务，适合海外用户",
		Kind: KindAnthropic, Model: "claude-sonnet-5", InputPrice: 2, CachedPrice: 0.2, OutputPrice: 10, Currency: "USD"},
	{ID: "claude-opus-5", Name: "Claude Opus 5", Note: "Claude API 不向中国大陆提供服务，适合海外用户",
		Kind: KindAnthropic, Model: "claude-opus-5", InputPrice: 5, CachedPrice: 0.5, OutputPrice: 25, Currency: "USD",
		FallbackModel: "claude-opus-4-8"},
	{ID: "claude-haiku-4.5", Name: "Claude Haiku 4.5", Note: "Claude API 不向中国大陆提供服务，适合海外用户",
		Kind: KindAnthropic, Model: "claude-haiku-4-5", InputPrice: 1, CachedPrice: 0.1, OutputPrice: 5, Currency: "USD"},
	{ID: "custom", Name: "自定义（兼容 OpenAI 接口）", Note: "填写服务商提供的接口地址和模型名",
		Kind: KindOpenAI, Currency: "CNY"},
}

// PresetByID returns the preset with the given ID.
func PresetByID(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// Cost estimates what u cost at these prices.
func (p Preset) Cost(u Usage) float64 {
	uncached := u.Input - u.CachedInput
	if uncached < 0 {
		uncached = 0
	}
	return (float64(uncached)*p.InputPrice + float64(u.CachedInput)*p.CachedPrice +
		float64(u.Output)*p.OutputPrice) / 1e6
}
