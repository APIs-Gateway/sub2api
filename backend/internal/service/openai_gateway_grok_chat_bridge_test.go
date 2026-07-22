//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGrokChatResponsesBridgeEligibilityKeepsOnlyLosslessRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "text with function tool",
			body: `{"model":"grok-4.3","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"auto"}`,
			want: true,
		},
		{
			name: "image content",
			body: `{"model":"grok-4.3","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`,
			want: true,
		},
		{
			name: "stop stays native",
			body: `{"model":"grok-4.3","messages":[{"role":"user","content":"hello"}],"stop":["END"]}`,
			want: false,
		},
		{
			name: "unknown content stays native",
			body: `{"model":"grok-4.3","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AA=="}}]}]}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eligible, reason := grokChatResponsesBridgeEligibility([]byte(tt.body))
			require.Equal(t, tt.want, eligible, "reason=%s", reason)
		})
	}
}

func TestGrokChatResponsesBridgeEligibilityRejectsUnsafeShapes(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		reason string
	}{
		{name: "invalid json", body: `{"model":`, reason: "invalid_json"},
		{name: "null root", body: `null`, reason: "invalid_json"},
		{name: "unsupported stop", body: `{"model":"grok-4.3","messages":[{"role":"user","content":"hello"}],"stop":["END"]}`, reason: "unsupported_stop"},
		{name: "invalid instructions", body: `{"model":"grok-4.3","instructions":1,"messages":[{"role":"user","content":"hello"}]}`, reason: "invalid_instructions"},
		{name: "invalid response format", body: `{"model":"grok-4.3","response_format":[],"messages":[{"role":"user","content":"hello"}]}`, reason: "invalid_response_format"},
		{name: "invalid service tier", body: `{"model":"grok-4.3","service_tier":1,"messages":[{"role":"user","content":"hello"}]}`, reason: "invalid_service_tier"},
		{name: "invalid tools", body: `{"model":"grok-4.3","tools":{},"messages":[{"role":"user","content":"hello"}]}`, reason: "invalid_tools"},
		{name: "unsupported functions", body: `{"model":"grok-4.3","functions":[{}],"messages":[{"role":"user","content":"hello"}]}`, reason: "unsupported_functions"},
		{name: "unsupported tool choice", body: `{"model":"grok-4.3","tool_choice":1,"messages":[{"role":"user","content":"hello"}]}`, reason: "unsupported_tool_choice"},
		{name: "required tool choice without tools", body: `{"model":"grok-4.3","tool_choice":"required","messages":[{"role":"user","content":"hello"}]}`, reason: "required_tool_choice_without_tools"},
		{name: "unsupported function call", body: `{"model":"grok-4.3","function_call":"auto","messages":[{"role":"user","content":"hello"}]}`, reason: "unsupported_function_call"},
		{name: "unknown field", body: `{"model":"grok-4.3","unknown":true,"messages":[{"role":"user","content":"hello"}]}`, reason: "unknown_field_unknown"},
		{name: "invalid model", body: `{"model":1,"messages":[{"role":"user","content":"hello"}]}`, reason: "invalid_model"},
		{name: "invalid stream", body: `{"model":"grok-4.3","stream":null,"messages":[{"role":"user","content":"hello"}]}`, reason: "invalid_stream"},
		{name: "invalid parallel tool calls", body: `{"model":"grok-4.3","parallel_tool_calls":null,"messages":[{"role":"user","content":"hello"}]}`, reason: "invalid_parallel_tool_calls"},
		{name: "invalid stream options", body: `{"model":"grok-4.3","stream_options":[],"messages":[{"role":"user","content":"hello"}]}`, reason: "invalid_stream_options"},
		{name: "unknown stream option", body: `{"model":"grok-4.3","stream_options":{"unknown":true},"messages":[{"role":"user","content":"hello"}]}`, reason: "unknown_stream_option_unknown"},
		{name: "invalid stream include usage", body: `{"model":"grok-4.3","stream_options":{"include_usage":1},"messages":[{"role":"user","content":"hello"}]}`, reason: "invalid_stream_include_usage"},
		{name: "unsafe max tokens", body: `{"model":"grok-4.3","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`, reason: "unsafe_max_tokens"},
		{name: "invalid max tokens", body: `{"model":"grok-4.3","max_tokens":"128","messages":[{"role":"user","content":"hello"}]}`, reason: "unsafe_max_tokens"},
		{name: "conflicting max tokens", body: `{"model":"grok-4.3","max_tokens":128,"max_completion_tokens":128,"messages":[{"role":"user","content":"hello"}]}`, reason: "conflicting_max_tokens"},
		{name: "invalid temperature", body: `{"model":"grok-4.3","temperature":"hot","messages":[{"role":"user","content":"hello"}]}`, reason: "invalid_temperature"},
		{name: "invalid top p", body: `{"model":"grok-4.3","top_p":null,"messages":[{"role":"user","content":"hello"}]}`, reason: "invalid_top_p"},
		{name: "invalid prompt cache key", body: `{"model":"grok-4.3","prompt_cache_key":1,"messages":[{"role":"user","content":"hello"}]}`, reason: "invalid_prompt_cache_key"},
		{name: "missing messages", body: `{"model":"grok-4.3"}`, reason: "invalid_messages"},
		{name: "empty messages", body: `{"model":"grok-4.3","messages":[]}`, reason: "invalid_messages"},
		{name: "invalid message role", body: `{"model":"grok-4.3","messages":[{"role":1,"content":"hello"}]}`, reason: "invalid_message_role"},
		{name: "missing user content", body: `{"model":"grok-4.3","messages":[{"role":"user"}]}`, reason: "non_text_message_content"},
		{name: "empty user content", body: `{"model":"grok-4.3","messages":[{"role":"user","content":""}]}`, reason: "empty_message_content"},
		{name: "unsupported content part", body: `{"model":"grok-4.3","messages":[{"role":"user","content":[{"type":"input_audio"}]}]}`, reason: "unsupported_content_part_input_audio"},
		{name: "unsafe assistant field", body: `{"model":"grok-4.3","messages":[{"role":"assistant","content":"hello","metadata":{}}]}`, reason: "unsafe_message_field_metadata"},
		{name: "invalid reasoning content", body: `{"model":"grok-4.3","messages":[{"role":"assistant","content":"hello","reasoning_content":1}]}`, reason: "invalid_reasoning_content"},
		{name: "invalid assistant tool calls", body: `{"model":"grok-4.3","messages":[{"role":"assistant","content":"hello","tool_calls":{}}]}`, reason: "invalid_tool_calls"},
		{name: "assistant without content", body: `{"model":"grok-4.3","messages":[{"role":"assistant","content":null}]}`, reason: "non_text_message_content"},
		{name: "empty assistant content", body: `{"model":"grok-4.3","messages":[{"role":"assistant","content":""}]}`, reason: "empty_message_content"},
		{name: "unsupported assistant content", body: `{"model":"grok-4.3","messages":[{"role":"assistant","content":[{"type":"input_audio"}]}]}`, reason: "unsupported_content_part_input_audio"},
		{name: "missing tool call id", body: `{"model":"grok-4.3","messages":[{"role":"tool","content":"ok"}]}`, reason: "invalid_tool_call_id"},
		{name: "invalid tool content", body: `{"model":"grok-4.3","messages":[{"role":"tool","tool_call_id":"call-1","content":1}]}`, reason: "invalid_tool_message_content"},
		{name: "unsupported message role", body: `{"model":"grok-4.3","messages":[{"role":"developer","content":"hello"}]}`, reason: "unsupported_message_role_developer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eligible, reason := grokChatResponsesBridgeEligibility([]byte(tt.body))
			require.False(t, eligible)
			require.Equal(t, tt.reason, reason)
		})
	}
}

