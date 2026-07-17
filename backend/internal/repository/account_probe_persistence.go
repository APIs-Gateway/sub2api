package repository

import (
	"context"
	"encoding/json"
	"errors"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *accountRepository) updateAccountWithProbe(ctx context.Context, account *service.Account) error {
	if account == nil {
		return nil
	}

	baseCtx := ctx
	contextTx := dbent.TxFromContext(ctx)
	client := r.client
	var tx *dbent.Tx
	if contextTx != nil {
		client = contextTx.Client()
	} else {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return err
		}
		if tx != nil {
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}

	updated, err := r.updateLockedAccountWithProbe(ctx, client, account)
	if err != nil {
		return translatePersistenceError(err, service.ErrAccountNotFound, nil)
	}
	if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &account.ID, nil, buildSchedulerGroupPayload(account.GroupIDs)); err != nil {
		return err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	account.UpdatedAt = updated.UpdatedAt
	if contextTx == nil {
		r.syncSchedulerAccountSnapshot(baseCtx, account.ID)
	}
	return nil
}

func (r *accountRepository) updateLockedAccountWithProbe(ctx context.Context, client *dbent.Client, account *service.Account) (*dbent.Account, error) {
	extra, err := lockAndMergeAccountProbeExtra(ctx, client, account)
	if err != nil {
		return nil, err
	}
	account.Extra = extra

	schedulable := account.Schedulable
	if account.Status == service.StatusError {
		schedulable = false
	}
	builder := client.Account.UpdateOneID(account.ID).
		SetName(account.Name).
		SetNillableNotes(account.Notes).
		SetPlatform(account.Platform).
		SetType(account.Type).
		SetCredentials(normalizeJSONMap(account.Credentials)).
		SetExtra(extra).
		SetConcurrency(account.Concurrency).
		SetPriority(account.Priority).
		SetStatus(account.Status).
		SetErrorMessage(account.ErrorMessage).
		SetSchedulable(schedulable).
		SetAutoPauseOnExpired(account.AutoPauseOnExpired)

	if account.RateMultiplier != nil {
		builder.SetRateMultiplier(*account.RateMultiplier)
	}
	if account.LoadFactor != nil {
		builder.SetLoadFactor(*account.LoadFactor)
	} else {
		builder.ClearLoadFactor()
	}
	if account.ProxyID != nil {
		builder.SetProxyID(*account.ProxyID)
	} else {
		builder.ClearProxyID()
	}
	if account.LastUsedAt != nil {
		builder.SetLastUsedAt(*account.LastUsedAt)
	} else {
		builder.ClearLastUsedAt()
	}
	if account.ExpiresAt != nil {
		builder.SetExpiresAt(*account.ExpiresAt)
	} else {
		builder.ClearExpiresAt()
	}
	if account.RateLimitedAt != nil {
		builder.SetRateLimitedAt(*account.RateLimitedAt)
	} else {
		builder.ClearRateLimitedAt()
	}
	if account.RateLimitResetAt != nil {
		builder.SetRateLimitResetAt(*account.RateLimitResetAt)
	} else {
		builder.ClearRateLimitResetAt()
	}
	if account.OverloadUntil != nil {
		builder.SetOverloadUntil(*account.OverloadUntil)
	} else {
		builder.ClearOverloadUntil()
	}
	if account.SessionWindowStart != nil {
		builder.SetSessionWindowStart(*account.SessionWindowStart)
	} else {
		builder.ClearSessionWindowStart()
	}
	if account.SessionWindowEnd != nil {
		builder.SetSessionWindowEnd(*account.SessionWindowEnd)
	} else {
		builder.ClearSessionWindowEnd()
	}
	if account.SessionWindowStatus != "" {
		builder.SetSessionWindowStatus(account.SessionWindowStatus)
	} else {
		builder.ClearSessionWindowStatus()
	}
	if account.Notes == nil {
		builder.ClearNotes()
	}
	return builder.Save(ctx)
}

