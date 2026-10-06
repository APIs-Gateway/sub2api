//go:build unit

package service

// W6 PR7b-2a：白名单分组无价拦截的补全。测试名以 TestStagedPolicy_ 或 TestUnpricedAllowlist_ 开头。
// 全部是内存夹具，不碰数据库，也不往共享表写数据。

import (
	"context"
	"errors"
	"testing"
	"time"

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

// 候选链与计费的取价回退一致：上游模型无价但请求模型（或渠道映射模型）有价时，block_allowlist 不拦。
func TestUnpricedAllowlist_UpstreamSourceFallsBackToRequestedModel(t *testing.T) {
	ctx := context.Background()
	creds := map[string]any{"model_mapping": map[string]any{"req-priced-xyz": "up-unpriced-xyz"}}
	oaAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: creds}
	gwAccount := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: creds}
	oaUp := resolveOpenAIAccountUpstreamModelForRequest(oaAccount, "req-priced-xyz", false)
	gwUp := resolveAccountUpstreamModel(gwAccount, "req-priced-xyz")
	require.NotEqual(t, "req-priced-xyz", oaUp, "the account really maps the request to a different upstream model")
	require.NotEqual(t, "req-priced-xyz", gwUp)

	f := newSPFixture(t, v2Snap(func(c *MatrixGroupConfig) {
		c.AccessMode = MatrixAccessAllowlist
		c.BillingModelSource = mpS(BillingModelSourceUpstream)
	}, 1, mpInherit(oaUp), mpInherit(gwUp), mpCustom("req-priced-xyz", 1e-6)))
	settings := NewSettingService(&brSettingRepo{value: BillingUnpricedPolicyBlockAllowlist}, nil)
	bs := newTestBillingService()
	openai := &OpenAIGatewayService{billingService: bs, policyOverride: f.staged, settingService: settings}
	gw := &GatewayService{billingService: bs, policyOverride: f.staged, settingService: settings}

	require.False(t, openai.isUpstreamModelRestrictedByChannel(ctx, 1, oaAccount, "req-priced-xyz", false), "the requested model has a price")
	require.False(t, gw.isUpstreamModelRestrictedByChannel(ctx, 1, gwAccount, "req-priced-xyz"), "the requested model has a price")

	// 对照：请求模型也无价时照拦。
	creds2 := map[string]any{"model_mapping": map[string]any{"req-unpriced-xyz": "up-unpriced-xyz"}}
	gwAccount2 := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: creds2}
	f2 := newSPFixture(t, v2Snap(func(c *MatrixGroupConfig) {
		c.AccessMode = MatrixAccessAllowlist
		c.BillingModelSource = mpS(BillingModelSourceUpstream)
	}, 1, mpInherit(resolveAccountUpstreamModel(gwAccount2, "req-unpriced-xyz"))))
	gw2 := &GatewayService{billingService: bs, policyOverride: f2.staged, settingService: settings}
	require.True(t, gw2.isUpstreamModelRestrictedByChannel(ctx, 1, gwAccount2, "req-unpriced-xyz"))
}

// 已知免费名单：运行时无价检查也认（按分组、不区分大小写），名单里的模型不算无价、不记观测、不拦；名单只读一次（缓存 15 秒）。
func TestStagedPolicy_RuntimeAccessHonorsKnownFreeList(t *testing.T) {
	ctx := context.Background()
	f := newSPFixture(t, v2Snap(allowlistConfig, 1, mpInherit("free-model"), mpInherit("other-group-free"), mpInherit("not-free")))
	reads := 0
	in := RuntimePriceInputs{
		ReadPolicy: func(context.Context) string { return BillingUnpricedPolicyBlockAllowlist },
		ReadKnownFree: func(context.Context) ([]BillingKnownFreeEntry, error) {
			reads++
			return []BillingKnownFreeEntry{{Model: "Free-Model"}, {GroupID: 2, Model: "other-group-free"}}, nil
		},
	}
	require.Equal(t, QuoteAccess{OK: true}, f.staged.RuntimeAccess(ctx, 1, []string{"free-model"}, in))
	require.False(t, f.staged.RuntimeAccess(ctx, 1, []string{"other-group-free"}, in).OK, "the entry is for another group")
	require.False(t, f.staged.RuntimeAccess(ctx, 1, []string{"not-free"}, in).OK)
	// 链里任一在名单里就放行。
	require.True(t, f.staged.RuntimeAccess(ctx, 1, []string{"not-free", "free-model"}, in).OK)
	require.Equal(t, 1, reads, "cached: the list is not read per request")
	observed, _ := f.staged.UnpricedAdmissionStats()
	require.EqualValues(t, 2, observed, "known-free models are not counted as unpriced")

	f.clock.Advance(runtimePolicyTTL + time.Second)
	f.staged.RuntimeAccess(ctx, 1, []string{"not-free"}, in)
	require.Equal(t, 2, reads, "re-read after the TTL")
}

// 网关入口：名单来自 settings 的 billing_known_free_list。
type unpricedMapRepo struct {
	SettingRepository
	values map[string]string
}

func (r *unpricedMapRepo) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

