//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/common"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdaptResponsesClientToolsForAnthropic_FlattensNamespace(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"claude-fable-5",
		"input":[{"type":"function_call","call_id":"call_1","namespace":"codex_app","name":"read_thread","arguments":"{}"}],
		"tools":[{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"read_thread","description":"Read a task","parameters":{"type":"object","properties":{}}}]}]
	}`)

	adapted, mapping, err := adaptResponsesClientToolsForAnthropic(body)
	require.NoError(t, err)
	require.Equal(t, apicompat.ResponsesNamespaceName{Namespace: "codex_app", Name: "read_thread"}, mapping.NamespaceTools["codex_app__read_thread"])

	var request map[string]any
	require.NoError(t, common.Unmarshal(adapted, &request))
	tools := request["tools"].([]any)
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	require.Equal(t, "function", tool["type"])
	require.Equal(t, "codex_app__read_thread", tool["name"])

	input := request["input"].([]any)
	call := input[0].(map[string]any)
	require.Equal(t, "codex_app__read_thread", call["name"])
	require.NotContains(t, call, "namespace")
}

func TestAdaptResponsesClientToolsForAnthropic_LiftsAdditionalTools(t *testing.T) {
	body := []byte(`{
		"model":"claude-fable-5",
		"input":[
			{"type":"additional_tools","tools":[
				{"type":"custom","name":"exec","description":"Run a command"},
				{"type":"namespace","name":"codex_app","tools":[
					{"type":"function","name":"read_thread","parameters":{"type":"object"}}
				]}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"}]}
		]
	}`)

	adapted, mapping, err := adaptResponsesClientToolsForAnthropic(body)
	require.NoError(t, err)
	require.True(t, mapping.CustomTools["exec"])
	require.Equal(t, apicompat.ResponsesNamespaceName{Namespace: "codex_app", Name: "read_thread"}, mapping.NamespaceTools["codex_app__read_thread"])

	var request map[string]any
	require.NoError(t, common.Unmarshal(adapted, &request))
	tools := request["tools"].([]any)
	require.Len(t, tools, 2)
	require.Equal(t, "function", tools[0].(map[string]any)["type"])
	require.Equal(t, "codex_app__read_thread", tools[1].(map[string]any)["name"])

	input := request["input"].([]any)
	require.Len(t, input, 1)
	require.Equal(t, "message", input[0].(map[string]any)["type"])
}

func TestAdaptResponsesClientToolsForAnthropic_HandlesPassthroughAndInvalidBodies(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5","input":"hello"}`)
	adapted, mapping, err := adaptResponsesClientToolsForAnthropic(body)
	require.NoError(t, err)
	require.Equal(t, body, adapted)
	require.Empty(t, mapping.CustomTools)
	require.False(t, mapping.ToolSearch)

	_, _, err = adaptResponsesClientToolsForAnthropic([]byte(`not-json`))
	require.Error(t, err)

	request := map[string]any{"input": []any{"unchanged", map[string]any{"type": "message"}}}
	changed, err := liftResponsesAdditionalTools(request)
	require.NoError(t, err)
	require.False(t, changed)
	require.Len(t, request["input"].([]any), 2)

	_, err = liftResponsesAdditionalTools(map[string]any{"input": []any{map[string]any{"type": "additional_tools", "tools": "not-an-array"}}})
	require.Error(t, err)

	changed, err = liftResponsesAdditionalTools(map[string]any{})
	require.NoError(t, err)
	require.False(t, changed)
}

func TestResponsesClientToolAdapter_LowersHistoryAndRestoresPayload(t *testing.T) {
	req := map[string]any{
		"tools": []any{
			map[string]any{"type": "custom", "name": "exec", "format": map[string]any{"type": "grammar"}},
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "namespace", "name": "team", "tools": []any{map[string]any{"type": "function", "name": "send"}}},
		},
		"tool_choice": map[string]any{"type": "custom", "name": "exec"},
		"input": []any{
			map[string]any{"type": "custom_tool_call", "call_id": "c1", "name": "exec", "input": "dir"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "c1", "output": "ok"},
			map[string]any{"type": "tool_search_call", "call_id": "s1", "arguments": map[string]any{"query": "git"}},
			map[string]any{"type": "tool_search_output", "call_id": "s1", "output": map[string]any{"groups": []string{"git"}}},
			map[string]any{"type": "function_call", "call_id": "n1", "namespace": "team", "name": "send", "arguments": "{}"},
		},
	}

	mapping, changed, err := apicompat.AdaptResponsesClientTools(req)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, mapping.CustomTools["exec"])
	require.True(t, mapping.ToolSearch)
	require.Equal(t, apicompat.ResponsesNamespaceName{Namespace: "team", Name: "send"}, mapping.NamespaceTools["team__send"])

	choice := req["tool_choice"].(map[string]any)
	require.Equal(t, "function", choice["type"])
	input := req["input"].([]any)
	require.Equal(t, "function_call", input[0].(map[string]any)["type"])
	require.Equal(t, "function_call_output", input[1].(map[string]any)["type"])
	require.Equal(t, "function_call", input[2].(map[string]any)["type"])
	require.Equal(t, "function_call_output", input[3].(map[string]any)["type"])
	require.Equal(t, "team__send", input[4].(map[string]any)["name"])

	payload := []byte(`{"id":"resp","output":[{"type":"function_call","id":"i1","call_id":"c1","name":"exec","arguments":"{\"input\":\"dir\"}"},{"type":"function_call","id":"i2","call_id":"s1","name":"tool_search","arguments":"{\"query\":\"git\"}"},{"type":"function_call","id":"i3","call_id":"n1","name":"team__send","arguments":"{}"}]}`)
	restored, restoredChanged, err := apicompat.RestoreResponsesClientToolPayload(payload, mapping)
	require.NoError(t, err)
	require.True(t, restoredChanged)
	require.JSONEq(t, `{"id":"resp","output":[{"type":"custom_tool_call","id":"i1","call_id":"c1","name":"exec","input":"dir"},{"type":"tool_search_call","id":"i2","call_id":"s1","execution":"client","arguments":{"query":"git"}},{"type":"function_call","id":"i3","call_id":"n1","name":"send","namespace":"team","arguments":"{}"}]}`, string(restored))
}

