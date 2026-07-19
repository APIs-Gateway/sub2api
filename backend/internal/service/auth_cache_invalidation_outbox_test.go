//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type authCacheInvalidationCacheStub struct {
	apiKeyCacheStub
	published  []string
	deleteErr  error
	publishErr error
}

func (s *authCacheInvalidationCacheStub) DeleteAuthCache(ctx context.Context, key string) error {
	s.deleteAuthKeys = append(s.deleteAuthKeys, key)
	return s.deleteErr
}

func (s *authCacheInvalidationCacheStub) PublishAuthCacheInvalidation(ctx context.Context, key string) error {
	s.published = append(s.published, key)
	return s.publishErr
}

type authCacheInvalidationOutboxStub struct {
	stats        AuthCacheInvalidationOutboxStats
	statsErr     error
	claimEvents  []AuthCacheInvalidationEvent
	claimErr     error
	claimStarted chan struct{}
	claimOnce    sync.Once
	claimMu      sync.Mutex
	claimed      bool
	scheduled    []int64
	scheduleErr  error
	deleted      []int64
	deleteErr    error
	retried      []int64
	retryErr     error
}

func (s *authCacheInvalidationOutboxStub) Claim(context.Context, string, int, time.Duration) ([]AuthCacheInvalidationEvent, error) {
	s.claimOnce.Do(func() {
		if s.claimStarted != nil {
			close(s.claimStarted)
		}
	})
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	if s.claimed {
		return nil, nil
	}
	s.claimed = true
	return s.claimEvents, nil
}

func (s *authCacheInvalidationOutboxStub) DeleteClaimed(_ context.Context, id int64, _ string) error {
	s.deleted = append(s.deleted, id)
	return s.deleteErr
}

func (s *authCacheInvalidationOutboxStub) ScheduleSecondPass(_ context.Context, id int64, _ string, _ time.Time) error {
	s.scheduled = append(s.scheduled, id)
	return s.scheduleErr
}

func (s *authCacheInvalidationOutboxStub) RetryClaimed(_ context.Context, id int64, _ string, _ time.Time, _ string) error {
	s.retried = append(s.retried, id)
	return s.retryErr
}

func (s *authCacheInvalidationOutboxStub) Stats(context.Context) (AuthCacheInvalidationOutboxStats, error) {
	return s.stats, s.statsErr
}

func TestAuthCacheInvalidationWorkerStartStopProcessesBatch(t *testing.T) {
	repo := &authCacheInvalidationOutboxStub{
		claimEvents:  []AuthCacheInvalidationEvent{{ID: 11, CacheKey: "cache-key", Stage: 1}},
		claimStarted: make(chan struct{}),
	}
	cache := &authCacheInvalidationCacheStub{}
	worker := NewAuthCacheInvalidationWorker(repo, cache, nil)
	worker.Start()

	select {
	case <-repo.claimStarted:
	case <-time.After(time.Second):
		t.Fatal("worker did not claim an outbox batch")
	}
	require.Eventually(t, func() bool { return worker.processed.Load() == 1 }, time.Second, 10*time.Millisecond)
	worker.Stop()
	require.False(t, worker.running.Load())
	require.Equal(t, []int64{11}, repo.deleted)
}

func TestAuthCacheInvalidationWorkerRunsSafetyPassBeforeAck(t *testing.T) {
	repo := &authCacheInvalidationOutboxStub{}
	cache := &authCacheInvalidationCacheStub{}
	worker := NewAuthCacheInvalidationWorker(repo, cache, nil)
	event := AuthCacheInvalidationEvent{ID: 7, CacheKey: "cache-key", CreatedAt: time.Now()}

	worker.processEvent(context.Background(), event)
	require.Equal(t, []string{"cache-key"}, cache.deleteAuthKeys)
	require.Equal(t, []string{"cache-key"}, cache.published)
	require.Equal(t, []int64{7}, repo.scheduled)
	require.Empty(t, repo.deleted)
	require.EqualValues(t, 1, worker.processed.Load())

	worker.processEvent(context.Background(), AuthCacheInvalidationEvent{ID: 7, CacheKey: "cache-key", Stage: 1})
	require.Equal(t, []string{"cache-key", "cache-key"}, cache.deleteAuthKeys)
	require.Equal(t, []string{"cache-key", "cache-key"}, cache.published)
	require.Equal(t, []int64{7}, repo.deleted)
	require.EqualValues(t, 2, worker.processed.Load())
}