func lockAndMergeAccountProbeExtra(ctx context.Context, client *dbent.Client, account *service.Account) (map[string]any, error) {
	credentials, err := json.Marshal(normalizeJSONMap(account.Credentials))
	if err != nil {
		return nil, err
	}
	var proxyID any
	if account.ProxyID != nil {
		proxyID = *account.ProxyID
	}
	rows, err := client.QueryContext(ctx, `
		SELECT
			platform = $2
			AND type = $3
			AND credentials = $4::jsonb
			AND proxy_id IS NOT DISTINCT FROM $5,
			extra -> 'upstream_billing_probe_enabled',
			extra -> 'upstream_billing_probe'
		FROM accounts
		WHERE id = $1 AND deleted_at IS NULL
		FOR NO KEY UPDATE
	`, account.ID, account.Platform, account.Type, string(credentials), proxyID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrAccountNotFound
	}
	var (
		identityUnchanged bool
		currentEnabled    []byte
		currentSnapshot   []byte
	)
	if err := rows.Scan(&identityUnchanged, &currentEnabled, &currentSnapshot); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	extra := copyJSONMap(normalizeJSONMap(account.Extra))
	delete(extra, service.UpstreamBillingProbeEnabledExtraKey)
	delete(extra, service.UpstreamBillingProbeExtraKey)
	if account.IsOpenAIApiKey() && identityUnchanged && len(currentEnabled) > 0 && string(currentEnabled) != "null" {
		var enabled any
		if err := json.Unmarshal(currentEnabled, &enabled); err != nil {
			return nil, err
		}
		extra[service.UpstreamBillingProbeEnabledExtraKey] = enabled
	}
	if account.IsOpenAIApiKey() && identityUnchanged && len(currentSnapshot) > 0 && string(currentSnapshot) != "null" {
		var snapshot any
		if err := json.Unmarshal(currentSnapshot, &snapshot); err != nil {
			return nil, err
		}
		extra[service.UpstreamBillingProbeExtraKey] = snapshot
	}
	return extra, nil
}

func (r *accountRepository) updateCredentialsWithProbe(ctx context.Context, id int64, credentials map[string]any) error {
	payload, err := json.Marshal(normalizeJSONMap(credentials))
	if err != nil {
		return err
	}
	baseCtx := ctx
	contextTx := dbent.TxFromContext(ctx)
	client := r.client
	var tx *dbent.Tx
	if contextTx != nil {
		client = contextTx.Client()
	} else {
		tx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return err
		}
		if tx != nil {
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}
	result, err := client.ExecContext(ctx, `
		UPDATE accounts
		SET
			credentials = $1::jsonb,
			extra = CASE
				WHEN platform = 'openai'
					AND type = 'apikey'
					AND credentials IS DISTINCT FROM $1::jsonb
				THEN COALESCE(extra, '{}'::jsonb) - 'upstream_billing_probe'
				ELSE extra
			END,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
	`, string(payload), id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAccountNotFound
	}
	if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if contextTx == nil {
		r.syncSchedulerAccountSnapshot(baseCtx, id)
	}
	return nil
}

func (r *accountRepository) updateExtraWithProbe(ctx context.Context, id int64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	payload, err := json.Marshal(updates)
	if err != nil {
		return err
	}
	clearSnapshot := upstreamBillingProbeSnapshotClearRequested(updates) || upstreamBillingProbeExplicitlyDisabled(updates)
	durableSchedulerChange := shouldEnqueueSchedulerOutboxForExtraUpdates(updates) || clearSnapshot
	baseCtx := ctx
	contextTx := dbent.TxFromContext(ctx)
	client := clientFromContext(ctx, r.client)
	var tx *dbent.Tx
	if durableSchedulerChange && contextTx == nil {
		var txErr error
		tx, txErr = r.client.Tx(ctx)
		if txErr != nil && !errors.Is(txErr, dbent.ErrTxStarted) {
			return txErr
		}
		if tx != nil {
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}
	result, err := client.ExecContext(ctx, `
		UPDATE accounts
		SET extra = CASE
			WHEN $3 THEN (COALESCE(extra, '{}'::jsonb) || $1::jsonb) - 'upstream_billing_probe'
			ELSE COALESCE(extra, '{}'::jsonb) || $1::jsonb
		END,
		updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
	`, string(payload), id, clearSnapshot)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAccountNotFound
	}
	if durableSchedulerChange {
		if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			return err
		}
		if tx != nil {
			if err := tx.Commit(); err != nil {
				return err
			}
		}
		if contextTx == nil {
			r.syncSchedulerAccountSnapshot(baseCtx, id)
		}
	} else {
		if dbent.TxFromContext(ctx) == nil {
			r.syncSchedulerAccountSnapshot(ctx, id)
		}
	}
	return nil
}

