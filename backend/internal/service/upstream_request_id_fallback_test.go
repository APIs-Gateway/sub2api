//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// opsEventsOf 取 ctx 里已记录的 ops 上游错误事件（至少一条）。
func opsEventsOf(t *testing.T, c *gin.Context) []*OpsUpstreamErrorEvent {
	t.Helper()
	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok, "expected ops upstream error events")
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.NotEmpty(t, events)
	return events
}

// cfRayOnlyHeader 模拟只过 Cloudflare、不带任何 request id 头的中转响应。
func cfRayOnlyHeader() http.Header {
	return http.Header{"Cf-Ray": []string{"8a1b2c3d4e5f-HKG"}, "Content-Type": []string{"application/json"}}
}

// ---- ops_upstream_context.go：纯函数补齐 ----

// 五个兼容头名各自单独存在时都能取到；按顺序两两同时存在取前者。
func TestUpstreamRequestIDFromHeader_EachNameAloneAndPriorityChain(t *testing.T) {
	for i, name := range upstreamRequestIDHeaderNames {
		h := http.Header{}
		h.Set(name, " id-"+name+" ")
		require.Equal(t, "id-"+name, upstreamRequestIDFromHeader(h), "单独存在: %s", name)

		if i+1 < len(upstreamRequestIDHeaderNames) {
			next := upstreamRequestIDHeaderNames[i+1]
			h.Set(next, "id-"+next)
			require.Equal(t, "id-"+name, upstreamRequestIDFromHeader(h), "%s 优先于 %s", name, next)
		}
	}
	require.Equal(t, []string{"x-request-id", "x-oneapi-request-id", "x-rixapi-request-id", "x-amzn-requestid", "cf-ray"}, upstreamRequestIDHeaderNames)

	// 全部存在但都是空白：返回空。
	blank := http.Header{}
	for _, name := range upstreamRequestIDHeaderNames {
		blank.Set(name, "   ")
	}
	require.Empty(t, upstreamRequestIDFromHeader(blank))
	require.Empty(t, upstreamRequestIDFromHeader(nil))
}

func TestUpstreamRequestIDFromErrorBody_EdgeCases(t *testing.T) {
	// error.request_id 空白时落到顶层 request_id。
	require.Equal(t, "top-000001", upstreamRequestIDFromErrorBody(`{"error":{"request_id":"   "},"request_id":"top-000001"}`))
	// error.request_id 空白、顶层缺失时落到 message 正则。
	require.Equal(t, "msg-000001", upstreamRequestIDFromErrorBody(`{"error":{"request_id":"","message":"boom request_id:msg-000001"}}`))
	// 顶层 request_id 非字符串（对象）忽略，再看 message。
	require.Equal(t, "msg-000002", upstreamRequestIDFromErrorBody(`{"request_id":{"v":1},"error":{"message":"request_id: msg-000002 tail"}}`))
	// message 里有多个候选：取第一个匹配。
	require.Equal(t, "first-000001", upstreamRequestIDFromErrorBody(`{"error":{"message":"request_id: first-000001; request_id: second-000002"}}`))
	// message 里的 id 含非法字符处截断（只取 [A-Za-z0-9_.-]）。
	require.Equal(t, "abc.def-123_x", upstreamRequestIDFromErrorBody(`{"error":{"message":"request_id: abc.def-123_x/extra"}}`))
	// 正好 64KB 的合法 JSON 仍解析；超过 1 字节即拒绝。
	pad := strings.Repeat("x", upstreamRequestIDFromErrorBodyMaxBytes-len(`{"request_id":"rid-00000001","p":""}`))
	exact := `{"request_id":"rid-00000001","p":"` + pad + `"}`
	require.Len(t, exact, upstreamRequestIDFromErrorBodyMaxBytes)
	require.Equal(t, "rid-00000001", upstreamRequestIDFromErrorBody(exact))
	require.Equal(t, "rid-00000001", upstreamRequestIDFromErrorBody("  "+exact+"\n"), "前后空白先 TrimSpace 再比长度：带空白仍在上限内")
	over := `{"request_id":"rid-00000001","p":"` + pad + `x"}`
	require.Len(t, over, upstreamRequestIDFromErrorBodyMaxBytes+1)
	require.Empty(t, upstreamRequestIDFromErrorBody(over))
	// JSON 数组根：合法 JSON 但取不到任何字段。
	require.Empty(t, upstreamRequestIDFromErrorBody(`["request_id","rid-00000001"]`))
	// 截断到 128 字节：error.request_id 与 message 路径都截。
	longID := strings.Repeat("a", 300)
	require.Len(t, upstreamRequestIDFromErrorBody(`{"error":{"request_id":"`+longID+`"}}`), upstreamRequestIDMaxBytes)
	require.Len(t, upstreamRequestIDFromErrorBody(`{"error":{"message":"request_id: `+longID+`"}}`), upstreamRequestIDMaxBytes)
}

