package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsRepositoryBatchUpsertIngressRejectsUsesFixedMultiRowChunks(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &opsRepository{db: db}
	now := time.Now().UTC().Truncate(time.Minute)
	items := make([]*service.OpsIngressRejectAggregate, opsIngressRejectUpsertChunkSize+1)
	for i := range items {
		items[i] = &service.OpsIngressRejectAggregate{
			BucketStart: now, RejectReason: "invalid_api_key", RouteFamily: "messages",
			Protocol: "anthropic", ClientIP: "192.0.2.0/24", RequestCount: 1, FirstSeen: now, LastSeen: now,
		}
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO ops_ingress_reject_aggregates").WillReturnResult(sqlmock.NewResult(0, int64(opsIngressRejectUpsertChunkSize)))
	mock.ExpectExec("INSERT INTO ops_ingress_reject_aggregates").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.BatchUpsertIngressRejects(context.Background(), items))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsRepositoryBatchUpsertIngressRejectsSkipsEmptyItems(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &opsRepository{db: db}

	// A nil/empty slice must short-circuit before ever touching the database.
	require.NoError(t, repo.BatchUpsertIngressRejects(context.Background(), nil))
	require.NoError(t, mock.ExpectationsWereMet())

	// A non-empty slice whose entries are all nil/zero-count still opens and commits
	// a transaction (chunking happens per-batch, not pre-filtered), but must not
	// issue an INSERT for a chunk with no valid rows.
	mock.ExpectBegin()
	mock.ExpectCommit()
	require.NoError(t, repo.BatchUpsertIngressRejects(context.Background(), []*service.OpsIngressRejectAggregate{
		nil, {RequestCount: 0},
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsRepositoryBatchUpsertIngressRejectsPropagatesExecError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &opsRepository{db: db}
	now := time.Now().UTC()

	mock.ExpectBegin()
	execErr := errors.New("database unavailable")
	mock.ExpectExec("INSERT INTO ops_ingress_reject_aggregates").WillReturnError(execErr)
	mock.ExpectRollback()
	err = repo.BatchUpsertIngressRejects(context.Background(), []*service.OpsIngressRejectAggregate{
		{BucketStart: now, RejectReason: "invalid_api_key", RouteFamily: "messages", Protocol: "anthropic",
			ClientIP: "192.0.2.0/24", RequestCount: 1, FirstSeen: now, LastSeen: now},
	})
	require.ErrorIs(t, err, execErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsIngressRejectUpsertQueryUsesEachDatabaseDialect(t *testing.T) {
	postgres := opsIngressRejectUpsertQuery(migrationDatabasePostgres, 1)
	require.Contains(t, postgres, "VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)")
	require.Contains(t, postgres, "ON CONFLICT (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id)")
	require.Contains(t, postgres, "EXCLUDED.request_count")
	require.Contains(t, postgres, "updated_at = NOW()")

	mysql := opsIngressRejectUpsertQuery(migrationDatabaseMySQL, 1)
	require.Contains(t, mysql, "VALUES (?,?,?,?,?,?,?,?,?,?)")
	require.Contains(t, mysql, "ON DUPLICATE KEY UPDATE")
	require.Contains(t, mysql, "VALUES(request_count)")
	require.Contains(t, mysql, "updated_at = CURRENT_TIMESTAMP")
	require.NotContains(t, mysql, "$1")
	require.NotContains(t, mysql, "EXCLUDED")

	sqlite := opsIngressRejectUpsertQuery(migrationDatabaseSQLite, 1)
	require.Contains(t, sqlite, "VALUES (?,?,?,?,?,?,?,?,?,?)")
	require.Contains(t, sqlite, "ON CONFLICT (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id)")
	require.Contains(t, sqlite, "DO UPDATE SET request_count")
	require.Contains(t, sqlite, "excluded.request_count")
	require.Contains(t, sqlite, "MIN(ops_ingress_reject_aggregates.first_seen, excluded.first_seen)")
	require.NotContains(t, sqlite, "$1")
}

func TestOpsIngressRejectPlaceholderUsesQuestionMarkOutsidePostgres(t *testing.T) {
	require.Equal(t, "$4", opsIngressRejectPlaceholder(migrationDatabasePostgres, 4))
	require.Equal(t, "?", opsIngressRejectPlaceholder(migrationDatabaseMySQL, 4))
	require.Equal(t, "?", opsIngressRejectPlaceholder(migrationDatabaseSQLite, 4))
}

func TestOpsRepositoryListIngressRejectsAppliesFiltersAndPagination(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &opsRepository{db: db}

	now := time.Now().UTC()
	userID := int64(42)
	filter := &service.OpsIngressRejectFilter{
		StartTime: &now, RejectReason: "invalid_api_key", RouteFamily: "messages", Protocol: "anthropic",
		ClientIP: "192.0.2.0/24", UserID: &userID, Page: 2, PageSize: 10,
	}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM ops_ingress_reject_aggregates")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT id,bucket_start,reject_reason,route_family,protocol,client_ip,user_id,api_key_id,request_count,first_seen,last_seen").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "bucket_start", "reject_reason", "route_family", "protocol", "client_ip",
			"user_id", "api_key_id", "request_count", "first_seen", "last_seen",
		}).AddRow(int64(1), now, "invalid_api_key", "messages", "anthropic", "192.0.2.0/24", int64(42), int64(0), int64(5), now, now))

	result, err := repo.ListIngressRejects(context.Background(), filter)
	require.NoError(t, err)
	require.Equal(t, 1, result.Total)
	require.Equal(t, 2, result.Page)
	require.Equal(t, 10, result.PageSize)
	require.Len(t, result.Items, 1)
	require.Equal(t, int64(42), *result.Items[0].UserID)
	require.Nil(t, result.Items[0].APIKeyID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsRepositoryListIngressRejectsDefaultsAndBoundsPageSize(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &opsRepository{db: db}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM ops_ingress_reject_aggregates")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT id,bucket_start,reject_reason,route_family,protocol,client_ip,user_id,api_key_id,request_count,first_seen,last_seen").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "bucket_start", "reject_reason", "route_family", "protocol", "client_ip",
			"user_id", "api_key_id", "request_count", "first_seen", "last_seen",
		}))

	result, err := repo.ListIngressRejects(context.Background(), &service.OpsIngressRejectFilter{PageSize: 10000})
	require.NoError(t, err)
	require.Equal(t, 1, result.Page)
	require.Equal(t, 200, result.PageSize)
	require.Empty(t, result.Items)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsRepositoryListIngressRejectsRejectsInvalidDependencies(t *testing.T) {
	var repo *opsRepository
	_, err := repo.ListIngressRejects(context.Background(), nil)
	require.Error(t, err)

	repo = &opsRepository{}
	_, err = repo.ListIngressRejects(context.Background(), nil)
	require.Error(t, err)
}

func TestOpsRepositoryListIngressRejectsPropagatesCountError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &opsRepository{db: db}

	countErr := errors.New("count failed")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM ops_ingress_reject_aggregates")).WillReturnError(countErr)
	_, err = repo.ListIngressRejects(context.Background(), nil)
	require.ErrorIs(t, err, countErr)
	require.NoError(t, mock.ExpectationsWereMet())
}
