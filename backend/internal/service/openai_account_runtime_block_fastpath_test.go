//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAI429FastPath_UsesThresholdBeforeCoolingOAuthAccount(t *testing.T) {
	resetUpstream429TrackerForTest()
	repo := &rateLimit429AccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKeyAccount := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for i := 0; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(account.ID)
		recordUpstream429Attempt(apiKeyAccount.ID)
	}

	shouldDisable := svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, nil)
	apiKeyShouldDisable := svc.handleOpenAIAccountUpstreamError(context.Background(), apiKeyAccount, http.StatusTooManyRequests, http.Header{}, nil)

	require.False(t, shouldDisable)
	require.False(t, apiKeyShouldDisable)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(apiKeyAccount))

	for i := 0; i < upstream429MinAttempts/2-1; i++ {
		_ = svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, nil)
	}
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIAccountUpstreamError_NilRateLimitService(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 47, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	require.False(t, svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusServiceUnavailable,
		http.Header{},
		[]byte(`{"error":{"message":"temporary"}}`),
		"gpt-5.4",
	))
}

func TestOpenAI429FastPath_OpenCodeGoUsageLimitUsesMessageResetDuration(t *testing.T) {
	resetUpstream429TrackerForTest()
	repo := &rateLimit429AccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	rateLimitService.SetAccountRuntimeBlocker(svc)
	account := &Account{ID: 48, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"type":"error","error":{"type":"GoUsageLimitError","message":"5-hour usage limit reached. Resets in 4hr 59min. To continue using this model now, enable usage from your available balance: https://opencode.ai/workspace/wrk_test/go"},"metadata":{"workspace":"wrk_test","limitName":"5 hour"}}`)

	before := time.Now()
	shouldDisable := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		http.Header{},
		body,
	)
	after := time.Now()

	require.False(t, shouldDisable)
	require.Equal(t, 1, repo.rateLimitCalls)
	require.Equal(t, account.ID, repo.lastRateLimitID)
	expectedResetAfter := 4*time.Hour + 59*time.Minute
	require.False(t, repo.lastRateLimitReset.Before(before.Add(expectedResetAfter-time.Second)))
	require.False(t, repo.lastRateLimitReset.After(after.Add(expectedResetAfter)))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeBlock_AppliesToOpenAIAPIKeyWhenRateLimitServiceStopsScheduling(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 44, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	svc.BlockAccountScheduling(account, time.Time{}, "custom_error_code")

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeBlock_DoesNotApplyToOtherPlatforms(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 45, Platform: PlatformGemini, Type: AccountTypeOAuth}

	svc.BlockAccountScheduling(account, time.Time{}, "custom_error_code")

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeBlocker_IgnoresNonOpenAIFromRateLimitService(t *testing.T) {
	gateway := &OpenAIGatewayService{}
	repo := &rateLimitAccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rateLimitService.SetAccountRuntimeBlocker(gateway)
	account := &Account{ID: 45, Platform: PlatformGemini, Type: AccountTypeOAuth}

	shouldDisable := rateLimitService.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte("forbidden"))

	require.True(t, shouldDisable)
	require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIModelNotFound_DoesNotRuntimeBlockWholeAccount(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := openAIModelNotFoundTempAccount()

	shouldDisable := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"code":"model_not_found","message":"model not found"}}`),
		"gpt-5.4",
	)

	require.True(t, shouldDisable)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
}

func TestOpenAIModelTempUnschedulable_DoesNotRuntimeBlockWholeAccount(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := openAIModelNotFoundTempAccount()

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
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope)
}

func TestOpenAIModelTempUnschedulable_WriteFailureDoesNotRuntimeBlockWholeAccount(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{modelRateLimitErr: errors.New("write failed")}
	svc := &OpenAIGatewayService{
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := openAIModelNotFoundTempAccount()

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
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
}

func TestOpenAIOAuth429_MatchingModelTempRuleAvoidsAccountRuntimeBlock(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := openAIModelNotFoundTempAccount()
	account.Type = AccountTypeOAuth
	account.Credentials["temp_unschedulable_rules"] = []any{
		map[string]any{
			"error_code":       float64(http.StatusTooManyRequests),
			"keywords":         []any{"model quota"},
			"duration_minutes": float64(10),
		},
	}

	shouldDisable := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		http.Header{},
		[]byte(`{"error":{"message":"model quota exhausted"}}`),
		"gpt-5.4",
	)

	require.True(t, shouldDisable)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope)
}

