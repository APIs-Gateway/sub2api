//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	entgroup "github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSimpleModeImageEligibilitySeederPreservesOldAndManualRows(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	old, err := client.Group.Create().SetName("openai-default").SetPlatform(service.PlatformOpenAI).SetDescription(simpleModeDefaultGroupDescription).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, createGroupIfNotExists(ctx, client, "openai-default", service.PlatformOpenAI))
	old, err = client.Group.Get(ctx, old.ID)
	require.NoError(t, err)
	require.False(t, old.SimpleModeAutoImageEligible)
	require.NoError(t, createGroupIfNotExists(ctx, client, "openai-new-system-default", service.PlatformOpenAI))
	seeded, err := client.Group.Query().Where(entgroup.NameEQ("openai-new-system-default")).Only(ctx)
	require.NoError(t, err)
	require.True(t, seeded.SimpleModeAutoImageEligible)
	require.False(t, seeded.AllowImageGeneration)
	repo := newGroupRepositoryWithSQL(client, nil)
	manual := &service.Group{Name: "manual-claimed-eligibility", Platform: service.PlatformOpenAI, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard, RateMultiplier: 1, SimpleModeAutoImageEligible: true}
	require.NoError(t, repo.Create(ctx, manual))
	saved, err := repo.GetByIDLite(ctx, manual.ID)
	require.NoError(t, err)
	require.False(t, saved.SimpleModeAutoImageEligible)
}

func TestGroupRepositoryCannotReviveClearedSimpleModeImageEligibility(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	row, err := client.Group.Create().SetName("sticky-image-eligibility").SetPlatform(service.PlatformOpenAI).SetSimpleModeAutoImageEligible(true).Save(ctx)
	require.NoError(t, err)
	repo := newGroupRepositoryWithSQL(client, nil)
	stale, err := repo.GetByIDLite(ctx, row.ID)
	require.NoError(t, err)
	require.True(t, stale.SimpleModeAutoImageEligible)
	current := *stale
	current.SimpleModeAutoImageEligible = false
	current.AllowImageGeneration = false
	require.NoError(t, repo.Update(ctx, &current))
	stale.Description = "ordinary stale update"
	require.NoError(t, repo.Update(ctx, stale))
	saved, err := repo.GetByIDLite(ctx, row.ID)
	require.NoError(t, err)
	require.False(t, saved.SimpleModeAutoImageEligible)
	user := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-image-query", Name: "image-query", GroupID: &row.ID})
	auth, err := NewAPIKeyRepository(client, nil).GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.False(t, auth.Group.SimpleModeAutoImageEligible)
	_, err = client.Group.UpdateOneID(row.ID).SetSimpleModeAutoImageEligible(true).Save(ctx)
	require.NoError(t, err)
	auth, err = NewAPIKeyRepository(client, nil).GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.True(t, auth.Group.SimpleModeAutoImageEligible)
}

func TestImagePermissionChangesEnqueueDurableInvalidation(t *testing.T) {
	ctx := context.Background()
	client := integrationEntClient
	group, err := client.Group.Create().SetName("image-outbox-" + time.Now().Format("150405.000000000")).SetPlatform(service.PlatformOpenAI).SetSimpleModeAutoImageEligible(true).Save(ctx)
	require.NoError(t, err)
	user := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-image-outbox-" + time.Now().Format("150405.000000000"), Name: "image-outbox", GroupID: &group.ID})
	digest := sha256.Sum256([]byte(key.Key))
	hashed := hex.EncodeToString(digest[:])
	for _, update := range []string{"simple_mode_auto_image_eligible = FALSE", "allow_image_generation = TRUE", "allow_image_generation = FALSE"} {
		_, err = integrationDB.ExecContext(ctx, "DELETE FROM auth_cache_invalidation_outbox WHERE cache_key=$1", hashed)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET "+update+" WHERE id=$1", group.ID)
		require.NoError(t, err)
		var count int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_cache_invalidation_outbox WHERE cache_key=$1", hashed).Scan(&count))
		require.Equal(t, 1, count, update)
	}
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM auth_cache_invalidation_outbox WHERE cache_key=$1", hashed)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET name=name || '-renamed' WHERE id=$1", group.ID)
	require.NoError(t, err)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_cache_invalidation_outbox WHERE cache_key=$1", hashed).Scan(&count))
	require.Zero(t, count)
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id=$1", group.ID)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_cache_invalidation_outbox WHERE cache_key=$1", hashed).Scan(&count))
	require.Positive(t, count)

}
