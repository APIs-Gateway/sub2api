//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// 这组测试证明两件事：
//  1. 两个网关在「算不出价」的分支上都会留下 reason 日志和进程内计数；
//  2. 这些观测只是旁路，计费结果与改动前逐位一致（成本为 0、照常写用量行、不扣费）。

const unpricedTestModel = "pricing-missing-test-model"

func unpricedObservedContext() (context.Context, *observer.ObservedLogs) {
	core, logs := observer.New(zap.WarnLevel)
	return logger.IntoContext(context.Background(), zap.New(core)), logs
}

func unpricedLogsByMessage(logs *observer.ObservedLogs, message string) []observer.LoggedEntry {
	var out []observer.LoggedEntry
	for _, entry := range logs.All() {
		if entry.Message == message {
			out = append(out, entry)
		}
	}
	return out
}

func TestOpenAIRecordUsage_MissingPricingNotesUnpricedBillingAndKeepsZeroCost(t *testing.T) {
	resetUnpricedBillingCountersForTest()
	ctx, logs := unpricedObservedContext()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)

	err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_unpriced_openai",
			Usage:     OpenAIUsage{InputTokens: 1200, OutputTokens: 300},
			Model:     unpricedTestModel,
			Duration:  time.Second,
		},
		APIKey:  &APIKey{ID: 1002, Quota: 100, Group: &Group{ID: 16, Platform: PlatformOpenAI, RateMultiplier: 1}},
		User:    &User{ID: 2002},
		Account: &Account{ID: 3002, Type: AccountTypeAPIKey},
	})

	// 计费结果与既有行为一致：不报错、写一行零成本用量、不扣费。
	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.NotNil(t, usageRepo.lastLog)
	require.Zero(t, usageRepo.lastLog.TotalCost)
	require.Zero(t, usageRepo.lastLog.ActualCost)
	require.Equal(t, 1200, usageRepo.lastLog.InputTokens)
	require.Equal(t, 300, usageRepo.lastLog.OutputTokens)
	require.NotNil(t, billingRepo.lastCmd)
	require.Zero(t, billingRepo.lastCmd.BalanceCost)
	require.Zero(t, billingRepo.lastCmd.SubscriptionCost)
	require.Equal(t, 0, userRepo.deductCalls)

	// 新增的观测：一次 missing_price 计数和一行结构化日志。
	require.EqualValues(t, 1, unpricedCounterValue("openai", 16, unpricedTestModel, UnpricedBillingReasonMissingPrice))
	entries := unpricedLogsByMessage(logs, "billing.unpriced_usage")
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.Equal(t, UnpricedBillingReasonMissingPrice, fields["reason"])
	require.Equal(t, "zero_cost", fields["outcome"])
	require.Equal(t, unpricedTestModel, fields["model"])
	require.EqualValues(t, 16, fields["group_id"])
}

func TestOpenAIRecordUsage_PricedModelDoesNotNoteUnpricedBilling(t *testing.T) {
	resetUnpricedBillingCountersForTest()
	ctx, logs := unpricedObservedContext()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	usage := OpenAIUsage{InputTokens: 1200, OutputTokens: 300}

	err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{RequestID: "resp_priced_openai", Usage: usage, Model: "gpt-5.1", Duration: time.Second},
		APIKey:  &APIKey{ID: 1003, Quota: 100, Group: &Group{ID: 16, Platform: PlatformOpenAI, RateMultiplier: 1}},
		User:    &User{ID: 2003},
		Account: &Account{ID: 3003, Type: AccountTypeAPIKey},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Greater(t, usageRepo.lastLog.TotalCost, 0.0, "有价模型照常计费")
	require.Empty(t, UnpricedBillingCounterSnapshot())
	require.Empty(t, unpricedLogsByMessage(logs, "billing.unpriced_usage"))
}

func TestOpenAIRecordUsage_CalcErrorNotesUnpricedBillingAndStillReturnsError(t *testing.T) {
	resetUnpricedBillingCountersForTest()
	ctx, logs := unpricedObservedContext()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)

	// 没有任何可用于计费的模型名：算价返回「价格缺失」以外的错误，既有行为是把错误返回给调用方、不写用量行。
	err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{RequestID: "resp_calc_error", Usage: OpenAIUsage{InputTokens: 10, OutputTokens: 5}, Duration: time.Second},
		APIKey:  &APIKey{ID: 1004, Quota: 100, Group: &Group{ID: 16, Platform: PlatformOpenAI, RateMultiplier: 1}},
		User:    &User{ID: 2004},
		Account: &Account{ID: 3004, Type: AccountTypeAPIKey},
	})

	require.Error(t, err)
	require.Equal(t, 0, usageRepo.calls)
	require.Equal(t, 0, billingRepo.calls)
	require.EqualValues(t, 1, unpricedCounterValue("openai", 16, "", UnpricedBillingReasonCalcError))
	entries := unpricedLogsByMessage(logs, "billing.unpriced_usage")
	require.Len(t, entries, 1)
	require.Equal(t, "error_returned", entries[0].ContextMap()["outcome"])
}

