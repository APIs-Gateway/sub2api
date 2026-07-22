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

type retirementRaceCache struct {
	SchedulerCache

	mu          sync.Mutex
	epochs      map[string]int64
	retired     map[string]bool
	listBuckets []SchedulerBucket
	captures    []SchedulerBucket
	reopens     []SchedulerBucket
	setAttempts map[string]int
	published   map[string]int
	versions    map[string]int
	beforeSet   func()
	captureErrs map[string]error
	setErrs     map[string]error
	tryLockOK   bool
	tryLockErr  error
	unlockErr   error
}

func newRetirementRaceCache(buckets ...SchedulerBucket) *retirementRaceCache {
	return &retirementRaceCache{
		epochs:      make(map[string]int64),
		retired:     make(map[string]bool),
		listBuckets: buckets,
		setAttempts: make(map[string]int),
		published:   make(map[string]int),
		versions:    make(map[string]int),
		captureErrs: make(map[string]error),
		setErrs:     make(map[string]error),
		tryLockOK:   true,
	}
}

func (c *retirementRaceCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return nil, false, nil
}

func (c *retirementRaceCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucket.String()
	c.captures = append(c.captures, bucket)
	if err := c.captureErrs[key]; err != nil {
		return SchedulerBucketWriteToken{}, err
	}
	if c.retired[key] {
		return SchedulerBucketWriteToken{}, ErrSchedulerBucketRetired
	}
	if c.epochs[key] == 0 {
		c.epochs[key] = 1
	}
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: c.epochs[key]}, nil
}

func (c *retirementRaceCache) SetSnapshot(_ context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, _ []Account) error {
	if c.beforeSet != nil {
		c.beforeSet()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucket.String()
	c.setAttempts[key]++
	if err := c.setErrs[key]; err != nil {
		return err
	}
	if !token.ValidFor(bucket) {
		return ErrSchedulerBucketWriteFenced
	}
	if c.retired[key] {
		return ErrSchedulerBucketRetired
	}
	if c.epochs[key] != token.Epoch {
		return ErrSchedulerBucketWriteFenced
	}
	c.versions[key]++
	c.published[key]++
	return nil
}

func (c *retirementRaceCache) RetireBucket(_ context.Context, bucket SchedulerBucket) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucket.String()
	if !c.retired[key] {
		c.epochs[key]++
		if c.epochs[key] < 1 {
			c.epochs[key] = 1
		}
		c.retired[key] = true
	}
	return nil
}

func (c *retirementRaceCache) ReopenBucket(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucket.String()
	if c.epochs[key] == 0 {
		c.epochs[key] = 1
	}
	delete(c.retired, key)
	c.reopens = append(c.reopens, bucket)
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: c.epochs[key]}, nil
}

func (c *retirementRaceCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tryLockOK, c.tryLockErr
}

func (c *retirementRaceCache) UnlockBucket(context.Context, SchedulerBucket) error {
	return c.unlockErr
}

func (c *retirementRaceCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	return append([]SchedulerBucket(nil), c.listBuckets...), nil
}

func (c *retirementRaceCache) counts(bucket SchedulerBucket) (setAttempts, published int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setAttempts[bucket.String()], c.published[bucket.String()]
}

func (c *retirementRaceCache) captureAndReopenCounts() (captures, reopens int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.captures), len(c.reopens)
}

func (c *retirementRaceCache) version(bucket SchedulerBucket) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.versions[bucket.String()]
}

type retirementGroupRepo struct {
	GroupRepository
	groups []Group
	err    error
}

func (r *retirementGroupRepo) ListActive(context.Context) ([]Group, error) {
	return r.groups, r.err
}

func TestSchedulerFullRebuildCapturesAllRegistryTokensBeforeDBLoad(t *testing.T) {
	first := SchedulerBucket{GroupID: 61, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	queued := SchedulerBucket{GroupID: 61, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newRetirementRaceCache(first, queued)
	dbStarted := make(chan struct{})
	releaseDB := make(chan struct{})
	var firstDB sync.Once
	repo := &mockAccountRepoForPlatform{
		listPlatformFunc: func(context.Context, string) ([]Account, error) {
			firstDB.Do(func() {
				close(dbStarted)
				<-releaseDB
			})
			return []Account{{ID: 6101, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}}, nil
		},
	}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, &config.Config{
		RunMode: config.RunModeStandard,
		Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
			DbFallbackEnabled: true,
		}},
	})

	result := make(chan error, 1)
	go func() { result <- svc.triggerFullRebuild("retirement_race_a") }()
	select {
	case <-dbStarted:
	case <-time.After(time.Second):
		t.Fatal("first DB load did not start")
	}

	captures, reopens := cache.captureAndReopenCounts()
	require.Equal(t, 2, captures, "all registry tokens must be captured before the first DB load")
	require.Zero(t, reopens)
	require.NoError(t, cache.RetireBucket(context.Background(), queued))
	_, err := cache.ReopenBucket(context.Background(), queued)
	require.NoError(t, err)
	close(releaseDB)
	require.NoError(t, <-result)

	_, firstPublished := cache.counts(first)
	queuedAttempts, queuedPublished := cache.counts(queued)
	require.Equal(t, 1, firstPublished)
	require.Equal(t, 1, queuedAttempts)
	require.Zero(t, queuedPublished, "queued registry task must not adopt the reopened epoch")
}

