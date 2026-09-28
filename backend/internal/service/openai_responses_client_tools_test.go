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
		"Responses Lite function-only carrier": {
			body: []byte(`{"model":"gpt-5.5","input":[{"type":"additional_tools","tools":[{"type":"function","name":"run"}]}]}`),
			want: true,
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

func TestAdaptOpenAIResponsesClientToolsLiftsResponsesLiteTools(t *testing.T) {
	request := []byte(`{"model":"deepseek-chat","input":[
		{"type":"additional_tools","role":"developer","tools":[
			{"type":"namespace","name":"functions","tools":[{"type":"function","name":"exec"}]},
			{"type":"custom","name":"apply_patch"},
			{"type":"function","name":"lookup","parameters":{"type":"object"}}
		]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
	],"tool_choice":{"type":"function","namespace":"functions","name":"exec"}}`)

	adapted, mapping, err := adaptOpenAIResponsesClientTools(request)

	require.NoError(t, err)
	require.False(t, gjson.GetBytes(adapted, `input.#(type=="additional_tools")`).Exists())
	require.Equal(t, "message", gjson.GetBytes(adapted, "input.0.type").String())
	require.Equal(t, "function", gjson.GetBytes(adapted, "tools.0.type").String())
	require.Equal(t, "functions__exec", gjson.GetBytes(adapted, "tools.0.name").String())
	require.Equal(t, "function", gjson.GetBytes(adapted, "tools.1.type").String())
	require.Equal(t, "apply_patch", gjson.GetBytes(adapted, "tools.1.name").String())
	require.Equal(t, "function", gjson.GetBytes(adapted, "tools.2.type").String())
	require.Equal(t, "lookup", gjson.GetBytes(adapted, "tools.2.name").String())
	require.True(t, mapping.CustomTools["apply_patch"])
	require.Equal(t, apicompat.ResponsesNamespaceName{Namespace: "functions", Name: "exec"}, mapping.NamespaceTools["functions__exec"])
	require.Equal(t, "functions__exec", gjson.GetBytes(adapted, "tool_choice.name").String())
	require.False(t, gjson.GetBytes(adapted, "tool_choice.namespace").Exists())
}

func TestAdaptOpenAIResponsesClientToolsLiftsFunctionOnlyCarrierAndRejectsMalformedCarrier(t *testing.T) {
	request := []byte(`{"input":[{"type":"additional_tools","tools":[{"type":"function","name":"lookup"}]},{"type":"message","role":"user"}]}`)
	adapted, mapping, err := adaptOpenAIResponsesClientTools(request)
	require.NoError(t, err)
	require.Empty(t, mapping)
	require.Equal(t, "lookup", gjson.GetBytes(adapted, "tools.0.name").String())
	require.Equal(t, "message", gjson.GetBytes(adapted, "input.0.type").String())
	require.False(t, gjson.GetBytes(adapted, `input.#(type=="additional_tools")`).Exists())

	invalid := []byte(`{"input":[{"type":"additional_tools","tools":"not-an-array"}]}`)
	unchanged, mapping, err := adaptOpenAIResponsesClientTools(invalid)
	require.ErrorContains(t, err, "additional_tools.tools must be an array")
	require.Equal(t, invalid, unchanged)
	require.Empty(t, mapping)
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

func TestOpenAIForward_NativeResponsesLiteToolsReachCustomAPIKeyUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"deepseek-chat","stream":false,"tool_choice":{"type":"custom","name":"exec"},"input":[
		{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"run pwd"}]}
	]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set(responsesLiteHeader, "true")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_lite_tools","status":"completed","output":[
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec","arguments":"{\"input\":\"pwd\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}
	svc := openAIClientToolsTestService(upstream)
	account := &Account{
		ID:          7660,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://api.deepseek.com"},
		Extra:       openAIResponsesSupportedTestExtra(),
	}

	result, err := svc.Forward(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "exec", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tool_choice.type").String())
	require.Equal(t, "exec", gjson.GetBytes(upstream.lastBody, "tool_choice.name").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools")`).Exists())
	require.Equal(t, "custom_tool_call", gjson.Get(recorder.Body.String(), "output.0.type").String())
	require.Equal(t, "pwd", gjson.Get(recorder.Body.String(), "output.0.input").String())
	require.Equal(t, "deepseek-chat", result.BillingModel)
}

func TestOpenAIForward_NativeResponsesLiteToolsRestoreStreamingCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"deepseek-chat","stream":true,"input":[
		{"type":"additional_tools","tools":[{"type":"custom","name":"exec"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"run pwd"}]}
	]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set(responsesLiteHeader, "true")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

	sse := strings.Join([]string{
		`data: {"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec","status":"in_progress"}}`,
		`data: {"type":"response.function_call_arguments.done","sequence_number":1,"item_id":"fc_1","call_id":"call_1","name":"exec","arguments":"{\"input\":\"pwd\"}"}`,
		`data: {"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec","arguments":"{\"input\":\"pwd\"}","status":"completed"}}`,
		`data: {"type":"response.completed","sequence_number":3,"response":{"id":"resp_lite_stream","model":"deepseek-chat","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec","arguments":"{\"input\":\"pwd\"}"}],"usage":{"input_tokens":2,"output_tokens":3}}}`,
	}, "\n\n") + "\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}
	svc := openAIClientToolsTestService(upstream)
	account := &Account{
		ID:          7661,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://api.deepseek.com"},
		Extra:       openAIResponsesSupportedTestExtra(),
	}

	result, err := svc.Forward(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools")`).Exists())
	output := recorder.Body.String()
	require.Contains(t, output, `"type":"custom_tool_call"`)
	require.Contains(t, output, `"type":"response.custom_tool_call_input.done"`)
	require.Contains(t, output, `"input":"pwd"`)
	require.NotContains(t, output, `"type":"function_call"`)
	require.Equal(t, "deepseek-chat", result.BillingModel)
}

func TestOpenAIForward_NativeResponsesLiteFunctionCarrierAndOfficialPreservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"deepseek-chat","stream":false,"input":[
		{"type":"additional_tools","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"look up"}]}
	]}`)
	for _, tc := range []struct {
		name       string
		baseURL    string
		wantLifted bool
	}{
		{name: "third-party native Responses", baseURL: "https://api.deepseek.com", wantLifted: true},
		{name: "official OpenAI default URL", baseURL: "", wantLifted: false},
		{name: "official OpenAI explicit URL", baseURL: "https://api.openai.com", wantLifted: false},
		{name: "official OpenAI versioned URL", baseURL: "https://api.openai.com/v1/", wantLifted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			c.Request.Header.Set(responsesLiteHeader, "true")
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"resp_tools","model":"deepseek-chat","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
			}}
			svc := openAIClientToolsTestService(upstream)
			account := &Account{
				ID:          7662,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "test-key", "base_url": tc.baseURL},
				Extra:       openAIResponsesSupportedTestExtra(),
			}

			result, err := svc.Forward(context.Background(), c, account, body)

			require.NoError(t, err)
			require.NotNil(t, result)
			if tc.wantLifted {
				require.Equal(t, "lookup", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
				require.False(t, gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools")`).Exists())
			} else {
				require.False(t, gjson.GetBytes(upstream.lastBody, "tools").Exists())
				require.True(t, gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools")`).Exists())
			}
		})
	}
}

