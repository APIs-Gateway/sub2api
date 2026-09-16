package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// shouldFlattenOpenAIResponsesNamespaces decision table.
// ---------------------------------------------------------------------------

func TestShouldFlattenOpenAIResponsesNamespaces(t *testing.T) {
	oauth := &Account{Type: AccountTypeOAuth}
	apiKey := &Account{Type: AccountTypeAPIKey}

	tests := []struct {
		name               string
		account            *Account
		transport          OpenAIUpstreamTransport
		passthroughEnabled bool
		want               bool
	}{
		{name: "oauth_http", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, want: true},
		{name: "oauth_http_passthrough", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, passthroughEnabled: true, want: true},
		// WSv2 出口原样转发上游事件、不做回程还原，摊平会让客户端收到无法匹配的平名。
		{name: "oauth_wsv2", account: oauth, transport: OpenAIUpstreamTransportResponsesWebsocketV2, want: false},
		// 透传账号先于 WSv2 分支经 HTTP 转发返回，仍需摊平。
		{name: "oauth_wsv2_passthrough", account: oauth, transport: OpenAIUpstreamTransportResponsesWebsocketV2, passthroughEnabled: true, want: true},
		{name: "apikey_http", account: apiKey, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "nil_account", account: nil, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldFlattenOpenAIResponsesNamespaces(tt.account, tt.transport, tt.passthroughEnabled))
		})
	}
}

// ---------------------------------------------------------------------------
// flattenOpenAIResponsesNamespaces / restoreOpenAIResponsesNamespacePayload
// helper-level behavior, including the no-op fast paths.
// ---------------------------------------------------------------------------

func TestFlattenOpenAIResponsesNamespaces_NoNamespaceIsNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"plain"}]}`)

	got, err := flattenOpenAIResponsesNamespaces(c, body)
	require.NoError(t, err)
	// bytes.Contains 快速路径：不含 "namespace" 时原样返回同一份字节，不做任何
	// Unmarshal/Marshal 开销。
	require.Equal(t, body, got)
	require.Nil(t, openAIResponsesNamespaceNames(c))
}

func TestFlattenOpenAIResponsesNamespaces_FlattensAndSetsContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{
		"model":"gpt-5.5",
		"tools":[
			{"type":"function","name":"plain"},
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}
		],
		"tool_choice":{"type":"function","name":"spawn_agent","namespace":"collaboration"},
		"input":[{"type":"function_call","name":"spawn_agent","namespace":"collaboration","call_id":"call_1","arguments":"{}"}]
	}`)

	got, err := flattenOpenAIResponsesNamespaces(c, body)
	require.NoError(t, err)
	require.Equal(t, "plain", gjson.GetBytes(got, "tools.0.name").String())
	require.Equal(t, "function", gjson.GetBytes(got, "tools.1.type").String())
	require.Equal(t, "collaboration__spawn_agent", gjson.GetBytes(got, "tools.1.name").String())
	require.Equal(t, "collaboration__spawn_agent", gjson.GetBytes(got, "tool_choice.name").String())
	require.False(t, gjson.GetBytes(got, "tool_choice.namespace").Exists())
	require.Equal(t, "collaboration__spawn_agent", gjson.GetBytes(got, "input.0.name").String())
	require.False(t, gjson.GetBytes(got, "input.0.namespace").Exists())

	names := openAIResponsesNamespaceNames(c)
	require.Equal(t, apicompat.ResponsesNamespaceName{Namespace: "collaboration", Name: "spawn_agent"}, names["collaboration__spawn_agent"])
}

func TestFlattenOpenAIResponsesNamespaces_PropagatesCollisionError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"tools":[
		{"type":"function","name":"collaboration__spawn_agent"},
		{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}
	]}`)

	_, err := flattenOpenAIResponsesNamespaces(c, body)
	require.ErrorContains(t, err, "conflicts with a top-level tool")
	require.Nil(t, openAIResponsesNamespaceNames(c))
}

func TestRestoreOpenAIResponsesNamespacePayload_NoopWithoutContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	payload := []byte(`{"type":"function_call","name":"collaboration__spawn_agent"}`)

	got, err := restoreOpenAIResponsesNamespacePayload(c, payload)
	require.NoError(t, err)
	require.Equal(t, payload, got)
}

func TestRestoreOpenAIResponsesNamespacePayload_NoopOnInvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	setOpenAIResponsesNamespaceNames(c, map[string]apicompat.ResponsesNamespaceName{
		"collaboration__spawn_agent": {Namespace: "collaboration", Name: "spawn_agent"},
	})
	payload := []byte("[DONE]")

	got, err := restoreOpenAIResponsesNamespacePayload(c, payload)
	require.NoError(t, err)
	require.Equal(t, payload, got)
}

func TestRestoreOpenAIResponsesNamespacePayload_Restores(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	setOpenAIResponsesNamespaceNames(c, map[string]apicompat.ResponsesNamespaceName{
		"collaboration__spawn_agent": {Namespace: "collaboration", Name: "spawn_agent"},
	})
	payload := []byte(`{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}"}`)

	got, err := restoreOpenAIResponsesNamespacePayload(c, payload)
	require.NoError(t, err)
	require.Equal(t, "spawn_agent", gjson.GetBytes(got, "name").String())
	require.Equal(t, "collaboration", gjson.GetBytes(got, "namespace").String())
}

