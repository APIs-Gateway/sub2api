//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// schedulerQueryCallKey identifies one distinct account-eligibility query
// issued by the scheduler snapshot rebuild path, mirroring the
// (groupID, platform) cache key used by schedulerAccountQueryCache, plus a
// mixed flag since mixed buckets query a different platform set and must
// never share a cache slot with single/forced buckets.
type schedulerQueryCallKey struct {
	groupID  int64
	platform string
	mixed    bool
}

// schedulerQueryReuseRepo counts how many times each distinct account query
// actually reaches the "database" so tests can assert reuse without caring
// about the accounts payload itself.
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

func newSchedulerQueryReuseService(repo AccountRepository, cache SchedulerCache) *SchedulerSnapshotService {
	return NewSchedulerSnapshotService(cache, nil, repo, nil, testConfig())
}

func TestSchedulerRebuildBatchReusesSingleAndForcedAccountQuery(t *testing.T) {
	repo := newSchedulerQueryReuseRepo()
	cache := newRetirementRaceCache()
	svc := newSchedulerQueryReuseService(repo, cache)
	buckets := []SchedulerBucket{
		{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeSingle},
		{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeForced},
	}

	require.NoError(t, svc.rebuildBuckets(context.Background(), buckets, "test"))
	require.Equal(t, 1, repo.callCount(schedulerQueryCallKey{groupID: 7, platform: PlatformOpenAI}))

	// Reusing the query result must not skip publishing either bucket's own
	// snapshot: both still get their own token-fenced SetSnapshot call.
	for _, bucket := range buckets {
		setAttempts, published := cache.counts(bucket)
		require.Equal(t, 1, setAttempts)
		require.Equal(t, 1, published)
	}
}

func TestSchedulerRebuildBatchKeepsMixedQueryIndependent(t *testing.T) {
	repo := newSchedulerQueryReuseRepo()
	cache := newRetirementRaceCache()
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
	svc := newSchedulerQueryReuseService(repo, newRetirementRaceCache())
	buckets := []SchedulerBucket{
		{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeSingle},
		{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeForced},
	}

	require.ErrorIs(t, svc.rebuildBuckets(context.Background(), buckets, "test"), wantErr)
	require.Equal(t, 2, repo.callCount(key), "a failed query must be retried for the next bucket, not poisoned into the cache")
}

func TestSchedulerRebuildBatchQueryCacheDoesNotCrossBatches(t *testing.T) {
	repo := newSchedulerQueryReuseRepo()
	svc := newSchedulerQueryReuseService(repo, newRetirementRaceCache())
	buckets := []SchedulerBucket{
		{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeSingle},
		{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeForced},
	}

	require.NoError(t, svc.rebuildBuckets(context.Background(), buckets, "first"))
	require.NoError(t, svc.rebuildBuckets(context.Background(), buckets, "second"))
	require.Equal(t, 2, repo.callCount(schedulerQueryCallKey{groupID: 7, platform: PlatformOpenAI}),
		"the query cache must not persist across separate rebuildBuckets batches")
}

// TestSchedulerRebuildBatchQueryReuseSkipsRetiredSiblingBucket covers the
// combination the original orphan PR #360 never tested: a bucket that never
// makes it past prepareBucketWriteTasks (retired) sharing an account-query
// key with a bucket that does get rebuilt. The cache is built from the
// batch's *prepared* tasks, not the raw bucket list, so the retired sibling
// must not be counted toward the shared query's refcount, and its retirement
// must be fully respected (no token captured, no snapshot published).
func TestSchedulerRebuildBatchQueryReuseSkipsRetiredSiblingBucket(t *testing.T) {
	single := SchedulerBucket{GroupID: 71, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: 71, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	repo := newSchedulerQueryReuseRepo()
	cache := newRetirementRaceCache()
	require.NoError(t, cache.RetireBucket(context.Background(), single))
	svc := newSchedulerQueryReuseService(repo, cache)

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "retired_sibling")
	require.NoError(t, err)

	key := schedulerQueryCallKey{groupID: 71, platform: PlatformOpenAI}
	require.Equal(t, 1, repo.callCount(key), "only the surviving bucket should query accounts")

	forcedAttempts, forcedPublished := cache.counts(forced)
	require.Equal(t, 1, forcedAttempts)
	require.Equal(t, 1, forcedPublished)

	singleAttempts, singlePublished := cache.counts(single)
	require.Zero(t, singleAttempts, "a retired bucket must never reach SetSnapshot")
	require.Zero(t, singlePublished)
}

// TestSchedulerRebuildBatchQueryReuseSurvivesSiblingFencing is the hard
// constraint from issue #750: reusing a query result must never let a bucket
// skip its own write-token/fencing check. Here the single bucket's query
// succeeds and gets cached, but its publish is independently fenced
// (simulating a concurrent retirement racing the batch); the forced bucket
// must still reuse the cached query result *and* still pass its own
// independent SetSnapshot fencing check.
func TestSchedulerRebuildBatchQueryReuseSurvivesSiblingFencing(t *testing.T) {
	single := SchedulerBucket{GroupID: 72, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: 72, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	repo := newSchedulerQueryReuseRepo()
	cache := newRetirementRaceCache()
	cache.setErrs[single.String()] = ErrSchedulerBucketWriteFenced
	svc := newSchedulerQueryReuseService(repo, cache)

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "fenced_sibling")
	require.NoError(t, err, "a fenced publish must not fail the whole non-strict batch")

	key := schedulerQueryCallKey{groupID: 72, platform: PlatformOpenAI}
	require.Equal(t, 1, repo.callCount(key), "the forced bucket must reuse the query the fenced single bucket already ran")

	singleAttempts, singlePublished := cache.counts(single)
	require.Equal(t, 1, singleAttempts)
	require.Zero(t, singlePublished, "the fenced bucket must not publish despite the shared query succeeding")

	forcedAttempts, forcedPublished := cache.counts(forced)
	require.Equal(t, 1, forcedAttempts)
	require.Equal(t, 1, forcedPublished, "the sibling bucket keeps its own token/fencing check independent of query reuse")
}
