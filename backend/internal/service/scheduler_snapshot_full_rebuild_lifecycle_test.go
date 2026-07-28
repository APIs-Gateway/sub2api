//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type groupLifecycleTestCache struct {
	*retirementRaceCache

	stateMu sync.Mutex

	leaseHeld       bool
	lease           SchedulerGroupLifecycleLease
	leaseSequence   int
	leaseBusy       bool
	leaseAcquireErr error
	leaseReleaseErr error
	acquireCalls    int
	releaseCalls    int
	acquireTTL      time.Duration
	acquireDeadline bool
	releaseDeadline bool
	releaseCtxErr   error

	listErr   error
	listCalls int

	retireCalls  []SchedulerBucket
	reopenTokens []SchedulerBucketWriteToken
	retireHeld   []bool
	reopenHeld   []bool
	retireErr    error
	retireErrAt  int
	reopenErr    error
	reopenErrAt  int

	bucketLockBusy bool
	bucketLockErr  error
	bucketLockTTLs []time.Duration
	unlockCalls    int
	setErr         error
}

func newGroupLifecycleTestCache(buckets ...SchedulerBucket) *groupLifecycleTestCache {
	return &groupLifecycleTestCache{retirementRaceCache: newRetirementRaceCache(buckets...)}
}

func (c *groupLifecycleTestCache) TryAcquireGroupLifecycleLease(ctx context.Context, groupID int64, ttl time.Duration) (SchedulerGroupLifecycleLease, bool, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.acquireCalls++
	c.acquireTTL = ttl
	_, c.acquireDeadline = ctx.Deadline()
	if c.leaseAcquireErr != nil {
		return SchedulerGroupLifecycleLease{}, false, c.leaseAcquireErr
	}
	if c.leaseBusy || c.leaseHeld {
		return SchedulerGroupLifecycleLease{}, false, nil
	}
	c.leaseSequence++
	c.lease = SchedulerGroupLifecycleLease{GroupID: groupID, OwnerToken: fmt.Sprintf("owner-%d", c.leaseSequence)}
	c.leaseHeld = true
	return c.lease, true, nil
}

func (c *groupLifecycleTestCache) ReleaseGroupLifecycleLease(ctx context.Context, lease SchedulerGroupLifecycleLease) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.releaseCalls++
	_, c.releaseDeadline = ctx.Deadline()
	c.releaseCtxErr = ctx.Err()
	if c.leaseReleaseErr != nil {
		return c.leaseReleaseErr
	}
	if !c.leaseHeld || lease != c.lease {
		return ErrSchedulerGroupLifecycleLeaseLost
	}
	c.leaseHeld = false
	return nil
}

func (c *groupLifecycleTestCache) RetireBucket(ctx context.Context, bucket SchedulerBucket) error {
	c.stateMu.Lock()
	c.retireCalls = append(c.retireCalls, bucket)
	c.retireHeld = append(c.retireHeld, c.leaseHeld)
	held := c.leaseHeld
	call := len(c.retireCalls)
	err := c.retireErr
	errAt := c.retireErrAt
	c.stateMu.Unlock()
	if !held {
		return errors.New("retire called outside group lifecycle lease")
	}
	if err != nil && (errAt <= 0 || call == errAt) {
		return err
	}
	return c.retirementRaceCache.RetireBucket(ctx, bucket)
}

func (c *groupLifecycleTestCache) ReopenBucket(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	if err := ctx.Err(); err != nil {
		return SchedulerBucketWriteToken{}, err
	}
	c.stateMu.Lock()
	c.reopenHeld = append(c.reopenHeld, c.leaseHeld)
	held := c.leaseHeld
	call := len(c.reopenHeld)
	reopenErr := c.reopenErr
	reopenErrAt := c.reopenErrAt
	c.stateMu.Unlock()
	if bucket.GroupID > 0 && !held {
		return SchedulerBucketWriteToken{}, errors.New("reopen called outside group lifecycle lease")
	}
	if reopenErr != nil && (reopenErrAt <= 0 || call == reopenErrAt) {
		return SchedulerBucketWriteToken{}, reopenErr
	}
	token, err := c.retirementRaceCache.ReopenBucket(ctx, bucket)
	if err != nil {
		return SchedulerBucketWriteToken{}, err
	}
	c.stateMu.Lock()
	c.reopenTokens = append(c.reopenTokens, token)
	c.stateMu.Unlock()
	return token, nil
}

