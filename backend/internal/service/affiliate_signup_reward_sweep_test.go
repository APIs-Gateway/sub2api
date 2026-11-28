//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 「被邀请人累计实际消费达标后才给邀请人发注册奖励」的单测。
//
// 门槛 affiliate_signup_reward_min_spend：0 = 注册绑定即发（原行为）；>0 = 由后台扫描
// 在被邀请人累计消费 ≥ 门槛时发一次。生效起点 affiliate_signup_reward_min_spend_since 为空时扫描不工作，
// 只扫 since 之后注册的被邀请人（SQL 条件，见仓储集成测试）。
// 这里盯：门槛 0 不变、读门槛失败不退回注册即发、未达标不发、达标只发一次、重复扫描不重复发、
// 开关关闭不发、重新打开后补发、额度/人民币混合换算、游标分页、批失败降级且游标推进、Stop 及时返回。
//
// 不涉及 SQLite 时间比较，所以不需要处理 UTC / 上海时区的老坑。

var testSweepSince = time.Date(2026, 11, 28, 14, 1, 0, 0, time.UTC)

const testSweepSinceStr = "2026-11-28T14:01:00Z"

func minSpendSettings(amount, minSpend string) map[string]string {
	s := signupRewardSettings(amount)
	s[SettingKeyAffiliateSignupRewardMinSpend] = minSpend
	s[SettingKeyAffiliateSignupRewardMinSpendSince] = testSweepSinceStr
	return s
}

func usdSum(userID int64, cost float64) UsageCostSum {
	return UsageCostSum{UserID: userID, ActualCost: cost}
}

const sweepLimit = 100

// TestAccrueSignupReward_MinSpendZero_StillImmediate 门槛 0（含没配这个键、值为空）维持注册即发。
func TestAccrueSignupReward_MinSpendZero_StillImmediate(t *testing.T) {
	t.Parallel()

	noKey := signupRewardSettings("50")
	for name, settings := range map[string]map[string]string{
		"显式配 0": minSpendSettings("50", "0"),
		"键不存在":  noKey,
		"值为空":   minSpendSettings("50", ""),
	} {
		t.Run(name, func(t *testing.T) {
			aff := &pointsEarnAffiliateRepo{summaries: inviteePair(7, 42, nil)}
			prepo := &pointsEarnRepo{}
			svc := newEarnPointsService(settings, aff, prepo)

			got, err := svc.AccrueSignupReward(context.Background(), 7)
			require.NoError(t, err)
			require.Equal(t, int64(50), got)
			require.Len(t, prepo.signupCalls, 1)
		})
	}
}

// failingMinSpendRepo 让读门槛这一个键返回指定错误，其余键照常。
type failingMinSpendRepo struct {
	pointsEarnSettingRepo
	err error
}

func (r *failingMinSpendRepo) GetValue(ctx context.Context, key string) (string, error) {
	if key == SettingKeyAffiliateSignupRewardMinSpend {
		return "", r.err
	}
	return r.pointsEarnSettingRepo.GetValue(ctx, key)
}

// TestAccrueSignupReward_MinSpendReadFailureDoesNotFallBackToImmediate 读门槛失败 / 值写坏时绝不退回「注册即发」。
// 只有键不存在才算 0。
func TestAccrueSignupReward_MinSpendReadFailureDoesNotFallBackToImmediate(t *testing.T) {
	t.Parallel()

	boom := errors.New("redis/pg timeout")

	t.Run("读库出错", func(t *testing.T) {
		setSvc := &SettingService{settingRepo: &failingMinSpendRepo{
			pointsEarnSettingRepo: pointsEarnSettingRepo{values: signupRewardSettings("50")},
			err:                   boom,
		}}
		aff := &pointsEarnAffiliateRepo{summaries: inviteePair(7, 42, nil)}
		prepo := &pointsEarnRepo{}
		svc := &PointsService{repo: prepo, settingService: setSvc, affiliateService: NewAffiliateService(aff, setSvc, nil, nil)}

		got, err := svc.AccrueSignupReward(context.Background(), 7)
		require.ErrorIs(t, err, boom)
		require.Zero(t, got)
		require.Empty(t, prepo.signupCalls, "读不到门槛不能当成没有门槛")
	})

	for name, raw := range map[string]string{"值写坏": "abc", "值为负": "-1", "NaN": "NaN", "Inf": "Inf"} {
		t.Run(name, func(t *testing.T) {
			prepo := &pointsEarnRepo{}
			svc := newEarnPointsService(minSpendSettings("50", raw), &pointsEarnAffiliateRepo{summaries: inviteePair(7, 42, nil)}, prepo)

			got, err := svc.AccrueSignupReward(context.Background(), 7)
			require.Error(t, err)
			require.Zero(t, got)
			require.Empty(t, prepo.signupCalls)
		})
	}

	t.Run("扫描侧同样不发", func(t *testing.T) {
		prepo := &pointsEarnRepo{
			sweepCandidates: []SignupRewardCandidate{{InviteeID: 3, InviterID: 200}},
			sweepSums:       []UsageCostSum{usdSum(3, 99)},
		}
		svc := newEarnPointsService(minSpendSettings("5", "abc"), &pointsEarnAffiliateRepo{}, prepo)
		next, granted, err := svc.SweepSignupRewards(context.Background(), 0, sweepLimit)
		require.Error(t, err)
		require.Zero(t, granted)
		require.Zero(t, next)
		require.Empty(t, prepo.signupCalls)
		require.Zero(t, prepo.sweepListCalls)
	})
}

