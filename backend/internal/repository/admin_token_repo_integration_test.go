//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAdminTokenRepositoryIntegration_RoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewAdminTokenRepository(integrationDB)

	admin := mustCreateUser(t, testEntClient(t), &service.User{
		Email: fmt.Sprintf("admin-token-%d@example.com", time.Now().UnixNano()),
		Role:  service.RoleAdmin,
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(),
			"DELETE FROM admin_tokens WHERE acting_user_id = $1", admin.ID)
	})

	plaintext, err := service.GenerateAdminTokenPlaintext()
	require.NoError(t, err)
	hash := service.HashAdminToken(plaintext)

	createdBy := admin.ID
	token := &service.AdminToken{
		Name:            "integration",
		TokenHash:       hash,
		TokenPrefix:     plaintext[:8],
		Scope:           service.AdminTokenScopeWrite,
		ActingUserID:    admin.ID,
		CreatedByUserID: &createdBy,
		IPAllowlist:     []string{"10.0.0.0/8", "127.0.0.1/32"},
		ExpiresAt:       time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond),
	}
	require.NoError(t, repo.Create(ctx, token))
	require.NotZero(t, token.ID)
	require.False(t, token.CreatedAt.IsZero())

	got, err := repo.GetByHash(ctx, hash)
	require.NoError(t, err)
	require.Equal(t, token.ID, got.ID)
	require.Equal(t, "integration", got.Name)
	require.Equal(t, hash, got.TokenHash)
	require.Equal(t, plaintext[:8], got.TokenPrefix)
	require.Equal(t, service.AdminTokenScopeWrite, got.Scope)
	require.Equal(t, admin.ID, got.ActingUserID)
	require.NotNil(t, got.CreatedByUserID)
	require.Equal(t, admin.ID, *got.CreatedByUserID)
	require.Equal(t, []string{"10.0.0.0/8", "127.0.0.1/32"}, got.IPAllowlist)
	require.WithinDuration(t, token.ExpiresAt, got.ExpiresAt, time.Millisecond)
	require.Nil(t, got.RevokedAt)
	require.Nil(t, got.LastUsedAt)
	require.Equal(t, "", got.LastUsedIP)

	// The plaintext must not be stored anywhere in the row.
	var stored int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM admin_tokens WHERE token_hash = $1 OR name = $1 OR token_prefix = $1", plaintext).Scan(&stored))
	require.Zero(t, stored)

	usedAt := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.TouchLastUsed(ctx, token.ID, usedAt, "203.0.113.9"))
	got, err = repo.GetByHash(ctx, hash)
	require.NoError(t, err)
	require.NotNil(t, got.LastUsedAt)
	require.WithinDuration(t, usedAt, *got.LastUsedAt, time.Millisecond)
	require.Equal(t, "203.0.113.9", got.LastUsedIP)

	listed, err := repo.List(ctx)
	require.NoError(t, err)
	found := false
	for _, item := range listed {
		if item.ID == token.ID {
			found = true
		}
	}
	require.True(t, found, "created token appears in the list")

	firstRevoke := time.Now().UTC().Truncate(time.Microsecond)
	revoked, err := repo.Revoke(ctx, token.ID, firstRevoke)
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt)
	require.WithinDuration(t, firstRevoke, *revoked.RevokedAt, time.Millisecond)

	again, err := repo.Revoke(ctx, token.ID, firstRevoke.Add(time.Hour))
	require.NoError(t, err)
	require.NotNil(t, again.RevokedAt)
	require.WithinDuration(t, firstRevoke, *again.RevokedAt, time.Millisecond, "second revoke keeps the first revocation time")

	// Soft revoke: the row is still resolvable by hash.
	got, err = repo.GetByHash(ctx, hash)
	require.NoError(t, err)
	require.NotNil(t, got.RevokedAt)
}

func TestAdminTokenRepositoryIntegration_NotFound(t *testing.T) {
	ctx := context.Background()
	repo := NewAdminTokenRepository(integrationDB)

	_, err := repo.GetByHash(ctx, "no-such-hash")
	require.ErrorIs(t, err, service.ErrAdminTokenNotFound)

	_, err = repo.Revoke(ctx, 1<<40, time.Now())
	require.ErrorIs(t, err, service.ErrAdminTokenNotFound)
}

func TestAdminTokenRepositoryIntegration_ConstraintsHoldAtTheDatabase(t *testing.T) {
	ctx := context.Background()
	repo := NewAdminTokenRepository(integrationDB)

	admin := mustCreateUser(t, testEntClient(t), &service.User{
		Email: fmt.Sprintf("admin-token-constraints-%d@example.com", time.Now().UnixNano()),
		Role:  service.RoleAdmin,
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(),
			"DELETE FROM admin_tokens WHERE acting_user_id = $1", admin.ID)
	})

	base := func(hash, scope string) *service.AdminToken {
		return &service.AdminToken{
			Name:         "constraint",
			TokenHash:    hash,
			TokenPrefix:  "s2a_test",
			Scope:        scope,
			ActingUserID: admin.ID,
			ExpiresAt:    time.Now().UTC().Add(time.Hour),
		}
	}

	hash := service.HashAdminToken(fmt.Sprintf("s2a_constraint-%d", time.Now().UnixNano()))
	require.NoError(t, repo.Create(ctx, base(hash, service.AdminTokenScopeRead)))

	// token_hash is unique.
	require.Error(t, repo.Create(ctx, base(hash, service.AdminTokenScopeRead)))

	// scope is constrained to read / write / danger.
	require.Error(t, repo.Create(ctx, base(service.HashAdminToken(fmt.Sprintf("s2a_bad-scope-%d", time.Now().UnixNano())), "root")))

	// acting_user_id must reference a real user.
	orphan := base(service.HashAdminToken(fmt.Sprintf("s2a_orphan-%d", time.Now().UnixNano())), service.AdminTokenScopeRead)
	orphan.ActingUserID = 1 << 40
	require.Error(t, repo.Create(ctx, orphan))
}
