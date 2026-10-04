//go:build unit

package service

// W6 PR4-2：PriceQuoter 镜像额外倍率、Quote.Access、入口名字规范化（设计 3.2、3.3）。
//
// 契约测试的写法：报价器与网关共用同一个 BillingService 和 ModelPricingResolver（同一份夹具），
// 参照侧自己调网关的成本函数（calculateOpenAIRecordUsageCost、calculateRecordUsageCost），
// 两边 require.Equal 整个费用明细（含额外倍率标记），误差为 0。
//
// legacy 零变化的证明：默认装配（渠道服务现取 legacyPolicy）、显式注入 legacyPolicy、没有单元格的 v2 分组，
// 三条路径的报价逐字段相同，额外倍率恒为 1。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

// qxMatrix 为报价测试里的计价分组（quoteTestGroupID）构造一个 matrixPolicy。
func qxMatrix(platform string, snap GroupStateSnapshot) *matrixPolicy {
	p, _ := newMPForTest(newMPSource(platform, map[int64]GroupStateSnapshot{quoteTestGroupID: snap}), nil)
	return p
}

// qxCustomWithExtra 造一个真实快照编译不出来的组合：价格覆盖来自 custom 单元格，额外倍率恒为 extra。
// 一个单元格只有一种价格模式，所以真实的 v2 分组里「某个模型既有 custom 价、又有额外倍率」不会出现；
// 这里只用来验证「token 模式渠道价 + 图片请求」这条路径把额外倍率乘在 token 倍率上。
func qxCustomWithExtra(platform string, cells []StoredMatrixCell, extra float64) GroupPolicy {
	return mcFixedExtra{GroupPolicy: qxMatrix(platform, GroupStateSnapshot{Cells: cells}), extra: extra}
}

// qxFixture 按生产装配方式拼出报价器，再把分组策略换成 policy。报价器与下面两个网关共用同一个
// BillingService 和 ModelPricingResolver，对照时不会因为各用各的实例而偏离。
func qxFixture(group *Group, policy GroupPolicy) *quoteTestFixture {
	f := newQuoteTestFixture(nil, nil, []*Group{group}, nil)
	f.resolver.policyOverride = policy
	return f
}

func qxOpenAIGateway(f *quoteTestFixture, policy GroupPolicy) *OpenAIGatewayService {
	return &OpenAIGatewayService{billingService: f.billing, policyOverride: policy, resolver: f.resolver}
}

func qxGateway(f *quoteTestFixture, policy GroupPolicy) *GatewayService {
	return &GatewayService{billingService: f.billing, policyOverride: policy, resolver: f.resolver}
}

func qxKey(g *Group) *APIKey {
	gid := g.ID
	return &APIKey{ID: 1, GroupID: &gid, Group: g}
}

func qxQuote(t *testing.T, f *quoteTestFixture, model string) *Quote {
	t.Helper()
	q, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: model, GroupID: quoteTestGroupID, At: quoteTestAtLow})
	require.NoError(t, err)
	return q
}

// qxQuoteJSON 把报价序列化成 JSON，用来整体比较两份报价是否逐字段相同。
// Quote.Model 原样回显调用方给的写法，比较「同一个模型的不同写法」时先清掉。
func qxQuoteJSON(t *testing.T, q *Quote) string {
	t.Helper()
	cp := *q
	cp.Model = ""
	raw, err := json.Marshal(&cp)
	require.NoError(t, err)
	return string(raw)
}

// ---------------------------------------------------------------------------
// 契约：额外倍率的取值位置与网关一致
// ---------------------------------------------------------------------------

// 候选回退：网关在候选循环里逐个试，第一个出价的候选用它自己的额外倍率。
// Quote 只有一个模型（调用方给的计费模型），给出出价那个候选时，费用与网关逐字段相同。
func TestQuote_ExtraMultiplierMirrorsOpenAICandidateLoop(t *testing.T) {
	ctx := context.Background()
	rate := 1.5
	group := quoteTestGroupOn(PlatformOpenAI, rate)
	policy := qxMatrix(PlatformOpenAI, GroupStateSnapshot{Cells: []StoredMatrixCell{
		mpExtra("no-such-model-xyz", 5), // 没有官方价：轮不到出价
		mpExtra("gpt-5.4", 2),
		mpExtra("gpt-5.4-mini", 3),
	}})
	f := qxFixture(group, policy)
	gateway := qxOpenAIGateway(f, policy)
	key := qxKey(group)
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 200}

	cases := []struct {
		name       string
		candidates []string
		priced     string
		wantExtra  float64
	}{
		{"first candidate has no price: the second one prices and multiplies", []string{"no-such-model-xyz", "gpt-5.4"}, "gpt-5.4", 2},
		{"first candidate prices: later candidates take no part", []string{"gpt-5.4-mini", "gpt-5.4"}, "gpt-5.4-mini", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := gateway.calculateOpenAIRecordUsageCost(ctx, &OpenAIForwardResult{}, key, tc.candidates, rate, rate, tokens, "", quoteTestAtLow)
			require.NoError(t, err)
			require.Equal(t, tc.wantExtra, want.extraMultiplier)

			quote := qxQuote(t, f, tc.priced)
			require.True(t, quote.Priced)
			require.NotNil(t, quote.ExtraMultiplier)
			require.Equal(t, tc.wantExtra, *quote.ExtraMultiplier)
			require.Equal(t, rate*tc.wantExtra, quote.EffectiveMultiplier)
			require.Equal(t, rate, quote.GroupMultiplier, "the group multiplier is shown unmultiplied")
			got, err := quote.Cost(ctx, QuoteUsage{Tokens: tokens})
			require.NoError(t, err)
			require.Equal(t, *want, *got)

			// 展示单价：Prices 是未乘倍率的官方价，FinalPrices 乘了倍率（含额外倍率）。
			base := qxQuote(t, qxFixture(group, qxMatrix(PlatformOpenAI, GroupStateSnapshot{})), tc.priced)
			require.Equal(t, base.Prices, quote.Prices, "the extra multiplier never changes the unit price")
			require.InDelta(t, base.FinalPrices.PerToken.Input*tc.wantExtra, quote.FinalPrices.PerToken.Input, 1e-15)
			require.InDelta(t, base.FinalPrices.PerToken.Output*tc.wantExtra, quote.FinalPrices.PerToken.Output, 1e-15)
		})
	}

	// 没有价格的候选：Quote 如实报「无价」，额外倍率照取（它是单元格上的配置），但算不出费用。
	unpriced := qxQuote(t, f, "no-such-model-xyz")
	require.False(t, unpriced.Priced)
	require.NotNil(t, unpriced.ExtraMultiplier)
	require.Equal(t, 5.0, *unpriced.ExtraMultiplier)
	_, err := unpriced.Cost(ctx, QuoteUsage{Tokens: tokens})
	require.Error(t, err)
	require.True(t, isUsagePricingUnavailableError(err))
}

