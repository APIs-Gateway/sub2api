package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/websearch"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type webSearchHandlerRoundTrip func(*http.Request) (*http.Response, error)

func (f webSearchHandlerRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func callWebSearchTestHandler(t *testing.T) (int, response.Response, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	responseRecorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(responseRecorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/web-search-emulation/test", strings.NewReader(`{"query":"test"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	(&SettingHandler{}).TestWebSearchEmulation(ctx)
	var body response.Response
	require.NoError(t, json.Unmarshal(responseRecorder.Body.Bytes(), &body))
	return responseRecorder.Code, body, responseRecorder.Body.String()
}

func TestWebSearchTestHandlerNoManagerAndNoCandidate(t *testing.T) {
	service.SetWebSearchManager(nil)
	defer service.SetWebSearchManager(nil)
	status, body, _ := callWebSearchTestHandler(t)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "WEB_SEARCH_TEST_NOT_CONFIGURED", body.Reason)

	service.SetWebSearchManager(websearch.NewManager([]websearch.ProviderConfig{{Type: websearch.ProviderTypeBrave}}, nil))
	status, body, _ = callWebSearchTestHandler(t)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "WEB_SEARCH_TEST_NO_PROVIDER", body.Reason)
}

func TestWebSearchTestHandlerProxyFailureIsSafe422(t *testing.T) {
	const credentials = "user:password-secret"
	const apiKey = "api-key-secret"
	service.SetWebSearchManager(websearch.NewManager([]websearch.ProviderConfig{{
		Type:     websearch.ProviderTypeBrave,
		APIKey:   apiKey,
		ProxyURL: "://" + credentials + "@proxy.invalid",
	}}, nil))
	defer service.SetWebSearchManager(nil)

	status, body, raw := callWebSearchTestHandler(t)
	if strings.Contains(raw, credentials) || strings.Contains(raw, apiKey) || strings.Contains(raw, "proxy.invalid") {
		t.Fatal("admin test response contains proxy credentials or API key")
	}
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, http.StatusUnprocessableEntity, body.Code)
	require.Equal(t, "WEB_SEARCH_TEST_FAILED", body.Reason)
	require.Equal(t, "Web Search test failed: Brave: proxy", body.Message)
}

func TestWebSearchTestHandlerSuccessDoesNotEchoMalformedProviderType(t *testing.T) {
	const maliciousType = "unknown-secret-provider-user:pass"
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	var requests int
	client := &http.Client{Transport: webSearchHandlerRoundTrip(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"web":{"results":[{"url":"https://example.com","title":"result","description":"found"}]}}`)),
			Header: make(http.Header),
		}, nil
	})}
	service.SetWebSearchManager(websearch.NewManagerWithHTTPClient([]websearch.ProviderConfig{{
		Type: maliciousType, APIKey: "api-key-secret",
	}}, nil, client))
	defer service.SetWebSearchManager(nil)

	status, body, raw := callWebSearchTestHandler(t)
	if strings.Contains(raw, maliciousType) || strings.Contains(raw, "api-key-secret") || strings.Contains(logs.String(), maliciousType) || strings.Contains(logs.String(), "api-key-secret") {
		t.Fatal("successful admin test response or log contains malformed provider type or key")
	}
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, 1, requests)
	data, ok := body.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, websearch.ProviderTypeBrave, data["provider"])
}
