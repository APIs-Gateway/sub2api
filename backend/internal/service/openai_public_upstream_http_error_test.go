//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const publicErrTestUpstreamBody = `{"error":{"type":"invalid_request_error","message":"当前凭证的可用额度已用完。 原因：商家额度不足 request_id: 20261108083307947543269abc"}}`

func requireNoUpstreamLeak(t *testing.T, body string) {
	t.Helper()
	require.NotContains(t, body, "request_id")
	require.NotContains(t, body, "2026110808")
	require.NotContains(t, body, "当前凭证")
	require.NotContains(t, body, "商家")
}

func TestOpenAIPublicUpstreamHTTPError_StatusAndMessage(t *testing.T) {
	cases := []struct {
		upstream   int
		wantStatus int
		wantType   string
		wantMsg    string
	}{
		{400, 400, "invalid_request_error", "Invalid request parameters, please check the request body and try again"},
		{404, 404, "not_found_error", "Requested upstream resource was not found"},
		{413, 413, "invalid_request_error", "Request body is too large, please reduce the input size and try again"},
		{422, 422, "invalid_request_error", "Invalid request parameters, please check the request body and try again"},
		{429, 429, "rate_limit_error", "Upstream rate limit exceeded, please retry later"},
		// upstream 401 -> 503 (same exception as the Responses table)
		{401, 503, "upstream_error", "Upstream authentication failed, please contact administrator"},
		{402, 402, "upstream_error", "Upstream payment required: insufficient balance or billing issue"},
		{403, 403, "upstream_error", "Upstream access forbidden, please contact administrator"},
		{500, 500, "upstream_error", "Upstream service temporarily unavailable"},
		{502, 502, "upstream_error", "Upstream service temporarily unavailable"},
		{503, 503, "upstream_error", "Upstream service temporarily unavailable"},
		{504, 504, "upstream_error", "Upstream service temporarily unavailable"},
		// 529 / 522 / 524 / unregistered codes follow codexCanonicalStatus
		{529, 503, "overloaded_error", "Upstream service overloaded, please retry later"},
		{522, 504, "upstream_error", "Upstream request failed"},
		{524, 504, "upstream_error", "Upstream request failed"},
		{520, 502, "upstream_error", "Upstream request failed"},
		{418, 418, "upstream_error", "Upstream request failed"},
	}
	for _, tc := range cases {
		status, errType, msg := openAIPublicUpstreamHTTPError(tc.upstream)
		require.Equal(t, tc.wantStatus, status, "status for upstream %d", tc.upstream)
		require.Equal(t, tc.wantType, errType, "type for upstream %d", tc.upstream)
		require.Equal(t, tc.wantMsg, msg, "message for upstream %d", tc.upstream)
	}
}

func TestForwardEmbeddings_NonFailoverUpstreamErrorIsSanitized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reqBody := []byte(`{"model":"text-embedding-3-small","input":"hello"}`)
	for _, tc := range []struct {
		name       string
		upstream   int
		wantStatus int
		wantMsg    string
	}{
		{"bad_request", http.StatusBadRequest, 400, "Invalid request parameters, please check the request body and try again"},
		{"unprocessable", http.StatusUnprocessableEntity, 422, "Invalid request parameters, please check the request body and try again"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(reqBody))
			c.Request.Header.Set("Content-Type", "application/json")
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: tc.upstream,
				Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"up-rid"}},
				Body:       io.NopCloser(strings.NewReader(publicErrTestUpstreamBody)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{
				ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.example.com"},
			}

			_, err := svc.ForwardEmbeddings(context.Background(), c, account, reqBody, "")

			require.Error(t, err)
			require.Equal(t, tc.wantStatus, rec.Code)
			require.JSONEq(t, `{"error":{"type":"`+errTypeFor(tc.upstream)+`","message":"`+tc.wantMsg+`"}}`, rec.Body.String())
			requireNoUpstreamLeak(t, rec.Body.String())
			// upstream status/body still reach the ops error log
			code, _ := c.Get(OpsUpstreamStatusCodeKey)
			require.Equal(t, tc.upstream, code)
			opsMsg, _ := c.Get(OpsUpstreamErrorMessageKey)
			require.Contains(t, opsMsg.(string), "request_id")
		})
	}
}

func errTypeFor(status int) string {
	_, errType, _ := openAIPublicUpstreamHTTPError(status)
	return errType
}

