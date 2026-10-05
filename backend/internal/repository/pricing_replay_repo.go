package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
)

// W6 PR6：30 天价格回放的数据来源（设计 4.5）。全部是只读查询：
//   - 连接在会话级设置 default_transaction_read_only=on、statement_timeout、lock_timeout（见 PricingReplayReadOnlyDSN），
//     数据库自己拒绝任何写入；
//   - 读用量行时再套一层 READ ONLY 事务；
//   - 用量按 id 做 keyset 分页，不用 OFFSET。

// PricingReplayDBApplicationName 回放连接的 application_name，方便在 pg_stat_activity 里认出来。
const PricingReplayDBApplicationName = "sub2api-pricing-replay"

// DefaultPricingReplayStatementTimeout 回放里单条语句的超时：最重的是窗口内按分组计数与取 id 范围
// （扫一个月的 usage_logs，生产实测 1 秒左右），其余每批 5000 行走主键。
const DefaultPricingReplayStatementTimeout = 120 * time.Second

// DefaultPricingReplayLockTimeout 回放不拿任何锁；设一个很短的 lock_timeout 是兜底，万一碰到锁就立刻放弃，不排队。
const DefaultPricingReplayLockTimeout = 2 * time.Second

// PricingReplayReadOnlyDSN 在连接串后面追加只读会话参数：这些参数随连接建立时的启动包带给服务端，
// 连接池里的每一条连接都生效。
func PricingReplayReadOnlyDSN(base string, statementTimeout, lockTimeout time.Duration) string {
	return fmt.Sprintf("%s default_transaction_read_only=on statement_timeout=%d lock_timeout=%d",
		base, statementTimeout.Milliseconds(), lockTimeout.Milliseconds())
}

// VerifyPricingReplaySession 确认会话确实是只读的（default_transaction_read_only=on）并带着超时，
// 返回这三个设置的当前值，供写进汇总。
func VerifyPricingReplaySession(ctx context.Context, db *sql.DB) (map[string]string, error) {
	var readOnly, stmt, lock string
	err := db.QueryRowContext(ctx,
		`SELECT current_setting('default_transaction_read_only'), current_setting('statement_timeout'), current_setting('lock_timeout')`,
	).Scan(&readOnly, &stmt, &lock)
	if err != nil {
		return nil, fmt.Errorf("read session settings: %w", err)
	}
	if readOnly != "on" {
		return nil, fmt.Errorf("refusing to run: default_transaction_read_only is %q, expected on", readOnly)
	}
	if stmt == "0" {
		return nil, fmt.Errorf("refusing to run: statement_timeout is not set")
	}
	return map[string]string{
		"default_transaction_read_only": readOnly,
		"statement_timeout":             stmt,
		"lock_timeout":                  lock,
	}, nil
}

type pricingReplayRepository struct {
	db *sql.DB
}

// NewPricingReplayDataSource 构造回放的数据来源。db 必须是只读会话（见 PricingReplayReadOnlyDSN）。
func NewPricingReplayDataSource(db *sql.DB) service.PricingReplayDataSource {
	return &pricingReplayRepository{db: db}
}

// readOnlyTx 开一个 READ ONLY 事务执行 fn，结束时一律回滚（只读，没有东西要提交）。
func (r *pricingReplayRepository) readOnlyTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin read-only tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

func (r *pricingReplayRepository) UsageGroupCounts(ctx context.Context, from, to time.Time) (map[int64]int64, error) {
	out := make(map[int64]int64)
	err := r.readOnlyTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT group_id, COUNT(*) FROM usage_logs
			 WHERE created_at >= $1 AND created_at < $2 AND group_id IS NOT NULL
			 GROUP BY group_id`, from, to)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id, n int64
			if err := rows.Scan(&id, &n); err != nil {
				return err
			}
			out[id] = n
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("count usage rows by group: %w", err)
	}
	return out, nil
}

func (r *pricingReplayRepository) LoadGroups(ctx context.Context, ids []int64) (map[int64]*service.Group, error) {
	out := make(map[int64]*service.Group, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	err := r.readOnlyTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, platform, rate_multiplier, image_rate_independent, image_rate_multiplier,
			        image_price_1k, image_price_2k, image_price_4k
			 FROM groups WHERE id = ANY($1) AND deleted_at IS NULL`, pq.Array(ids))
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			g := &service.Group{}
			var p1, p2, p4 sql.NullFloat64
			if err := rows.Scan(&g.ID, &g.Platform, &g.RateMultiplier, &g.ImageRateIndependent, &g.ImageRateMultiplier, &p1, &p2, &p4); err != nil {
				return err
			}
			g.ImagePrice1K, g.ImagePrice2K, g.ImagePrice4K = nullFloatPtr(p1), nullFloatPtr(p2), nullFloatPtr(p4)
			out[g.ID] = g
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("load groups: %w", err)
	}
	return out, nil
}

func nullFloatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

