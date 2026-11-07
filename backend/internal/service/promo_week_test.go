//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

var promoTestLoc = time.FixedZone("CST", 8*3600)

// 活动窗口：2026-11-08 00:00 +08 起，2026-11-15 00:00 +08 止（不含）。
func promoTestValues() map[string]string {
	return map[string]string{
		SettingKeyPromoStartAt:                     "2026-11-08T00:00:00+08:00",
		SettingKeyPromoEndAt:                       "2026-11-15T00:00:00+08:00",
		SettingKeyPromoSubscriptionDailyMultiplier: "1.5",
		SettingKeyPromoBalanceRateMultiplier:       "0.52",
		SettingKeyPromoExcludedUserIDs:             "4242, 9",
	}
}

func setPromoForTest(t *testing.T, values map[string]string) {
	t.Helper()
	SetPromoSettingsSource(func(ctx context.Context, keys []string) (map[string]string, error) {
		out := map[string]string{}
		for _, k := range keys {
			if v, ok := values[k]; ok {
				out[k] = v
			}
		}
		return out, nil
	})
	t.Cleanup(func() { SetPromoSettingsSource(nil) })
}

func promoAt(day, hour int) time.Time {
	return time.Date(2026, 11, day, hour, 0, 0, 0, promoTestLoc)
}

// promoDailyFactor 取日限额的放大倍数（用 D=100 探测 PromoEffectiveLimits 的日限额）。
func promoDailyFactor(userID int64, now time.Time) float64 {
	d, _, _ := PromoEffectiveLimits(userID, now, 100, 0, 0)
	return d / 100
}

func TestPromoSubscriptionFactor_Window(t *testing.T) {
	setPromoForTest(t, promoTestValues())

	// 活动前一天 23:59 → 不生效
	require.Equal(t, 1.0, promoDailyFactor(1, promoAt(7, 23).Add(59*time.Minute)))
	// 开始日 00:00 与当天任意时刻 → 生效
	require.Equal(t, 1.5, promoDailyFactor(1, promoAt(8, 0)))
	require.Equal(t, 1.5, promoDailyFactor(1, promoAt(8, 23)))
	// 最后一天（14 日）整天生效
	require.Equal(t, 1.5, promoDailyFactor(1, promoAt(14, 23)))
	// 结束时刻（15 日 00:00，不含）起恢复
	require.Equal(t, 1.0, promoDailyFactor(1, promoAt(15, 0)))
	require.Equal(t, 1.0, promoDailyFactor(1, promoAt(20, 12)))
}

func TestPromoSubscriptionFactor_DayGranularity(t *testing.T) {
	// 活动从 8 日中午开始：8 日这个自然日的开始时刻(0 点)早于 START，整天不算；9 日起算。
	v := promoTestValues()
	v[SettingKeyPromoStartAt] = "2026-11-08T12:00:00+08:00"
	setPromoForTest(t, v)
	require.Equal(t, 1.0, promoDailyFactor(1, promoAt(8, 20)))
	require.Equal(t, 1.5, promoDailyFactor(1, promoAt(9, 0)))
}

func TestPromoSubscriptionFactor_Excluded(t *testing.T) {
	setPromoForTest(t, promoTestValues())
	require.Equal(t, 1.0, promoDailyFactor(4242, promoAt(9, 12)))
	require.Equal(t, 1.0, promoDailyFactor(9, promoAt(9, 12)))
	require.Equal(t, 1.5, promoDailyFactor(10, promoAt(9, 12)))
}

func TestPromoFactors_MissingOrInvalidSettingsAreOff(t *testing.T) {
	now := promoAt(9, 12)
	cases := map[string]map[string]string{
		"empty":         {},
		"no end":        {SettingKeyPromoStartAt: "2026-11-08T00:00:00+08:00", SettingKeyPromoSubscriptionDailyMultiplier: "1.5"},
		"bad start":     {SettingKeyPromoStartAt: "yesterday", SettingKeyPromoEndAt: "2026-11-15T00:00:00+08:00", SettingKeyPromoSubscriptionDailyMultiplier: "1.5"},
		"end<=start":    {SettingKeyPromoStartAt: "2026-11-15T00:00:00+08:00", SettingKeyPromoEndAt: "2026-11-08T00:00:00+08:00", SettingKeyPromoSubscriptionDailyMultiplier: "1.5"},
		"no tz":         {SettingKeyPromoStartAt: "2026-11-08T00:00:00", SettingKeyPromoEndAt: "2026-11-15T00:00:00", SettingKeyPromoSubscriptionDailyMultiplier: "1.5"},
		"no multiplier": {SettingKeyPromoStartAt: "2026-11-08T00:00:00+08:00", SettingKeyPromoEndAt: "2026-11-15T00:00:00+08:00"},
		"bad excluded":  {SettingKeyPromoStartAt: "2026-11-08T00:00:00+08:00", SettingKeyPromoEndAt: "2026-11-15T00:00:00+08:00", SettingKeyPromoSubscriptionDailyMultiplier: "1.5", SettingKeyPromoBalanceRateMultiplier: "0.52", SettingKeyPromoExcludedUserIDs: "4242,abc"},
	}
	for name, vals := range cases {
		t.Run(name, func(t *testing.T) {
			setPromoForTest(t, vals)
			require.Equal(t, 1.0, promoDailyFactor(1, now))
			require.Equal(t, 1.0, PromoBalanceRateFactor(1, now))
		})
	}
}

