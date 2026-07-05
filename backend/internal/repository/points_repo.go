package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// pointsRepository 邀请返利积分制（issue #11）仓储实现：原始 SQL + 事务原子 + partial-unique 幂等。
// 计价纯函数在 service 包（ComputeEarnPoints / ComputeClawbackPoints / PointsToBalance / ...）。
type pointsRepository struct {
	client *dbent.Client
}

// NewPointsRepository 构造 points 仓储。第二参数（*sql.DB）保持与其他 repo 一致，未使用。
func NewPointsRepository(client *dbent.Client, _ *sql.DB) service.PointsRepository {
	return &pointsRepository{client: client}
}

func (r *pointsRepository) withTx(ctx context.Context, fn func(txCtx context.Context, txClient *dbent.Client) error) error {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return fn(ctx, tx.Client())
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin points transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if err := fn(txCtx, tx.Client()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit points transaction: %w", err)
	}
	return nil
}

// --- 账户 ---

func (r *pointsRepository) EnsureAccount(ctx context.Context, userID int64) (*service.PointsAccount, error) {
	if userID <= 0 {
		return nil, service.ErrUserNotFound
	}
	client := clientFromContext(ctx, r.client)
	if _, err := client.ExecContext(ctx, `
INSERT INTO user_points_accounts (user_id, created_at, updated_at)
VALUES ($1, NOW(), NOW())
ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return nil, fmt.Errorf("ensure points account: %w", err)
	}
	return pointsGetAccount(ctx, client, userID)
}

func (r *pointsRepository) GetAccount(ctx context.Context, userID int64) (*service.PointsAccount, error) {
	client := clientFromContext(ctx, r.client)
	return pointsGetAccount(ctx, client, userID)
}

func pointsGetAccount(ctx context.Context, client affiliateQueryExecer, userID int64) (*service.PointsAccount, error) {
	rows, err := client.QueryContext(ctx, `
SELECT user_id, available, frozen, lifetime_earned, created_at, updated_at
FROM user_points_accounts WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("get points account: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		// 无账户视为全 0（未产生过积分）。
		return &service.PointsAccount{UserID: userID}, nil
	}
	var a service.PointsAccount
	if err := rows.Scan(&a.UserID, &a.Available, &a.Frozen, &a.LifetimeEarned, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	return &a, rows.Err()
}

// --- Earning / Clawback / Thaw ---

func (r *pointsRepository) EarnPoints(ctx context.Context, in service.EarnPointsInput) (bool, error) {
	if in.Points <= 0 || in.InviterID <= 0 {
		return false, nil
	}
	// 来源锚二选一：法币订单 or 兑换码（方案 C）。各自 partial-unique 幂等。
	// sourceCol 为受控常量列名（非用户输入），可安全 fmt.Sprintf 拼接。
	var sourceCol string
	var sourceVal int64
	switch {
	case in.SourceOrderID > 0:
		sourceCol, sourceVal = "source_order_id", in.SourceOrderID
	case in.SourceRedeemCodeID > 0:
		sourceCol, sourceVal = "source_redeem_code_id", in.SourceRedeemCodeID
	default:
		return false, nil
	}
	var applied bool
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := txClient.ExecContext(txCtx, `
INSERT INTO user_points_accounts (user_id, created_at, updated_at)
VALUES ($1, NOW(), NOW()) ON CONFLICT (user_id) DO NOTHING`, in.InviterID); err != nil {
			return fmt.Errorf("ensure inviter points account: %w", err)
		}
		// 幂等插入 earn 流水（partial-unique on (user_id, <sourceCol>) WHERE kind='earn'）。
		inserted, err := scanInt64(txCtx, txClient, fmt.Sprintf(`
WITH ins AS (
    INSERT INTO user_points_ledger (user_id, kind, points, peg_at, source_user_id, %s, frozen_until, frozen_remaining, created_at, updated_at)
    VALUES ($1, 'earn', $2, $3, $4, $5,
            CASE WHEN $6 > 0 THEN NOW() + make_interval(hours => $6) ELSE NULL END,
            CASE WHEN $6 > 0 THEN $2::bigint ELSE 0::bigint END,
            NOW(), NOW())
    ON CONFLICT DO NOTHING
    RETURNING 1
)
SELECT COUNT(*) FROM ins`, sourceCol),
			in.InviterID, in.Points, nullablePegArg(in.PegAt), in.SourceUserID, sourceVal, in.FreezeHours)
		if err != nil {
			return fmt.Errorf("insert earn ledger: %w", err)
		}
		if inserted == 0 {
			applied = false
			return nil
		}
		var updateSQL string
		if in.FreezeHours > 0 {
			updateSQL = `UPDATE user_points_accounts SET frozen = frozen + $1, lifetime_earned = lifetime_earned + $1, updated_at = NOW() WHERE user_id = $2 RETURNING available, frozen`
		} else {
			updateSQL = `UPDATE user_points_accounts SET available = available + $1, lifetime_earned = lifetime_earned + $1, updated_at = NOW() WHERE user_id = $2 RETURNING available, frozen`
		}
		avail, frozen, ok, err := scanTwoInt64(txCtx, txClient, updateSQL, in.Points, in.InviterID)
		if err != nil {
			return fmt.Errorf("bump points account: %w", err)
		}
		if !ok {
			return fmt.Errorf("points account missing after ensure")
		}
		if _, err := txClient.ExecContext(txCtx, fmt.Sprintf(`
UPDATE user_points_ledger SET available_after = $1, frozen_after = $2, updated_at = NOW()
WHERE user_id = $3 AND kind = 'earn' AND %s = $4`, sourceCol), avail, frozen, in.InviterID, sourceVal); err != nil {
			return fmt.Errorf("backfill earn ledger snapshot: %w", err)
		}
		applied = true
		return nil
	})
	return applied, err
}

