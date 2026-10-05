package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	// pricingSnapshotAutoFetchInterval 是 pinned 模式下自动拉取候选的最短间隔（每个实例各自计时，
	// 多实例重复拉到同一份内容由 content_sha256 去重）。
	pricingSnapshotAutoFetchInterval = 24 * time.Hour
	// pricingSnapshotCandidateRetention 是未批准候选与已拒绝快照的保留时间。
	pricingSnapshotCandidateRetention = 30 * 24 * time.Hour
)

// PricingMaxDownloadBytes 是价格 JSON 下载的大小上限；超过就拒绝（LiteLLM 的文件约 1 MB）。
const PricingMaxDownloadBytes = 50 << 20

// ErrPricingDownloadTooLarge 表示下载的价格数据超过 PricingMaxDownloadBytes。
var ErrPricingDownloadTooLarge = errors.New("pricing download exceeds the size limit")

// PricingSnapshotFetchResult 是拉取候选的结果。
type PricingSnapshotFetchResult struct {
	// Unchanged 为真表示远程内容与生效快照完全相同，没有保存任何东西。
	Unchanged bool
	// Candidate 是保存（或已存在）的候选；Unchanged 时为 nil。
	Candidate *PricingSnapshotMeta
	// Created 为假表示同一内容的候选已经存在（按 content_sha256 去重），没有重复保存。
	Created bool
	// Active 是拉取时的生效快照。
	Active *PricingSnapshotMeta
}

// fetchRemotePricing 下载并校验远程价格 JSON（沿用现有的重试与 JSON 校验），不写文件、不改内存。
func (s *PricingService) fetchRemotePricing(parent context.Context) ([]byte, map[string]*LiteLLMModelPricing, string, error) {
	if s.cfg == nil || s.remoteClient == nil {
		return nil, nil, "", errors.New("remote pricing source is not configured")
	}
	ctx, cancel := context.WithTimeout(parent, pricingDownloadBudget)
	defer cancel()
	remoteURL, err := s.validatePricingURL(s.cfg.Pricing.RemoteURL)
	if err != nil {
		return nil, nil, "", err
	}
	body, err := s.fetchPricingJSONWithContext(ctx, remoteURL, nil)
	if err != nil {
		return nil, nil, "", fmt.Errorf("download failed: %w", err)
	}
	if len(body) > PricingMaxDownloadBytes {
		return nil, nil, "", fmt.Errorf("download failed: %w", ErrPricingDownloadTooLarge)
	}
	data, err := s.parsePricingData(body)
	if err != nil {
		return nil, nil, "", fmt.Errorf("parse pricing data: %w", err)
	}
	return body, data, remoteURL, nil
}

// FetchCandidateSnapshot 拉取远程价格并保存为 candidate 快照。拉取不影响账单：候选只有被批准后才会生效。
// 与生效快照内容相同时不保存；同一内容的候选已经存在时返回已有的那份。要求 pinned 模式。
func (s *PricingService) FetchCandidateSnapshot(ctx context.Context, fetchedBy *int64) (*PricingSnapshotFetchResult, error) {
	if s == nil || s.snap == nil {
		return nil, ErrPricingSnapshotsUnavailable
	}
	if !s.isPinned() {
		return nil, ErrPricingNotPinned
	}
	body, data, sourceURL, err := s.fetchRemotePricing(ctx)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])

	active, err := s.snap.store.GetActiveMeta(ctx)
	if err != nil {
		if errors.Is(err, ErrPricingSnapshotNotFound) {
			return nil, ErrPricingNotPinned
		}
		return nil, fmt.Errorf("read active snapshot: %w", err)
	}
	if strings.EqualFold(active.ContentSHA256, sha) {
		return &PricingSnapshotFetchResult{Unchanged: true, Active: active}, nil
	}
	// 批准之后生效快照是合成出来的（键有序、紧凑），字节哈希永远与远程原文对不上，
	// 所以再按解析后的数据比较：没有任何差异就算没变，不再存零差异的候选。
	if same, err := s.sameAsSnapshot(ctx, active, data); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] WARN: compare remote data with the active snapshot failed, saving as candidate: %v", err)
	} else if same {
		return &PricingSnapshotFetchResult{Unchanged: true, Active: active}, nil
	}
	candidate, created, err := s.snap.store.InsertCandidate(ctx, NewPricingSnapshot{
		Label:            "remote-" + time.Now().UTC().Format("2006-01-02") + "-" + sha[:8],
		Source:           PricingSnapshotSourceRemote,
		SourceURL:        sourceURL,
		ContentSHA256:    sha,
		ModelCount:       len(data),
		Payload:          body,
		ParentSnapshotID: &active.ID,
		FetchedBy:        fetchedBy,
	})
	if err != nil {
		return nil, fmt.Errorf("save candidate snapshot: %w", err)
	}
	return &PricingSnapshotFetchResult{Candidate: candidate, Created: created, Active: active}, nil
}