// TestAccrueSignupReward_MinSpendPositive_DefersToSweep 门槛 >0 时注册流程里不发，
// 也不去查任何消费数据（热路径零额外查询）。
func TestAccrueSignupReward_MinSpendPositive_DefersToSweep(t *testing.T) {
	t.Parallel()

	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(7, 42, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(minSpendSettings("50", "1"), aff, prepo)

	got, err := svc.AccrueSignupReward(context.Background(), 7)
	require.NoError(t, err)
	require.Zero(t, got)
	require.Empty(t, prepo.signupCalls, "未达标不能在注册时发")
	require.Zero(t, prepo.sweepSumCalls, "注册路径不能做消费聚合")
	require.Zero(t, aff.ensureN, "注册路径在门槛>0 时应当尽早返回")
}

// TestSweepSignupRewards_BelowThresholdNotGranted_AtThresholdGranted 未达标不发，达标（含恰好等于）才发。
func TestSweepSignupRewards_BelowThresholdNotGranted_AtThresholdGranted(t *testing.T) {
	t.Parallel()

	prepo := &pointsEarnRepo{
		sweepCandidates: []SignupRewardCandidate{
			{InviteeID: 1, InviterID: 100}, // 没有任何用量
			{InviteeID: 2, InviterID: 100}, // 0.99，差一点
			{InviteeID: 3, InviterID: 200}, // 恰好 1.00
			{InviteeID: 4, InviterID: 200}, // 远超
		},
		sweepSums: []UsageCostSum{usdSum(2, 0.99), usdSum(3, 1.0), usdSum(4, 12.5)},
	}
	svc := newEarnPointsService(minSpendSettings("5", "1"), &pointsEarnAffiliateRepo{}, prepo)

	next, granted, err := svc.SweepSignupRewards(context.Background(), 0, sweepLimit)
	require.NoError(t, err)
	require.Equal(t, 2, granted)
	require.Zero(t, next, "本批不满 limit 说明扫到末尾，游标回绕")
	require.Equal(t, testSweepSince, prepo.sweepLastSince, "候选查询必须带上生效起点")

	require.Len(t, prepo.signupCalls, 2)
	require.Equal(t, SignupRewardInput{InviterID: 200, SourceUserID: 3, Points: 5, PegAt: 0.01}, prepo.signupCalls[0])
	require.Equal(t, int64(4), prepo.signupCalls[1].SourceUserID)
	require.Equal(t, int64(200), prepo.signupCalls[1].InviterID, "拿到积分的是邀请人")
}

// TestSweepSignupRewards_EmptySinceDoesNotScan since 为空 / 写坏时扫描不工作，也不查库。
func TestSweepSignupRewards_EmptySinceDoesNotScan(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(map[string]string){
		"键不存在": func(s map[string]string) { delete(s, SettingKeyAffiliateSignupRewardMinSpendSince) },
		"值为空":  func(s map[string]string) { s[SettingKeyAffiliateSignupRewardMinSpendSince] = "" },
	} {
		t.Run(name, func(t *testing.T) {
			settings := minSpendSettings("5", "1")
			mutate(settings)
			prepo := &pointsEarnRepo{
				sweepCandidates: []SignupRewardCandidate{{InviteeID: 3, InviterID: 200}},
				sweepSums:       []UsageCostSum{usdSum(3, 99)},
			}
			svc := newEarnPointsService(settings, &pointsEarnAffiliateRepo{}, prepo)

			next, granted, err := svc.SweepSignupRewards(context.Background(), 0, sweepLimit)
			require.NoError(t, err)
			require.Zero(t, next)
			require.Zero(t, granted)
			require.Zero(t, prepo.sweepListCalls, "since 为空时扫描不工作，不该碰数据库")
			require.Empty(t, prepo.signupCalls)
		})
	}

	t.Run("值写坏", func(t *testing.T) {
		settings := minSpendSettings("5", "1")
		settings[SettingKeyAffiliateSignupRewardMinSpendSince] = "yesterday"
		prepo := &pointsEarnRepo{sweepCandidates: []SignupRewardCandidate{{InviteeID: 3, InviterID: 200}}}
		svc := newEarnPointsService(settings, &pointsEarnAffiliateRepo{}, prepo)
		_, granted, err := svc.SweepSignupRewards(context.Background(), 0, sweepLimit)
		require.Error(t, err)
		require.Zero(t, granted)
		require.Zero(t, prepo.sweepListCalls)
	})
}

// TestSweepSignupRewards_MixedCreditUnits 累计消费按行换算成站点当前单位后再和门槛比。
// 额度行（cost_unit NULL）与人民币行（cost_unit=1）分开处理，换算走 CreditUnit.UsageAmountToCurrent。
func TestSweepSignupRewards_MixedCreditUnits(t *testing.T) {
	t.Parallel()

	cnyRow := func(id int64, cost float64) UsageCostSum {
		return UsageCostSum{UserID: id, CostUnitCNY: true, ActualCost: cost}
	}
	cnyMode := CreditUnit{Currency: CreditCurrencyCNY, LegacyDivisor: 13}
	usdMode := CreditUnit{Currency: CreditCurrencyUSD, LegacyDivisor: 13}

	cases := []struct {
		name      string
		unit      CreditUnit
		minSpend  string
		sums      []UsageCostSum
		wantGrant bool
	}{
		// 当前人民币：额度行 ÷13，人民币行原样。13 额度 = ¥1。
		{"人民币模式：13 额度 = ¥1，达标", cnyMode, "1", []UsageCostSum{usdSum(3, 13)}, true},
		{"人民币模式：12 额度 < ¥1，不达标", cnyMode, "1", []UsageCostSum{usdSum(3, 12)}, false},
		{"人民币模式：额度 6.5(=¥0.5) + 人民币 ¥0.5 = ¥1，混合达标", cnyMode, "1", []UsageCostSum{usdSum(3, 6.5), cnyRow(3, 0.5)}, true},
		{"人民币模式：额度 6.5(=¥0.5) + 人民币 ¥0.4 = ¥0.9，混合不达标", cnyMode, "1", []UsageCostSum{usdSum(3, 6.5), cnyRow(3, 0.4)}, false},
		{"人民币模式：只有人民币行 ¥1 原样达标", cnyMode, "1", []UsageCostSum{cnyRow(3, 1)}, true},
		{"人民币模式：额度行不能被当成人民币（5 额度 ≠ ¥5）", cnyMode, "1", []UsageCostSum{usdSum(3, 5)}, false},
		// 当前额度（USD 站）：额度行原样，人民币行（切换后回滚的残留）×divisor。
		{"额度模式：额度行原样，1.0 达标", usdMode, "1", []UsageCostSum{usdSum(3, 1)}, true},
		{"额度模式：额度 0.5 + 人民币 ¥0.04(=0.52 额度) 混合达标", usdMode, "1", []UsageCostSum{usdSum(3, 0.5), cnyRow(3, 0.04)}, true},
		{"额度模式：额度 0.5 + 人民币 ¥0.03(=0.39 额度) 混合不达标", usdMode, "1", []UsageCostSum{usdSum(3, 0.5), cnyRow(3, 0.03)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prepo := &pointsEarnRepo{
				sweepCandidates: []SignupRewardCandidate{{InviteeID: 3, InviterID: 200}},
				sweepSums:       tc.sums,
			}
			svc := newEarnPointsService(minSpendSettings("5", tc.minSpend), &pointsEarnAffiliateRepo{}, prepo)
			unit := tc.unit
			svc.creditUnitFn = func() CreditUnit { return unit }

			_, granted, err := svc.SweepSignupRewards(context.Background(), 0, sweepLimit)
			require.NoError(t, err)
			if tc.wantGrant {
				require.Equal(t, 1, granted)
			} else {
				require.Zero(t, granted)
				require.Empty(t, prepo.signupCalls)
			}
		})
	}
}

// TestSweepSignupRewards_RepeatedSweepDoesNotDuplicate 重复扫描不重复发。
func TestSweepSignupRewards_RepeatedSweepDoesNotDuplicate(t *testing.T) {
	t.Parallel()

	prepo := &pointsEarnRepo{
		sweepCandidates: []SignupRewardCandidate{{InviteeID: 3, InviterID: 200}},
		sweepSums:       []UsageCostSum{usdSum(3, 3)},
	}
	svc := newEarnPointsService(minSpendSettings("5", "1"), &pointsEarnAffiliateRepo{}, prepo)
	ctx := context.Background()

	_, g1, err := svc.SweepSignupRewards(ctx, 0, sweepLimit)
	require.NoError(t, err)
	require.Equal(t, 1, g1)

	_, g2, err := svc.SweepSignupRewards(ctx, 0, sweepLimit)
	require.NoError(t, err)
	require.Zero(t, g2, "第二轮名单里已经没有他")
	require.Len(t, prepo.signupCalls, 1, "只入过一次账")

	// 即便名单口径出错（比如另一个实例同时在扫），按被邀请人的唯一索引也只入账一次。
	again, err := svc.grantSignupReward(ctx, 200, 3, 5)
	require.NoError(t, err)
	require.Zero(t, again)
}

// TestSweepSignupRewards_NoopWhenSwitchesOff 开关、金额、门槛任何一项不满足都不发，也不查库。
func TestSweepSignupRewards_NoopWhenSwitchesOff(t *testing.T) {
	t.Parallel()

	cases := map[string]func(map[string]string){
		"积分总开关关闭": func(s map[string]string) { s[SettingKeyPointsEnabled] = "false" },
		"奖励开关关闭":  func(s map[string]string) { s[SettingKeyAffiliateSignupRewardEnabled] = "false" },
		"奖励金额为 0": func(s map[string]string) { s[SettingKeyAffiliateSignupRewardAmount] = "0" },
		"门槛为 0":   func(s map[string]string) { s[SettingKeyAffiliateSignupRewardMinSpend] = "0" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			settings := minSpendSettings("5", "1")
			mutate(settings)
			prepo := &pointsEarnRepo{
				sweepCandidates: []SignupRewardCandidate{{InviteeID: 3, InviterID: 200}},
				sweepSums:       []UsageCostSum{usdSum(3, 99)},
			}
			svc := newEarnPointsService(settings, &pointsEarnAffiliateRepo{}, prepo)

			next, granted, err := svc.SweepSignupRewards(context.Background(), 0, sweepLimit)
			require.NoError(t, err)
			require.Zero(t, next)
			require.Zero(t, granted)
			require.Empty(t, prepo.signupCalls)
			require.Zero(t, prepo.sweepListCalls, "条件不满足时不该碰数据库")
			require.Zero(t, prepo.sweepSumCalls)
		})
	}
}

