package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"

	_ "github.com/lib/pq" // PostgreSQL 驱动
)

// W6 价格回放命令（设计 4.5，PR6）。
//
// 以服务端二进制的子命令形式提供（与 model-catalog seed 同一种做法）：线上跑的是镜像里的 /app/sub2api，
// 子命令能直接在容器里执行，不需要另外分发一个二进制；连接参数沿用服务自己的配置与环境变量
// （DATABASE_HOST、DATABASE_DBNAME 等），所以 codex 库与 free 库用同一条命令、换环境即可。
//
//	sub2api pricing-replay [--days 30] [--groups 16,18,27,30,33,26] [--out-dir DIR] [--strict] ...
//
// 离线、只读：连接在会话级是只读的，不写库、不写 usage log、不扣费，价格数据按服务同样的规则取（pinned 用生效快照，否则读本地文件）。
// 输出两个文件：JSON 汇总（含配置绑定信息，供 PR7 切换时核对）与差异 CSV。

const pricingReplayUsage = "usage: pricing-replay [--days 30] [--until RFC3339] [--groups 16,18,...] [--out-dir DIR] " +
	"[--matrix-source derived|stored] [--pricing-file PATH] [--workers N] [--batch-size 5000] " +
	"[--statement-timeout 120s] [--max-diff-rows N] [--sample 20] [--strict] [--record]"

const (
	replayMatrixDerived = "derived"
	replayMatrixStored  = "stored"
)

// pricingReplayArgs `pricing-replay` 的参数。
type pricingReplayArgs struct {
	days             int
	until            time.Time
	groups           []int64
	outDir           string
	matrixSource     string
	pricingFile      string
	workers          int
	batchSize        int
	statementTimeout time.Duration
	lockTimeout      time.Duration
	maxDiffRows      int
	sample           int
	strict           bool
	record           bool
}

