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

func openAIClientToolsRequest(stream bool) []byte {
	streamValue := "false"
	if stream {
		streamValue = "true"
	}
	return []byte(`{"model":"gpt-5.4","input":"fix it","stream":` + streamValue + `,"tools":[{"type":"custom","name":"exec"},{"type":"custom","name":"apply_patch"}]}`)
}

func assertOpenAIClientToolsLowered(t *testing.T, body []byte) {
	t.Helper()
	for index, name := range []string{"exec", "apply_patch"} {
		tool := gjson.GetBytes(body, "tools."+string(rune('0'+index)))
		require.Equal(t, "function", tool.Get("type").String())
		require.Equal(t, name, tool.Get("name").String())
		require.Equal(t, "string", tool.Get("parameters.properties.input.type").String())
	}
}

func openAIClientToolsTestService(upstream *httpUpstreamRecorder) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg: &config.Config{Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		}},
	}
}

func TestNeedsOpenAIResponsesClientToolAdaptation(t *testing.T) {
	tests := map[string]struct {
		body []byte
		want bool
	}{
		"custom tool declaration": {
			body: openAIClientToolsRequest(false),
			want: true,
		},
		"tool_search declaration": {
			body: []byte(`{"model":"gpt-5.5","tools":[{"type":"tool_search"}]}`),
			want: true,
		},
		"custom_tool_call in history": {
			body: []byte(`{"model":"gpt-5.5","input":[{"type":"custom_tool_call","call_id":"c1","name":"exec"}]}`),
			want: true,
		},
		"plain function tools": {
			body: []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"run"}]}`),
			want: false,
		},
		"no tools at all": {
			body: []byte(`{"model":"gpt-5.5","input":"hi"}`),
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, needsOpenAIResponsesClientToolAdaptation(tc.body))
		})
	}
}

func TestAdaptOpenAIResponsesClientToolsLeavesNamespaceOnlyBodyUnchanged(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.5",
		"tools": [{"type": "namespace", "name": "code_tools", "tools": [{"type": "function", "name": "run"}]}],
		"tool_choice": "auto"
	}`)

	adapted, mapping, err := adaptOpenAIResponsesClientTools(body)

	require.NoError(t, err)
	require.Equal(t, body, adapted)
	require.Empty(t, mapping.CustomTools)
	require.Empty(t, mapping.NamespaceTools)
	require.False(t, mapping.ToolSearch)
}

func TestAdaptOpenAIResponsesClientToolsLowersCustomTools(t *testing.T) {
	body := openAIClientToolsRequest(false)

	adapted, mapping, err := adaptOpenAIResponsesClientTools(body)

	require.NoError(t, err)
	assertOpenAIClientToolsLowered(t, adapted)
	require.True(t, mapping.CustomTools["exec"])
	require.True(t, mapping.CustomTools["apply_patch"])
	require.True(t, hasResponsesClientToolMapping(mapping))
}

func TestAdaptOpenAIResponsesClientToolsRejectsTrailingData(t *testing.T) {
	tests := map[string][]byte{
		"trailing garbage":     append(openAIClientToolsRequest(false), []byte(` garbage`)...),
		"second JSON document": append(openAIClientToolsRequest(false), []byte(` {"model":"other"}`)...),
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			adapted, mapping, err := adaptOpenAIResponsesClientTools(body)

			require.ErrorContains(t, err, "decode OpenAI Responses client tools trailing data")
			require.Equal(t, body, adapted)
			require.Empty(t, mapping)
		})
	}
}

func TestClearOpenAIResponsesClientToolMappingRemovesStaleContextState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(openAIResponsesClientToolMappingContextKey, apicompat.ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}})

	clearOpenAIResponsesClientToolMapping(c)

	_, ok := openAIResponsesClientToolMapping(c)
	require.False(t, ok)
}

func TestSetOpenAIResponsesClientToolMappingIgnoresEmptyMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	setOpenAIResponsesClientToolMapping(c, apicompat.ResponsesClientToolMapping{})

	_, ok := openAIResponsesClientToolMapping(c)
	require.False(t, ok)
}

func TestRestoreOpenAIResponsesClientToolPayloadWithoutMappingIsNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	payload := []byte(`{"id":"resp","output":[]}`)

	restored, err := restoreOpenAIResponsesClientToolPayload(c, payload)

	require.NoError(t, err)
	require.Equal(t, payload, restored)
}

func TestOpenAIPassthroughAPIKeyRestoresClientToolsNonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := openAIClientToolsRequest(false)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_tools","status":"completed","output":[
			{"type":"function_call","id":"i1","call_id":"c1","name":"exec","arguments":"{\"input\":\"pwd\"}"},
			{"type":"function_call","id":"i2","call_id":"c2","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}"}],"usage":{}}`)),
	}}
	svc := openAIClientToolsTestService(upstream)
	account := &Account{ID: 5659, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}

	result, err := svc.forwardOpenAIPassthrough(context.Background(), c, account, body, "gpt-5.4", nil, false, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	assertOpenAIClientToolsLowered(t, upstream.lastBody)
	require.Equal(t, "custom_tool_call", gjson.Get(recorder.Body.String(), "output.0.type").String())
	require.Equal(t, "pwd", gjson.Get(recorder.Body.String(), "output.0.input").String())
	require.Equal(t, "custom_tool_call", gjson.Get(recorder.Body.String(), "output.1.type").String())
	require.Equal(t, "*** Begin Patch", gjson.Get(recorder.Body.String(), "output.1.input").String())
}

func TestOpenAIPassthroughAPIKeyRestoresClientToolsStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := openAIClientToolsRequest(true)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	sse := strings.Join([]string{
		`data: {"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"apply_patch","status":"in_progress"}}`,
		`data: {"type":"response.function_call_arguments.done","sequence_number":1,"item_id":"i1","call_id":"c1","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}"}`,
		`data: {"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}","status":"completed"}}`,
		`data: {"type":"response.completed","sequence_number":3,"response":{"id":"resp_stream_tools","status":"completed","output":[{"type":"function_call","id":"i1","call_id":"c1","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
	}, "\n\n") + "\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}
	svc := openAIClientToolsTestService(upstream)
	account := &Account{ID: 5660, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}

	result, err := svc.forwardOpenAIPassthrough(context.Background(), c, account, body, "gpt-5.4", nil, true, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	assertOpenAIClientToolsLowered(t, upstream.lastBody)
	output := recorder.Body.String()
	require.Contains(t, output, `"type":"custom_tool_call"`)
	require.Contains(t, output, `"type":"response.custom_tool_call_input.done"`)
	require.Contains(t, output, `"input":"*** Begin Patch"`)
	require.NotContains(t, output, `"input":{`)
}

// OAuth 账号走官方上游，官方认得 custom / tool_search，不该被降级。
func TestOpenAIPassthroughOAuthLeavesClientToolsUntouched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := openAIClientToolsRequest(false)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set(openAIResponsesClientToolMappingContextKey, apicompat.ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}})

	clearOpenAIResponsesClientToolMapping(c)

	_, ok := openAIResponsesClientToolMapping(c)
	require.False(t, ok, "换到非 apikey 账号后必须清掉上一次的降级映射")
}
