//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildOpenAIOAuthUpstreamModelsRequest_AgentIdentityUsesAssertionHeaders
// covers the item10 branch of buildOpenAIOAuthUpstreamModelsRequest that
// TestBuildOpenAIUpstreamModelsRequestSupportsOAuthAccounts (upstream_models_test.go)
// does not reach: OpenAI OAuth accounts authenticated via Agent Identity must
// sign an assertion (BuildOpenAIAgentIdentityAuthenticationHeaders) instead of
// sending a plain bearer access token.
//
// newAgentIdentityTestCredentials/newAgentIdentityTestAccount are shared
// helpers from openai_agent_identity_test.go (same package, same build tag).
// Setting Credentials["task_id"] up front (mirroring
// TestBuildAgentIdentityAuthenticationHeadersUsesTask) avoids the task
// registration network round trip.
func TestBuildOpenAIOAuthUpstreamModelsRequest_AgentIdentityUsesAssertionHeaders(t *testing.T) {
	t.Parallel()

	_, encoded := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, encoded)
	account.Credentials["task_id"] = "task-header"
	account.Credentials["chatgpt_account_id"] = "chatgpt-account-agent"
	require.True(t, account.IsOpenAIAgentIdentity())

	svc := &AccountTestService{cfg: upstreamModelSyncTestConfig()}
	req, err := svc.buildOpenAIUpstreamModelsRequest(context.Background(), account)
	require.NoError(t, err)

	require.Equal(t, chatgptCodexModelsURL, req.URL.Scheme+"://"+req.URL.Host+req.URL.Path)
	require.Equal(t, openAICodexProbeVersion, req.URL.Query().Get("client_version"))
	require.True(t, strings.HasPrefix(req.Header.Get("Authorization"), "AgentAssertion "),
		"agent identity accounts must authenticate with a signed assertion, not a bearer token")
	require.Equal(t, "chatgpt-account-agent", req.Header.Get("chatgpt-account-id"))
	require.NotEmpty(t, req.Header.Get("Originator"))
	require.NotEmpty(t, req.Header.Get("User-Agent"))
	require.NotEmpty(t, req.Header.Get("Version"))
}
