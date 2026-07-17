package repository

import (
	"context"
	"errors"
	"reflect"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbproxy "github.com/Wei-Shaw/sub2api/ent/proxy"
	"github.com/Wei-Shaw/sub2api/internal/common"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"entgo.io/ent/dialect"
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
	if client.Driver().Dialect() != dialect.Postgres {
		return lockAndMergeAccountProbeExtraEnt(ctx, client, account)
	}

	credentials, err := common.Marshal(normalizeJSONMap(account.Credentials))
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
	probeExplicitlyDisabled := false
	if account.IsOpenAIApiKey() && identityUnchanged && len(currentEnabled) > 0 && string(currentEnabled) != "null" {
		var enabled any
		if err := common.Unmarshal(currentEnabled, &enabled); err != nil {
			return nil, err
		}
		extra[service.UpstreamBillingProbeEnabledExtraKey] = enabled
		if value, ok := enabled.(bool); ok && !value {
			probeExplicitlyDisabled = true
		}
	}
	if account.IsOpenAIApiKey() && identityUnchanged && !probeExplicitlyDisabled && len(currentSnapshot) > 0 && string(currentSnapshot) != "null" {
		var snapshot any
		if err := common.Unmarshal(currentSnapshot, &snapshot); err != nil {
			return nil, err
		}
		extra[service.UpstreamBillingProbeExtraKey] = snapshot
	}
	return extra, nil
}

func lockAndMergeAccountProbeExtraEnt(ctx context.Context, client *dbent.Client, account *service.Account) (map[string]any, error) {
	query := client.Account.Query().Where(dbaccount.IDEQ(account.ID), dbaccount.DeletedAtIsNil())
	if client.Driver().Dialect() != dialect.SQLite {
		query = query.ForUpdate()
	}
	current, err := query.Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrAccountNotFound
		}
		return nil, err
	}

	identityUnchanged := probeAccountIdentityMatches(current, account)
	extra := copyJSONMap(normalizeJSONMap(account.Extra))
	delete(extra, service.UpstreamBillingProbeEnabledExtraKey)
	delete(extra, service.UpstreamBillingProbeExtraKey)
	if account.IsOpenAIApiKey() && identityUnchanged {
		if enabled, ok := current.Extra[service.UpstreamBillingProbeEnabledExtraKey]; ok && enabled != nil {
			extra[service.UpstreamBillingProbeEnabledExtraKey] = enabled
			if value, ok := enabled.(bool); ok && !value {
				return extra, nil
			}
		}
		if snapshot, ok := current.Extra[service.UpstreamBillingProbeExtraKey]; ok && snapshot != nil {
			extra[service.UpstreamBillingProbeExtraKey] = snapshot
		}
	}
	return extra, nil
}

func probeAccountIdentityMatches(current *dbent.Account, account *service.Account) bool {
	if current == nil || account == nil || current.Platform != account.Platform || current.Type != account.Type ||
		!probeJSONEqual(current.Credentials, account.Credentials) {
		return false
	}
	if current.ProxyID == nil || account.ProxyID == nil {
		return current.ProxyID == nil && account.ProxyID == nil
	}
	return *current.ProxyID == *account.ProxyID
}

func probeJSONEqual(left, right any) bool {
	normalize := func(value any) (any, bool) {
		encoded, err := common.Marshal(value)
		if err != nil {
			return nil, false
		}
		var decoded any
		if err := common.Unmarshal(encoded, &decoded); err != nil {
			return nil, false
		}
		return decoded, true
	}
	normalizedLeft, leftOK := normalize(left)
	normalizedRight, rightOK := normalize(right)
	return leftOK && rightOK && reflect.DeepEqual(normalizedLeft, normalizedRight)
}

