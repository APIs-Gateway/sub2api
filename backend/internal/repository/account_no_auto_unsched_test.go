//go:build unit

package repository

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

const (
	exemptExprPlain  = `COALESCE\("accounts"\."extra", '\{\}'::jsonb\) @> '\{"no_auto_unsched": true\}'::jsonb`
	exemptExprWithID = `\(COALESCE\("accounts"\."extra", '\{\}'::jsonb\) @> '\{"no_auto_unsched": true\}'::jsonb OR "accounts"\."id" IN \(3230\)\)`
)

func newNoAutoUnschedMockRepo(t *testing.T) (*accountRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	t.Cleanup(func() { service.SetNoAutoUnschedConfigIDs(nil) })
	return newAccountRepositoryWithSQL(client, db, nil), mock
}

func TestNoAutoUnschedExpr(t *testing.T) {
	service.SetNoAutoUnschedConfigIDs(nil)
	t.Cleanup(func() { service.SetNoAutoUnschedConfigIDs(nil) })

	require.Equal(t,
		`COALESCE(extra, '{}'::jsonb) @> '{"no_auto_unsched": true}'::jsonb`,
		noAutoUnschedRawSQL(""))
	require.Equal(t,
		`COALESCE(a.extra, '{}'::jsonb) @> '{"no_auto_unsched": true}'::jsonb`,
		noAutoUnschedRawSQL("a"))

	service.SetNoAutoUnschedConfigIDs([]int64{3230, 12, 0})
	require.Equal(t,
		`(COALESCE(a.extra, '{}'::jsonb) @> '{"no_auto_unsched": true}'::jsonb OR a.id IN (12, 3230))`,
		noAutoUnschedRawSQL("a"))
}

func TestExtraNoAutoUnsched(t *testing.T) {
	t.Cleanup(func() { service.SetNoAutoUnschedConfigIDs(nil) })
	service.SetNoAutoUnschedConfigIDs(nil)
	require.True(t, extraNoAutoUnsched([]byte(`{"no_auto_unsched": true}`), 1))
	require.False(t, extraNoAutoUnsched([]byte(`{"no_auto_unsched": false}`), 1))
	require.False(t, extraNoAutoUnsched([]byte(`{"no_auto_unsched": "true"}`), 1))
	require.False(t, extraNoAutoUnsched([]byte(`{`), 1))
	require.False(t, extraNoAutoUnsched([]byte(`{}`), 1))
	service.SetNoAutoUnschedConfigIDs([]int64{1})
	require.True(t, extraNoAutoUnsched([]byte(`{}`), 1))
}

func TestGroupAccountSQLHonorsNoAutoUnsched(t *testing.T) {
	service.SetNoAutoUnschedConfigIDs(nil)
	t.Cleanup(func() { service.SetNoAutoUnschedConfigIDs(nil) })

	available := groupAccountAvailableSQL()
	require.Contains(t, available, `@> '{"no_auto_unsched": true}'::jsonb AND (a.status = 'error' OR (a.status = 'active' AND a.schedulable = true)))`)
	require.Contains(t, available, "a.expires_at > NOW()")
	require.Contains(t, available, "a.rate_limit_reset_at <= NOW()")
	limited := groupAccountTemporarilyLimitedSQL()
	require.Contains(t, limited, `AND NOT (COALESCE(a.extra, '{}'::jsonb) @> '{"no_auto_unsched": true}'::jsonb)`)
	require.Contains(t, limited, "a.temp_unschedulable_until > NOW()")
}