// 变体名使用基名的 extra；字面名上的单元格（哪怕是 inherit）优先，不再回落到基名。
func TestQuote_VariantNameUsesBaseCellExtraLikeTheGateway(t *testing.T) {
	ctx := context.Background()
	rate := 1.5
	group := quoteTestGroupOn(PlatformOpenAI, rate)
	policy := qxMatrix(PlatformOpenAI, GroupStateSnapshot{Cells: []StoredMatrixCell{
		mpExtra("gpt-5.6-luna", 4),
		mpExtra("gpt-5.4", 2),
		mpInherit("gpt-5.2-high"), // 变体名上的 inherit：停止查找，不回落到基名
		mpExtra("gpt-5.2", 5),
	}})
	f := qxFixture(group, policy)
	gateway := qxOpenAIGateway(f, policy)
	key := qxKey(group)
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 100}

	for model, wantExtra := range map[string]float64{
		"gpt-5.6-luna-xhigh": 4,
		"gpt-5.4-high":       2,
		"gpt-5.2-high":       1,
	} {
		t.Run(model, func(t *testing.T) {
			want, err := gateway.calculateOpenAIRecordUsageCost(ctx, &OpenAIForwardResult{}, key, []string{model}, rate, rate, tokens, "", quoteTestAtLow)
			require.NoError(t, err)
			quote := qxQuote(t, f, model)
			require.True(t, quote.Priced)
			if wantExtra == 1 {
				require.Nil(t, quote.ExtraMultiplier, "no extra: the field is absent, not 1")
				require.Equal(t, rate, quote.EffectiveMultiplier)
				require.Zero(t, want.extraMultiplier)
			} else {
				require.NotNil(t, quote.ExtraMultiplier)
				require.Equal(t, wantExtra, *quote.ExtraMultiplier)
				require.Equal(t, wantExtra, want.extraMultiplier)
			}
			got, err := quote.Cost(ctx, QuoteUsage{Tokens: tokens})
			require.NoError(t, err)
			require.Equal(t, *want, *got)
		})
	}
}

// Anthropic 网关在 billableModelWithFallback 选定计费模型之后取一次额外倍率：别名没有价格、退到后面的候选时，
// 用的是被选中的那个模型的倍率，别名自己的倍率不参与。
func TestQuote_ExtraMultiplierMirrorsAnthropicGatewayAfterBillableModelFallback(t *testing.T) {
	ctx := context.Background()
	rate := 1.3
	group := quoteTestGroupOn(PlatformAnthropic, rate)
	policy := qxMatrix(PlatformAnthropic, GroupStateSnapshot{Cells: []StoredMatrixCell{
		mpExtra("unpriced-alias-xyz", 9),
		mpExtra("claude-sonnet-4", 2.5),
	}})
	f := qxFixture(group, policy)
	gateway := qxGateway(f, policy)
	key := qxKey(group)

	selected := gateway.billableModelWithFallback(ctx, key, "unpriced-alias-xyz", "claude-sonnet-4")
	require.Equal(t, "claude-sonnet-4", selected, "the alias has no price; the first priced fallback is chosen")

	result := &ForwardResult{
		Model: selected,
		Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 100, CacheReadInputTokens: 200, CacheCreationInputTokens: 50},
	}
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 200, CacheCreationTokens: 50}
	want := gateway.calculateRecordUsageCost(ctx, result, key, selected, rate, rate, &recordUsageOpts{}, quoteTestAtLow)
	require.Equal(t, 2.5, want.extraMultiplier, "the multiplier of the selected model, not of the alias")

	quote := qxQuote(t, f, selected)
	require.True(t, quote.Priced)
	require.Equal(t, 2.5, *quote.ExtraMultiplier)
	require.Equal(t, rate*2.5, quote.EffectiveMultiplier)
	got, err := quote.Cost(ctx, QuoteUsage{Tokens: tokens})
	require.NoError(t, err)
	require.Equal(t, *want, *got)

	// 别名本身：没有价格；它的倍率照取，但网关不会按它计费（选中的是后面的候选）。
	alias := qxQuote(t, f, "unpriced-alias-xyz")
	require.False(t, alias.Priced)
	require.Equal(t, 9.0, *alias.ExtraMultiplier)
}

