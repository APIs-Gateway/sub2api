//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Anthropic 网关（GatewayService）对「无渠道价」DeepSeek 的扣费：
// 与 OpenAI 网关一致，走 CalculateCostUnified 并传 PricingAt，默认价卡在工作日高峰（UTC 01–04、06–10）乘 2。
// 此前这条分支走 CalculateCost，不带峰时倍率，高峰时段永远按低谷价收费。
//
// 价格来源（billing_service.go deepseekFlashOffPeak*，2026-09-10 官方口径，$/token）：
//
//	deepseek-v4-flash：input 1.5e-7、output 6e-7、cache read 3e-9；没有 cache write 价。
//	2026-09-14 04:00 UTC 起 deepseek-v4-pro 同样按 Flash 价计费；之前按 Pro 价（6.6e-7 / 1.98e-6 / 2.2e-8）。
//
// 所有计费时点都显式传入，不依赖当前时间。2026-10-05 是周一，2026-10-10 是周六，2026-10-11 是周日。

type gatewayDeepSeekSlot struct {
	name string
	at   time.Time
	mult float64 // 期望的峰时倍率
}

func gatewayDeepSeekSlots() []gatewayDeepSeekSlot {
	utc := func(day, hour, minute int) time.Time {
		return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC)
	}
	return []gatewayDeepSeekSlot{
		{"weekday_00_59_off_peak", utc(5, 0, 59), 1},
		{"weekday_01_00_morning_peak_start", utc(5, 1, 0), 2},
		{"weekday_03_59_morning_peak_last_minute", utc(5, 3, 59), 2},
		{"weekday_04_00_morning_peak_end", utc(5, 4, 0), 1},
		{"weekday_06_00_afternoon_peak_start", utc(5, 6, 0), 2},
		{"weekday_09_59_afternoon_peak_last_minute", utc(5, 9, 59), 2},
		{"weekday_10_00_afternoon_peak_end", utc(5, 10, 0), 1},
		{"weekday_12_00_off_peak", utc(5, 12, 0), 1},
		// 周六/周日北京时间全天低谷，即使 UTC 小时落在高峰窗口。
		{"saturday_02_00_utc", utc(10, 2, 0), 1},
		{"sunday_07_00_utc", utc(11, 7, 0), 1},
	}
}

// newGatewayDeepSeekBillingService 按生产装配方式拼出带渠道服务与定价解析器的 GatewayService。
// channels 为空表示分组没有任何渠道定价。
func newGatewayDeepSeekBillingService(channels []Channel, groupID int64, platform string) *GatewayService {
	billing := newTestBillingService()
	cs := &ChannelService{}
	cs.cache.Store(populateChannelCache(channels, map[int64]string{groupID: platform}))
	return &GatewayService{
		billingService: billing,
		channelService: cs,
		resolver:       NewModelPricingResolver(cs, billing),
	}
}

func gatewayDeepSeekAPIKey(groupID int64, platform string) *APIKey {
	return &APIKey{ID: 1, GroupID: &groupID, Group: &Group{ID: groupID, Platform: platform, RateMultiplier: 1}}
}

// requireSameCostAmounts 逐位比较两份费用明细的所有金额字段（不比较 BillingMode：CalculateCost 不填，Unified 填 "token"）。
func requireSameCostAmounts(t *testing.T, want, got *CostBreakdown) {
	t.Helper()
	require.NotNil(t, want)
	require.NotNil(t, got)
	require.Equal(t, want.InputCost, got.InputCost)
	require.Equal(t, want.ImageInputCost, got.ImageInputCost)
	require.Equal(t, want.OutputCost, got.OutputCost)
	require.Equal(t, want.ImageOutputCost, got.ImageOutputCost)
	require.Equal(t, want.CacheCreationCost, got.CacheCreationCost)
	require.Equal(t, want.CacheReadCost, got.CacheReadCost)
	require.Equal(t, want.TotalCost, got.TotalCost)
	require.Equal(t, want.ActualCost, got.ActualCost)
}

