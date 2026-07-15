//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newAgentIdentityGatewayContext(t *testing.T, path string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5"}`))
	return c
}

func TestOpenAIGatewayGetAccessTokenAgentIdentityBuildsAssertion(t *testing.T) {
	_, privateKey := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, privateKey)
	account.Credentials["task_id"] = "task-http"

	svc := &OpenAIGatewayService{}
	token, kind, err := svc.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "agent_identity", kind)
	require.True(t, strings.HasPrefix(token, "AgentAssertion "))
	require.NotContains(t, token, "Bearer ")
}

func TestOpenAIBuildUpstreamRequestAgentIdentityUsesAssertion(t *testing.T) {
	c := newAgentIdentityGatewayContext(t, "/v1/responses")
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":          OpenAIAuthModeAgentIdentity,
			"chatgpt_account_id": "chatgpt-agent",
		},
	}

	svc := &OpenAIGatewayService{}
	req, err := svc.buildUpstreamRequest(c.Request.Context(), c, account, []byte(`{"model":"gpt-5"}`), "AgentAssertion signed", false, "", false)
	require.NoError(t, err)
	require.Equal(t, "AgentAssertion signed", req.Header.Get("Authorization"))
	require.NotContains(t, req.Header.Get("Authorization"), "Bearer ")
	require.Equal(t, "chatgpt-agent", req.Header.Get("chatgpt-account-id"))
}

func TestOpenAIBuildUpstreamRequestOpenAIPassthroughAgentIdentityUsesAssertion(t *testing.T) {
	c := newAgentIdentityGatewayContext(t, "/v1/responses/compact")
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":          OpenAIAuthModeAgentIdentity,
			"chatgpt_account_id": "chatgpt-agent",
		},
	}

	svc := &OpenAIGatewayService{}
	req, err := svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, []byte(`{"model":"gpt-5"}`), "AgentAssertion passthrough")
	require.NoError(t, err)
	require.Equal(t, "AgentAssertion passthrough", req.Header.Get("Authorization"))
	require.NotContains(t, req.Header.Get("Authorization"), "Bearer ")
	require.Equal(t, "chatgpt-agent", req.Header.Get("chatgpt-account-id"))
}

func TestOpenAIBuildOpenAIWSHeadersAgentIdentityUsesAssertion(t *testing.T) {
	c := newAgentIdentityGatewayContext(t, "/v1/responses")
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":          OpenAIAuthModeAgentIdentity,
			"chatgpt_account_id": "chatgpt-agent",
		},
	}

	svc := &OpenAIGatewayService{}
	headers, _ := svc.buildOpenAIWSHeaders(c, account, "AgentAssertion websocket", OpenAIWSProtocolDecision{}, false, "", "", "")
	require.Equal(t, "AgentAssertion websocket", headers.Get("Authorization"))
	require.NotContains(t, headers.Get("Authorization"), "Bearer ")
	require.Equal(t, "chatgpt-agent", headers.Get("chatgpt-account-id"))
}

func TestOpenAIAuthorizationHeaderKeepsOAuthBearerBehavior(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.Equal(t, "Bearer oauth-token", buildOpenAIAuthorizationHeader(account, "oauth-token"))
	require.Equal(t, "Bearer api-key", buildOpenAIAuthorizationHeader(&Account{Type: AccountTypeAPIKey}, "api-key"))
}
