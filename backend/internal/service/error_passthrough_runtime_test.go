package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyErrorPassthroughRule_NoBoundService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	status, errType, errMsg, matched := applyErrorPassthroughRule(
		c,
		PlatformAnthropic,
		http.StatusUnprocessableEntity,
		[]byte(`{"error":{"message":"invalid schema"}}`),
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	)

	assert.False(t, matched)
	assert.Equal(t, http.StatusBadGateway, status)
	assert.Equal(t, "upstream_error", errType)
	assert.Equal(t, "Upstream request failed", errMsg)
}

// issue #16 Part B(B2):无规则时,上游请求形 4xx(422)默认透传真实状态码 + 上游报文,
// 而非旧的「压成 502 + 通用文案」。
func TestGatewayHandleErrorResponse_NoRulePassesThrough4xx(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := &GatewayService{}
	respBody := []byte(`{"error":{"message":"Invalid schema for field messages"}}`)
	resp := &http.Response{
		StatusCode: http.StatusUnprocessableEntity,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	account := &Account{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account)
	require.Error(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "invalid_request_error", errField["type"])
	assert.Equal(t, "Invalid schema for field messages", errField["message"])
}

// issue #16 Part B(B2):OpenAI 非-failover 此前 422 落 default→502;现与 Anthropic 侧对齐,透传 422。
func TestOpenAIHandleErrorResponse_NoRulePassesThrough4xx(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := &OpenAIGatewayService{}
	respBody := []byte(`{"error":{"message":"Invalid schema for field messages"}}`)
	resp := &http.Response{
		StatusCode: http.StatusUnprocessableEntity,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account, nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "invalid_request_error", errField["type"])
	assert.Equal(t, "Invalid schema for field messages", errField["message"])
}

func TestGeminiWriteGeminiMappedError_NoRuleKeepsDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := &GeminiMessagesCompatService{}
	respBody := []byte(`{"error":{"code":422,"message":"Invalid schema for field messages","status":"INVALID_ARGUMENT"}}`)
	account := &Account{ID: 13, Platform: PlatformGemini, Type: AccountTypeAPIKey}

	err := svc.writeGeminiMappedError(c, account, http.StatusUnprocessableEntity, "req-2", respBody)
	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "invalid_request_error", errField["type"])
	assert.Equal(t, "Upstream request failed", errField["message"])
}

func TestGatewayHandleErrorResponse_AppliesRuleFor422(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	ruleSvc := &ErrorPassthroughService{}
	ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{newNonFailoverPassthroughRule(http.StatusUnprocessableEntity, "invalid schema", http.StatusTeapot, "上游请求失败")})
	BindErrorPassthroughService(c, ruleSvc)

	svc := &GatewayService{}
	respBody := []byte(`{"error":{"message":"Invalid schema for field messages"}}`)
	resp := &http.Response{
		StatusCode: http.StatusUnprocessableEntity,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account)
	require.Error(t, err)
	assert.Equal(t, http.StatusTeapot, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errField["type"])
	assert.Equal(t, "上游请求失败", errField["message"])
}

func TestOpenAIHandleErrorResponse_AppliesRuleFor422(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	ruleSvc := &ErrorPassthroughService{}
	ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{newNonFailoverPassthroughRule(http.StatusUnprocessableEntity, "invalid schema", http.StatusTeapot, "OpenAI上游失败")})
	BindErrorPassthroughService(c, ruleSvc)

	svc := &OpenAIGatewayService{}
	respBody := []byte(`{"error":{"message":"Invalid schema for field messages"}}`)
	resp := &http.Response{
		StatusCode: http.StatusUnprocessableEntity,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Header:     http.Header{},
	}
	account := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account, nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusTeapot, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errField["type"])
	assert.Equal(t, "OpenAI上游失败", errField["message"])
}

