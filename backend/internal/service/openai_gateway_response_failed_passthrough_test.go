//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const responseFailedContextMessage = "Your input exceeds the context window of this model. Please adjust your input and try again."

func buildResponseFailedSSEForPassthroughTest(errorCode, errorType, message string) string {
	return fmt.Sprintf(
		"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"object\":\"response\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":%q,\"type\":%q,\"message\":%q}}}\n\n",
		errorCode,
		errorType,
		message,
	)
}

func newResponseFailedUpstreamForPassthroughTest() *httpUpstreamRecorder {
	return newResponseFailedUpstreamForPassthroughTestWithError(
		"context_length_exceeded",
		"invalid_request_error",
		responseFailedContextMessage,
	)
}

func newResponseFailedUpstreamForPassthroughTestWithError(errorCode, errorType, message string) *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"rid-response-failed-passthrough"},
		},
		Body: io.NopCloser(strings.NewReader(buildResponseFailedSSEForPassthroughTest(errorCode, errorType, message))),
	}}
}

func newResponseFailedAfterOutputUpstreamForPassthroughTest() *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"rid-response-failed-after-output"},
		},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: response.created`,
			`data: {"type":"response.created","response":{"id":"resp_failed","object":"response","model":"gpt-5.4","status":"in_progress","output":[]}}`,
			"",
			`event: response.output_text.delta`,
			`data: {"type":"response.output_text.delta","delta":"partial output"}`,
			"",
			strings.TrimSpace(buildResponseFailedSSEForPassthroughTest(
				"context_length_exceeded",
				"invalid_request_error",
				responseFailedContextMessage,
			)),
		}, "\n"))),
	}}
}

func bindResponseFailedStatusCodeRule(c *gin.Context, platform string) {
	bindResponseFailedRule(c, platform, http.StatusBadRequest, "context_length_exceeded", http.StatusBadRequest)
}

func bindResponseFailedRule(c *gin.Context, platform string, upstreamStatus int, keyword string, responseCode int) {
	rule := &model.ErrorPassthroughRule{
		ID:              1,
		Name:            "response-failed-context-rule",
		Enabled:         true,
		Priority:        1,
		Platforms:       []string{platform},
		ErrorCodes:      []int{upstreamStatus},
		Keywords:        []string{keyword},
		MatchMode:       model.MatchModeAll,
		ResponseCode:    &responseCode,
		PassthroughBody: true,
	}
	svc := &ErrorPassthroughService{}
	svc.setLocalCache([]*model.ErrorPassthroughRule{rule})
	BindErrorPassthroughService(c, svc)
}

func responseFailedResponsesTestAccount() *Account {
	account := rawChatCompletionsTestAccount()
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceResponses),
	}
	return account
}

func assertResponseFailedWasNotRetried(t *testing.T, err error) {
	t.Helper()
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
}

func TestOpenAIResponseFailedContextWindowDetection(t *testing.T) {
	tests := []struct {
		name    string
		message string
		payload []byte
		want    bool
	}{
		{
			name:    "nested error code",
			payload: []byte(`{"response":{"error":{"code":"context_length_exceeded"}}}`),
			want:    true,
		},
		{
			name:    "maximum context length message",
			message: "Maximum context length is 8192 tokens",
			want:    true,
		},
		{
			name:    "context window too large",
			message: "The context window is too large for this model",
			want:    true,
		},
		{
			name:    "unrelated upstream failure",
			message: "Selected model is at capacity",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isOpenAIContextWindowError(tt.message, tt.payload))
		})
	}
}

func TestOpenAIContextWindowErrorDoesNotFailover(t *testing.T) {
	svc := &OpenAIGatewayService{}
	contextBody := []byte(`{"error":{"code":"context_length_exceeded","message":"` + responseFailedContextMessage + `"}}`)
	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(http.StatusBadGateway, responseFailedContextMessage, contextBody))
	require.True(t, svc.shouldFailoverOpenAIUpstreamResponse(http.StatusBadGateway, "upstream unavailable", []byte(`{"error":{"message":"upstream unavailable"}}`)))
}

func TestOpenAIStreamFailedEventSemanticStatus(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		message string
		want    int
	}{
		{
			name:    "context window",
			payload: []byte(`{"response":{"error":{"code":"context_length_exceeded"}}}`),
			want:    http.StatusBadRequest,
		},
		{
			name:    "invalid request",
			payload: []byte(`{"error":{"type":"invalid_request_error"}}`),
			want:    http.StatusBadRequest,
		},
		{
			name:    "rate limit",
			payload: []byte(`{"response":{"error":{"code":"rate_limit_exceeded"}}}`),
			want:    http.StatusTooManyRequests,
		},
		{
			name:    "authentication",
			payload: []byte(`{"error":{"type":"authentication_error"}}`),
			want:    http.StatusUnauthorized,
		},
		{
			name:    "permission",
			payload: []byte(`{"error":{"message":"Access denied by permission policy"}}`),
			want:    http.StatusForbidden,
		},
		{
			name:    "overloaded",
			payload: []byte(`{"response":{"error":{"code":"server_is_overloaded"}}}`),
			want:    http.StatusServiceUnavailable,
		},
		{
			name:    "unknown",
			payload: []byte(`{"error":{"code":"upstream_error"}}`),
			want:    http.StatusBadGateway,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, openAIStreamFailedEventSemanticStatus(tt.payload, tt.message))
		})
	}
}

func TestOpenAIStreamFailedEventPassthroughBody(t *testing.T) {
	t.Run("preserves top level error", func(t *testing.T) {
		payload := []byte(`{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`)
		require.Equal(t, payload, openAIStreamFailedEventPassthroughBody(payload, "ignored"))
	})

	t.Run("normalizes nested response error", func(t *testing.T) {
		body := openAIStreamFailedEventPassthroughBody([]byte(`{"response":{"error":{"type":"invalid_request_error","code":"context_length_exceeded","param":"input","message":"too long"}}}`), "ignored")
		require.Equal(t, "invalid_request_error", gjson.GetBytes(body, "error.type").String())
		require.Equal(t, "context_length_exceeded", gjson.GetBytes(body, "error.code").String())
		require.Equal(t, "input", gjson.GetBytes(body, "error.param").String())
		require.Equal(t, "too long", gjson.GetBytes(body, "error.message").String())
	})

	t.Run("creates fallback error body", func(t *testing.T) {
		body := openAIStreamFailedEventPassthroughBody([]byte(`{"type":"response.failed"}`), "upstream stopped")
		require.Equal(t, "upstream stopped", gjson.GetBytes(body, "error.message").String())
	})

	t.Run("keeps a payload without an error or fallback", func(t *testing.T) {
		payload := []byte(`{"type":"response.failed"}`)
		require.Equal(t, payload, openAIStreamFailedEventPassthroughBody(payload, ""))
	})

	t.Run("leaves invalid payload untouched", func(t *testing.T) {
		payload := []byte(`not-json`)
		require.Equal(t, payload, openAIStreamFailedEventPassthroughBody(payload, "upstream stopped"))
	})
}

func TestOpenAIResponseFailedAfterClientOutputUsesProtocolError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		endpoint string
		body     []byte
		forward  func(*OpenAIGatewayService, *gin.Context, *Account, []byte) error
	}{
		{
			name:     "chat streaming",
			endpoint: "/v1/chat/completions",
			body:     []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`),
			forward: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
				return err
			},
		},
		{
			name:     "messages streaming",
			endpoint: "/v1/messages",
			body:     []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`),
			forward: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tt.endpoint, bytes.NewReader(tt.body))
			bindResponseFailedStatusCodeRule(c, PlatformOpenAI)

			upstream := newResponseFailedAfterOutputUpstreamForPassthroughTest()
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			err := tt.forward(svc, c, responseFailedResponsesTestAccount(), tt.body)

			require.Error(t, err)
			assertResponseFailedWasNotRetried(t, err)
			require.Equal(t, "/v1/responses", upstream.lastReq.URL.Path)
			require.True(t, IsResponseCommitted(c))
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), "partial output")
			require.Contains(t, rec.Body.String(), "upstream_error")
			require.Contains(t, rec.Body.String(), "context window")
		})
	}

}

func TestOpenAIHandleErrorResponse_ContextWindowKeepsMessageWithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"` + responseFailedContextMessage + `","type":"upstream_error"}}`))),
	}
	account := &Account{ID: 14, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	_, err := (&OpenAIGatewayService{}).handleErrorResponse(context.Background(), resp, c, account, nil)

	require.Error(t, err)
	assertResponseFailedWasNotRetried(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, responseFailedContextMessage, gjson.Get(rec.Body.String(), "error.message").String())
}

