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

func TestGeminiChatCompletionsStream_MalformedSignatureSwitchesAccount(t *testing.T) {
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
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Contains(t, string(failoverErr.ResponseBody), "malformed function call")
	require.Empty(t, rec.Body.String(), "role chunk and [DONE] must wait for real content")
	require.False(t, c.Writer.Written())
}

func TestGeminiChatCompletionsStream_OtherEmptyStreamsRetrySameAccount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"usage-only", `{"response":{"usageMetadata":{"promptTokenCount":10}}}`},
		{"unparseable", "not json"},
		{"signature-only stop", `{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"STOP"}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			resp := antigravityEmptyStreamTestResponse(tc.payload)

			result, err := svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-3.8-flash", true, false)
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.RetryableOnSameAccount)
			require.Empty(t, rec.Body.String())
			require.False(t, c.Writer.Written())
		})
	}
}

func TestGeminiChatCompletionsStream_MalformedThenEmptyStopRetriesSameAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := antigravityEmptyStreamTestResponse(
		geminiMalformedFunctionCall,
		`{"response":{"candidates":[{"finishReason":"STOP"}]}}`,
	)

	result, err := svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-3.8-flash", true, false)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Empty(t, rec.Body.String())
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
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Empty(t, rec.Body.String())
}

func TestGeminiChatCompletionsNonStream_OtherEmptyRetriesSameAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"STOP"}]}}`))}

	usage, err := svc.handleChatCompletionsNonStreamingResponseFromGemini(c, resp, "gemini-3.8-flash", true)
	require.Nil(t, usage)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameAccount)
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
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Empty(t, rec.Body.String())
}

func TestGeminiChatCompletionsBufferedOAuth_MalformedThenEmptyStopRetriesSameAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &geminiCompatHTTPUpstreamStub{response: antigravityEmptyStreamTestResponse(
		geminiMalformedFunctionCall,
		`{"response":{"candidates":[{"finishReason":"STOP"}]}}`,
		"[DONE]",
	)}
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
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Empty(t, rec.Body.String())
}

func TestHasGeminiChatCompletionContent(t *testing.T) {
	tests := []struct {
		name string
		part map[string]any
		want bool
	}{
		{"signature only", map[string]any{"thoughtSignature": "sig"}, false},
		{"text", map[string]any{"text": "answer"}, true},
		{"function call", map[string]any{"functionCall": map[string]any{"name": "lookup", "args": map[string]any{}}}, true},
		{"inline image", map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "aGVsbG8="}}, true},
		{"invalid inline image", map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "bad base64"}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			response := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{tc.part}}}}}
			require.Equal(t, tc.want, hasGeminiChatCompletionContent(response))
		})
	}
}

func TestGeminiChatCompletionsStream_FirstContentStartsProtocolOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{"text", `{"response":{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}}`, `"content":"answer"`},
		{"function call", `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"id":1}}}]},"finishReason":"STOP"}]}}`, `lookup`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			resp := antigravityEmptyStreamTestResponse(geminiMalformedFunctionCall, tc.payload, "[DONE]")

			result, err := svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-3.8-flash", true, false)
			require.NoError(t, err)
			require.NotNil(t, result)
			body := rec.Body.String()
			require.Contains(t, body, tc.want)
			require.Equal(t, 1, strings.Count(body, `"role":"assistant"`))
			require.Contains(t, body, "[DONE]")
		})
	}
}

type cancelOnRead struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (r *cancelOnRead) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.cancel()
	}
	return n, err
}

func TestGeminiChatCompletionsStream_CancelBeforeFirstContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"text", `{"response":{"candidates":[{"content":{"parts":[{"text":"answer"}]}}]}}`},
		{"function call", `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
			resp := antigravityEmptyStreamTestResponse(tc.payload)
			resp.Body = io.NopCloser(&cancelOnRead{reader: resp.Body, cancel: cancel})

			result, err := svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-3.8-flash", true, false)
			require.Nil(t, result)
			require.ErrorIs(t, err, context.Canceled)
			require.Empty(t, rec.Body.String())
		})
	}
}

func TestGeminiChatCompletionsNonStream_CancelBeforeResponseWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(&cancelOnRead{reader: strings.NewReader(`{"response":{"candidates":[{"content":{"parts":[{"text":"answer"}]}}]}}`), cancel: cancel})}

	usage, err := svc.handleChatCompletionsNonStreamingResponseFromGemini(c, resp, "gemini-3.8-flash", true)
	require.Nil(t, usage)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, rec.Body.String())
}

func TestGeminiChatCompletionsBufferedOAuth_CancelBeforeResponseWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := antigravityEmptyStreamTestResponse(`{"response":{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}}`, "[DONE]")
	resp.Body = io.NopCloser(&cancelOnRead{reader: resp.Body, cancel: cancel})
	upstream := &geminiCompatHTTPUpstreamStub{response: resp}
	svc := &GeminiMessagesCompatService{tokenProvider: &GeminiTokenProvider{}, httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{
		ID: 101, Platform: PlatformGemini, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "ya29.test-token", "project_id": "project-1"},
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hi"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)

	result, err := svc.ForwardAsChatCompletions(ctx, c, account, body)
	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, rec.Body.String())
}
