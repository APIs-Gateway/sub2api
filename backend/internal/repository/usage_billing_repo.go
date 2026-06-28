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
	// 三窗口结算：按瀑布把官方成本结算到「用户唯一生效卡 + 钱包」（订阅覆盖 1:1 加三窗口 usage
	// → 钱包正余额 1:1 → 钱包负数 1:1）。订阅余额与钱包余额地位等价、均按官方刀 1:1 抵扣，倍率
	// 不参与扣费（见 docs/billing-perday-redesign.md §4）。结算不含透支（透支走独立手动接口）。
	// 锁序 user→card，整套副作用同 tx，dedup 覆盖整笔。OfficialCost = 官方价；legacy/测试只传
	// BalanceCost 时回退（按它 1:1 扣，无卡即纯钱包，等价旧 deductUsageBillingBalance）。
	officialCost := cmd.OfficialCost
	if officialCost <= 0 && cmd.BalanceCost > 0 {
		officialCost = cmd.BalanceCost
	}
	if officialCost > 0 {
		settleRes, err := settleSubscriptionWindow(ctx, tx, cmd.UserID, officialCost)
		if err != nil {
			return err
		}
		result.NewBalance = &settleRes.newBalance
		result.WalletDebit = &settleRes.walletDebit
		result.OverdraftApplied = settleRes.overdraftApplied
		result.SubscriptionID = settleRes.subscriptionID
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

// subSettleResult 是三窗口订阅结算的落库结果（供上层重建旧余额、失效缓存）。
type subSettleResult struct {
	newBalance       float64 // 结算后钱包余额
	walletDebit      float64 // 本次从钱包实扣的售价货币额（钱包正余额 + 钱包负数兜底；订阅覆盖 1:1 部分不计）
	expiredGroupID   *int64  // 本次把卡惰性标记为 expired 时其 group_id（供失效订阅缓存）；否则 nil
	overdraftApplied bool    // 三窗口结算不自动透支，恒 false（保留字段兼容上层；透支走独立手动接口）
	subscriptionID   *int64  // 本次结算所用的用户生效卡 ID（有生效卡即填）；供 usage_log 标 subscription 计费
}

// settleSubscriptionWindow 按三窗口瀑布把一笔请求的官方成本结算到「用户唯一生效卡 + 钱包」。
// 锁序固定 user→card（与购买/续费/转套餐一致，防死锁）。无生效卡 → 纯钱包计费（官方价 1:1）。
// 订阅覆盖（1:1，加三窗口 usage，只升） → 钱包正余额（1:1） → 钱包负数兜底（1:1），整套副作用在
// 本事务内原子完成；dedup（claimUsageBillingKey）与本函数同 tx，重放整笔跳过。**结算不含透支**
// （透支走独立手动接口，单独改 expires_at + 月度计数）。瀑布逻辑复用 service.SettleWindow（已穷尽单测）。
// 订阅余额与钱包余额地位等价、均按官方刀 1:1 抵扣，倍率不参与扣费（见 docs/billing-perday-redesign.md §4）。
//
// ★安全闸（资损）：未配置/未回填卡（三限额全 NULL）经 SubRemaining 返回 0、订阅不覆盖、回落钱包，
// 不会“免费 1:1 全覆盖”（见 service.SubWindow.SubRemaining）。
func settleSubscriptionWindow(ctx context.Context, tx *sql.Tx, userID int64, officialCost float64) (*subSettleResult, error) {
	// 1) 锁 user 行（balance）。月度透支计数本路径不改（透支走独立接口），但一并读出构造 WalletState。
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

	// 2) 锁该用户唯一生效卡（status='active'，expires_at 最晚兜底）；可能无卡。
	//    过期是惰性的（卡可能 status='active' 而 now≥expires_at），不在 SQL 过滤 expires_at，
	//    由引擎按 expires_at 置 JustExpired，回写时翻 status。
	var (
		cardID    int64
		dLimit    sql.NullFloat64
		wLimit    sql.NullFloat64
		mLimit    sql.NullFloat64
		dUsage    float64
		wUsage    float64
		mUsage    float64
		dWin      sql.NullTime
		wWin      sql.NullTime
		mWin      sql.NullTime
		expiresAt time.Time
		status    string
		hasCard   bool
	)
	// 不 SELECT group_id：自定义/转套餐卡 group_id 为 NULL，且结算本不需要 group（限额读卡级）；
	// 误扫进 int64 会因 NULL 报 "converting NULL to int64 is unsupported" 使无 group 卡计费整笔失败（资损）。
	err := tx.QueryRowContext(ctx, `
		SELECT id,
		       daily_limit_usd, weekly_limit_usd, monthly_limit_usd,
		       daily_usage_usd, weekly_usage_usd, monthly_usage_usd,
		       daily_window_start, weekly_window_start, monthly_window_start,
		       expires_at, status
		FROM user_subscriptions
		WHERE user_id = $1 AND status = 'active' AND deleted_at IS NULL
		ORDER BY expires_at DESC, id DESC
		LIMIT 1
		FOR UPDATE
	`, userID).Scan(&cardID, &dLimit, &wLimit, &mLimit,
		&dUsage, &wUsage, &mUsage, &dWin, &wWin, &mWin, &expiresAt, &status)
	switch {
	case err == nil:
		hasCard = true
	case errors.Is(err, sql.ErrNoRows):
		hasCard = false
	default:
		return nil, err
	}

	// LOCK-005：now 必须在【取锁之后】按当前时间取，避免阻塞在 FOR UPDATE 上跨午夜后用 stale 时间
	// 喂进窗口重置（误判自然日/周/月边界）。锁内取保证与所持行状态同处一个时刻。
	now := time.Now()

	wallet := service.WalletState{Balance: balance, MonthlyOverdraftCount: monthCount, MonthlyOverdraftMonth: monthStr}
	var card *service.SubWindow
	if hasCard {
		card = &service.SubWindow{
			DailyLimitUSD:      nullFloatZero(dLimit),
			WeeklyLimitUSD:     nullFloatZero(wLimit),
			MonthlyLimitUSD:    nullFloatZero(mLimit),
			DailyUsageUSD:      dUsage,
			WeeklyUsageUSD:     wUsage,
			MonthlyUsageUSD:    mUsage,
			DailyWindowStart:   nullTimePtr(dWin),
			WeeklyWindowStart:  nullTimePtr(wWin),
			MonthlyWindowStart: nullTimePtr(mWin),
			ExpiresAt:          expiresAt,
			Status:             status,
		}
	}
	service.SettleWindow(card, &wallet, officialCost, now)

	out := &subSettleResult{}
	// 计费识别 = 用户存在生效中的订阅卡。即使本次订阅覆盖为 0（撞窗口上限、全由钱包支付），
	// 仍是「有生效卡」请求，应标 subscription。加载时 active 但 now≥expires_at 的假 active
	// 会被 SettleWindow 置 JustExpired，不标 subscription。
	if hasCard && card != nil && !card.JustExpired {
		id := cardID
		out.subscriptionID = &id
	}

	// 3) 回写卡（仅有卡时）：三窗口 usage/window_start + 惰性过期。结算不改 expires_at / 限额。
	if hasCard && card != nil {
		var nowExpired bool
		var gid sql.NullInt64 // 自定义/转套餐卡 group_id 为 NULL → 用 NullInt64 防 scan 崩
		if err := tx.QueryRowContext(ctx, `
			UPDATE user_subscriptions
			SET daily_usage_usd = $1, weekly_usage_usd = $2, monthly_usage_usd = $3,
				daily_window_start = $4, weekly_window_start = $5, monthly_window_start = $6,
				status = CASE WHEN $7 THEN 'expired' ELSE status END,
				updated_at = NOW()
			WHERE id = $8
			RETURNING status = 'expired', group_id
		`, card.DailyUsageUSD, card.WeeklyUsageUSD, card.MonthlyUsageUSD,
			card.DailyWindowStart, card.WeeklyWindowStart, card.MonthlyWindowStart,
			card.JustExpired, cardID).Scan(&nowExpired, &gid); err != nil {
			return nil, err
		}
		// 本次刚由 active→expired（加载时为 active）→ 失效订阅缓存；无 group 自定义卡（gid 为 NULL）
		// 无 (user,group) 组缓存需失效，跳过即可（用户级缓存由上层 SubscriptionID 路径处理）。
		if nowExpired && gid.Valid {
			g := gid.Int64
			out.expiredGroupID = &g
		}
	}

	// 4) 回写 user：balance（结算不改月度透支计数，故只写 balance）。
	if err := tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = $1, updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING balance
	`, wallet.Balance, userID).Scan(&out.newBalance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrUserNotFound
		}
		return nil, err
	}
	out.walletDebit = balance - wallet.Balance
	return out, nil
}

// nullFloatZero 把可空 decimal 列转 float64（NULL→0；三窗口引擎中 0 = 该窗口不限）。
func nullFloatZero(v sql.NullFloat64) float64 {
	if v.Valid {
		return v.Float64
	}
	return 0
}

// nullTimePtr 把可空 timestamptz 列转 *time.Time（NULL→nil；窗口未激活）。
func nullTimePtr(v sql.NullTime) *time.Time {
	if v.Valid {
		t := v.Time
		return &t
	}
	return nil
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