func TestAuthCacheInvalidationWorkerRetriesRedisFailure(t *testing.T) {
	repo := &authCacheInvalidationOutboxStub{}
	cache := &authCacheInvalidationCacheStub{deleteErr: errors.New("redis unavailable")}
	worker := NewAuthCacheInvalidationWorker(repo, cache, nil)

	worker.processEvent(context.Background(), AuthCacheInvalidationEvent{ID: 9, CacheKey: "cache-key", Attempts: 2})

	require.Equal(t, []int64{9}, repo.retried)
	require.Empty(t, repo.scheduled)
	require.Empty(t, repo.deleted)
	require.EqualValues(t, 1, worker.failures.Load())
	require.NotEmpty(t, worker.Health(context.Background()).LastError)
}

func TestAuthCacheInvalidationWorkerRecordsCompletionFailures(t *testing.T) {
	scheduleRepo := &authCacheInvalidationOutboxStub{scheduleErr: errors.New("schedule unavailable")}
	worker := NewAuthCacheInvalidationWorker(scheduleRepo, &authCacheInvalidationCacheStub{}, nil)
	worker.processEvent(context.Background(), AuthCacheInvalidationEvent{ID: 1, CacheKey: "cache-key"})
	require.EqualValues(t, 1, worker.failures.Load())

	ackRepo := &authCacheInvalidationOutboxStub{deleteErr: errors.New("ack unavailable")}
	worker = NewAuthCacheInvalidationWorker(ackRepo, &authCacheInvalidationCacheStub{}, nil)
	worker.processEvent(context.Background(), AuthCacheInvalidationEvent{ID: 2, CacheKey: "cache-key", Stage: 1})
	require.EqualValues(t, 1, worker.failures.Load())

	retryRepo := &authCacheInvalidationOutboxStub{retryErr: errors.New("retry release unavailable")}
	cache := &authCacheInvalidationCacheStub{deleteErr: errors.New("redis unavailable")}
	worker = NewAuthCacheInvalidationWorker(retryRepo, cache, nil)
	worker.recordFailure(nil)
	worker.processEvent(context.Background(), AuthCacheInvalidationEvent{ID: 3, CacheKey: "cache-key"})
	require.EqualValues(t, 2, worker.failures.Load())
}

func TestAuthCacheInvalidationWorkerBoundsErrorsAndRetryDelay(t *testing.T) {
	require.Empty(t, boundedAuthInvalidationError(nil))
	require.Len(t, boundedAuthInvalidationError(errors.New(strings.Repeat("x", 2048))), 1024)
	require.Greater(t, authInvalidationRetryDelay(0), time.Duration(0))
	require.LessOrEqual(t, authInvalidationRetryDelay(10), 10*time.Minute)
}

func TestAuthCacheInvalidationWorkerHealthReadsOutboxStats(t *testing.T) {
	createdAt := time.Now().Add(-2 * time.Second)
	repo := &authCacheInvalidationOutboxStub{stats: AuthCacheInvalidationOutboxStats{
		Pending:         3,
		OldestCreatedAt: &createdAt,
		MaxAttempts:     4,
		LastError:       "last failure",
	}}
	worker := NewAuthCacheInvalidationWorker(repo, &authCacheInvalidationCacheStub{}, nil)

	health := worker.Health(context.Background())
	require.EqualValues(t, 3, health.Pending)
	require.Equal(t, 4, health.MaxAttempts)
	require.Equal(t, "last failure", health.LastError)
	require.GreaterOrEqual(t, health.OldestLag, time.Second)
}

func TestAuthCacheInvalidationWorkerHealthHandlesNilAndStatsErrors(t *testing.T) {
	worker := NewAuthCacheInvalidationWorker(nil, &authCacheInvalidationCacheStub{}, nil)
	require.False(t, worker.Health(context.Background()).Running)

	worker = NewAuthCacheInvalidationWorker(&authCacheInvalidationOutboxStub{statsErr: errors.New("stats unavailable")}, &authCacheInvalidationCacheStub{}, nil)
	require.Equal(t, "stats unavailable", worker.Health(context.Background()).StatsError)

	var nilWorker *AuthCacheInvalidationWorker
	require.Equal(t, time.Duration(35*time.Second), nilWorker.Health(context.Background()).HealthySLA)
}

func TestAuthCacheInvalidationWorkerUsesLocalL1Invalidation(t *testing.T) {
	local := &APIKeyService{}
	local.initAuthCache(&config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L1Size: 8, L1TTLSeconds: 60}})
	local.invalidateLocalAuthCache("cache-key")
	local = nil
	local.invalidateLocalAuthCache("cache-key")
}
