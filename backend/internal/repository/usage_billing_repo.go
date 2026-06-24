package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type usageBillingRepository struct {
	db *sql.DB
}

func NewUsageBillingRepository(_ *dbent.Client, sqlDB *sql.DB) service.UsageBillingRepository {
	return &usageBillingRepository{db: sqlDB}
}

func (r *usageBillingRepository) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (_ *service.UsageBillingApplyResult, err error) {
	if cmd == nil {
		return &service.UsageBillingApplyResult{}, nil
	}
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}

	cmd.Normalize()
	if cmd.RequestID == "" {
		return nil, service.ErrUsageBillingRequestIDRequired
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	applied, err := r.claimUsageBillingKey(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	if !applied {
		return &service.UsageBillingApplyResult{Applied: false}, nil
	}

	result := &service.UsageBillingApplyResult{Applied: true}
	if err := r.applyUsageBillingEffects(ctx, tx, cmd, result); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return result, nil
}

func (r *usageBillingRepository) claimUsageBillingKey(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) (bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO usage_billing_dedup (request_id, api_key_id, request_fingerprint)
		VALUES ($1, $2, $3)
		ON CONFLICT (request_id, api_key_id) DO NOTHING
		RETURNING id
	`, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var existingFingerprint string
		if err := tx.QueryRowContext(ctx, `
			SELECT request_fingerprint
			FROM usage_billing_dedup
			WHERE request_id = $1 AND api_key_id = $2
		`, cmd.RequestID, cmd.APIKeyID).Scan(&existingFingerprint); err != nil {
			return false, err
		}
		if strings.TrimSpace(existingFingerprint) != strings.TrimSpace(cmd.RequestFingerprint) {
			return false, service.ErrUsageBillingRequestConflict
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var archivedFingerprint string
	err = tx.QueryRowContext(ctx, `
		SELECT request_fingerprint
		FROM usage_billing_dedup_archive
		WHERE request_id = $1 AND api_key_id = $2
	`, cmd.RequestID, cmd.APIKeyID).Scan(&archivedFingerprint)
	if err == nil {
		if strings.TrimSpace(archivedFingerprint) != strings.TrimSpace(cmd.RequestFingerprint) {
			return false, service.ErrUsageBillingRequestConflict
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return true, nil
}

func (r *usageBillingRepository) applyUsageBillingEffects(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, result *service.UsageBillingApplyResult) error {
	// per-day 结算：按瀑布把官方成本结算到「用户唯一生效卡 + 钱包」（套餐 1:1 → 钱包正余额×倍率
	// → 透支借未来天 → 钱包负数）。锁序 user→card，整套副作用同 tx，dedup 覆盖整笔。
	// OfficialCost = 官方价；legacy/测试只传 BalanceCost 时回退（官方=BalanceCost、倍率1，无卡即纯钱包，
	// 等价旧 deductUsageBillingBalance）。
	officialCost, multiplier := cmd.OfficialCost, cmd.RateMultiplier
	if officialCost <= 0 && cmd.BalanceCost > 0 {
		officialCost, multiplier = cmd.BalanceCost, 1
	}
	if officialCost > 0 {
		settleRes, err := settlePerDaySubscription(ctx, tx, cmd.UserID, officialCost, multiplier, time.Now())
		if err != nil {
			return err
		}
		result.NewBalance = &settleRes.newBalance
		result.WalletDebit = &settleRes.walletDebit
		if settleRes.expiredGroupID != nil {
			result.DepletedSubscriptionGroupIDs = []int64{*settleRes.expiredGroupID}
		}
	}

	if cmd.APIKeyQuotaCost > 0 {
		exhausted, err := incrementUsageBillingAPIKeyQuota(ctx, tx, cmd.APIKeyID, cmd.APIKeyQuotaCost)
		if err != nil {
			return err
		}
		result.APIKeyQuotaExhausted = exhausted
	}

	if cmd.APIKeyRateLimitCost > 0 {
		if err := incrementUsageBillingAPIKeyRateLimit(ctx, tx, cmd.APIKeyID, cmd.APIKeyRateLimitCost); err != nil {
			return err
		}
	}

	if cmd.AccountQuotaCost > 0 && (strings.EqualFold(cmd.AccountType, service.AccountTypeAPIKey) || strings.EqualFold(cmd.AccountType, service.AccountTypeBedrock)) {
		quotaState, err := incrementUsageBillingAccountQuota(ctx, tx, cmd.AccountID, cmd.AccountQuotaCost)
		if err != nil {
			return err
		}
		result.QuotaState = quotaState
	}

	return nil
}

// perDaySettleResult 是 per-day 结算的落库结果（供上层重建旧余额、失效缓存）。
type perDaySettleResult struct {
	newBalance     float64 // 结算后钱包余额
	walletDebit    float64 // 本次从钱包实扣的售价货币额（钱包正余额 + 钱包负数兜底；套餐 1:1 部分不计）
	expiredGroupID *int64  // 本次把卡惰性标记为 expired 时其 group_id（供失效订阅缓存）；否则 nil
}

// settlePerDaySubscription 按 per-day 瀑布把一笔请求的官方成本结算到「用户唯一生效卡 + 钱包」。
// 锁序固定 user→card（与购买/续费/转套餐一致，防死锁）。无生效卡 → 纯钱包标准计费（官方价×倍率）。
// 整套副作用（套餐扣减、钱包扣减、透支 expire_day−1+用户级月度计数、钱包负数）在本事务内原子完成；
// dedup（claimUsageBillingKey）与本函数同 tx，重放整笔跳过。瀑布逻辑复用 service.Settle（已穷尽单测）。
func settlePerDaySubscription(ctx context.Context, tx *sql.Tx, userID int64, officialCost, multiplier float64, now time.Time) (*perDaySettleResult, error) {
	today := service.EastDayNumber(now)
	monthKey := service.EastMonthKey(now)

	// 1) 锁 user 行（balance + 用户级月度透支计数）。
	var balance float64
	var monthCount int
	var monthStr string
	if err := tx.QueryRowContext(ctx, `
		SELECT balance, monthly_overdraft_count, monthly_overdraft_month
		FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE
	`, userID).Scan(&balance, &monthCount, &monthStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrUserNotFound
		}
		return nil, err
	}

	// 2) 锁该用户唯一生效卡（status='active'，expire_day 最晚兜底）；可能无卡。
	//    过期是惰性的（卡可能 status='active' 而 today>expire_day），不在 SQL 过滤 expire_day，
	//    由引擎 ResetIfNewDay 惰性置 0 + 标 expired。
	var (
		cardID    int64
		groupID   int64
		dAmount   float64
		todayRem  float64
		todayDay  int
		startDay  int
		expireDay int
		odOn      bool
		hasCard   bool
	)
	err := tx.QueryRowContext(ctx, `
		SELECT id, group_id, daily_amount_usd, today_remaining, today_day, start_day, expire_day, overdraft_on
		FROM user_subscriptions
		WHERE user_id = $1 AND status = 'active' AND deleted_at IS NULL
		ORDER BY expire_day DESC, id DESC
		LIMIT 1
		FOR UPDATE
	`, userID).Scan(&cardID, &groupID, &dAmount, &todayRem, &todayDay, &startDay, &expireDay, &odOn)
	switch {
	case err == nil:
		hasCard = true
	case errors.Is(err, sql.ErrNoRows):
		hasCard = false
	default:
		return nil, err
	}

	// 3) 跑引擎（无卡时用零值卡：ResetIfNewDay 会把它标过期、套餐层贡献 0 → 纯钱包）。
	wallet := service.WalletState{Balance: balance, MonthlyOverdraftCount: monthCount, MonthlyOverdraftMonth: monthStr}
	var card service.PerDayCard
	if hasCard {
		card = service.PerDayCard{
			DailyAmountUSD: dAmount,
			TodayRemaining: todayRem,
			TodayDay:       todayDay,
			StartDay:       startDay,
			ExpireDay:      expireDay,
			OverdraftOn:    odOn,
		}
	}
	service.Settle(&card, &wallet, officialCost, multiplier, today, monthKey)

	out := &perDaySettleResult{}

	// 4) 回写卡（仅有卡时）：today_remaining/today_day/expire_day + 惰性过期。
	if hasCard {
		var nowExpired bool
		var gid int64
		if err := tx.QueryRowContext(ctx, `
			UPDATE user_subscriptions
			SET today_remaining = $1, today_day = $2, expire_day = $3,
				status = CASE WHEN $4 THEN 'expired' ELSE status END,
				updated_at = NOW()
			WHERE id = $5
			RETURNING status = 'expired', group_id
		`, card.TodayRemaining, card.TodayDay, card.ExpireDay, card.Expired, cardID).Scan(&nowExpired, &gid); err != nil {
			return nil, err
		}
		if nowExpired { // 本次刚由 active→expired（加载时为 active），失效订阅缓存
			g := gid
			out.expiredGroupID = &g
		}
	}

	// 5) 回写 user：balance + 月度透支计数（含惰性按月重置；同 tx 内原子）。
	if err := tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = $1, monthly_overdraft_count = $2, monthly_overdraft_month = $3, updated_at = NOW()
		WHERE id = $4 AND deleted_at IS NULL
		RETURNING balance
	`, wallet.Balance, wallet.MonthlyOverdraftCount, wallet.MonthlyOverdraftMonth, userID).Scan(&out.newBalance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrUserNotFound
		}
		return nil, err
	}
	out.walletDebit = balance - wallet.Balance
	return out, nil
}