// TestSweepSignupRewards_BackfillAfterReenable 开关关闭期间已达标的人，重新打开后补发。
func TestSweepSignupRewards_BackfillAfterReenable(t *testing.T) {
	t.Parallel()

	settings := minSpendSettings("5", "1")
	settings[SettingKeyAffiliateSignupRewardEnabled] = "false"
	prepo := &pointsEarnRepo{
		sweepCandidates: []SignupRewardCandidate{
			{InviteeID: 3, InviterID: 200}, // 关闭期间已达标
			{InviteeID: 4, InviterID: 200}, // 一直没达标
		},
		sweepSums: []UsageCostSum{usdSum(3, 2), usdSum(4, 0.2)},
	}
	svc := newEarnPointsService(settings, &pointsEarnAffiliateRepo{}, prepo)
	ctx := context.Background()

	_, granted, err := svc.SweepSignupRewards(ctx, 0, sweepLimit)
	require.NoError(t, err)
	require.Zero(t, granted, "开关关闭期间不发")
	require.Empty(t, prepo.signupCalls)

	// 管理员重新打开开关（settings 是同一个 map，等价于库里改值）
	settings[SettingKeyAffiliateSignupRewardEnabled] = "true"
	_, granted, err = svc.SweepSignupRewards(ctx, 0, sweepLimit)
	require.NoError(t, err)
	require.Equal(t, 1, granted, "只补发已达标的那一个")
	require.Len(t, prepo.signupCalls, 1)
	require.Equal(t, int64(3), prepo.signupCalls[0].SourceUserID)

	// 之后他继续消费，达标了，下一轮也补上
	prepo.sweepSums = []UsageCostSum{usdSum(3, 2), usdSum(4, 1.5)}
	_, granted, err = svc.SweepSignupRewards(ctx, 0, sweepLimit)
	require.NoError(t, err)
	require.Equal(t, 1, granted)
	require.Len(t, prepo.signupCalls, 2)
}