func (r *pointsRepository) ClawbackByOrder(ctx context.Context, sourceOrderID int64, refundAmount, originalAmount float64) (int64, error) {
	if sourceOrderID <= 0 {
		return 0, nil
	}
	var clawed int64
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		// 锁定来源 earn 行：取 earned + 该行仍冻结待解冻额（frozen_remaining）。
		// FOR UPDATE 与 ThawDuePoints 的 to_thaw 锁序一致（earn 行 → 账户），杜绝竞态/死锁。
		inviterID, earned, frozenRem, ok, err := scanThreeInt64(txCtx, txClient, `
SELECT user_id, points, frozen_remaining FROM user_points_ledger
WHERE source_order_id = $1 AND kind = 'earn' LIMIT 1 FOR UPDATE`, sourceOrderID)
		if err != nil {
			return fmt.Errorf("lookup earn ledger for clawback: %w", err)
		}
		if !ok || earned <= 0 {
			return nil // 未返过积分 / 无邀请人 → 无需 clawback
		}
		claw := service.ComputeClawbackPoints(earned, refundAmount, originalAmount)
		if claw <= 0 {
			return nil
		}
		// 幂等插入 clawback 流水（partial-unique on (user_id, source_order_id) WHERE kind='clawback'）。
		inserted, err := scanInt64(txCtx, txClient, `
WITH ins AS (
    INSERT INTO user_points_ledger (user_id, kind, points, source_order_id, created_at, updated_at)
    VALUES ($1, 'clawback', $2, $3, NOW(), NOW())
    ON CONFLICT DO NOTHING
    RETURNING 1
)
SELECT COUNT(*) FROM ins`, inviterID, -claw, sourceOrderID)
		if err != nil {
			return fmt.Errorf("insert clawback ledger: %w", err)
		}
		if inserted == 0 {
			return nil // 已撤过（一单一撤）
		}
		avail, frozen, ok, err := scanTwoInt64(txCtx, txClient, `
SELECT available, frozen FROM user_points_accounts WHERE user_id = $1 FOR UPDATE`, inviterID)
		if err != nil {
			return fmt.Errorf("lock points account for clawback: %w", err)
		}
		if !ok {
			// earn 必然建过账户；保险起见建一个再扣。
			if _, err := txClient.ExecContext(txCtx, `
INSERT INTO user_points_accounts (user_id, created_at, updated_at) VALUES ($1, NOW(), NOW())
ON CONFLICT (user_id) DO NOTHING`, inviterID); err != nil {
				return err
			}
			avail, frozen = 0, 0
		}
		// 优先扣本来源 earn 行仍冻结的待解冻额（frozen_remaining），不足部分再扣 available（可转负）。
		// 关键：以「来源行的 frozen_remaining」而非「账户聚合 frozen」为冻结消费上限——多条冻结行
		// 共存时，撤回某一笔只能吃它自己的冻结额，绝不误伤其他行（否则 thaw 会串味）。
		fromFrozen := claw
		if fromFrozen > frozenRem {
			fromFrozen = frozenRem
		}
		if fromFrozen > frozen { // 防御：不变式下 frozenRem ≤ 账户 frozen
			fromFrozen = frozen
		}
		if fromFrozen < 0 {
			fromFrozen = 0
		}
		newFrozen := frozen - fromFrozen
		newAvail := avail - (claw - fromFrozen)
		if _, err := txClient.ExecContext(txCtx, `
UPDATE user_points_accounts SET available = $1, frozen = $2, updated_at = NOW() WHERE user_id = $3`,
			newAvail, newFrozen, inviterID); err != nil {
			return fmt.Errorf("apply clawback to account: %w", err)
		}
		// 同步递减来源 earn 行的待解冻额，使其将来 thaw 不再复活被撤回的冻结积分。
		if fromFrozen > 0 {
			if _, err := txClient.ExecContext(txCtx, `
UPDATE user_points_ledger SET frozen_remaining = frozen_remaining - $1, updated_at = NOW()
WHERE source_order_id = $2 AND kind = 'earn'`, fromFrozen, sourceOrderID); err != nil {
				return fmt.Errorf("reduce earn frozen_remaining on clawback: %w", err)
			}
		}
		if _, err := txClient.ExecContext(txCtx, `
UPDATE user_points_ledger SET available_after = $1, frozen_after = $2, updated_at = NOW()
WHERE user_id = $3 AND kind = 'clawback' AND source_order_id = $4`,
			newAvail, newFrozen, inviterID, sourceOrderID); err != nil {
			return fmt.Errorf("backfill clawback ledger snapshot: %w", err)
		}
		clawed = claw
		return nil
	})
	return clawed, err
}

