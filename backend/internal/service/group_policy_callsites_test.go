//go:build unit

package service

// W6 PR3：各调用点换成走 GroupPolicy 之后的等价性与接线测试。
//
// 等价性：同一份 ChannelService 数据，调用点（网关包装函数、功能开关、降级守卫、账号成本、价格解析器）
// 的结果与改造前的直接读取（group_policy_test.go 里的 pre* 函数）逐点相同，包括没有渠道服务、
// 没有分组、缓存加载失败这几种边界。
// 接线：注入一个记录调用的 GroupPolicy，同时把 channelService 换成「一读就失败」的版本，
// 证明每个调用点走的是门面、没有绕过它，并且传的参数是对的。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/websearch"
	"github.com/stretchr/testify/require"
)

// gpWrapperSurface 是两个网关共有的渠道读口包装函数。
type gpWrapperSurface interface {
	ResolveChannelMapping(ctx context.Context, groupID int64, model string) ChannelMappingResult
	IsModelRestricted(ctx context.Context, groupID int64, model string) bool
	ResolveChannelMappingAndRestrict(ctx context.Context, groupID *int64, model string) (ChannelMappingResult, bool)
	checkChannelPricingRestriction(ctx context.Context, groupID *int64, requestedModel string) bool
	needsUpstreamChannelRestrictionCheck(ctx context.Context, groupID *int64) bool
}

type gpNamedSurface struct {
	name string
	svc  gpWrapperSurface
}

func gpSurfaces(cs *ChannelService) []gpNamedSurface {
	return []gpNamedSurface{
		{name: "gateway", svc: &GatewayService{channelService: cs}},
		{name: "openai", svc: &OpenAIGatewayService{channelService: cs}},
	}
}

// ---------------------------------------------------------------------------
// 等价性
// ---------------------------------------------------------------------------

func TestGatewayChannelReads_MatchDirectChannelServiceReads(t *testing.T) {
	ctx := context.Background()
	var restrictedChecks, allowedChecks int

	// 有渠道服务与没有渠道服务（返回值是各自的默认值）各跑一遍。
	for _, withService := range []bool{true, false} {
		var cs *ChannelService
		if withService {
			cs = newGroupPolicyFixture()
		}
		pre := preGroupReads{cs: cs}
		for _, s := range gpSurfaces(cs) {
			for _, gid := range gpGroups {
				gid := gid
				for _, model := range gpProbeModels {
					require.Equal(t, pre.resolveChannelMapping(ctx, gid, model), s.svc.ResolveChannelMapping(ctx, gid, model),
						"%s ResolveChannelMapping service=%v group=%d model=%q", s.name, withService, gid, model)
					require.Equal(t, pre.isModelRestricted(ctx, gid, model), s.svc.IsModelRestricted(ctx, gid, model),
						"%s IsModelRestricted service=%v group=%d model=%q", s.name, withService, gid, model)

					wantMapping, wantRestricted := pre.resolveChannelMappingAndRestrict(ctx, &gid, model)
					gotMapping, gotRestricted := s.svc.ResolveChannelMappingAndRestrict(ctx, &gid, model)
					require.Equal(t, wantMapping, gotMapping, "%s ResolveChannelMappingAndRestrict service=%v group=%d model=%q", s.name, withService, gid, model)
					require.Equal(t, wantRestricted, gotRestricted)
					require.False(t, gotRestricted, "restriction moved to scheduling; this result is always false")

					wantCheck := pre.checkChannelPricingRestriction(ctx, &gid, model)
					require.Equal(t, wantCheck, s.svc.checkChannelPricingRestriction(ctx, &gid, model),
						"%s checkChannelPricingRestriction service=%v group=%d model=%q", s.name, withService, gid, model)
					if wantCheck {
						restrictedChecks++
					} else {
						allowedChecks++
					}
				}
				require.Equal(t, pre.needsUpstreamChannelRestrictionCheck(ctx, &gid), s.svc.needsUpstreamChannelRestrictionCheck(ctx, &gid),
					"%s needsUpstreamChannelRestrictionCheck service=%v group=%d", s.name, withService, gid)
			}

			// 没有分组：不读渠道，返回默认值。
			wantMapping, wantRestricted := pre.resolveChannelMappingAndRestrict(ctx, nil, "gpt-5.5")
			gotMapping, gotRestricted := s.svc.ResolveChannelMappingAndRestrict(ctx, nil, "gpt-5.5")
			require.Equal(t, wantMapping, gotMapping, s.name)
			require.Equal(t, wantRestricted, gotRestricted, s.name)
			require.Equal(t, ChannelMappingResult{MappedModel: "gpt-5.5"}, gotMapping, s.name)
			require.False(t, s.svc.checkChannelPricingRestriction(ctx, nil, "gpt-5.5"), s.name)
			require.False(t, s.svc.needsUpstreamChannelRestrictionCheck(ctx, nil), s.name)
		}
	}
	require.NotZero(t, restrictedChecks, "the grid must contain restricted requests")
	require.NotZero(t, allowedChecks, "the grid must contain admitted requests")
}