func (r *accountRepository) updateCredentialsWithProbe(ctx context.Context, id int64, credentials map[string]any) error {
	payload, err := common.Marshal(normalizeJSONMap(credentials))
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
	if client.Driver().Dialect() != dialect.Postgres {
		if err := updateCredentialsWithProbeEnt(ctx, client, id, credentials); err != nil {
			return err
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

func updateCredentialsWithProbeEnt(ctx context.Context, client *dbent.Client, id int64, credentials map[string]any) error {
	current, err := client.Account.Query().Where(dbaccount.IDEQ(id), dbaccount.DeletedAtIsNil()).Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return service.ErrAccountNotFound
		}
		return err
	}

	builder := client.Account.UpdateOneID(id).SetCredentials(normalizeJSONMap(credentials))
	if current.Platform == service.PlatformOpenAI && current.Type == service.AccountTypeAPIKey &&
		!probeJSONEqual(current.Credentials, credentials) {
		extra := copyJSONMap(current.Extra)
		delete(extra, service.UpstreamBillingProbeExtraKey)
		builder.SetExtra(extra)
	}
	if _, err := builder.Save(ctx); err != nil {
		if dbent.IsNotFound(err) {
			return service.ErrAccountNotFound
		}
		return err
	}
	return nil
}

func (r *accountRepository) updateExtraWithProbe(ctx context.Context, id int64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	payload, err := common.Marshal(updates)
	if err != nil {
		return err
	}
	clearSnapshot := upstreamBillingProbeSnapshotClearRequested(updates) || upstreamBillingProbeExplicitlyDisabled(updates)
	durableSchedulerChange := shouldEnqueueSchedulerOutboxForExtraUpdates(updates) || clearSnapshot
	baseCtx := ctx
	contextTx := dbent.TxFromContext(ctx)
	client := clientFromContext(ctx, r.client)
	var tx *dbent.Tx
	if (durableSchedulerChange || client.Driver().Dialect() != dialect.Postgres) && contextTx == nil {
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
	if client.Driver().Dialect() != dialect.Postgres {
		if err := updateExtraWithProbeEnt(ctx, client, id, updates, clearSnapshot); err != nil {
			return err
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
		} else if contextTx == nil {
			r.syncSchedulerAccountSnapshot(baseCtx, id)
		}
		return nil
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

func updateExtraWithProbeEnt(ctx context.Context, client *dbent.Client, id int64, updates map[string]any, clearSnapshot bool) error {
	query := client.Account.Query().Where(dbaccount.IDEQ(id), dbaccount.DeletedAtIsNil())
	if client.Driver().Dialect() != dialect.SQLite {
		query = query.ForUpdate()
	}
	current, err := query.Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return service.ErrAccountNotFound
		}
		return err
	}
	extra := copyJSONMap(current.Extra)
	for key, value := range updates {
		extra[key] = value
	}
	if clearSnapshot {
		delete(extra, service.UpstreamBillingProbeExtraKey)
	}
	if _, err := client.Account.UpdateOneID(id).SetExtra(extra).Save(ctx); err != nil {
		if dbent.IsNotFound(err) {
			return service.ErrAccountNotFound
		}
		return err
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
	if client.Driver().Dialect() != dialect.Postgres {
		return r.updateUpstreamBillingProbeSnapshotEnt(ctx, client, account, snapshot)
	}

	payload, err := common.Marshal(map[string]any{service.UpstreamBillingProbeExtraKey: snapshot})
	if err != nil {
		return err
	}
	credentials, err := common.Marshal(normalizeJSONMap(account.Credentials))
	if err != nil {
		return err
	}
	var expectedSnapshot any
	if account.Extra != nil {
		expectedSnapshot = account.Extra[service.UpstreamBillingProbeExtraKey]
	}
	expectedSnapshotJSON, err := common.Marshal(expectedSnapshot)
	if err != nil {
		return err
	}
	var expectedEnabled any
	if account.Extra != nil {
		expectedEnabled = account.Extra[service.UpstreamBillingProbeEnabledExtraKey]
	}
	expectedEnabledJSON, err := common.Marshal(expectedEnabled)
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

func (r *accountRepository) updateUpstreamBillingProbeSnapshotEnt(ctx context.Context, client *dbent.Client, account *service.Account, snapshot *service.UpstreamBillingProbeSnapshot) error {
	query := client.Account.Query().Where(dbaccount.IDEQ(account.ID), dbaccount.DeletedAtIsNil())
	if client.Driver().Dialect() != dialect.SQLite {
		query = query.ForUpdate()
	}
	current, err := query.Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return service.ErrUpstreamBillingProbeIdentityChanged
		}
		return err
	}
	if !probeAccountIdentityMatches(current, account) || !probeProxyIdentityMatches(ctx, client, account) ||
		!probeJSONEqual(current.Extra[service.UpstreamBillingProbeExtraKey], account.Extra[service.UpstreamBillingProbeExtraKey]) ||
		!probeJSONEqual(current.Extra[service.UpstreamBillingProbeEnabledExtraKey], account.Extra[service.UpstreamBillingProbeEnabledExtraKey]) {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}

	extra := copyJSONMap(current.Extra)
	extra[service.UpstreamBillingProbeExtraKey] = snapshot
	if _, err := client.Account.UpdateOneID(account.ID).SetExtra(extra).Save(ctx); err != nil {
		if dbent.IsNotFound(err) {
			return service.ErrUpstreamBillingProbeIdentityChanged
		}
		return err
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
	if client.Driver().Dialect() != dialect.Postgres {
		return probeProxyIdentityMatches(ctx, client, account), nil
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

func probeProxyIdentityMatches(ctx context.Context, client *dbent.Client, account *service.Account) bool {
	if account.ProxyID == nil {
		return true
	}
	query := client.Proxy.Query().Where(dbproxy.IDEQ(*account.ProxyID), dbproxy.DeletedAtIsNil())
	if client.Driver().Dialect() != dialect.SQLite {
		query = query.ForShare()
	}
	current, err := query.Only(ctx)
	if err != nil {
		return account.Proxy == nil
	}
	return account.Proxy != nil && current.ID == account.Proxy.ID &&
		current.Protocol == account.Proxy.Protocol && current.Host == account.Proxy.Host &&
		current.Port == account.Proxy.Port && derefString(current.Username) == account.Proxy.Username &&
		derefString(current.Password) == account.Proxy.Password && current.Status == account.Proxy.Status
}

func upstreamBillingProbeExplicitlyDisabled(updates map[string]any) bool {
	enabled, ok := updates[service.UpstreamBillingProbeEnabledExtraKey].(bool)
	return ok && !enabled
}

func upstreamBillingProbeSnapshotClearRequested(updates map[string]any) bool {
	value, ok := updates[service.UpstreamBillingProbeExtraKey]
	return ok && value == nil
}