func (r *pricingReplayRepository) IDBounds(ctx context.Context, from, to time.Time) (int64, int64, error) {
	var minID, maxID int64
	err := r.readOnlyTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT COALESCE(MIN(id), 0), COALESCE(MAX(id), 0) FROM usage_logs WHERE created_at >= $1 AND created_at < $2`,
			from, to).Scan(&minID, &maxID)
	})
	if err != nil {
		return 0, 0, fmt.Errorf("read id bounds: %w", err)
	}
	return minID, maxID, nil
}

func (r *pricingReplayRepository) Batch(ctx context.Context, q service.PricingReplayBatchQuery) ([]service.PricingReplayRow, error) {
	out := make([]service.PricingReplayRow, 0, q.Limit)
	err := r.readOnlyTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, created_at, user_id, account_id, group_id, COALESCE(served_group_id, 0),
			        model, requested_model, upstream_model, service_tier, billing_mode,
			        COALESCE(input_tokens, 0), COALESCE(output_tokens, 0),
			        COALESCE(cache_creation_tokens, 0), COALESCE(cache_read_tokens, 0),
			        COALESCE(cache_creation_5m_tokens, 0), COALESCE(cache_creation_1h_tokens, 0),
			        COALESCE(image_input_tokens, 0), COALESCE(image_output_tokens, 0),
			        COALESCE(image_count, 0), image_size, image_input_size, image_output_size,
			        COALESCE(actual_cost, 0)
			 FROM usage_logs
			 WHERE id > $1 AND id <= $2 AND created_at >= $3 AND created_at < $4 AND group_id = ANY($5)
			 ORDER BY id
			 LIMIT $6`,
			q.AfterID, q.MaxID, q.From, q.To, pq.Array(q.GroupIDs), q.Limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var row service.PricingReplayRow
			var requested, upstream, tier, mode, size, inSize, outSize sql.NullString
			if err := rows.Scan(&row.ID, &row.CreatedAt, &row.UserID, &row.AccountID, &row.GroupID, &row.ServedGroupID,
				&row.Model, &requested, &upstream, &tier, &mode,
				&row.InputTokens, &row.OutputTokens, &row.CacheCreationTokens, &row.CacheReadTokens,
				&row.CacheCreation5m, &row.CacheCreation1h, &row.ImageInputTokens, &row.ImageOutputTokens,
				&row.ImageCount, &size, &inSize, &outSize, &row.StoredActualCost); err != nil {
				return err
			}
			row.RequestedModel, row.UpstreamModel, row.ServiceTier = requested.String, upstream.String, tier.String
			row.BillingMode = mode.String
			row.ImageSize, row.ImageInputSize, row.ImageOutputSize = size.String, inSize.String, outSize.String
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read usage batch after id %d: %w", q.AfterID, err)
	}
	return out, nil
}

func (r *pricingReplayRepository) UserGroupRates(ctx context.Context, userIDs []int64) (map[service.PricingReplayRateKey]float64, error) {
	out := make(map[service.PricingReplayRateKey]float64)
	if len(userIDs) == 0 {
		return out, nil
	}
	err := r.readOnlyTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT user_id, group_id, rate_multiplier FROM user_group_rate_multipliers
			 WHERE user_id = ANY($1) AND rate_multiplier IS NOT NULL`, pq.Array(userIDs))
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var k service.PricingReplayRateKey
			var rate float64
			if err := rows.Scan(&k.UserID, &k.GroupID, &rate); err != nil {
				return err
			}
			out[k] = rate
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read user group rates: %w", err)
	}
	return out, nil
}

// pricingReplayChannelTables 渠道配置相关的全部表（设计 4.7 列的那 7 张），渠道配置指纹覆盖它们的全部内容。
// 表名是常量，不来自外部输入，可以直接拼进 SQL。
var pricingReplayChannelTables = []string{
	"channels",
	"channel_groups",
	"channel_model_pricing",
	"channel_pricing_intervals",
	"channel_account_stats_pricing_rules",
	"channel_account_stats_model_pricing",
	"channel_account_stats_pricing_intervals",
}

// Fingerprint 计算渠道配置指纹与各分组矩阵行指纹。摘要由数据库对整行文本做 md5，再在 Go 里合并成 sha256。
// 行按文本排序后聚合，所以与物理顺序无关；任何一列变化（含 updated_at）都会改变摘要，宁可误作废也不放过。
func (r *pricingReplayRepository) Fingerprint(ctx context.Context, groupIDs []int64) (*service.PricingReplayFingerprint, error) {
	fp := &service.PricingReplayFingerprint{GroupMatrixHash: make(map[int64]string, len(groupIDs))}
	err := r.readOnlyTx(ctx, func(tx *sql.Tx) error {
		var b strings.Builder
		for _, table := range pricingReplayChannelTables {
			var h string
			q := fmt.Sprintf(`SELECT COALESCE(md5(string_agg(t::text, E'\n' ORDER BY t::text)), '') FROM %s t`, table)
			if err := tx.QueryRowContext(ctx, q).Scan(&h); err != nil {
				return fmt.Errorf("hash %s: %w", table, err)
			}
			b.WriteString(table)
			b.WriteByte('=')
			b.WriteString(h)
			b.WriteByte('\n')
		}
		sum := sha256.Sum256([]byte(b.String()))
		fp.ChannelConfigHash = hex.EncodeToString(sum[:])

		for _, id := range groupIDs {
			var h string
			if err := tx.QueryRowContext(ctx, groupMatrixHashSQL, id).Scan(&h); err != nil {
				return fmt.Errorf("hash matrix rows of group %d: %w", id, err)
			}
			fp.GroupMatrixHash[id] = h
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return fp, nil
}

// groupMatrixHashSQL 一个分组在库里的矩阵行（配置、单元格、成本核算规则及其价格行）的内容摘要，参数 $1 是分组 id。
const groupMatrixHashSQL = `
SELECT md5(
  COALESCE((SELECT string_agg(c::text, E'\n' ORDER BY c::text) FROM group_model_config c WHERE c.group_id = $1), '')
  || '|' ||
  COALESCE((SELECT string_agg(p::text, E'\n' ORDER BY p::text) FROM model_group_prices p WHERE p.group_id = $1), '')
  || '|' ||
  COALESCE((SELECT string_agg(
              r::text || '/' || COALESCE((SELECT string_agg(x::text, E'\n' ORDER BY x::text) FROM cost_accounting_rule_prices x WHERE x.rule_id = r.id), ''),
              E'\n' ORDER BY r.id)
            FROM cost_accounting_rules r WHERE r.scope_group_id = $1), '')
)`