func TestGrokChatResponsesBridgeEligibilityAcceptsNullableAndToolMessages(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "nullable optional fields",
			body: `{"model":"grok-4.3","stop":null,"reasoning_effort":null,"instructions":null,"response_format":null,"service_tier":null,"functions":[],"function_call":"none","tool_choice":null,"stream":false,"parallel_tool_calls":true,"stream_options":{"include_usage":true},"max_completion_tokens":128,"temperature":0.2,"top_p":0.8,"prompt_cache_key":"cache-1","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name: "assistant tool call and tool response",
			body: `{"model":"grok-4.3","tools":[{"type":"function","function":{"name":"lookup","description":"look up","parameters":{"type":"object"},"strict":true}}],"tool_choice":"required","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","index":0,"function":{"name":"lookup","arguments":"{\"q\":\"hello\"}"}}]},{"role":"tool","tool_call_id":"call-1","content":"result"}]}`,
		},
		{
			name: "assistant reasoning with empty structured content",
			body: `{"model":"grok-4.3","messages":[{"role":"assistant","content":[],"reasoning_content":"thinking"}]}`,
		},
		{
			name: "user text and image parts",
			body: `{"model":"grok-4.3","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eligible, reason := grokChatResponsesBridgeEligibility([]byte(tt.body))
			require.True(t, eligible, "reason=%s", reason)
			require.Empty(t, reason)
		})
	}
}

