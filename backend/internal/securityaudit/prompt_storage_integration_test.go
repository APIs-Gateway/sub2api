//go:build integration

package securityaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPromptAuditStoragePostgreSQLIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("prompt_audit"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	exercisePromptAuditStorageIntegration(t, ctx, db)
}

func TestPromptAuditStorageMySQL57Integration(t *testing.T) {
	if runtime.GOARCH != "amd64" && os.Getenv("PROMPT_AUDIT_TEST_MYSQL57_EMULATED") != "1" {
		t.Skip("mysql:5.7.44 only publishes an amd64 image; run this test on native amd64 or set PROMPT_AUDIT_TEST_MYSQL57_EMULATED=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := mysql.Run(ctx, "mysql:5.7.44",
		mysql.WithDatabase("prompt_audit"),
		mysql.WithUsername("audit"),
		mysql.WithPassword("audit"),
		testcontainers.WithImagePlatform("linux/amd64"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "parseTime=true")
	require.NoError(t, err)
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	exercisePromptAuditStorageIntegration(t, ctx, db)
}

func exercisePromptAuditStorageIntegration(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.PingContext(ctx))
	for _, statement := range []string{
		`CREATE TABLE users (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE groups (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE api_keys (id BIGINT PRIMARY KEY)`,
	} {
		_, err := db.ExecContext(ctx, statement)
		require.NoError(t, err)
	}
	settingsKeyColumn := `"key"`
	if promptAuditSQLDialect(db) == promptAuditQuestionMark {
		settingsKeyColumn = "`key`"
	}
	_, err := db.ExecContext(ctx, `CREATE TABLE settings (`+settingsKeyColumn+` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	applyPromptAuditMigration(t, db)
	applyPromptAuditMigration(t, db)

	repo := NewSQLRepository(db)
	snapshot := storageSnapshot()
	snapshot.ScanText = "raw prompt must stay outside durable storage"
	event, err := repo.RecordBlocking(ctx, snapshot, 7, storageResult(), true, false)
	require.NoError(t, err)
	require.NotNil(t, event)
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	require.NotContains(t, string(raw), snapshot.ScanText)
	page, err := repo.ListEvents(ctx, EventFilter{Keyword: snapshot.RequestID}, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, page.Items, 1)
	require.Empty(t, page.Items[0].Snapshot.FullPrompt)
	loadedEvent, err := repo.GetEvent(ctx, event.ID)
	require.NoError(t, err)
	require.Equal(t, event.ID, loadedEvent.ID)
	require.Empty(t, loadedEvent.Snapshot.FullPrompt, "store_full_prompts was disabled for this event")

	job, err := repo.CreateStagingWithCapacity(ctx, snapshot, 7, 2, 10)
	require.NoError(t, err)
	require.NoError(t, repo.PublishQueued(ctx, job.ID))
	claimed, ok, err := repo.ClaimNextJob(ctx, time.Now().UTC().Add(time.Second))
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, repo.RefreshLease(ctx, claimed.ID, claimed.ClaimVersion, time.Now().UTC()))
	_, err = repo.Complete(ctx, claimed, storageResult(), true, false)
	require.NoError(t, err)
	stats, err := repo.QueueStats(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), stats.Done)

	// store_full_prompts enabled: the durable full_prompt column and the
	// authenticated single-event detail read (GetEvent) carry the unredacted
	// text, but the list page and the object Complete/RecordBlocking return
	// directly must not.
	fullPromptSnapshot := storageSnapshot()
	fullPromptSnapshot.RequestID = "request-full-prompt"
	fullPromptSnapshot.FullPrompt = "PROMPT_CANARY_full_prompt_integration_" + strings.Repeat("x", 200)
	fullPromptEvent, err := repo.RecordBlocking(ctx, fullPromptSnapshot, 7, storageResult(), true, true)
	require.NoError(t, err)
	require.NotNil(t, fullPromptEvent)
	rawFullPromptEvent, err := json.Marshal(fullPromptEvent)
	require.NoError(t, err)
	require.NotContains(t, string(rawFullPromptEvent), fullPromptSnapshot.FullPrompt, "Complete/RecordBlocking must not return full_prompt directly")
	fullPromptPlaceholder := "$1"
	if promptAuditSQLDialect(db) == promptAuditQuestionMark {
		fullPromptPlaceholder = "?"
	}
	var storedFullPrompt string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT full_prompt FROM prompt_audit_events WHERE id = `+fullPromptPlaceholder, fullPromptEvent.ID).Scan(&storedFullPrompt))
	require.Equal(t, fullPromptSnapshot.FullPrompt, storedFullPrompt)
	fullPromptDetail, err := repo.GetEvent(ctx, fullPromptEvent.ID)
	require.NoError(t, err)
	require.Equal(t, fullPromptSnapshot.FullPrompt, fullPromptDetail.Snapshot.FullPrompt)
	fullPromptPage, err := repo.ListEvents(ctx, EventFilter{RequestID: fullPromptSnapshot.RequestID}, 1, 20)
	require.NoError(t, err)
	require.Len(t, fullPromptPage.Items, 1)
	require.Empty(t, fullPromptPage.Items[0].Snapshot.FullPrompt, "list responses must stay lean even when store_full_prompts is enabled")

	manager := NewConfigManager(db, nil, nil, prefixEncryptor{})
	first, err := manager.Save(ctx, promptAuditUpdateRequest(1, 1, "first-token"), 9)
	require.NoError(t, err)
	require.Equal(t, int64(2), first.ConfigVersion)
	second, err := manager.Save(ctx, promptAuditUpdateRequest(2, 2, "second-token"), 9)
	require.NoError(t, err)
	require.Equal(t, int64(3), second.ConfigVersion)
	_, err = manager.Save(ctx, promptAuditUpdateRequest(2, 3, "stale-token"), 9)
	require.Equal(t, ErrorCodeConfigConflict, infraerrors.Reason(err))
}
