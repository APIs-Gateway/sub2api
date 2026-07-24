//go:build unit

package repository

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCacheUpdateLastUsedUsesSideKeyWithoutRewritingPayloads(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	initial := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	account := service.Account{
		ID:         9201,
		Platform:   service.PlatformOpenAI,
		Type:       service.AccountTypeOAuth,
		LastUsedAt: &initial,
		Credentials: map[string]any{
			"access_token": "large-token",
		},
	}
	require.NoError(t, cache.SetAccount(ctx, &account))

	id := strconv.FormatInt(account.ID, 10)
	fullBefore, err := cache.rdb.Get(ctx, schedulerAccountKey(id)).Bytes()
	require.NoError(t, err)
	metaBefore, err := cache.rdb.Get(ctx, schedulerAccountMetaKey(id)).Bytes()
	require.NoError(t, err)

	latest := initial.Add(37 * time.Second)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{account.ID: latest}))

	fullAfter, err := cache.rdb.Get(ctx, schedulerAccountKey(id)).Bytes()
	require.NoError(t, err)
	metaAfter, err := cache.rdb.Get(ctx, schedulerAccountMetaKey(id)).Bytes()
	require.NoError(t, err)
	require.Equal(t, fullBefore, fullAfter)
	require.Equal(t, metaBefore, metaAfter)
	require.Equal(t, strconv.FormatInt(latest.UnixMilli(), 10), cache.rdb.Get(ctx, schedulerLastUsedKey(id)).Val())

	cached, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.NotNil(t, cached.LastUsedAt)
	require.Equal(t, latest, *cached.LastUsedAt)
}

func TestSchedulerCacheLastUsedSideKeyIsMonotonicAndRequiresAccount(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	account := service.Account{ID: 9202, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	require.NoError(t, cache.SetAccount(ctx, &account))

	newer := time.Now().UTC().Truncate(time.Millisecond)
	older := newer.Add(-time.Minute)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{account.ID: newer}))
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{account.ID: older}))

	id := strconv.FormatInt(account.ID, 10)
	require.Equal(t, strconv.FormatInt(newer.UnixMilli(), 10), cache.rdb.Get(ctx, schedulerLastUsedKey(id)).Val())
	cached, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Equal(t, newer, *cached.LastUsedAt)

	const missingID int64 = 9299
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{missingID: newer}))
	_, err = cache.rdb.Get(ctx, schedulerLastUsedKey(strconv.FormatInt(missingID, 10))).Result()
	require.ErrorIs(t, err, redis.Nil)

	require.NoError(t, cache.DeleteAccount(ctx, account.ID))
	_, err = cache.rdb.Get(ctx, schedulerLastUsedKey(id)).Result()
	require.ErrorIs(t, err, redis.Nil)
}

func TestSchedulerCacheLastUsedSideKeySurvivesStaleAccountWrites(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	bucket := service.SchedulerBucket{
		GroupID:  10,
		Platform: service.PlatformOpenAI,
		Mode:     service.SchedulerModeSingle,
	}
	embedded := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	latest := embedded.Add(30 * time.Second)
	account := service.Account{
		ID:         9203,
		Platform:   service.PlatformOpenAI,
		Type:       service.AccountTypeOAuth,
		Schedulable: true,
		LastUsedAt: &embedded,
	}
	require.NoError(t, cache.SetAccount(ctx, &account))
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{account.ID: latest}))

	require.NoError(t, cache.SetAccount(ctx, &account))
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	cached, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Equal(t, latest, *cached.LastUsedAt)
	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.Equal(t, latest, *snapshot[0].LastUsedAt)
}

func TestSchedulerCacheLastUsedSideKeyFallsBackToNewerEmbeddedValue(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	embedded := time.Now().UTC().Truncate(time.Millisecond)
	account := service.Account{
		ID:         9204,
		Platform:   service.PlatformOpenAI,
		Type:       service.AccountTypeOAuth,
		LastUsedAt: &embedded,
	}
	require.NoError(t, cache.SetAccount(ctx, &account))

	id := strconv.FormatInt(account.ID, 10)
	require.NoError(t, cache.rdb.Set(ctx, schedulerLastUsedKey(id), embedded.Add(-time.Hour).UnixMilli(), 0).Err())
	cached, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Equal(t, embedded, *cached.LastUsedAt)
}

