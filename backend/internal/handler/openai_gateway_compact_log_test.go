package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

var handlerStructuredLogCaptureMu sync.Mutex

type handlerInMemoryLogSink struct {
	mu     sync.Mutex
	events []*logger.LogEvent
}

func (s *handlerInMemoryLogSink) WriteLogEvent(event *logger.LogEvent) {
	if event == nil {
		return
	}
	cloned := *event
	if event.Fields != nil {
		cloned.Fields = make(map[string]any, len(event.Fields))
		for k, v := range event.Fields {
			cloned.Fields[k] = v
		}
	}
	s.mu.Lock()
	s.events = append(s.events, &cloned)
	s.mu.Unlock()
}

func (s *handlerInMemoryLogSink) ContainsMessageAtLevel(substr, level string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	wantLevel := strings.ToLower(strings.TrimSpace(level))
	for _, ev := range s.events {
		if ev == nil {
			continue
		}
		if strings.Contains(ev.Message, substr) && strings.ToLower(strings.TrimSpace(ev.Level)) == wantLevel {
			return true
		}
	}
	return false
}

func (s *handlerInMemoryLogSink) ContainsFieldValue(field, substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.events {
		if ev == nil || ev.Fields == nil {
			continue
		}
		if v, ok := ev.Fields[field]; ok && strings.Contains(fmt.Sprint(v), substr) {
			return true
		}
	}
	return false
}

func (s *handlerInMemoryLogSink) FieldValueForMessage(message, field string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event == nil || event.Message != message || event.Fields == nil {
			continue
		}
		if value, ok := event.Fields[field]; ok {
			return value, true
		}
	}
	return nil, false
}

func captureHandlerStructuredLog(t *testing.T) (*handlerInMemoryLogSink, func()) {
	t.Helper()
	handlerStructuredLogCaptureMu.Lock()

	err := logger.Init(logger.InitOptions{
		Level:       "debug",
		Format:      "json",
		ServiceName: "sub2api",
		Environment: "test",
		Output: logger.OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Sampling: logger.SamplingOptions{Enabled: false},
	})
	require.NoError(t, err)

	sink := &handlerInMemoryLogSink{}
	logger.SetSink(sink)
	return sink, func() {
		logger.SetSink(nil)
		handlerStructuredLogCaptureMu.Unlock()
	}
}

func TestIsOpenAIRemoteCompactPath(t *testing.T) {
	require.False(t, isOpenAIRemoteCompactPath(nil))

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	require.True(t, isOpenAIRemoteCompactPath(c))

	c.Request = httptest.NewRequest(http.MethodPost, "/responses/compact/", nil)
	require.True(t, isOpenAIRemoteCompactPath(c))

	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	require.False(t, isOpenAIRemoteCompactPath(c))
}

// 原生 remote compaction v2（流式 + compaction_trigger）必须留在裸 /responses 上。
// 上游已下线 legacy unary /responses/compact 并直接返回 404（#5598/#5624），
// 一旦被提升过去，Codex 的压缩回合会稳定失败。
func TestNormalizeOpenAIResponsesCompactRequest_NativeV2StaysOnResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{}`))
	c.Set(ctxKeyInboundEndpoint, EndpointResponses)
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"compaction_trigger"}],"stream":true,"prompt_cache_key":"pc_123","store":true}`)

	h := &OpenAIGatewayHandler{}
	normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)

	require.True(t, ok)
	// 路径与入站端点保持原样，不进入 legacy compact 链路。
	require.Equal(t, "/openai/v1/responses", c.Request.URL.Path)
	require.False(t, service.IsOpenAIResponsesCompactPath(c))
	require.Equal(t, EndpointResponses, GetInboundEndpoint(c))
	// body 原样透传：不剥离 stream/prompt_cache_key/store，也不标记 SSE 桥接。
	require.Equal(t, body, normalized)
	require.True(t, gjson.GetBytes(normalized, "stream").Bool())
	require.Equal(t, "pc_123", gjson.GetBytes(normalized, "prompt_cache_key").String())
	require.True(t, gjson.GetBytes(normalized, "store").Bool())
	_, marked := c.Get(service.OpenAICompactClientStreamKeyForTest())
	require.False(t, marked)
}

func TestIsOpenAIRemoteCompactionV2Request(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want bool
	}{
		{name: "streaming compaction trigger", body: `{"input":[{"type":"compaction_trigger"}],"stream":true}`, want: true},
		{name: "non-streaming compaction trigger", body: `{"input":[{"type":"compaction_trigger"}],"stream":false}`, want: false},
		{name: "stream omitted", body: `{"input":[{"type":"compaction_trigger"}]}`, want: false},
		{name: "invalid stream type", body: `{"input":[{"type":"compaction_trigger"}],"stream":"yes"}`, want: false},
		{name: "streaming without trigger", body: `{"input":"summarize","stream":true}`, want: false},
	} {
		require.Equal(t, test.want, isOpenAIRemoteCompactionV2Request([]byte(test.body)), test.name)
	}
}

func TestOpenAIResponsesRequiredCapabilityForRequest(t *testing.T) {
	// 原生 v2 与 legacy compact 都只能由支持 Responses 端点的账号承载。
	require.Equal(t, service.OpenAIEndpointCapabilityResponses,
		openAIResponsesRequiredCapabilityForRequest(false, true, service.PlatformOpenAI))
	// 非 OpenAI 平台不套用该收紧：Grok 走自己的 Responses 兼容层。
	require.Equal(t, service.OpenAIEndpointCapabilityChatCompletions,
		openAIResponsesRequiredCapabilityForRequest(false, true, service.PlatformGrok))
	// 不需要 Responses 时，保持既有的「按生图意图判定」行为。
	require.Equal(t, service.OpenAIEndpointCapabilityChatCompletions,
		openAIResponsesRequiredCapabilityForRequest(false, false, service.PlatformOpenAI))
	require.Equal(t, service.OpenAIEndpointCapabilityResponses,
		openAIResponsesRequiredCapabilityForRequest(true, false, service.PlatformOpenAI))
}