func TestUpstreamModelRestrictedByChannel_MatchDirectReads(t *testing.T) {
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	pre := preGroupReads{cs: cs}
	gw := &GatewayService{channelService: cs}
	oa := &OpenAIGatewayService{channelService: cs}

	anthropicAccounts := []*Account{
		{Platform: PlatformAnthropic},
		{Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"model_mapping": map[string]any{"claude-opus-4-5": "claude-sonnet-4-5"}}},
	}
	openaiAccounts := []*Account{
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}, Extra: map[string]any{}},
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{},
			Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.5": "gpt-5.6-luna"}}},
	}

	var restricted, allowed int
	for _, gid := range gpGroups {
		gid := gid
		for _, model := range gpProbeModels {
			for _, acc := range anthropicAccounts {
				want := pre.isUpstreamModelRestricted(ctx, gid, resolveAccountUpstreamModel(acc, model))
				require.Equal(t, want, gw.isUpstreamModelRestrictedByChannel(ctx, gid, acc, model), "gateway group=%d model=%q", gid, model)
				wantSticky := pre.needsUpstreamChannelRestrictionCheck(ctx, &gid) && want
				require.Equal(t, wantSticky, gw.isStickyAccountUpstreamRestricted(ctx, &gid, acc, model), "sticky group=%d model=%q", gid, model)
				if want {
					restricted++
				} else {
					allowed++
				}
			}
			for _, acc := range openaiAccounts {
				for _, compact := range []bool{false, true} {
					want := pre.isUpstreamModelRestricted(ctx, gid, resolveOpenAIAccountUpstreamModelForRequest(acc, model, compact))
					require.Equal(t, want, oa.isUpstreamModelRestrictedByChannel(ctx, gid, acc, model, compact),
						"openai group=%d model=%q compact=%v", gid, model, compact)
				}
			}
		}
	}
	require.NotZero(t, restricted)
	require.NotZero(t, allowed)

	// 没有渠道服务：不限制。
	require.False(t, (&GatewayService{}).isUpstreamModelRestrictedByChannel(ctx, gpGroupUpstream, anthropicAccounts[0], "unknown-model"))
	require.False(t, (&OpenAIGatewayService{}).isUpstreamModelRestrictedByChannel(ctx, gpGroupUpstream, openaiAccounts[0], "unknown-model", false))
}

func TestGatewayFeatureReads_MatchDirectReads(t *testing.T) {
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	pre := preGroupReads{cs: cs}
	gw := &GatewayService{channelService: cs}

	var enabled, disabled int
	for _, gid := range gpGroups {
		gid := gid
		for _, platform := range gpPlatforms {
			want := pre.bedrockCCCompatEnabled(ctx, platform, &gid)
			require.Equal(t, want, gw.isBedrockCCCompatEnabled(ctx, &Account{Platform: platform}, &gid), "group=%d platform=%q", gid, platform)
			if want {
				enabled++
			} else {
				disabled++
			}
		}
	}
	require.NotZero(t, enabled)
	require.NotZero(t, disabled)

	// 没有分组、没有渠道服务、缓存加载失败：关闭。
	require.False(t, gw.isBedrockCCCompatEnabled(ctx, &Account{Platform: PlatformAnthropic}, nil))
	gid := gpGroupMain
	require.False(t, (&GatewayService{}).isBedrockCCCompatEnabled(ctx, &Account{Platform: PlatformAnthropic}, &gid))
	require.False(t, (&GatewayService{channelService: gpErrChannelService()}).isBedrockCCCompatEnabled(ctx, &Account{Platform: PlatformAnthropic}, &gid))
}

