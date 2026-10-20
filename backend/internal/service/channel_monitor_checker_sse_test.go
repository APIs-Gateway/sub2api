//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---- extractSSEText 纯函数测试（各 provider/apiMode 的增量聚合） ----

func TestExtractSSEText_Anthropic(t *testing.T) {
	raw := strings.Join([]string{
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`,
		``,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	got := extractSSEText(MonitorProviderAnthropic, MonitorAPIModeChatCompletions, []byte(raw))
	if got != "Hello world" {
		t.Errorf("anthropic SSE aggregate = %q, want %q", got, "Hello world")
	}
}

func TestExtractSSEText_OpenAIChat_StopsAtDone(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hi"}}]}`,
		`data: {"choices":[{"delta":{"content":" there"}}]}`,
		`data: [DONE]`,
		`data: {"choices":[{"delta":{"content":" IGNORED"}}]}`, // [DONE] 之后不再聚合
	}, "\n")

	got := extractSSEText(MonitorProviderOpenAI, MonitorAPIModeChatCompletions, []byte(raw))
	if got != "Hi there" {
		t.Errorf("openai chat SSE aggregate = %q, want %q", got, "Hi there")
	}
}

func TestExtractSSEText_OpenAIResponses(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"4"}`,
		`data: {"type":"response.output_text.delta","delta":"2"}`,
		`data: {"type":"response.completed"}`,
	}, "\n")

	got := extractSSEText(MonitorProviderOpenAI, MonitorAPIModeResponses, []byte(raw))
	if got != "42" {
		t.Errorf("openai responses SSE aggregate = %q, want %q", got, "42")
	}
}

func TestExtractSSEText_Gemini_WrappedAndUnwrapped(t *testing.T) {
	rawStudio := `data: {"candidates":[{"content":{"parts":[{"text":"7"}]}}]}`
	if got := extractSSEText(MonitorProviderGemini, MonitorAPIModeChatCompletions, []byte(rawStudio)); got != "7" {
		t.Errorf("gemini (AI Studio) aggregate = %q, want %q", got, "7")
	}

	rawCLI := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"7"}]}}]}}`
	if got := extractSSEText(MonitorProviderGemini, MonitorAPIModeChatCompletions, []byte(rawCLI)); got != "7" {
		t.Errorf("gemini (CLI wrapped) aggregate = %q, want %q", got, "7")
	}
}

func TestExtractSSEText_ToleratesTruncatedAndHeartbeatLines(t *testing.T) {
	raw := strings.Join([]string{
		`: heartbeat`, // 非 data 行
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}`,
		`data: {"type":"content_block_d`, // 截断的 JSON，应被跳过而非 panic
	}, "\n")

	got := extractSSEText(MonitorProviderAnthropic, MonitorAPIModeChatCompletions, []byte(raw))
	if got != "ok" {
		t.Errorf("aggregate with noise = %q, want %q", got, "ok")
	}
}

func TestExtractSSEText_EmptyStream(t *testing.T) {
	if got := extractSSEText(MonitorProviderAnthropic, MonitorAPIModeChatCompletions, []byte("")); got != "" {
		t.Errorf("empty stream aggregate = %q, want empty", got)
	}
}

// ---- buildRequestBody: SSE 模式注入 stream 标志 ----