func TestSchedulerRebuildRetireAfterDBLoadFencesPublish(t *testing.T) {
	bucket := SchedulerBucket{GroupID: 62, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	cache := newRetirementRaceCache()
	dbReturned := make(chan struct{})
	setEntered := make(chan struct{})
	releaseSet := make(chan struct{})
	cache.beforeSet = func() {
		close(setEntered)
		<-releaseSet
	}
	repo := &mockAccountRepoForPlatform{
		listPlatformFunc: func(context.Context, string) ([]Account, error) {
			close(dbReturned)
			return []Account{{ID: 6201, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}}, nil
		},
	}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, &config.Config{
		RunMode: config.RunModeStandard,
		Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
			DbFallbackEnabled: true,
		}},
	})

	result := make(chan error, 1)
	go func() {
		result <- svc.rebuildBuckets(context.Background(), []SchedulerBucket{bucket}, "retirement_race_b")
	}()
	select {
	case <-dbReturned:
	case <-time.After(time.Second):
		t.Fatal("DB load did not return")
	}
	select {
	case <-setEntered:
	case <-time.After(time.Second):
		t.Fatal("snapshot writer did not reach allocation boundary")
	}
	require.NoError(t, cache.RetireBucket(context.Background(), bucket))
	close(releaseSet)
	require.NoError(t, <-result)

	setAttempts, published := cache.counts(bucket)
	require.Equal(t, 1, setAttempts)
	require.Zero(t, published)
	require.Zero(t, cache.version(bucket), "retirement before allocation must not advance the snapshot version")
}

func TestSchedulerFallbackReturnsDBAccountsWhenBucketRetired(t *testing.T) {
	bucket := SchedulerBucket{GroupID: 63, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	cache := newRetirementRaceCache()
	require.NoError(t, cache.RetireBucket(context.Background(), bucket))
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{{ID: 6301, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}},
	}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, &config.Config{
		RunMode: config.RunModeStandard,
		Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
			DbFallbackEnabled: true,
		}},
	})
	groupID := bucket.GroupID

	accounts, useMixed, err := svc.ListSchedulableAccounts(context.Background(), &groupID, bucket.Platform, false)
	require.NoError(t, err)
	require.False(t, useMixed)
	require.Len(t, accounts, 1)
	setAttempts, published := cache.counts(bucket)
	require.Zero(t, setAttempts)
	require.Zero(t, published)
}

func TestSchedulerDefaultBucketsUseCaptureAndListActiveFailureKeepsGroupZero(t *testing.T) {
	cache := newRetirementRaceCache()
	svc := NewSchedulerSnapshotService(
		cache,
		nil,
		nil,
		&retirementGroupRepo{err: errors.New("list active failed")},
		testConfig(),
	)

	buckets, err := svc.defaultBuckets(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, buckets)
	for _, bucket := range buckets {
		require.Zero(t, bucket.GroupID)
	}

	tasks, err := svc.prepareBucketWriteTasks(context.Background(), buckets)
	require.NoError(t, err)
	require.Len(t, tasks, len(buckets))
	captures, reopens := cache.captureAndReopenCounts()
	require.Equal(t, len(buckets), captures)
	require.Zero(t, reopens)
}

