package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	// pricingSnapshotPollInterval 是各实例轮询生效快照的间隔，pubsub 通知丢失时靠它收敛。
	pricingSnapshotPollInterval = 60 * time.Second
	// pricingSnapshotOpTimeout 是一次加载 / 固定操作里访问数据库的总时限。
	pricingSnapshotOpTimeout = 30 * time.Second
	// pricingSnapshotNotifyTimeout 是发布 pubsub 通知的时限。
	pricingSnapshotNotifyTimeout = 3 * time.Second
)

// pricingSnapshotState 是 PricingService 上与固定快照有关的全部状态，集中在一个结构里，
// 使 pricing_service.go 只需加一个字段。nil 表示没有接快照存储，行为与改动前完全一致。
type pricingSnapshotState struct {
	settings SettingRepository
	store    PricingSnapshotRepository
	pubsub   ChannelCachePubSub

	pollInterval time.Duration
	refreshCh    chan struct{}
	cancelWatch  context.CancelFunc

	// reloadMu 串行化「重载」与「固定」，两者都会读写下面的字段并访问数据库。
	reloadMu sync.Mutex

	// 以下字段受 PricingService.mu 保护：pinned 为真时，下载逻辑不得改写生效数据。
	pinned    bool
	activeID  int64
	activeSHA string
}

// ConfigureSnapshots 接上快照存储，必须在 Initialize 之前调用。任一依赖为 nil 时什么也不做，
// 价格服务保持纯 auto 行为（单元测试里大量 NewPricingService(cfg, nil) 的构造方式即如此）。
// pubsub 可以为 nil（没有 Redis 时只靠 60 秒轮询收敛）。
func (s *PricingService) ConfigureSnapshots(settings SettingRepository, store PricingSnapshotRepository, pubsub ChannelCachePubSub) {
	if s == nil || settings == nil || store == nil {
		return
	}
	s.snap = &pricingSnapshotState{
		settings:     settings,
		store:        store,
		pubsub:       pubsub,
		pollInterval: pricingSnapshotPollInterval,
		refreshCh:    make(chan struct{}, 1),
	}
}

// isPinned 报告当前是否处于 pinned 模式。
func (s *PricingService) isPinned() bool {
	if s == nil || s.snap == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap.pinned
}

// pinnedLocked 与 isPinned 相同，调用方必须已持有 s.mu。
func (s *PricingService) pinnedLocked() bool {
	return s.snap != nil && s.snap.pinned
}

// readSnapshotMode 读取 pricing_snapshot_mode。键不存在或为空按 auto；出现 auto、pinned 之外的值
// 视为配置损坏并报错，由调用方决定 fail-closed（启动）还是保留现状告警（运行时）。
func (s *PricingService) readSnapshotMode(ctx context.Context) (string, error) {
	value, err := s.snap.settings.GetValue(ctx, SettingKeyPricingSnapshotMode)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return PricingSnapshotModeAuto, nil
		}
		return "", fmt.Errorf("read %s: %w", SettingKeyPricingSnapshotMode, err)
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", PricingSnapshotModeAuto:
		return PricingSnapshotModeAuto, nil
	case PricingSnapshotModePinned:
		return PricingSnapshotModePinned, nil
	default:
		return "", fmt.Errorf("%s has unsupported value %q", SettingKeyPricingSnapshotMode, value)
	}
}