func TestOpenAIResponseFailedStatusCodeRuleMatchesAcrossProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("chat buffered", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		bindResponseFailedStatusCodeRule(c, PlatformOpenAI)

		upstream := newResponseFailedUpstreamForPassthroughTest()
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		_, err := svc.ForwardAsChatCompletions(context.Background(), c, responseFailedResponsesTestAccount(), body, "", "")

		require.Error(t, err)
		assertResponseFailedWasNotRetried(t, err)
		require.Equal(t, "/v1/responses", upstream.lastReq.URL.Path)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "upstream_error", gjson.Get(rec.Body.String(), "error.type").String())
		require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), "context window")
	})

	t.Run("chat streaming", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		bindResponseFailedStatusCodeRule(c, PlatformOpenAI)

		upstream := newResponseFailedUpstreamForPassthroughTest()
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		_, err := svc.ForwardAsChatCompletions(context.Background(), c, responseFailedResponsesTestAccount(), body, "", "")

		require.Error(t, err)
		assertResponseFailedWasNotRetried(t, err)
		require.Equal(t, "/v1/responses", upstream.lastReq.URL.Path)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "upstream_error", gjson.Get(rec.Body.String(), "error.type").String())
	})

	t.Run("messages buffered", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		bindResponseFailedStatusCodeRule(c, PlatformOpenAI)

		upstream := newResponseFailedUpstreamForPassthroughTest()
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		_, err := svc.ForwardAsAnthropic(context.Background(), c, responseFailedResponsesTestAccount(), body, "", "")

		require.Error(t, err)
		assertResponseFailedWasNotRetried(t, err)
		require.Equal(t, "/v1/responses", upstream.lastReq.URL.Path)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "upstream_error", gjson.Get(rec.Body.String(), "error.type").String())
	})

	t.Run("messages streaming", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		bindResponseFailedStatusCodeRule(c, PlatformOpenAI)

		upstream := newResponseFailedUpstreamForPassthroughTest()
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		_, err := svc.ForwardAsAnthropic(context.Background(), c, responseFailedResponsesTestAccount(), body, "", "")

		require.Error(t, err)
		assertResponseFailedWasNotRetried(t, err)
		require.Equal(t, "/v1/responses", upstream.lastReq.URL.Path)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "upstream_error", gjson.Get(rec.Body.String(), "error.type").String())
	})
}