func TestOpenAICodexImageBridge_MatchDirectReads(t *testing.T) {
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	pre := preGroupReads{cs: cs}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	newService := func(channelService *ChannelService, global bool) *OpenAIGatewayService {
		cfg := &config.Config{}
		cfg.Gateway.CodexImageGenerationBridgeEnabled = global
		return &OpenAIGatewayService{cfg: cfg, channelService: channelService}
	}

	var overridden, followed int
	for _, global := range []bool{false, true} {
		svc := newService(cs, global)
		for _, gid := range gpGroups {
			gid := gid
			want := global
			if override := pre.codexImageBridgeOverride(ctx, &gid); override != nil {
				want = *override
				overridden++
			} else {
				followed++
			}
			require.Equal(t, want, svc.isCodexImageGenerationBridgeEnabled(ctx, account, &APIKey{GroupID: &gid}), "global=%v group=%d", global, gid)
		}
		// 没有分组或没有 API Key：跟随全局。
		require.Equal(t, global, svc.isCodexImageGenerationBridgeEnabled(ctx, account, &APIKey{}))
		require.Equal(t, global, svc.isCodexImageGenerationBridgeEnabled(ctx, account, nil))
		// 没有渠道服务、缓存加载失败（只打日志）：跟随全局。
		gid := gpGroupApply
		require.Equal(t, global, newService(nil, global).isCodexImageGenerationBridgeEnabled(ctx, account, &APIKey{GroupID: &gid}))
		require.Equal(t, global, newService(gpErrChannelService(), global).isCodexImageGenerationBridgeEnabled(ctx, account, &APIKey{GroupID: &gid}))
	}
	require.NotZero(t, overridden)
	require.NotZero(t, followed)
	// 接收者为 nil 也不能 panic。
	require.False(t, (*OpenAIGatewayService)(nil).isCodexImageGenerationBridgeEnabled(ctx, account, &APIKey{}))
}

// setupWebSearchEmulation 打开 web search 模拟所需的全局条件，返回清理函数。
func setupWebSearchEmulation() func() {
	SetWebSearchManager(websearch.NewManager([]websearch.ProviderConfig{{Type: "brave", APIKey: "k"}}, nil))
	setGlobalWebSearchConfig(&WebSearchEmulationConfig{
		Enabled:   true,
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "k"}},
	})
	return func() {
		SetWebSearchManager(nil)
		clearGlobalWebSearchConfig()
	}
}

func TestShouldEmulateWebSearch_ChannelSwitchMatchesDirectRead(t *testing.T) {
	defer setupWebSearchEmulation()()
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	pre := preGroupReads{cs: cs}
	svc := &GatewayService{settingService: newSettingServiceForWebSearchTest(true), channelService: cs}

	var enabled, disabled int
	for _, gid := range gpGroups {
		gid := gid
		for _, platform := range []string{PlatformAnthropic, PlatformOpenAI} {
			account := newAnthropicAPIKeyAccount(WebSearchModeDefault)
			account.Platform = platform
			want := pre.webSearchEmulationEnabled(ctx, platform, &gid)
			require.Equal(t, want, svc.shouldEmulateWebSearch(ctx, account, &gid, webSearchToolBody), "group=%d platform=%q", gid, platform)
			if want {
				enabled++
			} else {
				disabled++
			}
		}
	}
	require.NotZero(t, enabled)
	require.NotZero(t, disabled)

	// 没有分组、缓存加载失败：关闭。
	account := newAnthropicAPIKeyAccount(WebSearchModeDefault)
	require.False(t, svc.shouldEmulateWebSearch(ctx, account, nil, webSearchToolBody))
	gid := gpGroupMain
	errSvc := &GatewayService{settingService: newSettingServiceForWebSearchTest(true), channelService: gpErrChannelService()}
	require.False(t, errSvc.shouldEmulateWebSearch(ctx, account, &gid, webSearchToolBody))
}

