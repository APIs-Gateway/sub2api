package service

import (
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

const antigravityZeroMalformedFunctionCall = `{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0}}}`

func antigravityEmptyStreamTestResponse(payloads ...string) *http.Response {
	var body strings.Builder
	for _, payload := range payloads {
		_, _ = body.WriteString("data: ")
		_, _ = body.WriteString(payload)
		_, _ = body.WriteString("\n\n")
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body.String()))}
}

func TestHandleClaudeStreamingResponse_MalformedSignatureSwitchesAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := antigravityEmptyStreamTestResponse(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0}}}`)

	result, err := svc.handleClaudeStreamingResponse(c, resp, time.Now(), "gemini-3.8-flash")
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Contains(t, string(failoverErr.ResponseBody), "malformed function call")
	require.Empty(t, rec.Body.String(), "failed attempt must not commit a 200 stream before failover")
	require.False(t, c.Writer.Written())
}

func TestHandleClaudeStreamingResponse_OtherEmptyStreamsRetrySameAccount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"zero-usage-only", `{"response":{"usageMetadata":{"promptTokenCount":0}}}`},
		{"unparseable", "not json"},
		{"signature-only stop", `{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"STOP"}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			resp := antigravityEmptyStreamTestResponse(tc.payload)

			result, err := svc.handleClaudeStreamingResponse(c, resp, time.Now(), "gemini-3.8-flash")
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.RetryableOnSameAccount)
			require.Empty(t, rec.Body.String())
			require.False(t, c.Writer.Written())
		})
	}
}

func TestHandleClaudeStreamingResponse_MalformedThenEmptyStopRetriesSameAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := antigravityEmptyStreamTestResponse(
		antigravityZeroMalformedFunctionCall,
		`{"response":{"candidates":[{"finishReason":"STOP"}]}}`,
	)

	result, err := svc.handleClaudeStreamingResponse(c, resp, time.Now(), "gemini-3.8-flash")
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Empty(t, rec.Body.String())
	require.False(t, c.Writer.Written())
}