// 用户扣费：无渠道价的 DeepSeek 在高峰时段乘 2，低谷与周末乘 1，倍率之后再乘分组倍率。
func TestGatewayCalculateRecordUsageCost_DeepSeekNoChannelPricePeakMultiplier(t *testing.T) {
	const groupID = int64(1701)
	svc := newGatewayDeepSeekBillingService(nil, groupID, PlatformAnthropic)
	apiKey := gatewayDeepSeekAPIKey(groupID, PlatformAnthropic)
	usage := ClaudeUsage{InputTokens: 1000, OutputTokens: 500, CacheReadInputTokens: 2000}
	// 低谷成本：1000×1.5e-7 + 500×6e-7 + 2000×3e-9 = 1.5e-4 + 3e-4 + 6e-6 = 4.56e-4
	const offPeakTotal = 4.56e-4
	const groupRate = 1.5

	for _, model := range []string{"deepseek-v4-flash", "DeepSeek-V4-Flash", "deepseek-v4-pro"} {
		for _, slot := range gatewayDeepSeekSlots() {
			t.Run(model+"/"+slot.name, func(t *testing.T) {
				cost := svc.calculateRecordUsageCost(context.Background(),
					&ForwardResult{Model: model, Usage: usage}, apiKey, model, groupRate, 1.0, &recordUsageOpts{}, slot.at)
				require.NotNil(t, cost)
				// deepseek-v4-pro 在 2026-09-14 之后也按 Flash 价计费，所以三个模型名的低谷价相同。
				require.InDelta(t, offPeakTotal*slot.mult, cost.TotalCost, 1e-12)
				require.InDelta(t, offPeakTotal*slot.mult*groupRate, cost.ActualCost, 1e-12)
				require.Equal(t, string(BillingModeToken), cost.BillingMode)
			})
		}
	}
}

// 非高峰时段（含周末）DeepSeek 的扣费与改动前的 CalculateCost 逐位一致。
// 计费时点与 deepseekNowFunc 取同一个固定时刻，对应生产里 PricingAt 与「当前时刻」相同的情形。
func TestGatewayCalculateRecordUsageCost_DeepSeekOffPeakIdenticalToLegacyCalculateCost(t *testing.T) {
	const groupID = int64(1702)
	svc := newGatewayDeepSeekBillingService(nil, groupID, PlatformAnthropic)
	apiKey := gatewayDeepSeekAPIKey(groupID, PlatformAnthropic)
	usages := []ClaudeUsage{
		{InputTokens: 1000, OutputTokens: 500, CacheReadInputTokens: 2000},
		{InputTokens: 12345, OutputTokens: 6789, CacheReadInputTokens: 54321, CacheCreationInputTokens: 777},
		{InputTokens: 1_000_000, OutputTokens: 1_000_000},
		{OutputTokens: 42},
	}
	offPeak := []time.Time{
		time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), // 周一低谷
		time.Date(2026, 10, 5, 4, 0, 0, 0, time.UTC),  // 高峰窗口刚结束（半开区间）
		time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC), // 周六
		time.Date(2026, 10, 11, 7, 0, 0, 0, time.UTC), // 周日
		time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),  // 周二，pro→Flash 切换点之前的低谷：pro 仍按 Pro 价
		time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC),  // 周一，切换点本身（含）：pro 起按 Flash 价，且高峰窗口恰好结束
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), // 周一，切换点之后的低谷
	}
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-chat"} {
		for _, at := range offPeak {
			require.Equal(t, 1.0, deepseekPeakMultiplierAt(at), "本用例只比较非高峰时点：%s", at)
			for i, usage := range usages {
				t.Run(fmt.Sprintf("%s/%s/usage%d", model, at.Format(time.RFC3339), i), func(t *testing.T) {
					withDeepseekNow(t, at)
					tokens := UsageTokens{
						InputTokens:         usage.InputTokens,
						OutputTokens:        usage.OutputTokens,
						CacheCreationTokens: usage.CacheCreationInputTokens,
						CacheReadTokens:     usage.CacheReadInputTokens,
					}
					want, err := svc.billingService.CalculateCost(model, tokens, 1.3)
					require.NoError(t, err)
					got := svc.calculateRecordUsageCost(context.Background(),
						&ForwardResult{Model: model, Usage: usage}, apiKey, model, 1.3, 1.0, &recordUsageOpts{}, at)
					requireSameCostAmounts(t, want, got)
				})
			}
		}
	}
}