// 图片请求：额外倍率乘在图片倍率之后（不论是否独立图片倍率），两个网关的图片路径与 Quote 逐字段相同。
func TestQuote_ImageRequestExtraMirrorsBothGateways(t *testing.T) {
	ctx := context.Background()
	price := 0.04

	t.Run("openai gateway, group image price", func(t *testing.T) {
		group := quoteTestGroupOn(PlatformOpenAI, 1.3)
		group.ImageRateIndependent, group.ImageRateMultiplier, group.ImagePrice1K = true, 2.7, &price
		policy := qxMatrix(PlatformOpenAI, GroupStateSnapshot{Cells: []StoredMatrixCell{mpExtra("gpt-image-2", 3)}})
		f := qxFixture(group, policy)
		key := qxKey(group)
		result := &OpenAIForwardResult{Model: "gpt-image-2", ImageCount: 2, ImageSize: "1K"}

		want, err := qxOpenAIGateway(f, policy).calculateOpenAIRecordUsageCost(ctx, result, key, []string{"gpt-image-2"},
			1.3, resolveImageRateMultiplier(key, 1.3), UsageTokens{}, "", quoteTestAtLow)
		require.NoError(t, err)
		require.Equal(t, string(BillingModeImage), want.BillingMode)

		quote := qxQuote(t, f, "gpt-image-2")
		imageRate, extra := 2.7, 3.0
		require.Equal(t, imageRate*extra, quote.ImageMultiplier, "the extra multiplier goes after the independent image multiplier")
		require.Equal(t, 1.3*extra, quote.EffectiveMultiplier)
		got, err := quote.Cost(ctx, QuoteUsage{ImageCount: 2, ImageSize: "1K"})
		require.NoError(t, err)
		require.Equal(t, *want, *got)
		require.InDelta(t, 0.08*2.7*3, got.ActualCost, 1e-12)
	})

	t.Run("anthropic gateway family, gemini group", func(t *testing.T) {
		group := quoteTestGroupOn(PlatformGemini, 1.2)
		group.ImageRateIndependent, group.ImageRateMultiplier, group.ImagePrice1K = true, 1.5, &price
		policy := qxMatrix(PlatformGemini, GroupStateSnapshot{Cells: []StoredMatrixCell{mpExtra("gemini-image", 3)}})
		f := qxFixture(group, policy)
		key := qxKey(group)
		result := &ForwardResult{Model: "gemini-image", ImageCount: 2, ImageSize: "1K"}

		want := qxGateway(f, policy).calculateRecordUsageCost(ctx, result, key, "gemini-image",
			1.2, resolveImageRateMultiplier(key, 1.2), &recordUsageOpts{}, quoteTestAtLow)
		require.Equal(t, string(BillingModeImage), want.BillingMode)

		quote := qxQuote(t, f, "gemini-image")
		got, err := quote.Cost(ctx, QuoteUsage{ImageCount: 2, ImageSize: "1K"})
		require.NoError(t, err)
		require.Equal(t, *want, *got)
		require.InDelta(t, 0.08*1.5*3, got.ActualCost, 1e-12)
	})
}

// 图片请求走 token 路径：渠道价是 token 模式时，两个网关都把图片请求当普通 token 请求，倍率用 token 倍率，
// 额外倍率乘在它上面（不是图片倍率）。Quote 的 Cost 同样。
func TestQuote_ImageRequestOnTokenPathMultipliesTheTokenMultiplier(t *testing.T) {
	ctx := context.Background()
	usage := UsageTokens{InputTokens: 1000, OutputTokens: 100}

	t.Run("openai", func(t *testing.T) {
		group := quoteTestGroupOn(PlatformOpenAI, 1.3)
		group.ImageRateIndependent, group.ImageRateMultiplier = true, 2.7
		policy := qxCustomWithExtra(PlatformOpenAI, []StoredMatrixCell{mpCustom("gpt-image-2", 1e-6)}, 2)
		f := qxFixture(group, policy)
		key := qxKey(group)
		result := &OpenAIForwardResult{Model: "gpt-image-2", ImageCount: 2, ImageSize: "1K"}

		want, err := qxOpenAIGateway(f, policy).calculateOpenAIRecordUsageCost(ctx, result, key, []string{"gpt-image-2"},
			1.3, resolveImageRateMultiplier(key, 1.3), usage, "", quoteTestAtLow)
		require.NoError(t, err)
		require.Equal(t, string(BillingModeToken), want.BillingMode, "a token-mode channel price makes an image request a token request")
		require.Equal(t, 2.0, want.extraMultiplier)

		quote := qxQuote(t, f, "gpt-image-2")
		require.Equal(t, QuoteSourceChannel, quote.Source)
		got, err := quote.Cost(ctx, QuoteUsage{Tokens: usage, ImageCount: 2, ImageSize: "1K"})
		require.NoError(t, err)
		require.Equal(t, *want, *got)

		// 同一份价格、额外倍率为 1：费用恰好差 extra 倍，说明乘的是 token 倍率 1.3，不是图片倍率 2.7。
		noExtra := qxCustomWithExtra(PlatformOpenAI, []StoredMatrixCell{mpCustom("gpt-image-2", 1e-6)}, 1)
		base, err := qxOpenAIGateway(f, noExtra).calculateOpenAIRecordUsageCost(ctx, result, key, []string{"gpt-image-2"},
			1.3, resolveImageRateMultiplier(key, 1.3), usage, "", quoteTestAtLow)
		require.NoError(t, err)
		require.InDelta(t, base.ActualCost*2, got.ActualCost, 1e-12)
		require.InDelta(t, base.TotalCost, got.TotalCost, 1e-15)
	})

	t.Run("anthropic gateway family", func(t *testing.T) {
		group := quoteTestGroupOn(PlatformAnthropic, 1.3)
		group.ImageRateIndependent, group.ImageRateMultiplier = true, 2.7
		policy := qxCustomWithExtra(PlatformAnthropic, []StoredMatrixCell{mpCustom("claude-sonnet-4", 3e-6)}, 2)
		f := qxFixture(group, policy)
		key := qxKey(group)
		result := &ForwardResult{
			Model: "claude-sonnet-4", ImageCount: 2, ImageSize: "1K",
			Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 100},
		}

		want := qxGateway(f, policy).calculateRecordUsageCost(ctx, result, key, "claude-sonnet-4",
			1.3, resolveImageRateMultiplier(key, 1.3), &recordUsageOpts{}, quoteTestAtLow)
		require.Equal(t, string(BillingModeToken), want.BillingMode)
		require.Equal(t, 2.0, want.extraMultiplier)

		quote := qxQuote(t, f, "claude-sonnet-4")
		got, err := quote.Cost(ctx, QuoteUsage{Tokens: usage, ImageCount: 2, ImageSize: "1K"})
		require.NoError(t, err)
		require.Equal(t, *want, *got)
	})
}

