package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *affiliateRepository) ListCashbackSubscriptionMappings(ctx context.Context) ([]service.AffiliateCashbackSubscriptionMapping, error) {
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
WITH validity_options AS (
    SELECT DISTINCT rc.group_id, rc.validity_days
    FROM redeem_codes rc
    WHERE rc.type = 'subscription'
      AND rc.group_id IS NOT NULL
      AND rc.validity_days > 0
    UNION
    SELECT g.id, 30
    FROM groups g
    WHERE g.subscription_type = 'subscription'
      AND g.status = 'active'
),
mapping_rows AS (
    SELECT vo.group_id,
           vo.validity_days,
           m.cashback_base_amount::double precision,
           m.created_at,
           m.updated_at
    FROM validity_options vo
    LEFT JOIN affiliate_cashback_subscription_mappings m
      ON m.group_id = vo.group_id
     AND m.validity_days = vo.validity_days
)
SELECT mr.group_id,
       COALESCE(g.name, ''),
       COALESCE(g.description, ''),
       COALESCE(g.platform, ''),
       mr.validity_days,
       COALESCE(mr.cashback_base_amount, 0)::double precision,
       mr.created_at,
       mr.updated_at
FROM mapping_rows mr
JOIN groups g ON g.id = mr.group_id
WHERE g.subscription_type = 'subscription'
ORDER BY g.name ASC, mr.validity_days ASC`)
	if err != nil {
		return nil, fmt.Errorf("list affiliate cashback subscription mappings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []service.AffiliateCashbackSubscriptionMapping
	for rows.Next() {
		var item service.AffiliateCashbackSubscriptionMapping
		var createdAt sql.NullTime
		var updatedAt sql.NullTime
		if err := rows.Scan(&item.GroupID, &item.GroupName, &item.GroupDescription, &item.Platform, &item.ValidityDays, &item.CashbackBaseAmount, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			item.CreatedAt = &createdAt.Time
		}
		if updatedAt.Valid {
			item.UpdatedAt = &updatedAt.Time
		}
		item.DisplayName = fmt.Sprintf("%d 天 (%s)", item.ValidityDays, item.GroupName)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *affiliateRepository) ReplaceCashbackSubscriptionMappings(ctx context.Context, entries []service.AffiliateCashbackSubscriptionMapping) error {
	return r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := txClient.ExecContext(txCtx, `DELETE FROM affiliate_cashback_subscription_mappings`); err != nil {
			return fmt.Errorf("clear affiliate cashback subscription mappings: %w", err)
		}
		for _, entry := range entries {
			if _, err := txClient.ExecContext(txCtx, `
INSERT INTO affiliate_cashback_subscription_mappings (group_id, validity_days, cashback_base_amount, created_at, updated_at)
VALUES ($1, $2, $3, NOW(), NOW())`, entry.GroupID, entry.ValidityDays, entry.CashbackBaseAmount); err != nil {
				return fmt.Errorf("insert affiliate cashback subscription mapping: %w", err)
			}
		}
		return nil
	})
}

func (r *affiliateRepository) GetSubscriptionCashbackBaseAmount(ctx context.Context, groupID int64, validityDays int) (float64, bool, error) {
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
SELECT cashback_base_amount::double precision
FROM affiliate_cashback_subscription_mappings
WHERE group_id = $1 AND validity_days = $2
LIMIT 1`, groupID, validityDays)
	if err != nil {
		return 0, false, fmt.Errorf("get affiliate cashback subscription base amount: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0, false, rows.Err()
	}
	var amount float64
	if err := rows.Scan(&amount); err != nil {
		return 0, false, err
	}
	return amount, true, rows.Err()
}

