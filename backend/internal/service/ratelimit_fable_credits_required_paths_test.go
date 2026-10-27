//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const fableCreditsRequiredBody = `{"type":"error","error":{"details":{"error_code":"credits_required","model":"claude-fable-5"},"message":"Usage credits are required for this model."}}`

func newFableCreditsRateLimitService(t *testing.T, repo *anthropicWindowLimitRepo, fallback *RateLimit429CooldownSettings) *RateLimitService {
	t.Helper()
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	if fallback != nil {
		settingRepo := newMockSettingRepo()
		data, err := json.Marshal(fallback)
		require.NoError(t, err)
		settingRepo.data[SettingKeyRateLimit429CooldownSettings] = string(data)
		svc.SetSettingService(NewSettingService(settingRepo, &config.Config{}))
	}
	return svc
}

func TestPersistAnthropicFableCreditsRequired_NilGuards(t *testing.T) {
	account := &Account{ID: 42, Type: AccountTypeOAuth, Platform: PlatformAnthropic}
	body := []byte(fableCreditsRequiredBody)

	var nilSvc *RateLimitService
	require.False(t, nilSvc.persistAnthropicFableCreditsRequired(context.Background(), account, http.Header{}, body, "claude-fable-5"))

	noRepo := &RateLimitService{}
	require.False(t, noRepo.persistAnthropicFableCreditsRequired(context.Background(), account, http.Header{}, body, "claude-fable-5"))

	repo := &anthropicWindowLimitRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	require.False(t, svc.persistAnthropicFableCreditsRequired(context.Background(), nil, http.Header{}, body, "claude-fable-5"))
	require.Zero(t, repo.modelRateLimitCalls)
}