func TestHandleClaudeStreamingResponse_MalformedThenContentKeepsEventOrder(t *testing.T) {
	for _, tc := range []struct {
		name         string
		firstPayload string
		payload      string
		want         string
	}{
		{"text", geminiMalformedFunctionCall, `{"response":{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}}`, `"text":"answer"`},
		{"tool", geminiMalformedFunctionCall, `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]},"finishReason":"STOP"}]}}`, `"name":"lookup"`},
		{"thinking signature", `{"response":{"candidates":[{"content":{"parts":[{"text":"","thought":true,"thoughtSignature":"sig"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`, `{"response":{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}}`, `"text":"answer"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			resp := antigravityEmptyStreamTestResponse(tc.firstPayload, tc.payload)

			result, err := svc.handleClaudeStreamingResponse(c, resp, time.Now(), "gemini-3.8-flash")
			require.NoError(t, err)
			require.NotNil(t, result)
			body := rec.Body.String()
			require.Contains(t, body, "event: message_start")
			require.Contains(t, body, tc.want)
			require.Contains(t, body, "event: message_stop")
			require.Less(t, strings.Index(body, "event: message_start"), strings.Index(body, tc.want))
			require.Less(t, strings.Index(body, tc.want), strings.Index(body, "event: message_stop"))
			if tc.name == "thinking signature" {
				require.Contains(t, body, "signature_delta")
				require.Less(t, strings.Index(body, "signature_delta"), strings.Index(body, tc.want))
			}
			require.Equal(t, 1, strings.Count(body, "event: message_stop"))
		})
	}
}

func TestHandleClaudeStreamingResponse_PreludeFlushesBeforeFirstContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := antigravityEmptyStreamTestResponse(
		`{"response":{"candidates":[{"content":{"parts":[{"text":"","thought":true,"thoughtSignature":"sig"}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}}`,
	)

	result, err := svc.handleClaudeStreamingResponse(c, resp, time.Now(), "gemini-3.8-flash")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.clientDisconnect)
	body := rec.Body.String()
	require.Less(t, strings.Index(body, "event: message_start"), strings.Index(body, "signature_delta"))
	require.Less(t, strings.Index(body, "signature_delta"), strings.Index(body, `"text":"answer"`))
	require.Contains(t, body, "event: message_stop")
}

func TestHandleClaudeStreamingResponse_PreContentBufferIsBounded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	largeSignature := strings.Repeat("s", antigravityPreContentBufferLimit)
	resp := antigravityEmptyStreamTestResponse(`{"response":{"candidates":[{"content":{"parts":[{"text":"","thought":true,"thoughtSignature":"` + largeSignature + `"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`)

	result, err := svc.handleClaudeStreamingResponse(c, resp, time.Now(), "gemini-3.8-flash")
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Empty(t, rec.Body.String())
	require.True(t, failoverErr.RetryableOnSameAccount)
}

func TestHandleClaudeStreamingResponse_GroundingAtEOFReleasesPrelude(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := antigravityEmptyStreamTestResponse(
		`{"response":{"candidates":[{"content":{"parts":[{"text":"","thought":true,"thoughtSignature":"sig"}]}}]}}`,
		`{"response":{"candidates":[{"groundingMetadata":{"webSearchQueries":["lookup"]},"finishReason":"STOP"}]}}`,
	)

	result, err := svc.handleClaudeStreamingResponse(c, resp, time.Now(), "gemini-3.8-flash")
	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.Less(t, strings.Index(body, "event: message_start"), strings.Index(body, "signature_delta"))
	require.Less(t, strings.Index(body, "signature_delta"), strings.Index(body, "lookup"))
	require.Contains(t, body, "event: message_stop")
}

func TestHandleClaudeStreamingResponse_KeepaliveDoesNotCommitEmptyStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{
		MaxLineSize: defaultMaxLineSize, StreamKeepaliveInterval: 1,
	}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	reader, writer := io.Pipe()
	writeErr := make(chan error, 1)
	go func() {
		_, err := io.WriteString(writer, "data: "+antigravityZeroMalformedFunctionCall+"\n\n")
		if err == nil {
			time.Sleep(2200 * time.Millisecond) // Wait past two keepalive ticks before upstream EOF.
		}
		writeErr <- err
		_ = writer.Close()
	}()
	resp := &http.Response{StatusCode: http.StatusOK, Body: reader}

	result, err := svc.handleClaudeStreamingResponse(c, resp, time.Now(), "gemini-3.8-flash")
	require.NoError(t, <-writeErr)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Empty(t, rec.Body.String(), "pre-content keepalive must not commit HTTP 200")
	require.False(t, c.Writer.Written())
	require.False(t, failoverErr.RetryableOnSameAccount)
}

func TestHandleClaudeStreamingResponse_CanceledEmptyStreamDoesNotFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	resp := antigravityEmptyStreamTestResponse(`{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`)

	result, err := svc.handleClaudeStreamingResponse(c, resp, time.Now(), "gemini-3.8-flash")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.clientDisconnect)
	require.Empty(t, rec.Body.String())
}

func TestHandleClaudeStreamToNonStreaming_MalformedSignatureSwitchesAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := antigravityEmptyStreamTestResponse(`{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`)

	result, err := svc.handleClaudeStreamToNonStreaming(c, resp, time.Now(), "gemini-3.8-flash")
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Empty(t, rec.Body.String())
}

func TestHandleClaudeStreamToNonStreaming_OtherEmptyRetriesSameAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := antigravityEmptyStreamTestResponse(`{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"STOP"}]}}`)

	result, err := svc.handleClaudeStreamToNonStreaming(c, resp, time.Now(), "gemini-3.8-flash")
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Empty(t, rec.Body.String())
}

func TestHandleClaudeStreamToNonStreaming_MalformedThenEmptyStopRetriesSameAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := antigravityEmptyStreamTestResponse(
		antigravityZeroMalformedFunctionCall,
		`{"response":{"candidates":[{"finishReason":"STOP"}]}}`,
	)

	result, err := svc.handleClaudeStreamToNonStreaming(c, resp, time.Now(), "gemini-3.8-flash")
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Empty(t, rec.Body.String())
}

func TestHandleClaudeStreamToNonStreaming_CancelWinsOverEmptyStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	resp := antigravityEmptyStreamTestResponse(`{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`)

	result, err := svc.handleClaudeStreamToNonStreaming(c, resp, time.Now(), "gemini-3.8-flash")
	require.Nil(t, result)
	require.True(t, errors.Is(err, context.Canceled))
	require.Empty(t, rec.Body.String())
}

func TestHandleClaudeStreamToNonStreaming_SubstantiveContentSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{"text", `{"response":{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}}`, `answer`},
		{"thinking", `{"response":{"candidates":[{"content":{"parts":[{"text":"reason","thought":true}]},"finishReason":"STOP"}]}}`, `reason`},
		{"tool call", `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]},"finishReason":"STOP"}]}}`, `lookup`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			resp := antigravityEmptyStreamTestResponse(tc.payload)

			result, err := svc.handleClaudeStreamToNonStreaming(c, resp, time.Now(), "gemini-3.8-flash")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), tc.want)
		})
	}
}
