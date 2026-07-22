//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountTestServiceGrokAPIKeyUsesXAIResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := grokProtocolAPIKeyAccount(7107)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"test-grok-api-key\"}}\n\n",
		)),
	}}
	svc := &AccountTestService{httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/7107/test", nil)

	err := svc.testGrokAccountConnection(c, account, "grok-4.3")

	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer xai-protocol-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "grok-4.3", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
}

func TestAccountTestServiceGrokAPIKeyMissingCredentialReturnsError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := grokProtocolAPIKeyAccount(7108)
	delete(account.Credentials, "api_key")
	svc := &AccountTestService{httpUpstream: &httpUpstreamRecorder{}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/7108/test", nil)

	err := svc.testGrokAccountConnection(c, account, "grok-4.3")

	require.Error(t, err)
	require.Contains(t, recorder.Body.String(), "Grok API key is missing")
}
