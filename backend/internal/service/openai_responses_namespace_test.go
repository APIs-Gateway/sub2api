package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	// 账号级兼容开关：为不认识 namespace 的兼容上游恢复旧的摊平行为。
	flattenOAuth := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"openai_responses_flatten_namespaces": true},
	}
	flattenAPIKey := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{"openai_responses_flatten_namespaces": true},
	}

	tests := []struct {
		name               string
		account            *Account
		transport          OpenAIUpstreamTransport
		passthroughEnabled bool
		compactPath        bool
		want               bool
	}{
		// 默认保留：OAuth 出口是 namespace 扩展的定义方，摊平会让模型无法按
		// `to=functions.<namespace>.<tool>` 寻址（issue #4978）。
		{name: "oauth_http_default_preserves", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "oauth_http_passthrough_default_preserves", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, passthroughEnabled: true, want: false},
		{name: "oauth_wsv2_default_preserves", account: oauth, transport: OpenAIUpstreamTransportResponsesWebsocketV2, want: false},
		// compact 端点 schema 更窄且无实测证据，保持既有摊平行为。
		{name: "oauth_compact_flattens", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, compactPath: true, want: true},
		{name: "oauth_compact_wsv2_preserves", account: oauth, transport: OpenAIUpstreamTransportResponsesWebsocketV2, compactPath: true, want: false},
		{name: "apikey_compact", account: apiKey, transport: OpenAIUpstreamTransportHTTPSSE, compactPath: true, want: false},
		{name: "oauth_flatten_enabled_http", account: flattenOAuth, transport: OpenAIUpstreamTransportHTTPSSE, want: true},
		{name: "oauth_flatten_enabled_http_passthrough", account: flattenOAuth, transport: OpenAIUpstreamTransportHTTPSSE, passthroughEnabled: true, want: true},
		// WSv2 出口原样转发上游事件、不做回程还原，摊平会让客户端收到无法匹配的平名。
		{name: "oauth_flatten_enabled_wsv2", account: flattenOAuth, transport: OpenAIUpstreamTransportResponsesWebsocketV2, want: false},
		// 透传账号先于 WSv2 分支经 HTTP 转发返回，开关打开时仍需摊平。
		{name: "oauth_flatten_enabled_wsv2_passthrough", account: flattenOAuth, transport: OpenAIUpstreamTransportResponsesWebsocketV2, passthroughEnabled: true, want: true},
		// 开关仅对 OAuth 生效：API Key 走 chat completions 回退桥时由桥自行摊平。
		{name: "apikey_flatten_enabled_http", account: flattenAPIKey, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "apikey_http", account: apiKey, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "nil_account", account: nil, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldFlattenOpenAIResponsesNamespaces(
				tt.account, tt.transport, tt.passthroughEnabled, tt.compactPath,
			))
		})
	}
}

func TestShouldKeepOpenAIResponsesToolCallNamespacesPolicy(t *testing.T) {
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	setupToken := &Account{Platform: PlatformOpenAI, Type: AccountTypeSetupToken}
	flattenOAuth := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"openai_responses_flatten_namespaces": true},
	}

	tests := []struct {
		name               string
		account            *Account
		transport          OpenAIUpstreamTransport
		passthroughEnabled bool
		compactPath        bool
		want               bool
	}{
		// 上游按 namespace 解析历史调用，缺字段会 400 "Missing namespace for function_call"。
		{name: "oauth_http_keeps", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, want: true},
		{name: "oauth_http_passthrough_keeps", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, passthroughEnabled: true, want: true},
		// compact 端点 schema 不含该字段，携带即 400 "Unknown parameter: input[N].namespace"。
		{name: "oauth_compact_strips", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, compactPath: true, want: false},
		// 摊平后调用项已是平名，残留 namespace 指向的声明不存在。
		{name: "oauth_flatten_enabled_strips", account: flattenOAuth, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		// WSv2 实际由 shouldStrip 提前短路，此处只钉住策略本身的取值。
		{name: "oauth_wsv2_keeps", account: oauth, transport: OpenAIUpstreamTransportResponsesWebsocketV2, want: true},
		// WSv2 + compact 是唯一「不摊平但仍必须清理」的组合，钉住 compact 判定本身，
		// 使其不会被误当成可由 shouldFlatten 推导出的冗余分支。
		{name: "oauth_compact_wsv2_strips", account: oauth, transport: OpenAIUpstreamTransportResponsesWebsocketV2, compactPath: true, want: false},
		// API Key 出口是标准 Responses API，不认识该字段。
		{name: "apikey_strips", account: apiKey, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "setup_token_strips", account: setupToken, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "nil_account", account: nil, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldKeepOpenAIResponsesToolCallNamespaces(
				tt.account, tt.transport, tt.passthroughEnabled, tt.compactPath, nil,
			))
		})
	}
}

