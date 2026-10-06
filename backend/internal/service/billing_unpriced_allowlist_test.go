//go:build unit

package service

// W6 PR7b-2a：白名单分组无价拦截的补全。测试名以 TestStagedPolicy_ 或 TestUnpricedAllowlist_ 开头。
// 全部是内存夹具，不碰数据库，也不往共享表写数据。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 按计费模式判定「有价」：按次、图片模式的自定义价，以及无 token 价的图片模型和按次官方价都不被误判无价。
func TestStagedPolicy_RuntimeAccessPricesByBillingMode(t *testing.T) {
	ctx := context.Background()
	perRequest := mpCellBase("per-request-custom", MatrixPriceCustom)
	perRequest.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModePerRequest, PerRequestPrice: mpF(0.04)}
	imageMode := mpCellBase("image-custom", MatrixPriceCustom)
	imageMode.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModeImage, PerRequestPrice: mpF(0.1)}
	perRequestInterval := mpCellBase("interval-per-request", MatrixPriceCustom)
	perRequestInterval.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModePerRequest, Intervals: []MatrixPriceInterval{{MinTokens: 0, PerRequestPrice: mpF(0.02)}}}
	emptyToken := mpCellBase("empty-token-custom", MatrixPriceCustom)
	emptyToken.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModeToken}
	f := newSPFixture(t, v2Snap(allowlistConfig, 1,
		perRequest, imageMode, perRequestInterval, emptyToken,
		mpInherit("image-model"), mpInherit("official-per-request"), mpInherit("unknown-inherit"), mpExtra("unknown-extra", 2)))

	states := map[string]OfficialPriceState{
		// 图片模型：没有 token 价，但图片请求走 CalculateImageCost，总是有价。
		"image-model": {Known: true, ImageCapable: true},
		// 官方只有按次价：Known、token 价为 0。
		"official-per-request": {Known: true},
	}
	in := RuntimePriceInputs{
		OfficialState: func(model string) OfficialPriceState { return states[model] },
		ReadPolicy:    func(context.Context) string { return BillingUnpricedPolicyBlockAllowlist },
	}
	for _, model := range []string{"per-request-custom", "image-custom", "interval-per-request", "image-model", "official-per-request"} {
		got := f.staged.RuntimeAccess(ctx, 1, []string{model}, in)
		require.Equal(t, QuoteAccess{OK: true, Priced: yes()}, got, model)
	}
	for _, model := range []string{"unknown-inherit", "unknown-extra", "empty-token-custom"} {
		got := f.staged.RuntimeAccess(ctx, 1, []string{model}, in)
		require.Equal(t, QuoteAccess{OK: false, Reason: QuoteAccessReasonUnpriced, Priced: no()}, got, model)
	}
}

// 网关入口的按次、图片判定：官方没有 token 价的模型，只要单元格有按次价就不会被 block_allowlist 误拦。
func TestUnpricedAllowlist_PerRequestModelIsNotBlocked(t *testing.T) {
	ctx := context.Background()
	gid := int64(1)
	perRequest := mpCellBase("no-such-per-request-xyz", MatrixPriceCustom)
	perRequest.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModePerRequest, PerRequestPrice: mpF(0.04)}
	f := newSPFixture(t, v2Snap(allowlistConfig, 1, perRequest, mpInherit("no-such-model-xyz")))
	settings := NewSettingService(&brSettingRepo{value: BillingUnpricedPolicyBlockAllowlist}, nil)
	bs := newTestBillingService()
	openai := &OpenAIGatewayService{billingService: bs, policyOverride: f.staged, settingService: settings}
	gw := &GatewayService{billingService: bs, policyOverride: f.staged, settingService: settings}

	require.False(t, openai.checkChannelPricingRestriction(ctx, &gid, "no-such-per-request-xyz"))
	require.False(t, gw.checkChannelPricingRestriction(ctx, &gid, "no-such-per-request-xyz"))
	require.True(t, openai.checkChannelPricingRestriction(ctx, &gid, "no-such-model-xyz"), "control: a genuinely unpriced model is still blocked")
	require.True(t, gw.checkChannelPricingRestriction(ctx, &gid, "no-such-model-xyz"), "control: a genuinely unpriced model is still blocked")
}

