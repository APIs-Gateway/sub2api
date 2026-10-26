//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 鉴权快照里缓存了余额、Key 额度与限额、分组倍率等「额度类」数值，单位随 CREDIT_CURRENCY 变。
// 快照版本号叠加币种后，切换（或回滚）时旧单位的快照必须被整体丢弃、回源重建。

func TestAPIKeyAuthSnapshotVersion_FollowsCreditUnit(t *testing.T) {
	// 基础版本号没变：USD 模式（默认）下版本号仍是 17，部署新代码不会让缓存失效。
	require.Equal(t, 17, apiKeyAuthSnapshotVersion)

	t.Run("usd", func(t *testing.T) {
		t.Cleanup(SetCreditUnitForTest(DefaultCreditUnit()))
		require.Equal(t, apiKeyAuthSnapshotVersion, currentAPIKeyAuthSnapshotVersion())
	})
	t.Run("cny", func(t *testing.T) {
		t.Cleanup(SetCreditUnitForTest(CreditUnit{Currency: CreditCurrencyCNY, LegacyDivisor: 13}))
		require.Equal(t, apiKeyAuthSnapshotVersion+1000, currentAPIKeyAuthSnapshotVersion())
		require.NotEqual(t, apiKeyAuthSnapshotVersion, currentAPIKeyAuthSnapshotVersion())
	})
}

func TestAPIKeyService_AuthSnapshotRejectedAcrossCreditUnits(t *testing.T) {
	ctx := context.Background()
	svc := NewAPIKeyService(nil, nil, nil, nil, nil, nil, &config.Config{})
	usdUnit := DefaultCreditUnit()
	cnyUnit := CreditUnit{Currency: CreditCurrencyCNY, LegacyDivisor: 13}

	// 经 JSON 往返，模拟写进 Redis（L2）再读回来。
	roundTrip := func(t *testing.T, snap *APIKeyAuthSnapshot) *APIKeyAuthCacheEntry {
		t.Helper()
		raw, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snap})
		require.NoError(t, err)
		var decoded APIKeyAuthCacheEntry
		require.NoError(t, json.Unmarshal(raw, &decoded))
		return &decoded
	}

	// 分别在两种模式下各写一份快照。
	restore := SetCreditUnitForTest(usdUnit)
	usdSnap := svc.snapshotFromAPIKey(ctx, routesSnapshotTestAPIKey(false))
	restore()
	restore = SetCreditUnitForTest(cnyUnit)
	cnySnap := svc.snapshotFromAPIKey(ctx, routesSnapshotTestAPIKey(false))
	restore()
	require.NotNil(t, usdSnap)
	require.NotNil(t, cnySnap)
	require.Equal(t, 17, usdSnap.Version)
	require.Equal(t, 1017, cnySnap.Version)

	// USD 进程：接受 USD 快照，丢弃 CNY 快照。
	t.Run("usd_process", func(t *testing.T) {
		t.Cleanup(SetCreditUnitForTest(usdUnit))
		_, ok, err := svc.applyAuthCacheEntry("k", roundTrip(t, usdSnap))
		require.NoError(t, err)
		require.True(t, ok, "同单位的快照应命中")
		apiKey, ok, err := svc.applyAuthCacheEntry("k", roundTrip(t, cnySnap))
		require.NoError(t, err)
		require.False(t, ok, "回滚到 USD 后，CNY 快照必须丢弃")
		require.Nil(t, apiKey)
	})

	// CNY 进程：接受 CNY 快照，丢弃 USD 快照（切换前缓存的旧单位余额）。
	t.Run("cny_process", func(t *testing.T) {
		t.Cleanup(SetCreditUnitForTest(cnyUnit))
		_, ok, err := svc.applyAuthCacheEntry("k", roundTrip(t, cnySnap))
		require.NoError(t, err)
		require.True(t, ok, "同单位的快照应命中")
		apiKey, ok, err := svc.applyAuthCacheEntry("k", roundTrip(t, usdSnap))
		require.NoError(t, err)
		require.False(t, ok, "切换到 CNY 后，旧的 USD 快照必须丢弃")
		require.Nil(t, apiKey)
	})
}
