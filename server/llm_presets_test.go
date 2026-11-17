package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 预置目录是给表单用的静态数据，但它一旦写错（格式拼错、Base URL 少一段、模型名
// 说明漏了 provider/），用户拿到的就是一条跑不起来的配置——所以这里把"一个预置
// 必须自洽"的约束钉成用例。
func TestLLMPresetsWellFormed(t *testing.T) {
	presets := LLMPresets()
	if len(presets) == 0 {
		t.Fatal("presets 为空")
	}
	formats := map[string]bool{"openai": true, "anthropic": true, "openai-responses": true}
	seen := map[string]bool{}
	for _, p := range presets {
		if p.ID == "" || p.Label == "" {
			t.Fatalf("预置缺 id/label: %+v", p)
		}
		if seen[p.ID] {
			t.Fatalf("预置 id 重复: %s", p.ID)
		}
		seen[p.ID] = true
		if !formats[p.Format] {
			t.Fatalf("预置 %s 的 format=%q 不是 norma 支持的格式", p.ID, p.Format)
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			t.Fatalf("预置 %s 的 base_url 不可用: %q (err=%v)", p.ID, p.BaseURL, err)
		}
		// norma 直接拼 "/chat/completions" 等路径；结尾带斜杠会拼出 "//"。
		if strings.HasSuffix(p.BaseURL, "/") {
			t.Fatalf("预置 %s 的 base_url 不应以斜杠结尾: %q", p.ID, p.BaseURL)
		}
		if p.ModelPlaceholder == "" || p.ModelHint == "" || p.Notes == "" {
			t.Fatalf("预置 %s 缺模型示例/说明文案", p.ID)
		}
	}
}

// magpie 的三个协议入口各自该填什么，是这次接入最容易搞错的地方：Base URL 的
// 语义随 format 变（openai 拼 /chat/completions，anthropic 拼 /v1/messages）。
func TestMagpiePresets(t *testing.T) {
	byID := map[string]LLMPreset{}
	for _, p := range LLMPresets() {
		byID[p.ID] = p
	}
	chat, ok := byID[magpiePresetID]
	if !ok {
		t.Fatal("缺少 magpie 预置")
	}
	if chat.Format != "openai" {
		t.Fatalf("magpie 默认入口应为 openai(chat completions)，实际 %q", chat.Format)
	}
	if chat.BaseURL != "http://127.0.0.1:3425/v1" {
		t.Fatalf("magpie Base URL 应为 http://127.0.0.1:3425/v1，实际 %q", chat.BaseURL)
	}
	if chat.APIKey != magpieToken {
		t.Fatalf("magpie 应预填占位 token %q，实际 %q", magpieToken, chat.APIKey)
	}
	// 模型名是 magpie 目录里的 provider/model，也支持 group/<id> 走路由组。
	if !strings.Contains(chat.ModelHint, "provider/model") || !strings.Contains(chat.ModelHint, "group/") {
		t.Fatalf("magpie 的模型填法说明不全: %q", chat.ModelHint)
	}
	if chat.DocURL == "" {
		t.Fatal("magpie 预置应带上游文档链接")
	}

	anth, ok := byID["magpie-anthropic"]
	if !ok || anth.Format != "anthropic" || anth.BaseURL != magpieAnthropicBaseURL {
		t.Fatalf("magpie 的 Anthropic 入口不正确: %+v", anth)
	}
	resp, ok := byID["magpie-responses"]
	if !ok || resp.Format != "openai-responses" || resp.BaseURL != magpieBaseURL {
		t.Fatalf("magpie 的 Responses 入口不正确: %+v", resp)
	}
}

// 用户可能把整条端点 URL（含 /chat/completions 或 /v1/messages）粘进 Base URL
// 输入框；预置匹配要能容忍这种写法，否则模型列表会白报「未提供 API Key」。
func TestPresetKeyForBaseURL(t *testing.T) {
	cases := []struct {
		in      string
		wantKey string
		wantOK  bool
	}{
		{"http://127.0.0.1:3425/v1", magpieToken, true},
		{"http://127.0.0.1:3425/v1/", magpieToken, true},
		{"http://127.0.0.1:3425", magpieToken, true}, // anthropic 入口
		{"http://127.0.0.1:3425/v1/chat/completions", magpieToken, true},
		{"http://127.0.0.1:3425/v1/responses", magpieToken, true},
		{"  http://127.0.0.1:3425/v1  ", magpieToken, true},
		{"https://api.openai.com/v1", "", false}, // 官方端点必须真的填 Key
		{"", "", false},
	}
	for _, c := range cases {
		key, ok := presetKeyForBaseURL(c.in)
		if ok != c.wantOK || key != c.wantKey {
			t.Fatalf("presetKeyForBaseURL(%q) = (%q,%v)，期望 (%q,%v)", c.in, key, ok, c.wantKey, c.wantOK)
		}
	}
}

// 端点本身不依赖 DB：handler 只序列化静态目录，所以这里用零值 Server 直接测。
func TestLLMPresetsHandler(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.llmPresets(rec, httptest.NewRequest(http.MethodGet, "/api/llm/presets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200", rec.Code)
	}
	var body struct {
		Presets []LLMPreset `json:"presets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if len(body.Presets) == 0 || body.Presets[0].ID != magpiePresetID {
		t.Fatalf("预置列表应以 magpie 置顶，实际 %+v", body.Presets)
	}
}
