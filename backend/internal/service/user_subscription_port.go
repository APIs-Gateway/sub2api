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
	// GetActiveByUserID 返回用户「唯一生效订阅卡」（per-day 单卡模式，不按 group 匹配）。
	// 取 status='active' 中 expire_day 最晚的一张作兜底（理论至多一张）；过期判定（today>expire_day）
	// 由调用方按自然日惰性处理。无卡返回 ErrSubscriptionNotFound（调用方据此走纯钱包标准计费）。
	GetActiveByUserID(ctx context.Context, userID int64) (*UserSubscription, error)
	Update(ctx context.Context, sub *UserSubscription) error
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

	// ===== Burn-down 计费模型 =====
	// ListActiveBurndownIDs 返回需要参与每日清扣的活跃订阅 ID（id > afterID，按 id 升序，最多 limit 条）。
	ListActiveBurndownIDs(ctx context.Context, afterID int64, limit int) ([]int64, error)
	// ClawbackSubscription 对单张订阅做每日清扣（行级 FOR UPDATE 内重算）：
	// 若已进入新的日历天且消费落后于 N×D，则把差额从该卡剩余池与用户余额一并扣除。返回本次清扣金额。
	ClawbackSubscription(ctx context.Context, subID int64, now time.Time) (float64, error)
	// ForfeitExpiredSubscriptions 处理已到期的活跃订阅：标记 expired 并把剩余订阅余额作废（同时扣减用户余额）。
	// 每次最多处理 limit 条，返回余额被扣减的用户 ID 列表（供失效余额缓存）。
	ForfeitExpiredSubscriptions(ctx context.Context, now time.Time, limit int) ([]int64, error)

	// CloseSubscriptionWithReclaim 关闭一张订阅并回收其未花的发放余额（行级 FOR UPDATE 内重算）：
	// 把 remaining = granted-consumed-clawed 从该卡剩余池与用户余额一并扣除。
	// deleteRow=true 删除该行（撤销）；false 则置 status=expired、expires_at=now 并累加 clawed（整卡取消）。
	// 返回被扣减的用户 ID 与回收金额（找不到/已关闭则返回 0,0,nil）。
	CloseSubscriptionWithReclaim(ctx context.Context, subID int64, now time.Time, deleteRow bool) (userID int64, reclaimed float64, err error)
	// ShortenSubscriptionWithReclaim 缩短订阅天数并回收对应未花额度：
	// reclaimed = min(remaining, reduceDays×D)，granted_total -= reclaimed（保持 remaining 与总天数一致），
	// expires_at = newExpiresAt，同时从用户余额扣除 reclaimed。返回用户 ID 与回收金额。
	ShortenSubscriptionWithReclaim(ctx context.Context, subID int64, reduceDays int, newExpiresAt, now time.Time) (userID int64, reclaimed float64, err error)
	// GrantSubscriptionDays 延长订阅天数并按 D 增发额度：
	// granted = addDays×D，granted_total += granted，expires_at = newExpiresAt，同时给用户余额增发 granted。
	// 用于退款回滚 / 管理端增发，与 ShortenSubscriptionWithReclaim 对称。返回用户 ID 与增发金额。
	GrantSubscriptionDays(ctx context.Context, subID int64, addDays int, newExpiresAt, now time.Time) (userID int64, granted float64, err error)
}
