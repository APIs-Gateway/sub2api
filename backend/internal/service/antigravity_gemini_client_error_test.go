//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func forwardAntigravityGeminiErrorForTest(t *testing.T, status int, upstreamBody []byte) (*httptest.ResponseRecorder, *gin.Context, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewReader(body))
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize, LogUpstreamErrorBody: true}}
	svc := &AntigravityGatewayService{
		settingService: NewSettingService(&antigravitySettingRepoStub{}, cfg),
		tokenProvider:  &AntigravityTokenProvider{},
		httpUpstream: &httpUpstreamStub{resp: &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"upstream-request-1"}},
			Body:       io.NopCloser(bytes.NewReader(upstreamBody)),
		}},
	}
	account := &Account{
		ID: 137900, Name: "pool-account", Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{"access_token": "token", "project_id": "pool-project"},
	}
	_, err := svc.ForwardGemini(context.Background(), c, account, "gemini-2.5-flash", "generateContent", false, body, false)
	return writer, c, err
}

func TestForwardGeminiClientErrorOmitsUpstreamPoolIdentity(t *testing.T) {
	upstream := []byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"project projects/pool-project-123 caller pool-sa@internal.example.com","details":[{"metadata":{"consumer":"projects/123456789","secret":"do-not-echo"}}]}}`)
	writer, c, err := forwardAntigravityGeminiErrorForTest(t, http.StatusBadRequest, upstream)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, writer.Code)
	require.Equal(t, "application/json; charset=utf-8", writer.Header().Get("Content-Type"))
	require.Equal(t, "upstream-request-1", writer.Header().Get("X-Request-Id"))
	require.True(t, IsResponseCommitted(c))
	for _, sensitive := range []string{"pool-project-123", "pool-sa@", "123456789", "do-not-echo", "details"} {
		require.NotContains(t, writer.Body.String(), sensitive)
	}
	var client struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(writer.Body.Bytes(), &client))
	require.Equal(t, http.StatusBadRequest, client.Error.Code)
	require.Equal(t, "INVALID_ARGUMENT", client.Error.Status)
	require.Equal(t, upstreamClientMessageForStatus(http.StatusBadRequest), client.Error.Message)

	events, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	list, ok := events.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.NotEmpty(t, list)
	last := list[len(list)-1]
	require.Equal(t, "http_error", last.Kind)
	require.Equal(t, "upstream-request-1", last.UpstreamRequestID)
	require.Contains(t, last.Message, "pool-project-123")
	require.Contains(t, last.Detail, "do-not-echo")
}

func TestForwardGeminiNonJSONClientErrorIsSafeGeminiJSON(t *testing.T) {
	writer, _, err := forwardAntigravityGeminiErrorForTest(t, http.StatusNotFound,
		[]byte("resource projects/123456789 was not found for pool-sa@internal.example.com"))
	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, writer.Code)
	require.JSONEq(t, `{"error":{"code":404,"message":"Requested upstream resource was not found","status":"NOT_FOUND"}}`, writer.Body.String())
	require.NotContains(t, writer.Body.String(), "123456789")
	require.NotContains(t, writer.Body.String(), "pool-sa@")
}

func TestForwardGeminiFailoverKeepsRawContextWithoutClientWrite(t *testing.T) {
	upstream := []byte(`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"projects/123456789 pool-sa@internal.example.com"}}`)
	writer, c, err := forwardAntigravityGeminiErrorForTest(t, http.StatusForbidden, upstream)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, http.StatusForbidden, failover.StatusCode)
	require.Equal(t, upstream, failover.ResponseBody)
	require.True(t, failover.RedactClientMessage)
	require.Empty(t, writer.Body.String())
	require.False(t, IsResponseCommitted(c))
}

func TestForwardGeminiProjectConfigRetryKeepsRawContextMarkedSensitive(t *testing.T) {
	upstream := []byte(`{"error":{"code":400,"message":"invalid project resource name projects/123456789 for pool-sa@internal.example.com"}}`)
	writer, c, err := forwardAntigravityGeminiErrorForTest(t, http.StatusBadRequest, upstream)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.True(t, failover.RetryableOnSameAccount)
	require.True(t, failover.RedactClientMessage)
	require.Equal(t, upstream, failover.ResponseBody)
	require.Empty(t, writer.Body.String())
	require.False(t, IsResponseCommitted(c))
}
