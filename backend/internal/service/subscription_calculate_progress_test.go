package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Task 5: 验证 calculateProgress 纯函数行为正确 ---

func newTestSubscriptionService() *SubscriptionService {
	return &SubscriptionService{}
}

func ptrFloat64(v float64) *float64  { return &v }
func ptrTime(t time.Time) *time.Time { return &t }

func TestCalculateProgress_BasicFields(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now()

	sub := &UserSubscription{
		ID:        100,
		ExpiresAt: now.Add(30 * 24 * time.Hour),
	}
	group := &Group{
		Name: "Premium",
	}

	progress := svc.calculateProgress(sub, group)

	assert.Equal(t, int64(100), progress.ID)
	assert.Equal(t, "Premium", progress.GroupName)
	assert.Equal(t, sub.ExpiresAt, progress.ExpiresAt)
	assert.True(t, progress.ExpiresInDays == 29 || progress.ExpiresInDays == 30, "ExpiresInDays should be 29 or 30, got %d", progress.ExpiresInDays)
	assert.Nil(t, progress.Daily, "无日限额时 Daily 应为 nil")
	assert.Nil(t, progress.Weekly, "无周限额时 Weekly 应为 nil")
	assert.Nil(t, progress.Monthly, "无月限额时 Monthly 应为 nil")
}

func TestCalculateProgress_DailyUsage(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now()
	dailyStart := timezone.StartOfDay(now)

	sub := &UserSubscription{
		ID:               1,
		ExpiresAt:        now.Add(10 * 24 * time.Hour),
		DailyUsageUSD:    3.0,
		DailyWindowStart: ptrTime(dailyStart),
		DailyLimitUSD:    ptrFloat64(10.0),
	}
	group := &Group{
		Name: "Pro",
	}

	progress := svc.calculateProgress(sub, group)

	require.NotNil(t, progress.Daily, "有日限额和窗口时 Daily 不应为 nil")
	assert.Equal(t, 10.0, progress.Daily.LimitUSD)
	assert.Equal(t, 3.0, progress.Daily.UsedUSD)
	assert.Equal(t, 7.0, progress.Daily.RemainingUSD)
	assert.Equal(t, 30.0, progress.Daily.Percentage)
	assert.Equal(t, dailyStart, progress.Daily.WindowStart)
}

func TestCalculateProgress_IgnoresLegacyGroupLimits(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now()

	sub := &UserSubscription{
		ID:                 1,
		ExpiresAt:          now.Add(10 * 24 * time.Hour),
		DailyUsageUSD:      3.0,
		WeeklyUsageUSD:     4.0,
		MonthlyUsageUSD:    5.0,
		DailyWindowStart:   ptrTime(now.Add(-2 * time.Hour)),
		WeeklyWindowStart:  ptrTime(now.Add(-24 * time.Hour)),
		MonthlyWindowStart: ptrTime(now.Add(-48 * time.Hour)),
	}
	group := &Group{
		Name:            "Legacy",
		DailyLimitUSD:   ptrFloat64(10.0),
		WeeklyLimitUSD:  ptrFloat64(70.0),
		MonthlyLimitUSD: ptrFloat64(300.0),
	}

	progress := svc.calculateProgress(sub, group)

	assert.Nil(t, progress.Daily, "三窗口限额必须来自订阅卡，不能回退 group.daily_limit_usd")
	assert.Nil(t, progress.Weekly, "三窗口限额必须来自订阅卡，不能回退 group.weekly_limit_usd")
	assert.Nil(t, progress.Monthly, "三窗口限额必须来自订阅卡，不能回退 group.monthly_limit_usd")
}

func TestCalculateProgress_DailyCardUsesNaturalWindowReset(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now()
	dailyStart := timezone.StartOfDay(now)
	startsAt := dailyStart.Add(12 * time.Hour)
	expiresAt := startsAt.Add(24 * time.Hour)

	sub := &UserSubscription{
		ID:               1,
		StartsAt:         startsAt,
		ExpiresAt:        expiresAt,
		DailyUsageUSD:    3.0,
		DailyWindowStart: ptrTime(dailyStart),
		DailyLimitUSD:    ptrFloat64(10.0),
	}
	group := &Group{
		Name: "Daily",
	}

	progress := svc.calculateProgress(sub, group)

	require.NotNil(t, progress.Daily, "日卡有日限额和窗口时 Daily 不应为 nil")
	assert.Equal(t, dailyStart.Add(24*time.Hour), progress.Daily.ResetsAt, "三窗口日额度按自然日窗口重置")
}

func TestCalculateProgress_WeeklyUsage(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now()
	weeklyStart := now.Add(-3 * 24 * time.Hour)

	sub := &UserSubscription{
		ID:                1,
		ExpiresAt:         now.Add(10 * 24 * time.Hour),
		WeeklyUsageUSD:    25.0,
		WeeklyWindowStart: ptrTime(weeklyStart),
		WeeklyLimitUSD:    ptrFloat64(50.0),
	}
	group := &Group{
		Name: "Pro",
	}

	progress := svc.calculateProgress(sub, group)

	require.NotNil(t, progress.Weekly, "有周限额和窗口时 Weekly 不应为 nil")
	assert.Equal(t, 50.0, progress.Weekly.LimitUSD)
	assert.Equal(t, 25.0, progress.Weekly.UsedUSD)
	assert.Equal(t, 25.0, progress.Weekly.RemainingUSD)
	assert.Equal(t, 50.0, progress.Weekly.Percentage)
}