func TestPromoMultiplierBounds(t *testing.T) {
	now := promoAt(9, 12)
	// 契约「不生效」值（空、<=0、==1）：只关掉这一侧，另一侧不受影响
	for _, mult := range []string{"", "0", "-1", "1"} {
		v := promoTestValues()
		v[SettingKeyPromoSubscriptionDailyMultiplier] = mult
		setPromoForTest(t, v)
		require.Equal(t, 1.0, promoDailyFactor(1, now), "sub mult %q", mult)
		require.Equal(t, 0.52, PromoBalanceRateFactor(1, now), "balance side independent of sub mult %q", mult)
	}
	for _, rate := range []string{"", "0", "-0.5", "1"} {
		v := promoTestValues()
		v[SettingKeyPromoBalanceRateMultiplier] = rate
		setPromoForTest(t, v)
		require.Equal(t, 1.0, PromoBalanceRateFactor(1, now), "balance rate %q", rate)
		require.Equal(t, 1.5, promoDailyFactor(1, now), "sub side independent of rate %q", rate)
	}
	// 越界/无法解析：整份配置作废（从未有过有效配置 → 关闭）
	for _, mult := range []string{"0.8", "abc", "NaN", "15"} {
		v := promoTestValues()
		v[SettingKeyPromoSubscriptionDailyMultiplier] = mult
		setPromoForTest(t, v)
		require.Equal(t, 1.0, promoDailyFactor(1, now), "sub mult %q", mult)
		require.Equal(t, 1.0, PromoBalanceRateFactor(1, now), "invalid config rejected as a whole %q", mult)
	}
	for _, rate := range []string{"1.2", "abc"} {
		v := promoTestValues()
		v[SettingKeyPromoBalanceRateMultiplier] = rate
		setPromoForTest(t, v)
		require.Equal(t, 1.0, PromoBalanceRateFactor(1, now), "balance rate %q", rate)
		require.Equal(t, 1.0, promoDailyFactor(1, now), "invalid config rejected as a whole %q", rate)
	}
}

func TestParsePromoSettings_Status(t *testing.T) {
	p, st := parsePromoSettings(nil)
	require.Nil(t, p)
	require.Equal(t, promoParseOff, st)
	p, st = parsePromoSettings(map[string]string{SettingKeyPromoStartAt: "  ", SettingKeyPromoExcludedUserIDs: ""})
	require.Nil(t, p)
	require.Equal(t, promoParseOff, st, "全部为空白 = 有意关闭")
	// 时间有效、两侧倍率都「不生效」= 有意关闭
	v := promoTestValues()
	v[SettingKeyPromoSubscriptionDailyMultiplier] = "1"
	v[SettingKeyPromoBalanceRateMultiplier] = ""
	_, st = parsePromoSettings(v)
	require.Equal(t, promoParseOff, st)
	p, st = parsePromoSettings(promoTestValues())
	require.NotNil(t, p)
	require.Equal(t, promoParseOK, st)
	for name, mutate := range map[string]func(map[string]string){
		"end<=start":   func(m map[string]string) { m[SettingKeyPromoEndAt] = m[SettingKeyPromoStartAt] },
		"bad end":      func(m map[string]string) { m[SettingKeyPromoEndAt] = "x" },
		"missing end":  func(m map[string]string) { delete(m, SettingKeyPromoEndAt) },
		"bad excluded": func(m map[string]string) { m[SettingKeyPromoExcludedUserIDs] = "4242,abc" },
		"neg excluded": func(m map[string]string) { m[SettingKeyPromoExcludedUserIDs] = "-3" },
		"sub too big":  func(m map[string]string) { m[SettingKeyPromoSubscriptionDailyMultiplier] = "15" },
		"sub <1":       func(m map[string]string) { m[SettingKeyPromoSubscriptionDailyMultiplier] = "0.5" },
		"sub inf":      func(m map[string]string) { m[SettingKeyPromoSubscriptionDailyMultiplier] = "Inf" },
		"balance >1":   func(m map[string]string) { m[SettingKeyPromoBalanceRateMultiplier] = "1.5" },
		"balance junk": func(m map[string]string) { m[SettingKeyPromoBalanceRateMultiplier] = "x" },
	} {
		m := promoTestValues()
		mutate(m)
		p, st = parsePromoSettings(m)
		require.Nil(t, p, name)
		require.Equal(t, promoParseInvalid, st, name)
	}
}

