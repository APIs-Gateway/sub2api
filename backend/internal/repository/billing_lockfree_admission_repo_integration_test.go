//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 只读准入（AdmitBillingLockFree）与预占（ReserveBillingInflight 里的 AdmitWindow）必须逐场景给出相同的结果，
// 并且全程不加锁、不写 billing_inflight_leases。

type lockFreeAdmissionScenario struct {
	name    string
	balance float64
	// 订阅卡；nil 表示没有卡。
	card func(userID, groupID int64) *service.UserSubscription
	// 对卡做完创建后的额外 SQL（可空）。
	after func(t *testing.T, userID int64)
	// 期望：allowed 与 err（预占和只读准入应当一致）。
	wantAllowed bool
	wantErr     error
}

func lockFreeCard(d, w, m, du, wu, mu float64, mutate func(*service.UserSubscription)) func(userID, groupID int64) *service.UserSubscription {
	return func(userID, groupID int64) *service.UserSubscription {
		now := time.Now()
		today := service.TodayEastDayNumber()
		dl, wl, ml := d, w, m
		s := &service.UserSubscription{
			UserID: userID, GroupID: groupID,
			DailyAmountUSD: 1, TodayRemaining: 1, TodayDay: today, StartDay: today, ExpireDay: today + 12,
			ExpiresAt: service.ExpireDayToExpiresAt(today + 12), Status: service.SubscriptionStatusActive,
			DailyLimitUSD: &dl, WeeklyLimitUSD: &wl, MonthlyLimitUSD: &ml,
			DailyUsageUSD: du, WeeklyUsageUSD: wu, MonthlyUsageUSD: mu,
			DailyWindowStart: &now, WeeklyWindowStart: &now, MonthlyWindowStart: &now,
		}
		if mutate != nil {
			mutate(s)
		}
		return s
	}
}

func lockFreeNoLimitCard(u, g int64) *service.UserSubscription {
	s := lockFreeCard(0, 0, 0, 0, 0, 0, nil)(u, g)
	s.DailyLimitUSD, s.WeeklyLimitUSD, s.MonthlyLimitUSD = nil, nil, nil
	return s
}

func lockFreeAdmissionScenarios() []lockFreeAdmissionScenario {
	longAgo := time.Now().Add(-90 * 24 * time.Hour)
	return []lockFreeAdmissionScenario{
		{name: "no card, positive balance", balance: 10, wantAllowed: true},
		{name: "no card, zero balance", balance: 0, wantAllowed: false},
		{name: "no card, negative balance", balance: -5, wantAllowed: false},
		{name: "no card, tiny positive balance", balance: 0.00000001, wantAllowed: true},
		{name: "card unused, zero balance", balance: 0, card: lockFreeCard(10, 50, 100, 0, 0, 0, nil), wantAllowed: true},
		{name: "card partly used, negative balance", balance: -5, card: lockFreeCard(10, 50, 100, 1, 1, 1, nil), wantAllowed: true},
		// 三档限额全空的卡是「安全闸卡」：SubRemaining 返回 0（订阅不覆盖，见 subscription_window.go），
		// 只能回落钱包——余额 <=0 被拒（且没有撞限额码，所以是 allowed=false 而不是限额错误），余额 >0 放行。
		{name: "card with all limits NULL (safety card), zero balance", balance: 0, card: lockFreeNoLimitCard, wantAllowed: false},
		{name: "card with all limits NULL (safety card), positive balance", balance: 5, card: lockFreeNoLimitCard, wantAllowed: true},
		// 只配了一档（月）的卡：其余 NULL 档按「该窗口不限」处理，仍可用。
		{name: "card with only monthly limit configured, zero balance", balance: 0, card: func(u, g int64) *service.UserSubscription {
			s := lockFreeCard(0, 0, 100, 0, 0, 1, nil)(u, g)
			s.DailyLimitUSD, s.WeeklyLimitUSD = nil, nil
			return s
		}, wantAllowed: true},
		{name: "card daily exhausted, zero balance", balance: 0, card: lockFreeCard(10, 50, 100, 10, 10, 10, nil), wantErr: service.ErrDailyLimitExceeded},
		{name: "card weekly exhausted, zero balance", balance: 0, card: lockFreeCard(10, 50, 100, 1, 50, 50, nil), wantErr: service.ErrWeeklyLimitExceeded},
		{name: "card monthly exhausted, zero balance", balance: 0, card: lockFreeCard(10, 50, 100, 1, 1, 100, nil), wantErr: service.ErrMonthlyLimitExceeded},
		{name: "card exhausted, negative balance", balance: -3, card: lockFreeCard(10, 50, 100, 10, 10, 10, nil), wantErr: service.ErrDailyLimitExceeded},
		{name: "card exhausted but wallet positive falls back", balance: 5, card: lockFreeCard(10, 50, 100, 10, 10, 10, nil), wantAllowed: true},
		{name: "card exhausted windows long expired resets", balance: 0, card: lockFreeCard(10, 50, 100, 10, 50, 100, func(s *service.UserSubscription) {
			s.DailyWindowStart, s.WeeklyWindowStart, s.MonthlyWindowStart = &longAgo, &longAgo, &longAgo
		}), wantAllowed: true},
		{name: "card past expires_at but status active, zero balance", balance: 0, card: lockFreeCard(10, 50, 100, 0, 0, 0, func(s *service.UserSubscription) {
			s.ExpiresAt = time.Now().Add(-time.Hour)
		}), wantAllowed: false},
		{name: "card status expired is not selected, zero balance", balance: 0, card: lockFreeCard(10, 50, 100, 0, 0, 0, func(s *service.UserSubscription) {
			s.Status = service.SubscriptionStatusExpired
		}), wantAllowed: false},
		{name: "card soft-deleted is not selected, zero balance", balance: 0, card: lockFreeCard(10, 50, 100, 0, 0, 0, nil), after: func(t *testing.T, userID int64) {
			_, err := inflightTestDB(t).Exec(`UPDATE user_subscriptions SET deleted_at=NOW() WHERE user_id=$1`, userID)
			require.NoError(t, err)
		}, wantAllowed: false},
	}
}

