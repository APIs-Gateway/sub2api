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

// batchAccountLoadKey identifies one loadAccountsFromDB call target for
// batchLoadCountingAccountRepo: same group + platform always resolves to the same
// configured account list, exactly like the real repository does for
// SchedulerModeSingle/SchedulerModeForced buckets.
type batchAccountLoadKey struct {
	groupID  int64
	platform string
}

// batchLoadCountingAccountRepo counts how many times each (groupID, platform) pair is
// queried, so tests can prove that reusing account IDs across buckets in the same
// rebuild batch also skips the now-unnecessary duplicate DB load, not just the
// duplicate Redis write.
type batchLoadCountingAccountRepo struct {
	AccountRepository

	mu       sync.Mutex
	accounts map[batchAccountLoadKey][]Account
	calls    map[batchAccountLoadKey]int
}

func newBatchLoadCountingAccountRepo() *batchLoadCountingAccountRepo {
	return &batchLoadCountingAccountRepo{
		accounts: make(map[batchAccountLoadKey][]Account),
		calls:    make(map[batchAccountLoadKey]int),
	}
}

func (r *batchLoadCountingAccountRepo) setAccounts(groupID int64, platform string, accounts []Account) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accounts[batchAccountLoadKey{groupID: groupID, platform: platform}] = accounts
}

func (r *batchLoadCountingAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := batchAccountLoadKey{groupID: groupID, platform: platform}
	r.calls[key]++
	return append([]Account(nil), r.accounts[key]...), nil
}

// ListSchedulableByPlatform backs RunModeSimple's group-zero canonical buckets:
// loadAccountsFromDB collapses GroupID to 0 and calls this instead of
// ListSchedulableByGroupIDAndPlatform once isRunModeSimple() is true, so it shares the
// same (groupID: 0, platform) bookkeeping key.
func (r *batchLoadCountingAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := batchAccountLoadKey{groupID: 0, platform: platform}
	r.calls[key]++
	return append([]Account(nil), r.accounts[key]...), nil
}

// ListSchedulableByGroupIDAndPlatforms backs SchedulerModeMixed buckets (groupID > 0):
// loadAccountsFromDB fans a mixed-mode load out across bucket.Platform plus
// PlatformAntigravity. Each underlying platform is tracked under its own
// (groupID, platform) key, same as the singular method.
func (r *batchLoadCountingAccountRepo) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Account
	for _, platform := range platforms {
		key := batchAccountLoadKey{groupID: groupID, platform: platform}
		r.calls[key]++
		out = append(out, r.accounts[key]...)
	}
	return out, nil
}

func (r *batchLoadCountingAccountRepo) callCount(groupID int64, platform string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[batchAccountLoadKey{groupID: groupID, platform: platform}]
}

// batchAccountIDReuseCache is a minimal SchedulerCache double that also implements
// schedulerSnapshotAccountIDWriter, so it can exercise the actual reuse contract in
// rebuildPreparedBucketTasks: which of SetSnapshot, SetSnapshotAndReturnAccountIDs or
// SetSnapshotByAccountIDs gets called for each bucket, with what accounts/IDs, and how
// injected failures at each of those methods propagate.
type batchAccountIDReuseCache struct {
	SchedulerCache

	mu          sync.Mutex
	lockBusy    map[SchedulerBucket]bool
	setCalls    map[SchedulerBucket]int
	fullCalls   map[SchedulerBucket]int
	idOnlyCalls map[SchedulerBucket]int
	setAccounts map[SchedulerBucket][]Account
	idOnlyIDs   map[SchedulerBucket][]int64
	fullErr     map[SchedulerBucket]error
	idOnlyErr   map[SchedulerBucket]error
	setErr      map[SchedulerBucket]error
}

func newBatchAccountIDReuseCache() *batchAccountIDReuseCache {
	return &batchAccountIDReuseCache{
		lockBusy:    make(map[SchedulerBucket]bool),
		setCalls:    make(map[SchedulerBucket]int),
		fullCalls:   make(map[SchedulerBucket]int),
		idOnlyCalls: make(map[SchedulerBucket]int),
		setAccounts: make(map[SchedulerBucket][]Account),
		idOnlyIDs:   make(map[SchedulerBucket][]int64),
		fullErr:     make(map[SchedulerBucket]error),
		idOnlyErr:   make(map[SchedulerBucket]error),
		setErr:      make(map[SchedulerBucket]error),
	}
}