func TestResponsesClientToolStreamRestorer_ConvertsCustomToolLifecycle(t *testing.T) {
	restorer := apicompat.NewResponsesClientToolStreamRestorer(apicompat.ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}})
	added := restorer.Restore(apicompat.ResponsesStreamEvent{
		Type: "response.output_item.added", SequenceNumber: 7, OutputIndex: 0,
		Item: &apicompat.ResponsesOutput{Type: "function_call", ID: "i1", CallID: "c1", Name: "exec", Status: "in_progress"},
	})
	require.Len(t, added, 1)
	require.Equal(t, "custom_tool_call", added[0].Item.Type)
	require.Empty(t, restorer.Restore(apicompat.ResponsesStreamEvent{Type: "response.function_call_arguments.delta", SequenceNumber: 8, ItemID: "i1", Delta: `{"input":"di`}))

	done := restorer.Restore(apicompat.ResponsesStreamEvent{Type: "response.function_call_arguments.done", SequenceNumber: 9, ItemID: "i1", CallID: "c1", Name: "exec", Arguments: `{"input":"dir"}`})
	require.Len(t, done, 2)
	require.Equal(t, "response.custom_tool_call_input.delta", done[0].Type)
	require.Equal(t, "dir", done[0].Delta)
	require.Equal(t, "response.custom_tool_call_input.done", done[1].Type)
	require.Equal(t, "dir", done[1].Input)

	closed := restorer.Restore(apicompat.ResponsesStreamEvent{
		Type: "response.output_item.done", SequenceNumber: 10, OutputIndex: 0,
		Item: &apicompat.ResponsesOutput{Type: "function_call", ID: "i1", CallID: "c1", Name: "exec", Arguments: `{"input":"dir"}`, Status: "completed"},
	})
	require.Len(t, closed, 1)
	require.Equal(t, "custom_tool_call", closed[0].Item.Type)
	require.Equal(t, "dir", closed[0].Item.Input)
}

func namespaceToolAnthropicStream() string {
	return strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_namespace","type":"message","role":"assistant","content":[],"model":"claude-fable-5","stop_reason":"","usage":{"input_tokens":10}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_namespace","name":"codex_app__read_thread","input":{"thread_id":"123"}}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
}

func namespaceToolMapping() apicompat.ResponsesClientToolMapping {
	return apicompat.ResponsesClientToolMapping{NamespaceTools: map[string]apicompat.ResponsesNamespaceName{
		"codex_app__read_thread": {Namespace: "codex_app", Name: "read_thread"},
	}}
}

func TestHandleResponsesBufferedStreamingResponse_RestoresNamespaceTool(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream()))}

	svc := &GatewayService{}
	_, err := svc.handleResponsesBufferedStreamingResponse(resp, c, "claude-fable-5", "claude-fable-5", nil, time.Now(), namespaceToolMapping())
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), `"type":"function_call"`)
	require.Contains(t, rec.Body.String(), `"name":"read_thread"`)
	require.Contains(t, rec.Body.String(), `"namespace":"codex_app"`)
	require.NotContains(t, rec.Body.String(), `"name":"codex_app__read_thread"`)
}

func TestHandleResponsesStreamingResponse_RestoresNamespaceTool(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream()))}

	svc := &GatewayService{}
	_, err := svc.handleResponsesStreamingResponse(resp, c, "claude-fable-5", "claude-fable-5", nil, time.Now(), namespaceToolMapping())
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), `response.output_item.added`)
	require.Contains(t, rec.Body.String(), `"name":"read_thread"`)
	require.Contains(t, rec.Body.String(), `"namespace":"codex_app"`)
	require.NotContains(t, rec.Body.String(), `"name":"codex_app__read_thread"`)
}

