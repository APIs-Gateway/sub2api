//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type authCacheInvalidationCacheStub struct {
	apiKeyCacheStub
	published []string
	deleteErr error
}

func (s *authCacheInvalidationCacheStub) DeleteAuthCache(ctx context.Context, key string) error {
	s.deleteAuthKeys = append(s.deleteAuthKeys, key)
	return s.deleteErr
}

func (s *authCacheInvalidationCacheStub) PublishAuthCacheInvalidation(ctx context.Context, key string) error {
	s.published = append(s.published, key)
	return nil
}

type authCacheInvalidationOutboxStub struct {
	stats     AuthCacheInvalidationOutboxStats
	scheduled []int64
	deleted   []int64
	retried   []int64
}

func (s *authCacheInvalidationOutboxStub) Claim(context.Context, string, int, time.Duration) ([]AuthCacheInvalidationEvent, error) {
	return nil, nil
}

func (s *authCacheInvalidationOutboxStub) DeleteClaimed(_ context.Context, id int64, _ string) error {
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *authCacheInvalidationOutboxStub) ScheduleSecondPass(_ context.Context, id int64, _ string, _ time.Time) error {
	s.scheduled = append(s.scheduled, id)
	return nil
}

func (s *authCacheInvalidationOutboxStub) RetryClaimed(_ context.Context, id int64, _ string, _ time.Time, _ string) error {
	s.retried = append(s.retried, id)
	return nil
}

func (s *authCacheInvalidationOutboxStub) Stats(context.Context) (AuthCacheInvalidationOutboxStats, error) {
	return s.stats, nil
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