func (c *batchAccountIDReuseCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *batchAccountIDReuseCache) TryLockBucket(_ context.Context, bucket SchedulerBucket, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.lockBusy[bucket], nil
}

func (c *batchAccountIDReuseCache) UnlockBucket(context.Context, SchedulerBucket) error { return nil }

func (c *batchAccountIDReuseCache) SetSnapshot(_ context.Context, bucket SchedulerBucket, _ SchedulerBucketWriteToken, accounts []Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setCalls[bucket]++
	c.setAccounts[bucket] = append([]Account(nil), accounts...)
	return c.setErr[bucket]
}

func (c *batchAccountIDReuseCache) SetSnapshotAndReturnAccountIDs(_ context.Context, bucket SchedulerBucket, _ SchedulerBucketWriteToken, accounts []Account) ([]int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fullCalls[bucket]++
	c.setAccounts[bucket] = append([]Account(nil), accounts...)
	if err := c.fullErr[bucket]; err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids, nil
}

func (c *batchAccountIDReuseCache) SetSnapshotByAccountIDs(_ context.Context, bucket SchedulerBucket, _ SchedulerBucketWriteToken, accountIDs []int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idOnlyCalls[bucket]++
	c.idOnlyIDs[bucket] = append([]int64(nil), accountIDs...)
	return c.idOnlyErr[bucket]
}

// counts returns (plain SetSnapshot calls, SetSnapshotAndReturnAccountIDs calls,
// SetSnapshotByAccountIDs calls) recorded for bucket.
func (c *batchAccountIDReuseCache) counts(bucket SchedulerBucket) (set, full, idOnly int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setCalls[bucket], c.fullCalls[bucket], c.idOnlyCalls[bucket]
}

func (c *batchAccountIDReuseCache) idOnlyIDsFor(bucket SchedulerBucket) []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.idOnlyIDs[bucket]...)
}

func newBatchReuseTestService(cache SchedulerCache, repo AccountRepository, runMode string) *SchedulerSnapshotService {
	return NewSchedulerSnapshotService(cache, nil, repo, nil, &config.Config{RunMode: runMode})
}

// TestSchedulerRebuildBucketsReusesAccountIDsForSingleAndForced covers the core batch
// contract from issue #646: within one rebuildBuckets call, SchedulerModeSingle and
// SchedulerModeForced buckets for the same group+platform load the exact same account
// list, so the first bucket must publish the full payload (and get back the actually
// encoded account IDs) while the second republishes by ID only. The DB load itself must
// happen exactly once, since the second bucket's own load result would just be
// discarded anyway.
func TestSchedulerRebuildBucketsReusesAccountIDsForSingleAndForced(t *testing.T) {
	const groupID int64 = 301
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchAccountIDReuseCache()
	repo := newBatchLoadCountingAccountRepo()
	repo.setAccounts(groupID, PlatformOpenAI, []Account{{ID: 1}, {ID: 2}})
	svc := newBatchReuseTestService(cache, repo, config.RunModeStandard)

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "reuse")
	require.NoError(t, err)

	set, full, idOnly := cache.counts(single)
	require.Equal(t, 0, set)
	require.Equal(t, 1, full)
	require.Equal(t, 0, idOnly)

	set, full, idOnly = cache.counts(forced)
	require.Equal(t, 0, set)
	require.Equal(t, 0, full)
	require.Equal(t, 1, idOnly)
	require.Equal(t, []int64{1, 2}, cache.idOnlyIDsFor(forced))

	require.Equal(t, 1, repo.callCount(groupID, PlatformOpenAI), "the redundant DB load for the reused bucket must be skipped")
}

// TestSchedulerRebuildBucketsNeverLeaksReuseAcrossSeparateBatches proves that the reuse
// tracker is scoped to a single rebuildBuckets call: two independent calls with the same
// bucket pair must each do their own full+ID-only publish, never silently reusing IDs
// left over from an earlier, unrelated batch.
func TestSchedulerRebuildBucketsNeverLeaksReuseAcrossSeparateBatches(t *testing.T) {
	const groupID int64 = 302
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchAccountIDReuseCache()
	repo := newBatchLoadCountingAccountRepo()
	repo.setAccounts(groupID, PlatformOpenAI, []Account{{ID: 3}})
	svc := newBatchReuseTestService(cache, repo, config.RunModeStandard)

	for run := 1; run <= 2; run++ {
		require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "reuse"))
		_, full, idOnly := cache.counts(single)
		require.Equal(t, run, full)
		require.Zero(t, idOnly)
		_, full, idOnly = cache.counts(forced)
		require.Zero(t, full)
		require.Equal(t, run, idOnly)
	}
	require.Equal(t, 2, repo.callCount(groupID, PlatformOpenAI))
}