// TestSweepSignupRewards_CursorPaging 游标分页：每批最多 limit 个，翻页不漏人，扫完回绕为 0。
func TestSweepSignupRewards_CursorPaging(t *testing.T) {
	t.Parallel()

	prepo := &pointsEarnRepo{
		sweepCandidates: []SignupRewardCandidate{
			{InviteeID: 10, InviterID: 1}, {InviteeID: 20, InviterID: 1}, {InviteeID: 30, InviterID: 1},
		},
		// 只有最后一个达标；前两批没人达标，游标必须能推进过去
		sweepSums: []UsageCostSum{usdSum(30, 5)},
	}
	svc := newEarnPointsService(minSpendSettings("5", "1"), &pointsEarnAffiliateRepo{}, prepo)
	ctx := context.Background()

	next, granted, err := svc.SweepSignupRewards(ctx, 0, 2)
	require.NoError(t, err)
	require.Equal(t, int64(20), next)
	require.Zero(t, granted)

	next, granted, err = svc.SweepSignupRewards(ctx, next, 2)
	require.NoError(t, err)
	require.Zero(t, next, "只剩 1 个不满 limit，回绕")
	require.Equal(t, 1, granted)
	require.Equal(t, int64(30), prepo.signupCalls[0].SourceUserID)
}

