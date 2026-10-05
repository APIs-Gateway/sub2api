package repository

import (
	"context"
	"errors"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// CompareAndSwapCredentials keeps the existing credential/outbox/cache update
// contract while preventing a stale refresh result from overwriting a writer
// that does not participate in the OAuth refresh locks. No network I/O occurs
// while holding this short database lock.
func (r *accountRepository) CompareAndSwapCredentials(ctx context.Context, expected *service.Account, credentials map[string]any) (bool, error) {
	if expected == nil {
		return false, service.ErrAccountNilInput
	}
	if dbent.TxFromContext(ctx) != nil {
		return false, errors.New("conditional OAuth credentials require their own durable transaction")
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	query := tx.Client().Account.Query().Where(dbaccount.IDEQ(expected.ID), dbaccount.DeletedAtIsNil())
	if tx.Client().Driver().Dialect() != dialect.SQLite {
		query = query.ForUpdate()
	}
	current, err := query.Only(txCtx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if current.Platform != expected.Platform || current.Type != expected.Type || current.Status != expected.Status ||
		!probeJSONEqual(current.Credentials, expected.Credentials) || !probeJSONEqual(current.Extra, expected.Extra) ||
		(current.ProxyID == nil) != (expected.ProxyID == nil) || (current.ProxyID != nil && *current.ProxyID != *expected.ProxyID) {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := r.updateCredentialsWithProbe(txCtx, expected.ID, credentials); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	r.syncSchedulerAccountSnapshot(ctx, expected.ID)
	return true, nil
}
