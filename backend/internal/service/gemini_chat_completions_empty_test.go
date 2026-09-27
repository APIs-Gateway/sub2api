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
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const geminiMalformedFunctionCall = `{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}}}`

func TestGeminiChatCompletionsStream_SignatureOnlyDoesNotCommitEmptySuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := antigravityEmptyStreamTestResponse(geminiMalformedFunctionCall)

	result, err := svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-3.8-flash", true, false)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Empty(t, rec.Body.String(), "role chunk and [DONE] must wait for real content")
	require.False(t, c.Writer.Written())
}

func TestGeminiChatCompletionsStream_CancelWinsOverEmptyResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	resp := antigravityEmptyStreamTestResponse(geminiMalformedFunctionCall)

	result, err := svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-3.8-flash", true, false)
	require.Nil(t, result)
	require.True(t, errors.Is(err, context.Canceled))
	require.Empty(t, rec.Body.String())
}

func TestGeminiChatCompletionsNonStream_SignatureOnlyDoesNotReturn200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(geminiMalformedFunctionCall))}

	usage, err := svc.handleChatCompletionsNonStreamingResponseFromGemini(c, resp, "gemini-3.8-flash", true)
	require.Nil(t, usage)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Empty(t, rec.Body.String())
}

func TestGeminiChatCompletionsBufferedOAuth_SignatureOnlyDoesNotReturn200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &geminiCompatHTTPUpstreamStub{response: antigravityEmptyStreamTestResponse(geminiMalformedFunctionCall, "[DONE]")}
	svc := &GeminiMessagesCompatService{tokenProvider: &GeminiTokenProvider{}, httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{
		ID: 101, Platform: PlatformGemini, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "ya29.test-token", "project_id": "project-1"},
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hi"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Empty(t, rec.Body.String())
}
