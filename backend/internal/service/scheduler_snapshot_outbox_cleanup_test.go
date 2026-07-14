package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type outboxCleanupCache struct {
	watermark       int64
	setWatermarks   []int64
	watermarkErr    error
	updateErr       error
	listBucketCalls int
}

func (c *outboxCleanupCache) GetSnapshot(ctx context.Context, bucket SchedulerBucket) ([]*Account, bool, error) {
	return nil, false, nil
}

func (c *outboxCleanupCache) SetSnapshot(ctx context.Context, bucket SchedulerBucket, accounts []Account) error {
	return nil
}

func (c *outboxCleanupCache) GetAccount(ctx context.Context, accountID int64) (*Account, error) {
	return nil, nil
}

func (c *outboxCleanupCache) SetAccount(ctx context.Context, account *Account) error {
	return nil
}

func (c *outboxCleanupCache) DeleteAccount(ctx context.Context, accountID int64) error {
	return nil
}

func (c *outboxCleanupCache) UpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	return c.updateErr
}

func (c *outboxCleanupCache) TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error) {
	return true, nil
}

func (c *outboxCleanupCache) UnlockBucket(ctx context.Context, bucket SchedulerBucket) error {
	return nil
}

func (c *outboxCleanupCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	c.listBucketCalls++
	return nil, nil
}

func (c *outboxCleanupCache) GetOutboxWatermark(ctx context.Context) (int64, error) {
	return c.watermark, nil
}

func (c *outboxCleanupCache) SetOutboxWatermark(ctx context.Context, id int64) error {
	if c.watermarkErr != nil {
		return c.watermarkErr
	}
	c.watermark = id
	c.setWatermarks = append(c.setWatermarks, id)
	return nil
}

type outboxCleanupDeleteCall struct {
	watermark int64
	limit     int
}

type outboxCleanupRepo struct {
	events              []SchedulerOutboxEvent
	rows                []int64
	lockAcquired        bool
	lockAttempts        int
	releaseCount        int
	deleteCalls         []outboxCleanupDeleteCall
	firstCreatedAfterID []int64
	firstCreatedErr     error
}

func (r *outboxCleanupRepo) ListAfterAndReleaseDedup(ctx context.Context, afterID int64, limit int) ([]SchedulerOutboxEvent, error) {
	events := make([]SchedulerOutboxEvent, 0, len(r.events))
	for _, event := range r.events {
		if event.ID <= afterID {
			continue
		}
		events = append(events, event)
		if limit > 0 && len(events) >= limit {
			break
		}
	}
	return events, nil
}

func (r *outboxCleanupRepo) FirstCreatedAtAfter(ctx context.Context, afterID int64) (time.Time, bool, error) {
	r.firstCreatedAfterID = append(r.firstCreatedAfterID, afterID)
	if r.firstCreatedErr != nil {
		return time.Time{}, false, r.firstCreatedErr
	}
	for _, event := range r.events {
		if event.ID > afterID {
			return event.CreatedAt, true, nil
		}
	}
	return time.Time{}, false, nil
}

func (r *outboxCleanupRepo) MaxID(ctx context.Context) (int64, error) {
	var maxID int64
	for _, id := range r.rows {
		if id > maxID {
			maxID = id
		}
	}
	return maxID, nil
}

func (r *outboxCleanupRepo) DeleteConsumedUpTo(ctx context.Context, watermark int64, limit int) (int64, error) {
	r.deleteCalls = append(r.deleteCalls, outboxCleanupDeleteCall{
		watermark: watermark,
		limit:     limit,
	})
	if watermark <= 0 || limit <= 0 {
		return 0, nil
	}

	deleted := int64(0)
	kept := make([]int64, 0, len(r.rows))
	for _, id := range r.rows {
		if id <= watermark && deleted < int64(limit) {
			deleted++
			continue
		}
		kept = append(kept, id)
	}
	r.rows = kept
	return deleted, nil
}

func (r *outboxCleanupRepo) TryAcquireCleanupLock(ctx context.Context) (SchedulerOutboxCleanupLease, bool, error) {
	r.lockAttempts++
	if !r.lockAcquired {
		return nil, false, nil
	}
	return outboxCleanupLease{release: func() {
		r.releaseCount++
	}}, true, nil
}