// ---------------------------------------------------------------------------
// Response-side restore wiring, one direct test per handler covering all six
// response paths: streaming passthrough / non-streaming passthrough /
// passthrough SSE-to-JSON / streaming / non-streaming / SSE-to-JSON.
// ---------------------------------------------------------------------------

func namespaceTestNames() map[string]apicompat.ResponsesNamespaceName {
	return map[string]apicompat.ResponsesNamespaceName{
		"collaboration__spawn_agent": {Namespace: "collaboration", Name: "spawn_agent"},
	}
}

func TestHandleStreamingResponsePassthrough_RestoresNamespaceCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())

	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.output_item.done","item":{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}"}}`,
			"",
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))),
		Header: http.Header{"x-request-id": []string{"rid"}},
	}

	_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI}, time.Now(), "m", "m")
	require.NoError(t, err)
	require.NotContains(t, rec.Body.String(), "collaboration__spawn_agent")
	require.Contains(t, rec.Body.String(), `"name":"spawn_agent"`)
	require.Contains(t, rec.Body.String(), `"namespace":"collaboration"`)
}

func TestHandleNonStreamingResponsePassthrough_RestoresNamespaceCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_1","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}

	result, err := (&OpenAIGatewayService{cfg: &config.Config{}}).handleNonStreamingResponsePassthrough(
		context.Background(), resp, c, "gpt-5.5", "",
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotContains(t, rec.Body.String(), "collaboration__spawn_agent")
	require.Contains(t, rec.Body.String(), `"name":"spawn_agent"`)
	require.Contains(t, rec.Body.String(), `"namespace":"collaboration"`)
}

func TestHandlePassthroughSSEToJSON_RestoresNamespaceCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}}
	body := []byte(strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`data: [DONE]`,
	}, "\n"))

	result, err := svc.handlePassthroughSSEToJSON(resp, c, body, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotContains(t, rec.Body.String(), "collaboration__spawn_agent")
	require.Contains(t, rec.Body.String(), `"name":"spawn_agent"`)
	require.Contains(t, rec.Body.String(), `"namespace":"collaboration"`)
}

func TestHandleStreamingResponse_RestoresNamespaceCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &OpenAIGatewayService{cfg: cfg}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_ns"}}`,
			"",
			`data: {"type":"response.output_item.done","item":{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}"}}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp_ns","status":"completed","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-ns"}},
	}

	result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}, time.Now(), "model", "model")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotContains(t, rec.Body.String(), "collaboration__spawn_agent")
	require.Contains(t, rec.Body.String(), `"name":"spawn_agent"`)
	require.Contains(t, rec.Body.String(), `"namespace":"collaboration"`)
}

func TestHandleNonStreamingResponse_RestoresNamespaceCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_1","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)),
	}
	account := &Account{ID: 146, Type: AccountTypeOAuth}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "gpt-5.4", "gpt-5.4")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotContains(t, rec.Body.String(), "collaboration__spawn_agent")
	require.Contains(t, rec.Body.String(), `"name":"spawn_agent"`)
	require.Contains(t, rec.Body.String(), `"namespace":"collaboration"`)
}

func TestHandleSSEToJSON_RestoresNamespaceCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	body := []byte(strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_2","model":"gpt-4o","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}"}],"usage":{"input_tokens":7,"output_tokens":9}}}`,
		`data: [DONE]`,
	}, "\n"))

	usage, err := svc.handleSSEToJSON(resp, c, nil, body, "gpt-4o", "gpt-4o")
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.NotContains(t, rec.Body.String(), "collaboration__spawn_agent")
	require.Contains(t, rec.Body.String(), `"name":"spawn_agent"`)
	require.Contains(t, rec.Body.String(), `"namespace":"collaboration"`)
}

// ---------------------------------------------------------------------------
// Forward()-level wiring: request-side flatten for the native path, full
// round trip for the OAuth-passthrough path, and the collision error path.
// ---------------------------------------------------------------------------