// 用户专属倍率替换分组倍率，额外倍率再乘在它上面（Q1：final = (专属倍率 ?? 分组倍率) x 额外倍率）。
func TestQuote_ExtraMultiplierStacksOnTheUserRate(t *testing.T) {
	userRate := 0.8
	group := quoteTestGroupOn(PlatformOpenAI, 1.5)
	policy := qxMatrix(PlatformOpenAI, GroupStateSnapshot{Cells: []StoredMatrixCell{mpExtra("gpt-5.4", 2)}})
	f := newQuoteTestFixture(nil, nil, []*Group{group}, &userRate)
	f.resolver.policyOverride = policy

	quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "gpt-5.4", GroupID: quoteTestGroupID, UserID: quoteTestUserID, At: quoteTestAtLow})
	require.NoError(t, err)
	require.Equal(t, 1.5, quote.GroupMultiplier)
	require.NotNil(t, quote.UserMultiplier)
	require.Equal(t, userRate, *quote.UserMultiplier, "the user multiplier is shown before the extra multiplier")
	require.Equal(t, userRate*2, quote.EffectiveMultiplier)
	require.Equal(t, 2.0, *quote.ExtraMultiplier)
}

// 稳定优先兜底：额外倍率按服务组（回退到的那个分组）取，不是 home 组。
func TestQuote_ExtraMultiplierIsReadFromTheServedGroup(t *testing.T) {
	const servedID = int64(778)
	home := quoteTestGroupOn(PlatformOpenAI, 1)
	served := &Group{ID: servedID, Platform: PlatformOpenAI, RateMultiplier: 2}
	policy, _ := newMPForTest(newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{
		quoteTestGroupID: {Cells: []StoredMatrixCell{mpExtra("gpt-5.4", 7)}},
		servedID:         {Cells: []StoredMatrixCell{mpExtra("gpt-5.4", 3)}},
	}), nil)
	f := newQuoteTestFixture(nil, nil, []*Group{home, served}, nil)
	f.resolver.policyOverride = policy

	quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "gpt-5.4", GroupID: quoteTestGroupID, ServedGroupID: servedID, At: quoteTestAtLow})
	require.NoError(t, err)
	require.Equal(t, servedID, quote.ServedGroupID)
	require.Equal(t, 3.0, *quote.ExtraMultiplier)
	require.Equal(t, 2*3.0, quote.EffectiveMultiplier)
}