type outboxCleanupLease struct {
	release func()
}

func (l outboxCleanupLease) Release() {
	if l.release != nil {
		l.release()
	}
}

func TestSchedulerSnapshotServicePollOutboxCleansConsumedRowsAfterWatermark(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{
			{ID: 10000, EventType: SchedulerOutboxEventAccountLastUsed},
		},
		rows:         int64Range(1, 10003),
		lockAcquired: true,
	}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, nil)

	svc.pollOutbox()

	if cache.watermark != 10000 {
		t.Fatalf("expected watermark 10000, got %d", cache.watermark)
	}
	if !reflect.DeepEqual(cache.setWatermarks, []int64{10000}) {
		t.Fatalf("unexpected watermark writes: %#v", cache.setWatermarks)
	}
	if !reflect.DeepEqual(repo.rows, []int64{10001, 10002, 10003}) {
		t.Fatalf("expected rows above watermark to remain, got %#v", repo.rows)
	}
	if repo.lockAttempts != 1 || repo.releaseCount != 1 {
		t.Fatalf("expected one lock acquire/release, got acquire=%d release=%d", repo.lockAttempts, repo.releaseCount)
	}
	if len(repo.deleteCalls) != 3 {
		t.Fatalf("expected cleanup to loop until a short batch, got %d calls", len(repo.deleteCalls))
	}
	for _, call := range repo.deleteCalls {
		if call.watermark != 10000 || call.limit != schedulerOutboxCleanupBatch {
			t.Fatalf("unexpected cleanup call: %#v", call)
		}
	}
}

func TestSchedulerSnapshotServicePollOutboxSkipsCleanupWhenLockUnavailable(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{
			{ID: 3, EventType: SchedulerOutboxEventAccountLastUsed},
		},
		rows:         []int64{1, 2, 3, 4},
		lockAcquired: false,
	}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, nil)

	svc.pollOutbox()

	if cache.watermark != 3 {
		t.Fatalf("expected watermark 3, got %d", cache.watermark)
	}
	if !reflect.DeepEqual(repo.rows, []int64{1, 2, 3, 4}) {
		t.Fatalf("expected cleanup to skip all rows, got %#v", repo.rows)
	}
	if repo.lockAttempts != 1 {
		t.Fatalf("expected one lock attempt, got %d", repo.lockAttempts)
	}
	if len(repo.deleteCalls) != 0 {
		t.Fatalf("expected no delete calls, got %#v", repo.deleteCalls)
	}
	if repo.releaseCount != 0 {
		t.Fatalf("expected no release without lock, got %d", repo.releaseCount)
	}
}

func TestSchedulerSnapshotServicePollOutboxDoesNotCleanupOnHandleFailure(t *testing.T) {
	cache := &outboxCleanupCache{
		updateErr: errors.New("cache update failed"),
	}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{
			{
				ID:        5,
				EventType: SchedulerOutboxEventAccountLastUsed,
				Payload: map[string]any{
					"last_used": map[string]any{"101": float64(123)},
				},
			},
		},
		rows:         []int64{1, 2, 3, 4, 5, 6},
		lockAcquired: true,
	}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, nil)

	svc.pollOutbox()

	if len(cache.setWatermarks) != 0 {
		t.Fatalf("expected no watermark write on handle failure, got %#v", cache.setWatermarks)
	}
	if repo.lockAttempts != 0 {
		t.Fatalf("expected cleanup lock not to be attempted, got %d", repo.lockAttempts)
	}
	if len(repo.deleteCalls) != 0 {
		t.Fatalf("expected no delete calls, got %#v", repo.deleteCalls)
	}
	if !reflect.DeepEqual(repo.rows, []int64{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("expected rows unchanged, got %#v", repo.rows)
	}
}