func TestSchedulerListFallbackKeepsDBResultWhenCachePublishErrors(t *testing.T) {
	bucket := SchedulerBucket{GroupID: 64, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	account := Account{ID: 6401, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}
	repoErr := errors.New("cache token unavailable")
	cache := newRetirementRaceCache()
	cache.captureErrs[bucket.String()] = repoErr
	cfg := testConfig()
	cfg.Gateway.Scheduling.DbFallbackEnabled = true
	svc := NewSchedulerSnapshotService(cache, nil, &mockAccountRepoForPlatform{accounts: []Account{account}}, nil, cfg)

	groupID := bucket.GroupID
	accounts, _, err := svc.ListSchedulableAccounts(context.Background(), &groupID, bucket.Platform, false)
	require.NoError(t, err)
	require.Equal(t, []Account{account}, accounts)
	setAttempts, published := cache.counts(bucket)
	require.Zero(t, setAttempts)
	require.Zero(t, published)

	setErr := errors.New("cache publish failed")
	cache = newRetirementRaceCache()
	cache.setErrs[bucket.String()] = setErr
	svc = NewSchedulerSnapshotService(cache, nil, &mockAccountRepoForPlatform{accounts: []Account{account}}, nil, cfg)
	accounts, _, err = svc.ListSchedulableAccounts(context.Background(), &groupID, bucket.Platform, false)
	require.NoError(t, err)
	require.Equal(t, []Account{account}, accounts)
	setAttempts, published = cache.counts(bucket)
	require.Equal(t, 1, setAttempts)
	require.Zero(t, published)
}

func TestSchedulerPrepareTasksSkipsFencedBucketsAndReturnsCaptureErrors(t *testing.T) {
	retired := SchedulerBucket{GroupID: 65, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	failed := SchedulerBucket{GroupID: 66, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	ready := SchedulerBucket{GroupID: 67, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	cache := newRetirementRaceCache()
	require.NoError(t, cache.RetireBucket(context.Background(), retired))
	wantErr := errors.New("capture failed")
	cache.captureErrs[failed.String()] = wantErr
	svc := NewSchedulerSnapshotService(cache, nil, nil, nil, testConfig())

	tasks, err := svc.prepareBucketWriteTasks(context.Background(), []SchedulerBucket{retired, failed, ready})
	require.ErrorIs(t, err, wantErr)
	require.Len(t, tasks, 1)
	require.Equal(t, ready, tasks[0].bucket)
}

func TestSchedulerRebuildBucketReturnsLockAndDatabaseErrors(t *testing.T) {
	bucket := SchedulerBucket{GroupID: 68, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	lockErr := errors.New("lock backend failed")
	cache := newRetirementRaceCache()
	cache.tryLockErr = lockErr
	svc := NewSchedulerSnapshotService(cache, nil, nil, nil, testConfig())
	tasks, err := svc.prepareBucketWriteTasks(context.Background(), []SchedulerBucket{bucket})
	require.NoError(t, err)
	require.ErrorIs(t, svc.rebuildBucketWithToken(context.Background(), tasks[0], "lock_error"), lockErr)

	cache = newRetirementRaceCache()
	cache.tryLockOK = false
	svc = NewSchedulerSnapshotService(cache, nil, nil, nil, testConfig())
	tasks, err = svc.prepareBucketWriteTasks(context.Background(), []SchedulerBucket{bucket})
	require.NoError(t, err)
	require.NoError(t, svc.rebuildBucketWithToken(context.Background(), tasks[0], "lock_busy"))

	dbErr := errors.New("database load failed")
	cache = newRetirementRaceCache()
	svc = NewSchedulerSnapshotService(cache, nil, &mockAccountRepoForPlatform{listPlatformFunc: func(context.Context, string) ([]Account, error) {
		return nil, dbErr
	}}, nil, testConfig())
	tasks, err = svc.prepareBucketWriteTasks(context.Background(), []SchedulerBucket{bucket})
	require.NoError(t, err)
	require.ErrorIs(t, svc.rebuildBucketWithToken(context.Background(), tasks[0], "db_error"), dbErr)
}

func TestSchedulerRebuildBucketReturnsNonFencingCacheError(t *testing.T) {
	bucket := SchedulerBucket{GroupID: 69, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	cacheErr := errors.New("snapshot publish failed")
	cache := newRetirementRaceCache()
	cache.setErrs[bucket.String()] = cacheErr
	svc := NewSchedulerSnapshotService(cache, nil, &mockAccountRepoForPlatform{
		accounts: []Account{{ID: 6901, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}},
	}, nil, testConfig())

	require.ErrorIs(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{bucket}, "cache_error"), cacheErr)
}

func TestSchedulerBucketBuildersCoverPlatformModesAndDeduplication(t *testing.T) {
	svc := &SchedulerSnapshotService{}
	seen := make(map[batchSeenKey]struct{})

	buckets := svc.bucketsForPlatform(PlatformAnthropic, []int64{70, 70}, seen)
	require.Equal(t, []SchedulerBucket{
		{GroupID: 70, Platform: PlatformAnthropic, Mode: SchedulerModeSingle},
		{GroupID: 70, Platform: PlatformAnthropic, Mode: SchedulerModeForced},
		{GroupID: 70, Platform: PlatformAnthropic, Mode: SchedulerModeMixed},
	}, buckets)
	require.Empty(t, svc.bucketsForPlatform(PlatformAnthropic, []int64{70}, seen))
	require.Empty(t, svc.bucketsForPlatform("", []int64{70}, nil))

	cache := newRetirementRaceCache()
	cache.tryLockOK = false
	svc = NewSchedulerSnapshotService(cache, nil, nil, nil, testConfig())
	account := &Account{ID: 7001, Platform: PlatformAntigravity, GroupIDs: []int64{70}, Extra: map[string]any{"mixed_scheduling": true}}
	require.NoError(t, svc.rebuildByAccount(context.Background(), account, []int64{70}, "builder", nil))
	require.NoError(t, svc.rebuildByGroupIDs(context.Background(), []int64{70}, "builder", nil))
}