func TestGrokChatFunctionDeclarationsBridgeableRejectsUnsafeDefinitions(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		want   bool
		reason string
	}{
		{name: "null", raw: `null`, want: true},
		{name: "invalid list", raw: `{}`, reason: "invalid_tools"},
		{name: "invalid tool", raw: `[null]`, reason: "invalid_tool"},
		{name: "unsafe tool field", raw: `[{"type":"function","function":{},"extra":true}]`, reason: "unsafe_tool_field_extra"},
		{name: "unsupported tool type", raw: `[{"type":"custom","function":{}}]`, reason: "unsupported_tool_type"},
		{name: "missing function", raw: `[{"type":"function"}]`, reason: "invalid_tool_function"},
		{name: "invalid function", raw: `[{"type":"function","function":[]}]`, reason: "invalid_tool_function"},
		{name: "unsafe function field", raw: `[{"type":"function","function":{"name":"lookup","parameters":{},"extra":true}}]`, reason: "unsafe_tool_function_field_extra"},
		{name: "missing name", raw: `[{"type":"function","function":{"parameters":{}}}]`, reason: "invalid_tool_function_name"},
		{name: "invalid name", raw: `[{"type":"function","function":{"name":1,"parameters":{}}}]`, reason: "invalid_tool_function_name"},
		{name: "invalid description", raw: `[{"type":"function","function":{"name":"lookup","description":1,"parameters":{}}}]`, reason: "invalid_tool_function_description"},
		{name: "missing parameters", raw: `[{"type":"function","function":{"name":"lookup"}}]`, reason: "invalid_tool_function_parameters"},
		{name: "invalid parameters", raw: `[{"type":"function","function":{"name":"lookup","parameters":[]}}]`, reason: "invalid_tool_function_parameters"},
		{name: "invalid strict", raw: `[{"type":"function","function":{"name":"lookup","parameters":{},"strict":"true"}}]`, reason: "invalid_tool_function_strict"},
		{name: "valid", raw: `[{"type":"function","function":{"name":"lookup","description":"look up","parameters":{"type":"object"},"strict":true}}]`, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, reason := grokChatFunctionDeclarationsBridgeable([]byte(tt.raw))
			require.Equal(t, tt.want, ok)
			require.Equal(t, tt.reason, reason)
		})
	}
}

