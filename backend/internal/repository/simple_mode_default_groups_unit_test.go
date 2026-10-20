package repository

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

var errSimpleModeDefaultGroupsMockDB = errors.New("mock db failure")

func newSimpleModeDefaultGroupsSQLMockClient(t *testing.T) (*dbent.Client, sqlmock.Sqlmock) {
	t.Helper()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })

	return client, mock
}

func TestBackfillSimpleModeGrokDefaultImageGeneration_PropagatesUpdateError(t *testing.T) {
	client, mock := newSimpleModeDefaultGroupsSQLMockClient(t)

	mock.ExpectExec(".*").WillReturnError(errSimpleModeDefaultGroupsMockDB)

	err := backfillSimpleModeGrokDefaultImageGeneration(context.Background(), client)
	require.Error(t, err)
	require.Contains(t, err.Error(), "backfill auto-created grok default image generation")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureSimpleModeDefaultGroups_PropagatesBackfillError(t *testing.T) {
	client, mock := newSimpleModeDefaultGroupsSQLMockClient(t)

	mock.ExpectExec(".*").WillReturnError(errSimpleModeDefaultGroupsMockDB)

	err := ensureSimpleModeDefaultGroups(context.Background(), client)
	require.Error(t, err)
	require.Contains(t, err.Error(), "backfill auto-created grok default image generation")
	require.NoError(t, mock.ExpectationsWereMet())
}