// TestSchedulerRebuildBucketsFallsBackToPlainSetSnapshotAfterFirstWriteFailure covers
// that a failed full publish never marks its key reusable: the following bucket that
// shares the key must fall back to its own independent SetSnapshot instead of trying
// (and failing again on) an ID-only publish for IDs that were never produced.
func TestSchedulerRebuildBucketsFallsBackToPlainSetSnapshotAfterFirstWriteFailure(t *testing.T) {
	const groupID int64 = 303
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	wantErr := errors.New("full publish failed")
	cache := newBatchAccountIDReuseCache()
	cache.fullErr[single] = wantErr
	repo := newBatchLoadCountingAccountRepo()
	repo.setAccounts(groupID, PlatformOpenAI, []Account{{ID: 4}})
	svc := newBatchReuseTestService(cache, repo, config.RunModeStandard)

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "failure")
	require.ErrorIs(t, err, wantErr)

	set, full, idOnly := cache.counts(single)
	require.Equal(t, 0, set)
	require.Equal(t, 1, full)
	require.Equal(t, 0, idOnly)

	set, full, idOnly = cache.counts(forced)
	require.Equal(t, 1, set, "forced must fall back to the original SetSnapshot after single's full publish failed")
	require.Equal(t, 0, full)
	require.Equal(t, 0, idOnly)
	require.Equal(t, 2, repo.callCount(groupID, PlatformOpenAI), "forced must redo its own DB load since there is nothing to reuse")
}

// TestSchedulerRebuildBucketsPropagatesIDOnlyWriteFailureWithoutFallback covers that an
// ID-only publish failure is a real error, not something the pipeline silently retries
// as a full write.
func TestSchedulerRebuildBucketsPropagatesIDOnlyWriteFailureWithoutFallback(t *testing.T) {
	const groupID int64 = 304
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	wantErr := errors.New("id-only publish failed")
	cache := newBatchAccountIDReuseCache()
	cache.idOnlyErr[forced] = wantErr
	repo := newBatchLoadCountingAccountRepo()
	repo.setAccounts(groupID, PlatformOpenAI, []Account{{ID: 5}})
	svc := newBatchReuseTestService(cache, repo, config.RunModeStandard)

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "id-error")
	require.ErrorIs(t, err, wantErr)

	set, full, idOnly := cache.counts(forced)
	require.Zero(t, set, "an ID-only failure must not silently fall back to a full SetSnapshot")
	require.Zero(t, full)
	require.Equal(t, 1, idOnly)
}

// TestSchedulerRebuildBucketsKeepsDifferentPlatformsAndMixedModeIndependent covers that
// the reuse key is scoped per (group, platform, mixed): a same-group OpenAI pair being
// reused must not affect an Anthropic pair processed in the same batch, and a Mixed
// bucket (which always has a unique key) must never participate in ID reuse even though
// it shares group+platform with a reused Single/Forced pair.
func TestSchedulerRebuildBucketsKeepsDifferentPlatformsAndMixedModeIndependent(t *testing.T) {
	const groupID int64 = 305
	openAISingle := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	openAIForced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	anthropicSingle := SchedulerBucket{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeSingle}
	anthropicForced := SchedulerBucket{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeForced}
	anthropicMixed := SchedulerBucket{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeMixed}
	cache := newBatchAccountIDReuseCache()
	repo := newBatchLoadCountingAccountRepo()
	repo.setAccounts(groupID, PlatformOpenAI, []Account{{ID: 6}})
	repo.setAccounts(groupID, PlatformAnthropic, []Account{{ID: 7}})
	svc := newBatchReuseTestService(cache, repo, config.RunModeStandard)

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{
		openAISingle, openAIForced, anthropicSingle, anthropicForced, anthropicMixed,
	}, "scope")
	require.NoError(t, err)

	_, full, idOnly := cache.counts(openAISingle)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	_, full, idOnly = cache.counts(openAIForced)
	require.Zero(t, full)
	require.Equal(t, 1, idOnly)

	_, full, idOnly = cache.counts(anthropicSingle)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	_, full, idOnly = cache.counts(anthropicForced)
	require.Zero(t, full)
	require.Equal(t, 1, idOnly)

	set, full, idOnly := cache.counts(anthropicMixed)
	require.Equal(t, 1, set, "a mixed bucket must always keep going through the original SetSnapshot")
	require.Zero(t, full)
	require.Zero(t, idOnly)
}

