package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

type UserSubscriptionRepository interface {
	Create(ctx context.Context, sub *UserSubscription) error
	GetByID(ctx context.Context, id int64) (*UserSubscription, error)
	GetByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*UserSubscription, error)
	GetActiveByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*UserSubscription, error)
	// GetActiveByUserID 返回用户「唯一生效订阅卡」（三窗口单卡模式，不按 group 匹配）。
	// 生效口径为 status='active' 且 now < expires_at。无卡返回 ErrSubscriptionNotFound（调用方据此走纯钱包标准计费）。
	GetActiveByUserID(ctx context.Context, userID int64) (*UserSubscription, error)
	// GetLatestActiveStatusByUserID 返回最近一张 status='active' 的卡，允许 expires_at 已过期。
	// 仅供续费复活惰性过期卡使用；准入/结算不得使用此方法。
	GetLatestActiveStatusByUserID(ctx context.Context, userID int64) (*UserSubscription, error)
	// GetLatestActiveStatusForUpdate 同上但加行级 FOR UPDATE，供手动透支在事务内锁卡行（与结算串行）。
	GetLatestActiveStatusForUpdate(ctx context.Context, userID int64) (*UserSubscription, error)
	Update(ctx context.Context, sub *UserSubscription) error
	// ApplyManualOverdraft 原子落库手动透支：三窗口用量/起点 + expires_at + expire_day（借天 −1，两者同步）。
	// 须在持有该卡 FOR UPDATE 锁的事务内调用；不可用通用 Update（它不写 expire_day，会致两者分裂）。
	ApplyManualOverdraft(ctx context.Context, sub *UserSubscription) error
	Delete(ctx context.Context, id int64) error

	ListByUserID(ctx context.Context, userID int64) ([]UserSubscription, error)
	ListActiveByUserID(ctx context.Context, userID int64) ([]UserSubscription, error)
	ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]UserSubscription, *pagination.PaginationResult, error)
	List(ctx context.Context, params pagination.PaginationParams, userID, groupID *int64, status, platform, sortBy, sortOrder string) ([]UserSubscription, *pagination.PaginationResult, error)

	ExistsByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (bool, error)
	ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error
	UpdateStatus(ctx context.Context, subscriptionID int64, status string) error
	UpdateNotes(ctx context.Context, subscriptionID int64, notes string) error
	// SetOverdraftDays 设置某张订阅卡的「最多透支天数」（按 owner 作用域更新，days=nil 关闭透支）。
	// 返回是否命中（属于该用户的现存卡）；未命中 → false（不存在或非本人）。
	SetOverdraftDays(ctx context.Context, userID, subID int64, days *int) (bool, error)

	ActivateWindows(ctx context.Context, id int64, start time.Time) error
	ResetDailyUsage(ctx context.Context, id int64, newWindowStart time.Time) error
	ResetWeeklyUsage(ctx context.Context, id int64, newWindowStart time.Time) error
	ResetMonthlyUsage(ctx context.Context, id int64, newWindowStart time.Time) error
	IncrementUsage(ctx context.Context, id int64, costUSD float64) error

	BatchUpdateExpiredStatus(ctx context.Context) (int64, error)

	// ForfeitExpiredSubscriptions 处理已到期的活跃订阅：标记 expired 并把剩余订阅余额作废（同时扣减用户余额）。
	// 每次最多处理 limit 条，返回余额被扣减的用户 ID 列表（供失效余额缓存）。
	ForfeitExpiredSubscriptions(ctx context.Context, now time.Time, limit int) ([]int64, error)

	// CloseSubscriptionWithReclaim 关闭一张订阅（行级 FOR UPDATE 内）。per-day：卡价值在
	// today_remaining、不在钱包，**不回收 users.balance**（主动退款另走 payment_refund）。
	// deleteRow=true 删除该行（撤销）；false 则立即过期（status=expired、today_remaining=0、
	// expire_day<today）。reclaimed 恒为 0（保留返回签名）。找不到返回 0,0,nil。
	CloseSubscriptionWithReclaim(ctx context.Context, subID int64, now time.Time, deleteRow bool) (userID int64, reclaimed float64, err error)
	// ShortenSubscriptionWithReclaim 缩短订阅。per-day：expire_day −= reduceDays（下限 today−1），
	// expires_at 从 expire_day 派生，**不动 users.balance**。reclaimed 恒为 0（保留返回签名）。
	ShortenSubscriptionWithReclaim(ctx context.Context, subID int64, reduceDays int, newExpiresAt, now time.Time) (userID int64, reclaimed float64, err error)
	// GrantSubscriptionDays 延长订阅。per-day：expire_day = clamp(max(原, today−1) + addDays)，
	// expires_at 从 expire_day 派生，**不增发 users.balance**。granted 恒为 0（保留返回签名）。
	// 入参 newExpiresAt 仅历史签名，实际 expires_at 按 expire_day 派生。
	GrantSubscriptionDays(ctx context.Context, subID int64, addDays int, newExpiresAt, now time.Time) (userID int64, granted float64, err error)
}
