//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 三窗口准入闸（CheckBillingEligibility → checkBalanceEligibility）的真实 DB + Redis 端到端：
// 有生效卡看三窗口余量（任一窗口耗尽 → 精确 *_LIMIT_EXCEEDED；钱包>0 回落放行），覆盖 spec §4 准入口径。

func newBillingCacheServiceForAdmission(t *testing.T) *service.BillingCacheService {
	t.Helper()
	client := testEntClient(t)
	svc := service.NewBillingCacheService(
		NewBillingCache(testRedis(t)),
		NewUserRepository(client, integrationDB),
		NewUserSubscriptionRepository(client),
		nil, nil, nil,
		&config.Config{}, // RunMode 空 != simple → 不短路准入
		nil, nil,
	)
	t.Cleanup(svc.Stop)
	return svc
}

// admissionCard 造一张生效卡，窗口起点设为当下（保证 usage 不被惰性重置），usage/limit 由参数指定。
func admissionCard(t *testing.T, client *dbent.Client, userID, groupID int64, dLim, wLim, mLim, dUse, wUse, mUse float64) {
	t.Helper()
	today := service.TodayEastDayNumber()
	now := time.Now()
	sub := &service.UserSubscription{
		UserID:             userID,
		GroupID:            groupID,
		DailyAmountUSD:     dLim,
		DailyUsageUSD:      dUse,
		WeeklyUsageUSD:     wUse,
		MonthlyUsageUSD:    mUse,
		DailyWindowStart:   &now,
		WeeklyWindowStart:  &now,
		MonthlyWindowStart: &now,
		TodayRemaining:     dLim,
		TodayDay:           today,
		StartDay:           today - 1,
		ExpireDay:          today + 20,
		ExpiresAt:          service.ExpireDayToExpiresAt(today + 20),
		Status:             service.SubscriptionStatusActive,
	}
	if dLim > 0 {
		sub.DailyLimitUSD = &dLim
	}
	if wLim > 0 {
		sub.WeeklyLimitUSD = &wLim
	}
	if mLim > 0 {
		sub.MonthlyLimitUSD = &mLim
	}
	mustCreateSubscription(t, client, sub)
}

func TestCheckBillingEligibility_ThreeWindowAdmissionPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := newBillingCacheServiceForAdmission(t)

	// 卡用无 group（GroupID=0 → group_id NULL）：checkBalanceEligibility 只读卡的三窗口字段、不读 group，
	// 且 GetActiveByUserID 不按 group 过滤；不建 active 组可避免污染 TestListActiveByPlatform 等计数断言。
	newUserWithCard := func(t *testing.T, balance, dLim, wLim, mLim, dUse, wUse, mUse float64) *service.User {
		u := mustCreateUser(t, client, &service.User{
			Email:   fmt.Sprintf("admit-%s@example.com", uuid.NewString()),
			Balance: balance,
		})
		admissionCard(t, client, u.ID, 0, dLim, wLim, mLim, dUse, wUse, mUse)
		return u
	}

	cases := []struct {
		name                               string
		balance                            float64
		dLim, wLim, mLim, dUse, wUse, mUse float64
		wantErr                            error
	}{
		{"window has room → admit", 0, 10, 100, 400, 0, 0, 0, nil},
		{"daily exhausted + empty wallet → daily limit", 0, 10, 100, 400, 10, 0, 0, service.ErrDailyLimitExceeded},
		{"weekly exhausted + empty wallet → weekly limit", 0, 10, 100, 400, 0, 100, 0, service.ErrWeeklyLimitExceeded},
		{"monthly exhausted + empty wallet → monthly limit", 0, 10, 100, 400, 0, 0, 400, service.ErrMonthlyLimitExceeded},
		{"daily exhausted but wallet has funds → admit (fallback)", 5, 10, 100, 400, 10, 0, 0, nil},
		{"unconfigured safety-gate card + empty wallet → insufficient", 0, 0, 0, 0, 0, 0, 0, service.ErrInsufficientBalance},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUserWithCard(t, tc.balance, tc.dLim, tc.wLim, tc.mLim, tc.dUse, tc.wUse, tc.mUse)
			err := svc.CheckBillingEligibility(ctx, u, nil, nil, nil, "")
			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.wantErr)
			}
		})
	}
}

func TestGetActiveSubscriptionCard_Postgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := newBillingCacheServiceForAdmission(t)

	// 无卡用户 → (nil, nil)。
	noCardUser := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("nocard-%s@example.com", uuid.NewString())})
	card, err := svc.GetActiveSubscriptionCard(ctx, noCardUser.ID)
	require.NoError(t, err)
	require.Nil(t, card)

	// 有卡用户 → 返回该卡（无 group 卡，避免污染计数断言）。
	u := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("hascard-%s@example.com", uuid.NewString())})
	admissionCard(t, client, u.ID, 0, 10, 100, 400, 0, 0, 0)
	card, err = svc.GetActiveSubscriptionCard(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, card)
	require.Equal(t, u.ID, card.UserID)
}