func TestOpenAIOAuth429_NonmatchingModelTempRuleKeepsAccountRuntimeBlock(t *testing.T) {
	resetUpstream429TrackerForTest()
	repo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := openAIModelNotFoundTempAccount()
	account.Type = AccountTypeOAuth
	account.Credentials["temp_unschedulable_rules"] = []any{
		map[string]any{
			"error_code":       float64(http.StatusTooManyRequests),
			"keywords":         []any{"different marker"},
			"duration_minutes": float64(10),
		},
	}
	// This fork intentionally gates weak 429 account blocking behind the
	// existing sliding-window threshold. Seed that threshold so this test
	// isolates the non-matching-rule behavior.
	for i := 0; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(account.ID)
	}
	for i := 0; i < upstream429MinAttempts/2-1; i++ {
		require.False(t, svc.handleOpenAIAccountUpstreamError(
			context.Background(),
			account,
			http.StatusTooManyRequests,
			http.Header{},
			[]byte(`{"error":{"message":"global rate limit"}}`),
			"gpt-5.4",
		))
	}

	shouldDisable := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		http.Header{},
		[]byte(`{"error":{"message":"global rate limit"}}`),
		"gpt-5.4",
	)

	require.False(t, shouldDisable)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestOpenAITempUnschedulable_UnknownModelKeepsAccountRuntimeBlock(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := openAIModelNotFoundTempAccount()

	shouldDisable := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"message":"endpoint not found"}}`),
	)

	require.True(t, shouldDisable)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Equal(t, 1, repo.tempCalls)
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestOpenAIFailoverSideEffects_PoolTempRuleStopsSameAccountRetry(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := &Account{
		ID:       48,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":                  true,
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusServiceUnavailable),
					"keywords":         []any{"unavailable"},
					"duration_minutes": float64(30),
				},
			},
		},
	}
	resp := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}}
	body := []byte("Service temporarily unavailable")

	shouldDisable := svc.handleFailoverSideEffects(context.Background(), resp, account, body, "gpt-5.4")

	require.True(t, shouldDisable)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope)
	require.False(t, openAIRetryableOnSameAccount(
		resp.StatusCode,
		"Service temporarily unavailable",
		body,
		!shouldDisable && account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
	))
}

func TestOpenAIRuntimeBlock_DoesNotShortenExistingBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 46, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	longUntil := time.Now().Add(10 * time.Minute)

	svc.BlockAccountScheduling(account, longUntil, "oauth_401")
	svc.BlockAccountScheduling(account, time.Time{}, "upstream_disable")

	value, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	actualUntil, ok := value.(time.Time)
	require.True(t, ok)
	require.WithinDuration(t, longUntil, actualUntil, time.Second)
}

func TestOpenAIRuntimeBlock_ClearAccountSchedulingBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 47, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	svc.BlockAccountScheduling(account, time.Now().Add(time.Minute), "429")
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))

	svc.ClearAccountSchedulingBlock(account.ID)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestShouldStopOpenAIOAuth429Failover_OnlyDuringStorm(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKeyAccount := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 1))

	for i := 0; i < openAIOAuth429StormThreshold; i++ {
		svc.recordOpenAIOAuth429()
	}

	require.True(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 1))
	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(apiKeyAccount, http.StatusTooManyRequests, 1))
	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusInternalServerError, 1))
	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 0))
}

func TestOpenAIPromoteTempUnscheduleFailover_AlreadyFailoverIsNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	account := openAIModelNotFoundTempAccount()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	shouldFailover, tempUnscheduled := svc.openAIPromoteTempUnscheduleFailover(
		context.Background(), c, account, http.StatusNotFound, []byte(`{"error":{"message":"not found"}}`), true, "gpt-5.4",
	)

	require.True(t, shouldFailover)
	require.False(t, tempUnscheduled)
}

func TestOpenAIPromoteTempUnscheduleFailover_NilRateLimitService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	account := openAIModelNotFoundTempAccount()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	shouldFailover, tempUnscheduled := svc.openAIPromoteTempUnscheduleFailover(
		context.Background(), c, account, http.StatusNotFound, []byte(`{"error":{"message":"not found"}}`), false, "gpt-5.4",
	)

	require.False(t, shouldFailover)
	require.False(t, tempUnscheduled)
}

func TestOpenAIPromoteTempUnscheduleFailover_GrokExcluded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &errorPolicyRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := openAIModelNotFoundTempAccount()
	account.Platform = PlatformGrok
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	shouldFailover, tempUnscheduled := svc.openAIPromoteTempUnscheduleFailover(
		context.Background(), c, account, http.StatusNotFound, []byte(`{"error":{"message":"not found"}}`), false, "gpt-5.4",
	)

	require.False(t, shouldFailover)
	require.False(t, tempUnscheduled)
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestOpenAIPromoteTempUnscheduleFailover_PromotesOnMatchingRule(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &errorPolicyRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := openAIModelNotFoundTempAccount()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	shouldFailover, tempUnscheduled := svc.openAIPromoteTempUnscheduleFailover(
		context.Background(), c, account, http.StatusNotFound, []byte(`{"error":{"message":"not found"}}`), false, "gpt-5.4",
	)

	require.True(t, shouldFailover)
	require.True(t, tempUnscheduled)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope)
}

func TestOpenAIPromoteTempUnscheduleFailover_ResponseCommittedSkipsPromotion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &errorPolicyRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := openAIModelNotFoundTempAccount()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(ResponseCommittedKey, true)

	shouldFailover, tempUnscheduled := svc.openAIPromoteTempUnscheduleFailover(
		context.Background(), c, account, http.StatusNotFound, []byte(`{"error":{"message":"not found"}}`), false, "gpt-5.4",
	)

	require.False(t, shouldFailover)
	require.False(t, tempUnscheduled)
	require.Empty(t, repo.modelRateLimitCalls)
}

func newOpenAI429FallbackTestService(t *testing.T, fallbackSettingsJSON string) (*OpenAIGatewayService, *rateLimit429AccountRepoStub) {
	t.Helper()
	resetUpstream429TrackerForTest()
	t.Cleanup(resetUpstream429TrackerForTest)
	repo := &rateLimit429AccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	if fallbackSettingsJSON != "" {
		settingRepo := newMockSettingRepo()
		settingRepo.data[SettingKeyRateLimit429CooldownSettings] = fallbackSettingsJSON
		rateLimitService.SetSettingService(NewSettingService(settingRepo, &config.Config{}))
	}
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	rateLimitService.SetAccountRuntimeBlocker(svc)
	return svc, repo
}

// openAI429GateOpeningCalls primes the fork's 429 sliding-window gate so the
// next upstream429MinAttempts/2 hits cross upstream429RatioThreshold, which
// is what lets the configurable fallback (and the OAuth runtime block) run.
func openAI429GateOpeningCalls(accountID int64) int {
	for i := 0; i < upstream429MinAttempts; i++ {
		recordUpstream429Attempt(accountID)
	}
	return upstream429MinAttempts / 2
}

func openAIRuntimeBlockUntilForTest(t *testing.T, svc *OpenAIGatewayService, account *Account) time.Time {
	t.Helper()
	raw, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok, "expected an OpenAI runtime block")
	until, ok := raw.(time.Time)
	require.True(t, ok)
	return until
}

func openAINonExhaustedCodexHeaders() http.Header {
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "37")
	headers.Set("x-codex-primary-reset-after-seconds", "604800")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "20")
	headers.Set("x-codex-secondary-reset-after-seconds", "3600")
	headers.Set("x-codex-secondary-window-minutes", "300")
	return headers
}

func openAIExhaustedSevenDayCodexHeaders() http.Header {
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "604800")
	headers.Set("x-codex-primary-window-minutes", "10080")
	return headers
}

func TestOpenAI429FastPath_DoesNotBlockOAuthWhenFallbackDisabled(t *testing.T) {
	svc, repo := newOpenAI429FallbackTestService(t, `{"enabled":false,"cooldown_seconds":12}`)
	account := &Account{ID: 425, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	calls := openAI429GateOpeningCalls(account.ID)
	for i := 0; i < calls; i++ {
		_ = svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, []byte(`{"detail":"Rate limit exceeded"}`))
	}

	require.True(t, ShouldSwitchAccountOn429(account.ID), "the 429 gate must be open so the disabled fallback is what keeps the account schedulable")
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "disabled 429 fallback must not create an OAuth runtime cooldown")
	require.Zero(t, repo.rateLimitCalls, "disabled 429 fallback must not persist a scheduler cooldown")
}

func TestOpenAI429FastPath_UsesConfiguredFallbackWhenEnabled(t *testing.T) {
	svc, _ := newOpenAI429FallbackTestService(t, `{"enabled":true,"cooldown_seconds":12}`)
	account := &Account{ID: 427, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	calls := openAI429GateOpeningCalls(account.ID)
	for i := 0; i < calls; i++ {
		_ = svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, []byte(`{"detail":"Rate limit exceeded"}`))
	}

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	until := openAIRuntimeBlockUntilForTest(t, svc, account)
	require.WithinDuration(t, time.Now().Add(12*time.Second), until, 3*time.Second)
}

func TestOpenAI429FastPath_DoesNotBlockOAuthWhenQuotaWindowIsNotExhausted(t *testing.T) {
	svc, repo := newOpenAI429FallbackTestService(t, `{"enabled":false,"cooldown_seconds":12}`)
	account := &Account{ID: 426, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	calls := openAI429GateOpeningCalls(account.ID)
	for i := 0; i < calls; i++ {
		_ = svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, openAINonExhaustedCodexHeaders(), []byte(`{"detail":"Rate limit exceeded"}`))
	}

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "non-exhausted quota headers must use the configurable fallback")
	require.Zero(t, repo.rateLimitCalls, "disabled 429 fallback must not persist a scheduler cooldown")
}

func TestOpenAI429FastPath_NonExhaustedQuotaWindowUsesShortFallbackInsteadOfWindowReset(t *testing.T) {
	svc, repo := newOpenAI429FallbackTestService(t, `{"enabled":true,"cooldown_seconds":12}`)
	account := &Account{ID: 428, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	calls := openAI429GateOpeningCalls(account.ID)
	for i := 0; i < calls; i++ {
		_ = svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, openAINonExhaustedCodexHeaders(), []byte(`{"detail":"Rate limit exceeded"}`))
	}

	require.Equal(t, 1, repo.rateLimitCalls)
	require.WithinDuration(t, time.Now().Add(12*time.Second), repo.lastRateLimitReset, 3*time.Second, "non-exhausted windows must not cool the account until the 5h/7d reset")
	require.WithinDuration(t, time.Now().Add(12*time.Second), openAIRuntimeBlockUntilForTest(t, svc, account), 3*time.Second)
}

func TestOpenAI429FastPath_ExhaustedQuotaWindowStillUsesResetHeader(t *testing.T) {
	svc, repo := newOpenAI429FallbackTestService(t, `{"enabled":false,"cooldown_seconds":12}`)
	account := &Account{ID: 429, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	_ = svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, openAIExhaustedSevenDayCodexHeaders(), []byte(`{"detail":"Rate limit exceeded"}`))

	require.Equal(t, 1, repo.rateLimitCalls)
	require.Greater(t, time.Until(repo.lastRateLimitReset), 6*24*time.Hour)
	require.Greater(t, time.Until(openAIRuntimeBlockUntilForTest(t, svc, account)), 6*24*time.Hour)
}

func TestOpenAIWSErrorEvent_IgnoresHandshakeQuotaHeaders(t *testing.T) {
	svc, repo := newOpenAI429FallbackTestService(t, "")
	account := &Account{ID: 430, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	payload := []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`)

	svc.persistOpenAIWSRateLimitSignal(context.Background(), account, openAIExhaustedSevenDayCodexHeaders(), payload, "rate_limit_exceeded", "rate_limit_error", "quota exhausted")

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "a semantic WS 429 must not inherit the handshake quota snapshot")
	require.Zero(t, repo.rateLimitCalls)
}

func TestOpenAIWSDial429_KeepsResponseQuotaHeaders(t *testing.T) {
	svc, repo := newOpenAI429FallbackTestService(t, "")
	account := &Account{ID: 431, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	svc.persistOpenAIWSRateLimitSignal(context.Background(), account, openAIExhaustedSevenDayCodexHeaders(), nil, "rate_limit_exceeded", "rate_limit_error", "websocket dial 429")

	require.Equal(t, 1, repo.rateLimitCalls)
	require.Greater(t, time.Until(repo.lastRateLimitReset), 6*24*time.Hour)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}
