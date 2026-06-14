package repository

import (
	"context"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type userSubscriptionRepository struct {
	client *dbent.Client
}

func NewUserSubscriptionRepository(client *dbent.Client) service.UserSubscriptionRepository {
	return &userSubscriptionRepository{client: client}
}

func (r *userSubscriptionRepository) Create(ctx context.Context, sub *service.UserSubscription) error {
	if sub == nil {
		return service.ErrSubscriptionNilInput
	}

	client := clientFromContext(ctx, r.client)
	builder := client.UserSubscription.Create().
		SetUserID(sub.UserID).
		SetGroupID(sub.GroupID).
		SetExpiresAt(sub.ExpiresAt).
		SetNillableDailyWindowStart(sub.DailyWindowStart).
		SetNillableWeeklyWindowStart(sub.WeeklyWindowStart).
		SetNillableMonthlyWindowStart(sub.MonthlyWindowStart).
		SetDailyUsageUsd(sub.DailyUsageUSD).
		SetWeeklyUsageUsd(sub.WeeklyUsageUSD).
		SetMonthlyUsageUsd(sub.MonthlyUsageUSD).
		SetGrantedTotalUsd(sub.GrantedTotalUSD).
		SetDailyAmountUsd(sub.DailyAmountUSD).
		SetConsumedUsd(sub.ConsumedUSD).
		SetClawedUsd(sub.ClawedUSD).
		SetLastClawbackDay(sub.LastClawbackDay).
		SetNillableActivatedAt(sub.ActivatedAt).
		SetNillableAssignedBy(sub.AssignedBy)

	if sub.StartsAt.IsZero() {
		builder.SetStartsAt(time.Now())
	} else {
		builder.SetStartsAt(sub.StartsAt)
	}
	if sub.Status != "" {
		builder.SetStatus(sub.Status)
	}
	if !sub.AssignedAt.IsZero() {
		builder.SetAssignedAt(sub.AssignedAt)
	}
	// Keep compatibility with historical behavior: always store notes as a string value.
	builder.SetNotes(sub.Notes)

	created, err := builder.Save(ctx)
	if err == nil {
		applyUserSubscriptionEntityToService(sub, created)
	}
	return translatePersistenceError(err, nil, service.ErrSubscriptionAlreadyExists)
}

func (r *userSubscriptionRepository) GetByID(ctx context.Context, id int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.UserSubscription.Query().
		Where(usersubscription.IDEQ(id)).
		WithUser().
		WithGroup().
		WithAssignedByUser().
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

func (r *userSubscriptionRepository) GetByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	// burn-down 模型支持叠加多卡：同一 (user, group) 可能有多张订阅，
	// 取最新创建的一张（按 created_at 降序），避免 Only() 在多行时报错。
	m, err := client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID), usersubscription.GroupIDEQ(groupID)).
		WithGroup().
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

func (r *userSubscriptionRepository) GetActiveByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	// 叠加多卡时取最新创建的有效订阅。
	m, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.GroupIDEQ(groupID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
		).
		WithGroup().
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	return userSubscriptionEntityToService(m), nil
}

func (r *userSubscriptionRepository) Update(ctx context.Context, sub *service.UserSubscription) error {
	if sub == nil {
		return service.ErrSubscriptionNilInput
	}

	client := clientFromContext(ctx, r.client)
	builder := client.UserSubscription.UpdateOneID(sub.ID).
		SetUserID(sub.UserID).
		SetGroupID(sub.GroupID).
		SetStartsAt(sub.StartsAt).
		SetExpiresAt(sub.ExpiresAt).
		SetStatus(sub.Status).
		SetNillableDailyWindowStart(sub.DailyWindowStart).
		SetNillableWeeklyWindowStart(sub.WeeklyWindowStart).
		SetNillableMonthlyWindowStart(sub.MonthlyWindowStart).
		SetDailyUsageUsd(sub.DailyUsageUSD).
		SetWeeklyUsageUsd(sub.WeeklyUsageUSD).
		SetMonthlyUsageUsd(sub.MonthlyUsageUSD).
		SetNillableAssignedBy(sub.AssignedBy).
		SetAssignedAt(sub.AssignedAt).
		SetNotes(sub.Notes)

	updated, err := builder.Save(ctx)
	if err == nil {
		applyUserSubscriptionEntityToService(sub, updated)
		return nil
	}
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, service.ErrSubscriptionAlreadyExists)
}

func (r *userSubscriptionRepository) Delete(ctx context.Context, id int64) error {
	// Match GORM semantics: deleting a missing row is not an error.
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.Delete().Where(usersubscription.IDEQ(id)).Exec(ctx)
	return err
}

