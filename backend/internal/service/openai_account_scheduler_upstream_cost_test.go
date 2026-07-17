//go:build unit

package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func upstreamCostTestAccount(id int64, status string, rate float64, receivedAt time.Time, interval time.Duration) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			UpstreamBillingProbeExtraKey: map[string]any{
				"status": status,
				"data": map[string]any{
					"billing_scope":             "token",
					"resolved_rate_multiplier":  rate,
					"peak_rate_enabled":         false,
					"effective_rate_multiplier": rate,
				},
				"received_at":   receivedAt.UTC().Format(time.RFC3339Nano),
				"fresh_until":   receivedAt.Add(2 * interval).UTC().Format(time.RFC3339Nano),
				"next_probe_at": receivedAt.Add(interval).UTC().Format(time.RFC3339Nano),
			},
		},
	}
}

func TestOpenAIUpstreamCostFactorsSparseProbeIsNeutral(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	accounts := []*Account{
		upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0.03, now.Add(-time.Minute), 30*time.Minute),
	}
	for id := int64(2); id <= 5; id++ {
		accounts = append(accounts, &Account{
			ID:       id,
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Extra: map[string]any{
				UpstreamBillingProbeExtraKey: map[string]any{
					"status":        UpstreamBillingProbeStatusFailed,
					"next_probe_at": now.Add(time.Hour).UTC().Format(time.RFC3339Nano),
				},
			},
		})
	}

	factors := openAIUpstreamCostFactors(accounts, now, defaultOpenAIOAuthSchedulingRateMultiplier)
	for id := int64(1); id <= 5; id++ {
		require.Equal(t, openAIUpstreamCostNeutralFactor, factors[id])
	}
}

func TestOpenAIUpstreamCostFactorsRankRatesAndOAuthReference(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	cheap := upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0.03, now.Add(-time.Minute), 30*time.Minute)
	oauth := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	expensive := upstreamCostTestAccount(3, UpstreamBillingProbeStatusOK, 0.8, now.Add(-time.Minute), 30*time.Minute)

	factors := openAIUpstreamCostFactors([]*Account{cheap, oauth, expensive}, now, 0.05)
	require.Greater(t, factors[cheap.ID], factors[oauth.ID])
	require.Greater(t, factors[oauth.ID], factors[expensive.ID])

	order := newOpenAILegacyUpstreamRateOrder([]*Account{cheap, oauth, expensive}, now, 0.05)
	require.True(t, order.enabled)
	require.Less(t, order.compare(cheap, oauth), 0)
	require.Less(t, order.compare(oauth, expensive), 0)
}

func TestOpenAIGatewaySelectBestAccountUsesLowRateOrder(t *testing.T) {
	now := time.Now()
	cheap := upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0.03, now.Add(-time.Minute), 30*time.Minute)
	expensive := upstreamCostTestAccount(2, UpstreamBillingProbeStatusOK, 0.8, now.Add(-time.Minute), 30*time.Minute)
	for _, account := range []*Account{cheap, expensive} {
		account.Status = StatusActive
		account.Schedulable = true
		account.Concurrency = 1
		account.Priority = 0
	}

	selected, compactBlocked := (&OpenAIGatewayService{}).selectBestAccount(
		context.Background(), nil, []Account{*expensive, *cheap}, "gpt-5.1", nil, false, "", true,
	)
	require.False(t, compactBlocked)
	require.NotNil(t, selected)
	require.Equal(t, cheap.ID, selected.ID)
}

func TestOpenAIFreshUpstreamBillingRateHonorsPeakAndExpiry(t *testing.T) {
	receivedAt := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	account := upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0.4, receivedAt, 12*time.Hour)
	snapshot := account.Extra[UpstreamBillingProbeExtraKey].(map[string]any)
	snapshot["data"] = map[string]any{
		"billing_scope":             "token",
		"resolved_rate_multiplier":  0.4,
		"peak_rate_enabled":         true,
		"peak_start":                "09:00",
		"peak_end":                  "18:00",
		"peak_rate_multiplier":      2.0,
		"effective_rate_multiplier": 0.8,
		"timezone":                  "UTC",
	}

	duringPeak, ok := openAIFreshUpstreamBillingRate(account, time.Date(2026, 7, 13, 17, 59, 0, 0, time.UTC))
	require.True(t, ok)
	require.Equal(t, 0.8, duringPeak)

	afterExpiry, ok := openAIFreshUpstreamBillingRate(account, receivedAt.Add(25*time.Hour))
	require.False(t, ok)
	require.Zero(t, afterExpiry)

	invalid := upstreamCostTestAccount(2, UpstreamBillingProbeStatusOK, math.NaN(), receivedAt, time.Hour)
	_, ok = openAIFreshUpstreamBillingRate(invalid, receivedAt.Add(time.Minute))
	require.False(t, ok)
}