func TestModelDowngradeCandidateFilter_ChannelCheckMatchesDirectReads(t *testing.T) {
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	pre := preGroupReads{cs: cs}
	svc := &OpenAIGatewayService{channelService: cs}
	const requestedModel = "gpt-6-astra"

	newCandidate := func(gid int64) *Account {
		return &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Credentials: map[string]any{}, Extra: map[string]any{},
			Groups: []*Group{{ID: gid}}}
	}
	filterFor := func(s *OpenAIGatewayService) ModelDowngradeCandidateFilter {
		return s.modelDowngradeCandidateFilter(ctx, requestedModel, "/v1/chat/completions", OpenAIUpstreamTransportAny)
	}

	var vetoed, admitted int
	for _, gid := range gpGroups {
		gid := gid
		candidate := newCandidate(gid)
		want := !(pre.needsUpstreamChannelRestrictionCheck(ctx, &gid) &&
			pre.isUpstreamModelRestricted(ctx, gid, resolveOpenAIAccountUpstreamModelForRequest(candidate, requestedModel, false)))
		require.Equal(t, want, filterFor(svc)(ctx, candidate, &gid), "group=%d", gid)
		if want {
			admitted++
		} else {
			vetoed++
		}
	}
	require.NotZero(t, vetoed, "the grid must contain a channel veto")
	require.NotZero(t, admitted)
	require.False(t, pre.needsUpstreamChannelRestrictionCheck(ctx, nil))

	// 没有渠道服务：不否决。缓存加载失败：候选不可用（这与调度器把失败当作「不需要检查」不同，原样保留）。
	gid := gpGroupNone
	require.True(t, filterFor(&OpenAIGatewayService{})(ctx, newCandidate(gid), &gid))
	require.False(t, filterFor(&OpenAIGatewayService{channelService: gpErrChannelService()})(ctx, newCandidate(gid), &gid))
}

func TestResolveAccountStatsCost_PolicyPathMatchesDirectChannelRead(t *testing.T) {
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	policy := newLegacyGroupPolicy(cs)
	bs := newTestBillingServiceWithPrices(map[string]*ModelPricing{
		"gpt-5.6-luna":  {InputPricePerToken: 0.001, OutputPricePerToken: 0.002},
		"unknown-model": {InputPricePerToken: 0.003, OutputPricePerToken: 0.004},
	})
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50}

	var priced, unpriced int
	for _, gid := range gpGroups {
		for _, accountID := range []int64{7, 8} {
			for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-luna-high", "unknown-model", ""} {
				for _, totalCost := range []float64{0, 0.75} {
					for _, billing := range []*BillingService{nil, bs} {
						want := preResolveAccountStatsCost(ctx, cs, billing, accountID, gid, model, tokens, 1, totalCost, "", time.Time{})
						got := resolveAccountStatsCost(ctx, policy, billing, accountID, gid, model, tokens, 1, totalCost, "", time.Time{})
						require.Equal(t, want, got, "group=%d account=%d model=%q totalCost=%v billing=%v", gid, accountID, model, totalCost, billing != nil)
						if got != nil {
							priced++
						} else {
							unpriced++
						}
					}
				}
			}
		}
	}
	require.NotZero(t, priced)
	require.NotZero(t, unpriced)

	// 优先级 1：账号命中的自定义规则（100×0.001 + 50×0.004）。
	custom := resolveAccountStatsCost(ctx, policy, nil, 7, gpGroupMain, "gpt-5.6-luna", tokens, 1, 0.75, "", time.Time{})
	require.NotNil(t, custom)
	require.InDelta(t, 0.3, *custom, 1e-12)
	// 优先级 2：应用模型定价到账号统计，直接用客户计费。
	follow := resolveAccountStatsCost(ctx, policy, bs, 8, gpGroupApply, "gpt-5.6-luna", tokens, 1, 0.75, "", time.Time{})
	require.NotNil(t, follow)
	require.InDelta(t, 0.75, *follow, 1e-12)
	// 没有（启用的）渠道：走默认公式，即使有模型定价文件的价格。
	require.Nil(t, resolveAccountStatsCost(ctx, policy, bs, 8, gpGroupNone, "gpt-5.6-luna", tokens, 1, 0.75, "", time.Time{}))
	require.Nil(t, resolveAccountStatsCost(ctx, policy, bs, 8, gpGroupDisabled, "gpt-5.6-luna", tokens, 1, 0.75, "", time.Time{}))
	// 没有策略：nil。
	require.Nil(t, resolveAccountStatsCost(ctx, nil, bs, 8, gpGroupApply, "gpt-5.6-luna", tokens, 1, 0.75, "", time.Time{}))
}