// 有效 → 写坏：仍沿用旧值；有效 → 全部清空：关闭；从未有效 → 非法：关闭。
func TestPromo_RefreshInvalidKeepsPreviousValid(t *testing.T) {
	var mu sync.Mutex
	vals := promoTestValues()
	SetPromoSettingsSource(func(ctx context.Context, keys []string) (map[string]string, error) {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]string{}
		for k, v := range vals {
			out[k] = v
		}
		return out, nil
	})
	t.Cleanup(func() { SetPromoSettingsSource(nil) })
	now := promoAt(9, 12)
	set := func(m map[string]string) {
		mu.Lock()
		vals = m
		mu.Unlock()
		require.NoError(t, refreshPromoSettings())
	}
	require.Equal(t, 1.5, promoDailyFactor(1, now))

	// 写坏（排除名单一项解析不了）：沿用旧值，旧排除名单也继续生效
	bad := promoTestValues()
	bad[SettingKeyPromoExcludedUserIDs] = "4242,abc"
	set(bad)
	require.Equal(t, 1.5, promoDailyFactor(1, now))
	require.Equal(t, 0.52, PromoBalanceRateFactor(1, now))
	require.Equal(t, 1.0, PromoBalanceRateFactor(4242, now))

	// END<=START、倍率越界同样沿用旧值
	bad = promoTestValues()
	bad[SettingKeyPromoEndAt] = bad[SettingKeyPromoStartAt]
	set(bad)
	require.Equal(t, 1.5, promoDailyFactor(1, now))
	bad = promoTestValues()
	bad[SettingKeyPromoBalanceRateMultiplier] = "1.5"
	set(bad)
	require.Equal(t, 0.52, PromoBalanceRateFactor(1, now))

	// 改回另一份有效配置：正常采用
	good := promoTestValues()
	good[SettingKeyPromoBalanceRateMultiplier] = "0.6"
	set(good)
	require.Equal(t, 0.6, PromoBalanceRateFactor(1, now))

	// 全部清空：有意关闭
	set(map[string]string{})
	require.Equal(t, 1.0, promoDailyFactor(1, now))
	require.Equal(t, 1.0, PromoBalanceRateFactor(1, now))

	// 清空后再写坏：此前没有有效配置（已关闭）→ 保持关闭
	bad = promoTestValues()
	bad[SettingKeyPromoSubscriptionDailyMultiplier] = "abc"
	set(bad)
	require.Equal(t, 1.0, promoDailyFactor(1, now))
}

func TestPromo_NeverValidThenInvalidStaysOff(t *testing.T) {
	bad := promoTestValues()
	bad[SettingKeyPromoExcludedUserIDs] = "abc"
	setPromoForTest(t, bad)
	require.Equal(t, 1.0, promoDailyFactor(1, promoAt(9, 12)))
	require.Equal(t, 1.0, PromoBalanceRateFactor(1, promoAt(9, 12)))
}

// 首次读库失败后，在第一次成功前按短间隔重试，成功后恢复正常间隔。
func TestPromoSettingsRefresher_StartupRetryUntilFirstSuccess(t *testing.T) {
	var calls atomic.Int32
	var failUntil atomic.Int32 // 前 N 次调用失败
	failUntil.Store(3)
	r := NewPromoSettingsRefresher(func(ctx context.Context, keys []string) (map[string]string, error) {
		n := calls.Add(1)
		if n <= failUntil.Load() {
			return nil, errors.New("db not ready")
		}
		return promoTestValues(), nil
	}, time.Hour) // 正常间隔很长：能在测试时间内成功只可能是启动重试
	r.retryInterval = 5 * time.Millisecond
	r.Start()
	t.Cleanup(func() { r.Stop(); SetPromoSettingsSource(nil) })
	now := promoAt(9, 12)

	require.Equal(t, int32(1), calls.Load(), "启动同步加载一次，失败")
	require.Equal(t, 1.0, promoDailyFactor(1, now), "加载失败期间按关闭处理")
	require.Eventually(t, func() bool { return promoDailyFactor(1, now) == 1.5 }, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, int32(4), calls.Load(), "第 4 次调用（第 3 次重试）成功")

	// 成功之后恢复正常（很长的）间隔：不再按重试间隔读库
	time.Sleep(60 * time.Millisecond)
	require.Equal(t, int32(4), calls.Load())
}

