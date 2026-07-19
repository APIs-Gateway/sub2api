package repository

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAuthCacheInvalidationOutboxRepositoryLifecycle(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := &authCacheInvalidationOutboxRepository{db: db}
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	mock.ExpectQuery("(?s)WITH candidates.*FOR UPDATE SKIP LOCKED.*RETURNING").
		WithArgs("worker-1", 3, int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "cache_key", "attempts", "delivery_stage", "created_at"}).
			AddRow(int64(7), strings.Repeat("a", 64)+" ", 2, 0, createdAt))

	events, err := repo.Claim(context.Background(), "worker-1", 3, 30*time.Second)
	require.NoError(t, err)
	require.Equal(t, []service.AuthCacheInvalidationEvent{{
		ID: 7, CacheKey: strings.Repeat("a", 64), Attempts: 2, Stage: 0, CreatedAt: createdAt,
	}}, events)

	availableAt := time.Now().UTC().Add(time.Minute)
	mock.ExpectExec("(?s)UPDATE auth_cache_invalidation_outbox.*delivery_stage = 1").
		WithArgs(int64(7), "worker-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.ScheduleSecondPass(context.Background(), 7, "worker-1", availableAt))

	mock.ExpectExec("(?s)UPDATE auth_cache_invalidation_outbox.*attempts = attempts \\+ 1").
		WithArgs(int64(7), "worker-1", sqlmock.AnyArg(), "redis unavailable").
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.RetryClaimed(context.Background(), 7, "worker-1", availableAt, "redis unavailable"))

	mock.ExpectExec("(?s)DELETE FROM auth_cache_invalidation_outbox").
		WithArgs(int64(7), "worker-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.DeleteClaimed(context.Background(), 7, "worker-1"))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*), MIN(created_at), COALESCE(MAX(attempts), 0)")).
		WillReturnRows(sqlmock.NewRows([]string{"count", "min", "max", "last_error"}).
			AddRow(int64(3), createdAt, 4, "redis unavailable"))
	stats, err := repo.Stats(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 3, stats.Pending)
	require.Equal(t, createdAt, *stats.OldestCreatedAt)
	require.Equal(t, 4, stats.MaxAttempts)
	require.Equal(t, "redis unavailable", stats.LastError)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthCacheInvalidationOutboxRepositoryRejectsInvalidDependencies(t *testing.T) {
	var repo *authCacheInvalidationOutboxRepository
	_, err := repo.Claim(context.Background(), "worker", 1, time.Second)
	require.Error(t, err)

	repo = &authCacheInvalidationOutboxRepository{}
	_, err = repo.Stats(context.Background())
	require.Error(t, err)
	require.Error(t, repo.DeleteClaimed(context.Background(), 1, "worker"))
}

func TestAuthCacheInvalidationOutboxRepositoryConstructsAndRejectsClaimInput(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := NewAuthCacheInvalidationOutboxRepository(db)
	require.NotNil(t, repo)
	_, err = repo.Claim(context.Background(), " ", 1, time.Second)
	require.Error(t, err)
}

func TestAuthCacheInvalidationOutboxRepositoryReportsLostClaims(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := &authCacheInvalidationOutboxRepository{db: db}

	mock.ExpectExec("(?s)UPDATE auth_cache_invalidation_outbox.*delivery_stage = 1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	require.Error(t, repo.ScheduleSecondPass(context.Background(), 1, "worker", time.Now()))

	mock.ExpectExec("(?s)UPDATE auth_cache_invalidation_outbox.*attempts = attempts \\+ 1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	require.Error(t, repo.RetryClaimed(context.Background(), 1, "worker", time.Now(), "failure"))

	mock.ExpectExec("(?s)DELETE FROM auth_cache_invalidation_outbox").
		WillReturnResult(sqlmock.NewResult(0, 0))
	require.Error(t, repo.DeleteClaimed(context.Background(), 1, "worker"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthCacheInvalidationOutboxRepositoryPropagatesQueryErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := &authCacheInvalidationOutboxRepository{db: db}
	wantErr := errors.New("database unavailable")
	mock.ExpectQuery("(?s)WITH candidates.*FOR UPDATE SKIP LOCKED.*RETURNING").
		WillReturnError(wantErr)
	_, err = repo.Claim(context.Background(), "worker", 0, 0)
	require.ErrorIs(t, err, wantErr)
	require.NoError(t, mock.ExpectationsWereMet())
}