func TestModelPricingResolver_PriceOverrideMatchesDirectRead(t *testing.T) {
	ctx := context.Background()
	cs := newGroupPolicyFixture()
	r := NewModelPricingResolver(cs, nil)

	var hits int
	for _, gid := range gpGroups {
		for _, model := range gpProbeModels {
			want := preLookupChannelPricingNormalized(ctx, cs, gid, model)
			got := r.priceOverride(ctx, gid, model)
			require.Equal(t, want, got, "group=%d model=%q", gid, model)
			if got != nil {
				hits++
			}
		}
	}
	require.NotZero(t, hits)

	// 没有渠道服务：没有覆盖。
	require.Nil(t, NewModelPricingResolver(nil, nil).priceOverride(ctx, gpGroupMain, "gpt-5.6-luna"))
}

// ---------------------------------------------------------------------------
// 接线：注入记录型 GroupPolicy，调用点必须走它，且不读 channelService
// ---------------------------------------------------------------------------

func TestGatewayService_ChannelReadsGoThroughGroupPolicy(t *testing.T) {
	ctx := context.Background()
	gid := int64(10)
	rec := &recordingGroupPolicy{
		mapping:        ChannelMappingResult{MappedModel: "mapped-model", ChannelID: 5, Mapped: true, BillingModelSource: BillingModelSourceRequested},
		modelAccess:    QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist},
		upstreamAccess: QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist},
		upstreamCheck:  true,
		feature:        boolOverridePtr(true),
	}
	svc := &GatewayService{channelService: gpForbiddenChannelService(t), policyOverride: rec}
	account := &Account{Platform: PlatformAnthropic}

	require.Equal(t, rec.mapping, svc.ResolveChannelMapping(ctx, gid, "req"))
	require.Equal(t, []string{"Mapping(10,req)"}, rec.take())

	require.True(t, svc.IsModelRestricted(ctx, gid, "req"))
	require.Equal(t, []string{"ModelAccess(10,req)"}, rec.take())

	gotMapping, gotRestricted := svc.ResolveChannelMappingAndRestrict(ctx, &gid, "req")
	require.Equal(t, rec.mapping, gotMapping)
	require.False(t, gotRestricted)
	require.Equal(t, []string{"Mapping(10,req)"}, rec.take())
	gotMapping, gotRestricted = svc.ResolveChannelMappingAndRestrict(ctx, nil, "req")
	require.Equal(t, ChannelMappingResult{MappedModel: "req"}, gotMapping)
	require.False(t, gotRestricted)
	require.Empty(t, rec.take(), "no group means no policy read")

	// 调度预检查：按计费来源取要检查的模型。
	require.True(t, svc.checkChannelPricingRestriction(ctx, &gid, "req"))
	require.Equal(t, []string{"Mapping(10,req)", "ModelAccess(10,req)"}, rec.take())
	rec.mapping.BillingModelSource = BillingModelSourceChannelMapped
	require.True(t, svc.checkChannelPricingRestriction(ctx, &gid, "req"))
	require.Equal(t, []string{"Mapping(10,req)", "ModelAccess(10,mapped-model)"}, rec.take())
	rec.mapping.BillingModelSource = BillingModelSourceUpstream
	require.False(t, svc.checkChannelPricingRestriction(ctx, &gid, "req"), "upstream billing is checked per account")
	require.Equal(t, []string{"Mapping(10,req)"}, rec.take())
	require.False(t, svc.checkChannelPricingRestriction(ctx, &gid, ""))
	require.Empty(t, rec.take())

	// 按账号检查上游模型：账号映射在网关里算，策略只收到算好的上游模型名。
	upstreamModel := resolveAccountUpstreamModel(account, "claude-sonnet-4-5")
	require.NotEmpty(t, upstreamModel)
	require.True(t, svc.isUpstreamModelRestrictedByChannel(ctx, gid, account, "claude-sonnet-4-5"))
	require.Equal(t, []string{"UpstreamAccess(10," + upstreamModel + ")"}, rec.take())
	require.True(t, svc.needsUpstreamChannelRestrictionCheck(ctx, &gid))
	require.Equal(t, []string{"UpstreamCheck(10)"}, rec.take())
	require.True(t, svc.isStickyAccountUpstreamRestricted(ctx, &gid, account, "claude-sonnet-4-5"))
	require.Equal(t, []string{"UpstreamCheck(10)", "UpstreamAccess(10," + upstreamModel + ")"}, rec.take())
	rec.upstreamCheckErr = errPolicyTest
	require.False(t, svc.needsUpstreamChannelRestrictionCheck(ctx, &gid), "a failed check means no per-account check")
	rec.take()
	rec.upstreamCheckErr = nil

	// Bedrock CC 兼容开关。
	require.True(t, svc.isBedrockCCCompatEnabled(ctx, account, &gid))
	require.Equal(t, []string{"Feature(10,anthropic,bedrock_cc_compat)"}, rec.take())
	rec.feature = nil
	require.False(t, svc.isBedrockCCCompatEnabled(ctx, account, &gid), "no explicit setting means off")
	rec.take()
	rec.feature = boolOverridePtr(true)
	rec.featureErr = errPolicyTest
	require.False(t, svc.isBedrockCCCompatEnabled(ctx, account, &gid))
	rec.take()
	require.False(t, svc.isBedrockCCCompatEnabled(ctx, account, nil))
	require.Empty(t, rec.take())
}