func TestGrokChatAssistantToolCallsBridgeableRejectsUnsafeCalls(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		count  int
		reason string
	}{
		{name: "null", raw: `null`},
		{name: "invalid list", raw: `{}`, reason: "invalid_tool_calls"},
		{name: "invalid call", raw: `[null]`, reason: "invalid_tool_call"},
		{name: "unsafe call field", raw: `[{"id":"call-1","type":"function","function":{},"extra":true}]`, reason: "unsafe_tool_call_field_extra"},
		{name: "invalid index", raw: `[{"id":"call-1","type":"function","index":"0","function":{}}]`, reason: "invalid_tool_call_index"},
		{name: "negative index", raw: `[{"id":"call-1","type":"function","index":-1,"function":{}}]`, reason: "invalid_tool_call_index"},
		{name: "missing id", raw: `[{"type":"function","function":{}}]`, reason: "invalid_tool_call_id"},
		{name: "invalid id", raw: `[{"id":1,"type":"function","function":{}}]`, reason: "invalid_tool_call_id"},
		{name: "unsupported type", raw: `[{"id":"call-1","type":"custom","function":{}}]`, reason: "unsupported_tool_call_type"},
		{name: "missing function", raw: `[{"id":"call-1","type":"function"}]`, reason: "invalid_tool_call_function"},
		{name: "invalid function", raw: `[{"id":"call-1","type":"function","function":[]}]`, reason: "invalid_tool_call_function"},
		{name: "unsafe function field", raw: `[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}","extra":true}}]`, reason: "unsafe_tool_call_function_field_extra"},
		{name: "missing function name", raw: `[{"id":"call-1","type":"function","function":{"arguments":"{}"}}]`, reason: "invalid_tool_call_function_name"},
		{name: "invalid function name", raw: `[{"id":"call-1","type":"function","function":{"name":1,"arguments":"{}"}}]`, reason: "invalid_tool_call_function_name"},
		{name: "missing arguments", raw: `[{"id":"call-1","type":"function","function":{"name":"lookup"}}]`, reason: "invalid_tool_call_arguments"},
		{name: "invalid arguments type", raw: `[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":1}}]`, reason: "invalid_tool_call_arguments"},
		{name: "invalid arguments json", raw: `[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"not-json"}}]`, reason: "invalid_tool_call_arguments"},
		{name: "valid", raw: `[{"id":"call-1","type":"function","index":0,"function":{"name":"lookup","arguments":"{}"}}]`, count: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, reason := grokChatAssistantToolCallsBridgeable([]byte(tt.raw))
			require.Equal(t, tt.count, count)
			require.Equal(t, tt.reason, reason)
		})
	}
}

func TestGrokChatStructuredContentBridgeableRejectsUnsafeParts(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		want   bool
		reason string
	}{
		{name: "invalid object", raw: `{}`, reason: "non_text_message_content"},
		{name: "empty array", raw: `[]`, reason: "empty_message_content"},
		{name: "missing type", raw: `[{}]`, reason: "non_text_message_content"},
		{name: "invalid type", raw: `[{"type":1}]`, reason: "non_text_message_content"},
		{name: "empty text", raw: `[{"type":"text","text":""}]`, reason: "empty_message_content"},
		{name: "invalid text", raw: `[{"type":"text","text":1}]`, reason: "empty_message_content"},
		{name: "unsupported part", raw: `[{"type":"input_audio"}]`, reason: "unsupported_content_part_input_audio"},
		{name: "text and image", raw: `[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]`, want: true},
		{name: "input image", raw: `[{"type":"input_image","image_url":"https://example.com/image.png"}]`, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, reason := grokChatStructuredContentBridgeable([]byte(tt.raw))
			require.Equal(t, tt.want, ok)
			require.Equal(t, tt.reason, reason)
		})
	}
}

func TestGrokChatBridgeSmallHelpers(t *testing.T) {
	require.True(t, grokChatNullOrNone([]byte(`null`)))
	require.True(t, grokChatNullOrNone([]byte(`"none"`)))
	require.True(t, grokChatNullOrNone([]byte(`"NONE"`)))
	require.False(t, grokChatNullOrNone([]byte(`"auto"`)))
	require.True(t, grokChatJSONNull([]byte(`null`)))
	require.False(t, grokChatJSONNull([]byte(`"null"`)))
	require.True(t, grokChatNullOrEmptyArray([]byte(`null`)))
	require.True(t, grokChatNullOrEmptyArray([]byte(`[]`)))
	require.False(t, grokChatNullOrEmptyArray([]byte(`[{}]`)))
}