func (c *groupLifecycleTestCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	c.stateMu.Lock()
	c.listCalls++
	err := c.listErr
	c.stateMu.Unlock()
	if err != nil {
		return nil, err
	}
	return c.retirementRaceCache.ListBuckets(ctx)
}

func (c *groupLifecycleTestCache) TryLockBucket(_ context.Context, _ SchedulerBucket, ttl time.Duration) (bool, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.bucketLockTTLs = append(c.bucketLockTTLs, ttl)
	if c.bucketLockErr != nil {
		return false, c.bucketLockErr
	}
	return !c.bucketLockBusy, nil
}

func (c *groupLifecycleTestCache) UnlockBucket(context.Context, SchedulerBucket) error {
	c.stateMu.Lock()
	c.unlockCalls++
	c.stateMu.Unlock()
	return nil
}

func (c *groupLifecycleTestCache) SetSnapshot(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, accounts []Account) error {
	c.stateMu.Lock()
	err := c.setErr
	c.stateMu.Unlock()
	if err != nil {
		return err
	}
	return c.retirementRaceCache.SetSnapshot(ctx, bucket, token, accounts)
}

func (c *groupLifecycleTestCache) lifecycleCounts() (acquires, releases, listCalls int) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.acquireCalls, c.releaseCalls, c.listCalls
}

func (c *groupLifecycleTestCache) retiredBuckets() []SchedulerBucket {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]SchedulerBucket(nil), c.retireCalls...)
}

func (c *groupLifecycleTestCache) tokens() []SchedulerBucketWriteToken {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]SchedulerBucketWriteToken(nil), c.reopenTokens...)
}

func (c *groupLifecycleTestCache) leaseHeldAndTokenCount() (bool, int) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.leaseHeld, len(c.reopenTokens)
}

func (c *groupLifecycleTestCache) lifecycleMutationLeaseStates() (retire, reopen []bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]bool(nil), c.retireHeld...), append([]bool(nil), c.reopenHeld...)
}

func bucketStrings(buckets []SchedulerBucket) map[string]struct{} {
	out := make(map[string]struct{}, len(buckets))
	for _, bucket := range buckets {
		out[bucket.String()] = struct{}{}
	}
	return out
}

type fullRebuildLifecycleCache struct {
	*groupLifecycleTestCache

	mu              sync.Mutex
	captureAttempts []SchedulerBucket
	captureErrors   map[string]error
	lockBusyOnce    map[string]bool
	watermark       int64
	watermarkWrites []int64
}

func newFullRebuildLifecycleCache(buckets ...SchedulerBucket) *fullRebuildLifecycleCache {
	return &fullRebuildLifecycleCache{
		groupLifecycleTestCache: newGroupLifecycleTestCache(buckets...),
		captureErrors:           make(map[string]error),
		lockBusyOnce:            make(map[string]bool),
	}
}

func (c *fullRebuildLifecycleCache) CaptureBucketWriteToken(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	c.captureAttempts = append(c.captureAttempts, bucket)
	err := c.captureErrors[bucket.String()]
	c.mu.Unlock()
	if err != nil {
		return SchedulerBucketWriteToken{}, err
	}
	return c.retirementRaceCache.CaptureBucketWriteToken(ctx, bucket)
}

func (c *fullRebuildLifecycleCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	buckets, err := c.groupLifecycleTestCache.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	c.retirementRaceCache.mu.Lock()
	defer c.retirementRaceCache.mu.Unlock()
	registered := make([]SchedulerBucket, 0, len(buckets))
	for _, bucket := range buckets {
		if !c.retirementRaceCache.retired[bucket.String()] {
			registered = append(registered, bucket)
		}
	}
	return registered, nil
}

