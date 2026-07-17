//go:build unit

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/channelmonitor"
	"github.com/Wei-Shaw/sub2api/ent/channelmonitorrequesttemplate"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func TestApplyChannelMonitorTemplatePreservesDuplicateOperationMetadataSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	template, err := client.ChannelMonitorRequestTemplate.Create().
		SetName("duplicate-metadata-template").
		SetProvider(channelmonitorrequesttemplate.ProviderOpenai).
		SetAPIMode(service.MonitorAPIModeResponses).
		SetExtraHeaders(map[string]string{"User-Agent": "template-client"}).
		SetBodyOverrideMode(service.MonitorBodyOverrideModeOff).
		Save(ctx)
	require.NoError(t, err)

	monitor, err := client.ChannelMonitor.Create().
		SetName("duplicate-copy").
		SetProvider(channelmonitor.ProviderOpenai).
		SetAPIMode(service.MonitorAPIModeResponses).
		SetEndpoint("https://api.example.com").
		SetAPIKeyEncrypted("encrypted-key").
		SetPrimaryModel("gpt-5.4-mini").
		SetIntervalSeconds(60).
		SetCreatedBy(1).
		SetTemplateID(template.ID).
		SetExtraHeaders(map[string]string{
			"X-Original": "replaced",
			service.ChannelMonitorDuplicateOperationIDMetadataKey: "operation-digest",
		}).
		Save(ctx)
	require.NoError(t, err)

	repo := NewChannelMonitorRequestTemplateRepository(client, db)
	affected, err := repo.ApplyToMonitors(ctx, template.ID, []int64{monitor.ID})
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	stored, err := client.ChannelMonitor.Get(ctx, monitor.ID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"User-Agent": "template-client",
		service.ChannelMonitorDuplicateOperationIDMetadataKey: "operation-digest",
	}, stored.ExtraHeaders)
}