func TestSchedulerCacheUpdateLastUsedChunksLargeBatches(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	total := schedulerLastUsedUpdateChunkSize + 1
	accounts := make([]service.Account, 0, total)
	updates := make(map[int64]time.Time, total)
	base := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < total; i++ {
		id := int64(9300 + i)
		accounts = append(accounts, service.Account{ID: id, Platform: service.PlatformOpenAI})
		updates[id] = base.Add(time.Duration(i) * time.Millisecond)
	}

	require.NoError(t, cache.writeAccounts(ctx, accounts))
	require.NoError(t, cache.UpdateLastUsed(ctx, updates))
	for id, usedAt := range updates {
		key := schedulerLastUsedKey(strconv.FormatInt(id, 10))
		require.Equal(t, strconv.FormatInt(usedAt.UnixMilli(), 10), cache.rdb.Get(ctx, key).Val())
	}
}

func TestSchedulerCacheLastUsedSkipsInvalidUpdatesAndNonPositiveDeletion(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	usedAt := time.Now().UTC().Truncate(time.Millisecond)

	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{0: usedAt, -1: usedAt}))
	require.NoError(t, cache.DeleteAccount(ctx, 0))
	require.NoError(t, cache.DeleteAccount(ctx, -1))

	_, err := cache.rdb.Get(ctx, schedulerLastUsedKey("0")).Result()
	require.ErrorIs(t, err, redis.Nil)
	_, err = cache.rdb.Get(ctx, schedulerLastUsedKey("-1")).Result()
	require.ErrorIs(t, err, redis.Nil)
}

func TestSchedulerCacheGetSnapshotRejectsMissingAndInvalidLastUsedEntries(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)

	missingBucket := service.SchedulerBucket{GroupID: 9205, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	missingAccount := service.Account{ID: 9205, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	missingToken, err := cache.CaptureBucketWriteToken(ctx, missingBucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, missingBucket, missingToken, []service.Account{missingAccount}))
	require.NoError(t, cache.rdb.Del(ctx, schedulerAccountMetaKey(strconv.FormatInt(missingAccount.ID, 10))).Err())

	snapshot, hit, err := cache.GetSnapshot(ctx, missingBucket)
	require.NoError(t, err)
	require.False(t, hit)
	require.Nil(t, snapshot)

	invalidBucket := service.SchedulerBucket{GroupID: 9206, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	invalidAccount := service.Account{ID: 9206, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	invalidToken, err := cache.CaptureBucketWriteToken(ctx, invalidBucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, invalidBucket, invalidToken, []service.Account{invalidAccount}))
	require.NoError(t, cache.rdb.Set(ctx, schedulerLastUsedKey(strconv.FormatInt(invalidAccount.ID, 10)), "invalid", 0).Err())

	_, hit, err = cache.GetSnapshot(ctx, invalidBucket)
	require.Error(t, err)
	require.False(t, hit)
	require.ErrorContains(t, err, "invalid last_used cache value")
}

func TestSchedulerCacheLastUsedHelpersHandleCacheRepresentations(t *testing.T) {
	base := time.UnixMilli(1_700_000_000_000).UTC()
	newer := base.Add(time.Second)

	account := &service.Account{}
	require.NoError(t, applySchedulerLastUsed(account, strconv.FormatInt(base.UnixMilli(), 10)))
	require.Equal(t, base, *account.LastUsedAt)
	require.NoError(t, applySchedulerLastUsed(account, []byte(strconv.FormatInt(newer.UnixMilli(), 10))))
	require.Equal(t, newer, *account.LastUsedAt)
	require.NoError(t, applySchedulerLastUsed(account, strconv.FormatInt(base.UnixMilli(), 10)))
	require.Equal(t, newer, *account.LastUsedAt)
	require.NoError(t, applySchedulerLastUsed(account, nil))
	require.NoError(t, applySchedulerLastUsed(nil, strconv.FormatInt(base.UnixMilli(), 10)))

	err := applySchedulerLastUsed(account, 1)
	require.ErrorContains(t, err, "unexpected last_used cache type")
	err = applySchedulerLastUsed(account, "invalid")
	require.ErrorContains(t, err, "invalid last_used cache value")

	millis, err := schedulerLastUsedMillis(base)
	require.NoError(t, err)
	require.Equal(t, base.UnixMilli(), millis)
}

func TestSchedulerCacheGetAccountHandlesMissingAccountAndAbsentSideKey(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)

	missing, err := cache.GetAccount(ctx, 9207)
	require.NoError(t, err)
	require.Nil(t, missing)

	embedded := time.Now().UTC().Truncate(time.Millisecond)
	account := service.Account{ID: 9208, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, LastUsedAt: &embedded}
	require.NoError(t, cache.SetAccount(ctx, &account))

	cached, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Equal(t, embedded, *cached.LastUsedAt)
}
