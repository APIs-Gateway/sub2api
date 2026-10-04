//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func resetUnpricedBillingCountersForTest() {
	unpricedBillingCounters.Range(func(k, _ any) bool {
		unpricedBillingCounters.Delete(k)
		return true
	})
	unpricedBillingCounterLen.Store(0)
}

func unpricedCounterValue(platform string, groupID int64, model, reason string) int64 {
	for _, c := range UnpricedBillingCounterSnapshot() {
		if c.Platform == platform && c.GroupID == groupID && c.Model == model && c.Reason == reason {
			return c.Count
		}
	}
	return 0
}

func TestNoteUnpricedBilling_CountsAndLogsStructuredFields(t *testing.T) {
	resetUnpricedBillingCountersForTest()
	core, logs := observer.New(zap.WarnLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	groupID := int64(16)
	apiKey := &APIKey{GroupID: &groupID, Group: &Group{ID: 16, Platform: PlatformOpenAI}}

	// 带前导空格的模型名必须原样进入维度，不能被 trim 掉。
	noteUnpricedBilling(ctx, apiKey, " gpt-x", UnpricedBillingReasonMissingPrice, errors.New("pricing not found"), "zero_cost", "gpt-x", "gpt-y")
	noteUnpricedBilling(ctx, apiKey, " gpt-x", UnpricedBillingReasonMissingPrice, nil, "zero_cost")
	noteUnpricedBilling(ctx, apiKey, "gpt-x", UnpricedBillingReasonCalcError, nil, "error_returned")

	require.EqualValues(t, 2, unpricedCounterValue("openai", 16, " gpt-x", "missing_price"))
	require.EqualValues(t, 1, unpricedCounterValue("openai", 16, "gpt-x", "calc_error"))
	require.EqualValues(t, 0, unpricedCounterValue("openai", 16, "gpt-x", "missing_price"), "不带空格的名字是另一个维度")

	entries := logs.All()
	require.Len(t, entries, 3)
	require.Equal(t, "billing.unpriced_usage", entries[0].Message)
	fields := entries[0].ContextMap()
	require.Equal(t, "service.billing", fields["component"])
	require.Equal(t, "missing_price", fields["reason"])
	require.Equal(t, "openai", fields["platform"])
	require.EqualValues(t, 16, fields["group_id"])
	require.Equal(t, " gpt-x", fields["model"])
	require.Equal(t, "zero_cost", fields["outcome"])
	require.Equal(t, []any{"gpt-x", "gpt-y"}, fields["billing_models"])
	require.Equal(t, "pricing not found", fields["error"])

	_, hasErr := entries[1].ContextMap()["error"]
	require.False(t, hasErr, "err 为 nil 时不带 error 字段")
	_, hasModels := entries[1].ContextMap()["billing_models"]
	require.False(t, hasModels, "没有候选模型时不带 billing_models 字段")
}

func TestNoteUnpricedBilling_NilAPIKey(t *testing.T) {
	resetUnpricedBillingCountersForTest()
	require.NotPanics(t, func() {
		noteUnpricedBilling(context.Background(), nil, "m-nil", UnpricedBillingReasonNoBillingService, nil, "zero_cost", "m-nil")
	})
	require.EqualValues(t, 1, unpricedCounterValue("", 0, "m-nil", "no_billing_service"))
}

func TestUnpricedBillingDims(t *testing.T) {
	platform, groupID := unpricedBillingDims(nil)
	require.Equal(t, "", platform)
	require.EqualValues(t, 0, groupID)

	id := int64(7)
	platform, groupID = unpricedBillingDims(&APIKey{GroupID: &id, Group: &Group{ID: 99, Platform: PlatformAnthropic}})
	require.Equal(t, PlatformAnthropic, platform)
	require.EqualValues(t, 7, groupID, "APIKey.GroupID 优先")

	platform, groupID = unpricedBillingDims(&APIKey{Group: &Group{ID: 99, Platform: PlatformOpenAI}})
	require.Equal(t, PlatformOpenAI, platform)
	require.EqualValues(t, 99, groupID, "没有 GroupID 时退回 Group.ID")

	platform, groupID = unpricedBillingDims(&APIKey{GroupID: &id})
	require.Equal(t, "", platform)
	require.EqualValues(t, 7, groupID)
}

func TestUnpricedBillingCounter_CardinalityCapFoldsIntoOverflowKey(t *testing.T) {
	resetUnpricedBillingCountersForTest()
	extra := 12
	for i := 0; i < unpricedBillingCounterMaxKeys+extra; i++ {
		incrementUnpricedBillingCounter("openai", 1, fmt.Sprintf("cap-model-%d", i), UnpricedBillingReasonMissingPrice)
	}
	// 已有维度超过上限后仍能继续累加。
	incrementUnpricedBillingCounter("openai", 1, "cap-model-0", UnpricedBillingReasonMissingPrice)

	snapshot := UnpricedBillingCounterSnapshot()
	require.LessOrEqual(t, len(snapshot), unpricedBillingCounterMaxKeys+1)
	require.EqualValues(t, extra, unpricedCounterValue("openai", 1, unpricedBillingOverflowModel, "missing_price"))
	require.EqualValues(t, 2, unpricedCounterValue("openai", 1, "cap-model-0", "missing_price"))
}

func TestUnpricedBillingCounter_TruncatesLongModelNames(t *testing.T) {
	resetUnpricedBillingCountersForTest()
	long := strings.Repeat("x", unpricedBillingModelMaxBytes+50)
	incrementUnpricedBillingCounter("openai", 2, long, UnpricedBillingReasonCalcError)
	require.EqualValues(t, 1, unpricedCounterValue("openai", 2, long[:unpricedBillingModelMaxBytes], "calc_error"))
}

func TestUnpricedBillingCounterSnapshot_SortedByCountThenKey(t *testing.T) {
	resetUnpricedBillingCountersForTest()
	incrementUnpricedBillingCounter("openai", 2, "b", "missing_price")
	incrementUnpricedBillingCounter("openai", 1, "a", "missing_price")
	incrementUnpricedBillingCounter("openai", 1, "a", "missing_price")
	incrementUnpricedBillingCounter("anthropic", 3, "c", "calc_error")

	snapshot := UnpricedBillingCounterSnapshot()
	require.Len(t, snapshot, 3)
	require.Equal(t, UnpricedBillingCounter{Platform: "openai", GroupID: 1, Model: "a", Reason: "missing_price", Count: 2}, snapshot[0])
	require.Equal(t, "anthropic", snapshot[1].Platform, "次数相同按平台排序")
	require.Equal(t, "openai", snapshot[2].Platform)
}

func TestParseBillingKnownFreeList(t *testing.T) {
	list, err := parseBillingKnownFreeList("   ")
	require.NoError(t, err)
	require.Empty(t, list)

	_, err = parseBillingKnownFreeList(`{"not":"an array"}`)
	require.Error(t, err)
	_, err = parseBillingKnownFreeList(`[`)
	require.Error(t, err)

	list, err = parseBillingKnownFreeList(`[
		{"group_id": 16, "model": "free-a", "note": "活动期免费"},
		{"model": "free-any"},
		{"group_id": -1, "model": "negative-group"},
		{"group_id": 5, "model": ""}
	]`)
	require.NoError(t, err)
	require.Equal(t, []BillingKnownFreeEntry{
		{GroupID: 16, Model: "free-a", Note: "活动期免费"},
		{GroupID: 0, Model: "free-any"},
	}, list, "model 为空或 group_id 为负的项被丢弃")
}

func TestBillingKnownFreeMatches(t *testing.T) {
	list := []BillingKnownFreeEntry{
		{GroupID: 16, Model: "Free-A"},
		{GroupID: 0, Model: "free-any"},
	}
	require.True(t, billingKnownFreeMatches(list, 16, "free-a"), "忽略大小写")
	require.False(t, billingKnownFreeMatches(list, 18, "free-a"), "分组不同不匹配")
	require.True(t, billingKnownFreeMatches(list, 18, "FREE-ANY"), "group_id=0 匹配任意分组")
	require.True(t, billingKnownFreeMatches(list, 0, "free-any"), "没有分组的用量也匹配任意分组项")
	require.False(t, billingKnownFreeMatches(list, 16, " free-a"), "不 trim：带前导空格的名字不会被盖住")
	require.False(t, billingKnownFreeMatches(nil, 16, "free-a"))
}
