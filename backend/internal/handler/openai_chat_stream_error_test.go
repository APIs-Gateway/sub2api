//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIHandlerStreamReadErrorBody struct {
	sent bool
}

func (r *openAIHandlerStreamReadErrorBody) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")), nil
	}
	return 0, errors.New("stream error: stream ID 7; INTERNAL_ERROR; received from peer")
}

func (r *openAIHandlerStreamReadErrorBody) Close() error { return nil }

type openAIHandlerStreamReadErrorUpstream struct{}

func (openAIHandlerStreamReadErrorUpstream) response() (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"stream-read-error"},
		},
		Body: &openAIHandlerStreamReadErrorBody{},
	}, nil
}

func (u openAIHandlerStreamReadErrorUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return u.response()
}

func (u openAIHandlerStreamReadErrorUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	return u.response()
}

func TestOpenAIGatewayHandlerChatCompletions_ClassifiesUpstreamStreamReadError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accountRepo := openAIImagesFailoverAccountRepo{accounts: []service.Account{
		{
			ID:          1,
			Name:        "stream-error-account",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Extra:       map[string]any{"openai_responses_supported": true},
			Credentials: map[string]any{"access_token": "token", "api_key": "sk-test", "openai_capabilities": []any{"chat_completions"}},
		},
	}}
	h := newOpenAIResponsesFailoverTestHandlerWithRepo(t, openAIHandlerStreamReadErrorUpstream{}, accountRepo)
	c, rec := newOpenAIFailoverTestContext(t, context.Background(), "/v1/chat/completions", `{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"hello"}]}`, false)

	h.ChatCompletions(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "event: error")
	require.Contains(t, rec.Body.String(), `"code":"upstream_http2_stream_error"`)
	require.NotContains(t, rec.Body.String(), "stream ID")
}