func TestWriteOpenAIPublicUpstreamHTTPError_RetryAfterKeptBodyDropped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"7"}}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}

	svc.writeOpenAIPublicUpstreamHTTPError(c, &Account{ID: 1, Platform: PlatformOpenAI}, resp, []byte(publicErrTestUpstreamBody), writeOpenAIEmbeddingsError)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "7", rec.Header().Get("Retry-After"))
	require.JSONEq(t, `{"error":{"type":"rate_limit_error","message":"Upstream rate limit exceeded, please retry later"}}`, rec.Body.String())
	requireNoUpstreamLeak(t, rec.Body.String())
}

func TestForwardAlphaSearch_NonFailoverUpstreamErrorIsSanitized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"id":"s","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/alpha/search", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusUnprocessableEntity,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(publicErrTestUpstreamBody)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://compat.example/v4"},
	}

	require.NoError(t, svc.ForwardAlphaSearch(context.Background(), c, account, body))

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.JSONEq(t, `{"error":{"type":"invalid_request_error","message":"Invalid request parameters, please check the request body and try again"}}`, rec.Body.String())
	requireNoUpstreamLeak(t, rec.Body.String())
}

func TestOpenAIImagesUpstreamErrorFromHTTP_ClientMessageIsFixed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upErr := openAIImagesUpstreamErrorFromHTTP(http.StatusBadRequest, http.Header{"X-Request-Id": []string{"up-rid"}}, []byte(publicErrTestUpstreamBody))

	require.Equal(t, http.StatusBadRequest, upErr.clientStatusCode())
	require.Equal(t, "Invalid request parameters, please check the request body and try again", upErr.clientMessage())
	// upstream text stays available for logs only
	require.Contains(t, upErr.Error(), "request_id")

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	require.True(t, writeOpenAIImagesUpstreamErrorResponse(c, upErr))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	requireNoUpstreamLeak(t, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Invalid request parameters")

	streamBody := string(buildOpenAIImagesStreamErrorBodyFromUpstream(upErr))
	requireNoUpstreamLeak(t, streamBody)

	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusTooManyRequests, "Upstream rate limit exceeded, please retry later"},
		{http.StatusInternalServerError, "Upstream service temporarily unavailable"},
		{http.StatusBadGateway, "Upstream service temporarily unavailable"},
		{http.StatusForbidden, "Upstream access forbidden, please contact administrator"},
		{http.StatusRequestEntityTooLarge, "Request body is too large, please reduce the input size and try again"},
	} {
		e := openAIImagesUpstreamErrorFromHTTP(tc.status, nil, []byte(publicErrTestUpstreamBody))
		require.Equal(t, tc.status, e.clientStatusCode(), "status must stay 1:1")
		require.Equal(t, tc.want, e.clientMessage())
	}
}

func TestOpenAIImagesAsyncTaskFailedError_ClientMessageIsFixed(t *testing.T) {
	upErr := openAIImagesAsyncTaskFailedError("failed", nil, []byte(publicErrTestUpstreamBody))

	require.Equal(t, http.StatusBadGateway, upErr.clientStatusCode())
	require.Equal(t, "Upstream image async task failed", upErr.clientMessage())
	require.Contains(t, upErr.Error(), "request_id")
	requireNoUpstreamLeak(t, string(openAIImagesUpstreamErrorResponseBody(upErr)))
}

func TestFetchCodexModelsManifest_UpstreamErrorBodyNotInClientMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(publicErrTestUpstreamBody))
	}))
	defer server.Close()
	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	_, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", "")

	require.Error(t, err)
	// status stays the gateway's own 502, reason unchanged, message fixed
	require.Equal(t, http.StatusBadGateway, infraerrors.Code(err))
	require.Equal(t, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", infraerrors.Reason(err))
	require.Equal(t, "Upstream service temporarily unavailable", infraerrors.Message(err))
	requireNoUpstreamLeak(t, infraerrors.Message(err))
	require.NotContains(t, infraerrors.Message(err), "403")
	require.False(t, IsRetryableCodexModelsManifestError(err), "403 must stay non-retryable")

	// the original status and body remain available for logs / ops
	status, detail, ok := CodexModelsManifestUpstreamInfo(err)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, status)
	require.Contains(t, detail, "403")
	require.Contains(t, detail, "request_id")
}

func TestFetchCodexModelsManifest_TransportErrorDoesNotLeakUpstreamURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	requestURL := server.URL
	server.Close()
	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = requestURL
	defer func() { chatgptCodexModelsURL = original }()

	s := &OpenAIGatewayService{}
	_, err := s.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", "")

	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, infraerrors.Code(err))
	require.Equal(t, "Upstream service temporarily unavailable", infraerrors.Message(err))
	require.NotContains(t, infraerrors.Message(err), strings.TrimPrefix(requestURL, "http://"))
	require.True(t, IsRetryableCodexModelsManifestError(err), "transport failure stays retryable")
}
