//go:build integration

package repository

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBillingInflightPostgres_StorageFailureRollsBackFunding(t *testing.T) {
	for _, failure := range []string{"admission_card_query", "owner_insert", "attempt_insert", "pending_insert", "attempt_adjustment", "renew_owner", "renew_child", "finish", "release", "settlement_consume"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			repo, user, key := inflightFixture(t, 10)
			db := inflightTestDB(t)
			owner := uuid.NewString()
			cmd := &service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, BalanceCost: 1, RateMultiplier: 1}
			if failure != "admission_card_query" && failure != "owner_insert" && failure != "attempt_insert" {
				ok, err := repo.ReserveBillingInflight(ctx, user.ID, owner, 0.5, false, time.Minute)
				require.NoError(t, err)
				require.True(t, ok)
			}
			if failure == "settlement_consume" {
				id, err := repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
				require.NoError(t, err)
				cmd.InflightObligationID = id
			}
			var ownerDeadline, attemptDeadline time.Time
			if failure == "renew_owner" || failure == "renew_child" {
				require.NoError(t, db.QueryRow(`SELECT expires_at FROM billing_inflight_leases WHERE id=$1`, owner).Scan(&ownerDeadline))
				require.NoError(t, db.QueryRow(`SELECT expires_at FROM billing_inflight_leases WHERE id=$1`, owner+":initial").Scan(&attemptDeadline))
			}
			// Inject a storage failure into this testing.T's dedicated database.
			// PostgreSQL transactions must preserve the old hold and wallet when
			// an owner/attempt/obligation operation fails halfway through.
			var sql string
			switch failure {
			case "admission_card_query":
				sql = `ALTER TABLE user_subscriptions RENAME TO unavailable_subscriptions`
			case "owner_insert":
				sql = `ALTER TABLE billing_inflight_leases ADD CONSTRAINT injected_failure CHECK (phase <> 'owner')`
			case "attempt_insert":
				sql = `ALTER TABLE billing_inflight_leases ADD CONSTRAINT injected_failure CHECK (phase <> 'attempt')`
			case "pending_insert":
				sql = `ALTER TABLE billing_inflight_leases ADD CONSTRAINT injected_failure CHECK (phase <> 'pending')`
			case "settlement_consume":
				sql = `ALTER TABLE billing_inflight_leases ADD CONSTRAINT injected_failure CHECK (phase <> 'settled')`
			default:
				event, condition := "UPDATE", "OLD.phase = 'attempt'"
				if failure == "renew_owner" {
					condition = "OLD.phase = 'owner'"
				}
				if failure == "release" {
					event, condition = "DELETE", "true"
				}
				_, err := db.Exec(`CREATE FUNCTION inflight_injected_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected storage outage'; END $$`)
				require.NoError(t, err)
				sql = `CREATE TRIGGER inflight_failure BEFORE ` + event + ` ON billing_inflight_leases FOR EACH ROW WHEN (` + condition + `) EXECUTE FUNCTION inflight_injected_failure()`
			}
			_, err := db.Exec(sql)
			require.NoError(t, err)
			switch failure {
			case "admission_card_query", "owner_insert", "attempt_insert":
				ok, err := repo.ReserveBillingInflight(ctx, user.ID, owner, 0.5, false, time.Minute)
				require.Error(t, err)
				require.False(t, ok)
			case "pending_insert", "attempt_adjustment":
				_, err := repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
				require.Error(t, err)
			case "renew_owner", "renew_child":
				_, err := repo.RenewBillingInflight(ctx, user.ID, owner, time.Hour)
				require.Error(t, err)
			case "finish":
				require.Error(t, repo.FinishBillingInflightAttempt(ctx, user.ID, owner, owner+":initial"))
			case "release":
				require.Error(t, repo.ReleaseBillingInflight(ctx, user.ID, owner))
			case "settlement_consume":
				result, err := repo.Apply(ctx, cmd)
				require.Error(t, err)
				require.Nil(t, result)
			}
			var wallet float64
			var count, dedup int
			require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&wallet))
			require.Equal(t, 10.0, wallet, "failed settlement must not debit the wallet")
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, key.ID).Scan(&dedup))
			require.Zero(t, dedup, "failed consume must roll back the billing claim too")
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE user_id=$1`, user.ID).Scan(&count))
			if failure == "admission_card_query" || failure == "owner_insert" || failure == "attempt_insert" {
				require.Zero(t, count, "failed admission cannot leave a partial owner")
			} else if failure == "settlement_consume" {
				require.Equal(t, 3, count)
				require.InDelta(t, 1, inflightHeld(t, user.ID), 1e-8, "pending actual remains held when atomic consume fails")
			} else {
				require.Equal(t, 2, count)
				require.InDelta(t, 0.5, inflightHeld(t, user.ID), 1e-8, "storage failure must preserve the estimate")
			}
			if failure == "renew_owner" || failure == "renew_child" {
				var ownerAfter, attemptAfter time.Time
				require.NoError(t, db.QueryRow(`SELECT expires_at FROM billing_inflight_leases WHERE id=$1`, owner).Scan(&ownerAfter))
				require.NoError(t, db.QueryRow(`SELECT expires_at FROM billing_inflight_leases WHERE id=$1`, owner+":initial").Scan(&attemptAfter))
				require.True(t, ownerDeadline.Equal(ownerAfter), "a child renewal failure must roll back the earlier owner renewal")
				require.True(t, attemptDeadline.Equal(attemptAfter), "failed renewal may not extend any child deadline")
			}
		})
	}
}

func TestBillingInflightPostgres_MissingUserAndCanceledAdmissionDoNotMutate(t *testing.T) {
	repo, user, _ := inflightFixture(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ok, err := repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 1, false, time.Minute)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, ok)
	ok, err = repo.ReserveBillingInflight(context.Background(), -1, uuid.NewString(), 1, false, time.Minute)
	require.ErrorIs(t, err, service.ErrUserNotFound)
	require.False(t, ok)
	for _, amount := range []float64{-1, math.Inf(1), math.NaN()} {
		ok, err := repo.ReserveBillingInflight(context.Background(), user.ID, uuid.NewString(), amount, false, time.Minute)
		require.ErrorIs(t, err, service.ErrBillingInflightIdentity)
		require.False(t, ok)
	}
	var count int
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM billing_inflight_leases`).Scan(&count))
	require.Zero(t, count)
}

