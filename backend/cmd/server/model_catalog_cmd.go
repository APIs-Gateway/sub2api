package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"

	_ "github.com/lib/pq" // PostgreSQL 驱动
)

// 模型目录种子命令（设计文档 2.2「种子」，S-3、REVIEW_OPUS_2 S-13）。
//
// 以服务端二进制的子命令形式提供，部署后手动执行，不进迁移：
//
//	sub2api model-catalog seed [--apply] [--include-usage-days N] [--statement-timeout 60s]
//
// 默认 dry-run：只读事务读取输入、受 statement_timeout 限制、每个阶段输出耗时，
// 只打印将登记的名单，不写任何东西；加 --apply 才登记（一律 active）。

const modelCatalogUsage = "usage: model-catalog seed [--apply] [--include-usage-days N] [--statement-timeout 60s]"

// parseModelCatalogSeedArgs 解析 `model-catalog seed` 的参数。
func parseModelCatalogSeedArgs(args []string, errOut io.Writer) (repository.ModelCatalogSeedOptions, error) {
	var opts repository.ModelCatalogSeedOptions
	fs := flag.NewFlagSet("model-catalog seed", flag.ContinueOnError)
	fs.SetOutput(errOut)
	apply := fs.Bool("apply", false, "register the planned models (default is a dry-run that writes nothing)")
	usageDays := fs.Int("include-usage-days", 0, "also use models seen in usage_logs over the last N days (0 = do not scan usage_logs; needs authorization)")
	timeout := fs.Duration("statement-timeout", repository.DefaultModelCatalogSeedStatementTimeout, "statement_timeout of the read-only transaction")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		return opts, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *usageDays < 0 || *usageDays > 365 {
		return opts, errors.New("--include-usage-days must be between 0 and 365")
	}
	if *timeout <= 0 || *timeout > 10*time.Minute {
		return opts, errors.New("--statement-timeout must be positive and at most 10m")
	}
	opts.Apply = *apply
	opts.UsageDays = *usageDays
	opts.StatementTimeout = *timeout
	return opts, nil
}

// runModelCatalogCommand 执行 `model-catalog` 子命令。args 不含子命令名本身。
func runModelCatalogCommand(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "seed" {
		return errors.New(modelCatalogUsage)
	}
	opts, err := parseModelCatalogSeedArgs(args[1:], out)
	if err != nil {
		return err
	}

	cfg, err := config.LoadForBootstrap()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// 只开一个普通的数据库连接：不走 repository.InitEnt，因为它会执行迁移与密钥初始化。
	db, err := sql.Open("postgres", cfg.Database.DSNWithTimezoneAndApplicationName(cfg.Timezone, config.DBApplicationNameCLI))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(2)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return repository.RunModelCatalogSeed(ctx, db, opts, out)
}