func (c *fullRebuildLifecycleCache) TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	busy := c.lockBusyOnce[bucket.String()]
	if busy {
		delete(c.lockBusyOnce, bucket.String())
	}
	c.mu.Unlock()
	if busy {
		return false, nil
	}
	return c.groupLifecycleTestCache.TryLockBucket(ctx, bucket, ttl)
}

func (c *fullRebuildLifecycleCache) GetOutboxWatermark(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.watermark, nil
}

func (c *fullRebuildLifecycleCache) SetOutboxWatermark(_ context.Context, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watermark = id
	c.watermarkWrites = append(c.watermarkWrites, id)
	return nil
}

func (c *fullRebuildLifecycleCache) captureAttemptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.captureAttempts)
}

func (c *fullRebuildLifecycleCache) currentWatermark() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.watermark
}

func (c *fullRebuildLifecycleCache) totalSetAttempts() int {
	c.retirementRaceCache.mu.Lock()
	defer c.retirementRaceCache.mu.Unlock()
	var total int
	for _, attempts := range c.retirementRaceCache.setAttempts {
		total += attempts
	}
	return total
}

type fullRebuildLifecycleGroupRepo struct {
	GroupRepository

	mu              sync.Mutex
	activeIDs       []int64
	activeIDsErr    error
	listActiveErr   error
	fresh           map[int64]*Group
	freshErr        map[int64]error
	activeIDCalls   int
	listActiveCalls int
	freshCalls      []int64
}

func (r *fullRebuildLifecycleGroupRepo) ListActiveIDs(context.Context) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeIDCalls++
	return append([]int64(nil), r.activeIDs...), r.activeIDsErr
}

func (r *fullRebuildLifecycleGroupRepo) ListActive(context.Context) ([]Group, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listActiveCalls++
	return nil, r.listActiveErr
}

func (r *fullRebuildLifecycleGroupRepo) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.freshCalls = append(r.freshCalls, id)
	if err := r.freshErr[id]; err != nil {
		return nil, err
	}
	group := r.fresh[id]
	if group == nil {
		return nil, ErrGroupNotFound
	}
	copyGroup := *group
	return &copyGroup, nil
}

func (r *fullRebuildLifecycleGroupRepo) stats() (activeIDs, listActive int, fresh []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeIDCalls, r.listActiveCalls, append([]int64(nil), r.freshCalls...)
}

type fullRebuildFallbackGroupRepo struct {
	GroupRepository

	mu        sync.Mutex
	groups    []Group
	err       error
	listCalls int
}

func (r *fullRebuildFallbackGroupRepo) ListActive(context.Context) ([]Group, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls++
	return append([]Group(nil), r.groups...), r.err
}

type fullRebuildAccountCall struct {
	groupID  int64
	platform string
}

type fullRebuildAccountRepo struct {
	AccountRepository

	mu          sync.Mutex
	calls       []fullRebuildAccountCall
	beforeFirst func()
	once        sync.Once
}

func (r *fullRebuildAccountRepo) record(groupID int64, platform string) ([]Account, error) {
	r.mu.Lock()
	r.calls = append(r.calls, fullRebuildAccountCall{groupID: groupID, platform: platform})
	beforeFirst := r.beforeFirst
	r.mu.Unlock()
	if beforeFirst != nil {
		r.once.Do(beforeFirst)
	}
	return []Account{{ID: 1, Platform: platform, Status: StatusActive, Schedulable: true}}, nil
}

func (r *fullRebuildAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]Account, error) {
	return r.record(groupID, platform)
}

func (r *fullRebuildAccountRepo) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]Account, error) {
	return r.record(groupID, firstPlatform(platforms))
}

func (r *fullRebuildAccountRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]Account, error) {
	return r.record(0, platform)
}

func (r *fullRebuildAccountRepo) ListSchedulableUngroupedByPlatforms(_ context.Context, platforms []string) ([]Account, error) {
	return r.record(0, firstPlatform(platforms))
}

func (r *fullRebuildAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]Account, error) {
	return r.record(0, platform)
}

func (r *fullRebuildAccountRepo) ListSchedulableByPlatforms(_ context.Context, platforms []string) ([]Account, error) {
	return r.record(0, firstPlatform(platforms))
}

