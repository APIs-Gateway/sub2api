//go:build unit

package service

// W6 PR7a：v2 分组读矩阵计费，legacy / shadow 分组的计费与改动前逐位相同。
// 成本函数本身（额外倍率乘在哪里）已由 PR4 的用例覆盖，这里验证的是「阶段 -> 走哪一边」。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func goldenKey() *APIKey { return mcKey(Group{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1}) }

func goldenResult() *OpenAIForwardResult {
	return &OpenAIForwardResult{Model: "gpt-5.4"}
}

func goldenWildCells() []StoredMatrixCell {
	return []StoredMatrixCell{mpCustom("gpt-5.4", 9e-6), mpExtra("gpt-5.4-mini", 3), mcImageCell("gpt-image-2", 0.5)}
}

func goldenCost(t *testing.T, f *spFixture, models []string, result *OpenAIForwardResult) *CostBreakdown {
	t.Helper()
	return mcOpenAICost(t, f.staged, goldenKey(), models, result, 1, 1)
}

// 基线：没有任何矩阵数据时的计费（官方价）。
func goldenBaseline(t *testing.T, models []string, result *OpenAIForwardResult) *CostBreakdown {
	t.Helper()
	return mcOpenAICost(t, newMPPolicyFor(GroupStateSnapshot{}), goldenKey(), models, result, 1, 1)
}

func TestStagedGolden_LegacyAndShadowBillingIgnoresTheMatrix(t *testing.T) {
	base := goldenBaseline(t, []string{"gpt-5.4"}, goldenResult())
	baseMini := goldenBaseline(t, []string{"gpt-5.4-mini"}, &OpenAIForwardResult{Model: "gpt-5.4-mini"})
	for _, stage := range []PricingStage{PricingStageLegacy, PricingStageShadow} {
		cfg := mpStoredConfig(stage, nil)
		f := newSPFixture(t, GroupStateSnapshot{Config: cfg, Cells: goldenWildCells()})
		got := goldenCost(t, f, []string{"gpt-5.4"}, goldenResult())
		require.Equal(t, *base, *got, "stage %s: cost is bitwise the legacy cost", stage)
		gotMini := goldenCost(t, f, []string{"gpt-5.4-mini"}, &OpenAIForwardResult{Model: "gpt-5.4-mini"})
		require.Equal(t, *baseMini, *gotMini, "stage %s", stage)
		require.Zero(t, got.extraMultiplier)
		require.Zero(t, gotMini.extraMultiplier)
	}
}

func TestStagedGolden_V2CustomTokenPrice(t *testing.T) {
	base := goldenBaseline(t, []string{"gpt-5.4"}, goldenResult())
	f := newSPFixture(t, v2Snap(nil, 1, goldenWildCells()...))
	got := goldenCost(t, f, []string{"gpt-5.4"}, goldenResult())
	require.NotEqual(t, base.TotalCost, got.TotalCost)
	// 1000 input x 9e-6 + 100 output x 36e-6（mpCustom 的输出价是输入价的 4 倍）
	require.InDelta(t, 1000*9e-6+100*36e-6, got.TotalCost, 1e-12)
}

func TestStagedGolden_V2InheritEqualsBaseline(t *testing.T) {
	base := goldenBaseline(t, []string{"gpt-5.4"}, goldenResult())
	f := newSPFixture(t, v2Snap(nil, 1, mpInherit("gpt-5.4")))
	got := goldenCost(t, f, []string{"gpt-5.4"}, goldenResult())
	require.Equal(t, *base, *got, "inherit routes as no channel price: same cost as the baseline")
}

func TestStagedGolden_V2ExtraMultiplierAndVariantName(t *testing.T) {
	base := goldenBaseline(t, []string{"gpt-5.4"}, goldenResult())
	f := newSPFixture(t, v2Snap(nil, 1, mpExtra("gpt-5.4", 2)))
	got := goldenCost(t, f, []string{"gpt-5.4"}, goldenResult())
	mcRequireScaled(t, base, got, 2)

	baseVar := goldenBaseline(t, []string{"gpt-5.4-high"}, &OpenAIForwardResult{Model: "gpt-5.4-high"})
	gotVar := goldenCost(t, f, []string{"gpt-5.4-high"}, &OpenAIForwardResult{Model: "gpt-5.4-high"})
	mcRequireScaled(t, baseVar, gotVar, 2)
}