func (r *pointsRepository) ThawDuePoints(ctx context.Context, userID int64) (int64, error) {
	var thawed int64
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		// 只解冻「仍冻结待解冻」额（frozen_remaining），不是原始 points——被 clawback 消费掉的冻结
		// 不得复活。先在 to_thaw 锁定并捕获旧 frozen_remaining，再清零，最后对旧值求和。
		sum, err := scanInt64(txCtx, txClient, `
WITH to_thaw AS (
    SELECT id, frozen_remaining FROM user_points_ledger
    WHERE user_id = $1 AND kind = 'earn' AND frozen_until IS NOT NULL AND frozen_until <= NOW()
    FOR UPDATE
), upd AS (
    UPDATE user_points_ledger l
       SET frozen_until = NULL, frozen_remaining = 0, updated_at = NOW()
      FROM to_thaw t WHERE l.id = t.id
)
SELECT COALESCE(SUM(frozen_remaining), 0)::bigint FROM to_thaw`, userID)
		if err != nil {
			return fmt.Errorf("mature frozen points: %w", err)
		}
		if sum <= 0 {
			return nil
		}
		if _, err := txClient.ExecContext(txCtx, `
UPDATE user_points_accounts SET available = available + $1, frozen = frozen - $1, updated_at = NOW()
WHERE user_id = $2`, sum, userID); err != nil {
			return fmt.Errorf("apply thaw to account: %w", err)
		}
		// 解冻审计行。
		avail, frozen, _, err := scanTwoInt64(txCtx, txClient, `
SELECT available, frozen FROM user_points_accounts WHERE user_id = $1`, userID)
		if err != nil {
			return err
		}
		if _, err := txClient.ExecContext(txCtx, `
INSERT INTO user_points_ledger (user_id, kind, points, available_after, frozen_after, created_at, updated_at)
VALUES ($1, 'thaw', $2, $3, $4, NOW(), NOW())`, userID, sum, avail, frozen); err != nil {
			return fmt.Errorf("insert thaw ledger: %w", err)
		}
		thawed = sum
		return nil
	})
	return thawed, err
}

