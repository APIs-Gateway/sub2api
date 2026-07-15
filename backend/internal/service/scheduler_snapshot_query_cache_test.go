//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type schedulerQueryCallKey struct {
	groupID  int64
	platform string
	mixed    bool
}

type schedulerQueryReuseRepo struct {
	AccountRepository

	mu       sync.Mutex
	calls    map[schedulerQueryCallKey]int
	errByKey map[schedulerQueryCallKey]error
}

func newSchedulerQueryReuseRepo() *schedulerQueryReuseRepo {
	return &schedulerQueryReuseRepo{
		calls:    make(map[schedulerQueryCallKey]int),
		errByKey: make(map[schedulerQueryCallKey]error),
	}
}

func (r *schedulerQueryReuseRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]Account, error) {
	return r.list(schedulerQueryCallKey{groupID: groupID, platform: platform})
}

func (r *schedulerQueryReuseRepo) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]Account, error) {
	platform := ""
	if len(platforms) > 0 {
		platform = platforms[0]
	}
	return r.list(schedulerQueryCallKey{groupID: groupID, platform: platform, mixed: true})
}

func (r *schedulerQueryReuseRepo) list(key schedulerQueryCallKey) ([]Account, error) {
	r.mu.Lock()
	r.calls[key]++
	err := r.errByKey[key]
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return []Account{{ID: 101, Platform: key.platform, Status: StatusActive, Schedulable: true}}, nil
}

func (r *schedulerQueryReuseRepo) callCount(key schedulerQueryCallKey) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[key]
}

type schedulerQueryReuseCache struct {
	SchedulerCache

	mu        sync.Mutex
	snapshots map[SchedulerBucket][]Account
}

func newSchedulerQueryReuseCache() *schedulerQueryReuseCache {
	return &schedulerQueryReuseCache{snapshots: make(map[SchedulerBucket][]Account)}
}

func (c *schedulerQueryReuseCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	return true, nil
}

func (c *schedulerQueryReuseCache) UnlockBucket(context.Context, SchedulerBucket) error {
	return nil
}

func (c *schedulerQueryReuseCache) SetSnapshot(_ context.Context, bucket SchedulerBucket, accounts []Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshots[bucket] = append([]Account(nil), accounts...)
	return nil
}

func (c *schedulerQueryReuseCache) snapshot(bucket SchedulerBucket) []Account {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Account(nil), c.snapshots[bucket]...)
}

func (c *schedulerQueryReuseCache) snapshotCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.snapshots)
}

func newSchedulerQueryReuseService(repo AccountRepository, cache SchedulerCache) *SchedulerSnapshotService {
	return NewSchedulerSnapshotService(cache, nil, repo, nil, &config.Config{RunMode: config.RunModeStandard})
}

func TestSchedulerRebuildBatchReusesSingleAndForcedAccountQuery(t *testing.T) {
	repo := newSchedulerQueryReuseRepo()
	cache := newSchedulerQueryReuseCache()
	svc := newSchedulerQueryReuseService(repo, cache)
	buckets := []SchedulerBucket{
		{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeSingle},
		{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeForced},
	}

	require.NoError(t, svc.rebuildBuckets(context.Background(), buckets, "test"))
	require.Equal(t, 1, repo.callCount(schedulerQueryCallKey{groupID: 7, platform: PlatformOpenAI}))
	require.Equal(t, 2, cache.snapshotCount())
	for _, bucket := range buckets {
		accounts := cache.snapshot(bucket)
		require.Len(t, accounts, 1)
		require.Equal(t, int64(101), accounts[0].ID)
	}
}

func TestSchedulerRebuildBatchKeepsMixedQueryIndependent(t *testing.T) {
	repo := newSchedulerQueryReuseRepo()
	cache := newSchedulerQueryReuseCache()
	svc := newSchedulerQueryReuseService(repo, cache)
	buckets := []SchedulerBucket{
		{GroupID: 7, Platform: PlatformAnthropic, Mode: SchedulerModeSingle},
		{GroupID: 7, Platform: PlatformAnthropic, Mode: SchedulerModeForced},
		{GroupID: 7, Platform: PlatformAnthropic, Mode: SchedulerModeMixed},
	}

	require.NoError(t, svc.rebuildBuckets(context.Background(), buckets, "test"))
	require.Equal(t, 1, repo.callCount(schedulerQueryCallKey{groupID: 7, platform: PlatformAnthropic}))
	require.Equal(t, 1, repo.callCount(schedulerQueryCallKey{groupID: 7, platform: PlatformAnthropic, mixed: true}))
}

func TestSchedulerRebuildBatchDoesNotCacheFailedAccountQuery(t *testing.T) {
	repo := newSchedulerQueryReuseRepo()
	wantErr := errors.New("account query failed")
	key := schedulerQueryCallKey{groupID: 7, platform: PlatformOpenAI}
	repo.errByKey[key] = wantErr
	svc := newSchedulerQueryReuseService(repo, newSchedulerQueryReuseCache())
	buckets := []SchedulerBucket{
		{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeSingle},
		{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeForced},
	}

	require.ErrorIs(t, svc.rebuildBuckets(context.Background(), buckets, "test"), wantErr)
	require.Equal(t, 2, repo.callCount(key), "a failed query must be retried for the next bucket")
}

func TestSchedulerRebuildBatchQueryCacheDoesNotCrossBatches(t *testing.T) {
	repo := newSchedulerQueryReuseRepo()
	svc := newSchedulerQueryReuseService(repo, newSchedulerQueryReuseCache())
	buckets := []SchedulerBucket{
		{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeSingle},
		{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeForced},
	}

	require.NoError(t, svc.rebuildBuckets(context.Background(), buckets, "first"))
	require.NoError(t, svc.rebuildBuckets(context.Background(), buckets, "second"))
	require.Equal(t, 2, repo.callCount(schedulerQueryCallKey{groupID: 7, platform: PlatformOpenAI}))
}
