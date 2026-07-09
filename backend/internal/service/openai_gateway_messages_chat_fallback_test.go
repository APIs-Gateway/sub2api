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

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func forceChatMessagesFallbackAccount() *Account {
	account := rawChatCompletionsTestAccount()
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
	}
	return account
}

type errTailReader struct {
	data []byte
	off  int
	err  error
}

func (r *errTailReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	return 0, r.err
}

func (r *errTailReader) Close() error { return nil }

func newMessagesChatFallbackContext(t *testing.T, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

func TestForwardAsAnthropic_ForceChatCompletionsNonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_msg_chat_json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_json","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":1}}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "http://upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "messages.0.content").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "stream_options").Exists())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "assistant", gjson.Get(rec.Body.String(), "role").String())
	require.Equal(t, "ok", gjson.Get(rec.Body.String(), "content.0.text").String())
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
	require.False(t, result.Stream)
}

func TestForwardAsAnthropic_ForceChatCompletionsRequestValidationErrors(t *testing.T) {
	tests := []struct {
		name       string
		body       []byte
		wantErr    string
		wantClient string
	}{
		{
			name:       "invalid json",
			body:       []byte(`{"model":`),
			wantErr:    "parse anthropic request",
			wantClient: "Failed to parse request body",
		},
		{
			name:       "missing model",
			body:       []byte(`{"max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`),
			wantErr:    "missing model",
			wantClient: "model is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := newMessagesChatFallbackContext(t, tt.body)
			upstream := &httpUpstreamRecorder{}
			svc := &OpenAIGatewayService{
				cfg:          rawChatCompletionsTestConfig(),
				httpUpstream: upstream,
			}

			result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), tt.body, "", "")

			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
			require.Nil(t, result)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), tt.wantClient)
			require.Nil(t, upstream.lastReq)
		})
	}
}

func TestForwardAsAnthropic_ForceChatCompletionsBetaFastModeSetsPriorityTier(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, rec := newMessagesChatFallbackContext(t, body)
	c.Request.Header.Set("anthropic-beta", claude.BetaFastMode)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_msg_chat_fast"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_fast","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "priority", gjson.GetBytes(upstream.lastBody, "service_tier").String())
	require.NotNil(t, result.ServiceTier)
	require.Equal(t, "priority", *result.ServiceTier)
}

func TestForwardAsAnthropic_ForceChatCompletionsBetaFastModeBlockedByPolicy(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, rec := newMessagesChatFallbackContext(t, body)
	c.Request.Header.Set("anthropic-beta", claude.BetaFastMode)

	upstream := &httpUpstreamRecorder{}
	svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:  OpenAIFastTierPriority,
			Action:       BetaPolicyActionBlock,
			Scope:        BetaPolicyScopeAll,
			ErrorMessage: "priority tier blocked",
		}},
	})
	svc.cfg = rawChatCompletionsTestConfig()
	svc.httpUpstream = upstream

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "priority tier blocked")
	require.Nil(t, result)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "priority tier blocked")
	require.Nil(t, upstream.lastReq)
}

func TestForwardAsAnthropic_ForceChatCompletionsAccountConfigurationErrors(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":false}`)

	tests := []struct {
		name    string
		mutate  func(*Account)
		wantErr string
	}{
		{
			name: "missing api key",
			mutate: func(account *Account) {
				account.Credentials["api_key"] = ""
			},
			wantErr: "missing api_key",
		},
		{
			name: "invalid base url",
			mutate: func(account *Account) {
				account.Credentials["base_url"] = "http://[::1"
			},
			wantErr: "invalid base_url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newMessagesChatFallbackContext(t, body)
			upstream := &httpUpstreamRecorder{}
			svc := &OpenAIGatewayService{
				cfg:          rawChatCompletionsTestConfig(),
				httpUpstream: upstream,
			}
			account := forceChatMessagesFallbackAccount()
			tt.mutate(account)

			result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")

			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
			require.Nil(t, result)
			require.Nil(t, upstream.lastReq)
		})
	}
}

func TestForwardAsAnthropic_ForceChatCompletionsTransportError(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, _ := newMessagesChatFallbackContext(t, body)
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: &httpUpstreamRecorder{err: errors.New("upstream dial failed")},
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")

	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "upstream error: 502")
}

func TestForwardAsAnthropic_ForceChatCompletionsFailover429RecordsOpsEvent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, _ := newMessagesChatFallbackContext(t, body)

	cfg := rawChatCompletionsTestConfig()
	cfg.Gateway.LogUpstreamErrorBody = true
	cfg.Gateway.LogUpstreamErrorBodyMaxBytes = 64
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_msg_chat_429"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited by upstream","type":"rate_limit_error"}}`)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          cfg,
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")

	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)

	eventsVal, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, castOK := eventsVal.([]*OpsUpstreamErrorEvent)
	require.True(t, castOK)
	require.Len(t, events, 1)
	require.Equal(t, "failover", events[0].Kind)
	require.Equal(t, "rate limited by upstream", events[0].Message)
	require.Contains(t, events[0].Detail, "rate limited")
}

func TestForwardAsAnthropic_ForceChatCompletionsInvalidUpstreamJSON(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, rec := newMessagesChatFallbackContext(t, body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_msg_chat_bad_json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":`)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "parse chat completions response")
	require.Nil(t, result)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "Failed to parse upstream response")
}

