package repository

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
)

// ModelCatalogSeedOptions `model-catalog seed` 的参数。
type ModelCatalogSeedOptions struct {
	// Apply 为 false（默认）时只做 dry-run：打印将登记的名单，不写任何东西。
	Apply bool
	// UsageDays 大于 0 时，把 usage_logs 近 N 天出现过的模型也作为候选（需要 cxw 授权，默认 0 = 不扫）。
	UsageDays int
	// StatementTimeout 只读事务里每条语句的超时（REVIEW_OPUS_2 S-13）。
	StatementTimeout time.Duration
}

// 种子命令的默认语句超时。
const DefaultModelCatalogSeedStatementTimeout = 60 * time.Second

// seedLockTimeout 写入事务等待锁的上限。
const seedLockTimeout = "5s"

// RunModelCatalogSeed 执行模型目录种子（设计文档 2.2「种子」，S-3、REVIEW_OPUS_2 S-13）：
//  1. 在只读事务里读取已登记的目录、渠道定价里出现过的精确模型名、（可选）usage_logs 近 N 天的模型，
//     每条语句受 statement_timeout 限制，每个阶段输出耗时；
//  2. 加上各平台默认模型列表，合并、规范化、去掉已登记的，打印将登记的名单（dry-run 到此为止）；
//  3. 仅 Apply 时在单独的事务里登记，登记的一律是 active。
//
// 部署后手动执行，不进迁移；扫 usage_logs 要读数百万行，请选低峰。
func RunModelCatalogSeed(ctx context.Context, db *sql.DB, opts ModelCatalogSeedOptions, out io.Writer) error {
	if opts.StatementTimeout <= 0 {
		opts.StatementTimeout = DefaultModelCatalogSeedStatementTimeout
	}
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	_, _ = fmt.Fprintf(out, "model-catalog seed mode=%s usage_days=%d statement_timeout=%s\n", mode, opts.UsageDays, opts.StatementTimeout)
	if opts.UsageDays > 0 {
		_, _ = fmt.Fprintln(out, "note: scanning usage_logs reads a large table; run it in a low-traffic window")
	}

	total := time.Now()
	existing, candidates, err := readSeedInputs(ctx, db, opts, out)
	if err != nil {
		return err
	}

	candidates = append(service.DefaultModelCatalogSeedCandidates(), candidates...)
	plan := service.PlanModelCatalogSeed(candidates, existing)
	_, _ = fmt.Fprintf(out, "plan: insert=%d already_registered=%d invalid=%d\n", len(plan.Insert), plan.AlreadyRegistered, plan.Invalid)
	for _, item := range plan.Insert {
		_, _ = fmt.Fprintf(out, "  + %s/%s sources=%v\n", item.Platform, item.ModelKey, item.Sources)
	}

	if !opts.Apply {
		_, _ = fmt.Fprintf(out, "dry-run finished in %s; nothing was written (re-run with --apply to register)\n", time.Since(total).Round(time.Millisecond))
		return nil
	}

	start := time.Now()
	inserted, err := applyModelCatalogSeed(ctx, db, plan.Insert)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "phase=insert elapsed=%s inserted=%d\n", time.Since(start).Round(time.Millisecond), inserted)
	_, _ = fmt.Fprintf(out, "seed finished in %s\n", time.Since(total).Round(time.Millisecond))
	return nil
}