func newUnpricedGatewayService(withBilling bool) *GatewayService {
	svc := &GatewayService{}
	if withBilling {
		svc.billingService = NewBillingService(&config.Config{}, nil)
	}
	return svc
}

func TestGatewayCalculateTokenCost_NotesEachUnpricedReason(t *testing.T) {
	apiKey := &APIKey{ID: 501, Group: &Group{ID: 18, Platform: PlatformAnthropic, RateMultiplier: 1}}
	result := &ForwardResult{Model: unpricedTestModel, Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 6}}

	t.Run("missing price returns zero cost", func(t *testing.T) {
		resetUnpricedBillingCountersForTest()
		ctx, logs := unpricedObservedContext()

		cost := newUnpricedGatewayService(true).calculateTokenCost(ctx, result, apiKey, unpricedTestModel, 1, &recordUsageOpts{}, time.Now())

		require.NotNil(t, cost)
		require.Zero(t, cost.TotalCost)
		require.Zero(t, cost.ActualCost)
		require.EqualValues(t, 1, unpricedCounterValue("anthropic", 18, unpricedTestModel, UnpricedBillingReasonMissingPrice))
		entries := unpricedLogsByMessage(logs, "billing.unpriced_usage")
		require.Len(t, entries, 1)
		require.Equal(t, "zero_cost", entries[0].ContextMap()["outcome"])
	})

	t.Run("missing price with long-context options still zero cost", func(t *testing.T) {
		resetUnpricedBillingCountersForTest()
		ctx, _ := unpricedObservedContext()
		opts := &recordUsageOpts{LongContextThreshold: 200000, LongContextMultiplier: 2}

		cost := newUnpricedGatewayService(true).calculateTokenCost(ctx, result, apiKey, unpricedTestModel, 1, opts, time.Now())

		require.Zero(t, cost.ActualCost)
		require.EqualValues(t, 1, unpricedCounterValue("anthropic", 18, unpricedTestModel, UnpricedBillingReasonMissingPrice))
	})

	t.Run("billing service not injected returns zero cost", func(t *testing.T) {
		resetUnpricedBillingCountersForTest()
		ctx, logs := unpricedObservedContext()

		cost := newUnpricedGatewayService(false).calculateTokenCost(ctx, result, apiKey, unpricedTestModel, 1, &recordUsageOpts{}, time.Now())

		require.NotNil(t, cost)
		require.Zero(t, cost.ActualCost)
		require.EqualValues(t, 1, unpricedCounterValue("anthropic", 18, unpricedTestModel, UnpricedBillingReasonNoBillingService))
		require.Len(t, unpricedLogsByMessage(logs, "billing.unpriced_usage"), 1)
	})

	t.Run("priced model is not noted and cost unchanged", func(t *testing.T) {
		resetUnpricedBillingCountersForTest()
		ctx, logs := unpricedObservedContext()
		svc := newUnpricedGatewayService(true)
		pricedResult := &ForwardResult{Model: "claude-sonnet-4", Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 6}}

		cost := svc.calculateTokenCost(ctx, pricedResult, apiKey, "claude-sonnet-4", 1, &recordUsageOpts{}, time.Now())

		expected, err := svc.billingService.CalculateCost("claude-sonnet-4", UsageTokens{InputTokens: 10, OutputTokens: 6}, 1)
		require.NoError(t, err)
		require.Greater(t, cost.ActualCost, 0.0)
		require.Equal(t, expected.ActualCost, cost.ActualCost)
		require.Empty(t, UnpricedBillingCounterSnapshot())
		require.Empty(t, unpricedLogsByMessage(logs, "billing.unpriced_usage"))
	})
}

func TestGatewayRecordUsage_MissingPricingNotesUnpricedBillingAndKeepsZeroCost(t *testing.T) {
	resetUnpricedBillingCountersForTest()
	ctx, logs := unpricedObservedContext()
	usageRepo := &openAIRecordUsageBestEffortLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_unpriced",
			Usage:     ClaudeUsage{InputTokens: 10, OutputTokens: 6},
			Model:     unpricedTestModel,
			Duration:  time.Second,
		},
		APIKey:  &APIKey{ID: 501, Quota: 100, Group: &Group{ID: 18, Platform: PlatformAnthropic, RateMultiplier: 1}},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
	})

	// 与改动前一致：不报错，照常记一行成本为 0 的用量。
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Zero(t, usageRepo.lastLog.TotalCost)
	require.Zero(t, usageRepo.lastLog.ActualCost)
	require.Equal(t, 10, usageRepo.lastLog.InputTokens)
	require.Equal(t, 6, usageRepo.lastLog.OutputTokens)
	require.NotNil(t, billingRepo.lastCmd)
	require.Zero(t, billingRepo.lastCmd.BalanceCost)

	require.EqualValues(t, 1, unpricedCounterValue("anthropic", 18, unpricedTestModel, UnpricedBillingReasonMissingPrice))
	require.Len(t, unpricedLogsByMessage(logs, "billing.unpriced_usage"), 1)
}