func TestOpsUpstreamHeaderFingerprint_WhitelistAndSecrets(t *testing.T) {
	h := http.Header{}
	for _, name := range opsUpstreamHeaderFingerprintNames {
		h.Set(name, " v-"+name+" ")
	}
	h.Set("Cookie", "sid=1")
	h.Set("Set-Cookie", "sid=1; HttpOnly")
	h.Set("Authorization", "Bearer secret")
	h.Set("X-Api-Key", "sk-secret")
	h.Set("Content-Length", "12")
	got := opsUpstreamHeaderFingerprint(h)
	require.Len(t, got, len(opsUpstreamHeaderFingerprintNames), "白名单全部命中，白名单外一个不进")
	for _, name := range opsUpstreamHeaderFingerprintNames {
		require.Equal(t, "v-"+name, got[name], "值 TrimSpace")
	}
	for _, secret := range []string{"cookie", "set-cookie", "authorization", "x-api-key", "content-length", "Cookie", "Authorization"} {
		require.NotContains(t, got, secret)
	}
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")

	// 只有 x-request-id 一个：map 只含一个键。
	one := http.Header{}
	one.Set("X-Request-Id", strings.Repeat("r", 130))
	got = opsUpstreamHeaderFingerprint(one)
	require.Len(t, got, 1)
	require.Len(t, got["x-request-id"], opsUpstreamHeaderFingerprintValueMaxBytes)

	// 白名单头全空白：nil。
	blank := http.Header{}
	for _, name := range opsUpstreamHeaderFingerprintNames {
		blank.Set(name, "  ")
	}
	require.Nil(t, opsUpstreamHeaderFingerprint(blank))
	require.Nil(t, opsUpstreamHeaderFingerprint(nil))
}

// appendOpsUpstreamError：头里有值（含空白）不被错误体覆盖；Detail 优先级低于响应体。
func TestAppendOpsUpstreamError_BodyFallbackPrecedence(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Kind:                 "http_error",
		UpstreamRequestID:    "  hdr-rid  ",
		UpstreamResponseBody: `{"request_id":"body-rid-0001"}`,
		Detail:               `{"request_id":"detail-rid-0001"}`,
	})
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Kind:                 "http_error",
		UpstreamResponseBody: `{"request_id":"body-rid-0002"}`,
		Detail:               `{"request_id":"detail-rid-0002"}`,
	})
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Kind:   "http_error",
		Detail: `{"error":{"message":"x request_id: detail-rid-0003"}}`,
	})
	events := opsEventsOf(t, c)
	require.Len(t, events, 3)
	require.Equal(t, "hdr-rid", events[0].UpstreamRequestID, "头里有值不覆盖，且 TrimSpace")
	require.Equal(t, "body-rid-0002", events[1].UpstreamRequestID, "响应体优先于 Detail")
	require.Equal(t, "detail-rid-0003", events[2].UpstreamRequestID, "响应体为空时用 Detail 里的 message 兜底")
}

// ---- Anthropic 入站（gateway_service.go）：上游只给 cf-ray ----