// Deprecated: burn-down 结算，已被 settlePerDaySubscription 取代、无调用方。
// 保留以兼容旧 burn-down 集成测试的编译，待 P8（退役清扣 + burn-down 残留清理）一并删除。
//
// allocateUsageBillingSubscriptions 按「订阅优先、按激活先后（先激活的卡先扣）」把本次消费 cost
// 归集到该用户各活跃订阅卡的 consumed_usd。每张卡最多消费其 remaining，超出所有订阅的部分
// 由充值余额承担（无需额外记账）。consumed_usd 仅用于每日清扣判定与进度展示，
// 实际可用额度始终以 users.balance 为准。
//
// 锁定顺序：SELECT ... FOR UPDATE 按 (activated_at, id) 确定序锁定订阅行，
// 与每日清扣 / 到期作废事务一致，避免交叉死锁。
// subscriptionDepletionEpsilon 判定 burn-down 卡「剩余被扣到 0」的容差（granted/consumed/clawed
// 为 numeric(20,10)，浮点尾差用此阈值吸收）。低于此值即视为用完。
const subscriptionDepletionEpsilon = 1e-7

func allocateUsageBillingSubscriptions(ctx context.Context, tx *sql.Tx, userID int64, cost float64) ([]int64, error) {
	if cost <= 0 {
		return nil, nil
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, group_id, starts_at, activated_at, granted_total_usd, daily_amount_usd,
			consumed_usd, clawed_usd, max_overdraft_days, total_overdraft_count,
			daily_spent_usd, daily_spent_day,
			(granted_total_usd - consumed_usd - clawed_usd) AS remaining
		FROM user_subscriptions
		WHERE user_id = $1
			AND status = 'active'
			AND deleted_at IS NULL
			AND expires_at > NOW()
			AND (granted_total_usd - consumed_usd - clawed_usd) > 0
		ORDER BY activated_at ASC NULLS FIRST, id ASC
		FOR UPDATE
	`, userID)
	if err != nil {
		return nil, err
	}

	type subRemaining struct {
		id                  int64
		groupID             int64
		startsAt            time.Time
		activatedAt         *time.Time
		grantedTotalUSD     float64
		dailyAmountUSD      float64
		consumedUSD         float64
		clawedUSD           float64
		maxOverdraftDays    *int
		totalOverdraftCount int
		dailySpentUSD       float64
		dailySpentDay       int
		remaining           float64
	}
	var subs []subRemaining
	for rows.Next() {
		var s subRemaining
		var activatedAt sql.NullTime
		var maxOverdraftDays sql.NullInt64
		if err := rows.Scan(
			&s.id,
			&s.groupID,
			&s.startsAt,
			&activatedAt,
			&s.grantedTotalUSD,
			&s.dailyAmountUSD,
			&s.consumedUSD,
			&s.clawedUSD,
			&maxOverdraftDays,
			&s.totalOverdraftCount,
			&s.dailySpentUSD,
			&s.dailySpentDay,
			&s.remaining,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if activatedAt.Valid {
			t := activatedAt.Time
			s.activatedAt = &t
		}
		if maxOverdraftDays.Valid {
			days := int(maxOverdraftDays.Int64)
			s.maxOverdraftDays = &days
		}
		subs = append(subs, s)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	// pq 驱动：同一连接上必须先耗尽并关闭 rows，才能执行后续 UPDATE。
	if err := rows.Close(); err != nil {
		return nil, err
	}

	now := time.Now()
	var depletedGroupIDs []int64

	// 物化每卡 service 模型,以复用 per-day 口径(SpendableNowAt / EffectiveOverdraftDaysAt)。
	type allocState struct {
		s     *subRemaining
		sub   *service.UserSubscription
		pass1 float64 // 计入透支额度内的分摊(用于 total_overdraft_count 的 delta)
		total float64 // 本卡总分摊(pass1 + slippage 超调)
	}
	states := make([]allocState, len(subs))
	var sumRemaining float64 // Σ活跃订阅卡 remaining,用于推算充值(非订阅)余额 = balance − sumRemaining
	for i := range subs {
		s := &subs[i]
		sumRemaining += s.remaining
		states[i] = allocState{
			s: s,
			sub: &service.UserSubscription{
				StartsAt:            s.startsAt,
				ActivatedAt:         s.activatedAt,
				GrantedTotalUSD:     s.grantedTotalUSD,
				DailyAmountUSD:      s.dailyAmountUSD,
				ConsumedUSD:         s.consumedUSD,
				ClawedUSD:           s.clawedUSD,
				MaxOverdraftDays:    s.maxOverdraftDays,
				TotalOverdraftCount: s.totalOverdraftCount,
				DailySpentUSD:       s.dailySpentUSD,
				DailySpentDay:       s.dailySpentDay,
			},
		}
	}

	// Pass 1:按激活先后,在各卡「今日可花」额度内分摊(尊重每卡每日限速 D + 生命周期剩余预支额度)。
	// 与准入闸门 aggregateSubscriptionLocks 同口径,避免「老卡今日已限速却被先扣、新卡反而不动」。
	remainingCost := cost
	for i := range states {
		if remainingCost <= 0 {
			break
		}
		capAmt := states[i].sub.SpendableNowAt(now, states[i].sub.EffectiveOverdraftDaysAt(now))
		a := capAmt
		if a > remainingCost {
			a = remainingCost
		}
		if a <= 0 {
			continue
		}
		states[i].pass1 += a
		states[i].total += a
		remainingCost -= a
	}
	// 溢出处理:单笔成本超过所有卡「今日可花」之和时——
	// (1) 先由「充值(非订阅)余额 = balance − Σ订阅remaining」承担,不动订阅卡的锁定额度
	//     (用户自己充的钱不受订阅每日限速;deductUsageBillingBalance 会从总余额如实扣减);
	// (2) 充值也不够,才 slippage 下探各卡 remaining(保 balance≥Σremaining 不变量;
	//     这部分突破今日限速、属不可控尾差,不计入透支)。
	if remainingCost > 0 {
		var balance float64
		// FOR UPDATE 锁 users 行:锁顺序 subs→users,与清扣/到期事务一致;稍后 deductUsageBillingBalance 同锁。
		if err := tx.QueryRowContext(ctx, `SELECT balance FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, userID).Scan(&balance); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, service.ErrUserNotFound
			}
			return nil, err
		}
		if recharge := balance - sumRemaining; recharge > 0 {
			absorb := recharge
			if absorb > remainingCost {
				absorb = remainingCost
			}
			remainingCost -= absorb // 由充值余额承担,不归属任何订阅卡
		}
		for i := range states {
			if remainingCost <= 0 {
				break
			}
			room := states[i].s.remaining - states[i].total
			a := room
			if a > remainingCost {
				a = remainingCost
			}
			if a <= 0 {
				continue
			}
			states[i].total += a
			remainingCost -= a
		}
	}

	// 落库:每卡一次 UPDATE。daily_spent_usd 记当天总分摊(惰性按日历天归零、不跨天结转);
	// 透支 delta 仅按 pass1(透支额度内)计、且仅开透支时累加,达上限即关闭透支;
	// 若本次把本卡剩余扣到 ~0,同一条 UPDATE 即时标记 expired(用完即失效)。行已 FOR UPDATE 锁定。
	for i := range states {
		st := &states[i]
		if st.total <= 0 {
			continue
		}
		n := st.sub.CalendarDayAt(now)
		newDailySpent := st.sub.DailySpentAt(now) + st.total
		overdraftDelta := 0
		if st.s.maxOverdraftDays != nil && st.sub.CanEnableOverdraft() {
			overdraftDelta = st.sub.OverdraftDaysDeltaAt(now, st.pass1)
		}
		closeOverdraft := st.s.maxOverdraftDays != nil &&
			st.s.totalOverdraftCount+overdraftDelta >= service.MaxSubscriptionOverdraftUses
		var nowExpired bool
		var groupID int64
		if err := tx.QueryRowContext(ctx, `
			UPDATE user_subscriptions
			SET consumed_usd = consumed_usd + $1,
				daily_spent_usd = $6,
				daily_spent_day = $7,
				total_overdraft_count = LEAST($8, total_overdraft_count + $4),
				max_overdraft_days = CASE WHEN $5 THEN NULL ELSE max_overdraft_days END,
				status = CASE
					WHEN (granted_total_usd - consumed_usd - clawed_usd - $1) <= $3
					THEN 'expired' ELSE status END,
				updated_at = NOW()
			WHERE id = $2
			RETURNING status = 'expired', group_id
		`, st.total, st.s.id, subscriptionDepletionEpsilon, overdraftDelta, closeOverdraft,
			newDailySpent, n, service.MaxSubscriptionOverdraftUses).Scan(&nowExpired, &groupID); err != nil {
			return nil, err
		}
		if nowExpired {
			depletedGroupIDs = append(depletedGroupIDs, groupID)
		}
	}
	return depletedGroupIDs, nil
}

