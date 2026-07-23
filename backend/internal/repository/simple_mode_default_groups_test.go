//go:build unit

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestEnsureSimpleModeDefaultGroupsReturnsBackfillError(t *testing.T) {
	db, err := sql.Open("sqlite", "file:simple_mode_default_groups_error_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.SQLite, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })

	err = ensureSimpleModeDefaultGroups(context.Background(), client)
	require.Error(t, err)
	require.ErrorContains(t, err, "backfill auto-created grok default image generation")
}