// 额外倍率的生效窗口按报价时点（QuoteRequest.At）判断，与网关用请求级 pricingAt 一致。
func TestQuote_ExtraMultiplierWindowFollowsTheQuoteTime(t *testing.T) {
	from, to := quoteTestAtLow.Add(-time.Hour), quoteTestAtLow.Add(time.Hour)
	group := quoteTestGroupOn(PlatformOpenAI, 1)
	policy := qxMatrix(PlatformOpenAI, GroupStateSnapshot{Cells: []StoredMatrixCell{mpWindow(mpExtra("gpt-5.4", 2), &from, &to)}})
	f := qxFixture(group, policy)

	for name, tc := range map[string]struct {
		at   time.Time
		want bool
	}{
		"inside the window":  {quoteTestAtLow, true},
		"before the window":  {from.Add(-time.Minute), false},
		"at the window end":  {to, false},
		"long after (today)": {to.Add(24 * time.Hour), false},
	} {
		t.Run(name, func(t *testing.T) {
			quote, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "gpt-5.4", GroupID: quoteTestGroupID, At: tc.at})
			require.NoError(t, err)
			if tc.want {
				require.NotNil(t, quote.ExtraMultiplier)
				require.Equal(t, 2.0, *quote.ExtraMultiplier)
			} else {
				require.Nil(t, quote.ExtraMultiplier)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// legacy 零变化
// ---------------------------------------------------------------------------

// 三条路径得到逐字段相同的报价与费用：
//   - 默认装配：渠道服务现取 legacyPolicy（生产的样子）；
//   - 显式注入 legacyPolicy；
//   - 没有渠道、没有单元格的 v2 分组（空快照的 matrixPolicy）。
//
// 额外倍率恒为 1：报价里没有 extra_multiplier，倍率与分组倍率逐位相同，费用没有标记。
// 前两条用同一份渠道配置；第三条没有渠道，所以与「没有渠道的默认装配」比较。
func TestQuote_LegacyAndEmptyMatrixQuotesAreIdentical(t *testing.T) {
	ctx := context.Background()
	rate := 1.3
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 200}
	models := []string{"gpt-5.5", "gpt-5.4-high", "gpt-5.6-luna", "no-such-model-xyz", "glm-4.6"}

	t.Run("with a channel: default wiring vs explicit legacyPolicy", func(t *testing.T) {
		channels := quoteTestChannel(tokenPricingForModels([]string{"gpt-5.5"}, 0.7))
		def := newQuoteTestFixture(nil, channels, []*Group{quoteTestGroup(rate)}, nil)
		explicit := newQuoteTestFixture(nil, channels, []*Group{quoteTestGroup(rate)}, nil)
		explicit.resolver.policyOverride = newLegacyGroupPolicy(explicit.resolver.channelService)
		require.NotNil(t, explicit.resolver.policyOverride)

		for _, model := range models {
			a, b := qxQuote(t, def, model), qxQuote(t, explicit, model)
			require.Equal(t, qxQuoteJSON(t, a), qxQuoteJSON(t, b), model)
			qxRequireLegacyShape(t, a, rate)
			ca, errA := a.Cost(ctx, QuoteUsage{Tokens: tokens})
			cb, errB := b.Cost(ctx, QuoteUsage{Tokens: tokens})
			require.Equal(t, errA == nil, errB == nil, model)
			if errA == nil {
				require.Equal(t, *ca, *cb, model)
				require.Zero(t, ca.extraMultiplier)
			}
		}
	})

	t.Run("without a channel: default wiring vs empty v2 snapshot", func(t *testing.T) {
		def := newQuoteTestFixture(nil, nil, []*Group{quoteTestGroup(rate)}, nil)
		v2 := qxFixture(quoteTestGroup(rate), qxMatrix(PlatformOpenAI, GroupStateSnapshot{}))
		for _, model := range models {
			a, b := qxQuote(t, def, model), qxQuote(t, v2, model)
			require.Equal(t, qxQuoteJSON(t, a), qxQuoteJSON(t, b), model)
			qxRequireLegacyShape(t, b, rate)
			ca, errA := a.Cost(ctx, QuoteUsage{Tokens: tokens})
			cb, errB := b.Cost(ctx, QuoteUsage{Tokens: tokens})
			require.Equal(t, errA == nil, errB == nil, model)
			if errA == nil {
				require.Equal(t, *ca, *cb, model)
			}
		}
	})

	t.Run("an independent oracle: Resolve + CalculateCostUnified as the gateway calls them", func(t *testing.T) {
		f := newQuoteTestFixture(nil, quoteTestChannel(tokenPricingForModels([]string{"gpt-5.5"}, 0.7)), []*Group{quoteTestGroup(rate)}, nil)
		for _, model := range []string{"gpt-5.5", "gpt-5.6-luna"} {
			quote := qxQuote(t, f, model)
			got, err := quote.Cost(ctx, QuoteUsage{Tokens: tokens})
			require.NoError(t, err)
			require.Equal(t, *f.referenceTokenCost(t, model, rate, tokens, "", quoteTestAtLow), *got, model)
			require.Equal(t, rate, quote.EffectiveMultiplier, "x * 1 is x, bit for bit")
		}
	})
}

func qxRequireLegacyShape(t *testing.T, q *Quote, rate float64) {
	t.Helper()
	require.Nil(t, q.ExtraMultiplier, q.Model)
	require.Equal(t, rate, q.EffectiveMultiplier, q.Model)
	require.Equal(t, rate, q.ImageMultiplier, q.Model)
	require.Equal(t, QuoteAccess{OK: true}, q.Access, q.Model)
}

// ---------------------------------------------------------------------------
// Quote.Access
// ---------------------------------------------------------------------------

func qxRestrictedChannel(source string, models ...string) []Channel {
	pricings := make([]ChannelModelPricing, 0, len(models))
	for _, m := range models {
		pricings = append(pricings, tokenPricingForModels([]string{m}, 0.7))
	}
	return []Channel{{
		ID: 1, Name: "restricted", Status: StatusActive,
		BillingModelSource: source, RestrictModels: true,
		ModelPricing: pricings, GroupIDs: []int64{quoteTestGroupID},
	}}
}

// legacy 分组：渠道限制模型时，不在定价列表里的模型 Access 为 not_in_allowlist，与网关调度前的检查一致。
// 计费来源是 upstream 的限制型分组，计费模型是上游模型，走 UpstreamAccess，结果相同。
func TestQuoteAccess_LegacyRestrictedChannel(t *testing.T) {
	for _, source := range []string{BillingModelSourceChannelMapped, BillingModelSourceRequested, BillingModelSourceUpstream} {
		t.Run(source, func(t *testing.T) {
			f := newQuoteTestFixture(nil, qxRestrictedChannel(source, "gpt-5.5"), []*Group{quoteTestGroup(1)}, nil)
			require.Equal(t, QuoteAccess{OK: true}, qxQuote(t, f, "gpt-5.5").Access)
			require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}, qxQuote(t, f, "gpt-5.4").Access)
			// 名字的写法不影响结论：与网关的 checkRestricted 一致（它的查找自带去空白、转小写）。
			for _, variant := range []string{" gpt-5.5", "gpt-5.5 ", "GPT-5.5", "\tGPT-5.5\n"} {
				require.Equal(t, QuoteAccess{OK: true}, qxQuote(t, f, variant).Access, "%q", variant)
			}
		})
	}
}