// TestSweepSignupRewards_BatchSumFailureFallsBackAndAdvancesCursor 一批求和失败时降级成逐人查：
// 单人失败就跳过，其他人照常判定，游标照常推进到本批末尾，不停在原地。
func TestSweepSignupRewards_BatchSumFailureFallsBackAndAdvancesCursor(t *testing.T) {
	t.Parallel()

	prepo := &pointsEarnRepo{
		sweepCandidates: []SignupRewardCandidate{
			{InviteeID: 10, InviterID: 1}, {InviteeID: 20, InviterID: 1}, {InviteeID: 30, InviterID: 1},
		},
		sweepSums:       []UsageCostSum{usdSum(10, 5), usdSum(20, 5), usdSum(30, 5)},
		sweepSumFailIDs: map[int64]bool{20: true}, // 20 号的数据让整批查询超时，单查他自己也超时
	}
	svc := newEarnPointsService(minSpendSettings("5", "1"), &pointsEarnAffiliateRepo{}, prepo)

	next, granted, err := svc.SweepSignupRewards(context.Background(), 0, 3)
	require.NoError(t, err, "单个被邀请人的求和失败不是整轮的错误")
	require.Equal(t, int64(30), next, "游标必须推进到本批末尾，不能停在原地反复重跑")
	require.Equal(t, 2, granted, "其余两人照常发放")
	got := []int64{prepo.signupCalls[0].SourceUserID, prepo.signupCalls[1].SourceUserID}
	require.ElementsMatch(t, []int64{10, 30}, got)
	require.NotContains(t, got, int64(20), "查不出累计消费的人本轮跳过，不能当 0 也不能当达标")
}

