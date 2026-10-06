package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// W6 PR6：回放需要的两个离线辅助。
//
//   - derivedMatrixSource：v2 一侧的矩阵快照来源之一，按渠道「当前」配置实时派生，而不是读库里已落库的行。
//     回放的目的是证明「渠道到矩阵的翻译」零差异；切到 v2 的那一刻会重新派生并冻结（设计 4.3），
//     所以实时派生的结果就是切换后 v2 实际读到的状态。库里还没有派生行的分组（钩子没跑过）也能回放；
//     库里的行与实时派生是否一致，另由 PricingReplayGroupBinding.StoredInSync 报告。
//   - LoadOfflinePricing：只读本地价格文件，不下载、不起定时更新，回放离线可跑。

// matrixMetaSource 是 derivedMatrixSource 需要的分组元信息读口（PricingMatrixRepository 的子集）。
type matrixMetaSource interface {
	GetGroupMeta(ctx context.Context, groupIDs []int64) (map[int64]DeriveGroup, error)
}

type derivedMatrixSource struct {
	meta    matrixMetaSource
	deriver PricingReplayDeriver
}

var _ MatrixSnapshotSource = (*derivedMatrixSource)(nil)

// NewDerivedMatrixSource 构造实时派生的矩阵快照来源。meta 通常是 PricingMatrixRepository，
// deriver 是 PricingDerivationService（ViewGroup 只读，不写任何东西）。
func NewDerivedMatrixSource(meta matrixMetaSource, deriver PricingReplayDeriver) MatrixSnapshotSource {
	return &derivedMatrixSource{meta: meta, deriver: deriver}
}

func (s *derivedMatrixSource) GetGroupMeta(ctx context.Context, groupIDs []int64) (map[int64]DeriveGroup, error) {
	return s.meta.GetGroupMeta(ctx, groupIDs)
}

func (s *derivedMatrixSource) LoadGroupSnapshots(ctx context.Context, groupIDs []int64) (map[int64]GroupStateSnapshot, error) {
	out := make(map[int64]GroupStateSnapshot, len(groupIDs))
	for _, id := range groupIDs {
		view, err := s.deriver.ViewGroup(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("derive group %d: %w", id, err)
		}
		out[id] = snapshotFromDerived(view.Derived)
	}
	return out, nil
}

// snapshotFromDerived 把派生结果还原成「已落库」的形态：id 用序号占位（快照编译只用它做稳定排序的最后一级），
// 阶段记为 v2（回放模拟的是切换之后的状态；matrixPolicy 的各个读口不看阶段）。
func snapshotFromDerived(d DerivedGroupState) GroupStateSnapshot {
	snap := GroupStateSnapshot{
		Config: &StoredGroupConfig{GroupID: d.GroupID, MatrixGroupConfig: d.Config, PricingStage: PricingStageV2},
	}
	snap.Cells = make([]StoredMatrixCell, len(d.Cells))
	for i, c := range d.Cells {
		snap.Cells[i] = StoredMatrixCell{ID: int64(i + 1), GroupID: d.GroupID, MatrixCell: c}
	}
	snap.Rules = make([]StoredMatrixCostRule, len(d.CostRules))
	for i, r := range d.CostRules {
		snap.Rules[i] = StoredMatrixCostRule{
			ID: int64(i + 1), ScopeGroupID: d.GroupID, Source: MatrixSourceLegacyDerived, MatrixCostRule: r,
		}
	}
	return snap
}

// 离线取价的来源。
const (
	PricingSourceFile     = "file"
	PricingSourceSnapshot = "snapshot"
)

// PricingDataInfo 一份离线加载的价格数据的摘要，写进回放汇总（价格数据变了，回放结果也就不能直接沿用）。
type PricingDataInfo struct {
	// Path 文件来源是文件路径；快照来源是 "snapshot-<id>"。
	Path   string
	SHA256 string
	Models int
	// Source 是 PricingSourceFile 或 PricingSourceSnapshot；SnapshotID 只在快照来源时有值。
	Source     string
	SnapshotID int64
}