func TestBillingInflightPostgres_UnavailableLedgerPreservesExistingObligation(t *testing.T) {
	ctx := context.Background()
	repo, user, key := inflightFixture(t, 10)
	db := inflightTestDB(t)
	owner := uuid.NewString()
	ok, err := repo.ReserveBillingInflight(ctx, user.ID, owner, 0.5, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	cmd := &service.UsageBillingCommand{RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, BalanceCost: 1, RateMultiplier: 1}
	id, err := repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
	require.NoError(t, err)
	cmd.InflightObligationID = id
	_, err = db.Exec(`ALTER TABLE billing_inflight_leases RENAME TO unavailable_ledger`)
	require.NoError(t, err)
	ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 1, false, time.Minute)
	require.Error(t, err)
	require.False(t, ok)
	ok, err = repo.ResizeBillingInflight(ctx, user.ID, owner, uuid.NewString(), 1, false, time.Minute)
	require.Error(t, err)
	require.False(t, ok)
	_, err = repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
	require.Error(t, err)
	result, err := repo.Apply(ctx, cmd)
	require.Error(t, err)
	require.Nil(t, result)
	_, err = db.Exec(`ALTER TABLE unavailable_ledger RENAME TO billing_inflight_leases`)
	require.NoError(t, err)
	var wallet float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&wallet))
	require.Equal(t, 10.0, wallet)
	require.InDelta(t, 1, inflightHeld(t, user.ID), 1e-8)
	result, err = repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied, "a recovered ledger must allow the original obligation to settle once")
	require.InDelta(t, 9, *result.NewBalance, 1e-8)
	require.Zero(t, inflightHeld(t, user.ID))
}

func TestBillingInflightPostgres_ObligationIdentityConflictRollsBackBilling(t *testing.T) {
	ctx := context.Background()
	repo, user, key := inflightFixture(t, 10)
	owner := uuid.NewString()
	ok, err := repo.ReserveBillingInflight(ctx, user.ID, owner, 0.5, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	cmd := &service.UsageBillingCommand{RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, BalanceCost: 1, RequestFingerprint: "original-payload", RateMultiplier: 1}
	id, err := repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
	require.NoError(t, err)
	cmd.InflightObligationID = id
	conflict := *cmd
	conflict.RequestFingerprint = "conflicting-payload"
	result, err := repo.Apply(ctx, &conflict)
	require.ErrorIs(t, err, service.ErrBillingInflightIdentity)
	require.Nil(t, result)
	var wallet float64
	var dedup int
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&wallet))
	require.Equal(t, 10.0, wallet)
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, key.ID).Scan(&dedup))
	require.Zero(t, dedup)
	require.InDelta(t, 1, inflightHeld(t, user.ID), 1e-8, "a mismatched command cannot consume someone else's pending cost")
	result, err = repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 9, *result.NewBalance, 1e-8)
}