func TestSchedulerSnapshotServicePollOutboxDoesNotUseConsumedEventForLag(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{
			{
				ID:        7,
				EventType: SchedulerOutboxEventAccountLastUsed,
				CreatedAt: time.Now().Add(-time.Hour),
			},
		},
	}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxLagWarnSeconds:     1,
				OutboxLagRebuildSeconds:  1,
				OutboxLagRebuildFailures: 1,
			},
		},
	}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, cfg)

	svc.pollOutbox()

	if cache.watermark != 7 {
		t.Fatalf("expected watermark 7, got %d", cache.watermark)
	}
	if !reflect.DeepEqual(repo.firstCreatedAfterID, []int64{7}) {
		t.Fatalf("expected lag check after consumed watermark, got %#v", repo.firstCreatedAfterID)
	}
	if cache.listBucketCalls != 0 {
		t.Fatalf("expected consumed event not to trigger full rebuild, got %d attempts", cache.listBucketCalls)
	}
	if svc.lagFailures != 0 {
		t.Fatalf("expected lag failures to remain reset, got %d", svc.lagFailures)
	}
}

func TestSchedulerSnapshotServicePollOutboxStopsAfterWatermarkWriteFailure(t *testing.T) {
	wantErr := errors.New("watermark write failed")
	cache := &outboxCleanupCache{watermarkErr: wantErr}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 8, EventType: SchedulerOutboxEventAccountLastUsed}},
		rows:   []int64{1, 2, 3},
	}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, nil)

	svc.pollOutbox()

	if len(cache.setWatermarks) != 0 {
		t.Fatalf("expected watermark write to fail before recording success, got %#v", cache.setWatermarks)
	}
	if len(repo.deleteCalls) != 0 || len(repo.firstCreatedAfterID) != 0 {
		t.Fatalf("expected no cleanup or lag query after watermark failure, deletes=%#v lag=%#v", repo.deleteCalls, repo.firstCreatedAfterID)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagStopsOnPendingReadFailure(t *testing.T) {
	wantErr := errors.New("pending event read failed")
	repo := &outboxCleanupRepo{firstCreatedErr: wantErr}
	cfg := &config.Config{Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{OutboxLagRebuildSeconds: 1, OutboxLagRebuildFailures: 1}}}
	svc := NewSchedulerSnapshotService(&outboxCleanupCache{}, repo, nil, nil, cfg)
	svc.lagFailures = 3

	svc.checkOutboxLag(context.Background(), 7)

	if svc.lagFailures != 3 {
		t.Fatalf("expected lag failures to remain unchanged after read failure, got %d", svc.lagFailures)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagTriggersFullRebuildForPendingEvent(t *testing.T) {
	cache := &schedulerFullRebuildTestCache{listErr: errors.New("rebuild list failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 8, CreatedAt: time.Now().Add(-time.Hour)}},
	}
	cfg := &config.Config{Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
		OutboxLagWarnSeconds:     1,
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
	}}}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, cfg)

	svc.checkOutboxLag(context.Background(), 7)

	if cache.listCalls != 1 {
		t.Fatalf("expected one full rebuild attempt, got %d", cache.listCalls)
	}
	if svc.lagFailures != 0 {
		t.Fatalf("expected lag failures to reset after triggering rebuild, got %d", svc.lagFailures)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagLatchesPersistentDegradation(t *testing.T) {
	cache := &schedulerFullRebuildTestCache{}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
	}
	cfg := &config.Config{Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
		OutboxBacklogRebuildRows: 50,
	}}}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, cfg)

	for range 3 {
		svc.checkOutboxLag(context.Background(), 0)
	}

	if cache.listCalls != 1 {
		t.Fatalf("expected one rebuild during a persistent lag episode, got %d", cache.listCalls)
	}
	if !svc.outboxRebuildLatched {
		t.Fatal("expected a successful rebuild to latch the degraded episode")
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagRetriesAfterCooldown(t *testing.T) {
	wantErr := errors.New("rebuild unavailable")
	cache := &schedulerFullRebuildTestCache{listErr: wantErr}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
	}
	cfg := &config.Config{Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
		OutboxBacklogRebuildRows: 50,
	}}}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, cfg)

	svc.checkOutboxLag(context.Background(), 0)
	if cache.listCalls != 1 || svc.outboxRebuildRetryReason != "outbox_lag" {
		t.Fatalf("expected one failed lag rebuild with retry state, calls=%d reason=%q", cache.listCalls, svc.outboxRebuildRetryReason)
	}

	svc.lagMu.Lock()
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listCalls != 2 {
		t.Fatalf("expected one retry after cooldown expiry, got %d calls", cache.listCalls)
	}

	// A recovered lag episode must clear the old retry reason before a new
	// backlog episode is evaluated, so the stale lag cooldown cannot delay it.
	repo.events[0].CreatedAt = time.Now()
	repo.rows = []int64{100}
	cache.listErr = nil
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listCalls != 3 {
		t.Fatalf("expected the new backlog episode to trigger immediately, got %d calls", cache.listCalls)
	}
	if !svc.outboxRebuildLatched || svc.outboxRebuildRetryReason != "" {
		t.Fatalf("expected successful backlog retry to latch and clear retry state, latched=%v reason=%q", svc.outboxRebuildLatched, svc.outboxRebuildRetryReason)
	}
}