func TestShouldEmulateWebSearch_GoesThroughGroupPolicy(t *testing.T) {
	defer setupWebSearchEmulation()()
	ctx := context.Background()
	gid := int64(42)
	rec := &recordingGroupPolicy{feature: boolOverridePtr(true)}
	svc := &GatewayService{settingService: newSettingServiceForWebSearchTest(true), channelService: gpForbiddenChannelService(t), policyOverride: rec}
	account := newAnthropicAPIKeyAccount(WebSearchModeDefault)

	require.True(t, svc.shouldEmulateWebSearch(ctx, account, &gid, webSearchToolBody))
	require.Equal(t, []string{"Feature(42,anthropic,web_search_emulation)"}, rec.take())
	rec.feature = boolOverridePtr(false)
	require.False(t, svc.shouldEmulateWebSearch(ctx, account, &gid, webSearchToolBody))
	rec.take()
	rec.feature = nil
	require.False(t, svc.shouldEmulateWebSearch(ctx, account, &gid, webSearchToolBody))
	rec.take()
	// 账号显式开启或关闭时不看分组。
	require.True(t, svc.shouldEmulateWebSearch(ctx, newAnthropicAPIKeyAccount(WebSearchModeEnabled), &gid, webSearchToolBody))
	require.Empty(t, rec.take())
}