func TestPromoBalanceRateFactor_WindowAndExcluded(t *testing.T) {
	setPromoForTest(t, promoTestValues())
	require.Equal(t, 1.0, PromoBalanceRateFactor(1, promoAt(7, 23).Add(59*time.Minute)))
	require.Equal(t, 0.52, PromoBalanceRateFactor(1, promoAt(8, 0)))
	require.Equal(t, 0.52, PromoBalanceRateFactor(1, promoAt(14, 23).Add(59*time.Minute)))
	require.Equal(t, 1.0, PromoBalanceRateFactor(1, promoAt(15, 0)))
	require.Equal(t, 1.0, PromoBalanceRateFactor(4242, promoAt(9, 12)))
}

func TestPromoNoSourceIsOff(t *testing.T) {
	SetPromoSettingsSource(nil)
	require.Equal(t, 1.0, promoDailyFactor(1, promoAt(9, 12)))
	require.Equal(t, 1.0, PromoBalanceRateFactor(1, promoAt(9, 12)))
}

func TestSubWindowApplyPromo(t *testing.T) {
	setPromoForTest(t, promoTestValues())
	now := promoAt(9, 12) // 周一；本周窗口 11-09 起，活动日 1 天(11-09)；本月窗口 11-01 起，活动日 2 天(11-08、11-09)

	c := &SubWindow{DailyLimitUSD: 100, WeeklyLimitUSD: 700, MonthlyLimitUSD: 0}
	c.ApplyPromo(1, now)
	require.Equal(t, 150.0, c.DailyLimitUSD)
	require.Equal(t, 750.0, c.WeeklyLimitUSD)
	require.Equal(t, 0.0, c.MonthlyLimitUSD, "不限窗口保持不限、无加成")

	// 排除名单 / 活动前：不变
	base := SubWindow{DailyLimitUSD: 100, WeeklyLimitUSD: 700, MonthlyLimitUSD: 3000}
	c = &SubWindow{DailyLimitUSD: 100, WeeklyLimitUSD: 700, MonthlyLimitUSD: 3000}
	c.ApplyPromo(4242, now)
	require.Equal(t, base, *c)
	c.ApplyPromo(1, promoAt(7, 12))
	require.Equal(t, base, *c)

	// 没有日限额：不产生任何加成
	c = &SubWindow{WeeklyLimitUSD: 700, MonthlyLimitUSD: 3000}
	c.ApplyPromo(1, now)
	require.Equal(t, SubWindow{WeeklyLimitUSD: 700, MonthlyLimitUSD: 3000}, *c)

	var nilWindow *SubWindow
	nilWindow.ApplyPromo(1, now) // 不 panic
	require.False(t, nilWindow.IsActive(now))
}

// 活动 11-08(周日)~11-14：周窗口 11-02~11-08 与 11-09~11-15，月窗口 11-01~11-30。
func TestPromoEffectiveLimits_BonusAccumulatesPerActiveDay(t *testing.T) {
	setPromoForTest(t, promoTestValues())
	const D, W, M = 100.0, 700.0, 3000.0
	bonus := (1.5 - 1) * D // 每个活动日的加成 (f-1)·D = 50

	type want struct{ d, wBonusDays, mBonusDays float64 }
	cases := []struct {
		name string
		at   time.Time
		want want
	}{
		{"活动前一天(周六)", promoAt(7, 23).Add(59 * time.Minute), want{1, 0, 0}},
		{"第1天 11-08 0点(周日, 旧周窗口)", promoAt(8, 0), want{1.5, 1, 1}},
		{"第2天 11-09(周一, 新周窗口)", promoAt(9, 12), want{1.5, 1, 2}},
		{"第3天 11-10", promoAt(10, 12), want{1.5, 2, 3}},
		{"第5天 11-12", promoAt(12, 12), want{1.5, 4, 5}},
		{"第7天 11-14 23:59", promoAt(14, 23).Add(59 * time.Minute), want{1.5, 6, 7}},
		{"活动结束 11-15 0点(周日)", promoAt(15, 0), want{1, 6, 7}},
		{"结束后同周 11-15 18点", promoAt(15, 18), want{1, 6, 7}},
		{"结束后下一周 11-16(周一, 周窗口重置)", promoAt(16, 12), want{1, 0, 7}},
		{"月底 11-30", promoAt(30, 12), want{1, 0, 7}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, w, m := PromoEffectiveLimits(1, c.at, D, W, M)
			require.InDelta(t, D*c.want.d, d, 1e-9)
			require.InDelta(t, W+bonus*c.want.wBonusDays, w, 1e-9)
			require.InDelta(t, M+bonus*c.want.mBonusDays, m, 1e-9)
			// 排除名单：加成恒为 0
			d, w, m = PromoEffectiveLimits(4242, c.at, D, W, M)
			require.Equal(t, []float64{D, W, M}, []float64{d, w, m})
		})
	}

	// 12-01 新月份：月窗口重置，月加成归零
	_, _, m := PromoEffectiveLimits(1, time.Date(2026, 12, 1, 9, 0, 0, 0, promoTestLoc), D, W, M)
	require.Equal(t, M, m)
}

