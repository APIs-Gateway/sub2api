//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBillingNonSettlementMarker(t *testing.T) {
	base := context.Background()
	require.False(t, IsBillingNonSettlement(base))

	marked := WithBillingNonSettlement(base)
	require.True(t, IsBillingNonSettlement(marked))
	require.False(t, IsBillingNonSettlement(base), "打标记不影响原 ctx")

	child, cancel := context.WithCancel(marked)
	defer cancel()
	require.True(t, IsBillingNonSettlement(child), "派生出的 ctx 继承标记")

	var nilCtx context.Context
	require.False(t, IsBillingNonSettlement(nilCtx))
	require.True(t, IsBillingNonSettlement(WithBillingNonSettlement(nilCtx)))
}

func TestNoteUnpricedBilling_SkipsNonSettlementCalls(t *testing.T) {
	resetUnpricedBillingCountersForTest()
	ctx, logs := unpricedObservedContext()
	apiKey := &APIKey{Group: &Group{ID: 16, Platform: PlatformOpenAI}}

	noteUnpricedBilling(WithBillingNonSettlement(ctx), apiKey, "m-estimate", UnpricedBillingReasonMissingPrice,
		errors.New("pricing not found"), "zero_cost", "m-estimate")
	require.Empty(t, UnpricedBillingCounterSnapshot(), "非结算调用不计数")
	require.Zero(t, logs.Len(), "非结算调用不打日志")

	noteUnpricedBilling(ctx, apiKey, "m-settle", UnpricedBillingReasonMissingPrice, nil, "zero_cost")
	require.EqualValues(t, 1, unpricedCounterValue("openai", 16, "m-settle", UnpricedBillingReasonMissingPrice), "结算调用照常计数")
	require.Equal(t, 1, logs.Len())
}