func (r *userSubscriptionRepository) SetOverdraftDays(ctx context.Context, userID, subID int64, days *int) (bool, error) {
	client := clientFromContext(ctx, r.client)
	upd := client.UserSubscription.Update().
		Where(usersubscription.IDEQ(subID), usersubscription.UserIDEQ(userID))
	if days != nil {
		upd = upd.SetMaxOverdraftDays(*days)
	} else {
		upd = upd.ClearMaxOverdraftDays()
	}
	affected, err := upd.Save(ctx)
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (r *userSubscriptionRepository) ListByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID)).
		WithGroup().
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(subs), nil
}

func (r *userSubscriptionRepository) ListActiveByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
		).
		WithGroup().
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(subs), nil
}

func (r *userSubscriptionRepository) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	q := client.UserSubscription.Query().Where(usersubscription.GroupIDEQ(groupID))

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	subs, err := q.
		WithUser().
		WithGroup().
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		Offset(params.Offset()).
		Limit(params.Limit()).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	return userSubscriptionEntitiesToService(subs), paginationResultFromTotal(int64(total), params), nil
}

func (r *userSubscriptionRepository) List(ctx context.Context, params pagination.PaginationParams, userID, groupID *int64, status, platform, sortBy, sortOrder string) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	q := client.UserSubscription.Query()
	if userID != nil {
		q = q.Where(usersubscription.UserIDEQ(*userID))
	}
	if groupID != nil {
		q = q.Where(usersubscription.GroupIDEQ(*groupID))
	}
	if platform != "" {
		q = q.Where(usersubscription.HasGroupWith(group.PlatformEQ(platform)))
	}

	// Status filtering with real-time expiration check
	now := time.Now()
	switch status {
	case service.SubscriptionStatusActive:
		// Active: status is active AND not yet expired
		q = q.Where(
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(now),
		)
	case service.SubscriptionStatusExpired:
		// Expired: status is expired OR (status is active but already expired)
		q = q.Where(
			usersubscription.Or(
				usersubscription.StatusEQ(service.SubscriptionStatusExpired),
				usersubscription.And(
					usersubscription.StatusEQ(service.SubscriptionStatusActive),
					usersubscription.ExpiresAtLTE(now),
				),
			),
		)
	case "":
		// No filter
	default:
		// Other status (e.g., revoked)
		q = q.Where(usersubscription.StatusEQ(status))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Apply sorting
	q = q.WithUser().WithGroup().WithAssignedByUser()

	// Determine sort field
	var field string
	switch sortBy {
	case "expires_at":
		field = usersubscription.FieldExpiresAt
	case "status":
		field = usersubscription.FieldStatus
	default:
		field = usersubscription.FieldCreatedAt
	}

	// Determine sort order (default: desc)
	if sortOrder == "asc" && sortBy != "" {
		q = q.Order(dbent.Asc(field))
	} else {
		q = q.Order(dbent.Desc(field))
	}

	subs, err := q.
		Offset(params.Offset()).
		Limit(params.Limit()).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	return userSubscriptionEntitiesToService(subs), paginationResultFromTotal(int64(total), params), nil
}

func (r *userSubscriptionRepository) ExistsByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (bool, error) {
	client := clientFromContext(ctx, r.client)
	return client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID), usersubscription.GroupIDEQ(groupID)).
		Exist(ctx)
}

func (r *userSubscriptionRepository) ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(subscriptionID).
		SetExpiresAt(newExpiresAt).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) UpdateStatus(ctx context.Context, subscriptionID int64, status string) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(subscriptionID).
		SetStatus(status).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) UpdateNotes(ctx context.Context, subscriptionID int64, notes string) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(subscriptionID).
		SetNotes(notes).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ActivateWindows(ctx context.Context, id int64, start time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).
		SetDailyWindowStart(start).
		SetWeeklyWindowStart(start).
		SetMonthlyWindowStart(start).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ResetDailyUsage(ctx context.Context, id int64, newWindowStart time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).
		SetDailyUsageUsd(0).
		SetDailyWindowStart(newWindowStart).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ResetWeeklyUsage(ctx context.Context, id int64, newWindowStart time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).
		SetWeeklyUsageUsd(0).
		SetWeeklyWindowStart(newWindowStart).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ResetMonthlyUsage(ctx context.Context, id int64, newWindowStart time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).
		SetMonthlyUsageUsd(0).
		SetMonthlyWindowStart(newWindowStart).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