func lockFreeSetupScenario(t *testing.T, s lockFreeAdmissionScenario) (*usageBillingRepository, int64) {
	t.Helper()
	repo, user, _ := inflightFixture(t, s.balance)
	if s.card != nil {
		client := inflightTestEntClient(t)
		group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString()})
		mustCreateSubscription(t, client, s.card(user.ID, group.ID))
	}
	if s.after != nil {
		s.after(t, user.ID)
	}
	return repo, user.ID
}

func lockFreeLeaseRows(t *testing.T, userID int64) int {
	t.Helper()
	var n int
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT COUNT(*) FROM billing_inflight_leases WHERE user_id=$1`, userID).Scan(&n))
	return n
}

func TestAdmitBillingLockFree_MatchesReservationAdmission(t *testing.T) {
	for _, sc := range lockFreeAdmissionScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			ctx := context.Background()
			repo, userID := lockFreeSetupScenario(t, sc)

			allowed, err := repo.AdmitBillingLockFree(ctx, userID)
			if sc.wantErr != nil {
				require.ErrorIs(t, err, sc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, sc.wantAllowed, allowed)
			require.Zero(t, lockFreeLeaseRows(t, userID), "只读准入不得写预占账本")

			// 对照：同一用户上预占的准入结果（first request：others=0，只有 AdmitWindow 把关）。
			reserved, reserveErr := repo.ReserveBillingInflight(ctx, userID, uuid.NewString(), 1, false, time.Minute)
			require.Equal(t, reserved, allowed, "与预占的 allowed 一致")
			if sc.wantErr != nil {
				require.True(t, errors.Is(reserveErr, sc.wantErr), "预占也返回 %v，实际 %v", sc.wantErr, reserveErr)
			} else {
				require.NoError(t, reserveErr)
			}
		})
	}
}

func TestAdmitBillingLockFree_UserNotFoundMatchesReservation(t *testing.T) {
	ctx := context.Background()
	repo, userID := lockFreeSetupScenario(t, lockFreeAdmissionScenario{name: "deleted", balance: 100})
	_, err := inflightTestDB(t).Exec(`UPDATE users SET deleted_at=NOW() WHERE id=$1`, userID)
	require.NoError(t, err)
	for _, id := range []int64{userID, userID + 987654321} {
		allowed, err := repo.AdmitBillingLockFree(ctx, id)
		require.False(t, allowed)
		require.ErrorIs(t, err, service.ErrUserNotFound)
		_, reserveErr := repo.ReserveBillingInflight(ctx, id, uuid.NewString(), 1, false, time.Minute)
		require.ErrorIs(t, reserveErr, service.ErrUserNotFound, "预占同样报 ErrUserNotFound（上层视为非拒绝错误，fail-open）")
	}
}

// 不加锁：别的事务把 users 行和订阅卡行都 FOR UPDATE 锁住，只读准入照样立即返回；对照预占会被挡住。
func TestAdmitBillingLockFree_DoesNotBlockOnRowLocks(t *testing.T) {
	repo, userID := lockFreeSetupScenario(t, lockFreeAdmissionScenario{
		name: "locked", balance: 10, card: lockFreeCard(10, 50, 100, 0, 0, 0, nil),
	})
	db := inflightTestDB(t)
	holder, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	var b float64
	require.NoError(t, holder.QueryRow(`SELECT balance FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&b))
	var cardID int64
	require.NoError(t, holder.QueryRow(`SELECT id FROM user_subscriptions WHERE user_id=$1 FOR UPDATE`, userID).Scan(&cardID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	allowed, err := repo.AdmitBillingLockFree(ctx, userID)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Less(t, time.Since(start), 2*time.Second, "只读准入不能等行锁")

	// 对照：预占要锁 users 行，被同一把锁挡住直到超时。
	blockedCtx, blockedCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer blockedCancel()
	_, reserveErr := repo.ReserveBillingInflight(blockedCtx, userID, uuid.NewString(), 1, false, time.Minute)
	require.Error(t, reserveErr, "对照组：预占应被行锁挡住")
	require.Zero(t, lockFreeLeaseRows(t, userID))
}

// 读失败 / 取消的 ctx 返回非拒绝错误（上层 fail-open）。
func TestAdmitBillingLockFree_CancelledContextIsNotADenial(t *testing.T) {
	repo, userID := lockFreeSetupScenario(t, lockFreeAdmissionScenario{name: "cancel", balance: 0})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	allowed, err := repo.AdmitBillingLockFree(ctx, userID)
	require.Error(t, err)
	require.False(t, allowed)
	require.False(t, errors.Is(err, service.ErrInsufficientBalance))
	require.False(t, errors.Is(err, service.ErrDailyLimitExceeded))
}
