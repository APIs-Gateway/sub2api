//go:build unit

package repository

import (
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

var adminTokenRepoColumns = []string{
	"id", "name", "token_hash", "token_prefix", "scope", "acting_user_id",
	"created_by_user_id", "ip_allowlist", "expires_at", "revoked_at", "last_used_at",
	"last_used_ip", "created_at", "updated_at",
}

func newAdminTokenRepoMock(t *testing.T) (*adminTokenRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &adminTokenRepository{db: db}, mock
}

func TestAdminTokenRepositoryCreateReturnsGeneratedColumns(t *testing.T) {
	repo, mock := newAdminTokenRepoMock(t)
	expiresAt := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	createdBy := int64(7)

	mock.ExpectQuery(`(?s)INSERT INTO admin_tokens .* RETURNING id, created_at, updated_at`).
		WithArgs("ops-bot", "hash-1", "s2a_abcd", "write", int64(7), int64(7),
			pq.StringArray{"10.0.0.0/8"}, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow(int64(42), createdAt, createdAt))

	token := &service.AdminToken{
		Name:            "ops-bot",
		TokenHash:       "hash-1",
		TokenPrefix:     "s2a_abcd",
		Scope:           service.AdminTokenScopeWrite,
		ActingUserID:    7,
		CreatedByUserID: &createdBy,
		IPAllowlist:     []string{"10.0.0.0/8"},
		ExpiresAt:       expiresAt,
	}
	require.NoError(t, repo.Create(t.Context(), token))
	require.EqualValues(t, 42, token.ID)
	require.Equal(t, createdAt, token.CreatedAt)
	require.Equal(t, createdAt, token.UpdatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAdminTokenRepositoryCreateStoresEmptyAllowlistAndNullCreator(t *testing.T) {
	repo, mock := newAdminTokenRepoMock(t)
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`INSERT INTO admin_tokens`).
		WithArgs("n", "h", "s2a_xxxx", "read", int64(1), nil, pq.StringArray{}, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(1), now, now))

	require.NoError(t, repo.Create(t.Context(), &service.AdminToken{
		Name: "n", TokenHash: "h", TokenPrefix: "s2a_xxxx", Scope: service.AdminTokenScopeRead,
		ActingUserID: 1, ExpiresAt: now.Add(time.Hour),
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAdminTokenRepositoryGetByHashScansAllColumns(t *testing.T) {
	repo, mock := newAdminTokenRepoMock(t)
	expiresAt := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	revoked := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	lastUsed := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`(?s)SELECT .* FROM admin_tokens WHERE token_hash = \$1`).
		WithArgs("hash-1").
		WillReturnRows(sqlmock.NewRows(adminTokenRepoColumns).AddRow(
			int64(42), "ops-bot", "hash-1", "s2a_abcd", "danger", int64(7),
			int64(9), "{10.0.0.0/8,127.0.0.1/32}", expiresAt, revoked, lastUsed,
			"127.0.0.1", created, created,
		))

	token, err := repo.GetByHash(t.Context(), "hash-1")
	require.NoError(t, err)
	require.EqualValues(t, 42, token.ID)
	require.Equal(t, "ops-bot", token.Name)
	require.Equal(t, "hash-1", token.TokenHash)
	require.Equal(t, "s2a_abcd", token.TokenPrefix)
	require.Equal(t, service.AdminTokenScopeDanger, token.Scope)
	require.EqualValues(t, 7, token.ActingUserID)
	require.NotNil(t, token.CreatedByUserID)
	require.EqualValues(t, 9, *token.CreatedByUserID)
	require.Equal(t, []string{"10.0.0.0/8", "127.0.0.1/32"}, token.IPAllowlist)
	require.Equal(t, expiresAt, token.ExpiresAt)
	require.NotNil(t, token.RevokedAt)
	require.Equal(t, revoked, *token.RevokedAt)
	require.NotNil(t, token.LastUsedAt)
	require.Equal(t, lastUsed, *token.LastUsedAt)
	require.Equal(t, "127.0.0.1", token.LastUsedIP)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAdminTokenRepositoryGetByHashHandlesNullableColumns(t *testing.T) {
	repo, mock := newAdminTokenRepoMock(t)
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT .* FROM admin_tokens`).
		WithArgs("hash-2").
		WillReturnRows(sqlmock.NewRows(adminTokenRepoColumns).AddRow(
			int64(1), "n", "hash-2", "s2a_zzzz", "read", int64(1),
			nil, "{}", now.Add(time.Hour), nil, nil, "", now, now,
		))

	token, err := repo.GetByHash(t.Context(), "hash-2")
	require.NoError(t, err)
	require.Nil(t, token.CreatedByUserID)
	require.Nil(t, token.RevokedAt)
	require.Nil(t, token.LastUsedAt)
	require.NotNil(t, token.IPAllowlist)
	require.Empty(t, token.IPAllowlist)
}

func TestAdminTokenRepositoryGetByHashMapsNoRowsToNotFound(t *testing.T) {
	repo, mock := newAdminTokenRepoMock(t)
	mock.ExpectQuery(`SELECT .* FROM admin_tokens`).
		WithArgs("missing").
		WillReturnRows(sqlmock.NewRows(adminTokenRepoColumns))

	token, err := repo.GetByHash(t.Context(), "missing")
	require.Nil(t, token)
	require.ErrorIs(t, err, service.ErrAdminTokenNotFound)
}

func TestAdminTokenRepositoryGetByHashPropagatesDatabaseErrors(t *testing.T) {
	repo, mock := newAdminTokenRepoMock(t)
	boom := errors.New("connection reset")
	mock.ExpectQuery(`SELECT .* FROM admin_tokens`).WithArgs("h").WillReturnError(boom)

	_, err := repo.GetByHash(t.Context(), "h")
	require.ErrorIs(t, err, boom)
	require.False(t, errors.Is(err, service.ErrAdminTokenNotFound))
}

func TestAdminTokenRepositoryListNewestFirst(t *testing.T) {
	repo, mock := newAdminTokenRepoMock(t)
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("FROM admin_tokens ORDER BY id DESC")).
		WillReturnRows(sqlmock.NewRows(adminTokenRepoColumns).
			AddRow(int64(2), "b", "h2", "s2a_2222", "read", int64(1), nil, "{}", now, nil, nil, "", now, now).
			AddRow(int64(1), "a", "h1", "s2a_1111", "write", int64(1), int64(1), "{10.0.0.0/8}", now, nil, nil, "", now, now))

	tokens, err := repo.List(t.Context())
	require.NoError(t, err)
	require.Len(t, tokens, 2)
	require.EqualValues(t, 2, tokens[0].ID)
	require.EqualValues(t, 1, tokens[1].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAdminTokenRepositoryRevokeKeepsFirstRevocation(t *testing.T) {
	repo, mock := newAdminTokenRepoMock(t)
	at := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	first := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`(?s)UPDATE admin_tokens\s+SET revoked_at = COALESCE\(revoked_at, \$2\).*WHERE id = \$1\s+RETURNING`).
		WithArgs(int64(5), at).
		WillReturnRows(sqlmock.NewRows(adminTokenRepoColumns).AddRow(
			int64(5), "n", "h", "s2a_5555", "read", int64(1), nil, "{}", at.Add(time.Hour), first, nil, "", first, first,
		))

	token, err := repo.Revoke(t.Context(), 5, at)
	require.NoError(t, err)
	require.NotNil(t, token.RevokedAt)
	require.Equal(t, first, *token.RevokedAt, "the original revocation time is returned")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAdminTokenRepositoryRevokeUnknownIDIsNotFound(t *testing.T) {
	repo, mock := newAdminTokenRepoMock(t)
	mock.ExpectQuery(`UPDATE admin_tokens`).
		WithArgs(int64(404), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows(adminTokenRepoColumns))

	_, err := repo.Revoke(t.Context(), 404, time.Now())
	require.ErrorIs(t, err, service.ErrAdminTokenNotFound)
}

func TestAdminTokenRepositoryTouchLastUsed(t *testing.T) {
	repo, mock := newAdminTokenRepoMock(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	mock.ExpectExec(`UPDATE admin_tokens SET last_used_at = \$2, last_used_ip = \$3 WHERE id = \$1`).
		WithArgs(int64(3), at, "203.0.113.9").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.TouchLastUsed(t.Context(), 3, at, "203.0.113.9"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAdminTokenRepositoryNilReceiverIsRejected(t *testing.T) {
	var repo *adminTokenRepository
	_, err := repo.GetByHash(t.Context(), "h")
	require.Error(t, err)
	require.Error(t, (&adminTokenRepository{}).Create(t.Context(), &service.AdminToken{}))
}