// 有渠道价时不叠加峰时倍率：渠道价分支没有改动，高峰与低谷的扣费相同，且等于渠道价。
func TestGatewayCalculateRecordUsageCost_DeepSeekChannelPriceNotScaledByPeak(t *testing.T) {
	const groupID = int64(1703)
	channels := []Channel{{
		ID:       1,
		Name:     "deepseek-channel",
		Status:   StatusActive,
		GroupIDs: []int64{groupID},
		ModelPricing: []ChannelModelPricing{{
			Platform:    PlatformAnthropic,
			Models:      []string{"deepseek-v4-flash"},
			BillingMode: BillingModeToken,
			InputPrice:  testPtrFloat64(5e-7),
			OutputPrice: testPtrFloat64(1e-6),
		}},
	}}
	svc := newGatewayDeepSeekBillingService(channels, groupID, PlatformAnthropic)
	apiKey := gatewayDeepSeekAPIKey(groupID, PlatformAnthropic)
	// 不带缓存读取：缓存读取价没有被渠道覆盖，不在本用例的比较范围内。
	usage := ClaudeUsage{InputTokens: 1000, OutputTokens: 500}
	// 1000×5e-7 + 500×1e-6 = 1e-3
	const channelTotal = 1e-3

	for _, slot := range gatewayDeepSeekSlots() {
		t.Run(slot.name, func(t *testing.T) {
			cost := svc.calculateRecordUsageCost(context.Background(),
				&ForwardResult{Model: "deepseek-v4-flash", Usage: usage}, apiKey, "deepseek-v4-flash", 2.0, 1.0, &recordUsageOpts{}, slot.at)
			require.NotNil(t, cost)
			require.InDelta(t, channelTotal, cost.TotalCost, 1e-12)
			require.InDelta(t, channelTotal*2.0, cost.ActualCost, 1e-12)
		})
	}
}

// 非 DeepSeek 模型不受影响：高峰时段的扣费与 CalculateCost 逐字段一致（包括 BillingMode 仍为空）。
func TestGatewayCalculateRecordUsageCost_NonDeepSeekUnchangedAtPeak(t *testing.T) {
	const groupID = int64(1704)
	svc := newGatewayDeepSeekBillingService(nil, groupID, PlatformAnthropic)
	apiKey := gatewayDeepSeekAPIKey(groupID, PlatformAnthropic)
	usage := ClaudeUsage{InputTokens: 1000, OutputTokens: 500, CacheReadInputTokens: 2000, CacheCreationInputTokens: 300}
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 2000, CacheCreationTokens: 300}
	peak := time.Date(2026, 10, 5, 2, 0, 0, 0, time.UTC)
	require.Equal(t, 2.0, deepseekPeakMultiplierAt(peak), "测试时点必须在 DeepSeek 高峰窗口内")

	for _, model := range []string{"claude-sonnet-4", "claude-opus-5", "glm-4.6", "gpt-5.5", "deepseekcoder"} {
		t.Run(model, func(t *testing.T) {
			want, wantErr := svc.billingService.CalculateCost(model, tokens, 1.3)
			got := svc.calculateRecordUsageCost(context.Background(),
				&ForwardResult{Model: model, Usage: usage}, apiKey, model, 1.3, 1.0, &recordUsageOpts{}, peak)
			if wantErr != nil {
				// 无价模型（deepseekcoder 没有 deepseek- 连字符前缀，不算 DeepSeek）：原有行为是记 0 元。
				require.Equal(t, CostBreakdown{}, *got)
				return
			}
			require.Equal(t, *want, *got)
		})
	}
}