// TestSweepSignupRewards_FallbackGivesUpAfterConsecutiveFailures 数据库整体不可用时不为每个人各等一次超时。
func TestSweepSignupRewards_FallbackGivesUpAfterConsecutiveFailures(t *testing.T) {
	t.Parallel()

	var cands []SignupRewardCandidate
	for i := int64(1); i <= 50; i++ {
		cands = append(cands, SignupRewardCandidate{InviteeID: i, InviterID: 1000})
	}
	prepo := &pointsEarnRepo{sweepCandidates: cands, sweepSumErr: errors.New("db is down")}
	svc := newEarnPointsService(minSpendSettings("5", "1"), &pointsEarnAffiliateRepo{}, prepo)

	next, granted, err := svc.SweepSignupRewards(context.Background(), 0, 50)
	require.NoError(t, err)
	require.Zero(t, granted)
	require.Equal(t, int64(50), next, "游标必须推进到本批末尾，而不是停在原地")
	require.LessOrEqual(t, prepo.sweepSumCalls, 1+signupRewardSweepMaxFallbackFailures, "连续失败后必须放弃剩下的人")
	require.Empty(t, prepo.signupCalls)
}

// TestSweepSignupRewards_ListErrorKeepsCursor 名单查询失败往上抛且游标不前进；单个发放失败不拦住同批其他人。
func TestSweepSignupRewards_ListErrorKeepsCursor(t *testing.T) {
	t.Parallel()

	boom := errors.New("db is down")
	ctx := context.Background()

	t.Run("名单查询失败", func(t *testing.T) {
		prepo := &pointsEarnRepo{sweepListErr: boom}
		svc := newEarnPointsService(minSpendSettings("5", "1"), &pointsEarnAffiliateRepo{}, prepo)
		next, granted, err := svc.SweepSignupRewards(ctx, 77, sweepLimit)
		require.ErrorIs(t, err, boom)
		require.Equal(t, int64(77), next)
		require.Zero(t, granted)
	})

	t.Run("发放失败不抛错、游标照常推进", func(t *testing.T) {
		prepo := &pointsEarnRepo{
			sweepCandidates: []SignupRewardCandidate{{InviteeID: 3, InviterID: 200}},
			sweepSums:       []UsageCostSum{usdSum(3, 9)},
			signupErr:       boom,
		}
		svc := newEarnPointsService(minSpendSettings("5", "1"), &pointsEarnAffiliateRepo{}, prepo)
		next, granted, err := svc.SweepSignupRewards(ctx, 0, sweepLimit)
		require.NoError(t, err)
		require.Zero(t, next)
		require.Zero(t, granted)
		require.Len(t, prepo.signupCalls, 1, "确实尝试过发放")
	})
}

// TestSweepSignupRewards_SkipsDegenerateRows 自邀请 / 非法 ID 的行直接跳过。
func TestSweepSignupRewards_SkipsDegenerateRows(t *testing.T) {
	t.Parallel()

	prepo := &pointsEarnRepo{
		sweepCandidates: []SignupRewardCandidate{
			{InviteeID: 5, InviterID: 5},
			{InviteeID: 6, InviterID: 0},
		},
		sweepSums: []UsageCostSum{usdSum(5, 9), usdSum(6, 9)},
	}
	svc := newEarnPointsService(minSpendSettings("5", "1"), &pointsEarnAffiliateRepo{}, prepo)

	_, granted, err := svc.SweepSignupRewards(context.Background(), 0, sweepLimit)
	require.NoError(t, err)
	require.Zero(t, granted)
	require.Empty(t, prepo.signupCalls)
}

// TestGetAffiliateSignupRewardMinSpend_Parses 设置读取：numeric 字面量、上限夹紧；
// 键不存在 / 为空才是 0，写坏是 error。
func TestGetAffiliateSignupRewardMinSpend_Parses(t *testing.T) {
	t.Parallel()

	ok := map[string]float64{
		"1":             1,
		"1.00000000":    1,
		" 2.5 ":         2.5,
		"0":             0,
		"":              0,
		"99999999999.0": AffiliateSignupRewardMinSpendMax,
	}
	for raw, want := range ok {
		t.Run("raw="+raw, func(t *testing.T) {
			svc := &SettingService{settingRepo: &pointsEarnSettingRepo{values: map[string]string{
				SettingKeyAffiliateSignupRewardMinSpend: raw,
			}}}
			got, err := svc.GetAffiliateSignupRewardMinSpend(context.Background())
			require.NoError(t, err)
			require.InDelta(t, want, got, 1e-9)
		})
	}
	for _, raw := range []string{"-3", "abc", "NaN", "Inf"} {
		t.Run("bad="+raw, func(t *testing.T) {
			svc := &SettingService{settingRepo: &pointsEarnSettingRepo{values: map[string]string{
				SettingKeyAffiliateSignupRewardMinSpend: raw,
			}}}
			_, err := svc.GetAffiliateSignupRewardMinSpend(context.Background())
			require.Error(t, err)
		})
	}

	empty := &SettingService{settingRepo: &pointsEarnSettingRepo{values: map[string]string{}}}
	got, err := empty.GetAffiliateSignupRewardMinSpend(context.Background())
	require.NoError(t, err)
	require.Zero(t, got, "没配这个键的老站点行为不变")
}

