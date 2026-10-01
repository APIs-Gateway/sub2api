package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type adminTokenRepository struct {
	db *sql.DB
}

// NewAdminTokenRepository returns the PostgreSQL-backed admin token store.
func NewAdminTokenRepository(db *sql.DB) service.AdminTokenRepository {
	return &adminTokenRepository{db: db}
}

const adminTokenSelectColumns = `id, name, token_hash, token_prefix, scope, acting_user_id,
created_by_user_id, ip_allowlist, expires_at, revoked_at, last_used_at, last_used_ip,
created_at, updated_at`

func scanAdminToken(scan func(dest ...any) error) (*service.AdminToken, error) {
	token := &service.AdminToken{}
	var (
		createdBy  sql.NullInt64
		allowlist  pq.StringArray
		revokedAt  sql.NullTime
		lastUsedAt sql.NullTime
	)
	if err := scan(
		&token.ID,
		&token.Name,
		&token.TokenHash,
		&token.TokenPrefix,
		&token.Scope,
		&token.ActingUserID,
		&createdBy,
		&allowlist,
		&token.ExpiresAt,
		&revokedAt,
		&lastUsedAt,
		&token.LastUsedIP,
		&token.CreatedAt,
		&token.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if createdBy.Valid {
		value := createdBy.Int64
		token.CreatedByUserID = &value
	}
	token.IPAllowlist = []string(allowlist)
	if token.IPAllowlist == nil {
		token.IPAllowlist = []string{}
	}
	if revokedAt.Valid {
		value := revokedAt.Time
		token.RevokedAt = &value
	}
	if lastUsedAt.Valid {
		value := lastUsedAt.Time
		token.LastUsedAt = &value
	}
	return token, nil
}

func (r *adminTokenRepository) Create(ctx context.Context, token *service.AdminToken) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil admin token repository")
	}
	if token == nil {
		return fmt.Errorf("nil admin token")
	}
	var createdBy any
	if token.CreatedByUserID != nil {
		createdBy = *token.CreatedByUserID
	}
	allowlist := token.IPAllowlist
	if allowlist == nil {
		allowlist = []string{}
	}
	return r.db.QueryRowContext(ctx, `
INSERT INTO admin_tokens (name, token_hash, token_prefix, scope, acting_user_id,
    created_by_user_id, ip_allowlist, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, created_at, updated_at`,
		token.Name,
		token.TokenHash,
		token.TokenPrefix,
		token.Scope,
		token.ActingUserID,
		createdBy,
		pq.StringArray(allowlist),
		token.ExpiresAt.UTC(),
	).Scan(&token.ID, &token.CreatedAt, &token.UpdatedAt)
}

func (r *adminTokenRepository) GetByHash(ctx context.Context, tokenHash string) (*service.AdminToken, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil admin token repository")
	}
	row := r.db.QueryRowContext(ctx,
		"SELECT "+adminTokenSelectColumns+" FROM admin_tokens WHERE token_hash = $1", tokenHash)
	token, err := scanAdminToken(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrAdminTokenNotFound
	}
	return token, err
}

func (r *adminTokenRepository) List(ctx context.Context) ([]*service.AdminToken, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil admin token repository")
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+adminTokenSelectColumns+" FROM admin_tokens ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	tokens := make([]*service.AdminToken, 0)
	for rows.Next() {
		token, err := scanAdminToken(rows.Scan)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tokens, nil
}

// Revoke sets revoked_at the first time it is called for a token; later calls
// leave the original revocation time untouched and return the same row.
func (r *adminTokenRepository) Revoke(ctx context.Context, id int64, at time.Time) (*service.AdminToken, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil admin token repository")
	}
	row := r.db.QueryRowContext(ctx, `
UPDATE admin_tokens
SET revoked_at = COALESCE(revoked_at, $2),
    updated_at = CASE WHEN revoked_at IS NULL THEN $2 ELSE updated_at END
WHERE id = $1
RETURNING `+adminTokenSelectColumns, id, at.UTC())
	token, err := scanAdminToken(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrAdminTokenNotFound
	}
	return token, err
}

func (r *adminTokenRepository) TouchLastUsed(ctx context.Context, id int64, at time.Time, clientIP string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil admin token repository")
	}
	_, err := r.db.ExecContext(ctx,
		"UPDATE admin_tokens SET last_used_at = $2, last_used_ip = $3 WHERE id = $1",
		id, at.UTC(), truncateAuditField(clientIP, 64))
	return err
}