// Gemini 原生入口的长上下文加价（200K 阈值、超出部分 2 倍）与 DeepSeek 峰时倍率叠加；
// 非高峰时与改动前的 CalculateCostWithLongContext 逐位一致。
func TestGatewayCalculateRecordUsageCost_DeepSeekLongContextStacksWithPeak(t *testing.T) {
	const groupID = int64(1705)
	svc := newGatewayDeepSeekBillingService(nil, groupID, PlatformGemini)
	apiKey := gatewayDeepSeekAPIKey(groupID, PlatformGemini)
	opts := &recordUsageOpts{LongContextThreshold: 200000, LongContextMultiplier: 2.0}

	cases := []struct {
		name  string
		usage ClaudeUsage
		// 低谷手算：
		//   inputOnly：输入 300K 在阈值之外 100K。范围内输入 200K×1.5e-7 = 0.03；范围外 100K×1.5e-7×2 = 0.03；输出 300K×6e-7 = 0.18；合计 0.24。
		//   withCache：缓存读取 250K 已超阈值。范围内缓存 200K×3e-9 = 6e-4、输出 1000×6e-7 = 6e-4；
		//              范围外缓存 50K×3e-9×2 = 3e-4、输入 100K×1.5e-7×2 = 0.03；合计 0.0315。
		//   belowThreshold：输入 100K + 缓存读取 50K 未超阈值，不加价。100K×1.5e-7 + 50K×3e-9 + 1000×6e-7 = 0.015 + 1.5e-4 + 6e-4 = 0.01575。
		offPeakTotal float64
	}{
		{"inputOnly", ClaudeUsage{InputTokens: 300_000, OutputTokens: 300_000}, 0.24},
		{"withCache", ClaudeUsage{InputTokens: 100_000, CacheReadInputTokens: 250_000, OutputTokens: 1000}, 0.0315},
		{"belowThreshold", ClaudeUsage{InputTokens: 100_000, CacheReadInputTokens: 50_000, OutputTokens: 1000}, 0.01575},
	}
	for _, tc := range cases {
		for _, slot := range gatewayDeepSeekSlots() {
			t.Run(tc.name+"/"+slot.name, func(t *testing.T) {
				cost := svc.calculateRecordUsageCost(context.Background(),
					&ForwardResult{Model: "deepseek-v4-flash", Usage: tc.usage}, apiKey, "deepseek-v4-flash", 1.0, 1.0, opts, slot.at)
				require.NotNil(t, cost)
				require.InDelta(t, tc.offPeakTotal*slot.mult, cost.ActualCost, 1e-9)

				if slot.mult == 1 {
					withDeepseekNow(t, slot.at)
					tokens := UsageTokens{
						InputTokens:     tc.usage.InputTokens,
						OutputTokens:    tc.usage.OutputTokens,
						CacheReadTokens: tc.usage.CacheReadInputTokens,
					}
					want, err := svc.billingService.CalculateCostWithLongContext("deepseek-v4-flash", tokens, 1.0, 200000, 2.0)
					require.NoError(t, err)
					requireSameCostAmounts(t, want, cost)
				}
			})
		}
	}
}

// 没有解析器或没有分组时退回 CalculateCost（与 OpenAI 网关 calculateOpenAIRecordUsageTokenCost 的保护分支一致），
// 且不会因为空指针 panic。
func TestGatewayCalculateRecordUsageCost_DeepSeekFallsBackWithoutResolverOrGroup(t *testing.T) {
	const groupID = int64(1706)
	peak := time.Date(2026, 10, 5, 2, 0, 0, 0, time.UTC)
	usage := ClaudeUsage{InputTokens: 1000, OutputTokens: 500}
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500}
	withDeepseekNow(t, peak)

	withoutResolver := newGatewayDeepSeekBillingService(nil, groupID, PlatformAnthropic)
	withoutResolver.resolver = nil
	withoutGroup := newGatewayDeepSeekBillingService(nil, groupID, PlatformAnthropic)

	want, err := withoutGroup.billingService.CalculateCost("deepseek-v4-flash", tokens, 1.0)
	require.NoError(t, err)

	got := withoutResolver.calculateRecordUsageCost(context.Background(),
		&ForwardResult{Model: "deepseek-v4-flash", Usage: usage}, gatewayDeepSeekAPIKey(groupID, PlatformAnthropic),
		"deepseek-v4-flash", 1.0, 1.0, &recordUsageOpts{}, peak)
	require.Equal(t, *want, *got)

	got = withoutGroup.calculateRecordUsageCost(context.Background(),
		&ForwardResult{Model: "deepseek-v4-flash", Usage: usage}, &APIKey{ID: 1},
		"deepseek-v4-flash", 1.0, 1.0, &recordUsageOpts{}, peak)
	require.Equal(t, *want, *got)
}