// refreshSnapshotState 对齐内存与数据库：读模式；pinned 时确保内存里是 status='active' 那份快照。
//
// 失败时不改动内存里的任何状态。启动时的调用方把错误当致命（fail-closed）；运行时的调用方
// （pubsub、轮询）保留当前快照并告警。auto 模式只读一次设置键，不访问快照表。
func (s *PricingService) refreshSnapshotState(ctx context.Context) error {
	st := s.snap
	st.reloadMu.Lock()
	defer st.reloadMu.Unlock()

	mode, err := s.readSnapshotMode(ctx)
	if err != nil {
		return err
	}
	if mode != PricingSnapshotModePinned {
		s.mu.Lock()
		wasPinned := st.pinned
		st.pinned = false
		st.activeID = 0
		st.activeSHA = ""
		s.mu.Unlock()
		if wasPinned {
			logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Snapshot mode switched to auto; in-memory data stays until the next remote sync")
		}
		return nil
	}

	meta, err := st.store.GetActiveMeta(ctx)
	if err != nil {
		if errors.Is(err, ErrPricingSnapshotNotFound) {
			return fmt.Errorf("mode is pinned but there is no active snapshot: %w", err)
		}
		return fmt.Errorf("load active snapshot: %w", err)
	}

	s.mu.RLock()
	unchanged := st.pinned && st.activeID == meta.ID && st.activeSHA == meta.ContentSHA256
	s.mu.RUnlock()
	if unchanged {
		return nil
	}

	payload, err := st.store.GetPayload(ctx, meta.ID)
	if err != nil {
		return fmt.Errorf("load snapshot %d payload: %w", meta.ID, err)
	}
	sum := sha256.Sum256(payload)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), meta.ContentSHA256) {
		return fmt.Errorf("snapshot %d payload does not match its content_sha256", meta.ID)
	}
	data, err := s.parsePricingData(payload)
	if err != nil {
		return fmt.Errorf("parse snapshot %d: %w", meta.ID, err)
	}

	loadedAt := meta.FetchedAt
	if meta.ApprovedAt != nil {
		loadedAt = *meta.ApprovedAt
	}
	s.mu.Lock()
	s.setPricingDataLocked(data)
	s.lastUpdated = loadedAt
	s.localHash = meta.ContentSHA256
	st.pinned = true
	st.activeID = meta.ID
	st.activeSHA = meta.ContentSHA256
	s.mu.Unlock()

	logger.LegacyPrintf("service.pricing", "[Pricing] Pinned snapshot %d (%s) loaded: %d models", meta.ID, meta.Label, len(data))
	return nil
}

// startSnapshots 在 Initialize 里调用：先对齐一次状态，失败即返回 ErrPricingSnapshotStartup（fail-closed），
// 成功后再起订阅与轮询。
func (s *PricingService) startSnapshots() error {
	st := s.snap
	ctx, cancel := context.WithTimeout(context.Background(), pricingSnapshotOpTimeout)
	err := s.refreshSnapshotState(ctx)
	cancel()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPricingSnapshotStartup, err)
	}

	watchCtx, cancelWatch := context.WithCancel(context.Background())
	st.cancelWatch = cancelWatch
	if st.pubsub != nil {
		st.pubsub.SubscribeUpdates(watchCtx, s.requestSnapshotRefresh)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(st.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-st.refreshCh:
			case <-s.stopCh:
				return
			}
			rctx, rcancel := context.WithTimeout(watchCtx, pricingSnapshotOpTimeout)
			if err := s.refreshSnapshotState(rctx); err != nil {
				// 运行时重载失败：保留内存里的当前快照，只告警；只有启动加载失败才 fail-closed。
				logger.LegacyPrintf("service.pricing", "[Pricing] WARN: snapshot reload failed, keeping the current in-memory data: %v", err)
			}
			rcancel()
		}
	}()
	return nil
}

// requestSnapshotRefresh 请求立即重载一次（合并重复请求，不阻塞通知回调）。
func (s *PricingService) requestSnapshotRefresh() {
	select {
	case s.snap.refreshCh <- struct{}{}:
	default:
	}
}

// stopSnapshots 在 Stop 里调用，结束订阅。
func (s *PricingService) stopSnapshots() {
	if s != nil && s.snap != nil && s.snap.cancelWatch != nil {
		s.snap.cancelWatch()
	}
}

func (s *PricingService) notifySnapshotChanged() {
	st := s.snap
	if st == nil || st.pubsub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pricingSnapshotNotifyTimeout)
	defer cancel()
	if err := st.pubsub.NotifyUpdate(ctx); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] WARN: failed to publish snapshot update: %v", err)
	}
}