func TestGatewayHandleErrorResponse_CfRayOnlyRecordedInOpsNotEchoedToClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{LogUpstreamErrorBody: true}}}
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     cfRayOnlyHeader(),
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"api_error","message":"internal"}}`)),
	}
	account := &Account{ID: 21, Name: "anthropic-relay", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account, "claude-sonnet-4-5")
	require.Error(t, err)
	require.Empty(t, rec.Header().Get("X-Request-Id"), "cf-ray 不能冒充客户端 x-request-id")
	require.True(t, IsResponseCommitted(c))

	events := opsEventsOf(t, c)
	ev := events[len(events)-1]
	require.Equal(t, "http_error", ev.Kind)
	require.Equal(t, int64(21), ev.AccountID)
	require.Equal(t, http.StatusInternalServerError, ev.UpstreamStatusCode)
	require.Equal(t, "8a1b2c3d4e5f-HKG", ev.UpstreamRequestID, "ops 事件用 cf-ray 兜底")
	require.Equal(t, "internal", ev.Message)
	require.Contains(t, ev.Detail, `"internal"`, "LogUpstreamErrorBody 开启时记录响应体摘要")
	status, _ := c.Get(OpsUpstreamStatusCodeKey)
	require.Equal(t, http.StatusInternalServerError, status)
}

func TestGatewayHandleRetryExhaustedError_CfRayOnlyRecordedInOps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := &GatewayService{cfg: &config.Config{}}
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     cfRayOnlyHeader(),
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad gateway request_id: body-rid-000123"}}`)),
	}
	account := &Account{ID: 22, Name: "anthropic-relay", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	_, err := svc.handleRetryExhaustedError(context.Background(), resp, c, account)
	require.Error(t, err)
	require.True(t, IsResponseCommitted(c))
	require.Empty(t, rec.Header().Get("X-Request-Id"))
	require.Equal(t, http.StatusBadGateway, rec.Code)

	events := opsEventsOf(t, c)
	ev := events[len(events)-1]
	require.Equal(t, "retry_exhausted", ev.Kind)
	require.Equal(t, "8a1b2c3d4e5f-HKG", ev.UpstreamRequestID, "头里有 cf-ray 时不再从错误体兜底")
	require.Equal(t, http.StatusBadGateway, ev.UpstreamStatusCode)

	// 头里一个 request id 都没有：LogUpstreamErrorBody 开启时事件 Detail 带响应体，
	// appendOpsUpstreamError 从 Detail 里的 message "request_id: xxx" 兜底。
	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp2 := &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad gateway request_id: body-rid-000123"}}`)),
	}
	svcLogBody := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{LogUpstreamErrorBody: true}}}
	_, err = svcLogBody.handleRetryExhaustedError(context.Background(), resp2, c2, account)
	require.Error(t, err)
	ev2 := opsEventsOf(t, c2)[0]
	require.Equal(t, "body-rid-000123", ev2.UpstreamRequestID)

	// 未开启 LogUpstreamErrorBody：Detail 为空，头里又没有，保持空（不解析未记录的响应体）。
	rec3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(rec3)
	c3.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp3 := &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad gateway request_id: body-rid-000123"}}`)),
	}
	_, err = svc.handleRetryExhaustedError(context.Background(), resp3, c3, account)
	require.Error(t, err)
	require.Empty(t, opsEventsOf(t, c3)[0].UpstreamRequestID)
}

func TestGatewayInvalidNonStreamingJSONFailoverError_CarriesHeadersAndPoolRetryFlag(t *testing.T) {
	svc := &GatewayService{}
	body := []byte("<html>not json</html>")
	resp := &http.Response{StatusCode: http.StatusOK, Header: cfRayOnlyHeader()}

	nonPool := &Account{ID: 23, Name: "relay", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	err := svc.invalidNonStreamingJSONFailoverError(context.Background(), resp, nonPool, body, errors.New("invalid character '<'"), "claude-sonnet-4-5")
	var ferr *UpstreamFailoverError
	require.ErrorAs(t, err, &ferr)
	require.Equal(t, http.StatusBadGateway, ferr.StatusCode)
	require.Equal(t, body, ferr.ResponseBody)
	require.Equal(t, "8a1b2c3d4e5f-HKG", ferr.ResponseHeaders.Get("Cf-Ray"), "上游响应头原样带给 failover 决策")
	require.False(t, ferr.RetryableOnSameAccount, "非池模式不做同账号重试")

	pool := &Account{ID: 24, Name: "pool", Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true, "pool_mode_retry_status_codes": []any{float64(http.StatusBadGateway)}}}
	err = svc.invalidNonStreamingJSONFailoverError(context.Background(), resp, pool, body, errors.New("invalid character '<'"))
	require.ErrorAs(t, err, &ferr)
	require.True(t, ferr.RetryableOnSameAccount, "池模式且 502 在重试码里：同账号重试")

	// account 为 nil 也不 panic。
	err = svc.invalidNonStreamingJSONFailoverError(context.Background(), resp, nil, body, errors.New("x"))
	require.ErrorAs(t, err, &ferr)
	require.False(t, ferr.RetryableOnSameAccount)
}

