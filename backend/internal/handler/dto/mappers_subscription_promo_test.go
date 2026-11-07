package dto

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 这些用例改全局活动配置，故意不用 t.Parallel（并行用例会在所有串行用例结束后才恢复运行）。
func setDTOPromoForTest(t *testing.T, excluded string) {
	t.Helper()
	vals := map[string]string{
		service.SettingKeyPromoStartAt:                     time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
		service.SettingKeyPromoEndAt:                       time.Now().Add(48 * time.Hour).Format(time.RFC3339),
		service.SettingKeyPromoSubscriptionDailyMultiplier: "1.5",
		service.SettingKeyPromoExcludedUserIDs:             excluded,
	}
	service.SetPromoSettingsSource(func(ctx context.Context, keys []string) (map[string]string, error) { return vals, nil })
	t.Cleanup(func() { service.SetPromoSettingsSource(nil) })
}

func promoTestSub(userID int64) *service.UserSubscription {
	d, w, m := 10.0, 70.0, 300.0
	now := time.Now()
	return &service.UserSubscription{
		ID: 1, UserID: userID, Status: service.SubscriptionStatusActive,
		StartsAt: now.Add(-24 * time.Hour), ExpiresAt: now.Add(20 * 24 * time.Hour),
		DailyAmountUSD: 10, DailyLimitUSD: &d, WeeklyLimitUSD: &w, MonthlyLimitUSD: &m,
	}
}

func TestUserSubscriptionFromService_PromoEffectiveLimits(t *testing.T) {
	setDTOPromoForTest(t, "4242")
	now := time.Now()

	sub := promoTestSub(1)
	wd, ww, wm := service.PromoEffectiveLimits(1, now, 10, 70, 300)
	got := UserSubscriptionFromService(sub)
	require.InDelta(t, 15.0, *got.DailyLimitUSD, 1e-9, "活动日日限额 ×1.5，不再显示 12/10")
	require.InDelta(t, wd, *got.DailyLimitUSD, 1e-9)
	require.InDelta(t, ww, *got.WeeklyLimitUSD, 1e-9)
	require.InDelta(t, wm, *got.MonthlyLimitUSD, 1e-9)
	require.Greater(t, *got.WeeklyLimitUSD, 70.0)
	// 卡本身与 D 不变
	require.Equal(t, 10.0, *sub.DailyLimitUSD)
	require.Equal(t, 10.0, got.DailyAmountUSD)

	// 管理端仍看到卡上原值
	admin := UserSubscriptionFromServiceAdmin(promoTestSub(1))
	require.Equal(t, 10.0, *admin.DailyLimitUSD)
	require.Equal(t, 70.0, *admin.WeeklyLimitUSD)
	require.Equal(t, 300.0, *admin.MonthlyLimitUSD)

	// 排除名单：原值
	ex := UserSubscriptionFromService(promoTestSub(4242))
	require.Equal(t, 10.0, *ex.DailyLimitUSD)
	require.Equal(t, 70.0, *ex.WeeklyLimitUSD)

	// 已过期/非生效的卡：原值
	expired := promoTestSub(1)
	expired.Status = service.SubscriptionStatusExpired
	require.Equal(t, 10.0, *UserSubscriptionFromService(expired).DailyLimitUSD)

	// nil（不限）保持 nil
	nolimit := promoTestSub(1)
	nolimit.WeeklyLimitUSD = nil
	nolimit.MonthlyLimitUSD = nil
	g := UserSubscriptionFromService(nolimit)
	require.InDelta(t, 15.0, *g.DailyLimitUSD, 1e-9)
	require.Nil(t, g.WeeklyLimitUSD)
	require.Nil(t, g.MonthlyLimitUSD)
}

func TestUserSubscriptionFromService_NoPromoKeepsRawLimits(t *testing.T) {
	service.SetPromoSettingsSource(nil)
	got := UserSubscriptionFromService(promoTestSub(1))
	require.Equal(t, 10.0, *got.DailyLimitUSD)
	require.Equal(t, 70.0, *got.WeeklyLimitUSD)
	require.Equal(t, 300.0, *got.MonthlyLimitUSD)
}
