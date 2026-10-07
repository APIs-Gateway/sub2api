//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// brSettingRepo 只实现 GetValue，其余方法不会被调用（内嵌 nil 接口）。
type brSettingRepo struct {
	SettingRepository
	value string
	err   error
}

func (r *brSettingRepo) GetValue(context.Context, string) (string, error) { return r.value, r.err }

func TestSettingService_BillingUnpricedPolicy(t *testing.T) {
	ctx := context.Background()
	var nilSvc *SettingService
	require.Equal(t, BillingUnpricedPolicyObserve, nilSvc.BillingUnpricedPolicy(ctx))
	require.Equal(t, BillingUnpricedPolicyObserve, NewSettingService(nil, nil).BillingUnpricedPolicy(ctx))

	cases := []struct {
		name string
		repo *brSettingRepo
		want string
	}{
		{"unset", &brSettingRepo{err: ErrSettingNotFound}, BillingUnpricedPolicyObserve},
		{"empty", &brSettingRepo{}, BillingUnpricedPolicyObserve},
		{"observe", &brSettingRepo{value: "observe"}, BillingUnpricedPolicyObserve},
		{"unknown", &brSettingRepo{value: "whatever"}, BillingUnpricedPolicyObserve},
		{"read failure fails open", &brSettingRepo{err: errors.New("db down")}, BillingUnpricedPolicyObserve},
		{"block", &brSettingRepo{value: " block_allowlist "}, BillingUnpricedPolicyBlockAllowlist},
	}
	for _, c := range cases {
		require.Equal(t, c.want, NewSettingService(c.repo, nil).BillingUnpricedPolicy(ctx), c.name)
	}
}

func TestRuntimeCandidates(t *testing.T) {
	require.Equal(t, []string{"a", "b"}, runtimeCandidates(" a ", "", "b", "a"))
	require.Empty(t, runtimeCandidates("", "  "))
}

func TestRuntimeUnpricedBlocked_NonStagedPolicyAndNoCandidates(t *testing.T) {
	ctx := context.Background()
	require.False(t, runtimeUnpricedBlocked(ctx, &spLegacy{}, nil, nil, 1, "m", "m", "m"))
	f := newSPFixture(t, v2Snap(allowlistConfig, 1, mpInherit("x")))
	require.False(t, runtimeUnpricedBlocked(ctx, f.staged, nil, nil, 1, "", " ", ""))
}

// 两个网关的调度阶段入口：observe 放行、block_allowlist 拦截无价请求，有价请求任何时候都放行。
func TestCheckChannelPricingRestriction_RuntimeUnpricedPolicy(t *testing.T) {
	ctx := context.Background()
	gid := int64(1)
	for _, policy := range []string{BillingUnpricedPolicyObserve, BillingUnpricedPolicyBlockAllowlist} {
		f := newSPFixture(t, v2Snap(allowlistConfig, 1, mpInherit("no-such-model-xyz"), mpCustom("gpt-5.4", 1e-6)))
		settings := NewSettingService(&brSettingRepo{value: policy}, nil)
		bs := newTestBillingService()
		openai := &OpenAIGatewayService{billingService: bs, policyOverride: f.staged, settingService: settings}
		gw := &GatewayService{billingService: bs, policyOverride: f.staged, settingService: settings}
		blocked := policy == BillingUnpricedPolicyBlockAllowlist

		require.Equal(t, blocked, openai.checkChannelPricingRestriction(ctx, &gid, "no-such-model-xyz"), policy)
		require.Equal(t, blocked, gw.checkChannelPricingRestriction(ctx, &gid, "no-such-model-xyz"), policy)
		require.False(t, openai.checkChannelPricingRestriction(ctx, &gid, "gpt-5.4"), policy)
		require.False(t, gw.checkChannelPricingRestriction(ctx, &gid, "gpt-5.4"), policy)
		// 不在白名单里的模型：准入阶段就被拦，与开关无关。
		require.True(t, openai.checkChannelPricingRestriction(ctx, &gid, "not-listed"), policy)
	}
}
