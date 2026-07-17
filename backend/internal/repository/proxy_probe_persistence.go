package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func lockProbeProxyIdentity(ctx context.Context, exec sqlExecutor, id int64) (probeProxyIdentity, error) {
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
