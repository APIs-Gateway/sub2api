package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/proxy"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"entgo.io/ent/dialect"
)

func lockProbeProxyIdentity(ctx context.Context, exec sqlExecutor, id int64) (probeProxyIdentity, error) {
	if tx, ok := exec.(*dbent.Tx); ok && tx.Client().Driver().Dialect() != dialect.Postgres {
		query := tx.Client().Proxy.Query().Where(proxy.IDEQ(id), proxy.DeletedAtIsNil())
		if tx.Client().Driver().Dialect() != dialect.SQLite {
			query = query.ForUpdate()
		}
		current, err := query.Only(ctx)
		if err != nil {
			if dbent.IsNotFound(err) {
				return probeProxyIdentity{}, service.ErrProxyNotFound
			}
			return probeProxyIdentity{}, err
		}
		return probeProxyIdentity{
			protocol: current.Protocol,
			host:     current.Host,
			port:     current.Port,
			username: derefString(current.Username),
			password: derefString(current.Password),
			status:   current.Status,
		}, nil
	}

	rows, err := exec.QueryContext(ctx, `
		SELECT protocol, host, port, COALESCE(username, ''), COALESCE(password, ''), status
		FROM proxies
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE
	`, id)
	if err != nil {
		return probeProxyIdentity{}, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return probeProxyIdentity{}, err
		}
		return probeProxyIdentity{}, service.ErrProxyNotFound
	}
	var identity probeProxyIdentity
	if err := rows.Scan(&identity.protocol, &identity.host, &identity.port, &identity.username, &identity.password, &identity.status); err != nil {
		return probeProxyIdentity{}, err
	}
	return identity, rows.Err()
}

func clearProbeSnapshotsForProxy(ctx context.Context, exec sqlExecutor, proxyID int64) error {
	if tx, ok := exec.(*dbent.Tx); ok && tx.Client().Driver().Dialect() != dialect.Postgres {
		accounts, err := tx.Client().Account.Query().Where(
			dbaccount.ProxyIDEQ(proxyID),
			dbaccount.PlatformEQ(service.PlatformOpenAI),
			dbaccount.TypeEQ(service.AccountTypeAPIKey),
			dbaccount.DeletedAtIsNil(),
		).All(ctx)
		if err != nil {
			return err
		}
		accountIDs := make([]int64, 0, len(accounts))
		for _, account := range accounts {
			if value, exists := account.Extra[service.UpstreamBillingProbeExtraKey]; !exists || value == nil {
				continue
			}
			extra := copyJSONMap(account.Extra)
			delete(extra, service.UpstreamBillingProbeExtraKey)
			if _, err := tx.Client().Account.UpdateOneID(account.ID).SetExtra(extra).Save(ctx); err != nil {
				return err
			}
			accountIDs = append(accountIDs, account.ID)
		}
		if len(accountIDs) == 0 {
			return nil
		}
		return enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountBulkChanged, nil, nil, map[string]any{
			"account_ids": accountIDs,
		})
	}

	rows, err := exec.QueryContext(ctx, `
		UPDATE accounts
		SET extra = COALESCE(extra, '{}'::jsonb) - 'upstream_billing_probe', updated_at = NOW()
		WHERE proxy_id = $1
			AND platform = 'openai'
			AND type = 'apikey'
			AND extra ? 'upstream_billing_probe'
			AND extra -> 'upstream_billing_probe' <> 'null'::jsonb
			AND deleted_at IS NULL
		RETURNING id
	`, proxyID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	accountIDs := make([]int64, 0)
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			return err
		}
		accountIDs = append(accountIDs, accountID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(accountIDs) == 0 {
		return nil
	}
	return enqueueSchedulerOutbox(ctx, exec, service.SchedulerOutboxEventAccountBulkChanged, nil, nil, map[string]any{
		"account_ids": accountIDs,
	})
}

var _ sqlExecutor = (*dbent.Tx)(nil)