func TestGeminiWriteGeminiMappedError_AppliesRuleFor422(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	ruleSvc := &ErrorPassthroughService{}
	ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{newNonFailoverPassthroughRule(http.StatusUnprocessableEntity, "invalid schema", http.StatusTeapot, "Gemini上游失败")})
	BindErrorPassthroughService(c, ruleSvc)

	svc := &GeminiMessagesCompatService{}
	respBody := []byte(`{"error":{"code":422,"message":"Invalid schema for field messages","status":"INVALID_ARGUMENT"}}`)
	account := &Account{ID: 3, Platform: PlatformGemini, Type: AccountTypeAPIKey}

	err := svc.writeGeminiMappedError(c, account, http.StatusUnprocessableEntity, "req-1", respBody)
	require.Error(t, err)
	assert.Equal(t, http.StatusTeapot, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errField["type"])
	assert.Equal(t, "Gemini上游失败", errField["message"])
}

func TestApplyErrorPassthroughRule_SkipMonitoringSetsContextKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	rule := newNonFailoverPassthroughRule(http.StatusBadRequest, "prompt is too long", http.StatusBadRequest, "上下文超限")
	rule.SkipMonitoring = true

	ruleSvc := &ErrorPassthroughService{}
	ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{rule})
	BindErrorPassthroughService(c, ruleSvc)

	_, _, _, matched := applyErrorPassthroughRule(
		c,
		PlatformAnthropic,
		http.StatusBadRequest,
		[]byte(`{"error":{"message":"prompt is too long"}}`),
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	)

	assert.True(t, matched)
	v, exists := c.Get(OpsSkipPassthroughKey)
	assert.True(t, exists, "OpsSkipPassthroughKey should be set when skip_monitoring=true")
	boolVal, ok := v.(bool)
	assert.True(t, ok, "value should be bool")
	assert.True(t, boolVal)
}

func TestApplyErrorPassthroughRule_NoSkipMonitoringDoesNotSetContextKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	rule := newNonFailoverPassthroughRule(http.StatusBadRequest, "prompt is too long", http.StatusBadRequest, "上下文超限")
	rule.SkipMonitoring = false

	ruleSvc := &ErrorPassthroughService{}
	ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{rule})
	BindErrorPassthroughService(c, ruleSvc)

	_, _, _, matched := applyErrorPassthroughRule(
		c,
		PlatformAnthropic,
		http.StatusBadRequest,
		[]byte(`{"error":{"message":"prompt is too long"}}`),
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	)

	assert.True(t, matched)
	_, exists := c.Get(OpsSkipPassthroughKey)
	assert.False(t, exists, "OpsSkipPassthroughKey should NOT be set when skip_monitoring=false")
}

// ---- ResponseCommittedKey: service 层写完错误响应后标记，handler 层检查跳过兜底写入 ----

func TestHandleErrorResponse_SetsResponseCommitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := &GatewayService{}
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"temperature: range: 0..1"}}`))),
		Header:     http.Header{},
	}
	account := &Account{ID: 100, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account)
	require.Error(t, err)
	assert.True(t, IsResponseCommitted(c), "non-failover error path must mark response committed")
	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
}

func TestHandleErrorResponse_PassthroughRuleSetsCommitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	ruleSvc := &ErrorPassthroughService{}
	ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{
		newNonFailoverPassthroughRule(http.StatusBadRequest, "temperature", http.StatusBadRequest, "参数错误"),
	})
	BindErrorPassthroughService(c, ruleSvc)

	svc := &GatewayService{}
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"temperature: range: 0..1"}}`))),
		Header:     http.Header{},
	}
	account := &Account{ID: 200, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account)
	require.Error(t, err)
	assert.True(t, IsResponseCommitted(c), "passthrough rule path must mark response committed")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok, "payload[\"error\"] should be map[string]any")
	assert.Equal(t, "参数错误", errField["message"])
}