func TestBuildOpenAIAccountLoadPlanCostSignalIsOptIn(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	now := time.Now()
	cheap := upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0.03, now.Add(-time.Minute), 30*time.Minute)
	expensive := upstreamCostTestAccount(2, UpstreamBillingProbeStatusOK, 0.8, now.Add(-time.Minute), 30*time.Minute)
	accounts := []*Account{cheap, expensive}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.UpstreamCost = 1.5
	scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{cfg: cfg}}
	loadMap := map[int64]*AccountLoadInfo{
		cheap.ID:     {AccountID: cheap.ID},
		expensive.ID: {AccountID: expensive.ID},
	}

	withoutSignal := scheduler.buildOpenAIAccountLoadPlan(
		context.Background(), OpenAIAccountScheduleRequest{}, accounts, loadMap,
	)
	require.Equal(t, withoutSignal.candidates[0].score, withoutSignal.candidates[1].score)
	require.False(t, withoutSignal.includeOverflowFallback)

	withSignal := scheduler.buildOpenAIAccountLoadPlan(
		context.Background(), OpenAIAccountScheduleRequest{UseUpstreamTokenCost: true}, accounts, loadMap,
	)
	require.Greater(t, withSignal.candidates[0].score, withSignal.candidates[1].score)
	require.True(t, withSignal.includeOverflowFallback)
}

func TestOpenAISchedulerSettingHelpers(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.UpstreamCost = 2.25
	require.Zero(t, defaultOpenAIAdvancedSchedulerWeightUpstreamCost(nil))
	require.Equal(t, 2.25, defaultOpenAIAdvancedSchedulerWeightUpstreamCost(cfg))

	for _, raw := range []string{"", "-1", "NaN", "+Inf", "not-a-number"} {
		require.Equal(t, defaultOpenAIOAuthSchedulingRateMultiplier, parseOpenAIOAuthSchedulingRateMultiplier(raw), raw)
	}
	require.Equal(t, 0.25, parseOpenAIOAuthSchedulingRateMultiplier(" 0.25 "))

	weight, err := parseNonNegativeFiniteSchedulerWeight(" 1.75 ")
	require.NoError(t, err)
	require.Equal(t, 1.75, weight)
	for _, raw := range []string{"-1", "NaN", "+Inf", "invalid"} {
		_, err = parseNonNegativeFiniteSchedulerWeight(raw)
		require.Error(t, err, raw)
	}
	require.Equal(t, 2.25, resolveOpenAIAdvancedSchedulerWeight("", 2.25))
	require.Equal(t, 2.25, resolveOpenAIAdvancedSchedulerWeight("invalid", 2.25))
	require.Equal(t, 1.75, resolveOpenAIAdvancedSchedulerWeight("1.75", 2.25))
	require.Equal(t, "2.25", formatOpenAIAdvancedSchedulerFloat(2.25))
}

func TestOpenAIAdvancedSchedulerSettingsLoadsOverridesAndCaches(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	repo := &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
		openAIAdvancedSchedulerSettingKey:                   " TRUE ",
		SettingKeyOpenAILowUpstreamRatePriorityEnabled:      "true",
		SettingKeyOpenAIOAuthSchedulingRateMultiplier:       "0.25",
		SettingKeyOpenAIAdvancedSchedulerWeightUpstreamCost: " 1.75 ",
	}}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.UpstreamCost = 0.5
	service := &OpenAIGatewayService{
		cfg: cfg,
		rateLimitService: &RateLimitService{
			settingService: NewSettingService(repo, cfg),
		},
	}
	ctx := context.Background()

	settings := service.openAIAdvancedSchedulerSettings(ctx)
	require.True(t, settings.enabled)
	require.True(t, settings.lowUpstreamRatePriorityEnabled)
	require.Equal(t, 0.25, settings.oauthSchedulingRateMultiplier)
	require.Equal(t, "1.75", settings.upstreamCostWeightOverride)
	require.Same(t, settings, service.openAIAdvancedSchedulerSettings(ctx))
	require.True(t, service.isOpenAIAdvancedSchedulerEnabled(ctx))
	require.False(t, service.isOpenAILowUpstreamRatePriorityEnabled(ctx))
	require.Equal(t, 0.25, service.openAIOAuthSchedulingRateMultiplier(ctx))
	require.Equal(t, 1.75, service.openAIWSSchedulerWeightsForRequest(ctx).UpstreamCost)

	var nilService *OpenAIGatewayService
	require.Nil(t, nilService.openAIAdvancedSchedulerSettingRepo())
}