func TestBuildRequestBody_SSEInjectsStream_OffMode(t *testing.T) {
	adapter := providerOpenAIChatAdapter
	body, err := buildRequestBody(adapter, MonitorProviderOpenAI, MonitorAPIModeChatCompletions, "gpt-x", "prompt", &CheckOptions{
		ResponseFormat: MonitorResponseFormatSSE,
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if parsed["stream"] != true {
		t.Errorf("sse off-mode should inject stream=true, got %v", parsed["stream"])
	}
}

func TestBuildRequestBody_SSEDoesNotInjectStreamForGemini(t *testing.T) {
	adapter := providerAdapters[MonitorProviderGemini]
	body, err := buildRequestBody(adapter, MonitorProviderGemini, MonitorAPIModeChatCompletions, "gemini-x", "prompt", &CheckOptions{
		ResponseFormat: MonitorResponseFormatSSE,
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if _, ok := parsed["stream"]; ok {
		t.Error("gemini sse should not inject body stream flag (uses URL streamGenerateContent)")
	}
}

func TestBuildRequestBody_SSEReplaceModeLeavesUserBody(t *testing.T) {
	userBody := map[string]any{
		"model":    "x",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"stream":   true,
	}
	body, err := buildRequestBody(providerAdapters[MonitorProviderAnthropic], MonitorProviderAnthropic, MonitorAPIModeChatCompletions, "claude-x", "prompt", &CheckOptions{
		ResponseFormat:   MonitorResponseFormatSSE,
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride:     userBody,
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if parsed["model"] != "x" {
		t.Errorf("replace mode should keep user model, got %v", parsed["model"])
	}
}

// ---- monitorRequestPath: Gemini SSE 切换到 streamGenerateContent ----

func TestMonitorRequestPath_GeminiSSEUsesStreamEndpoint(t *testing.T) {
	adapter := providerAdapters[MonitorProviderGemini]
	got := monitorRequestPath(adapter, MonitorProviderGemini, "gemini-x", MonitorResponseFormatSSE)
	want := fmt.Sprintf(providerGeminiStreamPathTemplate, "gemini-x")
	if got != want {
		t.Errorf("gemini sse path = %q, want %q", got, want)
	}
	// json 模式仍走非流式 path
	if jsonPath := monitorRequestPath(adapter, MonitorProviderGemini, "gemini-x", MonitorResponseFormatJSON); jsonPath != fmt.Sprintf(providerGeminiPathTemplate, "gemini-x") {
		t.Errorf("gemini json path = %q, want generateContent", jsonPath)
	}
}

// ---- runCheckForModel 端到端：SSE 上游 ----

// sseAnthropicHandler 模拟一个返回 SSE 事件流的 Anthropic 上游。
type sseAnthropicHandler struct {
	streamText    string // 固定流式回这段文本；echoChallenge=true 时忽略
	echoChallenge bool   // 从请求 messages[0].content 提取 challenge 并回正确答案
	lastBody      map[string]any
}

func (h *sseAnthropicHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	h.lastBody = body

	text := h.streamText
	if h.echoChallenge {
		prompt := ""
		if msgs, ok := body["messages"].([]any); ok && len(msgs) > 0 {
			if m, ok := msgs[0].(map[string]any); ok {
				prompt, _ = m["content"].(string)
			}
		}
		text = answerFromChallengePrompt(prompt)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if text != "" {
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", text)
	}
	_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
}

func setupFakeSSEAnthropic(t *testing.T, h *sseAnthropicHandler) string {
	t.Helper()
	swapMonitorHTTPClient(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestRunCheckForModel_ReplaceSSE_StreamedTextIsOperational(t *testing.T) {
	// 复现用户场景：replace 模式 + stream:true，上游回 SSE。
	// json 模式下 gjson 抽取为空会误报 "empty text"；sse 模式正确聚合 → operational。
	h := &sseAnthropicHandler{streamText: "streamed reply"}
	endpoint := setupFakeSSEAnthropic(t, h)

	opts := &CheckOptions{
		ResponseFormat:   MonitorResponseFormatSSE,
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride: map[string]any{
			"model":    "claude-x",
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			"stream":   true,
		},
	}
	res := runCheckForModel(context.Background(), MonitorProviderAnthropic, endpoint, "sk-fake", "claude-x", opts)

	if res.Status != MonitorStatusOperational {
		t.Fatalf("replace+sse with streamed text should be operational, got status=%s message=%q", res.Status, res.Message)
	}
}

func TestRunCheckForModel_ReplaceSSE_EmptyStreamIsFailed(t *testing.T) {
	// SSE 流里没有任何文本增量 → 聚合为空 → replace 模式判定 failed。
	h := &sseAnthropicHandler{streamText: ""}
	endpoint := setupFakeSSEAnthropic(t, h)

	opts := &CheckOptions{
		ResponseFormat:   MonitorResponseFormatSSE,
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride: map[string]any{
			"model":    "claude-x",
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			"stream":   true,
		},
	}
	res := runCheckForModel(context.Background(), MonitorProviderAnthropic, endpoint, "sk-fake", "claude-x", opts)

	if res.Status != MonitorStatusFailed {
		t.Fatalf("replace+sse with empty stream should be failed, got status=%s", res.Status)
	}
}

func TestRunCheckForModel_OffSSE_ChallengeStillValidated(t *testing.T) {
	// 非 replace 的 sse：默认 body 带 challenge，上游流式回正确答案 → 仍走 challenge 校验。
	h := &sseAnthropicHandler{echoChallenge: true}
	endpoint := setupFakeSSEAnthropic(t, h)

	opts := &CheckOptions{ResponseFormat: MonitorResponseFormatSSE}
	res := runCheckForModel(context.Background(), MonitorProviderAnthropic, endpoint, "sk-fake", "claude-x", opts)

	if res.Status != MonitorStatusOperational {
		t.Fatalf("off+sse with correct streamed challenge answer should be operational, got status=%s message=%q", res.Status, res.Message)
	}
	// 默认 body 应被注入 stream=true
	if h.lastBody["stream"] != true {
		t.Errorf("off+sse default body should carry stream=true, got %v", h.lastBody["stream"])
	}
}