// 没有限制的渠道、没有渠道、没有策略：放行。
func TestQuoteAccess_NoRestrictionAdmitsEverything(t *testing.T) {
	open := qxRestrictedChannel(BillingModelSourceChannelMapped, "gpt-5.5")
	open[0].RestrictModels = false
	for name, f := range map[string]*quoteTestFixture{
		"channel without restriction": newQuoteTestFixture(nil, open, []*Group{quoteTestGroup(1)}, nil),
		"no channel":                  newQuoteTestFixture(nil, nil, []*Group{quoteTestGroup(1)}, nil),
	} {
		require.Equal(t, QuoteAccess{OK: true}, qxQuote(t, f, "gpt-5.4").Access, name)
	}

	noPolicy := newQuoteTestFixture(nil, nil, []*Group{quoteTestGroup(1)}, nil)
	noPolicy.resolver.channelService, noPolicy.resolver.policyOverride = nil, nil
	require.Nil(t, noPolicy.resolver.groupPolicy())
	quote := qxQuote(t, noPolicy, "gpt-5.4")
	require.Equal(t, QuoteAccess{OK: true}, quote.Access)
	require.Nil(t, quote.ExtraMultiplier)
	require.True(t, quote.Priced, "the price chain does not depend on the policy")
}

func TestQuoteAccess_MatrixGroups(t *testing.T) {
	allowlist := func(source *string) *StoredGroupConfig {
		return mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) {
			c.AccessMode = MatrixAccessAllowlist
			c.BillingModelSource = source
		})
	}
	cases := []struct {
		name   string
		snap   GroupStateSnapshot
		model  string
		wantOK bool
		reason string
	}{
		{"open group, no cell", GroupStateSnapshot{}, "gpt-5.4", true, ""},
		{"open group, closed cell", GroupStateSnapshot{Cells: []StoredMatrixCell{mpClosed(mpInherit("gpt-5.4"))}}, "gpt-5.4", false, QuoteAccessReasonClosedInGroup},
		{"open group, closed cell, padded and upper-case name", GroupStateSnapshot{Cells: []StoredMatrixCell{mpClosed(mpInherit("gpt-5.4"))}}, "  GPT-5.4 ", false, QuoteAccessReasonClosedInGroup},
		{"open group, closed base does not close the variant", GroupStateSnapshot{Cells: []StoredMatrixCell{mpClosed(mpInherit("gpt-5.4"))}}, "gpt-5.4-high", true, ""},
		{"allowlist group, member", GroupStateSnapshot{Config: allowlist(nil), Cells: []StoredMatrixCell{mpInherit("gpt-5.4")}}, "gpt-5.4", true, ""},
		{"allowlist group, not a member", GroupStateSnapshot{Config: allowlist(nil), Cells: []StoredMatrixCell{mpInherit("gpt-5.4")}}, "gpt-5.5", false, QuoteAccessReasonNotInAllowlist},
		{"allowlist group, closed member", GroupStateSnapshot{Config: allowlist(nil), Cells: []StoredMatrixCell{mpClosed(mpInherit("gpt-5.4"))}}, "gpt-5.4", false, QuoteAccessReasonClosedInGroup},
		// 计费来源是 upstream 的白名单分组：计费模型是上游模型，网关做的是 UpstreamAccess，关闭的单元格不是白名单成员。
		{"allowlist + upstream billing, member", GroupStateSnapshot{Config: allowlist(mpS(BillingModelSourceUpstream)), Cells: []StoredMatrixCell{mpInherit("gpt-5.4")}}, "gpt-5.4", true, ""},
		{"allowlist + upstream billing, closed member", GroupStateSnapshot{Config: allowlist(mpS(BillingModelSourceUpstream)), Cells: []StoredMatrixCell{mpClosed(mpInherit("gpt-5.4"))}}, "gpt-5.4", false, QuoteAccessReasonNotInAllowlist},
		{"allowlist + channel_mapped billing, closed member", GroupStateSnapshot{Config: allowlist(mpS(BillingModelSourceChannelMapped)), Cells: []StoredMatrixCell{mpClosed(mpInherit("gpt-5.4"))}}, "gpt-5.4", false, QuoteAccessReasonClosedInGroup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := qxFixture(quoteTestGroup(1), qxMatrix(PlatformOpenAI, tc.snap))
			got := qxQuote(t, f, tc.model).Access
			require.Equal(t, tc.wantOK, got.OK)
			require.Equal(t, tc.reason, got.Reason)
		})
	}
}

// ---- 目录状态 ----

type qxCatalogSpy struct {
	inner    quoteCatalogReader
	calls    int
	platform string
	model    string
}

func (c *qxCatalogSpy) Resolve(ctx context.Context, platform, model string) (*ModelCatalogEntry, error) {
	c.calls++
	c.platform, c.model = platform, model
	return c.inner.Resolve(ctx, platform, model)
}

type qxCatalogDown struct{ calls int }

func (c *qxCatalogDown) Resolve(context.Context, string, string) (*ModelCatalogEntry, error) {
	c.calls++
	return nil, errors.New("catalog unavailable")
}

func qxCatalog() *ModelCatalogService {
	draft := mxCatalogEntry(PlatformOpenAI, "gpt-5.4")
	draft.Status = ModelCatalogDraft
	retired := mxCatalogEntry(PlatformOpenAI, "gpt-5.5")
	retired.Status = ModelCatalogRetired
	// 别名：用别名报价也能识别出状态。
	aliased := mxCatalogEntry(PlatformOpenAI, "gpt-5.6-luna", "luna-draft")
	aliased.Status = ModelCatalogDraft
	// 别的平台的同名草稿不能串到 openai 分组里。
	otherPlatform := mxCatalogEntry(PlatformAnthropic, "gpt-5.4-mini")
	otherPlatform.Status = ModelCatalogDraft
	return NewModelCatalogService(&mxCatalogRepo{entries: []ModelCatalogEntry{
		draft, retired, aliased, otherPlatform, mxCatalogEntry(PlatformOpenAI, "gpt-5.2"),
	}})
}