func (r *fullRebuildAccountRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *fullRebuildAccountRepo) groupCallCount(groupID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int
	for _, call := range r.calls {
		if call.groupID == groupID {
			count++
		}
	}
	return count
}

func firstPlatform(platforms []string) string {
	if len(platforms) == 0 {
		return ""
	}
	return platforms[0]
}

func newFullRebuildLifecycleService(
	cache SchedulerCache,
	outbox SchedulerOutboxRepository,
	accounts AccountRepository,
	groups GroupRepository,
	runMode string,
) *SchedulerSnapshotService {
	return NewSchedulerSnapshotService(cache, outbox, accounts, groups, &config.Config{RunMode: runMode})
}

func TestSchedulerFullRebuildActiveTombstoneDoesNotBlockFollowingGroupEvent(t *testing.T) {
	const groupID int64 = 101
	canonical := schedulerBucketsForGroup(groupID)
	cache := newFullRebuildLifecycleCache()
	require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[0]))
	groups := &fullRebuildLifecycleGroupRepo{
		activeIDs: []int64{groupID},
		fresh: map[int64]*Group{
			groupID: {ID: groupID, Status: StatusActive, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	accounts := &fullRebuildAccountRepo{}
	outbox := &outboxCleanupRepo{events: []SchedulerOutboxEvent{
		{ID: 1, EventType: SchedulerOutboxEventFullRebuild},
		{ID: 2, EventType: SchedulerOutboxEventGroupChanged, GroupID: ptrInt64(groupID)},
	}}
	svc := newFullRebuildLifecycleService(cache, outbox, accounts, groups, config.RunModeStandard)

	svc.pollOutbox()

	require.Equal(t, int64(2), cache.currentWatermark())
	cache.mu.Lock()
	require.Equal(t, []int64{2}, cache.watermarkWrites)
	cache.mu.Unlock()
	activeCalls, fallbackCalls, freshCalls := groups.stats()
	require.Equal(t, 1, activeCalls)
	require.Zero(t, fallbackCalls)
	require.Equal(t, []int64{groupID, groupID}, freshCalls)
	require.Len(t, cache.tokens(), 20, "full rebuild and the following group event must each run fresh authority")
	_, reopenHeld := cache.lifecycleMutationLeaseStates()
	require.Len(t, reopenHeld, 20)
	for _, held := range reopenHeld {
		require.True(t, held)
	}
	require.Equal(t, 30, accounts.callCount())
}

func TestSchedulerFullRebuildReopensRetiredGroupZeroCanonical(t *testing.T) {
	canonical := schedulerCanonicalBuckets(0)
	cache := newFullRebuildLifecycleCache()
	require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[0]))
	cache.captureErrors[canonical[0].String()] = ErrSchedulerBucketRetired
	groups := &fullRebuildLifecycleGroupRepo{
		fresh:    make(map[int64]*Group),
		freshErr: make(map[int64]error),
	}
	accounts := &fullRebuildAccountRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeStandard)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Equal(t, 10, cache.captureAttemptCount())
	require.Equal(t, 10, accounts.callCount())
	tokens := cache.tokens()
	require.Len(t, tokens, 1)
	require.Equal(t, canonical[0], tokens[0].Bucket)
	_, reopenHeld := cache.lifecycleMutationLeaseStates()
	require.Equal(t, []bool{false}, reopenHeld, "group0 recovery must not require a group lifecycle lease")
	_, published := cache.counts(canonical[0])
	require.Equal(t, 1, published)
}