func deductUsageBillingBalance(ctx context.Context, tx *sql.Tx, userID int64, amount float64) (float64, error) {
	var newBalance float64
	err := tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = balance - $1,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING balance
	`, amount, userID).Scan(&newBalance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, service.ErrUserNotFound
	}
	if err != nil {
		return 0, err
	}
	return newBalance, nil
}

func incrementUsageBillingAPIKeyQuota(ctx context.Context, tx *sql.Tx, apiKeyID int64, amount float64) (bool, error) {
	var exhausted bool
	err := tx.QueryRowContext(ctx, `
		UPDATE api_keys
		SET quota_used = quota_used + $1,
			status = CASE
				WHEN quota > 0
					AND status = $3
					AND quota_used < quota
					AND quota_used + $1 >= quota
				THEN $4
				ELSE status
			END,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING quota > 0 AND quota_used >= quota AND quota_used - $1 < quota
	`, amount, apiKeyID, service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted).Scan(&exhausted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, service.ErrAPIKeyNotFound
	}
	if err != nil {
		return false, err
	}
	return exhausted, nil
}

func incrementUsageBillingAPIKeyRateLimit(ctx context.Context, tx *sql.Tx, apiKeyID int64, cost float64) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE api_keys SET
			usage_5h = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start + INTERVAL '5 hours' <= NOW() THEN $1 ELSE usage_5h + $1 END,
			usage_1d = CASE WHEN window_1d_start IS NOT NULL AND window_1d_start + INTERVAL '24 hours' <= NOW() THEN $1 ELSE usage_1d + $1 END,
			usage_7d = CASE WHEN window_7d_start IS NOT NULL AND window_7d_start + INTERVAL '7 days' <= NOW() THEN $1 ELSE usage_7d + $1 END,
			window_5h_start = CASE WHEN window_5h_start IS NULL OR window_5h_start + INTERVAL '5 hours' <= NOW() THEN NOW() ELSE window_5h_start END,
			window_1d_start = CASE WHEN window_1d_start IS NULL OR window_1d_start + INTERVAL '24 hours' <= NOW() THEN date_trunc('day', NOW()) ELSE window_1d_start END,
			window_7d_start = CASE WHEN window_7d_start IS NULL OR window_7d_start + INTERVAL '7 days' <= NOW() THEN date_trunc('day', NOW()) ELSE window_7d_start END,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
	`, cost, apiKeyID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAPIKeyNotFound
	}
	return nil
}