func TestOpenAIGatewayService_ChannelReadsGoThroughGroupPolicy(t *testing.T) {
	ctx := context.Background()
	gid := int64(10)
	rec := &recordingGroupPolicy{
		mapping:        ChannelMappingResult{MappedModel: "mapped-model", ChannelID: 5, Mapped: true, BillingModelSource: BillingModelSourceChannelMapped},
		modelAccess:    QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist},
		upstreamAccess: QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist},
		upstreamCheck:  true,
		feature:        boolOverridePtr(true),
	}
	cfg := &config.Config{}
	svc := &OpenAIGatewayService{cfg: cfg, channelService: gpForbiddenChannelService(t), policyOverride: rec}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}, Extra: map[string]any{}}

	require.Equal(t, rec.mapping, svc.ResolveChannelMapping(ctx, gid, "req"))
	require.Equal(t, []string{"Mapping(10,req)"}, rec.take())
	require.True(t, svc.IsModelRestricted(ctx, gid, "req"))
	require.Equal(t, []string{"ModelAccess(10,req)"}, rec.take())
	gotMapping, gotRestricted := svc.ResolveChannelMappingAndRestrict(ctx, &gid, "req")
	require.Equal(t, rec.mapping, gotMapping)
	require.False(t, gotRestricted)
	require.Equal(t, []string{"Mapping(10,req)"}, rec.take())
	gotMapping, _ = svc.ResolveChannelMappingAndRestrict(ctx, nil, "req")
	require.Equal(t, ChannelMappingResult{MappedModel: "req"}, gotMapping)
	require.Empty(t, rec.take())

	require.True(t, svc.checkChannelPricingRestriction(ctx, &gid, "req"))
	require.Equal(t, []string{"Mapping(10,req)", "ModelAccess(10,mapped-model)"}, rec.take())

	require.True(t, svc.needsUpstreamChannelRestrictionCheck(ctx, &gid))
	require.Equal(t, []string{"UpstreamCheck(10)"}, rec.take())
	rec.upstreamCheckErr = errPolicyTest
	require.False(t, svc.needsUpstreamChannelRestrictionCheck(ctx, &gid))
	rec.take()
	rec.upstreamCheckErr = nil

	upstreamModel := resolveOpenAIAccountUpstreamModelForRequest(account, "gpt-5.6-luna", false)
	require.NotEmpty(t, upstreamModel)
	require.True(t, svc.isUpstreamModelRestrictedByChannel(ctx, gid, account, "gpt-5.6-luna", false))
	require.Equal(t, []string{"UpstreamAccess(10," + upstreamModel + ")"}, rec.take())

	// Codex 图片桥：分组显式设置优先于全局；没有显式设置或读取失败时跟随全局。
	cfg.Gateway.CodexImageGenerationBridgeEnabled = false
	require.True(t, svc.isCodexImageGenerationBridgeEnabled(ctx, account, &APIKey{GroupID: &gid}))
	require.Equal(t, []string{"Feature(10,openai,codex_image_generation_bridge)"}, rec.take())
	rec.feature = boolOverridePtr(false)
	cfg.Gateway.CodexImageGenerationBridgeEnabled = true
	require.False(t, svc.isCodexImageGenerationBridgeEnabled(ctx, account, &APIKey{GroupID: &gid}))
	rec.take()
	rec.feature = nil
	require.True(t, svc.isCodexImageGenerationBridgeEnabled(ctx, account, &APIKey{GroupID: &gid}))
	rec.take()
	rec.feature = boolOverridePtr(false)
	rec.featureErr = errPolicyTest
	require.True(t, svc.isCodexImageGenerationBridgeEnabled(ctx, account, &APIKey{GroupID: &gid}), "a failed read falls back to the global switch")
	rec.take()
	rec.featureErr = nil
	require.True(t, svc.isCodexImageGenerationBridgeEnabled(ctx, account, &APIKey{}), "no group, no read")
	require.Empty(t, rec.take())
}

func TestModelDowngradeCandidateFilter_GoesThroughGroupPolicy(t *testing.T) {
	ctx := context.Background()
	gid := int64(10)
	candidate := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Credentials: map[string]any{}, Extra: map[string]any{},
		Groups: []*Group{{ID: gid}}}
	rec := &recordingGroupPolicy{upstreamCheck: true, upstreamAccess: QuoteAccess{OK: true}}
	svc := &OpenAIGatewayService{channelService: gpForbiddenChannelService(t), policyOverride: rec}
	filter := svc.modelDowngradeCandidateFilter(ctx, "gpt-6-astra", "/v1/chat/completions", OpenAIUpstreamTransportAny)
	upstreamModel := resolveOpenAIAccountUpstreamModelForRequest(candidate, "gpt-6-astra", false)
	require.NotEmpty(t, upstreamModel)

	require.True(t, filter(ctx, candidate, &gid))
	require.Equal(t, []string{"UpstreamCheck(10)", "UpstreamAccess(10," + upstreamModel + ")"}, rec.take())
	rec.upstreamAccess = QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}
	require.False(t, filter(ctx, candidate, &gid), "a channel veto cannot count as an alternative route")
	rec.take()
	rec.upstreamCheck = false
	require.True(t, filter(ctx, candidate, &gid))
	require.Equal(t, []string{"UpstreamCheck(10)"}, rec.take())
	rec.upstreamCheckErr = errPolicyTest
	require.False(t, filter(ctx, candidate, &gid), "a failed check makes the candidate unavailable")
	rec.take()
}