// parsePricingReplayGroups 解析逗号分隔的分组 id 列表；空串表示自动（窗口内有用量的全部未软删分组）。
func parsePricingReplayGroups(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var ids []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid group id %q", part)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// parsePricingReplayArgs 解析并校验参数。now 是「现在」，--until 缺省时的窗口末尾。
func parsePricingReplayArgs(args []string, errOut io.Writer, now time.Time) (pricingReplayArgs, error) {
	var a pricingReplayArgs
	fs := flag.NewFlagSet("pricing-replay", flag.ContinueOnError)
	fs.SetOutput(errOut)
	days := fs.Int("days", 30, "replay the usage_logs rows of the last N days")
	until := fs.String("until", "", "end of the window, RFC3339 (default: now)")
	groups := fs.String("groups", "", "comma separated group ids (default: every live group with usage in the window)")
	outDir := fs.String("out-dir", ".", "directory for the JSON summary and the diff CSV")
	source := fs.String("matrix-source", replayMatrixDerived, "v2 side: 'derived' (derive from the channel config now, what a switch would freeze) or 'stored' (read the stored matrix rows)")
	pricingFile := fs.String("pricing-file", "", "local LiteLLM price file override (default: the service's source: the active snapshot when pricing is pinned, else <pricing.data_dir>/model_pricing.json, then the fallback file)")
	workers := fs.Int("workers", min(runtime.NumCPU(), 8), "parallel workers")
	batch := fs.Int("batch-size", service.DefaultPricingReplayBatchSize, "rows per keyset page")
	stmt := fs.Duration("statement-timeout", repository.DefaultPricingReplayStatementTimeout, "session statement_timeout")
	lock := fs.Duration("lock-timeout", repository.DefaultPricingReplayLockTimeout, "session lock_timeout")
	maxDiff := fs.Int("max-diff-rows", service.DefaultPricingReplayMaxDiffRows, "maximum rows written to the diff CSV (diffs are always counted)")
	sample := fs.Int("sample", service.DefaultPricingReplaySampleSize, "translation diffs kept as samples in the summary")
	strict := fs.Bool("strict", false, "exit with an error when the replay does not pass")
	record := fs.Bool("record", false, "write each group's result to pricing_replay_evidence (the stage switch gate reads it); needs a writable database user")
	if err := fs.Parse(args); err != nil {
		return a, err
	}
	if fs.NArg() > 0 {
		return a, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *days <= 0 || *days > 365 {
		return a, errors.New("--days must be between 1 and 365")
	}
	if *source != replayMatrixDerived && *source != replayMatrixStored {
		return a, errors.New("--matrix-source must be 'derived' or 'stored'")
	}
	if *workers <= 0 || *workers > 64 {
		return a, errors.New("--workers must be between 1 and 64")
	}
	if *batch <= 0 || *batch > 50000 {
		return a, errors.New("--batch-size must be between 1 and 50000")
	}
	if *stmt <= 0 || *stmt > 30*time.Minute {
		return a, errors.New("--statement-timeout must be positive and at most 30m")
	}
	if *lock <= 0 || *lock > time.Minute {
		return a, errors.New("--lock-timeout must be positive and at most 1m")
	}
	ids, err := parsePricingReplayGroups(*groups)
	if err != nil {
		return a, err
	}
	a.until = now
	if strings.TrimSpace(*until) != "" {
		if a.until, err = time.Parse(time.RFC3339, strings.TrimSpace(*until)); err != nil {
			return a, fmt.Errorf("invalid --until: %w", err)
		}
	}
	a.days, a.groups, a.outDir, a.matrixSource, a.pricingFile = *days, ids, *outDir, *source, *pricingFile
	a.workers, a.batchSize, a.statementTimeout, a.lockTimeout = *workers, *batch, *stmt, *lock
	a.maxDiffRows, a.sample, a.strict, a.record = *maxDiff, *sample, *strict, *record
	return a, nil
}

// resolvePricingReplayFile 决定读哪份本地价格文件：显式指定的优先，其次服务自己的数据目录，最后内置回退文件。
func resolvePricingReplayFile(flagPath string, cfg *config.Config) (string, error) {
	if strings.TrimSpace(flagPath) != "" {
		return flagPath, nil
	}
	for _, candidate := range []string{filepath.Join(cfg.Pricing.DataDir, "model_pricing.json"), cfg.Pricing.FallbackFile} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("no local price file found; pass --pricing-file")
}

// runPricingReplayCommand 执行 `pricing-replay` 子命令。args 不含子命令名本身。
func runPricingReplayCommand(args []string, out io.Writer) error {
	a, err := parsePricingReplayArgs(args, out, time.Now())
	if err != nil {
		return fmt.Errorf("%w\n%s", err, pricingReplayUsage)
	}
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := timezone.Init(cfg.Timezone); err != nil {
		return fmt.Errorf("init timezone: %w", err)
	}
	// 回放会把成本函数跑上百万次：服务日志降到 error，标准库 log（价格回退提示等）整体丢弃，免得刷屏。
	_ = logger.SetLevel("error")
	prevLog := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(prevLog)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := repository.PricingReplayReadOnlyDSN(
		cfg.Database.DSNWithTimezoneAndApplicationName(cfg.Timezone, repository.PricingReplayDBApplicationName),
		a.statementTimeout, a.lockTimeout)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(4)
	session, err := repository.VerifyPricingReplaySession(ctx, db)
	if err != nil {
		return err
	}

	// 与服务进程同一套取价规则（pinned 时用生效快照）；取价失败拒绝运行。
	pricingSvc, pricingInfo, err := loadServicePricing(ctx, db, cfg, a.pricingFile, true)
	if err != nil {
		return err
	}
	billing := service.NewBillingService(cfg, pricingSvc)

	channelRepo := repository.NewChannelRepository(db)
	matrixRepo := repository.NewPricingMatrixRepository(db)
	deriver := service.NewPricingDerivationService(matrixRepo, channelRepo, billing)
	channels := service.NewChannelService(channelRepo, nil, nil, pricingSvc, nil)
	var matrixSource service.MatrixSnapshotSource = matrixRepo
	if a.matrixSource == replayMatrixDerived {
		matrixSource = service.NewDerivedMatrixSource(matrixRepo, deriver)
	}
	replayer, err := service.NewPricingReplayer(billing, channels, matrixSource)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(a.outDir, 0o700); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	base := filepath.Join(a.outDir, fmt.Sprintf("pricing-replay-%s-%s", cfg.Database.DBName, stamp))
	csvFile, err := os.OpenFile(base+"-diffs.csv", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create diff csv: %w", err)
	}
	defer func() { _ = csvFile.Close() }()

	_, _ = fmt.Fprintf(out, "pricing-replay db=%s days=%d until=%s matrix_source=%s workers=%d batch=%d groups=%v\n",
		cfg.Database.DBName, a.days, a.until.Format(time.RFC3339), a.matrixSource, a.workers, a.batchSize, a.groups)
	summary, err := replayer.Run(ctx, repository.NewPricingReplayDataSource(db), deriver, service.PricingReplayOptions{
		From:        a.until.Add(-time.Duration(a.days) * 24 * time.Hour),
		To:          a.until,
		GroupIDs:    a.groups,
		BatchSize:   a.batchSize,
		Workers:     a.workers,
		MaxDiffRows: a.maxDiffRows,
		SampleSize:  a.sample,
		DiffCSV:     csvFile,
		Progress:    out,
	})
	if err != nil {
		return err
	}
	summary.Meta = map[string]string{
		"database":                  cfg.Database.DBName,
		"version":                   Version,
		"matrix_source":             a.matrixSource,
		"pricing_source":            pricingInfo.Source,
		"pricing_snapshot_id":       strconv.FormatInt(pricingInfo.SnapshotID, 10),
		"pricing_file":              filepath.Base(pricingInfo.Path),
		"pricing_data_sha256":       pricingInfo.SHA256,
		"pricing_data_models":       strconv.Itoa(pricingInfo.Models),
		"session_read_only":         session["default_transaction_read_only"],
		"session_statement_timeout": session["statement_timeout"],
		"session_lock_timeout":      session["lock_timeout"],
	}

	raw, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("encode summary: %w", err)
	}
	if err := os.WriteFile(base+"-summary.json", raw, 0o600); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}

	printPricingReplayResult(out, summary, base)
	if a.record {
		n, err := recordPricingReplayEvidence(ctx, cfg, summary)
		if err != nil {
			return fmt.Errorf("record replay evidence: %w", err)
		}
		_, _ = fmt.Fprintf(out, "recorded: %d group result(s) in pricing_replay_evidence\n", n)
	}
	if a.strict && !summary.Verdict.Pass {
		return fmt.Errorf("replay did not pass: %s", strings.Join(summary.Verdict.Reasons, "; "))
	}
	return nil
}