// 写侧：自动冷却/置错对带标记账号不落库、不发 outbox；不带标记账号照常。
func TestAccountRepository_AutoPauseWritesAreGatedForExemptAccounts(t *testing.T) {
	service.SetNoAutoUnschedConfigIDs([]int64{3230})

	t.Run("SetRateLimited skipped", func(t *testing.T) {
		repo, mock := newNoAutoUnschedMockRepo(t)
		service.SetNoAutoUnschedConfigIDs([]int64{3230})
		mock.ExpectExec(`(?s)UPDATE "accounts" SET.*"rate_limit_reset_at".*WHERE .*"accounts"\."id" = .*AND \(NOT \(` + exemptExprWithID).
			WillReturnResult(sqlmock.NewResult(0, 0))
		require.NoError(t, repo.SetRateLimited(context.Background(), 3230, time.Now().Add(time.Minute)))
		require.NoError(t, mock.ExpectationsWereMet(), "no outbox insert expected when the write is gated")
	})

	t.Run("SetRateLimited applied", func(t *testing.T) {
		repo, mock := newNoAutoUnschedMockRepo(t)
		mock.ExpectExec(`(?s)UPDATE "accounts" SET.*"rate_limit_reset_at".*AND \(NOT \(` + exemptExprPlain).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
			WillReturnResult(sqlmock.NewResult(1, 1))
		require.NoError(t, repo.SetRateLimited(context.Background(), 5, time.Now().Add(time.Minute)))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("SetOverloaded skipped", func(t *testing.T) {
		repo, mock := newNoAutoUnschedMockRepo(t)
		mock.ExpectExec(`(?s)UPDATE "accounts" SET.*"overload_until".*AND \(NOT \(` + exemptExprPlain).
			WillReturnResult(sqlmock.NewResult(0, 0))
		require.NoError(t, repo.SetOverloaded(context.Background(), 7, time.Now().Add(time.Minute)))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("SetError skipped", func(t *testing.T) {
		repo, mock := newNoAutoUnschedMockRepo(t)
		mock.ExpectExec(`(?s)UPDATE "accounts" SET.*"status".*"error_message".*AND \(NOT \(` + exemptExprPlain).
			WillReturnResult(sqlmock.NewResult(0, 0))
		require.NoError(t, repo.SetError(context.Background(), 7, "boom"))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("SetError applied for non exempt account", func(t *testing.T) {
		repo, mock := newNoAutoUnschedMockRepo(t)
		mock.ExpectExec(`(?s)UPDATE "accounts" SET.*"status".*AND \(NOT \(` + exemptExprPlain).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
			WillReturnResult(sqlmock.NewResult(1, 1))
		require.NoError(t, repo.SetError(context.Background(), 7, "boom"))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("manual SetError bypasses the exemption", func(t *testing.T) {
		repo, mock := newNoAutoUnschedMockRepo(t)
		// 手动置错：UPDATE 不带豁免条件，正常写库并发 outbox。
		mock.ExpectExec(`(?s)UPDATE "accounts" SET.*"status".*`).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
			WillReturnResult(sqlmock.NewResult(1, 1))
		require.NoError(t, repo.SetError(service.WithManualAccountState(context.Background()), 7, "manual"))
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestAccountRepository_SetTempUnschedulableCarriesExemptionGate(t *testing.T) {
	service.SetNoAutoUnschedConfigIDs([]int64{3230})
	t.Cleanup(func() { service.SetNoAutoUnschedConfigIDs(nil) })

	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)

	require.NoError(t, repo.SetTempUnschedulable(context.Background(), 3230, time.Now().Add(time.Minute), "x"))
	require.Len(t, exec.execQueries, 1)
	require.Contains(t, exec.execQueries[0], `AND NOT ((COALESCE(extra, '{}'::jsonb) @> '{"no_auto_unsched": true}'::jsonb OR id IN (3230)))`)
	require.NotContains(t, strings.Join(exec.execQueries, "\n"), "scheduler_outbox")
}

func TestAccountRepository_SetModelRateLimitGateForExemptAccounts(t *testing.T) {
	t.Run("exempt account: update affects no row, existence probe says exempt -> silently skipped", func(t *testing.T) {
		repo, mock := newNoAutoUnschedMockRepo(t)
		mock.ExpectExec(`(?s)UPDATE accounts SET.*AND NOT \(COALESCE\(extra, '\{\}'::jsonb\) @> '\{"no_auto_unsched": true\}'::jsonb\)`).
			WithArgs("gpt-6-luna", sqlmock.AnyArg(), int64(3230)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`(?s)SELECT 1 FROM accounts WHERE id = \$1.*no_auto_unsched`).
			WithArgs(int64(3230)).
			WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(1))
		require.NoError(t, repo.SetModelRateLimit(context.Background(), 3230, "gpt-6-luna", time.Now().Add(time.Minute)))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("missing account still reports not found", func(t *testing.T) {
		repo, mock := newNoAutoUnschedMockRepo(t)
		mock.ExpectExec(`(?s)UPDATE accounts SET`).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`(?s)SELECT 1 FROM accounts WHERE id = \$1`).
			WithArgs(int64(99)).
			WillReturnRows(sqlmock.NewRows([]string{"?column?"}))
		require.ErrorIs(t, repo.SetModelRateLimit(context.Background(), 99, "gpt-6-luna", time.Now().Add(time.Minute)), service.ErrAccountNotFound)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestExtendModelRateLimitSkipsExemptAccounts(t *testing.T) {
	repo, mock := newExtendModelRateLimitMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(extra.*FOR UPDATE`).WithArgs(int64(81)).
		WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{"no_auto_unsched": true}`)))
	mock.ExpectCommit()

	require.NoError(t, repo.ExtendModelRateLimit(context.Background(), 81, "openai:image_generation", time.Now().Add(5*time.Minute)))
	require.NoError(t, mock.ExpectationsWereMet())
}

// 读侧：所有可调度列表查询对带标记账号忽略因出错自动产生的状态（三类冷却窗口与自动置的 status=error），
// 否则账号一冷却就进不了候选池（pool=0）；手动停用与过期仍由条件保留。
func TestAccountRepository_SchedulableListsOmitAutoPauseWindowsForExemptAccounts(t *testing.T) {
	repo, mock := newNoAutoUnschedMockRepo(t)
	service.SetNoAutoUnschedConfigIDs([]int64{3230})

	pattern := `(?s)FROM "accounts".*"platform" = \$1.*` + exemptExprWithID +
		` AND \("accounts"\."status" = \$\d+ OR \("accounts"\."status" = \$\d+ AND "accounts"\."schedulable"\)\).*OR \("accounts"\."status" = \$\d+ AND "accounts"\."schedulable" AND .*"temp_unschedulable_until" IS NULL.*"overload_until" IS NULL.*"rate_limit_reset_at" IS NULL`
	mock.ExpectQuery(pattern).WillReturnRows(sqlmock.NewRows([]string{"id"}))

	accounts, err := repo.ListSchedulableByPlatform(context.Background(), service.PlatformOpenAI)
	require.NoError(t, err)
	require.Empty(t, accounts)
	require.NoError(t, mock.ExpectationsWereMet())
}
