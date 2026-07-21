package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAISelectionFilterStatsSummarySortsReasons(t *testing.T) {
	stats := openAISelectionFilterStats{pool: 3}
	stats.exclude("quota_auto_pause_7d")
	stats.exclude("excluded")
	stats.exclude("quota_auto_pause_7d")

	require.Equal(t, "pool=3, filtered: excluded=1 quota_auto_pause_7d=2, selection_order_empty", stats.summary("selection_order_empty"))
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_NoAvailableErrorReportsEmptyPool(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()

	groupID := int64(102401)
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{},
		cache:              &schedulerTestGatewayCache{},
		cfg:                &config.Config{},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithScheduler(
		context.Background(), &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false,
	)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	require.EqualError(t, err, "no available OpenAI accounts supporting model: gpt-5.1 (pool=0)")
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_NoAvailableErrorReportsQuotaAutoPause(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()

	ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold7d: 0.9})
	groupID := int64(102402)
	accounts := []Account{{
		ID:          38201,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"codex_7d_used_percent":  95.0,
			"codex_7d_reset_at":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"codex_usage_updated_at": time.Now().Add(-time.Minute).Format(time.RFC3339),
		},
	}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                &config.Config{},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "", "gpt-5.4-mini", nil, OpenAIUpstreamTransportAny, false,
	)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	require.EqualError(t, err, "no available OpenAI accounts supporting model: gpt-5.4-mini (pool=1, filtered: quota_auto_pause_7d=1)")
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_NoAvailableErrorAggregatesReasons(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()

	ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold7d: 0.9})
	groupID := int64(102403)
	quotaPaused := Account{
		ID:          38211,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"codex_7d_used_percent":  95.0,
			"codex_7d_reset_at":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"codex_usage_updated_at": time.Now().Add(-time.Minute).Format(time.RFC3339),
		},
	}
	mappingMiss := Account{
		ID:          38212,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-4o": "gpt-4o"},
		},
	}
	excluded := Account{
		ID:          38213,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{quotaPaused, mappingMiss, excluded}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                &config.Config{},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "", "gpt-5.4-mini", map[int64]struct{}{38213: {}}, OpenAIUpstreamTransportAny, false,
	)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	require.EqualError(t, err, "no available OpenAI accounts supporting model: gpt-5.4-mini (pool=3, filtered: excluded=1 model_not_supported=1 quota_auto_pause_7d=1)")
}