func TestSchedulerFullRebuildGlobalReadErrorsFailBeforeMutationOrDB(t *testing.T) {
	t.Run("list buckets", func(t *testing.T) {
		cache := newFullRebuildLifecycleCache()
		cache.listErr = errors.New("registry failed")
		groups := &fullRebuildLifecycleGroupRepo{fresh: make(map[int64]*Group), freshErr: make(map[int64]error)}
		accounts := &fullRebuildAccountRepo{}
		svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeStandard)

		err := svc.rebuildFullSnapshot(context.Background(), "test")
		require.ErrorIs(t, err, cache.listErr)
		activeCalls, fallbackCalls, freshCalls := groups.stats()
		require.Zero(t, activeCalls)
		require.Zero(t, fallbackCalls)
		require.Empty(t, freshCalls)
		requireFullRebuildNoMutationOrDB(t, cache, accounts)
	})

	t.Run("list active ids without fallback", func(t *testing.T) {
		cache := newFullRebuildLifecycleCache()
		groups := &fullRebuildLifecycleGroupRepo{
			activeIDsErr:  errors.New("active ids failed"),
			listActiveErr: errors.New("fallback must not run"),
			fresh:         make(map[int64]*Group),
			freshErr:      make(map[int64]error),
		}
		accounts := &fullRebuildAccountRepo{}
		svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeStandard)

		err := svc.rebuildFullSnapshot(context.Background(), "test")
		require.ErrorIs(t, err, groups.activeIDsErr)
		activeCalls, fallbackCalls, freshCalls := groups.stats()
		require.Equal(t, 1, activeCalls)
		require.Zero(t, fallbackCalls)
		require.Empty(t, freshCalls)
		requireFullRebuildNoMutationOrDB(t, cache, accounts)
	})

	t.Run("list active fallback", func(t *testing.T) {
		cache := newFullRebuildLifecycleCache()
		groups := &fullRebuildFallbackGroupRepo{err: errors.New("active groups failed")}
		accounts := &fullRebuildAccountRepo{}
		svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeStandard)

		err := svc.rebuildFullSnapshot(context.Background(), "test")
		require.ErrorIs(t, err, groups.err)
		groups.mu.Lock()
		require.Equal(t, 1, groups.listCalls)
		groups.mu.Unlock()
		requireFullRebuildNoMutationOrDB(t, cache, accounts)
	})
}

func TestSchedulerFullRebuildFreshActivePreparesEveryTokenBeforeFirstDB(t *testing.T) {
	const groupID int64 = 102
	historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "unknown"}
	cache := newFullRebuildLifecycleCache(historical)
	groups := &fullRebuildLifecycleGroupRepo{
		fresh: map[int64]*Group{
			groupID: {ID: groupID, Status: StatusActive, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	accounts := &fullRebuildAccountRepo{}
	var capturesAtFirstDB int
	accounts.beforeFirst = func() {
		capturesAtFirstDB = cache.captureAttemptCount()
		held, reopenCount := cache.leaseHeldAndTokenCount()
		require.False(t, held)
		require.Equal(t, 10, reopenCount)
		require.Equal(t, 11, capturesAtFirstDB, "C(0) and the historical bucket must be captured before DB")
	}
	svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeStandard)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Equal(t, capturesAtFirstDB, cache.captureAttemptCount())
	require.Equal(t, 21, accounts.callCount())
	require.Equal(t, 11, accounts.groupCallCount(groupID))
	_, historicalPublished := cache.counts(historical)
	require.Equal(t, 1, historicalPublished)
	activeCalls, fallbackCalls, freshCalls := groups.stats()
	require.Equal(t, 1, activeCalls)
	require.Zero(t, fallbackCalls)
	require.Equal(t, []int64{groupID}, freshCalls)
	_, _, listCalls := cache.lifecycleCounts()
	require.Equal(t, 1, listCalls, "known Rg must prevent a second registry read")
}

func TestSchedulerFullRebuildOrdinaryCaptureErrorReturnsBeforeFirstDB(t *testing.T) {
	first := SchedulerBucket{GroupID: 0, Platform: "legacy-a", Mode: "unknown"}
	last := SchedulerBucket{GroupID: 0, Platform: "legacy-b", Mode: "unknown"}
	cache := newFullRebuildLifecycleCache(first, last)
	wantErr := errors.New("capture failed")
	cache.captureErrors[last.String()] = wantErr
	groups := &fullRebuildLifecycleGroupRepo{fresh: make(map[int64]*Group), freshErr: make(map[int64]error)}
	accounts := &fullRebuildAccountRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeStandard)

	err := svc.rebuildFullSnapshot(context.Background(), "test")
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 12, cache.captureAttemptCount(), "all canonical and ordinary captures must be attempted before returning")
	require.Zero(t, accounts.callCount())
	require.Zero(t, cache.totalSetAttempts())
}