func TestQuoteAccess_CatalogStatusIsLayeredOnTopOfGroupAccess(t *testing.T) {
	v2 := func(cells ...StoredMatrixCell) GroupPolicy {
		return qxMatrix(PlatformOpenAI, GroupStateSnapshot{Config: mpStoredConfig(PricingStageV2, nil), Cells: cells})
	}
	build := func(policy GroupPolicy) (*quoteTestFixture, *qxCatalogSpy) {
		f := qxFixture(quoteTestGroup(1), policy)
		spy := &qxCatalogSpy{inner: qxCatalog()}
		f.quoter.SetModelCatalog(spy)
		return f, spy
	}

	t.Run("draft, retired, active, unregistered", func(t *testing.T) {
		f, spy := build(v2())
		require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonCatalogDraft}, qxQuote(t, f, "gpt-5.4").Access)
		require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonCatalogRetired}, qxQuote(t, f, "gpt-5.5").Access)
		require.Equal(t, QuoteAccess{OK: true}, qxQuote(t, f, "gpt-5.2").Access, "active")
		require.Equal(t, QuoteAccess{OK: true}, qxQuote(t, f, "gpt-5.3-codex").Access, "unregistered models count as active")
		require.Equal(t, QuoteAccess{OK: true}, qxQuote(t, f, "gpt-5.4-mini").Access, "a draft on another platform does not leak in")
		require.Equal(t, PlatformOpenAI, spy.platform, "the catalog is read with the pricing group's platform")
	})

	t.Run("names are normalized before the lookup, aliases resolve", func(t *testing.T) {
		f, spy := build(v2())
		for _, variant := range []string{"gpt-5.4", " gpt-5.4", "GPT-5.4 ", "\tGPT-5.4\n"} {
			require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonCatalogDraft}, qxQuote(t, f, variant).Access, "%q", variant)
			require.Equal(t, "gpt-5.4", spy.model, "%q", variant)
		}
		require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonCatalogDraft}, qxQuote(t, f, " LUNA-Draft ").Access)
	})

	t.Run("shadow stage also applies the catalog", func(t *testing.T) {
		shadow := qxMatrix(PlatformOpenAI, GroupStateSnapshot{Config: mpStoredConfig(PricingStageShadow, nil)})
		f, _ := build(shadow)
		require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonCatalogDraft}, qxQuote(t, f, "gpt-5.4").Access)
	})

	t.Run("group access comes first: a closed cell is reported as closed, the catalog is not asked", func(t *testing.T) {
		f, spy := build(v2(mpClosed(mpInherit("gpt-5.4"))))
		require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonClosedInGroup}, qxQuote(t, f, "gpt-5.4").Access)
		require.Zero(t, spy.calls)
	})

	t.Run("legacy stage ignores the catalog: the gateway does not read it either", func(t *testing.T) {
		f, spy := build(qxMatrix(PlatformOpenAI, GroupStateSnapshot{})) // 没有配置行：阶段是 legacy
		require.Equal(t, QuoteAccess{OK: true}, qxQuote(t, f, "gpt-5.4").Access)

		legacy := newQuoteTestFixture(nil, nil, []*Group{quoteTestGroup(1)}, nil)
		legacySpy := &qxCatalogSpy{inner: qxCatalog()}
		legacy.quoter.SetModelCatalog(legacySpy)
		require.Equal(t, QuoteAccess{OK: true}, qxQuote(t, legacy, "gpt-5.4").Access)
		require.Zero(t, spy.calls+legacySpy.calls)
	})

	t.Run("upstream-billed allowlist: the catalog does not stop an upstream model", func(t *testing.T) {
		policy := qxMatrix(PlatformOpenAI, GroupStateSnapshot{
			Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) {
				c.AccessMode = MatrixAccessAllowlist
				c.BillingModelSource = mpS(BillingModelSourceUpstream)
			}),
			Cells: []StoredMatrixCell{mpInherit("gpt-5.4")},
		})
		f, spy := build(policy)
		require.Equal(t, QuoteAccess{OK: true}, qxQuote(t, f, "gpt-5.4").Access, "gpt-5.4 is a draft in the catalog")
		require.Zero(t, spy.calls)
	})

	t.Run("no catalog attached: group access only", func(t *testing.T) {
		f := qxFixture(quoteTestGroup(1), v2())
		require.Equal(t, QuoteAccess{OK: true}, qxQuote(t, f, "gpt-5.4").Access)
		f.quoter.SetModelCatalog(nil)
		require.Equal(t, QuoteAccess{OK: true}, qxQuote(t, f, "gpt-5.4").Access)
	})

	t.Run("a catalog read failure admits the model", func(t *testing.T) {
		f := qxFixture(quoteTestGroup(1), v2())
		down := &qxCatalogDown{}
		f.quoter.SetModelCatalog(down)
		quote := qxQuote(t, f, "gpt-5.4")
		require.Equal(t, QuoteAccess{OK: true}, quote.Access)
		require.True(t, quote.Priced, "the quote itself is unaffected")
		require.Equal(t, 1, down.calls)
	})
}

// ---------------------------------------------------------------------------
// 入口名字规范化（设计 3.2、R2-D）
// ---------------------------------------------------------------------------

func TestNormalizePricingEntryModel(t *testing.T) {
	cases := map[string]string{
		"gpt-5.4":            "gpt-5.4",
		"  GPT-5.4  ":        "gpt-5.4",
		"\tGPT-5.6-Luna\r\n": "gpt-5.6-luna",
		"Claude-Opus-4.5":    "claude-opus-4-5",
		" claude-sonnet-4.5": "claude-sonnet-4-5",
		"Gemini-2.5-Pro":     "gemini-2.5-pro", // 只有 claude-* 做点号归一化
		"   ":                "",
		"":                   "",
	}
	for in, want := range cases {
		require.Equal(t, want, normalizePricingEntryModel(in), "%q", in)
		require.Equal(t, want, NormalizeCatalogModelKey(in), "the catalog entry uses the same function: %q", in)
	}
}