// ---- OpenAI 入站（openai_gateway_service.go）：上游只给 cf-ray ----

func TestOpenAIHandleErrorResponse_CfRayOnlyRecordedInOpsNotEchoedToClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     cfRayOnlyHeader(),
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"server_error","message":"internal"}}`)),
	}
	account := &Account{ID: 31, Name: "openai-relay", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account, nil, "gpt-5.1")
	require.Error(t, err)
	require.Empty(t, rec.Header().Get("X-Request-Id"))
	require.True(t, IsResponseCommitted(c))

	events := opsEventsOf(t, c)
	ev := events[len(events)-1]
	require.Equal(t, "http_error", ev.Kind)
	require.Equal(t, "8a1b2c3d4e5f-HKG", ev.UpstreamRequestID)
	require.Equal(t, http.StatusInternalServerError, ev.UpstreamStatusCode)
	require.Equal(t, "internal", ev.Message)
}

func TestOpenAIHandleCompatErrorResponse_CfRayOnlyRecordedInOps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     cfRayOnlyHeader(),
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"internal"}}`)),
	}
	var gotStatus int
	writeError := func(_ *gin.Context, statusCode int, _, _ string) { gotStatus = statusCode }
	_, err := svc.handleCompatErrorResponse(resp, c, &Account{ID: 32, Name: "openai-relay", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, writeError, "gpt-5.1")
	require.Error(t, err)
	require.NotZero(t, gotStatus, "兼容路径通过 writeError 回写错误")
	require.Empty(t, rec.Header().Get("X-Request-Id"))

	ev := opsEventsOf(t, c)[0]
	require.Equal(t, "8a1b2c3d4e5f-HKG", ev.UpstreamRequestID)
	require.Equal(t, http.StatusInternalServerError, ev.UpstreamStatusCode)
	require.Equal(t, "internal", ev.Message)
}

