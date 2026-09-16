package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// contentModerationHandlerSettingRepoStub is a minimal in-memory service.SettingRepository
// double, embedding the (nil) interface so only the methods actually exercised by
// GetConfig/UpdateConfig (GetValue/Set) need real implementations.
type contentModerationHandlerSettingRepoStub struct {
	service.SettingRepository
	values map[string]string
}

func (r *contentModerationHandlerSettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}

func (r *contentModerationHandlerSettingRepoStub) Set(ctx context.Context, key, value string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

// contentModerationHandlerProxyRepoStub is a minimal service.ProxyRepository double: only
// GetByID is implemented, matching the one method content moderation's proxy resolution and
// validation paths actually call.
type contentModerationHandlerProxyRepoStub struct {
	service.ProxyRepository
	proxies map[int64]*service.Proxy
}

func (r *contentModerationHandlerProxyRepoStub) GetByID(ctx context.Context, id int64) (*service.Proxy, error) {
	if p, ok := r.proxies[id]; ok {
		return p, nil
	}
	return nil, errors.New("proxy not found")
}

func setupContentModerationHandlerRouter() (*gin.Engine, *contentModerationHandlerSettingRepoStub) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	settingRepo := &contentModerationHandlerSettingRepoStub{values: map[string]string{}}
	proxyRepo := &contentModerationHandlerProxyRepoStub{proxies: map[int64]*service.Proxy{
		11: {ID: 11, Protocol: "http", Host: "127.0.0.1", Port: 3128},
	}}
	svc := service.NewContentModerationService(settingRepo, nil, nil, nil, nil, nil, nil, proxyRepo)
	handler := NewContentModerationHandler(svc)

	router.GET("/config", handler.GetConfig)
	router.PUT("/config", handler.UpdateConfig)
	router.POST("/test-keys", handler.TestAPIKeys)

	return router, settingRepo
}

func doJSONRequest(t *testing.T, router *gin.Engine, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var decoded map[string]any
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	}
	return rec, decoded
}

// TestContentModerationHandler_UpdateConfig_ProxyIDRoundTrip covers the exact gap the issue
// describes: the admin API must expose proxy_id, otherwise the field silently never reaches
// service.UpdateContentModerationConfigInput.
func TestContentModerationHandler_UpdateConfig_ProxyIDRoundTrip(t *testing.T) {
	router, _ := setupContentModerationHandlerRouter()

	rec, body := doJSONRequest(t, router, http.MethodPut, "/config", map[string]any{"proxy_id": 11})
	require.Equal(t, http.StatusOK, rec.Code)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "response must contain a data object")
	require.EqualValues(t, 11, data["proxy_id"])

	rec, body = doJSONRequest(t, router, http.MethodGet, "/config", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	data, ok = body["data"].(map[string]any)
	require.True(t, ok, "response must contain a data object")
	require.EqualValues(t, 11, data["proxy_id"], "GetConfig must reflect the previously saved proxy_id")

	rec, body = doJSONRequest(t, router, http.MethodPut, "/config", map[string]any{"proxy_id": 0})
	require.Equal(t, http.StatusOK, rec.Code)
	data, ok = body["data"].(map[string]any)
	require.True(t, ok, "response must contain a data object")
	require.Nil(t, data["proxy_id"], "proxy_id <= 0 must clear the saved proxy")

	rec, _ = doJSONRequest(t, router, http.MethodPut, "/config", map[string]any{"proxy_id": 999})
	require.Equal(t, http.StatusBadRequest, rec.Code, "a nonexistent proxy id must be rejected")
}

// TestContentModerationHandler_TestAPIKeys_ProxyIDPassthrough verifies the test-keys endpoint
// forwards proxy_id into service.TestContentModerationAPIKeysInput instead of silently
// dropping it (proxy_id: 0 forces a direct connection, which must reach the local server).
func TestContentModerationHandler_TestAPIKeys_ProxyIDPassthrough(t *testing.T) {
	router, _ := setupContentModerationHandlerRouter()

	upstreamHit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		_, _ = w.Write([]byte(`{"results":[{"flagged":false,"categories":{},"category_scores":{"sexual":0.01}}]}`))
	}))
	defer server.Close()

	rec, body := doJSONRequest(t, router, http.MethodPost, "/test-keys", map[string]any{
		"api_keys":   []string{"sk-test"},
		"base_url":   server.URL,
		"prompt":     "hello",
		"proxy_id":   0,
		"timeout_ms": 2000,
	})
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, upstreamHit, "proxy_id=0 must force a direct connection that reaches the test server")
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	items, ok := data["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
}