// IncrementUsage 原子性地累加订阅用量。
// 限额检查已在请求前由 BillingCacheService.CheckBillingEligibility 完成，
// 此处仅负责记录实际消费，确保消费数据的完整性。
func (r *userSubscriptionRepository) IncrementUsage(ctx context.Context, id int64, costUSD float64) error {
	const updateSQL = `
		UPDATE user_subscriptions us
		SET
			daily_usage_usd = us.daily_usage_usd + $1,
			weekly_usage_usd = us.weekly_usage_usd + $1,
			monthly_usage_usd = us.monthly_usage_usd + $1,
			updated_at = NOW()
		FROM groups g
		WHERE us.id = $2
			AND us.deleted_at IS NULL
			AND us.group_id = g.id
			AND g.deleted_at IS NULL
	`

	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(ctx, updateSQL, costUSD, id)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected > 0 {
		return nil
	}

	// affected == 0：订阅不存在或已删除
	return service.ErrSubscriptionNotFound
}

// ListActiveBurndownIDs 返回需要参与每日清扣的活跃订阅 ID。
func (r *userSubscriptionRepository) ListActiveBurndownIDs(ctx context.Context, afterID int64, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 200
	}
	client := clientFromContext(ctx, r.client)
	ids, err := client.UserSubscription.Query().
		Where(
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
			usersubscription.DailyAmountUsdGT(0),
			usersubscription.IDGT(afterID),
		).
		Order(dbent.Asc(usersubscription.FieldID)).
		Limit(limit).
		IDs(ctx)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// ClawbackSubscription 对单张订阅做每日清扣（行级 FOR UPDATE 内重算，避免与计费扣减竞态）。
func (r *userSubscriptionRepository) ClawbackSubscription(ctx context.Context, subID int64, now time.Time) (float64, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	m, err := tx.UserSubscription.Query().
		Where(
			usersubscription.IDEQ(subID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(now),
		).
		ForUpdate().
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			// 已过期/已删除/状态变更：本轮跳过，由到期作废流程处理。
			return 0, nil
		}
		return 0, err
	}

	sub := userSubscriptionEntityToService(m)
	n := sub.CalendarDayAt(now)
	// 幂等游标：同一日历天只对账一次。
	if n <= sub.LastClawbackDay {
		committed = true
		return 0, tx.Commit()
	}
	shortfall := sub.ClawbackShortfallAt(now)

	upd := tx.UserSubscription.UpdateOneID(subID).SetLastClawbackDay(n)
	if shortfall > 0 {
		upd = upd.AddClawedUsd(shortfall)
	}
	if _, err := upd.Save(ctx); err != nil {
		return 0, err
	}
	if shortfall > 0 {
		// 清扣额 ≤ 本卡剩余，永不动用户充值余额。
		if _, err := tx.User.UpdateOneID(sub.UserID).AddBalance(-shortfall).Save(ctx); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	committed = true
	return shortfall, nil
}

// ForfeitExpiredSubscriptions 处理已到期的活跃订阅：标记 expired 并作废剩余订阅余额。
func (r *userSubscriptionRepository) ForfeitExpiredSubscriptions(ctx context.Context, now time.Time, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 200
	}
	client := clientFromContext(ctx, r.client)

	affectedUserIDs := make([]int64, 0)
	// 每处理一条即把状态置为 expired，故按 id 升序重复取批直到取空。
	// maxBatches 兜底，避免极端数据量下单次运行过久；剩余的下一轮继续处理。
	const maxBatches = 100
	for b := 0; b < maxBatches; b++ {
		ids, err := client.UserSubscription.Query().
			Where(
				usersubscription.StatusEQ(service.SubscriptionStatusActive),
				usersubscription.ExpiresAtLTE(now),
			).
			Order(dbent.Asc(usersubscription.FieldID)).
			Limit(limit).
			IDs(ctx)
		if err != nil {
			return affectedUserIDs, err
		}
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			userID, forfeited, ferr := r.forfeitOneExpired(ctx, id, now)
			if ferr != nil {
				return affectedUserIDs, ferr
			}
			if forfeited > 0 && userID > 0 {
				affectedUserIDs = append(affectedUserIDs, userID)
			}
		}
		if len(ids) < limit {
			break
		}
	}
	return affectedUserIDs, nil
}