func TestCalculateProgress_MonthlyUsage(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now()
	monthlyStart := now.Add(-15 * 24 * time.Hour)

	sub := &UserSubscription{
		ID:                 1,
		ExpiresAt:          now.Add(10 * 24 * time.Hour),
		MonthlyUsageUSD:    80.0,
		MonthlyWindowStart: ptrTime(monthlyStart),
		MonthlyLimitUSD:    ptrFloat64(100.0),
	}
	group := &Group{
		Name: "Enterprise",
	}

	progress := svc.calculateProgress(sub, group)

	require.NotNil(t, progress.Monthly, "有月限额和窗口时 Monthly 不应为 nil")
	assert.Equal(t, 100.0, progress.Monthly.LimitUSD)
	assert.Equal(t, 80.0, progress.Monthly.UsedUSD)
	assert.Equal(t, 20.0, progress.Monthly.RemainingUSD)
	assert.Equal(t, 80.0, progress.Monthly.Percentage)
}

func TestCalculateProgress_OverLimit_ClampedTo100Percent(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now()
	dailyStart := timezone.StartOfDay(now)

	sub := &UserSubscription{
		ID:               1,
		ExpiresAt:        now.Add(10 * 24 * time.Hour),
		DailyUsageUSD:    15.0, // 超过限额
		DailyWindowStart: ptrTime(dailyStart),
		DailyLimitUSD:    ptrFloat64(10.0),
	}
	group := &Group{
		Name: "Pro",
	}

	progress := svc.calculateProgress(sub, group)

	require.NotNil(t, progress.Daily)
	assert.Equal(t, 100.0, progress.Daily.Percentage, "超额使用应被截断为 100%")
	assert.Equal(t, 0.0, progress.Daily.RemainingUSD, "超额使用时剩余应为 0")
}

func TestCalculateProgress_NoWindowStart_NoProgress(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now()

	// 有限额但无窗口起始时间（订阅未激活）
	sub := &UserSubscription{
		ID:              1,
		ExpiresAt:       now.Add(10 * 24 * time.Hour),
		DailyUsageUSD:   0,
		WeeklyUsageUSD:  0,
		DailyLimitUSD:   ptrFloat64(10.0),
		WeeklyLimitUSD:  ptrFloat64(50.0),
		MonthlyLimitUSD: ptrFloat64(100.0),
	}
	group := &Group{
		Name: "Pro",
	}

	progress := svc.calculateProgress(sub, group)

	require.NotNil(t, progress.Daily, "展示层会惰性补齐当前自然日窗口")
	require.NotNil(t, progress.Weekly, "展示层会惰性补齐当前自然周窗口")
	require.NotNil(t, progress.Monthly, "展示层会惰性补齐当前自然月窗口")
	assert.Equal(t, 0.0, progress.Daily.UsedUSD)
	assert.Equal(t, 0.0, progress.Weekly.UsedUSD)
	assert.Equal(t, 0.0, progress.Monthly.UsedUSD)
}

func TestCalculateProgress_AllLimits(t *testing.T) {
	svc := newTestSubscriptionService()
	now := time.Now()
	dailyStart := timezone.StartOfDay(now)
	weeklyStart := timezone.StartOfWeek(now)
	monthlyStart := timezone.StartOfMonth(now)

	sub := &UserSubscription{
		ID:                 1,
		ExpiresAt:          now.Add(10 * 24 * time.Hour),
		DailyUsageUSD:      5.0,
		WeeklyUsageUSD:     20.0,
		MonthlyUsageUSD:    60.0,
		DailyWindowStart:   ptrTime(dailyStart),
		WeeklyWindowStart:  ptrTime(weeklyStart),
		MonthlyWindowStart: ptrTime(monthlyStart),
		DailyLimitUSD:      ptrFloat64(10.0),
		WeeklyLimitUSD:     ptrFloat64(50.0),
		MonthlyLimitUSD:    ptrFloat64(100.0),
	}
	group := &Group{
		Name: "Full",
	}

	progress := svc.calculateProgress(sub, group)

	require.NotNil(t, progress.Daily)
	require.NotNil(t, progress.Weekly)
	require.NotNil(t, progress.Monthly)

	assert.Equal(t, 50.0, progress.Daily.Percentage)
	assert.Equal(t, 40.0, progress.Weekly.Percentage)
	assert.Equal(t, 60.0, progress.Monthly.Percentage)
}

func TestCalculateProgress_ExpiredSubscription(t *testing.T) {
	svc := newTestSubscriptionService()

	sub := &UserSubscription{
		ID:        1,
		ExpiresAt: time.Now().Add(-24 * time.Hour), // 已过期
	}
	group := &Group{Name: "Expired"}

	progress := svc.calculateProgress(sub, group)

	assert.Equal(t, 0, progress.ExpiresInDays, "过期订阅的剩余天数应为 0")
}

func TestCalculateProgress_ResetsInSeconds_NotNegative(t *testing.T) {
	svc := newTestSubscriptionService()
	// 使用过去的窗口起始时间，使得重置时间已过
	pastStart := time.Now().Add(-48 * time.Hour)

	sub := &UserSubscription{
		ID:               1,
		ExpiresAt:        time.Now().Add(10 * 24 * time.Hour),
		DailyUsageUSD:    1.0,
		DailyWindowStart: ptrTime(pastStart),
		DailyLimitUSD:    ptrFloat64(10.0),
	}
	group := &Group{
		Name: "Test",
	}

	progress := svc.calculateProgress(sub, group)

	require.NotNil(t, progress.Daily)
	assert.GreaterOrEqual(t, progress.Daily.ResetsInSeconds, int64(0),
		"ResetsInSeconds 不应为负数")
}