// --- Spending ---

func (r *pointsRepository) RedeemToBalance(ctx context.Context, userID, points int64, balanceDelta, pegAt float64) (float64, error) {
	if points <= 0 {
		return 0, service.ErrPointsAmountInvalid
	}
	var newBalance float64
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		avail, frozen, ok, err := scanTwoInt64(txCtx, txClient, `
UPDATE user_points_accounts SET available = available - $1, updated_at = NOW()
WHERE user_id = $2 AND available >= $1 RETURNING available, frozen`, points, userID)
		if err != nil {
			return fmt.Errorf("deduct points for balance: %w", err)
		}
		if !ok {
			return service.ErrPointsInsufficient
		}
		bal, found, err := scanFloat64(txCtx, txClient, `
UPDATE users SET balance = balance + $1, updated_at = NOW() WHERE id = $2 RETURNING balance::double precision`,
			balanceDelta, userID)
		if err != nil {
			return fmt.Errorf("credit balance from points: %w", err)
		}
		if !found {
			return service.ErrUserNotFound
		}
		newBalance = bal
		if _, err := txClient.ExecContext(txCtx, `
INSERT INTO user_points_ledger (user_id, kind, points, peg_at, available_after, frozen_after, created_at, updated_at)
VALUES ($1, 'to_balance', $2, $3, $4, $5, NOW(), NOW())`, userID, -points, nullablePegArg(pegAt), avail, frozen); err != nil {
			return fmt.Errorf("insert to_balance ledger: %w", err)
		}
		return nil
	})
	return newBalance, err
}

func (r *pointsRepository) DeductForPlan(ctx context.Context, userID, points int64, pegAt float64, note, idempotencyKey string) error {
	if points <= 0 {
		return service.ErrPointsAmountInvalid
	}
	// 在 service 事务内调用：clientFromContext 取到外层 tx，与发卡同事务。
	client := clientFromContext(ctx, r.client)
	key := strings.TrimSpace(idempotencyKey)
	if key != "" {
		// 先占用幂等键，再扣账户。这样重复请求即使当前余额不足，也会优先返回 duplicate；
		// 若后续扣款/发卡失败，外层事务回滚，占位流水不会残留。
		if _, err := client.ExecContext(ctx, `
INSERT INTO user_points_ledger (user_id, kind, points, peg_at, note, idempotency_key, created_at, updated_at)
VALUES ($1, 'to_plan', $2, $3, $4, $5, NOW(), NOW())`,
			userID, -points, nullablePegArg(pegAt), note, key); err != nil {
			if isUniqueConstraintViolation(err) {
				return service.ErrPointsPlanDuplicate
			}
			return fmt.Errorf("insert to_plan ledger: %w", err)
		}
	}
	avail, frozen, ok, err := scanTwoInt64(ctx, client, `
UPDATE user_points_accounts SET available = available - $1, updated_at = NOW()
WHERE user_id = $2 AND available >= $1 RETURNING available, frozen`, points, userID)
	if err != nil {
		return fmt.Errorf("deduct points for plan: %w", err)
	}
	if !ok {
		return service.ErrPointsInsufficient
	}
	if key != "" {
		if _, err := client.ExecContext(ctx, `
UPDATE user_points_ledger SET available_after = $1, frozen_after = $2, updated_at = NOW()
WHERE user_id = $3 AND kind = 'to_plan' AND idempotency_key = $4`,
			avail, frozen, userID, key); err != nil {
			return fmt.Errorf("backfill to_plan ledger snapshot: %w", err)
		}
		return nil
	}
	// to_plan 台账行带幂等键：重复请求（同 user+key）命中 partial-unique → 整事务回滚、不二次扣分。
	if _, err := client.ExecContext(ctx, `
INSERT INTO user_points_ledger (user_id, kind, points, peg_at, available_after, frozen_after, note, idempotency_key, created_at, updated_at)
VALUES ($1, 'to_plan', $2, $3, $4, $5, $6, $7, NOW(), NOW())`,
		userID, -points, nullablePegArg(pegAt), avail, frozen, note, nullableStringArg(idempotencyKey)); err != nil {
		if isUniqueConstraintViolation(err) {
			return service.ErrPointsPlanDuplicate
		}
		return fmt.Errorf("insert to_plan ledger: %w", err)
	}
	return nil
}