// 同一个模型的不同写法（前后空白、制表符与换行、大小写）得到逐字段相同的报价与费用，
// 不依赖调用方已经归一过。覆盖 legacy 的渠道限制，以及 v2 的白名单、额外倍率与目录状态。
func TestQuote_NameVariantsAreEquivalentAtTheEntry(t *testing.T) {
	ctx := context.Background()
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 100}
	variants := []string{" gpt-5.4", "gpt-5.4 ", "\tgpt-5.4\n", "GPT-5.4", "  GPT-5.4  "}

	build := map[string]func() *quoteTestFixture{
		"legacy restricted channel": func() *quoteTestFixture {
			return newQuoteTestFixture(nil, qxRestrictedChannel(BillingModelSourceChannelMapped, "gpt-5.4"), []*Group{quoteTestGroup(1.5)}, nil)
		},
		"v2 allowlist with an extra cell": func() *quoteTestFixture {
			policy := qxMatrix(PlatformOpenAI, GroupStateSnapshot{
				Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) { c.AccessMode = MatrixAccessAllowlist }),
				Cells:  []StoredMatrixCell{mpExtra("gpt-5.4", 2)},
			})
			return qxFixture(quoteTestGroup(1.5), policy)
		},
		"v2 with the catalog attached": func() *quoteTestFixture {
			f := qxFixture(quoteTestGroup(1.5), qxMatrix(PlatformOpenAI, GroupStateSnapshot{
				Config: mpStoredConfig(PricingStageV2, nil),
				Cells:  []StoredMatrixCell{mpExtra("gpt-5.4", 2)},
			}))
			f.quoter.SetModelCatalog(qxCatalog()) // gpt-5.4 在目录里是 draft
			return f
		},
	}
	for name, newFixture := range build {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			canonical := qxQuote(t, f, "gpt-5.4")
			wantCost, wantErr := canonical.Cost(ctx, QuoteUsage{Tokens: tokens})
			require.NoError(t, wantErr)
			for _, variant := range variants {
				got := qxQuote(t, f, variant)
				require.Equal(t, qxQuoteJSON(t, canonical), qxQuoteJSON(t, got), "%q", variant)
				cost, err := got.Cost(ctx, QuoteUsage{Tokens: tokens})
				require.NoError(t, err)
				require.Equal(t, *wantCost, *cost, "%q", variant)
			}
		})
	}

	// claude 名的点号：策略与目录都按同一条规则把点号写成连字符，字面写法不影响额外倍率与准入。
	t.Run("claude dotted names", func(t *testing.T) {
		group := quoteTestGroupOn(PlatformAnthropic, 1.3)
		policy := qxMatrix(PlatformAnthropic, GroupStateSnapshot{
			Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) { c.AccessMode = MatrixAccessAllowlist }),
			Cells:  []StoredMatrixCell{mpExtra("claude-sonnet-4-5", 2)},
		})
		f := qxFixture(group, policy)
		for _, model := range []string{"claude-sonnet-4-5", "Claude-Sonnet-4.5", " claude-sonnet-4.5 "} {
			quote := qxQuote(t, f, model)
			require.Equal(t, QuoteAccess{OK: true}, quote.Access, "%q", model)
			require.NotNil(t, quote.ExtraMultiplier, "%q", model)
			require.Equal(t, 2.0, *quote.ExtraMultiplier, "%q", model)
		}
	})
}

// ModelAccess 的查找不另加一层 trim：legacyPolicy 与 matrixPolicy 对各种写法给出相同的结论，
// 都等于 checkRestricted 的行为（它的缓存查找自带去空白、转小写、claude 名点号写成连字符，
// 不做 codex 归一化，所以变体名不会借基名放行）。
func TestModelAccess_NameHandlingMatchesCheckRestricted(t *testing.T) {
	ctx := context.Background()
	channels := qxRestrictedChannel(BillingModelSourceChannelMapped, "gpt-5.4", "claude-sonnet-4.5")
	platforms := map[int64]string{quoteTestGroupID: PlatformOpenAI}
	cs := &ChannelService{}
	cs.cache.Store(populateChannelCache(channels, platforms))
	legacy := newLegacyGroupPolicy(cs)

	matrix := qxMatrix(PlatformOpenAI, GroupStateSnapshot{
		Config: mpStoredConfig(PricingStageV2, func(c *MatrixGroupConfig) { c.AccessMode = MatrixAccessAllowlist }),
		Cells:  []StoredMatrixCell{mpInherit("gpt-5.4"), mpInherit("claude-sonnet-4.5")},
	})

	for _, model := range []string{
		"gpt-5.4", " gpt-5.4 ", "\tGPT-5.4\n", "GPT-5.4",
		"gpt-5.4-high", "gpt-5.5", "",
		"claude-sonnet-4.5", "claude-sonnet-4-5", " Claude-Sonnet-4.5 ",
	} {
		l, m := legacy.ModelAccess(ctx, quoteTestGroupID, model), matrix.ModelAccess(ctx, quoteTestGroupID, model)
		require.Equal(t, l.OK, m.OK, "%q", model)
		require.Equal(t, l.Reason, m.Reason, "%q", model)
	}
	require.False(t, legacy.ModelAccess(ctx, quoteTestGroupID, "gpt-5.4-high").OK, "no codex normalization on the admission path")
	require.True(t, legacy.ModelAccess(ctx, quoteTestGroupID, " gpt-5.4 ").OK, "the lookup itself ignores surrounding blanks")
}
