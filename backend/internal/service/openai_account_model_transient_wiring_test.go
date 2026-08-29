//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestShouldCooldownOpenAITransientUpstreamError(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       []byte
		want       bool
	}{
		{"500 is transient", http.StatusInternalServerError, nil, true},
		{"502 is transient", http.StatusBadGateway, nil, true},
		{"503 is transient", http.StatusServiceUnavailable, nil, true},
		{"504 is transient", http.StatusGatewayTimeout, nil, true},
		{"cloudflare 524 is transient", 524, nil, true},
		{"400 processing error is transient", http.StatusBadRequest, []byte(`{"error":{"message":"An error occurred while processing your request. You can retry your request, and if the issue persists, please contact us through our help center at help.openai.com and include the request ID"}}`), true},
		{"400 without a known marker is not transient", http.StatusBadRequest, []byte(`{"error":{"message":"invalid request"}}`), false},
		{"404 is not transient", http.StatusNotFound, nil, false},
		{"401 is not transient", http.StatusUnauthorized, nil, false},
		{"429 is not transient", http.StatusTooManyRequests, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldCooldownOpenAITransientUpstreamError(tc.statusCode, tc.body))
		})
	}
}

func TestHandleOpenAIAccountUpstreamError_TransientCooldownIsModelScopedNotAccountWide(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := &Account{ID: 91101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"message":"service unavailable"}}`)

	// First transient failure on a model: recorded, but not yet cooled down.
	shouldDisable := svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{}, body, "gpt-5.5")
	require.False(t, shouldDisable)
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.5"))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))

	// Second consecutive transient failure on the same model: model-scoped
	// cooldown kicks in, but the account itself must stay schedulable for
	// every other model — this is the whole point of the upstream fix.
	shouldDisable = svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{}, body, "gpt-5.5")
	require.False(t, shouldDisable)
	require.True(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.5"))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.5"))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
}

func TestHandleOpenAIAccountUpstreamError_TransientCooldownSkipsOAuthAccounts(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := &Account{ID: 91102, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"message":"service unavailable"}}`)

	for i := 0; i < 3; i++ {
		_ = svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{}, body, "gpt-5.5")
	}

	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.5"))
}

func TestOpenAIAccountScheduler_SkipsAccountBlockedForRequestedModel(t *testing.T) {
	now := time.Now()
	account := &Account{ID: 91103, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	svc := &OpenAIGatewayService{openaiModelTransient: newOpenAIAccountModelTransientState(128)}
	svc.openaiModelTransient.recordFailure(account.ID, "gpt-5.5", now)
	svc.openaiModelTransient.recordFailure(account.ID, "gpt-5.5", now.Add(time.Millisecond))
	scheduler := &defaultOpenAIAccountScheduler{service: svc}

	require.False(t, scheduler.isAccountRequestCompatible(context.Background(), account, OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.5"}))
	require.True(t, scheduler.isAccountRequestCompatible(context.Background(), account, OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.6-sol"}))
}

func TestCanonicalOpenAIAccountSchedulingModel_ResolvesMappedModel(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-5.5-alias": "gpt-5.5",
			},
		},
	}

	require.Equal(t, "gpt-5.5", canonicalOpenAIAccountSchedulingModel(account, "gpt-5.5-alias"))
	require.Equal(t, "gpt-5.6", canonicalOpenAIAccountSchedulingModel(account, "gpt-5.6"))
	require.Equal(t, "", canonicalOpenAIAccountSchedulingModel(account, ""))
	require.Equal(t, "gpt-5.5", canonicalOpenAIAccountSchedulingModel(nil, "gpt-5.5"))
}

func TestIsOpenAIAccountRequestRuntimeBlocked_NilSafe(t *testing.T) {
	var svc *OpenAIGatewayService
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(nil, "gpt-5.5"))

	svc = &OpenAIGatewayService{}
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(nil, "gpt-5.5"))
}
