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
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"

	_ "github.com/lib/pq" // PostgreSQL 驱动
)

// W6 批量派生命令。
//
// 迁移之后 group_model_config、model_group_prices、cost_accounting_rules 是空的，
// 而唯一的写入方是渠道保存钩子（AfterChannelSaved），上线时不能靠「把每个渠道在后台保存一遍」来触发。
// 与 model-catalog seed、pricing-replay 同一种做法：服务端二进制的子命令，在容器里直接执行，
// 连接参数沿用服务自己的配置与环境变量：
//
//	docker exec <ctr> /app/sub2api pricing-matrix derive [--apply] [--channel ID | --group ID]
//
// 默认 dry-run：会话级只读（default_transaction_read_only=on），实时派生并与库里现状对比，按分组打印摘要。
// --apply 复用渠道保存钩子的同一套派生与落库（PricingDerivationService.RefreshChannel / RefreshGroups），
// 每个渠道一个事务；不改任何分组的 pricing_stage，不碰 channel_model_pricing 与 groups。
// 官方价事实读本地价格文件（与 pricing-replay 同一套解析），服务里的内存价格数据以后若有差异，
// 下一次渠道保存会按那时的事实重新派生。

const pricingMatrixUsage = "usage: pricing-matrix derive [--apply] [--channel ID | --group ID] " +
	"[--pricing-file PATH] [--channel-timeout 2m] [--statement-timeout 60s] [--lock-timeout 10s]"

// pricingMatrixDeriveArgs `pricing-matrix derive` 的参数。
type pricingMatrixDeriveArgs struct {
	apply            bool
	channelID        int64
	groupID          int64
	pricingFile      string
	channelTimeout   time.Duration
	statementTimeout time.Duration
	lockTimeout      time.Duration
}

func parsePricingMatrixDeriveArgs(args []string, errOut io.Writer) (pricingMatrixDeriveArgs, error) {
	var a pricingMatrixDeriveArgs
	fs := flag.NewFlagSet("pricing-matrix derive", flag.ContinueOnError)
	fs.SetOutput(errOut)
	apply := fs.Bool("apply", false, "write the derived rows (default is a read-only dry-run)")
	channel := fs.Int64("channel", 0, "only this channel id (default: every enabled channel)")
	group := fs.Int64("group", 0, "only this group id")
	pricingFile := fs.String("pricing-file", "", "local LiteLLM price file (default: <pricing.data_dir>/model_pricing.json, then the fallback file)")
	channelTimeout := fs.Duration("channel-timeout", 2*time.Minute, "timeout of one channel (one transaction)")
	stmt := fs.Duration("statement-timeout", 60*time.Second, "session statement_timeout")
	lock := fs.Duration("lock-timeout", 10*time.Second, "session lock_timeout")
	if err := fs.Parse(args); err != nil {
		return a, err
	}
	if fs.NArg() > 0 {
		return a, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *channel < 0 || *group < 0 {
		return a, errors.New("--channel and --group must be positive ids")
	}
	if *channel != 0 && *group != 0 {
		return a, errors.New("--channel and --group are mutually exclusive")
	}
	if *channelTimeout <= 0 || *channelTimeout > 30*time.Minute {
		return a, errors.New("--channel-timeout must be positive and at most 30m")
	}
	if *stmt <= 0 || *stmt > 30*time.Minute {
		return a, errors.New("--statement-timeout must be positive and at most 30m")
	}
	if *lock <= 0 || *lock > time.Minute {
		return a, errors.New("--lock-timeout must be positive and at most 1m")
	}
	a.apply, a.channelID, a.groupID, a.pricingFile = *apply, *channel, *group, *pricingFile
	a.channelTimeout, a.statementTimeout, a.lockTimeout = *channelTimeout, *stmt, *lock
	return a, nil
}

// pricingMatrixDeriveDSN dry-run 用只读会话；apply 只带超时。
func pricingMatrixDeriveDSN(base string, a pricingMatrixDeriveArgs) string {
	if !a.apply {
		return repository.PricingReplayReadOnlyDSN(base, a.statementTimeout, a.lockTimeout)
	}
	return fmt.Sprintf("%s statement_timeout=%d lock_timeout=%d", base, a.statementTimeout.Milliseconds(), a.lockTimeout.Milliseconds())
}

// runPricingMatrixCommand 执行 `pricing-matrix` 子命令。args 不含子命令名本身。
func runPricingMatrixCommand(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "derive" {
		return errors.New(pricingMatrixUsage)
	}
	a, err := parsePricingMatrixDeriveArgs(args[1:], out)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, pricingMatrixUsage)
	}
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := timezone.Init(cfg.Timezone); err != nil {
		return fmt.Errorf("init timezone: %w", err)
	}
	// 价格回退提示等标准库日志不进终端，免得盖住摘要。
	_ = logger.SetLevel("error")
	prevLog := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(prevLog)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 只开一个普通的数据库连接：不走 repository.InitEnt，因为它会执行迁移与密钥初始化。
	db, err := sql.Open("postgres", pricingMatrixDeriveDSN(cfg.Database.DSNWithTimezoneAndApplicationName(cfg.Timezone, config.DBApplicationNameCLI), a))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(2)

	pricingFile, err := resolvePricingReplayFile(a.pricingFile, cfg)
	if err != nil {
		return err
	}
	pricingSvc := service.NewPricingService(cfg, nil)
	if _, err := pricingSvc.LoadOfflinePricing(pricingFile); err != nil {
		return fmt.Errorf("load price file: %w", err)
	}
	billing := service.NewBillingService(cfg, pricingSvc)

	deriver := service.NewPricingDerivationService(
		repository.NewPricingMatrixRepository(db), repository.NewChannelRepository(db), billing)

	mode := service.PricingDeriveModeDryRun
	if a.apply {
		mode = service.PricingDeriveModeApply
	}
	_, _ = fmt.Fprintf(out, "pricing-matrix derive db=%s mode=%s channel=%d group=%d pricing_file=%s\n",
		cfg.Database.DBName, mode, a.channelID, a.groupID, pricingFile)

	report, err := deriver.DeriveBatch(ctx, service.PricingDeriveBatchOptions{
		ChannelID: a.channelID, GroupID: a.groupID, Apply: a.apply, ChannelTimeout: a.channelTimeout,
	})
	if err != nil {
		if report != nil {
			printPricingDeriveReport(out, report, cfg.Database.DBName)
		}
		return err
	}
	printPricingDeriveReport(out, report, cfg.Database.DBName)
	if len(report.Failures) > 0 {
		return fmt.Errorf("%d channel(s) failed; the others were processed", len(report.Failures))
	}
	return nil
}

