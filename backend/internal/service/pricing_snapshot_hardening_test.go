//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func init() {
	pricingSnapshotStartRetryDelay = time.Millisecond
}

// pssFlakySettings 前 failFirst 次读取返回瞬时错误，之后正常。
type pssFlakySettings struct {
	*pssSettings
	failFirst int32
	calls     atomic.Int32
}

func (f *pssFlakySettings) GetValue(ctx context.Context, key string) (string, error) {
	if f.calls.Add(1) <= f.failFirst {
		return "", errors.New("connection reset")
	}
	return f.pssSettings.GetValue(ctx, key)
}

func TestPricingSnapshot_StartupRetriesTransientModeReadErrors(t *testing.T) {
	store := newPSSStore()
	store.setActive(5, pssPayload("0.000001"))

	// 前两次失败、第三次成功：正常启动。
	flaky := &pssFlakySettings{pssSettings: newPSSSettings("pinned"), failFirst: 2}
	svc := newPSSService(t, t.TempDir())
	svc.ConfigureSnapshots(flaky, store, nil)
	require.NoError(t, svc.Initialize())
	defer svc.Stop()
	require.True(t, svc.isPinned())
	require.EqualValues(t, 3, flaky.calls.Load())

	// 一直失败：重试用完后 fail-closed。
	always := &pssFlakySettings{pssSettings: newPSSSettings("pinned"), failFirst: 100}
	svc2 := newPSSService(t, t.TempDir())
	svc2.ConfigureSnapshots(always, store, nil)
	require.ErrorIs(t, svc2.Initialize(), ErrPricingSnapshotStartup)
	require.EqualValues(t, pricingSnapshotStartAttempts, always.calls.Load())
}

func TestPricingSnapshot_StartupDoesNotRetryPermanentErrors(t *testing.T) {
	store := newPSSStore()
	store.setActive(5, pssPayload("0.000001"))

	corrupt := &pssFlakySettings{pssSettings: newPSSSettings("pinnd")}
	svc := newPSSService(t, t.TempDir())
	svc.ConfigureSnapshots(corrupt, store, nil)
	require.ErrorIs(t, svc.Initialize(), ErrPricingSnapshotStartup)
	require.EqualValues(t, 1, corrupt.calls.Load(), "模式值损坏不是瞬时错误，不重试")

	noActive := newPSSStore()
	settings := &pssFlakySettings{pssSettings: newPSSSettings("pinned")}
	svc2 := newPSSService(t, t.TempDir())
	svc2.ConfigureSnapshots(settings, noActive, nil)
	require.ErrorIs(t, svc2.Initialize(), ErrPricingSnapshotStartup)
	require.EqualValues(t, 1, settings.calls.Load(), "没有生效快照不重试")
	require.EqualValues(t, 1, noActive.metaCalls)

	mismatch := newPSSStore()
	mismatch.setActive(1, pssPayload("0.000001"))
	mismatch.payloads[1] = pssPayload("0.000002")
	svc3 := newPSSService(t, t.TempDir())
	svc3.ConfigureSnapshots(newPSSSettings("pinned"), mismatch, nil)
	require.ErrorIs(t, svc3.Initialize(), ErrPricingSnapshotStartup)
	require.EqualValues(t, 1, mismatch.metaCalls, "哈希对不上不重试")
}

func TestPricingSnapshot_LoadPricingDataSkipsWhenPinnedMeanwhile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "fallback.json")
	require.NoError(t, os.WriteFile(file, pssPayload("0.000009"), 0o644))

	svc := newPSSService(t, dir)
	svc.ConfigureSnapshots(newPSSSettings(""), newPSSStore(), nil)

	// 与启动竞态等价：快照已经装入并置 pinned，随后兜底文件的加载不能覆盖它。
	snapData, err := svc.parsePricingData(pssPayload("0.000001"))
	require.NoError(t, err)
	svc.mu.Lock()
	svc.setPricingDataLocked(snapData)
	svc.snap.pinned = true
	svc.mu.Unlock()

	require.NoError(t, svc.loadPricingData(file))
	require.InDelta(t, 0.000001, pssInputCost(t, svc), 1e-12)

	// auto 下照常加载。
	svc.mu.Lock()
	svc.snap.pinned = false
	svc.mu.Unlock()
	require.NoError(t, svc.loadPricingData(file))
	require.InDelta(t, 0.000009, pssInputCost(t, svc), 1e-12)
}