// LoadOfflinePricing 从本地文件加载价格数据，不触网、不启动定时更新。
func (s *PricingService) LoadOfflinePricing(path string) (PricingDataInfo, error) {
	if err := s.loadPricingData(path); err != nil {
		return PricingDataInfo{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return PricingDataInfo{Path: path, SHA256: s.localHash, Models: len(s.pricingData), Source: PricingSourceFile}, nil
}

// PricingModeReader 读 pricing_snapshot_mode 所在的设置表（SettingRepository 的子集）。
type PricingModeReader interface {
	GetValue(ctx context.Context, key string) (string, error)
}

// LoadServicePricing 离线命令（pricing-replay、pricing-matrix derive）取官方价的唯一入口，规则与服务进程一致：
//
//   - pricing_snapshot_mode 为 pinned：加载 status='active' 的快照（校验 content_sha256 后解析），
//     不看价格文件（pinned 下文件不再更新，必然落后于服务内存）；没有生效快照、读不出、校验或解析失败一律报错，
//     不退回文件；
//   - 其余（auto）：调用 resolveFile 取本地价格文件路径再加载，与服务默认来源一致。
//
// settings 或 store 为 nil 视为调用方没有接快照存储，按 auto 处理。
func (s *PricingService) LoadServicePricing(ctx context.Context, settings PricingModeReader, store PricingSnapshotRepository, resolveFile func() (string, error)) (PricingDataInfo, error) {
	mode := PricingSnapshotModeAuto
	if settings != nil && store != nil {
		value, err := settings.GetValue(ctx, SettingKeyPricingSnapshotMode)
		switch {
		case err == nil:
			if mode, err = normalizePricingSnapshotMode(value); err != nil {
				return PricingDataInfo{}, err
			}
		case errors.Is(err, ErrSettingNotFound):
		default:
			return PricingDataInfo{}, fmt.Errorf("read %s: %w", SettingKeyPricingSnapshotMode, err)
		}
	}
	if mode == PricingSnapshotModePinned {
		return s.loadActiveSnapshotOffline(ctx, store)
	}
	path, err := resolveFile()
	if err != nil {
		return PricingDataInfo{}, err
	}
	return s.LoadOfflinePricing(path)
}

// loadActiveSnapshotOffline 与 refreshSnapshotState 同样地取生效快照：元数据、payload、sha 校验、解析。
func (s *PricingService) loadActiveSnapshotOffline(ctx context.Context, store PricingSnapshotRepository) (PricingDataInfo, error) {
	meta, err := store.GetActiveMeta(ctx)
	if err != nil {
		if errors.Is(err, ErrPricingSnapshotNotFound) {
			return PricingDataInfo{}, fmt.Errorf("pricing mode is pinned but there is no active snapshot: %w", err)
		}
		return PricingDataInfo{}, fmt.Errorf("load active snapshot: %w", err)
	}
	payload, err := store.GetPayload(ctx, meta.ID)
	if err != nil {
		return PricingDataInfo{}, fmt.Errorf("load snapshot %d payload: %w", meta.ID, err)
	}
	sum := sha256.Sum256(payload)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), meta.ContentSHA256) {
		return PricingDataInfo{}, fmt.Errorf("snapshot %d payload does not match its content_sha256", meta.ID)
	}
	data, err := s.parsePricingData(payload)
	if err != nil {
		return PricingDataInfo{}, fmt.Errorf("parse snapshot %d: %w", meta.ID, err)
	}
	loadedAt := meta.FetchedAt
	if meta.ApprovedAt != nil {
		loadedAt = *meta.ApprovedAt
	}
	if loadedAt.IsZero() {
		loadedAt = time.Now()
	}
	s.mu.Lock()
	s.setPricingDataLocked(data)
	s.lastUpdated = loadedAt
	s.localHash = meta.ContentSHA256
	s.mu.Unlock()
	return PricingDataInfo{
		Path: fmt.Sprintf("snapshot-%d", meta.ID), SHA256: meta.ContentSHA256, Models: len(data),
		Source: PricingSourceSnapshot, SnapshotID: meta.ID,
	}, nil
}
