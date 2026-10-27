//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func pssParse(t *testing.T, payload []byte) map[string]*LiteLLMModelPricing {
	t.Helper()
	data, err := NewPricingService(nil, nil).parsePricingData(payload)
	require.NoError(t, err)
	return data
}

func TestComputePricingDiff(t *testing.T) {
	base := pssParse(t, []byte(`{
  "keep": {"input_cost_per_token": 1e-6, "output_cost_per_token": 2e-6},
  "changed": {"input_cost_per_token": 1e-6, "output_cost_per_token": 2e-6, "litellm_provider": "openai"},
  "gone": {"input_cost_per_token": 3e-6}
}`))
	cand := pssParse(t, []byte(`{
  "keep": {"input_cost_per_token": 1e-6, "output_cost_per_token": 2e-6},
  "changed": {"input_cost_per_token": 2e-6, "output_cost_per_token": 2e-6, "litellm_provider": "azure", "cache_read_input_token_cost": 1e-7},
  "fresh": {"input_cost_per_token": 4e-6}
}`))

	entries, unchanged := computePricingDiff(base, cand)
	require.Equal(t, 1, unchanged)
	require.Len(t, entries, 3)
	require.Equal(t, []string{"changed", "fresh", "gone"}, []string{entries[0].ModelKey, entries[1].ModelKey, entries[2].ModelKey}, "按模型键排序")

	require.Equal(t, PricingDiffChanged, entries[0].ChangeType)
	require.Equal(t, []string{"input_cost_per_token", "cache_read_input_token_cost", "litellm_provider"}, entries[0].ChangedFields, "按结构体字段顺序，用 JSON 字段名")
	require.NotNil(t, entries[0].Old)
	require.NotNil(t, entries[0].New)
	require.Equal(t, PricingDiffAdded, entries[1].ChangeType)
	require.Nil(t, entries[1].Old)
	require.Equal(t, []string{}, entries[1].ChangedFields)
	require.Equal(t, PricingDiffRemoved, entries[2].ChangeType)
	require.Nil(t, entries[2].New)
	for _, e := range entries {
		require.Equal(t, PricingDiffDecisionApprove, e.Decision, "决定默认是批准")
	}

	none, same := computePricingDiff(base, base)
	require.Empty(t, none)
	require.Equal(t, 3, same)
}

func TestMergePricingPayload(t *testing.T) {
	basePayload := []byte(`{"sample_spec": {"note": "doc entry"}, "a": {"input_cost_per_token": 1e-6}, "b": {"input_cost_per_token": 2e-6, "source": "https://x/?a=1&b=2"}, "gone": {"input_cost_per_token": 3e-6}, "held": {"input_cost_per_token": 5e-6}}`)
	candPayload := []byte(`{"a": {"input_cost_per_token": 9e-6}, "b": {"input_cost_per_token": 2e-6}, "held": {"input_cost_per_token": 7e-6}, "fresh": {"input_cost_per_token": 4e-6}}`)
	entries, _ := computePricingDiff(pssParse(t, basePayload), pssParse(t, candPayload))
	held, err := applyHolds(entries, []string{"held"})
	require.NoError(t, err)
	require.Equal(t, []string{"held"}, held)

	merged, err := mergePricingPayload(basePayload, candPayload, entries)
	require.NoError(t, err)

	var got map[string]map[string]any
	require.NoError(t, json.Unmarshal(merged, &got))
	require.Contains(t, got, "sample_spec", "没有价格的文档条目原样保留")
	require.Equal(t, 9e-6, got["a"]["input_cost_per_token"], "批准的变化取候选")
	require.Equal(t, 2e-6, got["b"]["input_cost_per_token"])
	require.Equal(t, 5e-6, got["held"]["input_cost_per_token"], "搁置的模型保持旧值")
	require.Equal(t, 4e-6, got["fresh"]["input_cost_per_token"], "批准的新增取候选")
	require.NotContains(t, got, "gone", "批准的移除被删除")

	// 同样的输入永远得到同样的字节，且不转义 HTML 字符。
	again, err := mergePricingPayload(basePayload, candPayload, entries)
	require.NoError(t, err)
	require.Equal(t, merged, again)
	require.Contains(t, string(merged), "?a=1&b=2")
	require.NotContains(t, string(merged), "\n")

	// 合成结果能被价格解析器接受，模型数 = 4（a、b、held、fresh）。
	require.Len(t, pssParse(t, merged), 4)
}

func TestMergePricingPayloadErrors(t *testing.T) {
	good := []byte(`{"a": {"input_cost_per_token": 1e-6}}`)
	_, err := mergePricingPayload([]byte("nope"), good, nil)
	require.Error(t, err)
	_, err = mergePricingPayload(good, []byte("nope"), nil)
	require.Error(t, err)
	_, err = mergePricingPayload(good, good, []PricingSnapshotDiffEntry{{ModelKey: "zzz", ChangeType: PricingDiffAdded, Decision: PricingDiffDecisionApprove}})
	require.Error(t, err, "候选里没有的条目不能被批准")
}

func TestApplyHolds(t *testing.T) {
	entries := []PricingSnapshotDiffEntry{
		{ModelKey: "a", Decision: PricingDiffDecisionApprove},
		{ModelKey: "b", Decision: PricingDiffDecisionApprove},
	}
	held, err := applyHolds(entries, []string{" b ", "b", ""})
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, held)
	require.Equal(t, PricingDiffDecisionApprove, entries[0].Decision)
	require.Equal(t, PricingDiffDecisionHold, entries[1].Decision)

	held, err = applyHolds(entries, nil)
	require.NoError(t, err)
	require.Equal(t, []string{}, held)

	_, err = applyHolds(entries, []string{"a", "zz", "yy"})
	require.ErrorIs(t, err, ErrPricingSnapshotUnknownHold)
	require.Contains(t, err.Error(), "yy, zz")
}

func TestDiffRecords(t *testing.T) {
	base := pssParse(t, []byte(`{"c": {"input_cost_per_token": 1e-6}, "r": {"input_cost_per_token": 2e-6}}`))
	cand := pssParse(t, []byte(`{"c": {"input_cost_per_token": 3e-6}, "n": {"input_cost_per_token": 4e-6}}`))
	entries, _ := computePricingDiff(base, cand)
	_, err := applyHolds(entries, []string{"n"})
	require.NoError(t, err)

	recs := diffRecords(entries)
	require.Len(t, recs, 3)
	byKey := map[string]PricingSnapshotDiffRecord{}
	for _, r := range recs {
		byKey[r.ModelKey] = r
	}
	require.NotEmpty(t, byKey["c"].OldPrice)
	require.NotEmpty(t, byKey["c"].NewPrice)
	require.Equal(t, []string{"input_cost_per_token"}, byKey["c"].ChangedFields)
	require.Empty(t, byKey["n"].OldPrice, "新增没有旧价格")
	require.Equal(t, PricingDiffDecisionHold, byKey["n"].Decision)
	require.Empty(t, byKey["r"].NewPrice, "移除没有新价格")
	var decoded LiteLLMModelPricing
	require.NoError(t, json.Unmarshal(byKey["c"].NewPrice, &decoded))
	require.InDelta(t, 3e-6, decoded.InputCostPerToken, 1e-15)
}