// 非流式：上游只给 cf-ray 时，模型一致 → 正文照常写出且客户端 x-request-id 为空；
// 模型不一致 → 拦截，ops 事件 UpstreamRequestID 为 cf-ray，客户端一个字节都不写。
func TestOpenAIHandleNonStreamingResponse_CfRayOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newResp := func(model string) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     cfRayOnlyHeader(),
			Body: io.NopCloser(strings.NewReader(`{"id":"resp_1","object":"response","model":"` + model + `","status":"completed",` +
				`"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],` +
				`"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)),
		}
	}
	account := &Account{ID: 33, Name: "openai-relay", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	t.Run("model matches: body delivered, no x-request-id", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		svc := &OpenAIGatewayService{cfg: &config.Config{}}
		result, err := svc.handleNonStreamingResponse(context.Background(), newResp("gpt-5.1"), c, account, "gpt-5.1", "gpt-5.1")
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, 3, result.InputTokens)
		require.Contains(t, rec.Body.String(), "hello")
		require.Empty(t, rec.Header().Get("X-Request-Id"))
		require.Empty(t, rec.Header().Get("Cf-Ray"), "cf-ray 不在透传白名单")
		_, hasEvents := c.Get(OpsUpstreamErrorsKey)
		require.False(t, hasEvents)
	})

	t.Run("model mismatch: blocked, ops carries cf-ray, client untouched", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		svc := &OpenAIGatewayService{cfg: &config.Config{}}
		result, err := svc.handleNonStreamingResponse(context.Background(), newResp("gpt-4o-mini"), c, account, "gpt-5.1", "gpt-5.1")
		require.Nil(t, result)
		var ferr *UpstreamFailoverError
		require.ErrorAs(t, err, &ferr)
		require.Equal(t, http.StatusBadGateway, ferr.StatusCode)
		require.Empty(t, rec.Body.String(), "拦截前不能向客户端写任何字节")
		require.Empty(t, rec.Header().Get("X-Request-Id"))

		ev := opsEventsOf(t, c)[0]
		require.Equal(t, int64(33), ev.AccountID)
		require.Equal(t, "8a1b2c3d4e5f-HKG", ev.UpstreamRequestID)
		require.Equal(t, "8a1b2c3d4e5f-HKG", ev.UpstreamHeaders["cf-ray"])
		require.Contains(t, ev.Message, "sent=gpt-5.1 got=gpt-4o-mini")
		mark := GetOpsUpstreamModelMismatch(c)
		require.NotNil(t, mark)
		require.Equal(t, "gpt-4o-mini", mark.ResponseModel)
	})
}

// 首输出超时：request id 用兼容头名兜底进 ops 事件；failover 错误带回上游头。
func TestNewOpenAIFirstOutputTimeoutError_CfRayOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 34, Name: "openai-relay", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	ferr := svc.newOpenAIFirstOutputTimeoutError(context.Background(), c, account, time.Now().Add(-2*time.Second), "gpt-5.1", "high", time.Second, "stream", cfRayOnlyHeader())
	require.NotNil(t, ferr)
	require.Equal(t, http.StatusGatewayTimeout, ferr.StatusCode)
	require.True(t, ferr.SafeToFailoverAfterWrite)
	require.Equal(t, "8a1b2c3d4e5f-HKG", ferr.ResponseHeaders.Get("Cf-Ray"))
	require.Contains(t, string(ferr.ResponseBody), "first_output_timeout")

	ev := opsEventsOf(t, c)[0]
	require.Equal(t, "first_output_timeout", ev.Kind)
	require.Equal(t, "8a1b2c3d4e5f-HKG", ev.UpstreamRequestID)
	require.Equal(t, http.StatusGatewayTimeout, ev.UpstreamStatusCode)
	require.Contains(t, ev.Detail, "phase=stream")

	// 无响应头（连接阶段超时）：request id 为空，不 panic。
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	ferr = svc.newOpenAIFirstOutputTimeoutError(context.Background(), c2, account, time.Now(), "gpt-5.1", "", time.Second, "connect", nil)
	require.NotNil(t, ferr)
	require.Empty(t, opsEventsOf(t, c2)[0].UpstreamRequestID)
}

// images：纯函数构造的上游错误对象带兼容头名取到的 request id。
func TestOpenAIImagesUpstreamErrors_RequestIDFallbackHeaders(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error","code":"bad_model","message":"bad model","param":"model"}}`)

	upErr := openAIImagesUpstreamErrorFromHTTP(http.StatusBadRequest, cfRayOnlyHeader(), body)
	require.Equal(t, http.StatusBadRequest, upErr.StatusCode)
	require.Equal(t, "invalid_request_error", upErr.ErrorType)
	require.Equal(t, "bad_model", upErr.Code)
	require.Equal(t, "model", upErr.Param)
	require.Equal(t, "bad model", upErr.Message)
	require.Equal(t, "8a1b2c3d4e5f-HKG", upErr.UpstreamRequestID)

	oneapi := http.Header{"X-Oneapi-Request-Id": []string{"one-1"}, "Cf-Ray": []string{"ray-1"}}
	require.Equal(t, "one-1", openAIImagesUpstreamErrorFromHTTP(http.StatusBadGateway, oneapi, nil).UpstreamRequestID)
	require.Empty(t, openAIImagesUpstreamErrorFromHTTP(http.StatusBadGateway, nil, nil).UpstreamRequestID)
	require.Equal(t, "Upstream request failed (status 502)", openAIImagesUpstreamErrorFromHTTP(http.StatusBadGateway, nil, nil).Message)

	asyncErr := openAIImagesAsyncTaskFailedError("failed", cfRayOnlyHeader(), body)
	require.Equal(t, http.StatusBadGateway, asyncErr.StatusCode)
	require.Equal(t, "8a1b2c3d4e5f-HKG", asyncErr.UpstreamRequestID)
	require.Equal(t, "bad_model", asyncErr.Code)
	require.Empty(t, openAIImagesAsyncTaskFailedError("failed", nil, nil).UpstreamRequestID)
	require.Equal(t, "Upstream image async task failed", openAIImagesAsyncTaskFailedError("failed", nil, nil).Message)
}