func TestSchedulerSnapshotServicePollOutboxEmptyBatchClearsDegradedEpisode(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, &config.Config{})
	svc.lagFailures = 2
	svc.outboxRebuildLatched = true
	svc.outboxRebuildFailures = 2
	svc.outboxRebuildRetryAt = time.Now().Add(time.Minute)
	svc.outboxRebuildRetryReason = "outbox_lag"
	svc.outboxLagWarningActive = true

	svc.pollOutbox()

	if svc.lagFailures != 0 || svc.outboxRebuildLatched || svc.outboxRebuildFailures != 0 ||
		!svc.outboxRebuildRetryAt.IsZero() || svc.outboxRebuildRetryReason != "" || svc.outboxLagWarningActive {
		t.Fatalf("expected empty outbox batch to clear degraded state: %#v", svc)
	}
}

func TestOutboxRebuildRetryDelayIsBounded(t *testing.T) {
	previous := time.Duration(0)
	for failures := 1; failures <= 20; failures++ {
		delay := outboxRebuildRetryDelay(failures)
		if delay < previous || delay > outboxRebuildRetryMaxDelay {
			t.Fatalf("unexpected retry delay at failure %d: previous=%s current=%s", failures, previous, delay)
		}
		previous = delay
	}
	if previous != outboxRebuildRetryMaxDelay {
		t.Fatalf("expected retry delay to reach %s, got %s", outboxRebuildRetryMaxDelay, previous)
	}
}

func TestSchedulerSnapshotServiceOutboxWarningAndMaxIDErrorsAreSampled(t *testing.T) {
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, nil)
	now := time.Now()

	if !svc.shouldLogOutboxLagWarning(true) || svc.shouldLogOutboxLagWarning(true) || svc.shouldLogOutboxLagWarning(false) || !svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected lag warnings to be transition limited")
	}
	if !svc.shouldLogOutboxMaxIDError(now) || svc.shouldLogOutboxMaxIDError(now.Add(time.Second)) || !svc.shouldLogOutboxMaxIDError(now.Add(outboxMaxIDErrorLogSampleInterval)) {
		t.Fatal("expected MaxID errors to be sampled")
	}
}

func TestSchedulerSnapshotServiceCleanupSkipsNonPositiveWatermark(t *testing.T) {
	repo := &outboxCleanupRepo{
		rows:         []int64{1, 2, 3},
		lockAcquired: true,
	}
	svc := NewSchedulerSnapshotService(&outboxCleanupCache{}, repo, nil, nil, nil)

	svc.cleanupConsumedOutbox(0)

	if repo.lockAttempts != 0 {
		t.Fatalf("expected no lock attempt for non-positive watermark, got %d", repo.lockAttempts)
	}
	if len(repo.deleteCalls) != 0 {
		t.Fatalf("expected no delete calls, got %#v", repo.deleteCalls)
	}
	if !reflect.DeepEqual(repo.rows, []int64{1, 2, 3}) {
		t.Fatalf("expected rows unchanged, got %#v", repo.rows)
	}
}

func int64Range(start, end int64) []int64 {
	values := make([]int64, 0, end-start+1)
	for id := start; id <= end; id++ {
		values = append(values, id)
	}
	return values
}