func TestOpenAIPassthroughAPIKeyPreservesCustomToolOutputContentParts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","stream":false,"tools":[{"type":"custom","name":"exec"}],"input":[{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":"result"},{"type":"input_file","file_id":"file_123"}]}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_tools","status":"completed","output":[],"usage":{}}`)),
	}}
	svc := openAIClientToolsTestService(upstream)
	account := &Account{ID: 6240, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}

	result, err := svc.forwardOpenAIPassthrough(context.Background(), c, account, body, "gpt-5.4", nil, false, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "function_call_output", gjson.GetBytes(upstream.lastBody, "input.0.type").String())
	output := gjson.GetBytes(upstream.lastBody, "input.0.output")
	require.True(t, output.IsArray(), "native Responses content parts must reach the upstream as an array")
	require.Equal(t, "input_text", output.Get("0.type").String())
	require.Equal(t, "result", output.Get("0.text").String())
	require.Equal(t, "input_file", output.Get("1.type").String())
	require.Equal(t, "file_123", output.Get("1.file_id").String())
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

func TestAdaptOpenAIResponsesClientToolsWithInheritedMapping_DelegatesWhenBodyDeclaresTools(t *testing.T) {
	body := openAIClientToolsRequest(false)
	// A previous turn's tool_search state must not leak into a turn that
	// declares its own fresh tools.
	previousMapping := apicompat.ResponsesClientToolMapping{ToolSearch: true}
	previousLoweredTools := []any{map[string]any{"type": "function", "name": "prior_tool_search_proxy"}}

	adapted, mapping, loweredTools, err := adaptOpenAIResponsesClientToolsWithInheritedMapping(body, previousMapping, previousLoweredTools)

	require.NoError(t, err)
	assertOpenAIClientToolsLowered(t, adapted)
	require.True(t, mapping.CustomTools["exec"])
	require.False(t, mapping.ToolSearch)
	require.Len(t, loweredTools, 2)
}

