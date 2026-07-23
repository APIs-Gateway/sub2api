//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayServiceRecordUsageCompositeAliasUsesConcreteModel(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	groupID := int64(10)
	apiKey := &APIKey{
		ID:      10,
		GroupID: &groupID,
		Group: &Group{
			ID:             groupID,
			Platform:       PlatformComposite,
			RateMultiplier: 1.1,
		},
	}
	usage := OpenAIUsage{InputTokens: 20, OutputTokens: 10}
	expectedCost, err := svc.billingService.CalculateCost("claude-opus-4-7", UsageTokens{
		InputTokens:  20,
		OutputTokens: 10,
	}, 1.1)
	require.NoError(t, err)

	err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:     "composite-concrete-billing",
			Model:         "all/claude",
			BillingModel:  "all/claude",
			UpstreamModel: "claude-opus-4-7",
			Usage:         usage,
			Duration:      time.Second,
		},
		APIKey:  apiKey,
		User:    &User{ID: 20},
		Account: &Account{ID: 30},
		ChannelUsageFields: ChannelUsageFields{
			OriginalModel:      "all/claude",
			ChannelMappedModel: "all/claude",
			BillingModelSource: BillingModelSourceRequested,
		},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.InDelta(t, expectedCost.ActualCost, usageRepo.lastLog.ActualCost, 1e-12)
	require.InDelta(t, expectedCost.ActualCost, userRepo.lastAmount, 1e-12)
}
