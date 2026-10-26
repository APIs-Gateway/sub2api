//go:build unit

package service

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func routesSnapshotTestAPIKey(hasRoutes bool) *APIKey {
	groupID := int64(9)
	return &APIKey{
		ID:             1,
		UserID:         2,
		GroupID:        &groupID,
		Key:            "k-routes",
		Name:           "Routes Key",
		Status:         StatusActive,
		HasGroupRoutes: hasRoutes,
		User:           &User{ID: 2, Status: StatusActive, Role: RoleUser, Balance: 10, Concurrency: 3},
		Group: &Group{
			ID: groupID, Name: "openai", Platform: PlatformOpenAI, Status: StatusActive,
			SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1,
		},
	}
}

func TestAPIKeyAuthSnapshotVersionBumpedForGroupRoutes(t *testing.T) {
	require.Equal(t, 17, apiKeyAuthSnapshotVersion)
}

// 旧版本（v16，没有 has_group_routes）的缓存快照必须被整体丢弃，而不是被读成「没有链」。
func TestAPIKeyService_RejectsV16AuthSnapshotWithoutGroupRoutesFlag(t *testing.T) {
	svc := &APIKeyService{}

	raw := `{"version":16,"api_key_id":1,"user_id":2,"group_id":9,"name":"k","status":"active",
		"user":{"id":2,"status":"active","role":"user","balance":10,"concurrency":3},
		"group":{"id":9,"name":"g","platform":"openai","status":"active","subscription_type":"standard","rate_multiplier":1},
		"quota":0,"quota_used":0,"rate_limit_5h":0,"rate_limit_1d":0,"rate_limit_7d":0,"stable_priority_enabled":false}`
	var snap APIKeyAuthSnapshot
	require.NoError(t, json.Unmarshal([]byte(raw), &snap))
	require.Equal(t, 16, snap.Version)
	require.False(t, snap.HasGroupRoutes, "旧快照反序列化出来是零值，必须靠版本号丢弃")

	apiKey, ok, err := svc.applyAuthCacheEntry("k-v16", &APIKeyAuthCacheEntry{Snapshot: &snap})
	require.NoError(t, err)
	require.False(t, ok, "v16 快照应被丢弃，回源重建")
	require.Nil(t, apiKey)
}

func TestAPIKeyService_SnapshotRoundTrip_PreservesHasGroupRoutes(t *testing.T) {
	svc := NewAPIKeyService(nil, nil, nil, nil, nil, nil, &config.Config{})

	for _, want := range []bool{true, false} {
		snapshot := svc.snapshotFromAPIKey(context.Background(), routesSnapshotTestAPIKey(want))
		require.NotNil(t, snapshot)
		require.Equal(t, apiKeyAuthSnapshotVersion, snapshot.Version)
		require.Equal(t, want, snapshot.HasGroupRoutes)

		// 经 JSON（L2 缓存的序列化形态）往返。
		raw, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snapshot})
		require.NoError(t, err)
		var decoded APIKeyAuthCacheEntry
		require.NoError(t, json.Unmarshal(raw, &decoded))

		got, ok, err := svc.applyAuthCacheEntry("k-routes", &decoded)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, want, got.HasGroupRoutes)
	}
}

// 第 1 段加在 APIKey 上的 HomeGroupID / RouteSource 是运行时字段，不能进入快照，也不能经快照还原出来。
func TestAPIKeyAuthSnapshot_DoesNotCarryShadowKeyFields(t *testing.T) {
	svc := NewAPIKeyService(nil, nil, nil, nil, nil, nil, &config.Config{})
	home := int64(3)
	apiKey := routesSnapshotTestAPIKey(true)
	apiKey.HomeGroupID = &home
	apiKey.RouteSource = RouteSourceAdmin

	snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "home_group")
	require.NotContains(t, string(raw), "HomeGroupID")
	require.NotContains(t, string(raw), "route_source")
	require.NotContains(t, string(raw), "RouteSource")
	require.Contains(t, string(raw), `"has_group_routes":true`)

	restored := svc.snapshotToAPIKey("k-routes", snapshot)
	require.Nil(t, restored.HomeGroupID)
	require.Empty(t, restored.RouteSource)

	// APIKey 自身的 JSON 也不输出这些运行时字段与标志。
	rawKey, err := json.Marshal(apiKey)
	require.NoError(t, err)
	require.NotContains(t, string(rawKey), "HomeGroupID")
	require.NotContains(t, string(rawKey), "RouteSource")
	require.NotContains(t, string(rawKey), "HasGroupRoutes")
}

// S4：回退链的 EXISTS 查询失败（HasGroupRoutesUnknown）时，本次按无链处理，但不得写入 L1/L2 鉴权缓存；
// 正常结果照常写入。
func TestAPIKeyService_GetByKey_DoesNotCacheWhenGroupRoutesCheckFailed(t *testing.T) {
	cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L1Size: 100, L1TTLSeconds: 60, L2TTLSeconds: 60}}

	cache := &authCacheStub{}
	var calls int32
	repo := &authRepoStub{getByKeyForAuth: func(context.Context, string) (*APIKey, error) {
		atomic.AddInt32(&calls, 1)
		k := routesSnapshotTestAPIKey(false)
		k.HasGroupRoutesUnknown = true
		return k, nil
	}}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, cache, cfg)

	for i := 0; i < 2; i++ {
		got, err := svc.GetByKey(context.Background(), "k-routes")
		require.NoError(t, err)
		require.False(t, got.HasGroupRoutes, "本次按无链处理")
		if svc.authCacheL1 != nil {
			svc.authCacheL1.Wait() // ristretto 的写入是异步缓冲的，等它落地后再断言 L1 里确实没有
		}
	}
	require.EqualValues(t, 2, atomic.LoadInt32(&calls), "没有缓存，每次都回源重查（L1 也不能缓存）")
	require.Empty(t, cache.setAuthKeys, "EXISTS 失败的结果不得写入 L2")

	// 对照：查询成功时照常写缓存。
	cache2 := &authCacheStub{}
	repo2 := &authRepoStub{getByKeyForAuth: func(context.Context, string) (*APIKey, error) {
		return routesSnapshotTestAPIKey(true), nil
	}}
	svc2 := NewAPIKeyService(repo2, nil, nil, nil, nil, cache2, cfg)
	got, err := svc2.GetByKey(context.Background(), "k-routes")
	require.NoError(t, err)
	require.True(t, got.HasGroupRoutes)
	require.Len(t, cache2.setAuthKeys, 1)
}