func TestUnpricedAllowlist_KnownFreeModelIsNotBlockedAtTheGateway(t *testing.T) {
	ctx := context.Background()
	gid := int64(1)
	f := newSPFixture(t, v2Snap(allowlistConfig, 1, mpInherit("no-such-free-xyz"), mpInherit("no-such-model-xyz")))
	settings := NewSettingService(&unpricedMapRepo{values: map[string]string{
		SettingKeyBillingUnpricedPolicy: BillingUnpricedPolicyBlockAllowlist,
		SettingKeyBillingKnownFreeList:  `[{"group_id":1,"model":"no-such-free-xyz"}]`,
	}}, nil)
	bs := newTestBillingService()
	openai := &OpenAIGatewayService{billingService: bs, policyOverride: f.staged, settingService: settings}
	gw := &GatewayService{billingService: bs, policyOverride: f.staged, settingService: settings}
	require.False(t, openai.checkChannelPricingRestriction(ctx, &gid, "no-such-free-xyz"))
	require.False(t, gw.checkChannelPricingRestriction(ctx, &gid, "no-such-free-xyz"))
	require.True(t, openai.checkChannelPricingRestriction(ctx, &gid, "no-such-model-xyz"), "control: not on the list")
}

// 名单读取：出错保留上一次的名单，只缓存很短；不用请求自己的 ctx；冷启动出错只缓存很短。
func TestStagedPolicy_RuntimeKnownFreeListReadErrors(t *testing.T) {
	ctx := context.Background()
	f := newSPFixture(t, v2Snap(allowlistConfig, 1, mpInherit("free-model")))
	reads, fail := 0, false
	var sawCanceledCtx bool
	in := RuntimePriceInputs{
		ReadPolicy: func(context.Context) string { return BillingUnpricedPolicyBlockAllowlist },
		ReadKnownFree: func(c context.Context) ([]BillingKnownFreeEntry, error) {
			reads++
			if c.Err() != nil {
				sawCanceledCtx = true
			}
			if fail {
				return nil, errors.New("db down")
			}
			return []BillingKnownFreeEntry{{Model: "free-model"}}, nil
		},
	}
	// 冷启动读失败：空名单，只缓存 runtimeFreeErrorTTL，之后重试成功。
	fail = true
	require.False(t, f.staged.RuntimeAccess(ctx, 1, []string{"free-model"}, in).OK)
	require.Equal(t, 1, reads)
	f.clock.Advance(time.Second)
	require.False(t, f.staged.RuntimeAccess(ctx, 1, []string{"free-model"}, in).OK)
	require.Equal(t, 1, reads, "negative cache holds for a moment")
	fail = false
	f.clock.Advance(runtimeFreeErrorTTL)
	require.True(t, f.staged.RuntimeAccess(ctx, 1, []string{"free-model"}, in).OK)
	require.Equal(t, 2, reads)

	// 已有名单后再读失败：保留上一次的名单。
	fail = true
	f.clock.Advance(runtimePolicyTTL + time.Second)
	require.True(t, f.staged.RuntimeAccess(ctx, 1, []string{"free-model"}, in).OK, "keeps the last good list")
	require.Equal(t, 3, reads)
	f.clock.Advance(time.Second)
	require.True(t, f.staged.RuntimeAccess(ctx, 1, []string{"free-model"}, in).OK)
	require.Equal(t, 3, reads, "error result is cached only briefly, not re-read per request")

	// 请求自己的 ctx 已取消，也不影响读名单。
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	fail = false
	f.clock.Advance(runtimePolicyTTL + time.Second)
	require.True(t, f.staged.RuntimeAccess(canceled, 1, []string{"free-model"}, in).OK)
	require.False(t, sawCanceledCtx, "the list is read with its own context")
}

// 原始请求模型 A 经分组映射成 B：OpenAI 侧计费链含 A，所以 B 和上游模型无价、A 有价时不拦；
// Anthropic 侧计费链是映射后的 B 与上游模型，A 不算。
func TestUnpricedAllowlist_UpstreamSourceWithGroupMapping(t *testing.T) {
	ctx := context.Background()
	oaAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	gwAccount := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{"model-a-xyz": "model-b-xyz"}}}
	f := newSPFixture(t, v2Snap(func(c *MatrixGroupConfig) {
		c.AccessMode = MatrixAccessAllowlist
		c.BillingModelSource = mpS(BillingModelSourceUpstream)
		c.ModelMapping = []MatrixMappingEntry{{Src: "model-a-xyz", Dst: "model-b-xyz"}}
	}, 1, mpCustom("model-a-xyz", 1e-6), mpInherit("model-b-xyz")))
	settings := NewSettingService(&brSettingRepo{value: BillingUnpricedPolicyBlockAllowlist}, nil)
	bs := newTestBillingService()
	openai := &OpenAIGatewayService{billingService: bs, policyOverride: f.staged, settingService: settings}
	gw := &GatewayService{billingService: bs, policyOverride: f.staged, settingService: settings}

	// OpenAI：调度收到 A，forward model 覆盖成 B；A 有价。
	fwd := context.WithValue(ctx, openAIForwardModelContextKey{}, openAIForwardModel{model: "model-b-xyz"})
	require.False(t, openai.isUpstreamModelRestrictedByChannel(fwd, 1, oaAccount, "model-a-xyz", false), "A is in the billing chain and priced")
	// Anthropic：计费用 B，A 的价格不会被用到，所以按 B 拦。
	require.True(t, gw.isUpstreamModelRestrictedByChannel(ctx, 1, gwAccount, "model-a-xyz"), "A is never billed on this path")
}
