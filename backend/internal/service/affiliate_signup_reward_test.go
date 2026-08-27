//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// 「邀请注册即得积分」的单测。
//
// 这条路径与按订单/兑换码返积分的根本区别是：它在被邀请人还没有花过一分钱的时候就发放。
// 因此这里重点盯三件事——该发的时候发对了人和数目、不该发的时候一律不发、
// 同一个被邀请人绝不会发第二次。

// signupRewardSettings 在 earn 那组默认设置之上打开「注册即得积分」。
func signupRewardSettings(amount string) map[string]string {
	s := pointsEarnDefaults()
	s[SettingKeyAffiliateSignupRewardEnabled] = "true"
	s[SettingKeyAffiliateSignupRewardAmount] = amount
	return s
}

// TestAccrueSignupReward_GrantsToInviter 主路径：奖励发给邀请人，来源锚是被邀请人。
func TestAccrueSignupReward_GrantsToInviter(t *testing.T) {
	t.Parallel()

	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(7, 42, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(signupRewardSettings("50"), aff, prepo)

	got, err := svc.AccrueSignupReward(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(50), got)

	require.Len(t, prepo.signupCalls, 1)
	in := prepo.signupCalls[0]
	require.Equal(t, int64(42), in.InviterID, "拿到积分的必须是邀请人")
	require.Equal(t, int64(7), in.SourceUserID, "来源锚必须是被邀请人")
	require.Equal(t, int64(50), in.Points)
	require.InDelta(t, 0.01, in.PegAt, 1e-9, "应记录当时的积分面值以便审计")
}

// TestAccrueSignupReward_NotGrantedWhenOff 覆盖所有「不该发」的开关组合。
// 这些都是正常状态，必须安静地返回 0 而不是报错。
func TestAccrueSignupReward_NotGrantedWhenOff(t *testing.T) {
	t.Parallel()

	cases := map[string]func(map[string]string){
		"积分总开关关闭": func(s map[string]string) { s[SettingKeyPointsEnabled] = "false" },
		"奖励开关关闭":  func(s map[string]string) { s[SettingKeyAffiliateSignupRewardEnabled] = "false" },
		"奖励金额为 0": func(s map[string]string) { s[SettingKeyAffiliateSignupRewardAmount] = "0" },
		"奖励金额为负":  func(s map[string]string) { s[SettingKeyAffiliateSignupRewardAmount] = "-5" },
		"奖励金额写坏":  func(s map[string]string) { s[SettingKeyAffiliateSignupRewardAmount] = "abc" },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			settings := signupRewardSettings("50")
			mutate(settings)
			aff := &pointsEarnAffiliateRepo{summaries: inviteePair(7, 42, nil)}
			prepo := &pointsEarnRepo{}
			svc := newEarnPointsService(settings, aff, prepo)

			got, err := svc.AccrueSignupReward(context.Background(), 7)
			require.NoError(t, err)
			require.Zero(t, got)
			require.Empty(t, prepo.signupCalls, "开关关闭时不该碰仓储")
		})
	}
}

// TestAccrueSignupReward_RequiresInviter 没有邀请人就没有奖励可发。
func TestAccrueSignupReward_RequiresInviter(t *testing.T) {
	t.Parallel()

	t.Run("用户没有邀请人", func(t *testing.T) {
		// summaries 里没有这个用户 → EnsureUserAffiliate 返回一个 InviterID 为 nil 的档案
		aff := &pointsEarnAffiliateRepo{summaries: map[int64]*AffiliateSummary{}}
		prepo := &pointsEarnRepo{}
		svc := newEarnPointsService(signupRewardSettings("50"), aff, prepo)

		got, err := svc.AccrueSignupReward(context.Background(), 7)
		require.NoError(t, err)
		require.Zero(t, got)
		require.Empty(t, prepo.signupCalls)
	})

	t.Run("自己邀请自己", func(t *testing.T) {
		self := int64(7)
		aff := &pointsEarnAffiliateRepo{summaries: map[int64]*AffiliateSummary{
			self: {UserID: self, InviterID: &self},
		}}
		prepo := &pointsEarnRepo{}
		svc := newEarnPointsService(signupRewardSettings("50"), aff, prepo)

		got, err := svc.AccrueSignupReward(context.Background(), self)
		require.NoError(t, err)
		require.Zero(t, got)
		require.Empty(t, prepo.signupCalls, "自邀请是刷分的最短路径，必须在发放前挡掉")
	})

	t.Run("用户 ID 非法", func(t *testing.T) {
		prepo := &pointsEarnRepo{}
		svc := newEarnPointsService(signupRewardSettings("50"), &pointsEarnAffiliateRepo{}, prepo)

		got, err := svc.AccrueSignupReward(context.Background(), 0)
		require.NoError(t, err)
		require.Zero(t, got)
		require.Empty(t, prepo.signupCalls)
	})
}

