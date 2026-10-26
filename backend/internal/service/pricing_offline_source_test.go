//go:build unit

package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 离线取价（LoadServicePricing）：pinned 下用生效快照而不是文件，auto 下用文件，失败不静默回退。

func pssOfflineResolver(t *testing.T, dir string, wantCalled bool) (func() (string, error), *int) {
	t.Helper()
	calls := 0
	return func() (string, error) {
		calls++
		if !wantCalled {
			t.Fatal("pinned 模式不应去找价格文件")
		}
		return filepath.Join(dir, "model_pricing.json"), nil
	}, &calls
}

func TestLoadServicePricing_PinnedUsesActiveSnapshotNotTheFile(t *testing.T) {
	dir := t.TempDir()
	pssWriteFile(t, dir, pssPayload("0.000001")) // 文件落后于生效快照
	store := newPSSStore()
	snapshot := pssPayload("0.000002")
	store.setActive(7, snapshot)

	resolve, calls := pssOfflineResolver(t, dir, false)
	svc := newPSSService(t, dir)
	info, err := svc.LoadServicePricing(context.Background(), newPSSSettings(PricingSnapshotModePinned), store, resolve)
	require.NoError(t, err)
	require.Zero(t, *calls)
	require.Equal(t, PricingSourceSnapshot, info.Source)
	require.Equal(t, int64(7), info.SnapshotID)
	require.Equal(t, pssSHA(snapshot), info.SHA256)
	require.Positive(t, info.Models)
	require.InDelta(t, 0.000002, pssInputCost(t, svc), 1e-12, "用快照里的价，不是文件里的")
}

func TestLoadServicePricing_PinnedFailuresDoNotFallBackToTheFile(t *testing.T) {
	dir := t.TempDir()
	pssWriteFile(t, dir, pssPayload("0.000001"))
	pinned := newPSSSettings(PricingSnapshotModePinned)

	t.Run("没有生效快照", func(t *testing.T) {
		resolve, _ := pssOfflineResolver(t, dir, false)
		_, err := newPSSService(t, dir).LoadServicePricing(context.Background(), pinned, newPSSStore(), resolve)
		require.ErrorIs(t, err, ErrPricingSnapshotNotFound)
		require.ErrorContains(t, err, "no active snapshot")
	})
	t.Run("读生效快照失败", func(t *testing.T) {
		store := newPSSStore()
		store.getActiveErr = errors.New("db down")
		resolve, _ := pssOfflineResolver(t, dir, false)
		_, err := newPSSService(t, dir).LoadServicePricing(context.Background(), pinned, store, resolve)
		require.ErrorContains(t, err, "db down")
	})
	t.Run("payload 与 sha 对不上", func(t *testing.T) {
		store := newPSSStore()
		store.setActive(7, pssPayload("0.000002"))
		store.payloads[7] = pssPayload("0.000009")
		resolve, _ := pssOfflineResolver(t, dir, false)
		_, err := newPSSService(t, dir).LoadServicePricing(context.Background(), pinned, store, resolve)
		require.ErrorContains(t, err, "content_sha256")
	})
	t.Run("payload 无法解析", func(t *testing.T) {
		store := newPSSStore()
		store.setActive(7, []byte("not json"))
		resolve, _ := pssOfflineResolver(t, dir, false)
		_, err := newPSSService(t, dir).LoadServicePricing(context.Background(), pinned, store, resolve)
		require.ErrorContains(t, err, "parse snapshot")
	})
}

func TestLoadServicePricing_AutoReadsTheFile(t *testing.T) {
	dir := t.TempDir()
	pssWriteFile(t, dir, pssPayload("0.000001"))
	store := newPSSStore()
	store.setActive(7, pssPayload("0.000002")) // auto 下生效快照不参与

	for name, settings := range map[string]PricingModeReader{
		"显式 auto": newPSSSettings(PricingSnapshotModeAuto),
		"没有设置键":   newPSSSettings(""),
		"没接设置读口":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			resolve, calls := pssOfflineResolver(t, dir, true)
			svc := newPSSService(t, dir)
			info, err := svc.LoadServicePricing(context.Background(), settings, store, resolve)
			require.NoError(t, err)
			require.Equal(t, 1, *calls)
			require.Equal(t, PricingSourceFile, info.Source)
			require.InDelta(t, 0.000001, pssInputCost(t, svc), 1e-12)
		})
	}
}

func TestLoadServicePricing_BadModeOrSettingErrorRefuses(t *testing.T) {
	dir := t.TempDir()
	pssWriteFile(t, dir, pssPayload("0.000001"))
	store := newPSSStore()
	store.setActive(7, pssPayload("0.000002"))

	resolve, _ := pssOfflineResolver(t, dir, false)
	_, err := newPSSService(t, dir).LoadServicePricing(context.Background(), newPSSSettings("bogus"), store, resolve)
	require.ErrorContains(t, err, "unsupported value")

	broken := newPSSSettings("")
	broken.getErr = errors.New("settings unavailable")
	_, err = newPSSService(t, dir).LoadServicePricing(context.Background(), broken, store, resolve)
	require.ErrorContains(t, err, "settings unavailable")
}

func TestLoadServicePricing_AutoFileResolveErrorIsReturned(t *testing.T) {
	boom := errors.New("no local price file found")
	_, err := newPSSService(t, t.TempDir()).LoadServicePricing(context.Background(), newPSSSettings(""), newPSSStore(),
		func() (string, error) { return "", boom })
	require.ErrorIs(t, err, boom)
}
