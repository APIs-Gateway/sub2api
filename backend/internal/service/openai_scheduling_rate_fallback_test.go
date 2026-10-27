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

func TestOpenAISchedulingRateFallback(t *testing.T) {
	now := time.Now()
	for _, tt := range []struct {
		name   string
		status string
		age    time.Duration
		probe  float64
		want   float64
	}{
		{"fresh snapshot wins", UpstreamBillingProbeStatusOK, time.Minute, 0.6, 0.6},
		{"fresh snapshot survives failed refresh", UpstreamBillingProbeStatusFailed, time.Minute, 0.6, 0.6},
		{"expired snapshot falls back", UpstreamBillingProbeStatusOK, 3 * time.Hour, 0.6, 0.3},
		{"future snapshot falls back", UpstreamBillingProbeStatusOK, -time.Hour, 0.6, 0.3},
		{"invalid snapshot falls back", UpstreamBillingProbeStatusOK, time.Minute, -1, 0.3},
		{"zero snapshot remains valid", UpstreamBillingProbeStatusOK, time.Minute, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			account := upstreamCostTestAccount(1, tt.status, tt.probe, now.Add(-tt.age), time.Hour)
			account.RateMultiplier = float64Ptr(0.3)
			rate, ok := openAISchedulingRate(account, now, float64Ptr(0.9))
			require.True(t, ok)
			require.Equal(t, tt.want, rate)
		})
	}

	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(accountType+" without probe or override", func(t *testing.T) {
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: accountType}
			for _, tt := range []struct {
				configured *float64
				want       float64
				known      bool
			}{
				{float64Ptr(0.3), 0.3, true},
				{float64Ptr(0), 0, true},
				{nil, 1, true},
				{float64Ptr(-1), 1, true},
				{float64Ptr(math.NaN()), 0, false},
				{float64Ptr(math.Inf(1)), 0, false},
			} {
				account.RateMultiplier = tt.configured
				rate, ok := openAISchedulingRate(account, now, nil)
				require.Equal(t, tt.known, ok)
				if ok {
					require.Equal(t, tt.want, rate)
				}
			}
		})
	}

	oauth := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, RateMultiplier: float64Ptr(0.3)}
	for _, override := range []float64{0, 0.7} {
		override := override
		rate, ok := openAISchedulingRate(oauth, now, &override)
		require.True(t, ok)
		require.Equal(t, override, rate)
	}
	for _, override := range []float64{-1, math.NaN(), math.Inf(1)} {
		override := override
		rate, ok := openAISchedulingRate(oauth, now, &override)
		require.True(t, ok)
		require.Equal(t, 0.3, rate)
	}
	_, ok := openAISchedulingRate(nil, now, nil)
	require.False(t, ok)
	// Non-OpenAI accounts never get a scheduling rate, even with an account multiplier.
	_, ok = openAISchedulingRate(&Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, RateMultiplier: float64Ptr(0.1)}, now, nil)
	require.False(t, ok)
}

func TestOpenAISchedulingRateFallbackSharedByBothModes(t *testing.T) {
	now := time.Now()
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(accountType, func(t *testing.T) {
			cheap := &Account{ID: 1, Platform: PlatformOpenAI, Type: accountType, RateMultiplier: float64Ptr(0.3)}
			expensive := &Account{ID: 2, Platform: PlatformOpenAI, Type: accountType, RateMultiplier: float64Ptr(0.8)}
			accounts := []*Account{cheap, expensive}
			order := newOpenAILegacyUpstreamRateOrder(accounts, now, nil)
			require.True(t, order.enabled)
			require.Negative(t, order.compare(cheap, expensive))
			factors := openAIUpstreamCostFactors(accounts, now, nil)
			require.Greater(t, factors[cheap.ID], factors[expensive.ID])
			if accountType == AccountTypeOAuth {
				order = newOpenAILegacyUpstreamRateOrder(accounts, now, float64Ptr(0.7))
				require.False(t, order.enabled)
				factors = openAIUpstreamCostFactors(accounts, now, float64Ptr(0.7))
				require.Equal(t, factors[cheap.ID], factors[expensive.ID])
			}
		})
	}
}

func TestOpenAIOAuthSchedulingRateRuntimePreservesExplicitClear(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
	for _, tt := range []struct {
		name   string
		values map[string]string
		want   *float64
	}{
		{"absent", map[string]string{}, float64Ptr(1)},
		{"cleared", map[string]string{SettingKeyOpenAIOAuthSchedulingRateMultiplier: ""}, nil},
		{"zero", map[string]string{SettingKeyOpenAIOAuthSchedulingRateMultiplier: "0"}, float64Ptr(0)},
		{"override", map[string]string{SettingKeyOpenAIOAuthSchedulingRateMultiplier: "0.7"}, float64Ptr(0.7)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			cfg := &config.Config{}
			svc := &OpenAIGatewayService{
				cfg: cfg,
				rateLimitService: &RateLimitService{
					settingService: NewSettingService(&openAIAdvancedSchedulerSettingRepoStub{values: tt.values}, cfg),
				},
			}
			for i := 0; i < 2; i++ {
				require.Equal(t, tt.want, svc.openAIOAuthSchedulingRateMultiplier(context.Background()))
			}
		})
	}
}

func TestOpenAIOAuthSchedulingRateCacheRefreshAfterUpdate(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	repo := &settingUpdateRepoStub{}
	cfg := &config.Config{}
	settingService := NewSettingService(repo, cfg)
	gateway := &OpenAIGatewayService{cfg: cfg, rateLimitService: &RateLimitService{settingService: settingService}}

	rate := 0.4
	require.NoError(t, settingService.UpdateSettings(context.Background(), &SystemSettings{OpenAIOAuthSchedulingRateMultiplier: &rate}))
	rate = 0.9 // the cached value must not alias the caller's pointer
	require.Equal(t, float64Ptr(0.4), gateway.openAIOAuthSchedulingRateMultiplier(context.Background()))

	require.NoError(t, settingService.UpdateSettings(context.Background(), &SystemSettings{}))
	require.Nil(t, gateway.openAIOAuthSchedulingRateMultiplier(context.Background()))
}