// printPricingReplayResult 在终端打印一屏结论：行数、耗时、差异分布、通过与否、输出文件。
func printPricingReplayResult(out io.Writer, s *service.PricingReplaySummary, base string) {
	_, _ = fmt.Fprintf(out, "rows: in_window=%d selected=%d replayed=%d uncovered=%d errored=%d elapsed=%.1fs (%.0f rows/s)\n",
		s.RowsInWindow, s.RowsSelected, s.RowsReplayed, s.RowsUncovered, s.RowsErrored, s.ElapsedSeconds, s.RowsPerSecond)
	for _, d := range s.Diffs {
		_, _ = fmt.Fprintf(out, "diff: kind=%s class=%s reason=%s count=%d\n", d.Kind, d.Class, d.Reason, d.Count)
	}
	_, _ = fmt.Fprintf(out, "verdict: pass=%t translation_diffs=%d expected_diffs=%d errors=%d binding_stable=%t\n",
		s.Verdict.Pass, s.Verdict.TranslationDiffs, s.Verdict.ExpectedDiffs, s.Verdict.Errors, s.Binding.Stable)
	for _, reason := range s.Verdict.Reasons {
		_, _ = fmt.Fprintf(out, "not passed: %s\n", reason)
	}
	_, _ = fmt.Fprintf(out, "summary: %s-summary.json\ndiffs:   %s-diffs.csv (%d rows)\n", base, base, s.DiffCSVRows)
}

// recordPricingReplayEvidence 用一个可写连接把每个分组的回放结果记进 pricing_replay_evidence。
// 回放本身用的是只读会话，不能复用；这里只在回放结束后插入证据行，不动任何计费数据。
func recordPricingReplayEvidence(ctx context.Context, cfg *config.Config, summary *service.PricingReplaySummary) (int, error) {
	db, err := sql.Open("postgres", cfg.Database.DSNWithTimezone(cfg.Timezone))
	if err != nil {
		return 0, fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	rows := service.ReplayEvidenceFromSummary(summary, time.Now().UTC())
	if err := repository.NewPricingReplayEvidenceStore(db).RecordReplayEvidence(ctx, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}