func TestGrokChatToolChoiceBridgeableRejectsUnknownString(t *testing.T) {
	ok, reason := grokChatToolChoiceBridgeable([]byte(`"force"`))
	require.False(t, ok)
	require.Equal(t, "unsupported_tool_choice", reason)
}

func TestForwardGrokChatCompletionsBridgeGuards(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newContext := func() *gin.Context {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return c
	}
	validBody := []byte(`{"model":"grok-4.3","stream":false,"messages":[{"role":"user","content":"hello"}]}`)

	t.Run("invalid json", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		result, err := svc.forwardGrokChatCompletionsViaResponses(context.Background(), newContext(), grokProtocolOAuthAccount(7212), []byte(`{"model":`), "", "")
		require.Error(t, err)
		require.Nil(t, result)
		require.Contains(t, err.Error(), "parse grok chat completions request")
	})

	t.Run("missing token", func(t *testing.T) {
		account := grokProtocolOAuthAccount(7213)
		account.Credentials = map[string]any{"base_url": xai.DefaultBaseURL}
		svc := &OpenAIGatewayService{}
		result, err := svc.forwardGrokChatCompletionsViaResponses(context.Background(), newContext(), account, validBody, "", "")
		require.Error(t, err)
		require.Nil(t, result)
		require.Contains(t, err.Error(), "get grok access token")
	})

	t.Run("invalid upstream url", func(t *testing.T) {
		account := grokProtocolOAuthAccount(7214)
		account.Credentials["base_url"] = "://invalid"
		svc := &OpenAIGatewayService{}
		result, err := svc.forwardGrokChatCompletionsViaResponses(context.Background(), newContext(), account, validBody, "", "")
		require.Error(t, err)
		require.Nil(t, result)
		require.Contains(t, err.Error(), "build grok responses bridge request")
	})

	t.Run("upstream failover", func(t *testing.T) {
		upstream := &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"temporarily unavailable"}}`)),
		}}
		svc := &OpenAIGatewayService{httpUpstream: upstream}
		result, err := svc.forwardGrokChatCompletionsViaResponses(context.Background(), newContext(), grokProtocolOAuthAccount(7215), validBody, "", "")
		require.Error(t, err)
		require.Nil(t, result)
		var failoverErr *UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	})
}

func TestForwardGrokChatCompletionsUsesResponsesBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"grok-4.3","stream":false,"prompt_cache_key":"bridge-cache","messages":[{"role":"user","content":"hello"}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 7201})

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"grok-chat-responses"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_grok_chat","model":"grok-4.3","status":"in_progress","output":[]}}`,
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			`data: {"type":"response.completed","response":{"id":"resp_grok_chat","object":"response","model":"grok-4.3","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`,
			`data: [DONE]`,
		}, "\n\n"))),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := grokProtocolOAuthAccount(7201)

	result, err := svc.forwardGrokChatCompletions(context.Background(), c, account, body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Stream)
	require.Equal(t, xai.DefaultBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer oauth-protocol-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "grok-4.3", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "bridge-cache", gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "input.0.role").String())
	require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String(), upstream.lastReq.Header.Get(grokConversationIDHeader))
	require.Contains(t, recorder.Body.String(), `"chat.completion"`)
	require.Equal(t, int64(6), gjson.GetBytes(recorder.Body.Bytes(), "usage.total_tokens").Int())
}