// TestSchedulerRebuildBucketsLockBusyFirstBucketSkipsSpuriousReuse covers that when the
// first bucket sharing a key never gets to run (busy rebuild lock), the following
// bucket must not attempt an ID-only publish for IDs that were never produced; it has
// to fall back to its own full, independent SetSnapshot.
func TestSchedulerRebuildBucketsLockBusyFirstBucketSkipsSpuriousReuse(t *testing.T) {
	const groupID int64 = 306
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchAccountIDReuseCache()
	cache.lockBusy[single] = true
	repo := newBatchLoadCountingAccountRepo()
	repo.setAccounts(groupID, PlatformOpenAI, []Account{{ID: 8}})
	svc := newBatchReuseTestService(cache, repo, config.RunModeStandard)

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "busy"))

	set, full, idOnly := cache.counts(single)
	require.Zero(t, set)
	require.Zero(t, full)
	require.Zero(t, idOnly)

	set, full, idOnly = cache.counts(forced)
	require.Equal(t, 1, set, "forced must not attempt an ID-only publish when single never produced any IDs")
	require.Zero(t, full)
	require.Zero(t, idOnly)
}

// TestSchedulerRebuildBucketsReusesAccountIDsForSimpleRunModeGroupZero covers that
// RunModeSimple's canonical group-zero Single/Forced pair gets the same reuse treatment
// as a real group in standard run mode.
func TestSchedulerRebuildBucketsReusesAccountIDsForSimpleRunModeGroupZero(t *testing.T) {
	single := SchedulerBucket{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchAccountIDReuseCache()
	repo := newBatchLoadCountingAccountRepo()
	repo.setAccounts(0, PlatformOpenAI, []Account{{ID: 9}})
	svc := newBatchReuseTestService(cache, repo, config.RunModeSimple)

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "simple"))

	_, full, idOnly := cache.counts(single)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	_, full, idOnly = cache.counts(forced)
	require.Zero(t, full)
	require.Equal(t, 1, idOnly)
}

// legacySetSnapshotOnlyCache implements SchedulerCache but not
// schedulerSnapshotAccountIDWriter, modeling every cache implementation that predates
// issue #646. rebuildPreparedBucketTasks must keep working exactly as before for it:
// every bucket goes through the plain SetSnapshot path, with no reuse attempted.
type legacySetSnapshotOnlyCache struct {
	SchedulerCache

	mu       sync.Mutex
	setCalls map[SchedulerBucket]int
}

func newLegacySetSnapshotOnlyCache() *legacySetSnapshotOnlyCache {
	return &legacySetSnapshotOnlyCache{setCalls: make(map[SchedulerBucket]int)}
}

func (c *legacySetSnapshotOnlyCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *legacySetSnapshotOnlyCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	return true, nil
}

func (c *legacySetSnapshotOnlyCache) UnlockBucket(context.Context, SchedulerBucket) error { return nil }

func (c *legacySetSnapshotOnlyCache) SetSnapshot(_ context.Context, bucket SchedulerBucket, _ SchedulerBucketWriteToken, _ []Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setCalls[bucket]++
	return nil
}

func (c *legacySetSnapshotOnlyCache) callCount(bucket SchedulerBucket) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setCalls[bucket]
}

// TestSchedulerRebuildBucketsKeepsPlainSetSnapshotWhenCacheLacksReuseCapability covers
// backward compatibility with SchedulerCache implementations that do not implement
// schedulerSnapshotAccountIDWriter: both Single and Forced buckets must keep publishing
// through SetSnapshot, exactly like before this feature existed.
func TestSchedulerRebuildBucketsKeepsPlainSetSnapshotWhenCacheLacksReuseCapability(t *testing.T) {
	const groupID int64 = 307
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newLegacySetSnapshotOnlyCache()
	repo := newBatchLoadCountingAccountRepo()
	repo.setAccounts(groupID, PlatformOpenAI, []Account{{ID: 10}})
	svc := newBatchReuseTestService(cache, repo, config.RunModeStandard)

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "legacy"))

	require.Equal(t, 1, cache.callCount(single))
	require.Equal(t, 1, cache.callCount(forced))
	require.Equal(t, 2, repo.callCount(groupID, PlatformOpenAI), "without the reuse capability, each bucket still does its own independent load")
}