// sameAsSnapshot 报告 data 与快照里的价格数据是否没有任何差异（新增、移除、变化都没有）。
func (s *PricingService) sameAsSnapshot(ctx context.Context, meta *PricingSnapshotMeta, data map[string]*LiteLLMModelPricing) (bool, error) {
	payload, err := s.snap.store.GetPayload(ctx, meta.ID)
	if err != nil {
		return false, fmt.Errorf("load snapshot %d payload: %w", meta.ID, err)
	}
	base, err := s.parsePricingData(payload)
	if err != nil {
		return false, fmt.Errorf("parse snapshot %d: %w", meta.ID, err)
	}
	entries, _ := computePricingDiff(base, data)
	return len(entries) == 0, nil
}

// autoFetchSnapshotCandidate 由 pinned 模式下的定时同步调用：每天最多拉取一次候选（不改生效数据），
// 并顺带清理超过 30 天的未批准候选。失败只告警，下一个周期再试。
func (s *PricingService) autoFetchSnapshotCandidate(ctx context.Context) {
	st := s.snap
	now := time.Now()
	if last := st.lastAutoFetch.Load(); last != 0 && now.Sub(time.Unix(last, 0)) < pricingSnapshotAutoFetchInterval {
		return
	}
	res, err := s.FetchCandidateSnapshot(ctx, nil)
	if err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] WARN: automatic candidate fetch failed: %v", err)
		return
	}
	st.lastAutoFetch.Store(now.Unix())
	switch {
	case res.Unchanged:
		logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Remote pricing equals the pinned snapshot, no candidate saved")
	case res.Created:
		logger.LegacyPrintf("service.pricing", "[Pricing] New candidate snapshot %d (%s) saved, waiting for approval", res.Candidate.ID, res.Candidate.Label)
	}
	if n, err := st.store.DeleteExpiredCandidates(ctx, now.Add(-pricingSnapshotCandidateRetention)); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] WARN: expired candidate cleanup failed: %v", err)
	} else if n > 0 {
		logger.LegacyPrintf("service.pricing", "[Pricing] Removed %d expired pricing snapshots", n)
	}
}

// afterSnapshotApplied 在批准提交之后调用：本实例立即切到新的生效快照，并通知其他实例。
// 刷新失败只告警：数据库里的生效快照已经切换，各实例会在下一次轮询时收敛。
func (s *PricingService) afterSnapshotApplied() {
	if s == nil || s.snap == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pricingSnapshotOpTimeout)
	defer cancel()
	if err := s.refreshSnapshotState(ctx); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] WARN: reload after snapshot approval failed, other instances and the next poll will converge: %v", err)
	}
	s.notifySnapshotChanged()
}

// ActiveSnapshotID 返回本实例正在使用的生效快照 id；auto 模式或未接快照存储时为 0。
// 价格页与回退链的缓存键用它，批准之后缓存随之失效。
func (s *PricingService) ActiveSnapshotID() int64 {
	if s == nil || s.snap == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap.activeID
}