// --- 提现 ---

func (r *pointsRepository) CreateWithdrawal(ctx context.Context, in service.CreateWithdrawalInput) (*service.PointsWithdrawal, error) {
	if in.Points <= 0 {
		return nil, service.ErrPointsAmountInvalid
	}
	var out *service.PointsWithdrawal
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		avail, frozen, ok, err := scanTwoInt64(txCtx, txClient, `
UPDATE user_points_accounts SET available = available - $1, updated_at = NOW()
WHERE user_id = $2 AND available >= $1 RETURNING available, frozen`, in.Points, in.UserID)
		if err != nil {
			return fmt.Errorf("hold points for withdrawal: %w", err)
		}
		if !ok {
			return service.ErrPointsInsufficient
		}
		id, err := scanInt64(txCtx, txClient, `
INSERT INTO user_points_withdrawals
    (user_id, points, gross_amount, fee_amount, net_amount, peg_at, fee_percent_at,
     payout_method, payout_alipay_account, payout_alipay_name, payout_usdt_chain, payout_usdt_address, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'pending', NOW(), NOW())
RETURNING id`,
			in.UserID, in.Points, in.GrossAmount, in.FeeAmount, in.NetAmount,
			nullablePegArg(in.PegAt), nullablePegArg(in.FeePercentAt),
			in.PayoutMethod, nullableStringArg(in.PayoutAlipayAccount), nullableStringArg(in.PayoutAlipayName), nullableStringArg(in.PayoutUSDTChain), nullableStringArg(in.PayoutUSDTAddress))
		if err != nil {
			if isUniqueConstraintViolation(err) {
				return service.ErrPointsWithdrawPending
			}
			return fmt.Errorf("insert withdrawal: %w", err)
		}
		if _, err := txClient.ExecContext(txCtx, `
INSERT INTO user_points_ledger (user_id, kind, points, peg_at, withdrawal_id, available_after, frozen_after, created_at, updated_at)
VALUES ($1, 'withdraw_hold', $2, $3, $4, $5, $6, NOW(), NOW())`,
			in.UserID, -in.Points, nullablePegArg(in.PegAt), id, avail, frozen); err != nil {
			return fmt.Errorf("insert withdraw_hold ledger: %w", err)
		}
		w, err := pointsGetWithdrawal(txCtx, txClient, id)
		if err != nil {
			return err
		}
		out = w
		return nil
	})
	return out, err
}

func (r *pointsRepository) GetWithdrawal(ctx context.Context, id int64) (*service.PointsWithdrawal, error) {
	client := clientFromContext(ctx, r.client)
	w, err := pointsGetWithdrawal(ctx, client, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, service.ErrPointsWithdrawalNotFound
	}
	return w, nil
}

