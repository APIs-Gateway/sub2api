package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestAccountRepositoryListDueUpstreamBillingProbeAccountsBoundsQuery(t *testing.T) {
	db, mock := newSQLMock(t)
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	var capturedSQL string
	mock.ExpectQuery("WITH candidates AS").
		WithArgs(now, 20).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	repo := newAccountRepositoryWithSQL(nil, captureQuerySQL{db: db, captured: &capturedSQL}, nil)

	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), now, 20)

	require.NoError(t, err)
	require.Empty(t, accounts)
	normalized := normalizeSQLWhitespace(capturedSQL)
	require.Contains(t, normalized, "deleted_at IS NULL")
	require.Contains(t, normalized, "status = 'active'")
	require.Contains(t, normalized, "platform = 'openai'")
	require.Contains(t, normalized, "type = 'apikey'")
	require.Contains(t, normalized, `extra @> '{"upstream_billing_probe_enabled": true}'::jsonb`)
	require.Contains(t, normalized, "jsonb_path_query_first_tz")
	require.Contains(t, normalized, "parsed AS MATERIALIZED")
	require.Contains(t, normalized, "parsed_next_probe_at::timestamptz <= $1")
	require.Contains(t, normalized, "LIMIT $2")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryListDueUpstreamBillingProbeAccountsRejectsNonPositiveLimit(t *testing.T) {
	repo := newAccountRepositoryWithSQL(nil, nil, nil)

	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), time.Now(), 0)

	require.NoError(t, err)
	require.Empty(t, accounts)
}

func TestAccountRepositoryListDueUpstreamBillingProbeAccountsQueryAndRowErrors(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)

	t.Run("query error", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery("WITH candidates").WithArgs(now, 20).WillReturnError(sql.ErrConnDone)
		repo := newAccountRepositoryWithSQL(nil, db, nil)
		_, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), now, 20)
		require.ErrorIs(t, err, sql.ErrConnDone)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("scan error", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery("WITH candidates").WithArgs(now, 20).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("not-an-id"))
		repo := newAccountRepositoryWithSQL(nil, db, nil)
		_, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), now, 20)
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rows error", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery("WITH candidates").WithArgs(now, 20).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)).AddRow(int64(43)).RowError(1, sql.ErrNoRows))
		repo := newAccountRepositoryWithSQL(nil, db, nil)
		_, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), now, 20)
		require.ErrorIs(t, err, sql.ErrNoRows)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestAccountRepositoryListDueUpstreamBillingProbeAccountsHydratesIDs(t *testing.T) {
	db, mock := newSQLMock(t)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	mock.ExpectQuery("WITH candidates").WithArgs(now, 20).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))
	// An empty account result exercises the bounded hydration handoff without
	// requiring a full Ent account fixture.
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	repo := newAccountRepositoryWithSQL(client, db, nil)
	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), now, 20)
	require.NoError(t, err)
	require.Empty(t, accounts)
	require.NoError(t, mock.ExpectationsWereMet())
}