func TestPersistAnthropicFableCreditsRequired_IgnoresOtherErrorCodes(t *testing.T) {
	repo := &anthropicWindowLimitRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 42, Type: AccountTypeOAuth, Platform: PlatformAnthropic}

	for _, body := range []string{
		`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
		`{"type":"error","error":{"details":{"error_code":"rate_limited","model":"claude-fable-5"}}}`,
		``,
	} {
		require.False(t, svc.persistAnthropicFableCreditsRequired(context.Background(), account, http.Header{}, []byte(body), "claude-fable-5"), body)
	}
	require.Zero(t, repo.modelRateLimitCalls)
}

func TestPersistAnthropicFableCreditsRequired_ModelResolution(t *testing.T) {
	account := &Account{ID: 42, Type: AccountTypeOAuth, Platform: PlatformAnthropic}
	noModelBody := []byte(`{"type":"error","error":{"details":{"error_code":" Credits_Required "}}}`)

	t.Run("falls back to requested Fable model", func(t *testing.T) {
		repo := &anthropicWindowLimitRepo{}
		svc := NewRateLimitService(repo, nil, nil, nil, nil)
		require.True(t, svc.persistAnthropicFableCreditsRequired(context.Background(), account, http.Header{}, noModelBody, "  claude-fable-5  "))
		require.Equal(t, 1, repo.modelRateLimitCalls)
		require.Equal(t, anthropicFableRateLimitKey, repo.lastModelRateLimitScope)
	})

	t.Run("requested non-Fable model is not model-limited", func(t *testing.T) {
		repo := &anthropicWindowLimitRepo{}
		svc := NewRateLimitService(repo, nil, nil, nil, nil)
		require.False(t, svc.persistAnthropicFableCreditsRequired(context.Background(), account, http.Header{}, noModelBody, "claude-sonnet-5"))
		require.Zero(t, repo.modelRateLimitCalls)
	})

	t.Run("no model anywhere is not model-limited", func(t *testing.T) {
		repo := &anthropicWindowLimitRepo{}
		svc := NewRateLimitService(repo, nil, nil, nil, nil)
		require.False(t, svc.persistAnthropicFableCreditsRequired(context.Background(), account, http.Header{}, noModelBody, ""))
		require.Zero(t, repo.modelRateLimitCalls)
	})

	t.Run("body model wins over requested model", func(t *testing.T) {
		repo := &anthropicWindowLimitRepo{}
		svc := NewRateLimitService(repo, nil, nil, nil, nil)
		body := []byte(`{"type":"error","error":{"details":{"error_code":"credits_required","model":"claude-opus-5"}}}`)
		require.False(t, svc.persistAnthropicFableCreditsRequired(context.Background(), account, http.Header{}, body, "claude-fable-5"))
		require.Zero(t, repo.modelRateLimitCalls)
	})
}

func TestPersistAnthropicFableCreditsRequired_NoResetHeaderUsesConfiguredFallback(t *testing.T) {
	account := &Account{ID: 42, Type: AccountTypeOAuth, Platform: PlatformAnthropic}
	body := []byte(fableCreditsRequiredBody)

	t.Run("fallback enabled", func(t *testing.T) {
		repo := &anthropicWindowLimitRepo{}
		svc := newFableCreditsRateLimitService(t, repo, &RateLimit429CooldownSettings{Enabled: true, CooldownSeconds: 12})
		startedAt := time.Now()

		require.True(t, svc.persistAnthropicFableCreditsRequired(context.Background(), account, http.Header{}, body, "claude-fable-5"))
		require.Equal(t, 1, repo.modelRateLimitCalls)
		require.WithinDuration(t, startedAt.Add(12*time.Second), repo.lastModelRateLimitReset, 2*time.Second)
	})

	t.Run("fallback disabled only skips cooling", func(t *testing.T) {
		repo := &anthropicWindowLimitRepo{}
		svc := newFableCreditsRateLimitService(t, repo, &RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 12})

		require.True(t, svc.persistAnthropicFableCreditsRequired(context.Background(), account, http.Header{}, body, "claude-fable-5"))
		require.Zero(t, repo.modelRateLimitCalls, "disabled 429 fallback must not write a model cooldown")

		// Via HandleUpstreamError the account must also stay untouched.
		shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body, "claude-fable-5")
		require.False(t, shouldDisable)
		require.Zero(t, repo.modelRateLimitCalls)
		require.Zero(t, repo.rateLimitCalls)
		require.Zero(t, repo.tempUnschedCalls)
	})

	t.Run("out of range reset header falls back", func(t *testing.T) {
		repo := &anthropicWindowLimitRepo{}
		svc := newFableCreditsRateLimitService(t, repo, &RateLimit429CooldownSettings{Enabled: true, CooldownSeconds: 30})
		headers := http.Header{}
		headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(time.Now().Add(400*24*time.Hour).Unix(), 10))
		startedAt := time.Now()

		require.True(t, svc.persistAnthropicFableCreditsRequired(context.Background(), account, headers, body, "claude-fable-5"))
		require.WithinDuration(t, startedAt.Add(30*time.Second), repo.lastModelRateLimitReset, 2*time.Second)
	})
}

func TestPersistAnthropicFableCreditsRequired_PersistenceErrorStaysModelScoped(t *testing.T) {
	resetUpstream429TrackerForTest()
	repo := &anthropicWindowLimitRepo{modelRateLimitErr: errors.New("database unavailable")}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 42, Type: AccountTypeOAuth, Platform: PlatformAnthropic}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10))

	require.True(t, svc.persistAnthropicFableCreditsRequired(context.Background(), account, headers, []byte(fableCreditsRequiredBody), "claude-fable-5"))
	require.Equal(t, 1, repo.modelRateLimitCalls)

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, []byte(fableCreditsRequiredBody), "claude-fable-5")
	require.False(t, shouldDisable)
	require.Zero(t, repo.rateLimitCalls, "a model limit write failure must not widen into an account-level rate limit")
}

func TestSanitizeBedrockFieldsForBetaTokens_FallbackFields(t *testing.T) {
	body := []byte(`{"context_management":{"edits":[]},"fallbacks":"default","fallback_credit_token":"tok","messages":[]}`)

	t.Run("strips all without betas", func(t *testing.T) {
		out := sanitizeBedrockFieldsForBetaTokens(body, nil)
		require.False(t, gjson.GetBytes(out, "context_management").Exists())
		require.False(t, gjson.GetBytes(out, "fallbacks").Exists())
		require.False(t, gjson.GetBytes(out, "fallback_credit_token").Exists())
		require.True(t, gjson.GetBytes(out, "messages").Exists())
	})

	for _, token := range []string{claude.BetaFallbackCredit, claude.BetaFallbackCreditLegacy} {
		t.Run("credit beta keeps only credit token "+token, func(t *testing.T) {
			out := sanitizeBedrockFieldsForBetaTokens(body, []string{"other-beta", token})
			require.False(t, gjson.GetBytes(out, "fallbacks").Exists())
			require.Equal(t, "tok", gjson.GetBytes(out, "fallback_credit_token").String())
		})
	}

	t.Run("server-side fallback beta keeps both fallback fields", func(t *testing.T) {
		out := sanitizeBedrockFieldsForBetaTokens(body, []string{bedrockContextManagementBetaToken, claude.BetaServerSideFallback})
		require.True(t, gjson.GetBytes(out, "context_management").Exists())
		require.Equal(t, "default", gjson.GetBytes(out, "fallbacks").String())
		require.Equal(t, "tok", gjson.GetBytes(out, "fallback_credit_token").String())
	})

	t.Run("absent fields untouched", func(t *testing.T) {
		plain := []byte(`{"messages":[]}`)
		require.Equal(t, string(plain), string(sanitizeBedrockFieldsForBetaTokens(plain, nil)))
	})
}