func pssCompact(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, json.Compact(&buf, payload))
	return buf.Bytes()
}

func newPSSFetchService(t *testing.T, store *pssStore, remote func() []byte) *PricingService {
	t.Helper()
	client := pricingRemoteClientStub{
		hash: func(context.Context) (string, error) { return "", errors.New("no hash") },
		json: func(context.Context) ([]byte, error) { return remote(), nil },
	}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		DataDir: t.TempDir(), RemoteURL: "https://example.com/pricing.json", UpdateIntervalHours: 24 * 365,
	}}, client)
	svc.ConfigureSnapshots(newPSSSettings("pinned"), store, nil)
	require.NoError(t, svc.Initialize())
	t.Cleanup(svc.Stop)
	return svc
}

func TestFetchCandidateSnapshot_SameDataWithDifferentBytesIsUnchanged(t *testing.T) {
	// 批准之后生效快照是合成的（紧凑字节），远程原文是缩进格式：字节哈希不同，数据相同。
	active := pssCompact(t, pssPayload("0.000001"))
	remote := pssPayload("0.000001")
	require.NotEqual(t, pssSHA(active), pssSHA(remote))

	store := newPSSStore()
	store.setActive(1, active)
	svc := newPSSFetchService(t, store, func() []byte { return remote })

	res, err := svc.FetchCandidateSnapshot(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, res.Unchanged)
	require.Empty(t, store.inserted, "内容没变就不存候选")

	// 数据真的变了仍然存候选。
	changed := newPSSFetchService(t, store, func() []byte { return pssPayload("0.000002") })
	res, err = changed.FetchCandidateSnapshot(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, res.Created)
	require.Len(t, store.inserted, 1)

	// 读生效快照失败：保守地按候选保存，不吞掉变化。
	store2 := newPSSStore()
	store2.setActive(1, active)
	svc2 := newPSSFetchService(t, store2, func() []byte { return remote })
	store2.mu.Lock()
	store2.getPayloadErr = errors.New("db down")
	store2.mu.Unlock()
	res, err = svc2.FetchCandidateSnapshot(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, res.Created)
}

func TestFetchCandidateSnapshot_RejectsOversizedDownload(t *testing.T) {
	store := newPSSStore()
	store.setActive(1, pssPayload("0.000001"))
	huge := append([]byte(`{"gpt-5.1": {"input_cost_per_token": 1}}`), bytes.Repeat([]byte(" "), PricingMaxDownloadBytes)...)
	svc := newPSSFetchService(t, store, func() []byte { return huge })

	_, err := svc.FetchCandidateSnapshot(context.Background(), nil)
	require.ErrorIs(t, err, ErrPricingDownloadTooLarge)
	require.Empty(t, store.inserted)
}

func TestPricingSnapshotAdmin_OverviewHidesCandidatesWithoutDifferences(t *testing.T) {
	ctx := context.Background()
	store := newPSSStore()
	store.setActive(1, pssCompact(t, pssPayload("0.000001")))
	store.addCandidate(2, pssPayload("0.000001")) // 数据相同、字节不同
	store.addCandidate(3, pssPayload("0.000002")) // 真的有变化
	svc := newPSSService(t, t.TempDir())
	svc.ConfigureSnapshots(newPSSSettings("pinned"), store, nil)
	require.NoError(t, svc.Initialize())
	defer svc.Stop()

	out, err := NewPricingSnapshotAdminService(svc, store, nil, nil).Overview(ctx)
	require.NoError(t, err)
	require.Len(t, out.PendingCandidates, 1)
	require.Equal(t, int64(3), out.PendingCandidates[0].ID)
}
