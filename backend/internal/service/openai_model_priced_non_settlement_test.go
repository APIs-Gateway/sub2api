//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 回退链的每跳资格检查（IsModelPricedForGroup）借用结算同一条取价链，但不对应任何用量行。
// 无价观测（noteUnpricedBilling）只属于真正写用量行的结算：一个无价模型的链请求，
// 每次资格检查都让计数加一，计数就和 usage_logs 对不上了。

func TestIsModelPricedForGroup_UnpricedModelDoesNotCountUnpricedBilling(t *testing.T) {
	resetUnpricedBillingCountersForTest()
	ctx, logs := unpricedObservedContext()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	hopKey := &APIKey{ID: 1101, Quota: 100, Group: &Group{ID: 16, Platform: PlatformOpenAI, RateMultiplier: 1}}

	for i := 0; i < 3; i++ {
		require.False(t, svc.IsModelPricedForGroup(ctx, hopKey, unpricedTestModel, ChannelMappingResult{}), "无价模型不能作为兜底")
	}
	require.True(t, svc.IsModelPricedForGroup(ctx, hopKey, "gpt-5.1", ChannelMappingResult{}))
	require.Empty(t, UnpricedBillingCounterSnapshot(), "资格检查不是结算：不计入无价计数")
	require.Empty(t, unpricedLogsByMessage(logs, "billing.unpriced_usage"), "资格检查不打无价日志")
	require.False(t, IsBillingNonSettlement(ctx), "打标记只作用在内部派生的 ctx，调用方的 ctx 不受影响")

	// 对照：同一个服务、同一个 ctx 上真正结算同一个无价模型，计数照常加一。
	err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{RequestID: "resp_priced_for_group_control", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 10}, Model: unpricedTestModel, Duration: time.Second},
		APIKey:  hopKey,
		User:    &User{ID: 2101},
		Account: &Account{ID: 3101, Type: AccountTypeAPIKey},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, unpricedCounterValue("openai", 16, unpricedTestModel, UnpricedBillingReasonMissingPrice), "结算照常计数，且只有这一次")
	require.Len(t, unpricedLogsByMessage(logs, "billing.unpriced_usage"), 1)
}

func TestIsModelPricedForGroup_KeepsCallerContextUnmarked(t *testing.T) {
	svc := newOpenAIRecordUsageServiceForTest(&openAIRecordUsageLogRepoStub{inserted: true}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	hopKey := &APIKey{ID: 1102, Group: &Group{ID: 16, Platform: PlatformOpenAI, RateMultiplier: 1}}
	base := context.Background()

	require.False(t, svc.IsModelPricedForGroup(base, hopKey, unpricedTestModel, ChannelMappingResult{}))
	require.False(t, IsBillingNonSettlement(base))
}
