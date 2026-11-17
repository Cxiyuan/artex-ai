package server

import (
	"net/http"
	"strings"
)

// 本文件是「新建模型配置」表单的预置模板目录（GET /api/llm/presets）。
//
// 一个预置只回答一个问题：**这个端点该怎么填**（格式 / Base URL / Key / 模型名长什么样）。
// 它不写库、不建配置，前端点一下把表单填好，用户仍可逐项改——配置的唯一真值始终是
// llm_profiles 那一行，所以升级预置列表不会影响任何已存配置。
//
// 顺序即表单里的展示顺序：置顶的是本机网关（magpie），因为它是唯一"装上就能用、
// 不用先申请 Key"的一条路。

// LLMPreset is one starting point for the profile form.
type LLMPreset struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Format string `json:"format"` // anthropic | openai | openai-responses，与 llm.Format 一致
	// BaseURL 是 norma 会直接拼端点路径的那一段，所以语义随格式而变：
	//   openai          → 拼 "/chat/completions"
	//   openai-responses→ 拼 "/responses"
	//   anthropic       → 拼 "/v1/messages"
	BaseURL string `json:"base_url"`
	// APIKey 是表单预填值。空 = 必须由用户粘贴真实 Key；非空 = 占位 token
	// （本机网关只认"有没有值"，不校验内容）。
	APIKey string `json:"api_key"`
	// ModelPlaceholder 是模型输入框的 placeholder（点击预置时若模型为空会填进去）。
	ModelPlaceholder string `json:"model_placeholder"`
	// ModelHint 说明该端点的模型名格式，是预置里最容易填错的一项。
	ModelHint string `json:"model_hint"`
	// Notes 是预置下方的一行情境说明（谁在用、要不要额外准备）。
	Notes string `json:"notes"`
	// DocURL 指向上游文档，前端渲染成可点链接（可选）。
	DocURL string `json:"doc_url,omitempty"`
}

const (
	magpiePresetID = "magpie"

	// magpie 的网关默认只听 127.0.0.1:3425（端口可在 magpie 设置里改）。三种协议都有
	// 独立入口，故各给一条预置，其中 OpenAI Chat Completions 是默认那条——本项目所有
	// agent 的工具调用都走 messages/tools，Chat Completions 的兼容面最广。
	magpieBaseURL          = "http://127.0.0.1:3425/v1" // + /chat/completions、+ /responses
	magpieAnthropicBaseURL = "http://127.0.0.1:3425"    // + /v1/messages

	// magpieToken 是给"必须有 Bearer token"的客户端用的占位值：网关只在 loopback 上
	// 监听时不校验它。跨机共享（Settings → Share on local network）时要换成真正的
	// gateway key，那时它就不再是占位符了。
	magpieToken = "magpie"

	magpieDocURL = "https://github.com/yetone/magpie"
)

// LLMPresets returns the preset catalog in display order. A fresh slice is built on
// every call so a caller can't mutate the shared table.
func LLMPresets() []LLMPreset {
	return []LLMPreset{
		{
			ID:               magpiePresetID,
			Label:            "Magpie 本地网关（OpenAI Chat）",
			Format:           "openai",
			BaseURL:          magpieBaseURL,
			APIKey:           magpieToken,
			ModelPlaceholder: "deepseek/deepseek-v4-pro",
			ModelHint:        "provider/model，如 deepseek/deepseek-v4-pro、moonshot/kimi-k2.5；填 group/<id> 走路由组，额度耗尽时自动换号",
			Notes:            "magpie 在本机跑一个网关（默认 127.0.0.1:3425），把 OpenAI / Anthropic / Responses 三种协议翻译到你添加的订阅或 API Key。Key 填 magpie 即可（loopback 不校验）。模型列表点右侧刷新按钮可直接拉取。",
			DocURL:           magpieDocURL,
		},
		{
			ID:               "magpie-anthropic",
			Label:            "Magpie 本地网关（Anthropic Messages）",
			Format:           "anthropic",
			BaseURL:          magpieAnthropicBaseURL,
			APIKey:           magpieToken,
			ModelPlaceholder: "claude/claude-opus-5-5",
			ModelHint:        "provider/model（与 Chat 入口同名的同一批模型）；想用 Anthropic 协议原样转发时选这条",
			Notes:            "同一条网关的 Anthropic 入口。仅当你要保留 Anthropic 的 thinking / cache 字段原样时才需要，其余情况用上面那条 Chat 入口即可。",
			DocURL:           magpieDocURL,
		},
		{
			ID:               "magpie-responses",
			Label:            "Magpie 本地网关（OpenAI Responses）",
			Format:           "openai-responses",
			BaseURL:          magpieBaseURL,
			APIKey:           magpieToken,
			ModelPlaceholder: "codex/gpt-6-codex",
			ModelHint:        "provider/model；Responses 入口适合接到 magpie 的 Codex / ChatGPT 订阅账号上",
			Notes:            "同一条网关的 Responses 入口，适合走 ChatGPT/Codex 订阅的模型；本项目的工具调用在该协议下同样可用。",
			DocURL:           magpieDocURL,
		},
		{
			ID:               "anthropic",
			Label:            "Anthropic 官方",
			Format:           "anthropic",
			BaseURL:          "https://api.anthropic.com",
			ModelPlaceholder: "claude-opus-4-8",
			ModelHint:        "官方模型 ID，如 claude-opus-4-8",
			Notes:            "需要真实 API Key（sk-ant-…）。",
		},
		{
			ID:               "openai",
			Label:            "OpenAI 官方（Chat Completions）",
			Format:           "openai",
			BaseURL:          "https://api.openai.com/v1",
			ModelPlaceholder: "gpt-5.1",
			ModelHint:        "官方模型 ID，如 gpt-5.1",
			Notes:            "推理模型（o 系列 / GPT-5）需要把「上限字段名」改成 max_completion_tokens。",
		},
	}
}

// llmBaseRoot normalizes a base URL for preset matching: trimmed, no trailing
// slash, and any API path a profile may already carry stripped. This is the same
// tolerance pgListModels applies when it builds candidate endpoints, so a user who
// pasted a full ".../v1/chat/completions" still matches the preset it came from.
func llmBaseRoot(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.TrimRight(s, "/")
	for _, suffix := range []string{"/chat/completions", "/v1/messages", "/responses"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.TrimRight(s, "/")
}

// presetKeyForBaseURL returns the placeholder token a preset pairs with this base
// URL, if any. Used by the model-list endpoint: a loopback gateway like magpie
// needs *a* token to be sent but the user has nothing real to paste, so refusing
// to list models until they type something would be a dead end.
func presetKeyForBaseURL(raw string) (string, bool) {
	root := llmBaseRoot(raw)
	if root == "" {
		return "", false
	}
	for _, p := range LLMPresets() {
		if p.APIKey == "" {
			continue
		}
		if llmBaseRoot(p.BaseURL) == root {
			return p.APIKey, true
		}
	}
	return "", false
}

// GET /api/llm/presets — the profile form's starting points.
func (s *Server) llmPresets(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": LLMPresets()})
}