// TestNormalizeSignupRewardSince 生效起点规整成 UTC RFC3339；空合法；写坏不合法。
func TestNormalizeSignupRewardSince(t *testing.T) {
	t.Parallel()

	got, ok := NormalizeSignupRewardSince("2026-11-28T22:01:00+08:00")
	require.True(t, ok)
	require.Equal(t, "2026-11-28T14:01:00Z", got)

	got, ok = NormalizeSignupRewardSince("  ")
	require.True(t, ok)
	require.Empty(t, got)

	_, ok = NormalizeSignupRewardSince("2026-11-28")
	require.False(t, ok)
}

// TestAffiliateSignupRewardSweeper_RunOnce 后台循环一轮：发放达标的人，扫完后游标回绕，Stop 幂等。
func TestAffiliateSignupRewardSweeper_RunOnce(t *testing.T) {
	t.Parallel()

	prepo := &pointsEarnRepo{
		sweepCandidates: []SignupRewardCandidate{{InviteeID: 3, InviterID: 200}, {InviteeID: 4, InviterID: 200}},
		sweepSums:       []UsageCostSum{usdSum(3, 2), usdSum(4, 0.1)},
	}
	pts := newEarnPointsService(minSpendSettings("5", "1"), &pointsEarnAffiliateRepo{}, prepo)
	sw := NewAffiliateSignupRewardSweeper(pts, signupRewardSweepInterval)

	sw.runOnce()
	require.Len(t, prepo.signupCalls, 1)
	require.Equal(t, int64(3), prepo.signupCalls[0].SourceUserID)
	require.Zero(t, sw.cursor, "扫到末尾后游标回绕，没达标的人下一轮还会被看到")

	prepo.sweepSums = []UsageCostSum{usdSum(3, 2), usdSum(4, 3)}
	sw.runOnce()
	require.Len(t, prepo.signupCalls, 2)

	sw.Stop()
	sw.Stop()
}

// TestAffiliateSignupRewardSweeper_StopInterruptsInFlightRun Stop 能及时返回：
// 即使这一轮正卡在数据库查询里，也会被 stopCtx 打断，而不是等整轮超时（2 分钟）。
func TestAffiliateSignupRewardSweeper_StopInterruptsInFlightRun(t *testing.T) {
	t.Parallel()

	prepo := &pointsEarnRepo{sweepListBlock: true}
	pts := newEarnPointsService(minSpendSettings("5", "1"), &pointsEarnAffiliateRepo{}, prepo)
	sw := NewAffiliateSignupRewardSweeper(pts, 5*time.Millisecond)
	sw.Start()

	require.Eventually(t, func() bool { return prepo.listCalled() }, 2*time.Second, 5*time.Millisecond, "后台循环应当已经进到卡住的查询里")

	done := make(chan struct{})
	go func() { sw.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 没有及时返回：正在进行的那一轮没有被取消")
	}
}

// TestAffiliateSignupRewardSweeper_PanicDoesNotCrash 一轮里 panic 只记日志，循环和进程都还活着。
func TestAffiliateSignupRewardSweeper_PanicDoesNotCrash(t *testing.T) {
	t.Parallel()

	prepo := &pointsEarnRepo{sweepListPanic: true}
	pts := newEarnPointsService(minSpendSettings("5", "1"), &pointsEarnAffiliateRepo{}, prepo)
	sw := NewAffiliateSignupRewardSweeper(pts, 5*time.Millisecond)

	require.NotPanics(t, func() { sw.safeRunOnce() })

	sw.Start()
	require.Eventually(t, func() bool { return prepo.listCalls() >= 3 }, 2*time.Second, 5*time.Millisecond,
		"panic 之后后台循环应当继续按周期再跑")
	sw.Stop()
}