func TestIsBareOpenAIResponsesPath(t *testing.T) {
	require.False(t, isBareOpenAIResponsesPath(nil))

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	for _, test := range []struct {
		path string
		want bool
	}{
		{path: "/v1/responses", want: true},
		{path: "/openai/v1/responses", want: true},
		{path: "/responses", want: true},
		{path: "/backend-api/codex/responses", want: true},
		{path: "/v1/responses/", want: true},
		{path: "/v1/responses/compact", want: false},
		{path: "/v1/responses/resp_123/cancel", want: false},
		{path: "/v1/chat/responses", want: false},
	} {
		c.Request = httptest.NewRequest(http.MethodPost, test.path, nil)
		require.Equal(t, test.want, isBareOpenAIResponsesPath(c), test.path)
	}
}

func TestNormalizeOpenAIResponsesCompactRequest_PathBasedSetsInboundCompact(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/responses/compact", strings.NewReader(`{}`))
	c.Set(ctxKeyInboundEndpoint, EndpointResponses)
	body := []byte(`{"model":"gpt-5.4","input":"summarize","stream":true}`)

	h := &OpenAIGatewayHandler{}
	normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)

	require.True(t, ok)
	require.Equal(t, "/responses/compact", c.Request.URL.Path)
	require.Equal(t, EndpointResponsesCompact, GetInboundEndpoint(c))
	require.False(t, gjson.GetBytes(normalized, "stream").Exists())
	_, marked := c.Get(service.OpenAICompactClientStreamKeyForTest())
	require.False(t, marked)
}

func TestNormalizeOpenAIResponsesCompactRequest_BodySignalWithoutClientStreamDoesNotMarkBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	c.Set(ctxKeyInboundEndpoint, EndpointResponses)
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"compaction_trigger"}],"stream":false}`)

	h := &OpenAIGatewayHandler{}
	_, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)

	require.True(t, ok)
	require.Equal(t, "/v1/responses/compact", c.Request.URL.Path)
	_, marked := c.Get(service.OpenAICompactClientStreamKeyForTest())
	require.False(t, marked)
}

func TestNormalizeOpenAIResponsesCompactRequest_BodySignalDoesNotPromoteSubresources(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/resp_123/cancel", strings.NewReader(`{}`))
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"compaction_trigger"}],"stream":true}`)

	h := &OpenAIGatewayHandler{}
	normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)

	require.True(t, ok)
	require.Equal(t, body, normalized)
	require.Equal(t, "/v1/responses/resp_123/cancel", c.Request.URL.Path)
	require.False(t, service.IsOpenAIResponsesCompactPath(c))
}

func TestLogOpenAIRemoteCompactOutcome_Succeeded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.125.0")
	c.Set(opsModelKey, "gpt-5.3-codex")
	c.Set(opsAccountIDKey, int64(123))
	c.Header("x-request-id", "rid-compact-ok")
	c.Status(http.StatusOK)

	h := &OpenAIGatewayHandler{}
	h.logOpenAIRemoteCompactOutcome(c, time.Now().Add(-8*time.Millisecond))

	require.True(t, logSink.ContainsMessageAtLevel("codex.remote_compact.succeeded", "info"))
	require.True(t, logSink.ContainsFieldValue("compact_outcome", "succeeded"))
	require.True(t, logSink.ContainsFieldValue("status_code", "200"))
	require.True(t, logSink.ContainsFieldValue("path", "/v1/responses/compact"))
	require.True(t, logSink.ContainsFieldValue("request_model", "gpt-5.3-codex"))
	require.True(t, logSink.ContainsFieldValue("account_id", "123"))
	require.True(t, logSink.ContainsFieldValue("upstream_request_id", "rid-compact-ok"))
}

func TestLogOpenAIRemoteCompactOutcome_Failed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/responses/compact", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.125.0")
	c.Status(http.StatusBadGateway)

	h := &OpenAIGatewayHandler{}
	h.logOpenAIRemoteCompactOutcome(c, time.Now())

	require.True(t, logSink.ContainsMessageAtLevel("codex.remote_compact.failed", "warn"))
	require.True(t, logSink.ContainsFieldValue("compact_outcome", "failed"))
	require.True(t, logSink.ContainsFieldValue("status_code", "502"))
	require.True(t, logSink.ContainsFieldValue("path", "/responses/compact"))
}

func TestLogOpenAIRemoteCompactOutcome_NonCompactSkips(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Status(http.StatusOK)

	h := &OpenAIGatewayHandler{}
	h.logOpenAIRemoteCompactOutcome(c, time.Now())

	require.False(t, logSink.ContainsMessageAtLevel("codex.remote_compact.succeeded", "info"))
	require.False(t, logSink.ContainsMessageAtLevel("codex.remote_compact.failed", "warn"))
}

func TestOpenAIResponses_CompactUnauthorizedLogsFailed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5.3-codex"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.125.0")

	h := &OpenAIGatewayHandler{}
	h.Responses(c)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.True(t, logSink.ContainsMessageAtLevel("codex.remote_compact.failed", "warn"))
	require.True(t, logSink.ContainsFieldValue("status_code", "401"))
	require.True(t, logSink.ContainsFieldValue("path", "/v1/responses/compact"))
}