func TestShouldStripOpenAIResponsesInputNamespaces(t *testing.T) {
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	nonOpenAI := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	tests := []struct {
		name               string
		account            *Account
		transport          OpenAIUpstreamTransport
		passthroughEnabled bool
		want               bool
	}{
		{name: "nil_account", transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "non_openai_account", account: nonOpenAI, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "oauth_http", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, want: true},
		{name: "apikey_http", account: apiKey, transport: OpenAIUpstreamTransportHTTPSSE, want: true},
		{name: "oauth_wsv2", account: oauth, transport: OpenAIUpstreamTransportResponsesWebsocketV2, want: false},
		{name: "apikey_wsv2_passthrough", account: apiKey, transport: OpenAIUpstreamTransportResponsesWebsocketV2, passthroughEnabled: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldStripOpenAIResponsesInputNamespaces(tt.account, tt.transport, tt.passthroughEnabled))
		})
	}
}

func TestShouldKeepOpenAIResponsesToolCallNamespaces(t *testing.T) {
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	nonOpenAI := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	namespaceTool := []byte(`{"tools":[{"type":"namespace","name":"mcp__cua_repl","tools":[]}]}`)

	tests := []struct {
		name        string
		account     *Account
		transport   OpenAIUpstreamTransport
		passthrough bool
		compact     bool
		body        []byte
		want        bool
	}{
		{name: "nil_account", body: namespaceTool, want: false},
		{name: "compact", account: apiKey, compact: true, body: namespaceTool, want: false},
		{name: "apikey_without_declaration", account: apiKey, want: false},
		{name: "apikey_with_declaration", account: apiKey, body: namespaceTool, want: true},
		{name: "non_openai_account", account: nonOpenAI, body: namespaceTool, want: false},
		// OAuth preserves namespace declarations by default (upstream #4978), so
		// historical calls keep their namespace even without a declaration here.
		{name: "oauth_http_without_declaration", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, want: true},
		// With the flatten toggle on, calls were rewritten to flat names; strip.
		{name: "oauth_flatten_enabled_without_declaration", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"openai_responses_flatten_namespaces": true}}, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "oauth_http_with_declaration", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, body: namespaceTool, want: true},
		{name: "oauth_wsv2_without_declaration", account: oauth, transport: OpenAIUpstreamTransportResponsesWebsocketV2, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldKeepOpenAIResponsesToolCallNamespaces(
				tt.account, tt.transport, tt.passthrough, tt.compact, tt.body,
			))
		})
	}
}

func TestOpenAIResponsesNamespaceHelpers_NoOpAndItemTypes(t *testing.T) {
	for _, tt := range []struct {
		body []byte
		want bool
	}{
		{body: []byte(`{"tools":[{"type":"namespace","name":"mcp","tools":[]}]}`), want: true},
		{body: []byte(`{"input":"not-an-array"}`), want: false},
		{body: []byte(`{"input":[{"type":"message","tools":[{"type":"namespace"}]}]}`), want: false},
	} {
		require.Equal(t, tt.want, hasOpenAIResponsesNamespaceToolDeclaration(tt.body))
	}

	for _, itemType := range []string{"function_call", "tool_call", "custom_tool_call", "mcp_tool_call"} {
		require.True(t, isOpenAIResponsesToolCallItemType(itemType))
	}
	require.False(t, isOpenAIResponsesToolCallItemType("message"))

	for _, body := range [][]byte{
		[]byte(`{"input":[]}`),
		[]byte(`{"namespace":"top-level-only"}`),
		[]byte(`{"input":[{"type":"function_call","namespace":"mcp"}]}`),
	} {
		stripped, err := stripOpenAIResponsesInputNamespaces(body, true)
		require.NoError(t, err)
		require.Equal(t, body, stripped)
	}
}