func TestForwardGrokChatCompletionsBridgeContentPolicyUpdatesQuotaWithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := []byte(`{"model":"grok-4.3","stream":false,"messages":[{"role":"user","content":"describe this"}]}`)
	upstreamBody := `{"error":{"code":"new_sensitive","message":"image is sensitive"}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"Content-Type":                   []string{"application/json"},
			"X-Ratelimit-Remaining-Requests": []string{"0"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	updates := make(chan map[string]any, 1)
	repo := &snapshotUpdateAccountRepo{updateExtraCalls: updates}
	svc := &OpenAIGatewayService{httpUpstream: upstream, accountRepo: repo}
	account := grokProtocolOAuthAccount(7203)

	result, err := svc.forwardGrokChatCompletions(context.Background(), c, account, body, "", "")

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
	require.Equal(t, "image is sensitive", gjson.Get(recorder.Body.String(), "error.message").String())
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))

	select {
	case update := <-updates:
		snapshot, ok := update[grokQuotaSnapshotExtraKey].(*xai.QuotaSnapshot)
		require.True(t, ok)
		require.Equal(t, http.StatusForbidden, snapshot.StatusCode)
		require.NotNil(t, snapshot.Requests)
		require.NotNil(t, snapshot.Requests.Remaining)
		require.Equal(t, int64(0), *snapshot.Requests.Remaining)
	default:
		t.Fatal("expected Grok quota snapshot update")
	}
}

func TestForwardGrokChatCompletionsBridgeTransportError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	svc := &OpenAIGatewayService{
		httpUpstream: &httpUpstreamRecorder{err: errors.New("bridge upstream unavailable")},
	}

	result, err := svc.forwardGrokChatCompletions(
		context.Background(), c, grokProtocolOAuthAccount(7209),
		[]byte(`{"model":"grok-4.3","stream":false,"messages":[{"role":"user","content":"hello"}]}`), "", "",
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "upstream error")
}

func TestForwardGrokChatCompletionsBridgeOrdinaryClientError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.forwardGrokChatCompletions(
		context.Background(), c, grokProtocolOAuthAccount(7210),
		[]byte(`{"model":"grok-4.3","stream":false,"messages":[{"role":"user","content":"hello"}]}`), "", "",
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Upstream error: 400")
}

func TestForwardGrokChatCompletionsBridgeStreamsResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"grok-bridge-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_grok_stream","model":"grok-4.3","status":"in_progress","output":[]}}`,
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			`data: {"type":"response.completed","response":{"id":"resp_grok_stream","object":"response","model":"grok-4.3","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
			`data: [DONE]`,
		}, "\n\n"))),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.forwardGrokChatCompletions(
		context.Background(), c, grokProtocolOAuthAccount(7211),
		[]byte(`{"model":"grok-4.3","stream":true,"messages":[{"role":"user","content":"hello"}]}`), "", "",
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Contains(t, recorder.Body.String(), "hello")
}

func TestForwardGrokChatCompletionsRawMissingModelReturnsBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	svc := &OpenAIGatewayService{httpUpstream: &httpUpstreamRecorder{}}
	result, err := svc.forwardGrokChatCompletions(
		context.Background(), c, grokProtocolOAuthAccount(7204),
		[]byte(`{"messages":[{"role":"user","content":"hello"}]}`), "", "",
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "model is required", gjson.Get(recorder.Body.String(), "error.message").String())
}

func TestForwardGrokChatCompletionsRawTransportError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	upstream := &httpUpstreamRecorder{err: errors.New("upstream unavailable")}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.forwardGrokChatCompletions(
		context.Background(), c, grokProtocolOAuthAccount(7205),
		[]byte(`{"model":"grok-4.3","stop":["END"],"messages":[{"role":"user","content":"hello"}]}`), "", "",
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "upstream error")
	require.Len(t, upstream.requests, 1)
}

func TestForwardGrokChatCompletionsRawContentPolicyUpdatesQuotaWithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"Content-Type":                   []string{"application/json"},
			"X-Ratelimit-Remaining-Requests": []string{"0"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)),
	}}
	updates := make(chan map[string]any, 1)
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		accountRepo:  &snapshotUpdateAccountRepo{updateExtraCalls: updates},
	}
	account := grokProtocolOAuthAccount(7206)

	result, err := svc.forwardGrokChatCompletions(
		context.Background(), c, account,
		[]byte(`{"model":"grok-4.3","stop":["END"],"messages":[{"role":"user","content":"hello"}]}`), "", "",
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Equal(t, "text is sensitive", gjson.Get(recorder.Body.String(), "error.message").String())
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	update := <-updates
	snapshot, ok := update[grokQuotaSnapshotExtraKey].(*xai.QuotaSnapshot)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, snapshot.StatusCode)
}

func TestForwardGrokChatCompletionsRawOrdinaryClientError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.forwardGrokChatCompletions(
		context.Background(), c, grokProtocolOAuthAccount(7216),
		[]byte(`{"model":"grok-4.3","stop":["END"],"messages":[{"role":"user","content":"hello"}]}`), "", "",
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Upstream error: 400")
}

func TestForwardGrokChatCompletionsRawReturnsFailoverError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"temporarily unavailable"}}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.forwardGrokChatCompletions(
		context.Background(), c, grokProtocolOAuthAccount(7207),
		[]byte(`{"model":"grok-4.3","stop":["END"],"messages":[{"role":"user","content":"hello"}]}`), "", "",
	)

	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
}

func TestForwardGrokChatCompletionsRawStreamsChatChunks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "xai-request-id": []string{"grok-raw-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"id":"chatcmpl_grok_raw","object":"chat.completion.chunk","model":"grok-4.3","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl_grok_raw","object":"chat.completion.chunk","model":"grok-4.3","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl_grok_raw","object":"chat.completion.chunk","model":"grok-4.3","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
			`data: [DONE]`,
		}, "\n\n"))),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := grokProtocolOAuthAccount(7208)
	account.Credentials["model_mapping"] = map[string]any{"grok-4.3": "grok-4.5"}

	result, err := svc.forwardGrokChatCompletions(
		context.Background(), c, account,
		[]byte(`{"model":"grok-4.3","stream":true,"stop":["END"],"messages":[{"role":"user","content":"hello"}]}`), "", "",
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Contains(t, recorder.Body.String(), "hello")
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
}

func TestNormalizeGrokRequestIDHeader(t *testing.T) {
	require.NotPanics(t, func() { normalizeGrokRequestIDHeader(nil) })
	resp := &http.Response{Header: http.Header{"Xai-Request-Id": []string{"grok-request"}}}
	normalizeGrokRequestIDHeader(resp)
	require.Equal(t, "grok-request", resp.Header.Get("x-request-id"))
	resp.Header.Set("X-Request-Id", "existing")
	normalizeGrokRequestIDHeader(resp)
	require.Equal(t, "existing", resp.Header.Get("x-request-id"))
}

func TestForwardGrokChatCompletionsUsesNativeChatForUnsupportedFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"grok-4.3","stream":false,"stop":["END"],"prompt_cache_key":"internal-only","messages":[{"role":"user","content":"hello"}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 7202})

	upstreamBody := `{"id":"chatcmpl_grok","object":"chat.completion","model":"grok-4.3","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"grok-chat-native"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := grokProtocolAPIKeyAccount(7202)

	result, err := svc.forwardGrokChatCompletions(context.Background(), c, account, body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Stream)
	require.Equal(t, xai.DefaultBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer xai-protocol-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "END", gjson.GetBytes(upstream.lastBody, "stop.0").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
	require.NotEmpty(t, upstream.lastReq.Header.Get(grokConversationIDHeader))
	require.JSONEq(t, upstreamBody, recorder.Body.String())
}

func TestBuildGrokChatCompletionsURLUsesAccountBaseURL(t *testing.T) {
	t.Parallel()
	require.Equal(t, "https://xai.test/v1/chat/completions", xai.BuildChatCompletionsURL("https://xai.test/v1/"))
}