func TestExtractResponsesReasoningEffortFromBody(t *testing.T) {
	t.Parallel()

	got := ExtractResponsesReasoningEffortFromBody([]byte(`{"model":"claude-sonnet-4.5","reasoning":{"effort":"HIGH"}}`))
	require.NotNil(t, got)
	require.Equal(t, "high", *got)

	maxGot := ExtractResponsesReasoningEffortFromBody([]byte(`{"model":"deepseek-v4-pro","reasoning":{"effort":"max"}}`))
	require.NotNil(t, maxGot)
	require.Equal(t, "xhigh", *maxGot)

	require.Nil(t, ExtractResponsesReasoningEffortFromBody([]byte(`{"model":"claude-sonnet-4.5"}`)))
}

func TestHandleResponsesBufferedStreamingResponse_PreservesMessageStartCacheUsage(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_buffered"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":12,"cache_read_input_tokens":9,"cache_creation_input_tokens":3}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleResponsesBufferedStreamingResponse(resp, c, "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 9, result.Usage.CacheReadInputTokens)
	require.Equal(t, 3, result.Usage.CacheCreationInputTokens)
	require.Contains(t, rec.Body.String(), `"cached_tokens":9`)
}

func TestHandleResponsesBufferedStreamingResponse_RestoresCustomToolCall(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_custom"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_custom","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_exec","name":"exec"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	_, err := svc.handleResponsesBufferedStreamingResponse(resp, c, "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), apicompat.ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}})
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), `"type":"custom_tool_call"`)
	require.Contains(t, rec.Body.String(), `"name":"exec"`)
	require.Contains(t, rec.Body.String(), `"input":"pwd"`)
}

func TestHandleResponsesStreamingResponse_PreservesMessageStartCacheUsage(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":20,"cache_read_input_tokens":11,"cache_creation_input_tokens":4}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleResponsesStreamingResponse(resp, c, "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 20, result.Usage.InputTokens)
	require.Equal(t, 8, result.Usage.OutputTokens)
	require.Equal(t, 11, result.Usage.CacheReadInputTokens)
	require.Equal(t, 4, result.Usage.CacheCreationInputTokens)
	require.Contains(t, rec.Body.String(), `response.completed`)
}

func TestParseAnthropicSSEField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		line      string
		field     string
		wantValue string
		wantOK    bool
	}{
		{
			name:      "standard format with space",
			line:      "event: message_start",
			field:     "event",
			wantValue: "message_start",
			wantOK:    true,
		},
		{
			name:      "compact format without space",
			line:      "event:message_start",
			field:     "event",
			wantValue: "message_start",
			wantOK:    true,
		},
		{
			name:      "data field with space",
			line:      "data: {\"type\":\"message_start\"}",
			field:     "data",
			wantValue: "{\"type\":\"message_start\"}",
			wantOK:    true,
		},
		{
			name:      "data field without space",
			line:      "data:{\"type\":\"message_start\"}",
			field:     "data",
			wantValue: "{\"type\":\"message_start\"}",
			wantOK:    true,
		},
		{
			name:      "field with multiple spaces after colon",
			line:      "event:  message_delta",
			field:     "event",
			wantValue: "message_delta",
			wantOK:    true,
		},
		{
			name:      "wrong field name",
			line:      "event: message_start",
			field:     "data",
			wantValue: "",
			wantOK:    false,
		},
		{
			name:      "empty line",
			line:      "",
			field:     "event",
			wantValue: "",
			wantOK:    false,
		},
		{
			name:      "line without colon",
			line:      "invalid line",
			field:     "event",
			wantValue: "",
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotValue, gotOK := parseAnthropicSSEField(tt.line, tt.field)
			require.Equal(t, tt.wantOK, gotOK, "parseAnthropicSSEField() ok")
			require.Equal(t, tt.wantValue, gotValue, "parseAnthropicSSEField() value")
		})
	}
}

func TestHandleResponsesBufferedStreamingResponse_CompactSSEFormat(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// Simulate compact SSE format without spaces after colons (e.g. Kimi API)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_compact"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`invalid event line`,
			`event:message_start`,
			`invalid data line`,
			`event:message_start`,
			`data:{"type":"message_start","message":{"id":"msg_compact","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":10}}}`,
			``,
			`event:content_block_start`,
			`data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"OK"}}`,
			``,
			`event:message_delta`,
			`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			``,
			`event:ping`,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleResponsesBufferedStreamingResponse(resp, c, "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
}

func TestHandleResponsesStreamingResponse_CompactSSEFormat(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// Simulate compact SSE format without spaces after colons (e.g. Kimi API)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_compact_stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`invalid event line`,
			`event:message_start`,
			`invalid data line`,
			`event:message_start`,
			`data:{"type":"message_start","message":{"id":"msg_compact_stream","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":15}}}`,
			``,
			`event:content_block_start`,
			`data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"OK"}}`,
			``,
			`event:message_delta`,
			`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6}}`,
			``,
			`event:ping`,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleResponsesStreamingResponse(resp, c, "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 15, result.Usage.InputTokens)
	require.Equal(t, 6, result.Usage.OutputTokens)
	require.Contains(t, rec.Body.String(), `response.completed`)
}