// 用户扣费与账号统计成本在 DeepSeek 高峰时段一致，不倒挂：
// RecordUsage 在同一个请求级时点上同时算用户费用与账号统计成本（渠道没有配价、没有自定义规则时，
// 账号统计成本取模型价卡，经统一计费入口叠加峰时倍率）。分组倍率为 1 时，实扣金额应等于账号统计成本。
func TestGatewayServiceRecordUsage_DeepSeekUserCostMatchesAccountStatsCostAtPeak(t *testing.T) {
	const groupID = int64(1707)
	channels := []Channel{{ID: 1, Name: "no-pricing-channel", Status: StatusActive, GroupIDs: []int64{groupID}}}
	// 低谷成本 4.56e-4（见 TestGatewayCalculateRecordUsageCost_DeepSeekNoChannelPricePeakMultiplier）。
	const offPeakTotal = 4.56e-4

	for _, slot := range gatewayDeepSeekSlots() {
		t.Run(slot.name, func(t *testing.T) {
			withDeepseekNow(t, slot.at)
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
			cs := &ChannelService{}
			cs.cache.Store(populateChannelCache(channels, map[int64]string{groupID: PlatformAnthropic}))
			svc.channelService = cs
			svc.resolver = NewModelPricingResolver(cs, svc.billingService)

			err := svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result: &ForwardResult{
					RequestID:     "gateway_deepseek_peak_" + slot.name,
					Model:         "deepseek-v4-flash",
					UpstreamModel: "deepseek-v4-flash",
					Usage:         ClaudeUsage{InputTokens: 1000, OutputTokens: 500, CacheReadInputTokens: 2000},
					Duration:      time.Second,
				},
				APIKey:  gatewayDeepSeekAPIKey(groupID, PlatformAnthropic),
				User:    &User{ID: 601},
				Account: &Account{ID: 701, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
			})

			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			log := usageRepo.lastLog
			require.InDelta(t, offPeakTotal*slot.mult, log.TotalCost, 1e-12)
			require.InDelta(t, offPeakTotal*slot.mult, log.ActualCost, 1e-12)
			require.InDelta(t, log.ActualCost, userRepo.lastAmount, 1e-12)
			require.NotNil(t, log.AccountStatsCost, "分组有渠道且没有自定义规则时，账号统计成本取模型价卡")
			require.InDelta(t, offPeakTotal*slot.mult, *log.AccountStatsCost, 1e-12)
			require.GreaterOrEqual(t, log.ActualCost+1e-12, *log.AccountStatsCost, "实扣金额不得低于账号统计成本")
		})
	}
}

// ---------------------------------------------------------------------------
// BillingService.CalculateCostWithLongContextUnified：拆分规则与 CalculateCostWithLongContext 相同，只换逐段计价入口
// ---------------------------------------------------------------------------

func TestCalculateCostWithLongContextUnified_MatchesLegacySplit(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)
	// 非 DeepSeek 与非高峰的 DeepSeek：与 CalculateCostWithLongContext 逐位一致。
	offPeak := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	withDeepseekNow(t, offPeak)

	usages := []UsageTokens{
		{InputTokens: 100_000, OutputTokens: 1000},                              // 阈值以内
		{InputTokens: 300_000, OutputTokens: 300_000},                           // 只有输入超阈值
		{InputTokens: 100_000, CacheReadTokens: 250_000, OutputTokens: 1000},    // 缓存超阈值
		{InputTokens: 50_000, CacheReadTokens: 180_000, CacheCreationTokens: 9}, // 缓存未超阈值、合计超阈值
		{InputTokens: 300_000, ImageInputTokens: 250_000, OutputTokens: 10},     // 图片输入跨越拆分点
	}
	for _, model := range []string{"claude-sonnet-4", "deepseek-v4-flash", "deepseek-v4-pro"} {
		for i, tokens := range usages {
			input := CostInput{Ctx: context.Background(), Model: model, Tokens: tokens, RequestCount: 1, RateMultiplier: 1.3, PricingAt: offPeak, Resolver: resolver}
			got, err := bs.CalculateCostWithLongContextUnified(input, 200000, 2.0)
			require.NoError(t, err, "%s #%d", model, i)
			want, err := bs.CalculateCostWithLongContext(model, tokens, 1.3, 200000, 2.0)
			require.NoError(t, err)
			requireSameCostAmounts(t, want, got)
			require.Equal(t, string(BillingModeToken), got.BillingMode, "%s #%d", model, i)
		}
	}
}

func TestCalculateCostWithLongContextUnified_DisabledOrErrorMatchesUnified(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)
	at := time.Date(2026, 10, 5, 2, 0, 0, 0, time.UTC) // 周一高峰
	tokens := UsageTokens{InputTokens: 300_000, OutputTokens: 1000}
	input := CostInput{Ctx: context.Background(), Model: "deepseek-v4-flash", Tokens: tokens, RequestCount: 1, RateMultiplier: 1, PricingAt: at, Resolver: resolver}

	// 阈值或倍率未启用：整次请求按 CalculateCostUnified 计价，高峰仍乘 2。
	plain, err := bs.CalculateCostUnified(input)
	require.NoError(t, err)
	for _, tc := range []struct {
		name      string
		threshold int
		extra     float64
	}{
		{"threshold_zero", 0, 2.0},
		{"extra_not_above_one", 200000, 1.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bs.CalculateCostWithLongContextUnified(input, tc.threshold, tc.extra)
			require.NoError(t, err)
			require.Equal(t, *plain, *got)
		})
	}

	// 无价模型：错误原样返回，不返回部分结果。
	input.Model = "no-such-model-for-long-context"
	got, err := bs.CalculateCostWithLongContextUnified(input, 200000, 2.0)
	require.Error(t, err)
	require.Nil(t, got)
}