func TestPromoEffectiveLimits_WindowStraddlesStartAndEnd(t *testing.T) {
	// 活动 11-11(含)~11-13(不含) 夹在周窗口 11-09~11-15 中间：只算 11-11、11-12 两个活动日
	v := promoTestValues()
	v[SettingKeyPromoStartAt] = "2026-11-11T00:00:00+08:00"
	v[SettingKeyPromoEndAt] = "2026-11-13T00:00:00+08:00"
	setPromoForTest(t, v)
	const D, W, M = 100.0, 700.0, 3000.0
	check := func(at time.Time, d, wDays, mDays float64) {
		t.Helper()
		gd, gw, gm := PromoEffectiveLimits(1, at, D, W, M)
		require.InDelta(t, D*d, gd, 1e-9, at)
		require.InDelta(t, W+50*wDays, gw, 1e-9, at)
		require.InDelta(t, M+50*mDays, gm, 1e-9, at)
	}
	check(promoAt(10, 12), 1, 0, 0)
	check(promoAt(11, 0), 1.5, 1, 1)
	check(promoAt(12, 23), 1.5, 2, 2)
	check(promoAt(13, 0), 1, 2, 2)
	check(promoAt(14, 12), 1, 2, 2)
	check(promoAt(16, 12), 1, 0, 2)

	// 活动从 11-08 中午开始：11-08 整天不算（当天 0 点早于 START），11-09 才是第 1 个活动日
	v[SettingKeyPromoStartAt] = "2026-11-08T12:00:00+08:00"
	v[SettingKeyPromoEndAt] = "2026-11-15T00:00:00+08:00"
	setPromoForTest(t, v)
	check(promoAt(8, 20), 1, 0, 0)
	check(promoAt(9, 12), 1.5, 1, 1)
}

func TestToSubWindowAt_PromoOnlyInMemory(t *testing.T) {
	setPromoForTest(t, promoTestValues())
	d, w, m := 100.0, 700.0, 3000.0
	sub := &UserSubscription{UserID: 1, Status: SubscriptionStatusActive, DailyAmountUSD: 100,
		DailyLimitUSD: &d, WeeklyLimitUSD: &w, MonthlyLimitUSD: &m, ExpiresAt: promoAt(30, 0)}

	in := sub.ToSubWindowAt(promoAt(9, 12))
	require.Equal(t, 150.0, in.DailyLimitUSD)
	require.Equal(t, 750.0, in.WeeklyLimitUSD)
	require.Equal(t, 3100.0, in.MonthlyLimitUSD)
	// 卡本身（以及按 D 计价的一切）不变
	require.Equal(t, 100.0, *sub.DailyLimitUSD)
	require.Equal(t, 700.0, *sub.WeeklyLimitUSD)
	require.Equal(t, 3000.0, *sub.MonthlyLimitUSD)
	require.Equal(t, 100.0, sub.DailyAmountUSD)

	// 活动结束后的月窗口内：日限额恢复，月限额保留 7 天加成
	out := sub.ToSubWindowAt(promoAt(20, 12))
	require.Equal(t, 100.0, out.DailyLimitUSD)
	require.Equal(t, 700.0, out.WeeklyLimitUSD)
	require.Equal(t, 3350.0, out.MonthlyLimitUSD)

	sub.UserID = 4242
	got := sub.ToSubWindowAt(promoAt(9, 12))
	require.Equal(t, []float64{100, 700, 3000}, []float64{got.DailyLimitUSD, got.WeeklyLimitUSD, got.MonthlyLimitUSD})
}

// 四处（ToSubWindowAt / PromoEffectiveLimits / ApplyPromo / CheckUsageLimits 指针版）必须是同一个三元组。
func TestPromoEffectiveLimitPtrs_SameTripleAsEngine(t *testing.T) {
	setPromoForTest(t, promoTestValues())
	d, w, m := 100.0, 700.0, 3000.0
	for _, at := range []time.Time{promoAt(7, 12), promoAt(9, 12), promoAt(14, 23), promoAt(15, 12), promoAt(20, 12)} {
		ed, ew, em := PromoEffectiveLimits(1, at, d, w, m)
		pd, pw, pm := PromoEffectiveLimitPtrs(1, at, &d, &w, &m)
		require.Equal(t, []float64{ed, ew, em}, []float64{*pd, *pw, *pm})
		sub := &UserSubscription{UserID: 1, DailyLimitUSD: &d, WeeklyLimitUSD: &w, MonthlyLimitUSD: &m}
		sw := sub.ToSubWindowAt(at)
		require.Equal(t, []float64{ed, ew, em}, []float64{sw.DailyLimitUSD, sw.WeeklyLimitUSD, sw.MonthlyLimitUSD})
	}
	// nil / 0（不限）保持原样
	pd, pw, pm := PromoEffectiveLimitPtrs(1, promoAt(9, 12), nil, &w, nil)
	require.Nil(t, pd)
	require.Equal(t, 700.0, *pw)
	require.Nil(t, pm)
}