// ---- Antigravity：上游只给 cf-ray ----

func TestAntigravityGatewayService_Forward_CfRayOnlyErrorPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newSvc := func(resp *http.Response) *AntigravityGatewayService {
		return &AntigravityGatewayService{
			settingService: NewSettingService(&antigravitySettingRepoStub{}, &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}),
			tokenProvider:  &AntigravityTokenProvider{},
			httpUpstream:   &httpUpstreamStub{resp: resp},
		}
	}
	account := func(id int64) *Account {
		return &Account{
			ID: id, Name: "acc-cfray", Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive, Concurrency: 1,
			Credentials: map[string]any{"access_token": "token", "project_id": "proj"},
		}
	}
	newCtx := func(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, []byte) {
		t.Helper()
		writer := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(writer)
		body, err := json.Marshal(map[string]any{"model": "claude-sonnet-4-5", "messages": []map[string]any{{"role": "user", "content": "hello"}}, "max_tokens": 16, "stream": false})
		require.NoError(t, err)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		return c, writer, body
	}

	t.Run("400 prompt too long", func(t *testing.T) {
		c, writer, body := newCtx(t)
		resp := &http.Response{StatusCode: http.StatusBadRequest, Header: cfRayOnlyHeader(), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Prompt is too long"}}`))}
		result, err := newSvc(resp).Forward(context.Background(), c, account(41), body, false)
		require.Nil(t, result)
		var promptErr *PromptTooLongError
		require.ErrorAs(t, err, &promptErr)
		require.Equal(t, "8a1b2c3d4e5f-HKG", promptErr.RequestID, "PromptTooLongError.RequestID 用兼容头名取值")
		require.Empty(t, writer.Header().Get("X-Request-Id"))
		ev := opsEventsOf(t, c)[0]
		require.Equal(t, "prompt_too_long", ev.Kind)
		require.Equal(t, "8a1b2c3d4e5f-HKG", ev.UpstreamRequestID)
	})

	t.Run("422 mapped error", func(t *testing.T) {
		c, writer, body := newCtx(t)
		resp := &http.Response{StatusCode: http.StatusUnprocessableEntity, Header: cfRayOnlyHeader(), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"boom"}}`))}
		result, err := newSvc(resp).Forward(context.Background(), c, account(42), body, false)
		require.Nil(t, result)
		require.Error(t, err)
		require.Equal(t, http.StatusBadGateway, writer.Code, "writeMappedClaudeError 把非白名单 4xx 映射为 502")
		require.Contains(t, writer.Body.String(), `"type":"error"`)
		require.Empty(t, writer.Header().Get("X-Request-Id"))
		events := opsEventsOf(t, c)
		ev := events[len(events)-1]
		require.Equal(t, "http_error", ev.Kind)
		require.Equal(t, "8a1b2c3d4e5f-HKG", ev.UpstreamRequestID)
		require.Equal(t, http.StatusUnprocessableEntity, ev.UpstreamStatusCode)
	})

	t.Run("403 failover", func(t *testing.T) {
		c, writer, body := newCtx(t)
		resp := &http.Response{StatusCode: http.StatusForbidden, Header: cfRayOnlyHeader(), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"forbidden"}}`))}
		result, err := newSvc(resp).Forward(context.Background(), c, account(43), body, false)
		require.Nil(t, result)
		var ferr *UpstreamFailoverError
		require.ErrorAs(t, err, &ferr)
		require.Equal(t, http.StatusForbidden, ferr.StatusCode)
		require.Empty(t, writer.Body.String(), "failover 前不向客户端写")
		require.Empty(t, writer.Header().Get("X-Request-Id"))
		events := opsEventsOf(t, c)
		ev := events[len(events)-1]
		require.Equal(t, "failover", ev.Kind)
		require.Equal(t, "8a1b2c3d4e5f-HKG", ev.UpstreamRequestID)
	})
}