func TestStagedGolden_V2ImagePerRequest(t *testing.T) {
	f := newSPFixture(t, v2Snap(nil, 1, mcImageCell("gpt-image-2", 0.5)))
	res := &OpenAIForwardResult{Model: "gpt-image-2", ImageCount: 2, ImageSize: "1K"}
	got := mcOpenAICost(t, f.staged, goldenKey(), []string{"gpt-image-2"}, res, 1, 1)
	require.Equal(t, string(BillingModeImage), got.BillingMode)
	require.InDelta(t, 1.0, got.TotalCost, 1e-12)
}

func TestStagedGolden_V2InheritImageFallsBackToImageTable(t *testing.T) {
	res := &OpenAIForwardResult{Model: "gpt-image-2", ImageCount: 1, ImageSize: "1K"}
	base := mcOpenAICost(t, newMPPolicyFor(GroupStateSnapshot{}), goldenKey(), []string{"gpt-image-2"}, res, 1, 1)
	f := newSPFixture(t, v2Snap(nil, 1, mpInherit("gpt-image-2")))
	got := mcOpenAICost(t, f.staged, goldenKey(), []string{"gpt-image-2"}, res, 1, 1)
	require.Equal(t, *base, *got)
}

func TestStagedGolden_V2UnpricedIsClosedForAllowlist(t *testing.T) {
	ctx := context.Background()
	f := newSPFixture(t, v2Snap(allowlistConfig, 1, mpCustom("gpt-5.4", 1)))
	require.True(t, f.staged.ModelAccess(ctx, 1, "gpt-5.4").OK)
	require.False(t, f.staged.ModelAccess(ctx, 1, "gpt-other").OK, "unlisted models are not served by an allowlist v2 group")
}

func TestStagedGolden_RateMultiplierOnUsageLogOnlyForV2(t *testing.T) {
	legacyLog := mcRunOpenAIRecordUsage(t, newMPPolicyFor(GroupStateSnapshot{}))
	f := newSPFixture(t, v2Snap(nil, 1, mpExtra("gpt-5.4", 2)))
	v2Log := mcRunOpenAIRecordUsage(t, f.staged)
	require.InDelta(t, legacyLog.RateMultiplier*2, v2Log.RateMultiplier, 1e-12)
	require.InDelta(t, v2Log.TotalCost*v2Log.RateMultiplier, v2Log.ActualCost, 1e-12)

	shadow := newSPFixture(t, GroupStateSnapshot{Config: mpStoredConfig(PricingStageShadow, nil), Cells: []StoredMatrixCell{mpExtra("gpt-5.4", 2)}})
	shadowLog := mcRunOpenAIRecordUsage(t, shadow.staged)
	require.Equal(t, legacyLog.RateMultiplier, shadowLog.RateMultiplier)
	require.Equal(t, legacyLog.ActualCost, shadowLog.ActualCost)
}

func TestStagedGolden_AnthropicRecordUsageUsesTheMatrixOnlyInV2(t *testing.T) {
	base := mcRunGatewayRecordUsage(t, newMPPolicyFor(GroupStateSnapshot{}))
	f := newSPFixture(t, v2Snap(nil, 1, mpExtra("claude-sonnet-4", 2)))
	got := mcRunGatewayRecordUsage(t, f.staged)
	require.InDelta(t, base.ActualCost*2, got.ActualCost, 1e-12)
	require.InDelta(t, base.RateMultiplier*2, got.RateMultiplier, 1e-12)

	shadow := newSPFixture(t, GroupStateSnapshot{Config: mpStoredConfig(PricingStageShadow, nil), Cells: []StoredMatrixCell{mpExtra("claude-sonnet-4", 2)}})
	sg := mcRunGatewayRecordUsage(t, shadow.staged)
	require.Equal(t, base.ActualCost, sg.ActualCost)
	require.Equal(t, base.RateMultiplier, sg.RateMultiplier)
}
