//go:build unit

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func TestAccountRepositoryListDueUpstreamBillingProbeAccountsUsesPortableQuery(t *testing.T) {
	db, mock := newSQLMock(t)
	var capturedSQL string
	mock.ExpectQuery("SELECT id, extra FROM accounts").
		WillReturnRows(sqlmock.NewRows([]string{"id", "extra"}))
	repo := newAccountRepositoryWithSQL(nil, captureQuerySQL{db: db, captured: &capturedSQL}, nil)

	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), time.Now(), 20)

	require.NoError(t, err)
	require.Empty(t, accounts)
	normalized := normalizeSQLWhitespace(capturedSQL)
	require.Contains(t, normalized, "SELECT id, extra FROM accounts")
	require.Contains(t, normalized, "deleted_at IS NULL")
	require.Contains(t, normalized, "status = 'active'")
	require.Contains(t, normalized, "platform = 'openai'")
	require.Contains(t, normalized, "type = 'apikey'")
	for _, fragment := range []string{
		"#>>", "#>", "@>", "::jsonb", "jsonb_path_query_first_tz", "MATERIALIZED", "timestamptz", "NULLS FIRST",
	} {
		require.NotContains(t, normalized, fragment)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryListDueUpstreamBillingProbeAccountsRejectsNonPositiveLimit(t *testing.T) {
	repo := newAccountRepositoryWithSQL(nil, nil, nil)

	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), time.Now(), 0)

	require.NoError(t, err)
	require.Empty(t, accounts)
}

func TestAccountRepositoryListDueUpstreamBillingProbeAccountsRejectsMissingSQL(t *testing.T) {
	repo := newAccountRepositoryWithSQL(nil, nil, nil)

	_, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), time.Now(), 20)

	require.EqualError(t, err, "account repository SQL executor not configured")
}

func TestAccountRepositoryListDueUpstreamBillingProbeAccountsQueryAndRowErrors(t *testing.T) {
	testQuery := "SELECT id, extra FROM accounts"

	t.Run("query error", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery(testQuery).WillReturnError(sql.ErrConnDone)
		repo := newAccountRepositoryWithSQL(nil, db, nil)
		_, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), time.Now(), 20)
		require.ErrorIs(t, err, sql.ErrConnDone)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("scan error", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery(testQuery).
			WillReturnRows(sqlmock.NewRows([]string{"id", "extra"}).AddRow("not-an-id", []byte(`{}`)))
		repo := newAccountRepositoryWithSQL(nil, db, nil)
		_, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), time.Now(), 20)
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rows error", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery(testQuery).
			WillReturnRows(sqlmock.NewRows([]string{"id", "extra"}).
				AddRow(int64(42), []byte(`{}`)).
				AddRow(int64(43), []byte(`{}`)).
				RowError(1, sql.ErrNoRows))
		repo := newAccountRepositoryWithSQL(nil, db, nil)
		_, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), time.Now(), 20)
		require.ErrorIs(t, err, sql.ErrNoRows)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestDueUpstreamBillingProbeCandidateIDsFiltersAndOrdersInGo(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	earlier := now.Add(-2 * time.Minute)
	later := now.Add(-time.Minute)
	ids := dueUpstreamBillingProbeCandidateIDs([]upstreamBillingProbeDueCandidate{
		{id: 10, snapshot: &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK, NextProbeAt: future}},
		{id: 20, snapshot: &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK, NextProbeAt: later}},
		{id: 30, snapshot: nil},
		{id: 40, snapshot: &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusFailed, NextProbeAt: earlier}},
		{id: 50, snapshot: &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK, NextProbeAt: earlier}},
	}, now, 3)

	require.Equal(t, []int64{30, 40, 50}, ids)
}

func TestAccountRepositoryListDueUpstreamBillingProbeAccountsSQLite(t *testing.T) {
	db, client := newSQLiteProbePersistenceClient(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	_, err := client.Account.Create().
		SetName("sqlite-due-probe").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{"api_key": "sk-sqlite"}).
		SetExtra(map[string]any{
			service.UpstreamBillingProbeEnabledExtraKey: true,
			service.UpstreamBillingProbeExtraKey: map[string]any{
				"status":        service.UpstreamBillingProbeStatusOK,
				"next_probe_at": now.Add(-time.Minute).Format(time.RFC3339Nano),
			},
		}).
		SetConcurrency(1).
		SetPriority(0).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetAutoPauseOnExpired(false).
		Save(ctx)
	require.NoError(t, err)

	repo := newAccountRepositoryWithSQL(client, db, nil)
	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(ctx, now, 20)

	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, "sqlite-due-probe", accounts[0].Name)
}

func TestAccountRepositoryListDueUpstreamBillingProbeAccountsSQLiteSkipsFutureAndDisabled(t *testing.T) {
	db, client := newSQLiteProbePersistenceClient(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	create := func(name, status string, enabled bool, next time.Time) {
		_, err := client.Account.Create().
			SetName(name).
			SetPlatform(service.PlatformOpenAI).
			SetType(service.AccountTypeAPIKey).
			SetCredentials(map[string]any{"api_key": "sk-" + name}).
			SetExtra(map[string]any{
				service.UpstreamBillingProbeEnabledExtraKey: enabled,
				service.UpstreamBillingProbeExtraKey: map[string]any{
					"status":        service.UpstreamBillingProbeStatusOK,
					"next_probe_at": next.Format(time.RFC3339Nano),
				},
			}).
			SetConcurrency(1).
			SetPriority(0).
			SetStatus(status).
			SetSchedulable(true).
			SetAutoPauseOnExpired(false).
			Save(ctx)
		require.NoError(t, err)
	}
	create("sqlite-future", service.StatusActive, true, now.Add(time.Hour))
	create("sqlite-disabled", service.StatusDisabled, true, now.Add(-time.Minute))
	create("sqlite-disabled-flag", service.StatusActive, false, now.Add(-time.Minute))

	repo := newAccountRepositoryWithSQL(client, db, nil)
	accounts, err := repo.ListDueUpstreamBillingProbeAccounts(ctx, now, 20)

	require.NoError(t, err)
	require.Empty(t, accounts)
}

func TestAccountRepositoryListDueUpstreamBillingProbeAccountsMySQLQueryHasNoDialectSpecificSQL(t *testing.T) {
	db, mock := newSQLMock(t)
	mock.ExpectQuery("SELECT id, extra FROM accounts").
		WillReturnRows(sqlmock.NewRows([]string{"id", "extra"}))
	repo := newAccountRepositoryWithSQL(nil, db, nil)
	_, err := repo.ListDueUpstreamBillingProbeAccounts(context.Background(), time.Now(), 20)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
