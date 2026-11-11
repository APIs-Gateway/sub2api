package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// BillingLockFreeAdmissionReader 是特权用户（见 billing_lockfree_users.go）跳过在途预占时用的「只读准入」。
//
// 预占（ReserveBillingInflight）除了建 lease，还承担着「余额 / 订阅卡限额准入」：
// OpenAI Responses WebSocket 只在握手时做一次 CheckBillingEligibility，之后每一轮都靠预占里的
// AdmitWindow 判定余额和订阅卡的日/周/月限额。特权用户跳过预占后，这一半不能丢，所以改成：
//   - 不加任何行锁（不 SELECT ... FOR UPDATE，也不建 lease、不碰 billing_inflight_leases）地读出
//     用户余额和生效订阅卡的窗口数据；
//   - 读法和选卡条件与预占里的 billingInflightCapacity 完全一致，调用同一个 AdmitWindow；
//   - 区别只有一点：不计在途占用（held 当 0），也就不做「并发请求合计超过可用额度」的容量判断。
//
// 返回值与预占一致：
//   - allowed=false：余额 / 订阅额度不足（调用方转成 ErrInsufficientBalance）；
//   - 撞订阅卡日/周/月限额时返回 ErrDailyLimitExceeded / ErrWeeklyLimitExceeded / ErrMonthlyLimitExceeded；
//   - 其他错误（读库失败、超时、用户不存在）按预占失败同样处理：fail-open。
//
// 实现者必须是 *usageBillingRepository（repository 包）。非 PostgreSQL 驱动上预占本来就不生效，只读准入同样放行。
type BillingLockFreeAdmissionReader interface {
	AdmitBillingLockFree(ctx context.Context, userID int64) (bool, error)
}

// admitBillingLockFree 对特权用户做一次只读准入，语义与「预占被拒 / 预占失败」逐项一致：
// 被拒返回与预占相同的错误；读失败或超时（调用方已带 2 秒超时）只打 warn 并放行（fail-open，与预占失败一致）。
// 预占本身不生效的配置（总开关关闭、简单模式）下不做任何事，与 newBillingInflightLease 一致。
func admitBillingLockFree(ctx context.Context, reader BillingLockFreeAdmissionReader, cfg *config.Config, userID int64) error {
	if cfg == nil || !cfg.Billing.InflightReservation.Enabled || cfg.RunMode == config.RunModeSimple {
		return nil
	}
	allowed, err := reader.AdmitBillingLockFree(ctx, userID)
	if err != nil {
		if billingInflightDenial(err) {
			return err
		}
		logger.LegacyPrintf("service.billing_inflight", "Warning: read-only admission failed open user=%d: %v", userID, err)
		return nil
	}
	if !allowed {
		return ErrInsufficientBalance
	}
	return nil
}