// readSeedInputs 在只读事务里读取种子的全部输入。
func readSeedInputs(ctx context.Context, db *sql.DB, opts ModelCatalogSeedOptions, out io.Writer) (map[string]struct{}, []service.ModelCatalogSeedCandidate, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, fmt.Errorf("begin read-only tx: %w", err)
	}
	// 只读事务：不写任何东西，结束时一律回滚。
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", opts.StatementTimeout.Milliseconds())); err != nil {
		return nil, nil, fmt.Errorf("set statement_timeout: %w", err)
	}

	start := time.Now()
	existing, err := queryRegisteredCatalogKeys(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	_, _ = fmt.Fprintf(out, "phase=read_catalog elapsed=%s rows=%d\n", time.Since(start).Round(time.Millisecond), len(existing))

	var candidates []service.ModelCatalogSeedCandidate
	start = time.Now()
	channelNames, err := queryChannelPricingModels(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	candidates = append(candidates, channelNames...)
	_, _ = fmt.Fprintf(out, "phase=read_channel_pricing elapsed=%s rows=%d\n", time.Since(start).Round(time.Millisecond), len(channelNames))

	if opts.UsageDays > 0 {
		start = time.Now()
		usage, err := queryUsageModels(ctx, tx, opts.UsageDays)
		if err != nil {
			return nil, nil, err
		}
		candidates = append(candidates, usage...)
		_, _ = fmt.Fprintf(out, "phase=read_usage_logs elapsed=%s rows=%d\n", time.Since(start).Round(time.Millisecond), len(usage))
	}
	return existing, candidates, nil
}

func queryRegisteredCatalogKeys(ctx context.Context, exec dbExec) (map[string]struct{}, error) {
	rows, err := exec.QueryContext(ctx, `SELECT platform, model_key FROM model_catalog`)
	if err != nil {
		return nil, fmt.Errorf("query model_catalog: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]struct{})
	for rows.Next() {
		var platform, key string
		if err := rows.Scan(&platform, &key); err != nil {
			return nil, fmt.Errorf("scan model_catalog: %w", err)
		}
		out[service.CatalogSeedKey(platform, key)] = struct{}{}
	}
	return out, rows.Err()
}

// queryChannelPricingModels 渠道定价里出现过的精确模型名（不含通配符）。
func queryChannelPricingModels(ctx context.Context, exec dbExec) ([]service.ModelCatalogSeedCandidate, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT DISTINCT p.platform, m.model
		 FROM channel_model_pricing p
		 CROSS JOIN LATERAL jsonb_array_elements_text(
		   CASE WHEN jsonb_typeof(p.models) = 'array' THEN p.models ELSE '[]'::jsonb END) AS m(model)
		 WHERE right(m.model, 1) <> '*'`)
	if err != nil {
		return nil, fmt.Errorf("query channel pricing models: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []service.ModelCatalogSeedCandidate
	for rows.Next() {
		var c service.ModelCatalogSeedCandidate
		if err := rows.Scan(&c.Platform, &c.Model); err != nil {
			return nil, fmt.Errorf("scan channel pricing model: %w", err)
		}
		c.Source = service.SeedSourceChannelPricing
		out = append(out, c)
	}
	return out, rows.Err()
}

// queryUsageModels usage_logs 近 N 天出现过的（平台，模型）。按 created_at 索引范围扫描，平台取分组的平台。
func queryUsageModels(ctx context.Context, exec dbExec, days int) ([]service.ModelCatalogSeedCandidate, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT DISTINCT g.platform, u.model
		 FROM usage_logs u
		 JOIN groups g ON g.id = u.group_id
		 WHERE u.created_at >= NOW() - ($1::int * INTERVAL '1 day') AND u.model <> ''`, days)
	if err != nil {
		return nil, fmt.Errorf("query usage models: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []service.ModelCatalogSeedCandidate
	for rows.Next() {
		var c service.ModelCatalogSeedCandidate
		if err := rows.Scan(&c.Platform, &c.Model); err != nil {
			return nil, fmt.Errorf("scan usage model: %w", err)
		}
		c.Source = service.SeedSourceUsageLogs
		out = append(out, c)
	}
	return out, rows.Err()
}

// applyModelCatalogSeed 在单独的事务里登记；已存在的键跳过（并发安全）。
func applyModelCatalogSeed(ctx context.Context, db *sql.DB, items []service.ModelCatalogSeedItem) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '"+seedLockTimeout+"'"); err != nil {
		return 0, fmt.Errorf("set lock_timeout: %w", err)
	}
	inserted := 0
	for _, item := range items {
		entry := item.SeedEntry()
		res, err := tx.ExecContext(ctx,
			`INSERT INTO model_catalog (model_key, platform, display_name, aliases, status, note)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (platform, model_key) DO NOTHING`,
			entry.ModelKey, entry.Platform, entry.DisplayName, pq.Array(entry.Aliases), string(entry.Status), entry.Note)
		if err != nil {
			return 0, fmt.Errorf("insert %s/%s: %w", entry.Platform, entry.ModelKey, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("rows affected: %w", err)
		}
		inserted += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return inserted, nil
}