func TestSchedulerFullRebuildPreservesGroupZeroActiveHistoricalAndInvalidRegistryBuckets(t *testing.T) {
	const groupID int64 = 103
	groupZeroHistorical := SchedulerBucket{GroupID: 0, Platform: "legacy-zero", Mode: "unknown"}
	activeHistorical := SchedulerBucket{GroupID: groupID, Platform: "legacy-active", Mode: "unknown"}
	activeForced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	activeMixed := SchedulerBucket{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeMixed}
	invalidHistorical := SchedulerBucket{GroupID: -7, Platform: "legacy-invalid", Mode: "unknown"}
	cache := newFullRebuildLifecycleCache(groupZeroHistorical, activeHistorical, activeForced, activeMixed, invalidHistorical)
	groups := &fullRebuildFallbackGroupRepo{groups: []Group{{ID: groupID, Status: StatusActive}}}
	accounts := &fullRebuildAccountRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeStandard)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Equal(t, 23, cache.captureAttemptCount())
	require.Equal(t, 23, accounts.callCount())
	groups.mu.Lock()
	require.Equal(t, 1, groups.listCalls)
	groups.mu.Unlock()
	require.Empty(t, cache.retiredBuckets())
	require.Empty(t, cache.tokens())
	for _, bucket := range []SchedulerBucket{groupZeroHistorical, activeHistorical, activeForced, activeMixed, invalidHistorical} {
		_, published := cache.counts(bucket)
		require.Equal(t, 1, published, bucket.String())
	}
}

func TestSchedulerFullRebuildActiveTombstoneFreshInactiveOrMissingFiltersAllGroupTasks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group *Group
	}{
		{name: "inactive", group: &Group{ID: 104, Status: StatusDisabled, Hydrated: true}},
		{name: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const groupID int64 = 104
			canonical := schedulerBucketsForGroup(groupID)
			historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "unknown"}
			cache := newFullRebuildLifecycleCache(historical)
			require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[5]))
			groups := &fullRebuildLifecycleGroupRepo{
				activeIDs: []int64{groupID},
				fresh:     make(map[int64]*Group),
				freshErr:  make(map[int64]error),
			}
			if tc.group != nil {
				groups.fresh[groupID] = tc.group
			}
			accounts := &fullRebuildAccountRepo{}
			svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeStandard)

			require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
			require.Zero(t, accounts.groupCallCount(groupID))
			require.Equal(t, 10, accounts.groupCallCount(0))
			require.Empty(t, cache.tokens())
			require.Equal(t, bucketStrings(append(canonical, historical)), bucketStrings(cache.retiredBuckets()))
			for _, bucket := range append(canonical, historical) {
				attempts, published := cache.counts(bucket)
				require.Zero(t, attempts, bucket.String())
				require.Zero(t, published, bucket.String())
			}
			_, _, listCalls := cache.lifecycleCounts()
			require.Equal(t, 1, listCalls)
		})
	}
}

func TestSchedulerFullRebuildStaleCandidatesAreSortedAndNeverReopenedAcrossRounds(t *testing.T) {
	historical := []SchedulerBucket{
		{GroupID: 3, Platform: "legacy", Mode: "unknown"},
		{GroupID: 1, Platform: "legacy", Mode: "unknown"},
		{GroupID: 2, Platform: "legacy", Mode: "unknown"},
	}
	cache := newFullRebuildLifecycleCache(historical...)
	groups := &fullRebuildLifecycleGroupRepo{
		fresh: map[int64]*Group{
			1: {ID: 1, Status: StatusDisabled, Hydrated: true},
			3: {ID: 3, Status: StatusDisabled, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	accounts := &fullRebuildAccountRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeStandard)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "first"))
	retireCallsAfterFirst := len(cache.retiredBuckets())
	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "second"))
	_, _, freshCalls := groups.stats()
	require.Equal(t, []int64{1, 2, 3}, freshCalls)
	require.Equal(t, retireCallsAfterFirst, len(cache.retiredBuckets()))
	require.Empty(t, cache.tokens())
	require.Zero(t, accounts.groupCallCount(1))
	require.Zero(t, accounts.groupCallCount(2))
	require.Zero(t, accounts.groupCallCount(3))
	_, _, listCalls := cache.lifecycleCounts()
	require.Equal(t, 2, listCalls, "each round must use only its one global registry snapshot")
	for _, bucket := range historical {
		require.Contains(t, bucketStrings(cache.retiredBuckets()), bucket.String())
	}
}

