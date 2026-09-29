package admin

import (
	"encoding/json"
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