// TestAccrueSignupReward_OncePerInvitee 是这组测试里最关键的一条：
// 同一个被邀请人重复触发只发一次。注册流程重放、绑定接口被重复调用都会走到这里。
func TestAccrueSignupReward_OncePerInvitee(t *testing.T) {
	t.Parallel()

	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(7, 42, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(signupRewardSettings("50"), aff, prepo)
	ctx := context.Background()

	first, err := svc.AccrueSignupReward(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, int64(50), first)

	second, err := svc.AccrueSignupReward(ctx, 7)
	require.NoError(t, err, "重复触发不是错误")
	require.Zero(t, second, "第二次必须什么都不发")

	require.Len(t, prepo.signupCalls, 2, "两次都该问过仓储，由唯一索引来兜底")
}

// TestAccrueSignupReward_PropagatesRepoError 仓储出错必须往上抛，
// 不能用「没发放」冒充「发放失败」——那会让一次真实的故障被静默吞掉。
func TestAccrueSignupReward_PropagatesRepoError(t *testing.T) {
	t.Parallel()

	boom := errors.New("db is down")
	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(7, 42, nil)}
	prepo := &pointsEarnRepo{signupErr: boom}
	svc := newEarnPointsService(signupRewardSettings("50"), aff, prepo)

	got, err := svc.AccrueSignupReward(context.Background(), 7)
	require.ErrorIs(t, err, boom)
	require.Zero(t, got)
}

// TestAccrueSignupReward_ClampsToMax 防止手滑填出天文数字。
func TestAccrueSignupReward_ClampsToMax(t *testing.T) {
	t.Parallel()

	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(7, 42, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(signupRewardSettings("99999999"), aff, prepo)

	got, err := svc.AccrueSignupReward(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(AffiliateSignupRewardAmountMax), got)
}

// TestGetAffiliateSignupRewardAmount_ParsesNumericLiteral 钉死一个真实的线上坑。
//
// 这个键在历史部署里是以 numeric 形式落库的，取出来长成 "50.00000000"。
// 用 strconv.Atoi 解析会直接失败，把一个已经配好并且正在生效的奖励静默降为 0——
// 表面上功能还在，实际一分不发。
func TestGetAffiliateSignupRewardAmount_ParsesNumericLiteral(t *testing.T) {
	t.Parallel()

	cases := map[string]int64{
		"50.00000000": 50,
		"50":          50,
		"50.9":        50, // 积分是整数，小数部分向下取整丢弃
		"0":           0,
		"-1":          0,
		"":            0,
		"abc":         0,
	}

	for raw, want := range cases {
		t.Run("raw="+raw, func(t *testing.T) {
			svc := &SettingService{settingRepo: &pointsEarnSettingRepo{values: map[string]string{
				SettingKeyAffiliateSignupRewardAmount: raw,
			}}}
			require.Equal(t, want, svc.GetAffiliateSignupRewardAmount(context.Background()))
		})
	}
}

// TestGetAffiliateSignupRewardEnabled_DefaultsOff 默认必须关闭。
// 这笔奖励不要求被邀请人付出任何成本，误开就直接变成刷号的收益来源，
// 所以读不到或读坏都得回落到关闭。
func TestGetAffiliateSignupRewardEnabled_DefaultsOff(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"true":  true,
		" true": true,
		"false": false,
		"":      false,
		"1":     false, // 只认字面量 true，避免和别处的布尔口径分叉
	}

	for raw, want := range cases {
		t.Run("raw="+raw, func(t *testing.T) {
			svc := &SettingService{settingRepo: &pointsEarnSettingRepo{values: map[string]string{
				SettingKeyAffiliateSignupRewardEnabled: raw,
			}}}
			require.Equal(t, want, svc.GetAffiliateSignupRewardEnabled(context.Background()))
		})
	}

	// 键完全不存在时也必须是关闭
	empty := &SettingService{settingRepo: &pointsEarnSettingRepo{values: map[string]string{}}}
	require.False(t, empty.GetAffiliateSignupRewardEnabled(context.Background()))
	require.Zero(t, empty.GetAffiliateSignupRewardAmount(context.Background()))
}
