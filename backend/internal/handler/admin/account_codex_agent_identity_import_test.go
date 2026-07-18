package admin

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestNormalizeCodexImportEntryAcceptsAgentIdentityAuthJSON(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	privateKeyBase64 := base64.StdEncoding.EncodeToString(der)

	item, err := normalizeCodexImportEntry(codexImportEntry{
		Index: 1,
		Value: map[string]any{
			"auth_mode": "agentIdentity",
			"agent_identity": map[string]any{
				"agent_runtime_id":           "runtime-import",
				"agent_private_key":          privateKeyBase64,
				"task_id":                    "task-import",
				"account_id":                 "account-import",
				"chatgpt_user_id":            "user-import",
				"email":                      "agent@example.invalid",
				"plan_type":                  "pro",
				"chatgpt_account_is_fedramp": false,
			},
		},
	})
	require.NoError(t, err)
	require.True(t, item.IsAgentIdentity)
	require.Equal(t, service.OpenAIAuthModeAgentIdentity, item.Credentials["auth_mode"])
	require.Equal(t, "runtime-import", item.Credentials["agent_runtime_id"])
	require.Equal(t, privateKeyBase64, item.Credentials["agent_private_key"])
	require.Equal(t, "task-import", item.Credentials["task_id"])
	require.NotContains(t, item.Credentials, "access_token")
	require.NotContains(t, item.Credentials, "refresh_token")
	require.Contains(t, item.IdentityKeys, "agent:runtime-import")
}

func TestNormalizeCodexImportEntryRejectsInvalidAgentIdentity(t *testing.T) {
	_, err := normalizeCodexImportEntry(codexImportEntry{
		Index: 1,
		Value: map[string]any{
			"auth_mode": "agentIdentity",
			"agent_identity": map[string]any{
				"agent_runtime_id":  "runtime-import",
				"agent_private_key": "not-a-key",
				"account_id":        "account-import",
				"chatgpt_user_id":   "user-import",
			},
		},
	})
	require.EqualError(t, err, "agent identity private key 格式无效")
}

func TestResolveCodexImportExpirySkipsOAuthPolicyForAgentIdentity(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour).Unix()
	autoPause := false
	accountExpiresAt, credentialExpiresAt, resolvedAutoPause, warnings, err := resolveCodexImportExpiry(
		CodexSessionImportRequest{ExpiresAt: &past, AutoPauseOnExpired: &autoPause},
		&codexImportAccount{IsAgentIdentity: true},
	)
	require.NoError(t, err)
	require.Nil(t, accountExpiresAt)
	require.Nil(t, credentialExpiresAt)
	require.Nil(t, resolvedAutoPause)
	require.Empty(t, warnings)
}

func TestMergeCodexImportCredentialsRemovesOAuthTokensForAgentIdentity(t *testing.T) {
	merged := mergeCodexImportCredentials(
		map[string]any{
			"access_token":  "old-access",
			"refresh_token": "old-refresh",
			"client_id":     "old-client",
			"id_token":      "old-id",
			"expires_at":    "2026-08-05T13:40:42Z",
			"model_mapping": map[string]any{"from": "existing"},
		},
		map[string]any{
			"auth_mode":        service.OpenAIAuthModeAgentIdentity,
			"agent_runtime_id": "runtime-import",
		},
		&codexImportAccount{IsAgentIdentity: true},
	)
	require.NotContains(t, merged, "access_token")
	require.NotContains(t, merged, "refresh_token")
	require.NotContains(t, merged, "client_id")
	require.NotContains(t, merged, "id_token")
	require.NotContains(t, merged, "expires_at")
	require.Contains(t, merged, "model_mapping")
}

func TestImportCodexSessionsCreatesAgentIdentityWithoutOAuthExpiry(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)

	adminSvc := newStubAdminService()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	skipDefaultGroupBind := true
	result, err := handler.importCodexSessions(t.Context(), CodexSessionImportRequest{
		SkipDefaultGroupBind: &skipDefaultGroupBind,
	}, []codexImportEntry{{
		Index: 1,
		Value: map[string]any{
			"auth_mode": "agentIdentity",
			"agent_identity": map[string]any{
				"agent_runtime_id":  "runtime-import",
				"agent_private_key": base64.StdEncoding.EncodeToString(der),
				"task_id":           "task-import",
				"account_id":        "account-import",
				"chatgpt_user_id":   "user-import",
			},
		},
	}})
	require.NoError(t, err)
	require.Equal(t, 1, result.Created)
	require.Zero(t, result.Failed)
	require.Len(t, adminSvc.createdAccounts, 1)
	created := adminSvc.createdAccounts[0]
	require.Nil(t, created.ExpiresAt)
	require.Nil(t, created.AutoPauseOnExpired)
	require.Equal(t, service.OpenAIAuthModeAgentIdentity, created.Credentials["auth_mode"])
	require.NotContains(t, created.Credentials, "access_token")
	require.NotContains(t, created.Credentials, "refresh_token")
}