// 计费来源为 upstream 的白名单分组：逐账号的上游模型检查也做无价检查；observe 一律不拦。
func TestUnpricedAllowlist_UpstreamSourceChecksPerAccount(t *testing.T) {
	ctx := context.Background()
	oaAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	gwAccount := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	oaUnpriced := resolveOpenAIAccountUpstreamModelForRequest(oaAccount, "no-such-model-xyz", false)
	gwUnpriced := resolveAccountUpstreamModel(gwAccount, "no-such-model-xyz")
	oaPriced := resolveOpenAIAccountUpstreamModelForRequest(oaAccount, "no-such-priced-xyz", false)
	gwPriced := resolveAccountUpstreamModel(gwAccount, "no-such-priced-xyz")
	require.NotEmpty(t, oaUnpriced)
	require.NotEmpty(t, gwUnpriced)

	for _, policy := range []string{BillingUnpricedPolicyObserve, BillingUnpricedPolicyBlockAllowlist} {
		blocked := policy == BillingUnpricedPolicyBlockAllowlist
		f := newSPFixture(t, v2Snap(func(c *MatrixGroupConfig) {
			c.AccessMode = MatrixAccessAllowlist
			c.BillingModelSource = mpS(BillingModelSourceUpstream)
		}, 1, mpInherit(oaUnpriced), mpInherit(gwUnpriced), mpCustom(oaPriced, 1e-6), mpCustom(gwPriced, 1e-6)))
		settings := NewSettingService(&brSettingRepo{value: policy}, nil)
		bs := newTestBillingService()
		openai := &OpenAIGatewayService{billingService: bs, policyOverride: f.staged, settingService: settings}
		gw := &GatewayService{billingService: bs, policyOverride: f.staged, settingService: settings}

		need, err := f.staged.UpstreamCheck(ctx, 1)
		require.NoError(t, err)
		require.True(t, need, "the scheduling loop checks every account for an upstream-source allowlist group")

		require.Equal(t, blocked, openai.isUpstreamModelRestrictedByChannel(ctx, 1, oaAccount, "no-such-model-xyz", false), policy)
		require.Equal(t, blocked, gw.isUpstreamModelRestrictedByChannel(ctx, 1, gwAccount, "no-such-model-xyz"), policy)
		require.False(t, openai.isUpstreamModelRestrictedByChannel(ctx, 1, oaAccount, "no-such-priced-xyz", false), policy)
		require.False(t, gw.isUpstreamModelRestrictedByChannel(ctx, 1, gwAccount, "no-such-priced-xyz"), policy)
		// 不在白名单里的上游模型：与开关无关，一直被拦（既有行为）。
		require.True(t, openai.isUpstreamModelRestrictedByChannel(ctx, 1, oaAccount, "not-listed-xyz", false), policy)
	}
}

// 开放分组、legacy 分组的上游检查不受影响：不会因为无价被拦。
func TestUnpricedAllowlist_OpenAndLegacyGroupsNeverBlockUpstream(t *testing.T) {
	ctx := context.Background()
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	settings := NewSettingService(&brSettingRepo{value: BillingUnpricedPolicyBlockAllowlist}, nil)
	bs := newTestBillingService()

	open := newSPFixture(t, v2Snap(func(c *MatrixGroupConfig) {
		c.BillingModelSource = mpS(BillingModelSourceUpstream)
	}, 1, mpInherit("no-such-model-xyz")))
	svc := &OpenAIGatewayService{billingService: bs, policyOverride: open.staged, settingService: settings}
	require.False(t, svc.isUpstreamModelRestrictedByChannel(ctx, 1, account, "no-such-model-xyz", false))

	legacy := &OpenAIGatewayService{billingService: bs, policyOverride: &spLegacy{access: QuoteAccess{OK: true}}, settingService: settings}
	require.False(t, legacy.isUpstreamModelRestrictedByChannel(ctx, 1, account, "no-such-model-xyz", false))
}

// 写入 billing_unpriced_policy：只认两个值，非法值 400 且不落库。
type unpricedWriteRepo struct {
	SettingRepository
	set map[string]string
}

func (r *unpricedWriteRepo) Set(_ context.Context, key, value string) error {
	if r.set == nil {
		r.set = map[string]string{}
	}
	r.set[key] = value
	return nil
}

func TestUnpricedAllowlist_SetBillingUnpricedPolicy(t *testing.T) {
	ctx := context.Background()
	for in, want := range map[string]string{"observe": "observe", " block_allowlist ": "block_allowlist"} {
		repo := &unpricedWriteRepo{}
		require.NoError(t, NewSettingService(repo, nil).SetBillingUnpricedPolicy(ctx, in))
		require.Equal(t, want, repo.set[SettingKeyBillingUnpricedPolicy], in)
	}
	for _, bad := range []string{"", "  ", "block", "Observe", "block_all", "true"} {
		repo := &unpricedWriteRepo{}
		err := NewSettingService(repo, nil).SetBillingUnpricedPolicy(ctx, bad)
		require.Error(t, err, bad)
		require.Empty(t, repo.set, "an invalid value must not be stored: %q", bad)
	}
	require.Error(t, NewSettingService(nil, nil).SetBillingUnpricedPolicy(ctx, "observe"))
}