func TestApplyAccountStatsCost_GoesThroughGroupPolicy(t *testing.T) {
	ctx := context.Background()
	gid := int64(10)
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50}
	bs := newTestBillingServiceWithPrices(map[string]*ModelPricing{
		"gpt-5.6-luna": {InputPricePerToken: 0.001, OutputPricePerToken: 0.002},
	})
	rule := AccountStatsPricingRule{
		AccountIDs: []int64{7},
		Pricing:    []ChannelModelPricing{gpTokenPricing(PlatformOpenAI, 0.01, "gpt-5.6-luna")},
	}
	run := func(rec *recordingGroupPolicy, billing *BillingService, accountID int64, totalCost float64) *float64 {
		usageLog := &UsageLog{}
		applyAccountStatsCost(ctx, usageLog, rec, billing, accountID, gid, "gpt-5.6-luna", "ignored-requested-model", tokens, totalCost, time.Time{})
		return usageLog.AccountStatsCost
	}

	// account_rate：只问模式，不取规则，走默认公式。
	rec := &recordingGroupPolicy{costMode: MatrixCostAccountRate, costRules: []AccountStatsPricingRule{rule}}
	require.Nil(t, run(rec, bs, 7, 0.75))
	require.Equal(t, []string{"CostMode(10)"}, rec.take())

	// follow_billing：直接用客户计费；自定义规则优先于它。
	rec = &recordingGroupPolicy{costMode: MatrixCostFollowBilling, costPlatform: PlatformOpenAI}
	got := run(rec, bs, 7, 0.75)
	require.NotNil(t, got)
	require.InDelta(t, 0.75, *got, 1e-12)
	require.Equal(t, []string{"CostMode(10)", "CostRules(10)"}, rec.take())
	rec.costRules = []AccountStatsPricingRule{rule}
	got = run(rec, bs, 7, 0.75)
	require.NotNil(t, got)
	require.InDelta(t, 0.01*100+0.04*50, *got, 1e-12)
	rec.take()
	require.Nil(t, run(rec, bs, 8, 0), "follow_billing with a zero customer cost does not override")
	rec.take()

	// catalog_upstream：规则不命中时查模型定价文件。
	rec = &recordingGroupPolicy{costMode: MatrixCostCatalogUpstream, costRules: []AccountStatsPricingRule{rule}, costPlatform: PlatformOpenAI}
	got = run(rec, bs, 8, 0.75)
	require.NotNil(t, got)
	require.InDelta(t, 100*0.001+50*0.002, *got, 1e-12)
	require.Equal(t, []string{"CostMode(10)", "CostRules(10)"}, rec.take())

	// 上游模型与请求模型都为空：不读策略。
	usageLog := &UsageLog{}
	applyAccountStatsCost(ctx, usageLog, rec, bs, 7, gid, "", "", tokens, 0.75, time.Time{})
	require.Nil(t, usageLog.AccountStatsCost)
	require.Empty(t, rec.take())
}

func TestModelPricingResolver_PriceOverrideGoesThroughGroupPolicy(t *testing.T) {
	ctx := context.Background()
	gid := int64(10)
	override := gpTokenPricing(PlatformOpenAI, 7e-6, "gpt-5.6-luna")
	rec := &recordingGroupPolicy{override: &override}
	r := &ModelPricingResolver{channelService: gpForbiddenChannelService(t), policyOverride: rec, billingService: newTestBillingService()}

	// 策略收到的是原始模型名：字面名、归一化名两步查找在策略内部，不在 resolver 里。
	resolved := r.Resolve(ctx, PricingInput{Model: "gpt-5.6-luna-high", GroupID: &gid})
	require.Equal(t, PricingSourceChannel, resolved.Source)
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, 7e-6, resolved.BasePricing.InputPricePerToken, 1e-15)
	require.Equal(t, []string{"PriceOverride(10,gpt-5.6-luna-high)"}, rec.take())

	// 没有覆盖：回落官方价，来源不是渠道。
	rec.override = nil
	resolved = r.Resolve(ctx, PricingInput{Model: "gpt-5.6-luna-high", GroupID: &gid})
	require.NotEqual(t, PricingSourceChannel, resolved.Source)
	calls := rec.take()
	require.NotEmpty(t, calls)
	for _, call := range calls {
		require.Equal(t, "PriceOverride(10,gpt-5.6-luna-high)", call)
	}

	// 没有分组：不读策略。
	r.Resolve(ctx, PricingInput{Model: "gpt-5.6-luna-high"})
	require.Empty(t, rec.take())
}

// errPolicyTest 供接线测试注入读取失败。
var errPolicyTest = errors.New("group policy read failed")