func TestOpenAIGatewayService_Forward_NativeOAuth_FlattensNamespaceTools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	originalBody := []byte(`{
		"model":"gpt-5.5","stream":false,"instructions":"local-test-instructions",
		"tools":[
			{"type":"function","name":"plain","description":"keep"},
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"spawn"}]}
		],
		"tool_choice":{"type":"function","name":"spawn_agent","namespace":"collaboration"},
		"input":[{"type":"function_call","call_id":"call_old","name":"spawn_agent","namespace":"collaboration","arguments":"{}"}]
	}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(originalBody))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_native_ns"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop after capture"}}`)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID: 123, Name: "acc", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Status:      StatusActive, Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, originalBody)
	require.Error(t, err)
	require.Nil(t, result)
	require.NotNil(t, upstream.lastReq)

	require.Equal(t, "plain", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.1.type").String())
	require.Equal(t, "collaboration__spawn_agent", gjson.GetBytes(upstream.lastBody, "tools.1.name").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "tools.1.tools").Exists())
	require.Equal(t, "collaboration__spawn_agent", gjson.GetBytes(upstream.lastBody, "tool_choice.name").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "tool_choice.namespace").Exists())
	require.Equal(t, "collaboration__spawn_agent", gjson.GetBytes(upstream.lastBody, "input.0.name").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "input.0.namespace").Exists())
}

func TestOpenAIGatewayService_Forward_OAuthPassthrough_RequestAndStreamResponseRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")

	originalBody := []byte(`{
		"model":"gpt-5.5","stream":true,"instructions":"local-test-instructions",
		"tools":[
			{"type":"function","name":"plain","description":"keep","parameters":{"type":"object"}},
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"spawn","parameters":{"type":"object"}}]}
		],
		"tool_choice":{"type":"function","name":"spawn_agent","namespace":"collaboration"},
		"input":[{"type":"function_call","call_id":"call_old","name":"spawn_agent","namespace":"collaboration","arguments":"{}"}]
	}`)

	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"collaboration__spawn_agent","arguments":"{}"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"collaboration__spawn_agent","arguments":"{}"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_namespace"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID: 123, Name: "acc", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:       map[string]any{"openai_passthrough": true}, Status: StatusActive, Schedulable: true, RateMultiplier: f64p(1),
	}

	result, err := svc.Forward(context.Background(), c, account, originalBody)
	require.NoError(t, err)
	require.NotNil(t, result)

	// 请求侧：namespace 工具已摊平为标准 function 工具，tool_choice/input 的
	// namespace 限定名也被改写为摊平名。
	require.Equal(t, "plain", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.1.type").String())
	require.Equal(t, "collaboration__spawn_agent", gjson.GetBytes(upstream.lastBody, "tools.1.name").String())
	require.Equal(t, "collaboration__spawn_agent", gjson.GetBytes(upstream.lastBody, "tool_choice.name").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "tool_choice.namespace").Exists())
	require.Equal(t, "collaboration__spawn_agent", gjson.GetBytes(upstream.lastBody, "input.0.name").String())

	// 响应侧：摊平名已还原为客户端认识的 namespace/name 形态。
	downstream := rec.Body.String()
	require.NotContains(t, downstream, "collaboration__spawn_agent")
	require.Contains(t, downstream, `"name":"spawn_agent"`)
	require.Contains(t, downstream, `"namespace":"collaboration"`)
}

func TestOpenAIGatewayService_Forward_NamespaceCollisionReturnsBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	body := []byte(`{
		"model":"gpt-5.5","stream":true,"instructions":"test",
		"tools":[
			{"type":"function","name":"collaboration__spawn_agent","parameters":{"type":"object"}},
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}
		],"input":"hi"
	}`)
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID: 123, Name: "acc", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Status:      StatusActive, Schedulable: true, RateMultiplier: f64p(1),
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.Error(t, err)
	require.Nil(t, result)
	require.Nil(t, upstream.lastReq)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, "tools", gjson.Get(rec.Body.String(), "error.param").String())
	require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), "conflicts with a top-level tool")
}

// ---------------------------------------------------------------------------
// Error-branch coverage: the guard clauses above only exercise the "no error"
// side of each internal error check (decode/encode/nil-context/restore). The
// tests below drive the other side of each branch directly.
// ---------------------------------------------------------------------------

