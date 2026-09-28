//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func (s *ProxyExpirySuite) TestRevertRejectsUnavailableOriginWithoutChangingAccount() {
	for _, unavailable := range []string{"soft deleted", "missing"} {
		s.Run(unavailable, func() {
			original := s.mkProxy("unavailable-origin", service.FallbackModeDirect, nil, nil)
			backup := s.mkProxy("current-backup", service.FallbackModeNone, nil, nil)
			account := s.mkAccountWithProxy(backup)
			_, err := s.tx.ExecContext(s.ctx, `
				UPDATE accounts SET platform='openai', type='apikey', proxy_fallback_origin_id=$1,
					extra='{"upstream_billing_probe":{"status":"ok"},"keep_me":true}'::jsonb
				WHERE id=$2`, original, account)
			s.Require().NoError(err)
			switch unavailable {
			case "soft deleted":
				s.Require().NoError(s.repo.Delete(s.ctx, original))
			case "missing":
				_, err = s.tx.ExecContext(s.ctx, `DELETE FROM proxies WHERE id=$1`, original)
				s.Require().NoError(err)
			}

			var outboxBefore int
			s.Require().NoError(scanSingleRow(s.ctx, s.tx, `
				SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type=$2`,
				[]any{account, service.SchedulerOutboxEventAccountChanged}, &outboxBefore))
			repo := newAccountRepositoryWithSQL(s.tx.Client(), s.tx, nil)
			s.ErrorIs(repo.RevertProxyFallback(s.ctx, account), service.ErrProxyNotFound)
			s.Equal(&backup, s.accountProxyID(account))

			var origin *int64
			var raw []byte
			s.Require().NoError(scanSingleRow(s.ctx, s.tx, `
				SELECT proxy_fallback_origin_id, extra FROM accounts WHERE id=$1`,
				[]any{account}, &origin, &raw))
			s.Equal(&original, origin, "failed restore must preserve the recovery marker")
			var extra map[string]any
			s.Require().NoError(json.Unmarshal(raw, &extra))
			s.Contains(extra, "upstream_billing_probe", "failed restore must preserve the current network's probe")
			s.Equal(true, extra["keep_me"])

			var outboxAfter int
			s.Require().NoError(scanSingleRow(s.ctx, s.tx, `
				SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type=$2`,
				[]any{account, service.SchedulerOutboxEventAccountChanged}, &outboxAfter))
			s.Equal(outboxBefore, outboxAfter, "failed restore must not enqueue a scheduler change")
		})
	}
}

func (s *ProxyExpirySuite) TestRevertWithoutFallbackKeepsExistingError() {
	proxyID := s.mkProxy("not-in-fallback", service.FallbackModeNone, nil, nil)
	account := s.mkAccountWithProxy(proxyID)
	repo := newAccountRepositoryWithSQL(s.tx.Client(), s.tx, nil)
	s.ErrorIs(repo.RevertProxyFallback(s.ctx, account), service.ErrAccountNotInFallback)
	s.Equal(&proxyID, s.accountProxyID(account))
}

func (s *ProxyExpirySuite) TestRevertLiveOriginEnqueuesAccountChange() {
	original := s.mkProxy("live-origin", service.FallbackModeNone, nil, nil)
	backup := s.mkProxy("live-backup", service.FallbackModeNone, nil, nil)
	account := s.mkAccountWithProxy(backup)
	_, err := s.tx.ExecContext(s.ctx, `UPDATE accounts SET proxy_fallback_origin_id=$1 WHERE id=$2`, original, account)
	s.Require().NoError(err)
	repo := newAccountRepositoryWithSQL(s.tx.Client(), s.tx, nil)
	s.Require().NoError(repo.RevertProxyFallback(s.ctx, account))
	s.Equal(&original, s.accountProxyID(account))
	var marker *int64
	s.Require().NoError(scanSingleRow(s.ctx, s.tx, `SELECT proxy_fallback_origin_id FROM accounts WHERE id=$1`, []any{account}, &marker))
	s.Nil(marker)
	var outboxCount int
	s.Require().NoError(scanSingleRow(s.ctx, s.tx, `
		SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type=$2`,
		[]any{account, service.SchedulerOutboxEventAccountChanged}, &outboxCount))
	s.Equal(1, outboxCount)
}

// FOR SHARE must conflict with the deleted_at UPDATE, not merely with key changes.
func TestRevertProxyHoldsLiveOriginLockAgainstSoftDelete(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	proxies := newProxyRepositoryWithSQL(client, integrationDB)
	original := &service.Proxy{
		Name: "locked-origin", Protocol: "http", Host: "127.0.0.1", Port: 8080,
		Status: service.StatusActive, FallbackMode: service.FallbackModeNone,
	}
	require.NoError(t, proxies.Create(ctx, original))
	account := mustCreateAccount(t, client, &service.Account{
		Name: "locked-restore", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"},
	})
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET proxy_fallback_origin_id=$1 WHERE id=$2`, original.ID, account.ID)
	require.NoError(t, err)
	restoreTx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(restoreTx.Client(), restoreTx, nil)
	require.NoError(t, repo.RevertProxyFallback(ctx, account.ID))

	deleteTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = deleteTx.Rollback() }()
	_, err = deleteTx.ExecContext(ctx, `SET LOCAL lock_timeout='100ms'`)
	require.NoError(t, err)
	_, err = deleteTx.ExecContext(ctx, `UPDATE proxies SET deleted_at=NOW() WHERE id=$1`, original.ID)
	var pgErr *pq.Error
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, pq.ErrorCode("55P03"), pgErr.Code)
}