func TestPromoSubscription_AdmitAndSettleUseScaledLimit(t *testing.T) {
	setPromoForTest(t, promoTestValues())
	d, w, m := 100.0, 700.0, 3000.0
	now := promoAt(9, 12)
	mk := func(uid int64) SubWindow {
		sub := &UserSubscription{UserID: uid, Status: SubscriptionStatusActive,
			DailyLimitUSD: &d, WeeklyLimitUSD: &w, MonthlyLimitUSD: &m, ExpiresAt: promoAt(30, 0)}
		return sub.ToSubWindowAt(now)
	}

	// 普通用户：今天可覆盖到 150，不碰钱包
	sw := mk(1)
	wallet := WalletState{Balance: 1000}
	res := SettleWindow(&sw, &wallet, 150, 1, now)
	require.InDelta(t, 150.0, res.SubCover, 1e-9)
	require.InDelta(t, 0.0, res.WalletPay, 1e-9)
	require.InDelta(t, 1000.0, wallet.Balance, 1e-9)

	// 排除名单用户：同样 150 的消费，只有 100 被覆盖，其余落钱包
	sw = mk(4242)
	wallet = WalletState{Balance: 1000}
	res = SettleWindow(&sw, &wallet, 150, 1, now)
	require.InDelta(t, 100.0, res.SubCover, 1e-9)
	require.InDelta(t, 50.0, res.WalletPay, 1e-9)

	// 准入：用满 100 后，普通用户钱包 0 仍可进，排除用户不行
	ds := timezone.StartOfDay(now)
	sw = mk(1)
	sw.DailyUsageUSD, sw.DailyWindowStart = 100, &ds
	require.True(t, AdmitWindow(&sw, &WalletState{Balance: 0}, now))
	sw = mk(4242)
	sw.DailyUsageUSD, sw.DailyWindowStart = 100, &ds
	require.False(t, AdmitWindow(&sw, &WalletState{Balance: 0}, now))
}

func TestCheckUsageLimits_PromoScaling(t *testing.T) {
	// CheckUsageLimits 取真实当前时间，窗口开在「现在」前后。
	v := promoTestValues()
	v[SettingKeyPromoStartAt] = time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	v[SettingKeyPromoEndAt] = time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	setPromoForTest(t, v)
	d, w, m := 100.0, 700.0, 3000.0
	svc := &SubscriptionService{}
	mk := func(uid int64, dailyUsage float64) *UserSubscription {
		return &UserSubscription{UserID: uid, DailyLimitUSD: &d, WeeklyLimitUSD: &w, MonthlyLimitUSD: &m, DailyUsageUSD: dailyUsage}
	}
	// 用了 120：活动内普通用户不超限，排除用户超限；ptr 不被篡改
	sub := mk(1, 120)
	require.NoError(t, svc.CheckUsageLimits(context.Background(), sub, nil, 0))
	require.Equal(t, 100.0, *sub.DailyLimitUSD)
	require.ErrorIs(t, svc.CheckUsageLimits(context.Background(), mk(4242, 120), nil, 0), ErrDailyLimitExceeded)
	// 超过 150 仍然超限
	require.ErrorIs(t, svc.CheckUsageLimits(context.Background(), mk(1, 151), nil, 0), ErrDailyLimitExceeded)
}