func (r *pointsRepository) ReviewWithdrawal(ctx context.Context, id, adminID int64, approve bool, note, payoutProof string) (*service.PointsWithdrawal, error) {
	var out *service.PointsWithdrawal
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		userID, points, ok, err := scanTwoInt64(txCtx, txClient, `
SELECT user_id, points FROM user_points_withdrawals WHERE id = $1 AND status = 'pending' FOR UPDATE`, id)
		if err != nil {
			return fmt.Errorf("lock withdrawal: %w", err)
		}
		if !ok {
			return service.ErrPointsWithdrawNotPending
		}
		status := service.PointsWithdrawalStatusPaid
		if !approve {
			status = service.PointsWithdrawalStatusRejected
		}
		if _, err := txClient.ExecContext(txCtx, `
UPDATE user_points_withdrawals
SET status = $1, review_note = $2, reviewed_by = $3, payout_proof = $4, reviewed_at = NOW(), updated_at = NOW()
WHERE id = $5`, status, nullableStringArg(note), adminID, nullableStringArg(payoutProof), id); err != nil {
			return fmt.Errorf("update withdrawal status: %w", err)
		}
		if approve {
			avail, frozen, _, err := scanTwoInt64(txCtx, txClient, `
SELECT available, frozen FROM user_points_accounts WHERE user_id = $1`, userID)
			if err != nil {
				return err
			}
			if _, err := txClient.ExecContext(txCtx, `
INSERT INTO user_points_ledger (user_id, kind, points, withdrawal_id, available_after, frozen_after, created_at, updated_at)
VALUES ($1, 'withdraw_paid', 0, $2, $3, $4, NOW(), NOW())`, userID, id, avail, frozen); err != nil {
				return fmt.Errorf("insert withdraw_paid ledger: %w", err)
			}
		} else {
			avail, frozen, ok2, err := scanTwoInt64(txCtx, txClient, `
UPDATE user_points_accounts SET available = available + $1, updated_at = NOW()
WHERE user_id = $2 RETURNING available, frozen`, points, userID)
			if err != nil {
				return fmt.Errorf("refund points on reject: %w", err)
			}
			if !ok2 {
				return service.ErrUserNotFound
			}
			if _, err := txClient.ExecContext(txCtx, `
INSERT INTO user_points_ledger (user_id, kind, points, withdrawal_id, available_after, frozen_after, created_at, updated_at)
VALUES ($1, 'withdraw_refund', $2, $3, $4, $5, NOW(), NOW())`, userID, points, id, avail, frozen); err != nil {
				return fmt.Errorf("insert withdraw_refund ledger: %w", err)
			}
		}
		w, err := pointsGetWithdrawal(txCtx, txClient, id)
		if err != nil {
			return err
		}
		out = w
		return nil
	})
	return out, err
}

// --- 列表查询 ---

const pointsWithdrawalSelect = `
SELECT w.id, w.user_id, COALESCE(u.email, ''), COALESCE(u.username, ''),
       w.points, w.gross_amount::double precision, w.fee_amount::double precision, w.net_amount::double precision,
       w.peg_at, w.fee_percent_at, w.payout_method, w.payout_alipay_account, w.payout_alipay_name, w.payout_usdt_chain, w.payout_usdt_address,
       w.status, w.review_note, w.reviewed_by, w.payout_proof,
       w.created_at, w.updated_at, w.reviewed_at
FROM user_points_withdrawals w
LEFT JOIN users u ON u.id = w.user_id`