func TestOpenAIHandleErrorResponse_SetsResponseCommitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := &OpenAIGatewayService{}
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"rate limit exceeded"}}`))),
		Header:     http.Header{},
	}
	account := &Account{ID: 101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account, nil)
	require.Error(t, err)
	assert.True(t, IsResponseCommitted(c), "OpenAI non-failover path must mark response committed")
}

func TestGeminiWriteGeminiMappedError_SetsResponseCommitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := &GeminiMessagesCompatService{}
	body := []byte(`{"error":{"message":"invalid field"}}`)
	account := &Account{ID: 102, Platform: PlatformGemini, Type: AccountTypeAPIKey}

	err := svc.writeGeminiMappedError(c, account, http.StatusBadRequest, "req-99", body)
	require.Error(t, err)
	assert.True(t, IsResponseCommitted(c), "Gemini path must mark response committed")
}

// TestMapUpstreamErrorDefault 覆盖 issue #16 Part B 的默认映射矩阵:请求形 4xx 透传,其余保留 502/429/503。
func TestMapUpstreamErrorDefault(t *testing.T) {
	tests := []struct {
		status      int
		wantStatus  int
		wantErrType string
		wantPass    bool
	}{
		// 请求形 4xx(Q1 扩展集)→ 透传真实状态码
		{400, 400, "invalid_request_error", true},
		{404, 404, "not_found_error", true},
		{408, 408, "invalid_request_error", true},
		{409, 409, "invalid_request_error", true},
		{413, 413, "invalid_request_error", true},
		{415, 415, "invalid_request_error", true},
		{416, 416, "invalid_request_error", true},
		{422, 422, "invalid_request_error", true},
		// 账号/鉴权/计费/限流/5xx/529 → 保留既有语义,不透传
		{401, http.StatusBadGateway, "upstream_error", false},
		{402, http.StatusBadGateway, "upstream_error", false},
		{403, http.StatusBadGateway, "upstream_error", false},
		{429, http.StatusTooManyRequests, "rate_limit_error", false},
		{529, http.StatusServiceUnavailable, "overloaded_error", false},
		{500, http.StatusBadGateway, "upstream_error", false},
		{502, http.StatusBadGateway, "upstream_error", false},
		{503, http.StatusBadGateway, "upstream_error", false},
		{504, http.StatusBadGateway, "upstream_error", false},
		// 集合外的 4xx(405/406/414/451)与未知码 → 不透传,保留 502
		{405, http.StatusBadGateway, "upstream_error", false},
		{406, http.StatusBadGateway, "upstream_error", false},
		{414, http.StatusBadGateway, "upstream_error", false},
		{451, http.StatusBadGateway, "upstream_error", false},
		{418, http.StatusBadGateway, "upstream_error", false},
	}
	for _, tt := range tests {
		gotStatus, gotErrType, _, gotPass := MapUpstreamErrorDefault(tt.status)
		assert.Equal(t, tt.wantStatus, gotStatus, "status %d -> code", tt.status)
		assert.Equal(t, tt.wantErrType, gotErrType, "status %d -> errType", tt.status)
		assert.Equal(t, tt.wantPass, gotPass, "status %d -> passthrough", tt.status)
	}
}

// TestGatewayFailoverStatusPolicyGuardsRequestShaped4xx locks the issue #19
// boundary: malformed-client upstream 4xx must not trigger account switching.
// 429 is intentionally excluded from this status-only guard: #19 treats it as
// a sliding-window threshold decision, not an unconditional per-response switch.
func TestGatewayFailoverStatusPolicyGuardsRequestShaped4xx(t *testing.T) {
	tests := []struct {
		name string
		got  func(int) bool
	}{
		{
			name: "anthropic gateway",
			got:  (&GatewayService{}).shouldFailoverUpstreamError,
		},
		{
			name: "openai gateway",
			got:  (&OpenAIGatewayService{}).shouldFailoverUpstreamError,
		},
		{
			name: "gemini compat",
			got: func(status int) bool {
				return (&GeminiMessagesCompatService{}).shouldFailoverGeminiUpstreamError(&Account{ID: 9101}, status)
			},
		},
		{
			name: "antigravity gateway",
			got: func(status int) bool {
				return (&AntigravityGatewayService{}).shouldFailoverUpstreamError(&Account{ID: 9102}, status)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, status := range []int{400, 404, 408, 409, 413, 415, 416, 422} {
				assert.False(t, tt.got(status), "request-shaped upstream %d must not fail over", status)
			}
			for _, status := range []int{401, 403, 500, 502, 503, 504, 529} {
				assert.True(t, tt.got(status), "provider/account upstream %d should fail over", status)
			}
		})
	}
}

// TestResolveUpstreamErrorResponse 覆盖共享策略的四段式:静默拒绝/透传规则/请求形 4xx 透传/默认保留。
func TestResolveUpstreamErrorResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func() *gin.Context {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		return c
	}

	t.Run("静默拒绝 → 502 + 客户端文案", func(t *testing.T) {
		c := newCtx()
		body := []byte(`{"error":{"code":"openai_silent_refusal","message":"empty stream"}}`)
		status, errType, msg := ResolveUpstreamErrorResponse(c, PlatformOpenAI, http.StatusOK, body)
		assert.Equal(t, http.StatusBadGateway, status)
		assert.Equal(t, "upstream_error", errType)
		assert.Equal(t, OpenAISilentRefusalClientMessage(), msg)
	})

	t.Run("请求形 4xx 默认透传真实状态码 + 上游报文", func(t *testing.T) {
		c := newCtx()
		body := []byte(`{"error":{"message":"bad field foo"}}`)
		status, errType, msg := ResolveUpstreamErrorResponse(c, PlatformAnthropic, http.StatusUnprocessableEntity, body)
		assert.Equal(t, http.StatusUnprocessableEntity, status)
		assert.Equal(t, "invalid_request_error", errType)
		assert.Equal(t, "bad field foo", msg)
		// 真实上游状态码须记入 context(A2 归因依赖)。
		v, ok := c.Get(OpsUpstreamStatusCodeKey)
		require.True(t, ok)
		assert.Equal(t, http.StatusUnprocessableEntity, v)
	})

	t.Run("请求形 4xx 上游无 message → 兜底文案", func(t *testing.T) {
		c := newCtx()
		status, _, msg := ResolveUpstreamErrorResponse(c, PlatformAnthropic, http.StatusBadRequest, nil)
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "Upstream rejected the request", msg)
	})

	t.Run("5xx 保留 502 语义", func(t *testing.T) {
		c := newCtx()
		status, errType, msg := ResolveUpstreamErrorResponse(c, PlatformAnthropic, http.StatusServiceUnavailable, []byte(`{"error":{"message":"upstream down"}}`))
		assert.Equal(t, http.StatusBadGateway, status)
		assert.Equal(t, "upstream_error", errType)
		assert.Equal(t, "Upstream service temporarily unavailable", msg)
	})

	t.Run("429 保留限流语义", func(t *testing.T) {
		c := newCtx()
		status, errType, _ := ResolveUpstreamErrorResponse(c, PlatformAnthropic, http.StatusTooManyRequests, []byte(`{"error":{"message":"slow down"}}`))
		assert.Equal(t, http.StatusTooManyRequests, status)
		assert.Equal(t, "rate_limit_error", errType)
	})

	t.Run("透传规则命中覆盖默认", func(t *testing.T) {
		c := newCtx()
		ruleSvc := &ErrorPassthroughService{}
		ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{
			newNonFailoverPassthroughRule(http.StatusUnprocessableEntity, "invalid", http.StatusTeapot, "自定义文案"),
		})
		BindErrorPassthroughService(c, ruleSvc)
		status, errType, msg := ResolveUpstreamErrorResponse(c, PlatformAnthropic, http.StatusUnprocessableEntity, []byte(`{"error":{"message":"invalid schema"}}`))
		assert.Equal(t, http.StatusTeapot, status)
		assert.Equal(t, "upstream_error", errType)
		assert.Equal(t, "自定义文案", msg)
	})
}

func newNonFailoverPassthroughRule(statusCode int, keyword string, respCode int, customMessage string) *model.ErrorPassthroughRule {
	return &model.ErrorPassthroughRule{
		ID:              1,
		Name:            "non-failover-rule",
		Enabled:         true,
		Priority:        1,
		ErrorCodes:      []int{statusCode},
		Keywords:        []string{keyword},
		MatchMode:       model.MatchModeAll,
		PassthroughCode: false,
		ResponseCode:    &respCode,
		PassthroughBody: false,
		CustomMessage:   &customMessage,
	}
}