func TestFlattenOpenAIResponsesNamespaces_PropagatesDecodeError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// Contains the "namespace" substring (passes the bytes.Contains fast path)
	// but is not valid JSON, so json.Unmarshal fails.
	body := []byte(`{"tools":[{"type":"namespace"`)

	got, err := flattenOpenAIResponsesNamespaces(c, body)
	require.ErrorContains(t, err, "decode OpenAI namespace body")
	require.Equal(t, body, got)
	require.Nil(t, openAIResponsesNamespaceNames(c))
}

func TestOpenAIResponsesNamespaceNames_NilContextIsNoop(t *testing.T) {
	require.Nil(t, openAIResponsesNamespaceNames(nil))
}

func TestRestoreOpenAIResponsesNamespacePayload_NoopOnNilContext(t *testing.T) {
	payload := []byte(`{"type":"function_call","name":"collaboration__spawn_agent"}`)

	got, err := restoreOpenAIResponsesNamespacePayload(nil, payload)
	require.NoError(t, err)
	require.Equal(t, payload, got)
}

func TestRestoreOpenAIResponsesNamespacePayload_PropagatesRestoreError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())
	// Syntactically valid JSON (json.Valid passes the guard) but the number
	// literal overflows float64, so apicompat.RestoreResponsesNamespaceCalls'
	// internal json.Unmarshal fails and the error propagates unchanged.
	payload := []byte(`{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","broken":1e400}`)

	got, err := restoreOpenAIResponsesNamespacePayload(c, payload)
	require.Error(t, err)
	require.Equal(t, payload, got)
}

// ---------------------------------------------------------------------------
// Response-side restore wiring: the six call sites above only exercise the
// "no error" branch of restoreOpenAIResponsesNamespacePayload. Each test
// below feeds a response payload with an out-of-range JSON number so the
// restore call fails and the handler's wrapped-error branch actually runs.
// ---------------------------------------------------------------------------

func TestHandleStreamingResponsePassthrough_PropagatesNamespaceRestoreError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())

	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.output_item.done","item":{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}","broken":1e400}}`,
			"",
		}, "\n"))),
		Header: http.Header{"x-request-id": []string{"rid"}},
	}

	_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI}, time.Now(), "m", "m")
	require.ErrorContains(t, err, "restore OpenAI passthrough namespace response")
}

func TestHandleNonStreamingResponsePassthrough_PropagatesNamespaceRestoreError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_1","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}","broken":1e400}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}

	result, err := (&OpenAIGatewayService{cfg: &config.Config{}}).handleNonStreamingResponsePassthrough(
		context.Background(), resp, c, "gpt-5.5", "",
	)
	require.Nil(t, result)
	require.ErrorContains(t, err, "restore OpenAI passthrough namespace response")
}

func TestHandlePassthroughSSEToJSON_PropagatesNamespaceRestoreError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}}
	body := []byte(strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}","broken":1e400}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`data: [DONE]`,
	}, "\n"))

	result, err := svc.handlePassthroughSSEToJSON(resp, c, body, "gpt-5.5", "gpt-5.5")
	require.Nil(t, result)
	require.ErrorContains(t, err, "restore OpenAI passthrough namespace response")
}

func TestHandleStreamingResponse_PropagatesNamespaceRestoreError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &OpenAIGatewayService{cfg: cfg}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_ns"}}`,
			"",
			`data: {"type":"response.output_item.done","item":{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}","broken":1e400}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-ns"}},
	}

	result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}, time.Now(), "model", "model")
	// Unlike the other five restore call sites, every error path in this
	// handler (scan errors, disconnects, and this restore error) returns
	// resultWithUsage() alongside the error instead of nil: the caller still
	// needs the usage collected so far for billing even when the stream ends
	// in error. Assert the error, not nilness, to match that contract.
	require.NotNil(t, result)
	require.ErrorContains(t, err, "restore OpenAI namespace response")
}

func TestHandleNonStreamingResponse_PropagatesNamespaceRestoreError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_1","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}","broken":1e400}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)),
	}
	account := &Account{ID: 146, Type: AccountTypeOAuth}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "gpt-5.4", "gpt-5.4")
	require.Nil(t, result)
	require.ErrorContains(t, err, "restore OpenAI namespace response")
}

func TestHandleSSEToJSON_PropagatesNamespaceRestoreError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	setOpenAIResponsesNamespaceNames(c, namespaceTestNames())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	body := []byte(strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_2","model":"gpt-4o","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}","broken":1e400}],"usage":{"input_tokens":7,"output_tokens":9}}}`,
		`data: [DONE]`,
	}, "\n"))

	usage, err := svc.handleSSEToJSON(resp, c, nil, body, "gpt-4o", "gpt-4o")
	require.Nil(t, usage)
	require.ErrorContains(t, err, "restore OpenAI namespace response")
}