// 保留模式下只有工具调用项留住 namespace：上游按 namespace 解析历史调用，
// 而 message / reasoning / 输出项带该字段会被 schema 拒绝。
func TestStripOpenAIResponsesInputNamespacesKeepsToolCallNamespaces(t *testing.T) {
	body := []byte(`{
		"meta":9007199254740993,
		"input":[
			{"type":"function_call","namespace":"collaboration","name":"spawn_agent","arguments":"{}","large":9007199254740993},
			{"type":"custom_tool_call","namespace":"codex_app","name":"exec","input":"{}"},
			{"type":"tool_call","namespace":"mcp__codex_apps__gmail","name":"send"},
			{"type":"mcp_tool_call","namespace":"mcp__codex_apps__gmail","name":"list"},
			{"type":"message","namespace":"leftover","role":"assistant","content":[{"type":"output_text","text":"hi"}]},
			{"type":"function_call_output","namespace":"leftover","output":"ok"},
			{"type":"reasoning","namespace":"leftover"},
			{"type":"item","namespace":"leftover"}
		]
	}`)

	stripped, err := stripOpenAIResponsesInputNamespaces(body, true)
	require.NoError(t, err)

	require.Equal(t, "collaboration", gjson.GetBytes(stripped, "input.0.namespace").String())
	require.Equal(t, "codex_app", gjson.GetBytes(stripped, "input.1.namespace").String())
	require.Equal(t, "mcp__codex_apps__gmail", gjson.GetBytes(stripped, "input.2.namespace").String())
	require.Equal(t, "mcp__codex_apps__gmail", gjson.GetBytes(stripped, "input.3.namespace").String())
	for index := 4; index < 8; index++ {
		require.False(t, gjson.GetBytes(stripped, "input."+strconv.Itoa(index)+".namespace").Exists())
	}
	// 大整数不得经 float64 往返。
	require.Equal(t, gjson.GetBytes(body, "meta").Raw, gjson.GetBytes(stripped, "meta").Raw)
	require.Equal(t, gjson.GetBytes(body, "input.0.large").Raw, gjson.GetBytes(stripped, "input.0.large").Raw)

	// 类型比对不区分大小写与首尾空白。
	mixedCase := []byte(`{"input":[{"type":" Function_Call ","namespace":"collaboration","name":"spawn_agent"}]}`)
	keptMixedCase, err := stripOpenAIResponsesInputNamespaces(mixedCase, true)
	require.NoError(t, err)
	require.Equal(t, mixedCase, keptMixedCase)

	// 全部为调用项时无改动，应原样返回。
	callsOnly := []byte(`{"input":[{"type":"function_call","namespace":"collaboration","name":"spawn_agent"}]}`)
	unchanged, err := stripOpenAIResponsesInputNamespaces(callsOnly, true)
	require.NoError(t, err)
	require.Equal(t, callsOnly, unchanged)

	// 关闭保留时回到全量清理。
	strippedAll, err := stripOpenAIResponsesInputNamespaces(body, false)
	require.NoError(t, err)
	for index := 0; index < 8; index++ {
		require.False(t, gjson.GetBytes(strippedAll, "input."+strconv.Itoa(index)+".namespace").Exists())
	}
}

