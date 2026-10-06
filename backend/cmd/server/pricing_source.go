package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 离线命令（pricing-replay、pricing-matrix derive）取官方价的共用入口。
//
// 规则与服务进程一致（service.PricingService.LoadServicePricing）：pricing_snapshot_mode 为 pinned 时加载生效快照，
// 否则读 <data_dir>/model_pricing.json。pinned 下价格文件不再更新、必然落后于服务内存，直接读文件会派生出与服务不一致的结果；
// 取价失败（没有生效快照、校验或解析失败）一律拒绝运行，不退回文件。

// dbSettingReader 用已有的数据库连接读 settings 表（离线命令不起 ent，也就没有 SettingRepository）。
type dbSettingReader struct{ db *sql.DB }

func (r dbSettingReader) GetValue(ctx context.Context, key string) (string, error) {
	var value string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", service.ErrSettingNotFound
	}
	if err != nil {
		return "", fmt.Errorf("query setting %s: %w", key, err)
	}
	return value, nil
}

// resolveDataDirPricingFile 只认显式指定的文件或服务数据目录里的价格文件，不退回内置回退文件。
func resolveDataDirPricingFile(flagPath string, cfg *config.Config) (string, error) {
	if strings.TrimSpace(flagPath) != "" {
		return flagPath, nil
	}
	candidate := filepath.Join(cfg.Pricing.DataDir, "model_pricing.json")
	if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
		return candidate, nil
	}
	return "", fmt.Errorf("no price file at %s (the bundled fallback file is not used here)", candidate)
}

// loadServicePricing 构造价格服务并按服务的规则装入官方价。allowFallback 为 true 时，auto 模式下没有数据目录文件
// 可以退到内置回退文件（只读的 dry-run 与回放用）；false 时拒绝。
func loadServicePricing(ctx context.Context, db *sql.DB, cfg *config.Config, flagPath string, allowFallback bool) (*service.PricingService, service.PricingDataInfo, error) {
	svc := service.NewPricingService(cfg, nil)
	// 显式 --pricing-file 是调用方点名的覆盖（例如拿一份候选价格文件试算），直接按文件加载，来源在输出里写明是 file；
	// 不带它时才走服务的取价规则。写库的命令不接受这个覆盖（见 parsePricingMatrixDeriveArgs）。
	if strings.TrimSpace(flagPath) != "" {
		info, err := svc.LoadOfflinePricing(flagPath)
		if err != nil {
			return nil, service.PricingDataInfo{}, fmt.Errorf("load price file: %w", err)
		}
		return svc, info, nil
	}
	resolve := func() (string, error) {
		if allowFallback {
			return resolvePricingReplayFile(flagPath, cfg)
		}
		return resolveDataDirPricingFile(flagPath, cfg)
	}
	info, err := svc.LoadServicePricing(ctx, dbSettingReader{db: db}, repository.NewPricingSnapshotRepository(db), resolve)
	if err != nil {
		return nil, service.PricingDataInfo{}, fmt.Errorf("load pricing data: %w", err)
	}
	return svc, info, nil
}

// describePricingSource 一行描述价格来源（表头用）。
func describePricingSource(info service.PricingDataInfo) string {
	sha := info.SHA256
	if len(sha) > 12 {
		sha = sha[:12]
	}
	if info.Source == service.PricingSourceSnapshot {
		return fmt.Sprintf("pricing_source=snapshot id=%d sha256=%s models=%d", info.SnapshotID, sha, info.Models)
	}
	return fmt.Sprintf("pricing_source=file path=%s sha256=%s models=%d", info.Path, sha, info.Models)
}