func TestForwardAsAnthropic_ForceChatCompletionsStreamingClosesOpenBlockOnDone(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"not valid json"`,
		"",
		`data: {"id":"chatcmpl_s","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_s","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"he"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_s","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"llo"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_s","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: {"id":"chatcmpl_s","object":"chat.completion.chunk","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_msg_chat_stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())

	out := rec.Body.String()
	require.Contains(t, out, "event: message_start")
	require.Contains(t, out, `"text":"he"`)
	require.Contains(t, out, `"text":"llo"`)
	require.Contains(t, out, "event: content_block_stop")
	require.Contains(t, out, `"stop_reason":"end_turn"`)
	require.Contains(t, out, "event: message_stop")

	blockStop := strings.Index(out, "event: content_block_stop")
	msgDelta := strings.Index(out, `"stop_reason":"end_turn"`)
	msgStop := strings.Index(out, "event: message_stop")
	require.Greater(t, msgDelta, blockStop)
	require.Greater(t, msgStop, msgDelta)

	require.Equal(t, 4, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.True(t, result.Stream)
	require.NotNil(t, result.FirstTokenMs)
}

func TestForwardAsAnthropic_ForceChatCompletionsStreamingToolCallAggregation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"weather in sf?"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_t","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_t","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_t","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"sf\"}"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_t","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: {"id":"chatcmpl_t","object":"chat.completion.chunk","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":6,"completion_tokens":5,"total_tokens":11}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_msg_chat_tool"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)

	out := rec.Body.String()
	require.Contains(t, out, `"type":"tool_use"`)
	require.Contains(t, out, `"name":"get_weather"`)
	require.Contains(t, out, `"input_json_delta"`)
	require.Contains(t, out, `"stop_reason":"tool_use"`)
	require.Contains(t, out, "event: message_stop")
	require.Equal(t, 6, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
}

func TestForwardAsAnthropic_ForceChatCompletionsStreamingLengthMapsToMaxTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_l","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant","content":"truncat"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_l","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":4,"completion_tokens":8,"total_tokens":12}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_msg_chat_len"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"stop_reason":"max_tokens"`)
	require.Contains(t, rec.Body.String(), "event: message_stop")
}

func TestForwardAsAnthropic_ForceChatCompletionsEmptyStreamStillFramesMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_msg_chat_empty"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "event: message_start")
	require.Contains(t, rec.Body.String(), "event: message_delta")
	require.Contains(t, rec.Body.String(), "event: message_stop")
}

func TestForwardAsAnthropic_ForceChatCompletionsNonFailover400UsesSharedErrorHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_msg_chat_400"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid roles","type":"invalid_request_error"}}`)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.Error(t, err)
	require.Nil(t, result)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "error", gjson.Get(rec.Body.String(), "type").String())
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, "invalid roles", gjson.Get(rec.Body.String(), "error.message").String())

	statusVal, ok := c.Get(OpsUpstreamStatusCodeKey)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, statusVal)

	eventsVal, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, castOK := eventsVal.([]*OpsUpstreamErrorEvent)
	require.True(t, castOK)
	require.Len(t, events, 1)
	require.Equal(t, http.StatusBadRequest, events[0].UpstreamStatusCode)
	require.Equal(t, "http_error", events[0].Kind)
	require.Equal(t, "invalid roles", events[0].Message)
}

func TestForwardAsAnthropic_ForceChatCompletionsStreamReadErrorSkipsFinalize(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	partial := strings.Join([]string{
		`data: {"id":"chatcmpl_e","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_e","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"he"},"finish_reason":null}]}`,
		"",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_msg_chat_err"}},
		Body:       &errTailReader{data: []byte(partial), err: errors.New("simulated upstream read failure")},
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "stream usage incomplete")
	require.NotNil(t, result)
	require.True(t, result.Stream)

	out := rec.Body.String()
	require.Contains(t, out, `"text":"he"`)
	require.NotContains(t, out, "event: message_stop")
}

func TestForwardAsAnthropic_ResponsesSupportedAccountStillUsesResponsesEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_native","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_msg_native"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}
	account := rawChatCompletionsTestAccount()
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesMode:      string(openai_compat.ResponsesSupportModeAuto),
		openai_compat.ExtraKeyResponsesSupported: true,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, strings.HasSuffix(upstream.lastReq.URL.Path, "/responses"))
	require.True(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
	require.Equal(t, "ok", gjson.Get(rec.Body.String(), "content.0.text").String())
}