func TestResponsesLiteNamespaceDeclarationsPreserveHistoricalToolCalls(t *testing.T) {
	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{
		"input":[
			{"type":"function_call","namespace":"collaboration","name":"spawn_agent","call_id":"call_spawn","arguments":"{}"},
			{"type":"function_call","namespace":"mcp__cua_repl","name":"js","call_id":"call_js","arguments":"{}"},
			{"type":"message","role":"user","namespace":"leftover","content":[{"type":"input_text","text":"continue"}]},
			{"type":" Additional_Tools ","role":"developer","tools":[
				{"type":" Namespace ","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]},
				{"type":"namespace","name":"mcp__cua_repl","tools":[{"type":"function","name":"js"}]}
			]}
		]
	}`)

	require.True(t, hasOpenAIResponsesNamespaceToolDeclaration(body))
	require.True(t, shouldKeepOpenAIResponsesToolCallNamespaces(
		apiKey, OpenAIUpstreamTransportHTTPSSE, false, false, body,
	))
	require.True(t, shouldKeepOpenAIResponsesToolCallNamespaces(
		oauth, OpenAIUpstreamTransportHTTPSSE, false, false, body,
	))

	forwarded, err := stripOpenAIResponsesInputNamespaces(body, true)
	require.NoError(t, err)
	require.Equal(t, "collaboration", gjson.GetBytes(forwarded, "input.0.namespace").String())
	require.Equal(t, "mcp__cua_repl", gjson.GetBytes(forwarded, "input.1.namespace").String())
	require.False(t, gjson.GetBytes(forwarded, "input.2.namespace").Exists())
	require.Equal(t, " Namespace ", gjson.GetBytes(forwarded, "input.3.tools.0.type").String())

	// compact does not support input[].namespace, including a real Lite namespace
	// declaration, so it keeps the existing cleanup contract.
	require.False(t, shouldKeepOpenAIResponsesToolCallNamespaces(
		apiKey, OpenAIUpstreamTransportHTTPSSE, false, true, body,
	))
	compact, err := stripOpenAIResponsesInputNamespaces(body, false)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(compact, "input.0.namespace").Exists())
	require.False(t, gjson.GetBytes(compact, "input.1.namespace").Exists())
}

func TestResponsesNamespaceDeclarationDoesNotTreatPlainFunctionAsNamespace(t *testing.T) {
	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{
		"tools":[{"type":"function","name":"js","namespace":"mcp__cua_repl"}],
		"input":[{"type":"function_call","namespace":"mcp__cua_repl","name":"js","arguments":"{}"}]
	}`)

	require.False(t, hasOpenAIResponsesNamespaceToolDeclaration(body))
	require.False(t, shouldKeepOpenAIResponsesToolCallNamespaces(
		apiKey, OpenAIUpstreamTransportHTTPSSE, false, false, body,
	))
	forwarded, err := stripOpenAIResponsesInputNamespaces(body, false)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(forwarded, "input.0.namespace").Exists())
}

// Responses Lite carries namespace declarations in input.additional_tools.
// Forward must preserve call namespaces for those declarations while still
// removing a namespace accidentally attached to an ordinary input message.
func TestOpenAIGatewayService_Forward_APIKeyPreservesLiteNamespaceToolCalls(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-terra",
		"stream":false,
		"input":[
			{"type":"function_call","namespace":"collaboration","name":"spawn_agent","call_id":"call_spawn","arguments":"{}"},
			{"type":"function_call","namespace":"mcp__cua_repl","name":"js","call_id":"call_js","arguments":"{}"},
			{"type":"message","role":"user","namespace":"leftover","content":[{"type":"input_text","text":"hello"}]},
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]},
				{"type":"namespace","name":"mcp__cua_repl","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]}
			]}
		]
	}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}
	c := newOpenAIRejectedFieldTestContext(body)
	c.Request.Header.Set(responsesLiteHeader, "true")

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(), c, newOpenAIRejectedFieldTestAccount(), body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 1)
	forwarded := upstream.bodies[0]
	require.Equal(t, "collaboration", gjson.GetBytes(forwarded, "input.0.namespace").String())
	require.Equal(t, "spawn_agent", gjson.GetBytes(forwarded, "input.0.name").String())
	require.Equal(t, "mcp__cua_repl", gjson.GetBytes(forwarded, "input.1.namespace").String())
	require.Equal(t, "js", gjson.GetBytes(forwarded, "input.1.name").String())
	require.False(t, gjson.GetBytes(forwarded, "input.2.namespace").Exists())
	require.Equal(t, "collaboration", gjson.GetBytes(forwarded, `input.#(type=="additional_tools").tools.0.name`).String())
	require.Equal(t, "mcp__cua_repl", gjson.GetBytes(forwarded, `input.#(type=="additional_tools").tools.1.name`).String())
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
		context.Background(), resp, c, nil, "gpt-5.5", "",
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

	result, err := svc.handlePassthroughSSEToJSON(resp, c, nil, body, "gpt-5.5", "gpt-5.5")
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

func TestOpenAIGatewayService_Forward_NativeOAuthFlattenToggle_FlattensNamespaceTools(t *testing.T) {
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
		Extra:       map[string]any{"openai_responses_flatten_namespaces": true},
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
		Extra: map[string]any{
			"openai_passthrough":                  true,
			"openai_responses_flatten_namespaces": true,
		},
		Status: StatusActive, Schedulable: true, RateMultiplier: f64p(1),
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
		Extra:       map[string]any{"openai_responses_flatten_namespaces": true},
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
		context.Background(), resp, c, nil, "gpt-5.5", "",
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

	result, err := svc.handlePassthroughSSEToJSON(resp, c, nil, body, "gpt-5.5", "gpt-5.5")
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