// 热路径（PromoSubscriptionFactor 等）只读内存快照，任何次数的调用都不得再触发读库。
func TestPromo_HotPathNeverLoads(t *testing.T) {
	var calls atomic.Int32
	SetPromoSettingsSource(func(ctx context.Context, keys []string) (map[string]string, error) {
		calls.Add(1)
		return promoTestValues(), nil
	})
	t.Cleanup(func() { SetPromoSettingsSource(nil) })
	require.Equal(t, int32(1), calls.Load(), "启动期同步加载一次")

	now := promoAt(9, 12)
	for i := 0; i < 50; i++ {
		require.Equal(t, 1.5, promoDailyFactor(1, now))
		require.Equal(t, 0.52, PromoBalanceRateFactor(1, now))
		PromoEffectiveLimits(1, now, 100, 700, 3000)
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestPromo_RefreshKeepsOldValueOnError(t *testing.T) {
	var mu sync.Mutex
	vals := promoTestValues()
	var fail bool
	SetPromoSettingsSource(func(ctx context.Context, keys []string) (map[string]string, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return nil, errors.New("db down")
		}
		return vals, nil
	})
	t.Cleanup(func() { SetPromoSettingsSource(nil) })
	now := promoAt(9, 12)
	require.Equal(t, 1.5, promoDailyFactor(1, now))

	// 读库失败：保留旧值
	mu.Lock()
	fail = true
	mu.Unlock()
	require.Error(t, refreshPromoSettings())
	require.Equal(t, 1.5, promoDailyFactor(1, now))

	// 恢复后下一次刷新拿到新配置（这里模拟改库关闭活动），无需重启
	mu.Lock()
	fail = false
	vals = map[string]string{}
	mu.Unlock()
	require.NoError(t, refreshPromoSettings())
	require.Equal(t, 1.0, promoDailyFactor(1, now))
}

func TestPromo_StartupLoadFailureMeansOff(t *testing.T) {
	SetPromoSettingsSource(func(ctx context.Context, keys []string) (map[string]string, error) {
		return nil, errors.New("db down")
	})
	t.Cleanup(func() { SetPromoSettingsSource(nil) })
	require.Equal(t, 1.0, promoDailyFactor(1, promoAt(9, 12)))
	require.Equal(t, 1.0, PromoBalanceRateFactor(1, promoAt(9, 12)))
	// 无读取源时刷新是空操作
	SetPromoSettingsSource(nil)
	require.NoError(t, refreshPromoSettings())
}

func TestPromoSettingsRefresher_BackgroundRefreshAndStop(t *testing.T) {
	var mu sync.Mutex
	vals := map[string]string{}
	var calls atomic.Int32
	r := NewPromoSettingsRefresher(func(ctx context.Context, keys []string) (map[string]string, error) {
		calls.Add(1)
		mu.Lock()
		defer mu.Unlock()
		out := map[string]string{}
		for k, v := range vals {
			out[k] = v
		}
		return out, nil
	}, 10*time.Millisecond)
	r.Start()
	t.Cleanup(func() { r.Stop(); SetPromoSettingsSource(nil) })
	now := promoAt(9, 12)
	require.Equal(t, 1.0, promoDailyFactor(1, now), "启动时库里没有配置 = 关闭")

	mu.Lock()
	vals = promoTestValues()
	mu.Unlock()
	require.Eventually(t, func() bool { return promoDailyFactor(1, now) == 1.5 }, 2*time.Second, 5*time.Millisecond)

	r.Stop()
	r.Stop() // 幂等
	after := calls.Load()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, after, calls.Load(), "Stop 之后 goroutine 已退出，不再读库")

	// 空对象 / 无源 / 非正间隔 都是安全的空操作
	var nilR *PromoSettingsRefresher
	nilR.Start()
	nilR.Stop()
	NewPromoSettingsRefresher(nil, time.Second).Start()
	NewPromoSettingsRefresher(func(context.Context, []string) (map[string]string, error) { return nil, nil }, 0).Start()
}

func TestPaidActualCost(t *testing.T) {
	require.Equal(t, 2.0, paidActualCost(2.0, nil))
	require.Equal(t, 2.0, paidActualCost(2.0, &UsageBillingApplyResult{}))
	require.Equal(t, 2.0, paidActualCost(2.0, &UsageBillingApplyResult{PaidRatio: 1}))
	require.InDelta(t, 1.04, paidActualCost(2.0, &UsageBillingApplyResult{PaidRatio: 0.52}), 1e-9)
	// 混合行（有卡、部分溢出钱包）同样按实付比例折算
	id := int64(3)
	require.InDelta(t, 1.6, paidActualCost(2.0, &UsageBillingApplyResult{PaidRatio: 0.8, SubscriptionID: &id}), 1e-9)
}

func TestApplyPromoWalletRateToUsageLog(t *testing.T) {
	newLog := func() *UsageLog { return &UsageLog{RateMultiplier: 1.0, ActualCost: 2.0, TotalCost: 2.0} }

	// 纯钱包行：记折后倍率与实扣；官方价不变
	l := newLog()
	applyPromoWalletRateToUsageLog(l, &UsageBillingApplyResult{PaidRatio: 0.52})
	require.InDelta(t, 0.52, l.RateMultiplier, 1e-9)
	require.InDelta(t, 1.04, l.ActualCost, 1e-9)
	require.InDelta(t, 2.0, l.TotalCost, 1e-9)

	// 混合行（SubscriptionID 非空）：实付 = 卡部分 + 折后钱包部分，倍率记混合有效倍率，不变式 actual=total×rate 保持
	l = newLog()
	id := int64(3)
	applyPromoWalletRateToUsageLog(l, &UsageBillingApplyResult{PaidRatio: 0.8, SubscriptionID: &id})
	require.InDelta(t, 1.6, l.ActualCost, 1e-9)
	require.InDelta(t, l.TotalCost*l.RateMultiplier, l.ActualCost, 1e-9)

	// 未打折（0 / 1）、空参数：不变也不 panic
	for _, f := range []float64{0, 1} {
		l = newLog()
		applyPromoWalletRateToUsageLog(l, &UsageBillingApplyResult{PaidRatio: f})
		require.Equal(t, newLog(), l)
	}
	applyPromoWalletRateToUsageLog(nil, &UsageBillingApplyResult{PaidRatio: 0.52})
	applyPromoWalletRateToUsageLog(newLog(), nil)
}