func (r *accountRepository) UpdateUpstreamBillingProbeSnapshot(ctx context.Context, account *service.Account, snapshot *service.UpstreamBillingProbeSnapshot) error {
	if account == nil || snapshot == nil {
		return service.ErrAccountNilInput
	}
	baseCtx := ctx
	contextTx := dbent.TxFromContext(ctx)
	client := r.client
	var tx *dbent.Tx
	if contextTx != nil {
		client = contextTx.Client()
	} else {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return err
		}
		if tx != nil {
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}
	if err := r.updateUpstreamBillingProbeSnapshotInTx(ctx, client, account, snapshot); err != nil {
		return err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if contextTx == nil {
		r.syncSchedulerAccountSnapshot(baseCtx, account.ID)
	}
	return nil
}

func (r *accountRepository) updateUpstreamBillingProbeSnapshotInTx(ctx context.Context, client *dbent.Client, account *service.Account, snapshot *service.UpstreamBillingProbeSnapshot) error {
	payload, err := json.Marshal(map[string]any{service.UpstreamBillingProbeExtraKey: snapshot})
	if err != nil {
		return err
	}
	credentials, err := json.Marshal(normalizeJSONMap(account.Credentials))
	if err != nil {
		return err
	}
	var expectedSnapshot any
	if account.Extra != nil {
		expectedSnapshot = account.Extra[service.UpstreamBillingProbeExtraKey]
	}
	expectedSnapshotJSON, err := json.Marshal(expectedSnapshot)
	if err != nil {
		return err
	}
	var expectedEnabled any
	if account.Extra != nil {
		expectedEnabled = account.Extra[service.UpstreamBillingProbeEnabledExtraKey]
	}
	expectedEnabledJSON, err := json.Marshal(expectedEnabled)
	if err != nil {
		return err
	}
	if matches, err := lockAndMatchProbeProxyIdentity(ctx, client, account); err != nil {
		return err
	} else if !matches {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	var proxyID any
	if account.ProxyID != nil {
		proxyID = *account.ProxyID
	}
	result, err := client.ExecContext(ctx, `
		UPDATE accounts
		SET extra = COALESCE(extra, '{}'::jsonb) || $1::jsonb, updated_at = NOW()
		WHERE id = $2
			AND platform = $3
			AND type = $4
			AND credentials = $5::jsonb
			AND proxy_id IS NOT DISTINCT FROM $6
			AND COALESCE(extra -> 'upstream_billing_probe', 'null'::jsonb) = $7::jsonb
			AND COALESCE(extra -> 'upstream_billing_probe_enabled', 'null'::jsonb) = $8::jsonb
			AND deleted_at IS NULL
	`, string(payload), account.ID, account.Platform, account.Type, string(credentials), proxyID, string(expectedSnapshotJSON), string(expectedEnabledJSON))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	return enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &account.ID, nil, nil)
}

type probeProxyIdentity struct {
	protocol string
	host     string
	port     int
	username string
	password string
	status   string
}

func probeProxyIdentityFromService(proxyIn *service.Proxy) probeProxyIdentity {
	if proxyIn == nil {
		return probeProxyIdentity{}
	}
	return probeProxyIdentity{
		protocol: proxyIn.Protocol,
		host:     proxyIn.Host,
		port:     proxyIn.Port,
		username: proxyIn.Username,
		password: proxyIn.Password,
		status:   proxyIn.Status,
	}
}

func lockAndMatchProbeProxyIdentity(ctx context.Context, client *dbent.Client, account *service.Account) (bool, error) {
	if account.ProxyID == nil {
		return true, nil
	}
	rows, err := client.QueryContext(ctx, `
		SELECT protocol, host, port, COALESCE(username, ''), COALESCE(password, ''), status
		FROM proxies
		WHERE id = $1 AND deleted_at IS NULL
		FOR SHARE
	`, *account.ProxyID)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return account.Proxy == nil, nil
	}
	if account.Proxy == nil || account.Proxy.ID != *account.ProxyID {
		return false, nil
	}
	var current probeProxyIdentity
	if err := rows.Scan(&current.protocol, &current.host, &current.port, &current.username, &current.password, &current.status); err != nil {
		return false, err
	}
	return current == probeProxyIdentityFromService(account.Proxy), rows.Err()
}

func upstreamBillingProbeExplicitlyDisabled(updates map[string]any) bool {
	enabled, ok := updates[service.UpstreamBillingProbeEnabledExtraKey].(bool)
	return ok && !enabled
}

func upstreamBillingProbeSnapshotClearRequested(updates map[string]any) bool {
	value, ok := updates[service.UpstreamBillingProbeExtraKey]
	return ok && value == nil
}
