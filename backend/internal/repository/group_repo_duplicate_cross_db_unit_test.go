//go:build unit

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func TestGroupDuplicateQueriesUsePortableDialectSyntax(t *testing.T) {
	tests := []struct {
		name             string
		dbDialect        string
		lockClause       string
		insertPrefix     string
		conflictClause   string
		placeholderToken string
	}{
		{name: "postgres", dbDialect: dialect.Postgres, lockClause: "FOR SHARE", insertPrefix: "INSERT INTO", conflictClause: "ON CONFLICT (account_id, group_id) DO NOTHING", placeholderToken: "$1"},
		{name: "mysql", dbDialect: dialect.MySQL, lockClause: "LOCK IN SHARE MODE", insertPrefix: "INSERT IGNORE INTO", placeholderToken: "?"},
		{name: "sqlite", dbDialect: dialect.SQLite, insertPrefix: "INSERT OR IGNORE INTO", placeholderToken: "?"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lockQuery := groupDuplicateSourceLockQuery(tt.dbDialect)
			copyQuery := groupDuplicateAccountCopyQuery(tt.dbDialect)
			require.Contains(t, lockQuery, tt.placeholderToken)
			require.Contains(t, copyQuery, tt.insertPrefix)
			require.Contains(t, copyQuery, "CURRENT_TIMESTAMP")
			if tt.lockClause != "" {
				require.Contains(t, lockQuery, tt.lockClause)
			} else {
				require.NotContains(t, lockQuery, "FOR SHARE")
				require.NotContains(t, lockQuery, "LOCK IN SHARE MODE")
			}
			if tt.conflictClause != "" {
				require.Contains(t, copyQuery, tt.conflictClause)
			} else {
				require.NotContains(t, copyQuery, "ON CONFLICT")
			}
		})
	}
}

func TestCreateFromSourceSQLiteUsesPortableQueries(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	_, err = db.Exec(`
		CREATE TABLE scheduler_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type TEXT NOT NULL,
			account_id INTEGER,
			group_id INTEGER,
			payload TEXT,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			dedup_key TEXT
		);
		CREATE UNIQUE INDEX idx_scheduler_outbox_pending_dedup_key
			ON scheduler_outbox (dedup_key) WHERE dedup_key IS NOT NULL;
	`)
	require.NoError(t, err)

	ctx := context.Background()
	source, err := client.Group.Create().
		SetName("sqlite-duplicate-source").
		SetPlatform(service.PlatformOpenAI).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	account, err := client.Account.Create().
		SetName("sqlite-duplicate-account").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountGroup.Create().
		SetAccountID(account.ID).
		SetGroupID(source.ID).
		SetPriority(7).
		Save(ctx)
	require.NoError(t, err)
	var sourceBindings int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM account_groups WHERE group_id = ?", source.ID).Scan(&sourceBindings))
	require.Equal(t, 1, sourceBindings)

	duplicate := &service.Group{
		Name:                 "sqlite-duplicate-copy",
		Platform:             service.PlatformOpenAI,
		Status:               "inactive",
		DuplicateOperationID: "sqlite-operation",
	}
	repo := newGroupRepositoryWithSQL(client, db)
	var sourceAccountID int64
	require.NoError(t, db.QueryRow(`
		SELECT ag.account_id
		FROM account_groups ag JOIN accounts a ON a.id = ag.account_id
		WHERE ag.group_id = ? AND a.deleted_at IS NULL`, source.ID).Scan(&sourceAccountID))
	require.Equal(t, account.ID, sourceAccountID)
	require.NoError(t, repo.CreateFromSource(ctx, duplicate, source.ID))

	var priority int
	require.NoError(t, db.QueryRow("SELECT priority FROM account_groups WHERE group_id = ?", duplicate.ID).Scan(&priority))
	require.Equal(t, 7, priority)
	var outboxCount int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduler_outbox WHERE group_id = ?", duplicate.ID).Scan(&outboxCount))
	require.Equal(t, 1, outboxCount)

	stored, err := repo.FindByDuplicateOperationID(ctx, strings.TrimSpace(" sqlite-operation "))
	require.NoError(t, err)
	require.Equal(t, duplicate.ID, stored.ID)

	require.NoError(t, repo.Delete(ctx, duplicate.ID))
	reuse := &service.Group{
		Name:                 "sqlite-duplicate-reuse",
		Platform:             service.PlatformOpenAI,
		Status:               "inactive",
		DuplicateOperationID: "sqlite-operation",
	}
	require.NoError(t, repo.Create(ctx, reuse))
}
