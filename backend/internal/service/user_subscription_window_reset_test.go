package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// P2#12:订阅展示侧周/月「是否重置 / 下次重置时间」改用自然边界(东八区周一/1 号),
// 与计费侧 SubWindow.ResetWindows 同口径——旧的 7×24h / 30×24h 滚动窗会与自然边界错位 0–6 天,
// 使 /v1/usage 展示的已用/重置时间与实际计费不一致。
func TestSubscriptionWindowReset_NaturalBoundaries(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC) // 任意时刻
	ws := timezone.StartOfWeek(now)
	ms := timezone.StartOfMonth(now)

	// 周:窗口起点早于本自然周起点 → 该重置;恰为本周起点 → 不重置。
	beforeWeek := ws.Add(-time.Second)
	require.True(t, (&UserSubscription{WeeklyWindowStart: &beforeWeek}).NeedsWeeklyResetAt(now))
	require.False(t, (&UserSubscription{WeeklyWindowStart: &ws}).NeedsWeeklyResetAt(now))

	// 月:窗口起点早于本自然月起点 → 该重置;恰为本月起点 → 不重置。
	beforeMonth := ms.Add(-time.Second)
	require.True(t, (&UserSubscription{MonthlyWindowStart: &beforeMonth}).NeedsMonthlyResetAt(now))
	require.False(t, (&UserSubscription{MonthlyWindowStart: &ms}).NeedsMonthlyResetAt(now))

	// 下次重置 = 下一个自然边界。
	require.Equal(t, ws.AddDate(0, 0, 7), *(&UserSubscription{WeeklyWindowStart: &ws}).WeeklyResetTimeAt(now))
	require.Equal(t, ms.AddDate(0, 1, 0), *(&UserSubscription{MonthlyWindowStart: &ms}).MonthlyResetTimeAt(now))

	// 与旧「滚动窗」分叉的关键场景:窗口起点距今仅 2 天但已跨过自然周一 → 自然口径判「该重置」,
	// 旧的 7×24h 滚动会判「未到」。构造 now=周二、窗口起点=上周日(本周一之前)。
	tuesday := timezone.StartOfWeek(now).AddDate(0, 0, 1) // 本周二 0 点
	lastSunday := timezone.StartOfWeek(now).Add(-24 * time.Hour)
	require.True(t, (&UserSubscription{WeeklyWindowStart: &lastSunday}).NeedsWeeklyResetAt(tuesday),
		"窗口起点在上周(跨过本周一)→ 自然周口径应判重置,即便距今不足 7 天")

	// nil 窗口不触发重置、ResetTime 为 nil。
	require.False(t, (&UserSubscription{}).NeedsWeeklyResetAt(now))
	require.False(t, (&UserSubscription{}).NeedsMonthlyResetAt(now))
	require.Nil(t, (&UserSubscription{}).WeeklyResetTimeAt(now))
	require.Nil(t, (&UserSubscription{}).MonthlyResetTimeAt(now))
}