// PricingSnapshotPinResult 是 PinCurrentPricing 的结果。
type PricingSnapshotPinResult struct {
	Snapshot *PricingSnapshotMeta
	// Created 为假表示已有一份内容完全相同的生效快照，复用了它。
	Created bool
}

// PinCurrentPricing 是启动引导（设计 2.6）：把此刻正在计费的价格数据（data_dir 下的价格文件）原样导入为
// bootstrap 快照并置 active，同时把模式切到 pinned。
//
// 零价格变化由比较保证：文件解析结果必须与内存里正在计费的数据逐字段相等，否则拒绝；导入的 payload
// 就是文件原文，所以之后从快照加载出来的数据与切换前的内存数据相同。
func (s *PricingService) PinCurrentPricing(ctx context.Context, adminID int64) (*PricingSnapshotPinResult, error) {
	if s == nil || s.snap == nil {
		return nil, ErrPricingSnapshotsUnavailable
	}
	st := s.snap
	st.reloadMu.Lock()
	defer st.reloadMu.Unlock()

	if s.isPinned() {
		return nil, ErrPricingAlreadyPinned
	}

	body, err := os.ReadFile(s.getPricingFilePath())
	if err != nil {
		return nil, fmt.Errorf("read pricing file: %w", err)
	}
	parsed, err := s.parsePricingData(body)
	if err != nil {
		return nil, fmt.Errorf("parse pricing file: %w", err)
	}

	// 比较与置位在同一把写锁里：此后进行中的下载在换入内存前会发现已 pinned 而放弃。
	s.mu.Lock()
	if !reflect.DeepEqual(parsed, s.pricingData) {
		s.mu.Unlock()
		return nil, ErrPricingBootstrapMismatch
	}
	st.pinned = true
	s.mu.Unlock()

	unpin := func() {
		s.mu.Lock()
		st.pinned = false
		s.mu.Unlock()
	}

	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])

	result := &PricingSnapshotPinResult{}
	existing, err := st.store.GetActiveMeta(ctx)
	switch {
	case err == nil && strings.EqualFold(existing.ContentSHA256, sha):
		result.Snapshot = existing
	case err == nil || errors.Is(err, ErrPricingSnapshotNotFound):
		meta, aerr := st.store.ActivateNew(ctx, NewPricingSnapshot{
			Label:         "bootstrap-" + time.Now().UTC().Format("2006-01-02") + "-" + sha[:8],
			Source:        PricingSnapshotSourceBootstrap,
			ContentSHA256: sha,
			ModelCount:    len(parsed),
			Payload:       body,
			FetchedBy:     optionalAdminID(adminID),
			ApprovedBy:    optionalAdminID(adminID),
			Note:          "bootstrap: imported the price file in use at the time of pinning",
		})
		if aerr != nil {
			unpin()
			return nil, fmt.Errorf("activate bootstrap snapshot: %w", aerr)
		}
		result.Snapshot = meta
		result.Created = true
	default:
		unpin()
		return nil, fmt.Errorf("read active snapshot: %w", err)
	}

	// 先有生效快照、后切模式：中途失败只会留下一份无人读取的快照，模式仍是 auto。
	if err := st.settings.Set(ctx, SettingKeyPricingSnapshotMode, PricingSnapshotModePinned); err != nil {
		unpin()
		return nil, fmt.Errorf("set %s: %w", SettingKeyPricingSnapshotMode, err)
	}

	s.mu.Lock()
	st.activeID = result.Snapshot.ID
	st.activeSHA = result.Snapshot.ContentSHA256
	s.localHash = result.Snapshot.ContentSHA256
	s.mu.Unlock()

	s.notifySnapshotChanged()
	logger.LegacyPrintf("service.pricing", "[Pricing] Pinned current pricing as snapshot %d (%s, created=%v)", result.Snapshot.ID, result.Snapshot.Label, result.Created)
	return result, nil
}

func optionalAdminID(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}
