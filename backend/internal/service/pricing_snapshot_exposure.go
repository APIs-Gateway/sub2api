package service

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// SnapshotExposureReader 是快照批准的保存时校验要读的状态：在 ExposureReader 之外，
// 还要列出全部白名单分组（批准影响所有分组，不像写入路径只看被改的那一个）。
type SnapshotExposureReader interface {
	ExposureReader
	// AllowlistGroupIDsTx 返回所有未删除的白名单分组 ID。
	AllowlistGroupIDsTx(ctx context.Context, exec MatrixExecutor) ([]int64, error)
}

// pricingSnapshotExposureChecker 用 ExposureGuard.CheckGroups 校验：假设 merged 价格数据已经生效
// （含内置兜底价），所有白名单分组里每一个 open 的精确单元格是否仍然「有价且非 0 元」。
// 判定口径与保存时校验（W6 设计 5.2、B1/B1′）完全一致，只是官方价换成 merged 数据。
type pricingSnapshotExposureChecker struct {
	reader   SnapshotExposureReader
	settings SettingRepository
	cfg      *config.Config
}

// NewSnapshotExposureChecker 创建快照批准的保存时校验。reader 为 nil 时返回 nil，批准按失败关闭处理。
func NewSnapshotExposureChecker(cfg *config.Config, reader SnapshotExposureReader, settings SettingRepository) SnapshotExposureChecker {
	if reader == nil {
		return nil
	}
	return &pricingSnapshotExposureChecker{reader: reader, settings: settings, cfg: cfg}
}

func (c *pricingSnapshotExposureChecker) CheckSnapshotApproval(ctx context.Context, exec MatrixExecutor, merged map[string]*LiteLLMModelPricing) error {
	billing := newSnapshotBilling(c.cfg, merged)
	guard := NewExposureGuard(c.reader, NewExposureValidator(billing, c.settings))
	ids, err := c.reader.AllowlistGroupIDsTx(ctx, exec)
	if err != nil {
		return fmt.Errorf("list allowlist groups: %w", err)
	}
	return guard.CheckGroups(ctx, exec, ids)
}
