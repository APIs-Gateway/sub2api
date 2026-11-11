package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.BillingLockFreeAdmissionReader = (*usageBillingRepository)(nil)

// AdmitBillingLockFree 是「计费免锁特权」用户的只读准入，见 service.BillingLockFreeAdmissionReader。
//
// 与预占（reserveBillingInflight -> billingInflightCapacity）的区别：
//   - 不开事务、不 SELECT ... FOR UPDATE（users 行和订阅卡行都不加锁），也不碰 billing_inflight_leases；
//   - 不计在途占用（held 当 0）。
//
// 相同点：用户余额取 users.balance（deleted_at IS NULL）；订阅卡取同一条
// 「status='active' AND deleted_at IS NULL ORDER BY expires_at DESC,id DESC LIMIT 1」，
// 窗口数据列、AdmitWindow 判定、撞限额时的三种错误码都与 billingInflightCapacity 一致
// （两者的一致性由 billing_lockfree_admission_repo_integration_test.go 逐场景对照）。
func (r *usageBillingRepository) AdmitBillingLockFree(ctx context.Context, userID int64) (bool, error) {
	// 预占账本只在 PostgreSQL 上生效；其他驱动上预占本身不拦截，只读准入同样放行。
	if !r.BillingInflightAvailable() {
		return true, nil
	}
	var balance float64
	err := r.db.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1 AND deleted_at IS NULL`, userID).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return false, service.ErrUserNotFound
	}
	if err != nil {
		return false, err
	}

	var card service.SubWindow
	var dl, wl, ml sql.NullFloat64
	var dw, ww, mw sql.NullTime
	err = r.db.QueryRowContext(ctx, `SELECT daily_limit_usd,weekly_limit_usd,monthly_limit_usd,
      daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start,expires_at,status
      FROM user_subscriptions WHERE user_id=$1 AND status='active' AND deleted_at IS NULL
      ORDER BY expires_at DESC,id DESC LIMIT 1`, userID).Scan(&dl, &wl, &ml, &card.DailyUsageUSD, &card.WeeklyUsageUSD, &card.MonthlyUsageUSD, &dw, &ww, &mw, &card.ExpiresAt, &card.Status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var c *service.SubWindow
	if err == nil {
		card.DailyLimitUSD = nullFloatZero(dl)
		card.WeeklyLimitUSD = nullFloatZero(wl)
		card.MonthlyLimitUSD = nullFloatZero(ml)
		card.DailyWindowStart = nullTimePtr(dw)
		card.WeeklyWindowStart = nullTimePtr(ww)
		card.MonthlyWindowStart = nullTimePtr(mw)
		c = &card
	}
	return billingLockFreeAdmit(c, balance, time.Now())
}

// billingLockFreeAdmit 是 billingInflightCapacity 里「准入判定 + 撞限额错误码」那一段的原样拷贝（不含容量计算）。
func billingLockFreeAdmit(c *service.SubWindow, balance float64, now time.Time) (bool, error) {
	allowed := service.AdmitWindow(c, &service.WalletState{Balance: balance}, now)
	if !allowed && c != nil && now.Before(c.ExpiresAt) {
		switch c.ExceededLimitCode() {
		case "DAILY":
			return false, service.ErrDailyLimitExceeded
		case "WEEKLY":
			return false, service.ErrWeeklyLimitExceeded
		case "MONTHLY":
			return false, service.ErrMonthlyLimitExceeded
		}
	}
	return allowed, nil
}