func pointsGetWithdrawal(ctx context.Context, client affiliateQueryExecer, id int64) (*service.PointsWithdrawal, error) {
	rows, err := client.QueryContext(ctx, pointsWithdrawalSelect+" WHERE w.id = $1", id)
	if err != nil {
		return nil, fmt.Errorf("get withdrawal: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanWithdrawalRow(rows)
}

func scanWithdrawalRow(rows *sql.Rows) (*service.PointsWithdrawal, error) {
	var w service.PointsWithdrawal
	var pegAt, feePct sql.NullFloat64
	var alipayAccount, alipayName, usdtChain, usdt, reviewNote, payoutProof sql.NullString
	var reviewedBy sql.NullInt64
	var reviewedAt sql.NullTime
	if err := rows.Scan(&w.ID, &w.UserID, &w.UserEmail, &w.Username,
		&w.Points, &w.GrossAmount, &w.FeeAmount, &w.NetAmount,
		&pegAt, &feePct, &w.PayoutMethod, &alipayAccount, &alipayName, &usdtChain, &usdt,
		&w.Status, &reviewNote, &reviewedBy, &payoutProof,
		&w.CreatedAt, &w.UpdatedAt, &reviewedAt); err != nil {
		return nil, err
	}
	if pegAt.Valid {
		w.PegAt = &pegAt.Float64
	}
	if feePct.Valid {
		w.FeePercentAt = &feePct.Float64
	}
	w.PayoutAlipayAccount = alipayAccount.String
	w.PayoutAlipayName = alipayName.String
	w.PayoutUSDTChain = usdtChain.String
	w.PayoutUSDTAddress = usdt.String
	w.ReviewNote = reviewNote.String
	w.PayoutProof = payoutProof.String
	if reviewedBy.Valid {
		w.ReviewedBy = &reviewedBy.Int64
	}
	if reviewedAt.Valid {
		w.ReviewedAt = &reviewedAt.Time
	}
	return &w, nil
}

func (r *pointsRepository) ListUserWithdrawals(ctx context.Context, userID int64, limit int) ([]service.PointsWithdrawal, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, pointsWithdrawalSelect+`
WHERE w.user_id = $1 ORDER BY w.created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list user withdrawals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.PointsWithdrawal, 0)
	for rows.Next() {
		w, err := scanWithdrawalRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func (r *pointsRepository) ListWithdrawals(ctx context.Context, filter service.PointsWithdrawalFilter) ([]service.PointsWithdrawal, int64, error) {
	client := clientFromContext(ctx, r.client)
	clauses := "WHERE 1=1"
	args := make([]any, 0)
	if filter.Status != "" {
		args = append(args, filter.Status)
		clauses += fmt.Sprintf(" AND w.status = $%d", len(args))
	}
	if filter.Search != "" {
		args = append(args, "%"+filter.Search+"%")
		clauses += fmt.Sprintf(" AND (u.email ILIKE $%d OR u.username ILIKE $%d)", len(args), len(args))
	}
	total, err := scanInt64(ctx, client, `SELECT COUNT(*) FROM user_points_withdrawals w LEFT JOIN users u ON u.id = w.user_id `+clauses, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("count withdrawals: %w", err)
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize)
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := client.QueryContext(ctx, pointsWithdrawalSelect+" "+clauses+
		fmt.Sprintf(" ORDER BY w.created_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list withdrawals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.PointsWithdrawal, 0)
	for rows.Next() {
		w, err := scanWithdrawalRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *w)
	}
	return out, total, rows.Err()
}

const pointsLedgerSelect = `
SELECT l.id, l.user_id, l.kind, l.points, l.peg_at, l.source_user_id, l.source_order_id, l.source_redeem_code_id,
       l.withdrawal_id, l.frozen_until, l.available_after, l.frozen_after, COALESCE(l.note, ''), l.created_at
FROM user_points_ledger l`

func scanLedgerRows(rows *sql.Rows) ([]service.PointsLedgerEntry, error) {
	out := make([]service.PointsLedgerEntry, 0)
	for rows.Next() {
		var e service.PointsLedgerEntry
		var pegAt sql.NullFloat64
		var srcUser, srcOrder, srcRedeem, withdrawalID, availAfter, frozenAfter sql.NullInt64
		var frozenUntil sql.NullTime
		if err := rows.Scan(&e.ID, &e.UserID, &e.Kind, &e.Points, &pegAt, &srcUser, &srcOrder, &srcRedeem,
			&withdrawalID, &frozenUntil, &availAfter, &frozenAfter, &e.Note, &e.CreatedAt); err != nil {
			return nil, err
		}
		if pegAt.Valid {
			e.PegAt = &pegAt.Float64
		}
		if srcUser.Valid {
			e.SourceUserID = &srcUser.Int64
		}
		if srcOrder.Valid {
			e.SourceOrderID = &srcOrder.Int64
		}
		if srcRedeem.Valid {
			e.SourceRedeemCodeID = &srcRedeem.Int64
		}
		if withdrawalID.Valid {
			e.WithdrawalID = &withdrawalID.Int64
		}
		if frozenUntil.Valid {
			e.FrozenUntil = &frozenUntil.Time
		}
		if availAfter.Valid {
			e.AvailableAfter = &availAfter.Int64
		}
		if frozenAfter.Valid {
			e.FrozenAfter = &frozenAfter.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *pointsRepository) ListUserLedger(ctx context.Context, userID int64, page, pageSize int) ([]service.PointsLedgerEntry, int64, error) {
	client := clientFromContext(ctx, r.client)
	total, err := scanInt64(ctx, client, `SELECT COUNT(*) FROM user_points_ledger WHERE user_id = $1`, userID)
	if err != nil {
		return nil, 0, fmt.Errorf("count user ledger: %w", err)
	}
	page, pageSize = normalizePage(page, pageSize)
	rows, err := client.QueryContext(ctx, pointsLedgerSelect+`
WHERE l.user_id = $1 ORDER BY l.created_at DESC, l.id DESC LIMIT $2 OFFSET $3`,
		userID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("list user ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries, err := scanLedgerRows(rows)
	return entries, total, err
}

func (r *pointsRepository) ListLedger(ctx context.Context, filter service.PointsLedgerFilter) ([]service.PointsLedgerEntry, int64, error) {
	client := clientFromContext(ctx, r.client)
	clauses := "WHERE 1=1"
	args := make([]any, 0)
	if filter.Kind != "" {
		args = append(args, filter.Kind)
		clauses += fmt.Sprintf(" AND l.kind = $%d", len(args))
	}
	if filter.Search != "" {
		args = append(args, "%"+filter.Search+"%")
		clauses += fmt.Sprintf(" AND (u.email ILIKE $%d OR u.username ILIKE $%d)", len(args), len(args))
	}
	total, err := scanInt64(ctx, client, `SELECT COUNT(*) FROM user_points_ledger l LEFT JOIN users u ON u.id = l.user_id `+clauses, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("count ledger: %w", err)
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize)
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := client.QueryContext(ctx, pointsLedgerSelect+`
LEFT JOIN users u ON u.id = l.user_id `+clauses+
		fmt.Sprintf(" ORDER BY l.created_at DESC, l.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries, err := scanLedgerRows(rows)
	return entries, total, err
}

// --- 本包内小助手 ---

// scanTwoInt64 执行返回两整数列的单行查询；found=false 表示无行（如 RETURNING 命中 0 行）。
func scanTwoInt64(ctx context.Context, client affiliateQueryExecer, query string, args ...any) (a int64, b int64, found bool, err error) {
	rows, qerr := client.QueryContext(ctx, query, args...)
	if qerr != nil {
		return 0, 0, false, qerr
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0, 0, false, rows.Err()
	}
	if serr := rows.Scan(&a, &b); serr != nil {
		return 0, 0, false, serr
	}
	for rows.Next() {
	}
	return a, b, true, rows.Err()
}

// scanThreeInt64 执行返回三整数列的单行查询；found=false 表示无行。
func scanThreeInt64(ctx context.Context, client affiliateQueryExecer, query string, args ...any) (a int64, b int64, c int64, found bool, err error) {
	rows, qerr := client.QueryContext(ctx, query, args...)
	if qerr != nil {
		return 0, 0, 0, false, qerr
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0, 0, 0, false, rows.Err()
	}
	if serr := rows.Scan(&a, &b, &c); serr != nil {
		return 0, 0, 0, false, serr
	}
	for rows.Next() {
	}
	return a, b, c, true, rows.Err()
}

func nullablePegArg(v float64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func nullableStringArg(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}
	return page, pageSize
}