func TestOpenAIAdvancedSchedulerSettingsLowRateModeAndInvalidWeight(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.UpstreamCost = 0.5
	repo := &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
		openAIAdvancedSchedulerSettingKey:                   "false",
		SettingKeyOpenAILowUpstreamRatePriorityEnabled:      "true",
		SettingKeyOpenAIOAuthSchedulingRateMultiplier:       "not-a-number",
		SettingKeyOpenAIAdvancedSchedulerWeightUpstreamCost: "-1",
	}}
	service := &OpenAIGatewayService{
		cfg: cfg,
		rateLimitService: &RateLimitService{
			settingService: NewSettingService(repo, cfg),
		},
	}
	ctx := context.Background()

	require.True(t, service.isOpenAILowUpstreamRatePriorityEnabled(ctx))
	require.Equal(t, defaultOpenAIOAuthSchedulingRateMultiplier, service.openAIOAuthSchedulingRateMultiplier(ctx))
	require.Equal(t, 0.5, service.openAIWSSchedulerWeightsForRequest(ctx).UpstreamCost)
}

func TestOpenAILegacyRateOrderHandlesUnknownAndEqualRates(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	cheap := upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0.03, now.Add(-time.Minute), 30*time.Minute)
	expensive := upstreamCostTestAccount(2, UpstreamBillingProbeStatusOK, 0.8, now.Add(-time.Minute), 30*time.Minute)
	unknown := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	order := newOpenAILegacyUpstreamRateOrder([]*Account{cheap, expensive, unknown}, now, 1)
	require.True(t, order.enabled)
	require.Less(t, order.compare(cheap, unknown), 0)
	require.Greater(t, order.compare(unknown, cheap), 0)
	require.Equal(t, 0, order.compare(nil, cheap))
	require.Equal(t, 0, order.compare(cheap, cheap))

	equal := newOpenAILegacyUpstreamRateOrder([]*Account{
		upstreamCostTestAccount(4, UpstreamBillingProbeStatusOK, 0.4, now.Add(-time.Minute), 30*time.Minute),
		upstreamCostTestAccount(5, UpstreamBillingProbeStatusOK, 0.4, now.Add(-time.Minute), 30*time.Minute),
	}, now, 1)
	require.False(t, equal.enabled)

	invalidOAuth := newOpenAILegacyUpstreamRateOrder([]*Account{
		{ID: 6, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
	}, now, math.NaN())
	require.False(t, invalidOAuth.enabled)
}

func TestOpenAIUpstreamCostFactorsUsesEvenMedianAndNeutralForNonOpenAI(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	zero := upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0, now.Add(-time.Minute), 30*time.Minute)
	cheap := upstreamCostTestAccount(2, UpstreamBillingProbeStatusOK, 0.03, now.Add(-time.Minute), 30*time.Minute)
	expensive := upstreamCostTestAccount(3, UpstreamBillingProbeStatusOK, 0.8, now.Add(-time.Minute), 30*time.Minute)
	other := &Account{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	factors := openAIUpstreamCostFactors([]*Account{nil, zero, cheap, expensive, other}, now, 1)
	require.NotEqual(t, openAIUpstreamCostNeutralFactor, factors[zero.ID])
	require.NotEqual(t, openAIUpstreamCostNeutralFactor, factors[cheap.ID])
	require.NotEqual(t, openAIUpstreamCostNeutralFactor, factors[expensive.ID])
	require.Equal(t, openAIUpstreamCostNeutralFactor, factors[other.ID])

	equal := openAIUpstreamCostFactors([]*Account{
		upstreamCostTestAccount(5, UpstreamBillingProbeStatusOK, 0.4, now.Add(-time.Minute), 30*time.Minute),
		upstreamCostTestAccount(6, UpstreamBillingProbeStatusOK, 0.4, now.Add(-time.Minute), 30*time.Minute),
	}, now, 1)
	require.Equal(t, openAIUpstreamCostNeutralFactor, equal[5])
	require.Equal(t, openAIUpstreamCostNeutralFactor, equal[6])
}

func TestOpenAIFreshUpstreamBillingRateDerivesExpiryAndRejectsStaleSnapshots(t *testing.T) {
	receivedAt := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	now := receivedAt.Add(90 * time.Minute)
	account := upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0.2, receivedAt, time.Hour)
	snapshot := account.Extra[UpstreamBillingProbeExtraKey].(map[string]any)
	delete(snapshot, "fresh_until")

	rate, ok := openAIFreshUpstreamBillingRate(account, now)
	require.True(t, ok)
	require.Equal(t, 0.2, rate)
	_, ok = openAIFreshUpstreamBillingRate(account, receivedAt.Add(-time.Second))
	require.False(t, ok)

	delete(snapshot, "next_probe_at")
	_, ok = openAIFreshUpstreamBillingRate(account, now)
	require.False(t, ok)
	_, ok = openAIFreshUpstreamBillingRate(nil, now)
	require.False(t, ok)
	_, ok = openAIFreshUpstreamBillingRate(&Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, now)
	require.False(t, ok)

	invalid := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{UpstreamBillingProbeExtraKey: map[string]any{
			"status": "unknown",
		}},
	}
	_, ok = openAIFreshUpstreamBillingRate(invalid, now)
	require.False(t, ok)
}
