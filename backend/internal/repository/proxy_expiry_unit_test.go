//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

// ---------------------------------------------------------------------------
// sweepOneExpiredProxyOnExec (Postgres raw-SQL path, client == nil forces it)
// ---------------------------------------------------------------------------

func TestSweepOneExpiredProxyOnExecReturnsErrorWhenUpdateProxiesExecFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	execErr := errors.New("update proxies exec failed")
	expectConditionalProxyExpiry(mock, expiredProxySnapshot(9601)).
		WillReturnError(execErr)

	repo := &proxyRepository{}
	accountIDs, err := repo.sweepOneExpiredProxyOnExec(context.Background(), nil, db, expiredProxySnapshot(9601), sweepTestNow, nil, true)

	require.ErrorIs(t, err, execErr)
	require.Nil(t, accountIDs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSweepOneExpiredProxyOnExecReturnsErrorWhenAccountsQueryFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	queryErr := errors.New("update accounts query failed")
	expectConditionalProxyExpiry(mock, expiredProxySnapshot(9602)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)UPDATE accounts.*RETURNING id`).
		WithArgs(int64(9602)).
		WillReturnError(queryErr)

	repo := &proxyRepository{}
	accountIDs, err := repo.sweepOneExpiredProxyOnExec(context.Background(), nil, db, expiredProxySnapshot(9602), sweepTestNow, nil, true)

	require.ErrorIs(t, err, queryErr)
	require.Nil(t, accountIDs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSweepOneExpiredProxyOnExecReturnsErrorWhenAccountIDScanFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	expectConditionalProxyExpiry(mock, expiredProxySnapshot(9603)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)UPDATE accounts.*RETURNING id`).
		WithArgs(int64(9603)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("not-an-id"))

	repo := &proxyRepository{}
	accountIDs, err := repo.sweepOneExpiredProxyOnExec(context.Background(), nil, db, expiredProxySnapshot(9603), sweepTestNow, nil, true)

	require.Error(t, err)
	require.Nil(t, accountIDs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSweepOneExpiredProxyOnExecReturnsErrorWhenRowsIterationFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	rowsErr := errors.New("rows iteration failed")
	expectConditionalProxyExpiry(mock, expiredProxySnapshot(9604)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)UPDATE accounts.*RETURNING id`).
		WithArgs(int64(9604)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(70001)).RowError(0, rowsErr))

	repo := &proxyRepository{}
	accountIDs, err := repo.sweepOneExpiredProxyOnExec(context.Background(), nil, db, expiredProxySnapshot(9604), sweepTestNow, nil, true)

	require.ErrorIs(t, err, rowsErr)
	require.Nil(t, accountIDs)
	require.NoError(t, mock.ExpectationsWereMet())
}

// NOTE: proxy_repo.go's final `if err := rows.Close(); err != nil { return
// nil, err }` (after the scan loop and the rows.Err() check) is unreachable
// in practice: Go's database/sql auto-closes *sql.Rows internally the
// moment the driver's Next() reports EOF (see sql.Rows.Next -> nextLocked,
// which calls Close() itself whenever it needs to stop iterating), and any
// driver-level close error surfaces through that auto-close into
// rows.Err(), not through a later explicit Close() call. sql.Rows.Close()
// is a no-op (returns nil) once already closed, so a *second*, explicit
// Close() can never itself return a fresh error once the loop has ended
// normally. Verified empirically with go-sqlmock's Rows.CloseError: setting
// it on a mocked result set surfaces the error via rows.Err() (already
// covered by TestSweepOneExpiredProxyOnExecReturnsErrorWhenRowsIterationFails-
// style RowError tests), never via the trailing rows.Close() branch. Left
// unaddressed here; flagged to the coordinator as likely dead code.

// ---------------------------------------------------------------------------
// sweepOneExpiredProxy (tx wrapper): Tx()/Commit() failures via sqlmock-backed
// Postgres-dialect ent client.
// ---------------------------------------------------------------------------

func TestSweepOneExpiredProxyReturnsErrorWhenBeginTxFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	beginErr := errors.New("begin tx failed")
	mock.ExpectBegin().WillReturnError(beginErr)

	repo := &proxyRepository{client: client}
	accountIDs, err := repo.sweepOneExpiredProxy(context.Background(), expiredProxySnapshot(9611), sweepTestNow, nil, true)

	require.Error(t, err)
	require.NotErrorIs(t, err, dbent.ErrTxStarted)
	require.Nil(t, accountIDs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSweepOneExpiredProxyReturnsErrorWhenCommitFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	commitErr := errors.New("commit failed")
	mock.ExpectBegin()
	expectConditionalProxyExpiry(mock, expiredProxySnapshot(9612)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)UPDATE accounts.*RETURNING id`).
		WithArgs(int64(9612)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectCommit().WillReturnError(commitErr)

	repo := &proxyRepository{client: client}
	accountIDs, err := repo.sweepOneExpiredProxy(context.Background(), expiredProxySnapshot(9612), sweepTestNow, nil, true)

	require.Error(t, err)
	require.Nil(t, accountIDs)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// sweepOneExpiredProxyOnEnt (non-Postgres ent fallback path), backed by a
// real in-memory SQLite ent client (dialect != Postgres routes here).
// ---------------------------------------------------------------------------

func TestSweepOneExpiredProxyOnEntReturnsErrorWhenProxyUpdateFails(t *testing.T) {
	db, client := newSQLiteProbePersistenceClient(t)
	ctx := context.Background()

	proxyRow, err := client.Proxy.Create().
		SetName("ent-update-fail-proxy").
		SetProtocol("http").
		SetHost("ent-update-fail.example").
		SetPort(8080).
		SetStatus(service.StatusActive).
		SetExpiresAt(time.Now().Add(-time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	snapshot := *proxyEntityToService(proxyRow)

	require.NoError(t, db.Close())

	repo := &proxyRepository{client: client}
	accountIDs, err := repo.sweepOneExpiredProxyOnEnt(ctx, client, snapshot, time.Now(), nil, true)

	require.Error(t, err)
	require.Nil(t, accountIDs)
}

func TestSweepOneExpiredProxyOnEntReturnsErrorWhenAccountsQueryFails(t *testing.T) {
	db, client := newSQLiteProbePersistenceClient(t)
	ctx := context.Background()

	proxyRow, err := client.Proxy.Create().
		SetName("ent-query-fail-proxy").
		SetProtocol("http").
		SetHost("ent-query-fail.example").
		SetPort(8080).
		SetStatus(service.StatusActive).
		SetExpiresAt(time.Now().Add(-time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	snapshot := *proxyEntityToService(proxyRow)

	_, err = db.Exec("DROP TABLE accounts")
	require.NoError(t, err)

	repo := &proxyRepository{client: client}
	accountIDs, err := repo.sweepOneExpiredProxyOnEnt(ctx, client, snapshot, time.Now(), nil, true)

	require.Error(t, err)
	require.Nil(t, accountIDs)

	// The proxy status update (the step before the failing query) must have
	// already been committed to the (still intact) proxies table.
	updated, getErr := client.Proxy.Get(ctx, proxyRow.ID)
	require.NoError(t, getErr)
	require.Equal(t, service.StatusExpired, updated.Status)
}

func TestSweepOneExpiredProxyOnEntReturnsErrorWhenAccountSaveFails(t *testing.T) {
	_, client := newSQLiteProbePersistenceClient(t)
	ctx := context.Background()

	proxyRow, err := client.Proxy.Create().
		SetName("ent-save-fail-proxy").
		SetProtocol("http").
		SetHost("ent-save-fail.example").
		SetPort(8080).
		SetStatus(service.StatusActive).
		SetExpiresAt(time.Now().Add(-time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	snapshot := *proxyEntityToService(proxyRow)

	_, err = client.Account.Create().
		SetName("ent-save-fail-account").
		SetPlatform(service.PlatformAnthropic).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{"api_key": "sk-save-fail"}).
		SetExtra(map[string]any{}).
		SetProxyID(proxyRow.ID).
		SetConcurrency(1).
		SetPriority(0).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetAutoPauseOnExpired(false).
		Save(ctx)
	require.NoError(t, err)

	// A fallback target proxy ID that does not exist trips the proxy_id
	// foreign-key constraint (SQLite opened with _fk=1) when the account
	// update tries to point at it, without touching the earlier successful
	// proxy-update / account-query steps.
	bogusTarget := int64(9999999)

	repo := &proxyRepository{client: client}
	accountIDs, err := repo.sweepOneExpiredProxyOnEnt(ctx, client, snapshot, time.Now(), &bogusTarget, true)

	require.Error(t, err)
	require.Nil(t, accountIDs)
}

// ---------------------------------------------------------------------------
// SweepExpiredProxies: outer scheduler-outbox enqueue failure must be logged,
// not propagated (the sweep itself already committed).
// ---------------------------------------------------------------------------

func TestSweepExpiredProxiesLogsOutboxErrorAndReturnsChangedCount(t *testing.T) {
	db, client := newSQLiteProbePersistenceClient(t)
	ctx := context.Background()

	past := time.Now().Add(-time.Hour)
	repo := newProxyRepositoryWithSQL(client, client)
	p := &service.Proxy{
		Name: "outbox-fail-proxy", Protocol: "http", Host: "outbox-fail.example", Port: 8080,
		Status: service.StatusActive, FallbackMode: service.FallbackModeDirect, ExpiryWarnDays: 7,
		ExpiresAt: &past,
	}
	require.NoError(t, repo.Create(ctx, p))

	_, err := client.Account.Create().
		SetName("outbox-fail-account").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{"api_key": "sk-outbox"}).
		SetExtra(map[string]any{}).
		SetProxyID(p.ID).
		SetConcurrency(1).
		SetPriority(0).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetAutoPauseOnExpired(false).
		Save(ctx)
	require.NoError(t, err)

	_, err = db.Exec("DROP TABLE scheduler_outbox")
	require.NoError(t, err)

	changed, err := repo.SweepExpiredProxies(ctx, time.Now())

	require.NoError(t, err, "a scheduler-outbox enqueue failure must be logged, not propagated")
	require.EqualValues(t, 1, changed)
}

// ---------------------------------------------------------------------------
// Stale snapshot guard: the conditional UPDATE must skip account rewrites when
// the proxy was renewed / disabled / reconfigured after the snapshot was taken.
// ---------------------------------------------------------------------------

func TestSweepOneExpiredProxyOnExecSkipsAccountsWhenSnapshotIsStale(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	backupID := int64(9702)
	snapshot := expiredProxySnapshot(9701)
	snapshot.FallbackMode = service.FallbackModeProxy
	snapshot.BackupProxyID = &backupID
	expectConditionalProxyExpiry(mock, snapshot).WillReturnResult(sqlmock.NewResult(0, 0))

	repo := &proxyRepository{}
	target := backupID
	accountIDs, err := repo.sweepOneExpiredProxyOnExec(context.Background(), nil, db, snapshot, sweepTestNow, &target, true)

	require.NoError(t, err)
	require.Nil(t, accountIDs)
	require.NoError(t, mock.ExpectationsWereMet(), "no account UPDATE may run for a stale snapshot")
}

func TestSweepOneExpiredProxyOnExecReturnsRowsAffectedError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	affectedErr := errors.New("rows affected failed")
	expectConditionalProxyExpiry(mock, expiredProxySnapshot(9703)).WillReturnResult(sqlmock.NewErrorResult(affectedErr))

	repo := &proxyRepository{}
	accountIDs, err := repo.sweepOneExpiredProxyOnExec(context.Background(), nil, db, expiredProxySnapshot(9703), sweepTestNow, nil, true)

	require.ErrorIs(t, err, affectedErr)
	require.Nil(t, accountIDs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSweepOneExpiredProxyOnEntHonorsSnapshotAndPreservesOrigin(t *testing.T) {
	_, client := newSQLiteProbePersistenceClient(t)
	ctx := context.Background()
	now := time.Now()
	past := now.Add(-time.Hour)

	mkProxy := func(name string, expiresAt *time.Time) *dbent.Proxy {
		builder := client.Proxy.Create().
			SetName(name).
			SetProtocol("http").
			SetHost(name + ".example").
			SetPort(8080).
			SetStatus(service.StatusActive)
		if expiresAt != nil {
			builder.SetExpiresAt(*expiresAt)
		}
		row, err := builder.Save(ctx)
		require.NoError(t, err)
		return row
	}
	mkAccount := func(name string, proxyID int64) *dbent.Account {
		row, err := client.Account.Create().
			SetName(name).
			SetPlatform(service.PlatformAnthropic).
			SetType(service.AccountTypeAPIKey).
			SetCredentials(map[string]any{"api_key": "sk-" + name}).
			SetExtra(map[string]any{}).
			SetProxyID(proxyID).
			SetConcurrency(1).
			SetPriority(0).
			SetStatus(service.StatusActive).
			SetSchedulable(true).
			SetAutoPauseOnExpired(false).
			Save(ctx)
		require.NoError(t, err)
		return row
	}
	repo := &proxyRepository{client: client}

	t.Run("snapshot without expiry is ignored", func(t *testing.T) {
		row := mkProxy("no-expiry", nil)
		accountIDs, err := repo.sweepOneExpiredProxyOnEnt(ctx, client, *proxyEntityToService(row), now, nil, true)
		require.NoError(t, err)
		require.Nil(t, accountIDs)
		got, err := client.Proxy.Get(ctx, row.ID)
		require.NoError(t, err)
		require.Equal(t, service.StatusActive, got.Status)
	})

	t.Run("renewed proxy keeps accounts", func(t *testing.T) {
		row := mkProxy("renewed", &past)
		account := mkAccount("renewed-account", row.ID)
		snapshot := *proxyEntityToService(row)
		_, err := client.Proxy.UpdateOneID(row.ID).SetExpiresAt(now.Add(24 * time.Hour)).Save(ctx)
		require.NoError(t, err)

		accountIDs, err := repo.sweepOneExpiredProxyOnEnt(ctx, client, snapshot, now, nil, true)
		require.NoError(t, err)
		require.Nil(t, accountIDs)
		got, err := client.Proxy.Get(ctx, row.ID)
		require.NoError(t, err)
		require.Equal(t, service.StatusActive, got.Status)
		gotAccount, err := client.Account.Get(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, row.ID, *gotAccount.ProxyID)
		require.Nil(t, gotAccount.ProxyFallbackOriginID)
	})

	t.Run("repeated fallback keeps first origin", func(t *testing.T) {
		original := mkProxy("origin", &past)
		last := mkProxy("last", nil)
		middle := mkProxy("middle", &past)
		_, err := client.Proxy.UpdateOneID(middle.ID).SetFallbackMode(service.FallbackModeProxy).SetBackupProxyID(last.ID).Save(ctx)
		require.NoError(t, err)
		middle, err = client.Proxy.Get(ctx, middle.ID)
		require.NoError(t, err)
		account := mkAccount("repeat-account", original.ID)

		middleID := middle.ID
		accountIDs, err := repo.sweepOneExpiredProxyOnEnt(ctx, client, *proxyEntityToService(original), now, &middleID, true)
		require.NoError(t, err)
		require.Equal(t, []int64{account.ID}, accountIDs)

		lastID := last.ID
		accountIDs, err = repo.sweepOneExpiredProxyOnEnt(ctx, client, *proxyEntityToService(middle), now, &lastID, true)
		require.NoError(t, err)
		require.Equal(t, []int64{account.ID}, accountIDs, "accounts already in fallback must still be rerouted")

		gotAccount, err := client.Account.Get(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, last.ID, *gotAccount.ProxyID)
		require.Equal(t, original.ID, *gotAccount.ProxyFallbackOriginID)
	})
}