func TestOpenAIResponseFailedStatusCodeRuleMatchesNativeStreams(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name            string
		wantPassthrough bool
		handle          func(*OpenAIGatewayService, *http.Response, *gin.Context, *Account) error
	}{
		{
			name:            "responses stream",
			wantPassthrough: false,
			handle: func(svc *OpenAIGatewayService, resp *http.Response, c *gin.Context, account *Account) error {
				_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.4", "gpt-5.4")
				return err
			},
		},
		{
			name:            "responses passthrough stream",
			wantPassthrough: true,
			handle: func(svc *OpenAIGatewayService, resp *http.Response, c *gin.Context, account *Account) error {
				_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.4", "gpt-5.4")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			bindResponseFailedStatusCodeRule(c, PlatformOpenAI)

			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			err := tt.handle(svc, newResponseFailedUpstreamForPassthroughTest().resp, c, rawChatCompletionsTestAccount())

			require.Error(t, err)
			assertResponseFailedWasNotRetried(t, err)
			require.True(t, IsResponseCommitted(c))
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "upstream_error", gjson.Get(rec.Body.String(), "error.type").String())
			rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok)
			events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.Len(t, events, 1)
			require.Equal(t, "http_error", events[0].Kind)
			require.Equal(t, tt.wantPassthrough, events[0].Passthrough)
			require.Contains(t, events[0].Message, "context window")
		})
	}
}

func TestOpenAIResponseFailedRetryableErrorKeepsFailoverPrecedence(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name   string
		handle func(*OpenAIGatewayService, *http.Response, *gin.Context, *Account) error
	}{
		{
			name: "responses stream",
			handle: func(svc *OpenAIGatewayService, resp *http.Response, c *gin.Context, account *Account) error {
				_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.4", "gpt-5.4")
				return err
			},
		},
		{
			name: "responses passthrough stream",
			handle: func(svc *OpenAIGatewayService, resp *http.Response, c *gin.Context, account *Account) error {
				_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.4", "gpt-5.4")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			bindResponseFailedRule(c, PlatformOpenAI, http.StatusTooManyRequests, "rate_limit_exceeded", http.StatusTooManyRequests)

			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			err := tt.handle(svc, newResponseFailedUpstreamForPassthroughTestWithError(
				"rate_limit_exceeded",
				"rate_limit_error",
				"rate limit reached",
			).resp, c, rawChatCompletionsTestAccount())

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.False(t, IsResponseCommitted(c))
			require.False(t, c.Writer.Written())
		})
	}
}
