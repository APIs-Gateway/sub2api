//go:build integration

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func integrationModelTempAccount(id int64, platform string) *Account {
	return &Account{
		ID:          id,
		Platform:    platform,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusServiceUnavailable),
					"keywords":         []any{"overloaded"},
					"duration_minutes": float64(10),
				},
			},
		},
	}
}

func TestIntegration_ModelScopedTempPolicyPersistsKnownModel(t *testing.T) {
	repo := &stubAntigravityAccountRepo{}
	svc := &RateLimitService{accountRepo: repo}
	account := integrationModelTempAccount(601, PlatformOpenAI)

	handled := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusServiceUnavailable,
		http.Header{},
		[]byte(`{"error":{"message":"endpoint overloaded"}}`),
		"gpt-5.4",
	)

	require.True(t, handled)
	require.Empty(t, repo.rateCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].modelKey)
}

func TestIntegration_AntigravityPolicyUsesResolvedMappedModel(t *testing.T) {
	repo := &stubAntigravityAccountRepo{}
	account := integrationModelTempAccount(602, PlatformAntigravity)
	account.Credentials["model_mapping"] = map[string]any{
		"claude-sonnet-4-5": "claude-sonnet-4-6",
	}
	svc := &AntigravityGatewayService{
		rateLimitService: &RateLimitService{accountRepo: repo},
	}

	handled, status, err := svc.applyErrorPolicy(antigravityRetryLoopParams{
		ctx:             context.Background(),
		prefix:          "[integration]",
		account:         account,
		requestedModel:  "claude-sonnet-4-5",
		isStickySession: true,
	}, http.StatusServiceUnavailable, http.Header{}, []byte("overloaded"))

	require.True(t, handled)
	require.Equal(t, http.StatusServiceUnavailable, status)
	var switchErr *AntigravityAccountSwitchError
	require.ErrorAs(t, err, &switchErr)
	require.Equal(t, account.ID, switchErr.OriginalAccountID)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "claude-sonnet-4-6", repo.modelRateLimitCalls[0].modelKey)
}

func TestIntegration_OpenAIModelTempFastPathStaysModelScoped(t *testing.T) {
	repo := &stubAntigravityAccountRepo{}
	svc := &OpenAIGatewayService{
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := integrationModelTempAccount(603, PlatformOpenAI)
	account.Credentials["temp_unschedulable_rules"] = []any{
		map[string]any{
			"error_code":       float64(http.StatusNotFound),
			"keywords":         []any{"endpoint not found"},
			"duration_minutes": float64(10),
		},
	}

	shouldDisable := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"message":"endpoint not found"}}`),
		"gpt-5.4",
	)

	require.True(t, shouldDisable)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Empty(t, repo.rateCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].modelKey)
}