func incrementUsageBillingAccountQuota(ctx context.Context, tx *sql.Tx, accountID int64, amount float64) (*service.AccountQuotaState, error) {
	rows, err := tx.QueryContext(ctx,
		`UPDATE accounts SET extra = (
			COALESCE(extra, '{}'::jsonb)
			|| jsonb_build_object('quota_used', COALESCE((extra->>'quota_used')::numeric, 0) + $1)
			|| CASE WHEN COALESCE((extra->>'quota_daily_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_daily_used',
					CASE WHEN `+dailyExpiredExpr+`
					THEN $1
					ELSE COALESCE((extra->>'quota_daily_used')::numeric, 0) + $1 END,
					'quota_daily_start',
					CASE WHEN `+dailyExpiredExpr+`
					THEN `+nowUTC+`
					ELSE COALESCE(extra->>'quota_daily_start', `+nowUTC+`) END
				)
				|| CASE WHEN `+dailyExpiredExpr+` AND `+nextDailyResetAtExpr+` IS NOT NULL
				   THEN jsonb_build_object('quota_daily_reset_at', `+nextDailyResetAtExpr+`)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
			|| CASE WHEN COALESCE((extra->>'quota_weekly_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_weekly_used',
					CASE WHEN `+weeklyExpiredExpr+`
					THEN $1
					ELSE COALESCE((extra->>'quota_weekly_used')::numeric, 0) + $1 END,
					'quota_weekly_start',
					CASE WHEN `+weeklyExpiredExpr+`
					THEN `+nowUTC+`
					ELSE COALESCE(extra->>'quota_weekly_start', `+nowUTC+`) END
				)
				|| CASE WHEN `+weeklyExpiredExpr+` AND `+nextWeeklyResetAtExpr+` IS NOT NULL
				   THEN jsonb_build_object('quota_weekly_reset_at', `+nextWeeklyResetAtExpr+`)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
		), updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING
			COALESCE((extra->>'quota_used')::numeric, 0),
			COALESCE((extra->>'quota_limit')::numeric, 0),
			COALESCE((extra->>'quota_daily_used')::numeric, 0),
			COALESCE((extra->>'quota_daily_limit')::numeric, 0),
			COALESCE((extra->>'quota_weekly_used')::numeric, 0),
			COALESCE((extra->>'quota_weekly_limit')::numeric, 0)`,
		amount, accountID)
	if err != nil {
		return nil, err
	}

	var state service.AccountQuotaState
	if rows.Next() {
		if err := rows.Scan(
			&state.TotalUsed, &state.TotalLimit,
			&state.DailyUsed, &state.DailyLimit,
			&state.WeeklyUsed, &state.WeeklyLimit,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
	} else {
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
		return nil, service.ErrAccountNotFound
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	// 必须在执行下一条 SQL 前显式关闭 rows：pq 驱动在同一连接上
	// 不允许前一条查询的结果集未耗尽时启动新查询，否则会返回
	// "unexpected Parse response" 错误。
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// 任意维度额度在本次递增中从"未超"跨越到"已超"时，必须刷新调度快照，
	// 否则 Redis 中缓存的 Account 仍显示旧的 used 值，后续请求会继续选中本账号，
	// 最终观察到 daily_used / weekly_used 大幅超过配置的 limit。
	// 对于日/周额度，即使本次触发了周期重置（pre=0、post=amount），
	// 判定式 (post-amount) < limit 同样成立，逻辑与总额度保持一致。
	crossedTotal := state.TotalLimit > 0 && state.TotalUsed >= state.TotalLimit && (state.TotalUsed-amount) < state.TotalLimit
	crossedDaily := state.DailyLimit > 0 && state.DailyUsed >= state.DailyLimit && (state.DailyUsed-amount) < state.DailyLimit
	crossedWeekly := state.WeeklyLimit > 0 && state.WeeklyUsed >= state.WeeklyLimit && (state.WeeklyUsed-amount) < state.WeeklyLimit
	if crossedTotal || crossedDaily || crossedWeekly {
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
			logger.LegacyPrintf("repository.usage_billing", "[SchedulerOutbox] enqueue quota exceeded failed: account=%d err=%v", accountID, err)
			return nil, err
		}
	}
	return &state, nil
}