// pricingDeriveMachineLine 输出的最后一行（一行 JSON）。
type pricingDeriveMachineLine struct {
	Command  string                         `json:"command"`
	Database string                         `json:"database"`
	Mode     string                         `json:"mode"`
	Totals   service.PricingDeriveTotals    `json:"totals"`
	Failures []service.PricingDeriveFailure `json:"failures"`
}

// printPricingDeriveReport 按分组打印摘要，最后一行是机器可读的 JSON 汇总。
func printPricingDeriveReport(out io.Writer, r *service.PricingDeriveBatchReport, database string) {
	for _, g := range r.Groups {
		orphan := ""
		if g.Orphan {
			orphan = " orphan=true"
		}
		_, _ = fmt.Fprintf(out,
			"channel=%d group=%d platform=%s stage=%s status=%s config=%s cells[new=%d updated=%d deleted=%d unchanged=%d blocked=%d] rules[new=%d deleted=%d unchanged=%d] modes[inherit=%d extra=%d custom=%d closed=%d] warnings=%d%s\n",
			g.ChannelID, g.GroupID, g.Platform, g.Stage, g.Status, g.Config,
			g.CellsNew, g.CellsUpdated, g.CellsDeleted, g.CellsUnchanged, g.CellsBlocked,
			g.RulesNew, g.RulesDeleted, g.RulesUnchanged,
			g.Inherit, g.Extra, g.Custom, g.Closed, g.Warnings, orphan)
		if g.SkipReason != "" {
			_, _ = fmt.Fprintf(out, "  skipped: %s\n", g.SkipReason)
		}
		for _, n := range g.Notes {
			if n.Level != service.DerivationNoteWarn {
				continue
			}
			_, _ = fmt.Fprintf(out, "  warn: %s model=%q %s\n", n.Code, n.Model, n.Message)
		}
	}
	for _, f := range r.Failures {
		_, _ = fmt.Fprintf(out, "FAILED channel=%d group=%d: %s\n", f.ChannelID, f.GroupID, f.Error)
	}
	t := r.Totals
	_, _ = fmt.Fprintf(out, "totals: channels=%d (inactive skipped %d) groups=%d changed=%d unchanged=%d skipped_v2=%d failures=%d\n",
		t.Channels, t.ChannelsInactive, t.Groups, t.GroupsChanged, t.GroupsUnchanged, t.GroupsSkipped, t.Failures)
	if r.Mode == service.PricingDeriveModeDryRun {
		_, _ = fmt.Fprintln(out, "dry-run: nothing was written; add --apply to write")
	}
	line := pricingDeriveMachineLine{Command: "pricing-matrix derive", Database: database, Mode: r.Mode, Totals: r.Totals, Failures: r.Failures}
	raw, err := json.Marshal(line)
	if err != nil {
		raw = []byte(fmt.Sprintf(`{"command":"pricing-matrix derive","error":%q}`, err.Error()))
	}
	_, _ = fmt.Fprintln(out, string(raw))
}