func TestSchedulerFullRebuildPartialLifecycleFailureReturnsBeforeDBAndRetries(t *testing.T) {
	historical := []SchedulerBucket{
		{GroupID: 1, Platform: "legacy", Mode: "unknown"},
		{GroupID: 2, Platform: "legacy", Mode: "unknown"},
		{GroupID: 3, Platform: "legacy", Mode: "unknown"},
	}
	wantErr := errors.New("fresh group query failed")
	cache := newFullRebuildLifecycleCache(historical...)
	groups := &fullRebuildLifecycleGroupRepo{
		fresh: map[int64]*Group{
			1: {ID: 1, Status: StatusDisabled, Hydrated: true},
			3: {ID: 3, Status: StatusDisabled, Hydrated: true},
		},
		freshErr: map[int64]error{2: wantErr},
	}
	accounts := &fullRebuildAccountRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeStandard)

	err := svc.triggerFullRebuild("first")
	require.ErrorIs(t, err, wantErr)
	_, _, freshCalls := groups.stats()
	require.Equal(t, []int64{1, 2}, freshCalls)
	require.Zero(t, accounts.callCount())
	require.Zero(t, cache.totalSetAttempts())
	require.Equal(t, 11, len(cache.retiredBuckets()))

	groups.mu.Lock()
	delete(groups.freshErr, 2)
	groups.fresh[2] = &Group{ID: 2, Status: StatusDisabled, Hydrated: true}
	groups.mu.Unlock()
	require.NoError(t, svc.triggerFullRebuild("retry"))
	_, _, freshCalls = groups.stats()
	require.Equal(t, []int64{1, 2, 2, 3}, freshCalls)
	require.Equal(t, 33, len(cache.retiredBuckets()))
	require.Equal(t, 10, accounts.callCount())
	require.Empty(t, cache.tokens())
}

func TestSchedulerFullRebuildActiveTombstoneLazyRecoveryDiscardsPartialCaptureTasks(t *testing.T) {
	const groupID int64 = 105
	canonical := schedulerBucketsForGroup(groupID)
	historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "unknown"}
	cache := newFullRebuildLifecycleCache(canonical[0], canonical[4], historical)
	require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[5]))
	groups := &fullRebuildLifecycleGroupRepo{
		activeIDs: []int64{groupID},
		fresh: map[int64]*Group{
			groupID: {ID: groupID, Status: StatusActive, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	accounts := &fullRebuildAccountRepo{}
	var capturesAtFirstDB int
	accounts.beforeFirst = func() {
		capturesAtFirstDB = cache.captureAttemptCount()
		held, reopenCount := cache.leaseHeldAndTokenCount()
		require.False(t, held)
		require.Equal(t, 10, reopenCount)
		require.Equal(t, 17, capturesAtFirstDB)
	}
	svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeStandard)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Equal(t, capturesAtFirstDB, cache.captureAttemptCount())
	require.Equal(t, 21, accounts.callCount())
	for _, bucket := range canonical {
		attempts, published := cache.counts(bucket)
		require.Equal(t, 1, attempts, "discarded pre-recovery tokens must never publish: %s", bucket.String())
		require.Equal(t, 1, published, bucket.String())
	}
	_, published := cache.counts(historical)
	require.Equal(t, 1, published)
	_, _, listCalls := cache.lifecycleCounts()
	require.Equal(t, 1, listCalls)
}

func TestSchedulerFullRebuildSimpleModePreservesRegistryWithoutLifecycleAuthority(t *testing.T) {
	registered := []SchedulerBucket{
		{GroupID: 0, Platform: "legacy-zero", Mode: "unknown"},
		{GroupID: 106, Platform: "legacy-positive", Mode: "unknown"},
		{GroupID: -8, Platform: "legacy-negative", Mode: "unknown"},
	}
	cache := newFullRebuildLifecycleCache(registered...)
	groups := &fullRebuildLifecycleGroupRepo{
		activeIDsErr: errors.New("simple mode must not query groups"),
		fresh:        make(map[int64]*Group),
		freshErr:     make(map[int64]error),
	}
	accounts := &fullRebuildAccountRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeSimple)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	activeCalls, fallbackCalls, freshCalls := groups.stats()
	require.Zero(t, activeCalls)
	require.Zero(t, fallbackCalls)
	require.Empty(t, freshCalls)
	require.Equal(t, 13, cache.captureAttemptCount())
	require.Equal(t, 13, accounts.callCount())
	require.Equal(t, 13, accounts.groupCallCount(0))
	require.Empty(t, cache.retiredBuckets())
	require.Empty(t, cache.tokens())
	for _, bucket := range registered {
		_, published := cache.counts(bucket)
		require.Equal(t, 1, published, bucket.String())
	}
}