func (r *userSubscriptionRepository) forfeitOneExpired(ctx context.Context, subID int64, now time.Time) (int64, float64, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return 0, 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	m, err := tx.UserSubscription.Query().
		Where(
			usersubscription.IDEQ(subID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtLTE(now),
		).
		ForUpdate().
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	sub := userSubscriptionEntityToService(m)
	remaining := sub.RemainingUSD()

	upd := tx.UserSubscription.UpdateOneID(subID).SetStatus(service.SubscriptionStatusExpired)
	if remaining > 0 {
		upd = upd.AddClawedUsd(remaining)
	}
	if _, err := upd.Save(ctx); err != nil {
		return 0, 0, err
	}
	if remaining > 0 {
		if _, err := tx.User.UpdateOneID(sub.UserID).AddBalance(-remaining).Save(ctx); err != nil {
			return 0, 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	committed = true
	return sub.UserID, remaining, nil
}

// reclaimTx 在「有 ambient ent 事务则复用、否则自开」的事务里执行 fn。
// 复用时不提交/回滚（交由外层事务所有者，如兑换码流程）；自开时负责提交与回滚。
// 这样既能被后台/管理端（无外层事务）直接调用，也能在兑换码外层事务内安全调用，
// 避免在同一连接上嵌套开启新事务导致死锁。
func (r *userSubscriptionRepository) reclaimTx(ctx context.Context, fn func(tx *dbent.Tx) error) error {
	if ambient := dbent.TxFromContext(ctx); ambient != nil {
		return fn(ambient)
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// CloseSubscriptionWithReclaim 见接口注释。
func (r *userSubscriptionRepository) CloseSubscriptionWithReclaim(ctx context.Context, subID int64, now time.Time, deleteRow bool) (int64, float64, error) {
	var userID int64
	var reclaimed float64
	err := r.reclaimTx(ctx, func(tx *dbent.Tx) error {
		m, err := tx.UserSubscription.Query().
			Where(usersubscription.IDEQ(subID)).
			ForUpdate().
			Only(ctx)
		if err != nil {
			if dbent.IsNotFound(err) {
				return nil
			}
			return err
		}
		sub := userSubscriptionEntityToService(m)
		userID = sub.UserID
		reclaimed = sub.RemainingUSD()

		if deleteRow {
			if _, err := tx.UserSubscription.Delete().Where(usersubscription.IDEQ(subID)).Exec(ctx); err != nil {
				return err
			}
		} else {
			upd := tx.UserSubscription.UpdateOneID(subID).
				SetStatus(service.SubscriptionStatusExpired).
				SetExpiresAt(now)
			if reclaimed > 0 {
				upd = upd.AddClawedUsd(reclaimed)
			}
			if _, err := upd.Save(ctx); err != nil {
				return err
			}
		}
		if reclaimed > 0 {
			if _, err := tx.User.UpdateOneID(sub.UserID).AddBalance(-reclaimed).Save(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return userID, reclaimed, nil
}

// ShortenSubscriptionWithReclaim 见接口注释。
func (r *userSubscriptionRepository) ShortenSubscriptionWithReclaim(ctx context.Context, subID int64, reduceDays int, newExpiresAt, now time.Time) (int64, float64, error) {
	var userID int64
	var reclaimed float64
	err := r.reclaimTx(ctx, func(tx *dbent.Tx) error {
		m, err := tx.UserSubscription.Query().
			Where(usersubscription.IDEQ(subID)).
			ForUpdate().
			Only(ctx)
		if err != nil {
			if dbent.IsNotFound(err) {
				return nil
			}
			return err
		}
		sub := userSubscriptionEntityToService(m)
		userID = sub.UserID

		remaining := sub.RemainingUSD()
		maxReclaim := float64(reduceDays) * sub.DailyAmountUSD
		reclaimed = maxReclaim
		if reclaimed > remaining {
			reclaimed = remaining
		}
		if reclaimed < 0 {
			reclaimed = 0
		}

		upd := tx.UserSubscription.UpdateOneID(subID).SetExpiresAt(newExpiresAt)
		if reclaimed > 0 {
			upd = upd.AddGrantedTotalUsd(-reclaimed)
		}
		if _, err := upd.Save(ctx); err != nil {
			return err
		}
		if reclaimed > 0 {
			if _, err := tx.User.UpdateOneID(sub.UserID).AddBalance(-reclaimed).Save(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return userID, reclaimed, nil
}

// GrantSubscriptionDays 见接口注释。
func (r *userSubscriptionRepository) GrantSubscriptionDays(ctx context.Context, subID int64, addDays int, newExpiresAt, now time.Time) (int64, float64, error) {
	var userID int64
	var granted float64
	err := r.reclaimTx(ctx, func(tx *dbent.Tx) error {
		m, err := tx.UserSubscription.Query().
			Where(usersubscription.IDEQ(subID)).
			ForUpdate().
			Only(ctx)
		if err != nil {
			if dbent.IsNotFound(err) {
				return nil
			}
			return err
		}
		sub := userSubscriptionEntityToService(m)
		userID = sub.UserID

		granted = float64(addDays) * sub.DailyAmountUSD
		if granted < 0 {
			granted = 0
		}

		upd := tx.UserSubscription.UpdateOneID(subID).SetExpiresAt(newExpiresAt)
		if granted > 0 {
			upd = upd.AddGrantedTotalUsd(granted)
		}
		if _, err := upd.Save(ctx); err != nil {
			return err
		}
		if granted > 0 {
			if _, err := tx.User.UpdateOneID(sub.UserID).AddBalance(granted).Save(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return userID, granted, nil
}

func (r *userSubscriptionRepository) BatchUpdateExpiredStatus(ctx context.Context) (int64, error) {
	client := clientFromContext(ctx, r.client)
	n, err := client.UserSubscription.Update().
		Where(
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtLTE(time.Now()),
		).
		SetStatus(service.SubscriptionStatusExpired).
		Save(ctx)
	return int64(n), err
}

// Extra repository helpers (currently used only by integration tests).

func (r *userSubscriptionRepository) ListExpired(ctx context.Context) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtLTE(time.Now()),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(subs), nil
}

func (r *userSubscriptionRepository) CountByGroupID(ctx context.Context, groupID int64) (int64, error) {
	client := clientFromContext(ctx, r.client)
	count, err := client.UserSubscription.Query().Where(usersubscription.GroupIDEQ(groupID)).Count(ctx)
	return int64(count), err
}

func (r *userSubscriptionRepository) CountActiveByGroupID(ctx context.Context, groupID int64) (int64, error) {
	client := clientFromContext(ctx, r.client)
	count, err := client.UserSubscription.Query().
		Where(
			usersubscription.GroupIDEQ(groupID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
		).
		Count(ctx)
	return int64(count), err
}

func (r *userSubscriptionRepository) DeleteByGroupID(ctx context.Context, groupID int64) (int64, error) {
	client := clientFromContext(ctx, r.client)
	n, err := client.UserSubscription.Delete().Where(usersubscription.GroupIDEQ(groupID)).Exec(ctx)
	return int64(n), err
}

func userSubscriptionEntityToService(m *dbent.UserSubscription) *service.UserSubscription {
	if m == nil {
		return nil
	}
	out := &service.UserSubscription{
		ID:                 m.ID,
		UserID:             m.UserID,
		GroupID:            m.GroupID,
		StartsAt:           m.StartsAt,
		ExpiresAt:          m.ExpiresAt,
		Status:             m.Status,
		DailyWindowStart:   m.DailyWindowStart,
		WeeklyWindowStart:  m.WeeklyWindowStart,
		MonthlyWindowStart: m.MonthlyWindowStart,
		DailyUsageUSD:      m.DailyUsageUsd,
		WeeklyUsageUSD:     m.WeeklyUsageUsd,
		MonthlyUsageUSD:    m.MonthlyUsageUsd,
		GrantedTotalUSD:    m.GrantedTotalUsd,
		DailyAmountUSD:     m.DailyAmountUsd,
		ConsumedUSD:        m.ConsumedUsd,
		ClawedUSD:          m.ClawedUsd,
		LastClawbackDay:    m.LastClawbackDay,
		MaxOverdraftDays:   m.MaxOverdraftDays,
		ActivatedAt:        m.ActivatedAt,
		AssignedBy:         m.AssignedBy,
		AssignedAt:         m.AssignedAt,
		Notes:              derefString(m.Notes),
		CreatedAt:          m.CreatedAt,
		UpdatedAt:          m.UpdatedAt,
	}
	if m.Edges.User != nil {
		out.User = userEntityToService(m.Edges.User)
	}
	if m.Edges.Group != nil {
		out.Group = groupEntityToService(m.Edges.Group)
	}
	if m.Edges.AssignedByUser != nil {
		out.AssignedByUser = userEntityToService(m.Edges.AssignedByUser)
	}
	return out
}

func userSubscriptionEntitiesToService(models []*dbent.UserSubscription) []service.UserSubscription {
	out := make([]service.UserSubscription, 0, len(models))
	for i := range models {
		if s := userSubscriptionEntityToService(models[i]); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

func applyUserSubscriptionEntityToService(dst *service.UserSubscription, src *dbent.UserSubscription) {
	if dst == nil || src == nil {
		return
	}
	dst.ID = src.ID
	dst.CreatedAt = src.CreatedAt
	dst.UpdatedAt = src.UpdatedAt
}