func TestAdaptOpenAIResponsesClientToolsWithInheritedMapping_LiteCarrierReplacesAndThenRestoresState(t *testing.T) {
	carrier := []byte(`{"model":"deepseek-chat","input":[{"type":"additional_tools","tools":[{"type":"custom","name":"exec"}]}]}`)
	previousMapping := apicompat.ResponsesClientToolMapping{ToolSearch: true}
	previousTools := []any{map[string]any{"type": "function", "name": "prior_tool_search_proxy"}}

	firstBody, firstMapping, firstTools, err := adaptOpenAIResponsesClientToolsWithInheritedMapping(carrier, previousMapping, previousTools)
	require.NoError(t, err)
	require.True(t, firstMapping.CustomTools["exec"])
	require.False(t, firstMapping.ToolSearch)
	require.Len(t, firstTools, 1)
	require.Equal(t, "exec", gjson.GetBytes(firstBody, "tools.0.name").String())
	require.False(t, gjson.GetBytes(firstBody, `input.#(type=="additional_tools")`).Exists())

	followup := []byte(`{"model":"deepseek-chat","input":[{"type":"custom_tool_call","call_id":"c1","name":"exec","input":"pwd"}]}`)
	secondBody, secondMapping, secondTools, err := adaptOpenAIResponsesClientToolsWithInheritedMapping(followup, firstMapping, firstTools)
	require.NoError(t, err)
	require.True(t, secondMapping.CustomTools["exec"])
	require.Len(t, secondTools, 1)
	require.Equal(t, "function_call", gjson.GetBytes(secondBody, "input.0.type").String())
	require.Equal(t, "exec", gjson.GetBytes(secondBody, "tools.0.name").String())
}

func TestAdaptOpenAIResponsesClientToolsWithInheritedMapping_NoPreviousStateLeavesOmittedToolsBodyUnchanged(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"continue please"}`)

	adapted, mapping, loweredTools, err := adaptOpenAIResponsesClientToolsWithInheritedMapping(body, apicompat.ResponsesClientToolMapping{}, nil)

	require.NoError(t, err)
	require.Equal(t, body, adapted)
	require.Empty(t, mapping)
	require.Nil(t, loweredTools)
}

func TestAdaptOpenAIResponsesClientToolsWithInheritedMapping_WithoutRememberedLoweredToolsBehavesAsNoClientTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"continue please"}`)
	previousMapping := apicompat.ResponsesClientToolMapping{ToolSearch: true}

	adapted, mapping, loweredTools, err := adaptOpenAIResponsesClientToolsWithInheritedMapping(body, previousMapping, nil)

	require.NoError(t, err)
	require.Equal(t, body, adapted)
	require.Empty(t, mapping)
	require.Nil(t, loweredTools)
}

func TestAdaptOpenAIResponsesClientToolsWithInheritedMapping_BypassesMarkerPrefilterWhenInheritable(t *testing.T) {
	// This turn's body carries no custom/tool_search/namespace markers at
	// all -- it's exactly the shape a WS HTTP bridge follow-up turn takes
	// when the client omits "tools" and simply continues the conversation,
	// relying on the previous turn's negotiated mapping. The cheap
	// gjson-based prefilter in needsOpenAIResponsesClientToolAdaptation
	// would say "nothing to do" on its own; a recorded previous mapping
	// must be able to override that.
	body := []byte(`{"model":"gpt-5.5","input":"continue please"}`)
	previousMapping := apicompat.ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}}
	previousLoweredTools := []any{map[string]any{"type": "function", "name": "exec", "parameters": map[string]any{"type": "object"}}}

	adapted, mapping, loweredTools, err := adaptOpenAIResponsesClientToolsWithInheritedMapping(body, previousMapping, previousLoweredTools)

	require.NoError(t, err)
	require.True(t, mapping.CustomTools["exec"])
	require.Len(t, loweredTools, 1)
	require.Equal(t, "function", gjson.GetBytes(adapted, "tools.0.type").String())
	require.Equal(t, "exec", gjson.GetBytes(adapted, "tools.0.name").String())
	require.Equal(t, "continue please", gjson.GetBytes(adapted, "input").String())
}

func TestAdaptOpenAIResponsesClientToolsWithInheritedMapping_ReinstatesMappingAndDowngradesInputHistoryWhenToolsOmitted(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"custom_tool_call","call_id":"c1","name":"exec","input":"pwd"}]}`)
	previousMapping := apicompat.ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}}
	previousLoweredTools := []any{map[string]any{"type": "function", "name": "exec", "parameters": map[string]any{"type": "object"}}}

	adapted, mapping, loweredTools, err := adaptOpenAIResponsesClientToolsWithInheritedMapping(body, previousMapping, previousLoweredTools)

	require.NoError(t, err)
	require.True(t, mapping.CustomTools["exec"])
	require.Len(t, loweredTools, 1)
	require.Equal(t, "function", gjson.GetBytes(adapted, "tools.0.type").String())
	require.Equal(t, "function_call", gjson.GetBytes(adapted, "input.0.type").String())
	require.JSONEq(t, `{"input":"pwd"}`, gjson.GetBytes(adapted, "input.0.arguments").String())
}

func TestAdaptOpenAIResponsesClientToolsWithInheritedMapping_RejectsMalformedBodyWhenInheritable(t *testing.T) {
	previousMapping := apicompat.ResponsesClientToolMapping{ToolSearch: true}
	previousLoweredTools := []any{map[string]any{"type": "function", "name": "prior_tool_search_proxy"}}

	adapted, mapping, loweredTools, err := adaptOpenAIResponsesClientToolsWithInheritedMapping([]byte(`not-json`), previousMapping, previousLoweredTools)

	require.ErrorContains(t, err, "decode OpenAI Responses client tools")
	require.Equal(t, []byte(`not-json`), adapted)
	require.Empty(t, mapping)
	require.Nil(t, loweredTools)
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