func TestSchedulerFullRebuildFreshReopenLockBusyRetriesWithoutBlockingOrdinaryTasks(t *testing.T) {
	const groupID int64 = 107
	canonical := schedulerBucketsForGroup(groupID)
	cache := newFullRebuildLifecycleCache()
	require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[0]))
	cache.lockBusyOnce[canonical[0].String()] = true
	groups := &fullRebuildLifecycleGroupRepo{
		activeIDs: []int64{groupID},
		fresh: map[int64]*Group{
			groupID: {ID: groupID, Status: StatusActive, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	accounts := &fullRebuildAccountRepo{}
	outbox := &outboxCleanupRepo{events: []SchedulerOutboxEvent{{ID: 1, EventType: SchedulerOutboxEventFullRebuild}}}
	svc := newFullRebuildLifecycleService(cache, outbox, accounts, groups, config.RunModeStandard)

	svc.pollOutbox()
	require.Zero(t, cache.currentWatermark())
	_, groupZeroPublished := cache.counts(schedulerCanonicalBuckets(0)[0])
	require.Equal(t, 1, groupZeroPublished, "ordinary tasks must still run when one strict Reopen task is busy")
	require.Equal(t, 19, accounts.callCount())

	svc.pollOutbox()
	require.Equal(t, int64(1), cache.currentWatermark())
	require.Equal(t, 39, accounts.callCount())
	_, busyBucketPublished := cache.counts(canonical[0])
	require.Equal(t, 1, busyBucketPublished)
	activeCalls, fallbackCalls, freshCalls := groups.stats()
	require.Equal(t, 2, activeCalls)
	require.Zero(t, fallbackCalls)
	require.Equal(t, []int64{groupID}, freshCalls)
}

func TestSchedulerFullRebuildOrdinaryLockBusyKeepsExistingSkipSemantics(t *testing.T) {
	busyBucket := schedulerCanonicalBuckets(0)[0]
	cache := newFullRebuildLifecycleCache()
	cache.lockBusyOnce[busyBucket.String()] = true
	groups := &fullRebuildLifecycleGroupRepo{fresh: make(map[int64]*Group), freshErr: make(map[int64]error)}
	accounts := &fullRebuildAccountRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, accounts, groups, config.RunModeStandard)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Equal(t, 9, accounts.callCount())
	attempts, published := cache.counts(busyBucket)
	require.Zero(t, attempts)
	require.Zero(t, published)
}

func requireFullRebuildNoMutationOrDB(t *testing.T, cache *fullRebuildLifecycleCache, accounts *fullRebuildAccountRepo) {
	t.Helper()
	require.Zero(t, cache.captureAttemptCount())
	require.Empty(t, cache.retiredBuckets())
	require.Empty(t, cache.tokens())
	require.Zero(t, accounts.callCount())
	require.Zero(t, cache.totalSetAttempts())
}