// 引擎层：钱包折扣只作用于订阅覆盖之后钱包承担的部分，卡部分原价计入卡用量。
func TestSettleWindowWithWalletRate(t *testing.T) {
	now := promoAt(9, 12)
	ds, ws, ms := timezone.StartOfDay(now), timezone.StartOfWeek(now), timezone.StartOfMonth(now)
	card := func(dailyUsage float64) *SubWindow {
		return &SubWindow{DailyLimitUSD: 150, WeeklyLimitUSD: 1000, MonthlyLimitUSD: 4000, DailyUsageUSD: dailyUsage,
			DailyWindowStart: &ds, WeeklyWindowStart: &ws, MonthlyWindowStart: &ms, Status: SubscriptionStatusActive, ExpiresAt: promoAt(30, 0)}
	}

	// 纯钱包：全额折扣
	w := WalletState{Balance: 100}
	res := SettleWindowWithWalletRate(nil, &w, 10, 1, 0.52, now)
	require.InDelta(t, 5.2, res.WalletPay, 1e-9)
	require.InDelta(t, 10.0, res.WalletRaw, 1e-9)
	require.InDelta(t, 94.8, w.Balance, 1e-9)

	// 有卡、未溢出：不打折，钱包不动
	c := card(90)
	w = WalletState{Balance: 100}
	res = SettleWindowWithWalletRate(c, &w, 40, 1, 0.52, now)
	require.InDelta(t, 40.0, res.SubCover, 1e-9)
	require.InDelta(t, 0.0, res.WalletRaw, 1e-9)
	require.InDelta(t, 100.0, w.Balance, 1e-9)

	// 有卡、部分溢出：卡 10 原价计入卡用量，溢出 20 × 0.52 = 10.4 扣钱包
	c = card(140)
	w = WalletState{Balance: 100}
	res = SettleWindowWithWalletRate(c, &w, 30, 1, 0.52, now)
	require.InDelta(t, 10.0, res.SubCover, 1e-9)
	require.InDelta(t, 20.0, res.WalletRaw, 1e-9)
	require.InDelta(t, 10.4, res.WalletPay, 1e-9)
	require.InDelta(t, 150.0, c.DailyUsageUSD, 1e-9)
	require.InDelta(t, 89.6, w.Balance, 1e-9)

	// 钱包不足：折后缺口落负数
	w = WalletState{Balance: 5}
	res = SettleWindowWithWalletRate(nil, &w, 10, 1, 0.5, now)
	require.InDelta(t, 5.0, res.WalletPay, 1e-9)
	require.InDelta(t, 0.0, res.WalletNegPay, 1e-9)
	w = WalletState{Balance: 2}
	res = SettleWindowWithWalletRate(nil, &w, 10, 1, 0.5, now)
	require.InDelta(t, 2.0, res.WalletPay, 1e-9)
	require.InDelta(t, 3.0, res.WalletNegPay, 1e-9)
	require.InDelta(t, -3.0, w.Balance, 1e-9)

	// 非法折扣（<=0 / >=1）等同不打折，且与 SettleWindow 一致
	for _, f := range []float64{0, -1, 1, 1.5} {
		w = WalletState{Balance: 100}
		res = SettleWindowWithWalletRate(nil, &w, 10, 1, f, now)
		require.InDelta(t, 10.0, res.WalletPay, 1e-9)
	}
	w = WalletState{Balance: 100}
	require.InDelta(t, 10.0, SettleWindow(nil, &w, 10, 1, now).WalletPay, 1e-9)
}

// 退款 / 转套餐 / 合并兑换 / 报价所依赖的纯函数只读 D、T 与到期日，活动期间结果与活动关闭时逐位一致：
// 赠送的 0.5×D 不会变成钱。
func TestPromo_MoneyPathsIgnorePromo(t *testing.T) {
	today := EastDayNumber(promoAt(9, 12))
	card := PerDayCard{DailyAmountUSD: 100, TodayRemaining: 100, TodayDay: today, StartDay: today - 5, ExpireDay: today + 10}

	measure := func() (float64, float64, int, float64) {
		c := card
		c.ResetIfNewDay(today)
		cfg := DefaultSubscriptionPricingConfig()
		return c.TodayRemaining, cfg.Price(100, c.RefundableDays(today)), c.RefundableDays(today), ChangePlanNewCardTodayBalance(200, c.TodaySpentFromPackage(today))
	}
	a1, b1, c1, d1 := measure()
	setPromoForTest(t, promoTestValues())
	a2, b2, c2, d2 := measure()
	require.Equal(t, []any{a1, b1, c1, d1}, []any{a2, b2, c2, d2})
	require.Equal(t, 100.0, a2, "today_remaining 仍按 D 发放")
}