func (r *affiliateRepository) ApplyRedeemCashback(ctx context.Context, input service.AffiliateRedeemCashbackInput) (bool, error) {
	var applied bool
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		// 1) 幂等插入返现流水行；ON CONFLICT DO NOTHING 保证重复返现（同 code/user/source）不重复入账。
		//    注意：不能用单条 CTE「INSERT 再回填同表行」——PostgreSQL 中同一语句内 data-modifying CTE
		//    刚插入的行对主查询不可见（只能经 RETURNING 取值），主 UPDATE 会匹配 0 行导致 applied 恒 false。
		//    故拆为事务内分步执行。
		ledgerID, err := scanInsertedLedgerID(txCtx, txClient, input)
		if err != nil {
			return fmt.Errorf("apply affiliate redeem cashback: %w", err)
		}
		// 冲突命中（重复返现）→ 未插入，幂等跳过，不动余额。
		if ledgerID == 0 {
			applied = false
			return nil
		}

		// 2) 仅在真正插入后入账：加邀请人余额 + 历史返现额度，并取回更新后的快照值。
		balanceAfter, err := scanUpdatedFloat(txCtx, txClient,
			"UPDATE users SET balance = balance + $2, updated_at = NOW() WHERE id = $1 RETURNING balance::double precision",
			input.InviterID, input.CashbackAmount)
		if err != nil {
			return fmt.Errorf("bump inviter balance: %w", err)
		}
		quotaAfter, err := scanUpdatedFloat(txCtx, txClient,
			"UPDATE user_affiliates SET aff_history_quota = aff_history_quota + $2, updated_at = NOW() WHERE user_id = $1 RETURNING aff_history_quota::double precision",
			input.InviterID, input.CashbackAmount)
		if err != nil {
			return fmt.Errorf("bump inviter aff_history_quota: %w", err)
		}

		// 3) 回填流水的快照列（在 ledger 行已提交进当前事务后，单独 UPDATE 可正常命中）。
		if _, err := txClient.ExecContext(txCtx,
			"UPDATE user_affiliate_ledger SET balance_after = $2, aff_history_quota_after = $3, updated_at = NOW() WHERE id = $1",
			ledgerID, balanceAfter, quotaAfter); err != nil {
			return fmt.Errorf("backfill cashback ledger snapshot: %w", err)
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

// scanInsertedLedgerID 幂等插入返现流水行，返回新行 id；ON CONFLICT 命中（重复返现）时返回 0。
func scanInsertedLedgerID(ctx context.Context, txClient *dbent.Client, input service.AffiliateRedeemCashbackInput) (int64, error) {
	rows, err := txClient.QueryContext(ctx, `
INSERT INTO user_affiliate_ledger (
	user_id, action, amount, source_user_id, source_redeem_code_id,
	source_redeem_code_type, source_redeem_code_value, source_subscription_group_id,
	source_subscription_validity_days, cashback_base_amount, cashback_rate_percent,
	created_at, updated_at
)
	VALUES ($1, 'cashback', $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
ON CONFLICT DO NOTHING
RETURNING id`,
		input.InviterID,
		input.CashbackAmount,
		input.InviteeUserID,
		input.RedeemCodeID,
		input.RedeemCodeType,
		input.RedeemValue,
		nullableInt64Arg(input.SubscriptionGroupID),
		nullableIntArg(input.SubscriptionValidity),
		input.BaseAmount,
		input.RatePercent,
	)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var id int64
	if rows.Next() {
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return id, nil
}

// scanUpdatedFloat 执行一条 RETURNING 单个 float 值的 UPDATE，返回该值；无匹配行时返回 0。
// 每次调用用完即关 rows，避免同一事务连接残留未读结果导致后续查询阻塞。
func scanUpdatedFloat(ctx context.Context, txClient *dbent.Client, query string, args ...any) (float64, error) {
	rows, err := txClient.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var v float64
	if rows.Next() {
		if err := rows.Scan(&v); err != nil {
			return 0, err
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return v, nil
}

func (r *affiliateRepository) ListUserCashbackRecords(ctx context.Context, userID int64, limit int) ([]service.AffiliateCashbackRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, cashbackRecordsSelectSQL()+`
WHERE l.action = 'cashback' AND l.user_id = $1
ORDER BY l.created_at DESC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list user affiliate cashback records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanCashbackRecords(rows)
}

func (r *affiliateRepository) GetUserCashbackTotal(ctx context.Context, userID int64) (float64, error) {
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `
SELECT COALESCE(SUM(amount), 0)::double precision
FROM user_affiliate_ledger
WHERE action = 'cashback' AND user_id = $1`, userID)
	if err != nil {
		return 0, fmt.Errorf("get user affiliate cashback total: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var total float64
	if rows.Next() {
		if err := rows.Scan(&total); err != nil {
			return 0, err
		}
	}
	return total, rows.Err()
}

func (r *affiliateRepository) ListCashbackRecords(ctx context.Context, filter service.AffiliateRecordFilter) ([]service.AffiliateCashbackRecord, int64, error) {
	client := clientFromContext(ctx, r.client)
	where, args := buildCashbackRecordWhere(filter)
	countRows, err := client.QueryContext(ctx, `SELECT COUNT(*) FROM user_affiliate_ledger l
JOIN users inviter ON inviter.id = l.user_id
JOIN users invitee ON invitee.id = l.source_user_id
LEFT JOIN redeem_codes rc ON rc.id = l.source_redeem_code_id `+where, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("count affiliate cashback records: %w", err)
	}
	var total int64
	if countRows.Next() {
		if err := countRows.Scan(&total); err != nil {
			_ = countRows.Close()
			return nil, 0, err
		}
	}
	if err := countRows.Close(); err != nil {
		return nil, 0, err
	}
	sortBy := "l.created_at"
	switch filter.SortBy {
	case "cashback_amount":
		sortBy = "l.amount"
	case "redeem_value":
		sortBy = "l.source_redeem_code_value"
	case "cashback_base_amount":
		sortBy = "l.cashback_base_amount"
	case "cashback_rate_percent":
		sortBy = "l.cashback_rate_percent"
	}
	sortOrder := "DESC"
	if !filter.SortDesc {
		sortOrder = "ASC"
	}
	offset := (filter.Page - 1) * filter.PageSize
	queryArgs := append(args, filter.PageSize, offset)
	rows, err := client.QueryContext(ctx, cashbackRecordsSelectSQL()+where+fmt.Sprintf(`
ORDER BY %s %s
LIMIT $%d OFFSET $%d`, sortBy, sortOrder, len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list affiliate cashback records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	records, err := scanCashbackRecords(rows)
	return records, total, err
}

func cashbackRecordsSelectSQL() string {
	return `
SELECT l.id,
       l.user_id,
       COALESCE(inviter.email, ''),
       COALESCE(inviter.username, ''),
       COALESCE(l.source_user_id, 0),
       COALESCE(invitee.email, ''),
       COALESCE(invitee.username, ''),
       l.source_redeem_code_id,
       COALESCE(rc.code, ''),
       COALESCE(l.source_redeem_code_type, ''),
       COALESCE(l.source_redeem_code_value, 0)::double precision,
       l.source_subscription_group_id,
       COALESCE(g.name, ''),
       l.source_subscription_validity_days,
       COALESCE(l.cashback_base_amount, 0)::double precision,
       COALESCE(l.cashback_rate_percent, 0)::double precision,
       l.amount::double precision,
       l.balance_after,
       l.created_at
FROM user_affiliate_ledger l
JOIN users inviter ON inviter.id = l.user_id
LEFT JOIN users invitee ON invitee.id = l.source_user_id
LEFT JOIN redeem_codes rc ON rc.id = l.source_redeem_code_id
LEFT JOIN groups g ON g.id = l.source_subscription_group_id
`
}

func buildCashbackRecordWhere(filter service.AffiliateRecordFilter) (string, []any) {
	clauses := []string{"l.action = 'cashback'"}
	args := make([]any, 0)
	if search := strings.TrimSpace(filter.Search); search != "" {
		args = append(args, "%"+strings.ToLower(search)+"%")
		idx := len(args)
		clauses = append(clauses, fmt.Sprintf(`(
LOWER(inviter.email) LIKE $%d OR LOWER(inviter.username) LIKE $%d OR
LOWER(invitee.email) LIKE $%d OR LOWER(invitee.username) LIKE $%d OR
LOWER(COALESCE(rc.code, '')) LIKE $%d
)`, idx, idx, idx, idx, idx))
	}
	if filter.StartAt != nil {
		args = append(args, *filter.StartAt)
		clauses = append(clauses, fmt.Sprintf("l.created_at >= $%d", len(args)))
	}
	if filter.EndAt != nil {
		args = append(args, *filter.EndAt)
		clauses = append(clauses, fmt.Sprintf("l.created_at <= $%d", len(args)))
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func scanCashbackRecords(rows *sql.Rows) ([]service.AffiliateCashbackRecord, error) {
	out := make([]service.AffiliateCashbackRecord, 0)
	for rows.Next() {
		var item service.AffiliateCashbackRecord
		var redeemCodeID sql.NullInt64
		var subscriptionGroupID sql.NullInt64
		var validityDays sql.NullInt64
		var balanceAfter sql.NullFloat64
		if err := rows.Scan(
			&item.LedgerID,
			&item.InviterID,
			&item.InviterEmail,
			&item.InviterUsername,
			&item.InviteeID,
			&item.InviteeEmail,
			&item.InviteeUsername,
			&redeemCodeID,
			&item.RedeemCode,
			&item.RedeemCodeType,
			&item.RedeemValue,
			&subscriptionGroupID,
			&item.SubscriptionGroup,
			&validityDays,
			&item.CashbackBaseAmount,
			&item.CashbackRatePercent,
			&item.CashbackAmount,
			&balanceAfter,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		if redeemCodeID.Valid {
			item.RedeemCodeID = &redeemCodeID.Int64
		}
		if subscriptionGroupID.Valid {
			item.SubscriptionGroupID = &subscriptionGroupID.Int64
		}
		if validityDays.Valid {
			v := int(validityDays.Int64)
			item.ValidityDays = &v
		}
		if balanceAfter.Valid {
			item.InviterBalanceAfter = &balanceAfter.Float64
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func nullableIntArg(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}
