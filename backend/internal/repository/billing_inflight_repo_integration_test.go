//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func inflightFixture(t *testing.T, balance float64) (*usageBillingRepository, *service.User, *service.APIKey) {
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@inflight.test", PasswordHash: "hash", Balance: balance})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-" + uuid.NewString(), Name: "inflight"})
	return NewUsageBillingRepository(client, integrationDB).(*usageBillingRepository), user, key
}

func inflightHeld(t *testing.T, userID int64) float64 {
	var amount float64
	require.NoError(t, integrationDB.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM billing_inflight_leases WHERE user_id=$1 AND expires_at>clock_timestamp()`, userID).Scan(&amount))
	return amount
}

func TestBillingInflightPostgres_ConcurrentWalletAndFirstRequest(t *testing.T) {
	ctx := context.Background()
	for _, estimate := range []float64{1, 3} {
		t.Run(fmt.Sprint(estimate), func(t *testing.T) {
			repo, user, _ := inflightFixture(t, 1)
			var wg sync.WaitGroup
			results := make(chan bool, 20)
			errs := make(chan error, 20)
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					ok, err := repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), estimate, false, time.Minute)
					results <- ok
					errs <- err
				}()
			}
			wg.Wait()
			close(results)
			close(errs)
			admitted := 0
			for err := range errs {
				require.NoError(t, err)
			}
			for ok := range results {
				if ok {
					admitted++
				}
			}
			require.Equal(t, 1, admitted, "only the first estimate may exceed historical single-request funding")
			require.InDelta(t, estimate, inflightHeld(t, user.ID), 1e-8)
		})
	}
}

func TestBillingInflightPostgres_KnownCostAndAtomicSettlement(t *testing.T) {
	ctx := context.Background()
	for _, officialOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(officialOnly), func(t *testing.T) {
			repo, user, key := inflightFixture(t, 10)
			owner := uuid.NewString()
			ok, err := repo.ReserveBillingInflight(ctx, user.ID, owner, 0.1, false, time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			cmd := &service.UsageBillingCommand{RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, RateMultiplier: 3}
			if officialOnly {
				cmd.OfficialCost = 2
			} else {
				cmd.BalanceCost = 2
			}
			id, err := repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
			require.NoError(t, err)
			require.InDelta(t, 6, inflightHeld(t, user.ID), 1e-8, "known actual must grow the estimate before Apply")
			ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 5, false, time.Minute)
			require.NoError(t, err)
			require.False(t, ok)
			cmd.InflightObligationID = id
			result, err := repo.Apply(ctx, cmd)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.InDelta(t, 4, *result.NewBalance, 1e-8)
			require.InDelta(t, 0, inflightHeld(t, user.ID), 1e-8)
			result, err = repo.Apply(ctx, cmd)
			require.NoError(t, err)
			require.False(t, result.Applied)
			require.NoError(t, repo.ReleaseBillingInflight(ctx, user.ID, owner))
			var n int
			require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE user_id=$1`, user.ID).Scan(&n))
			require.Zero(t, n)
		})
	}
}

func TestBillingInflightPostgres_NoExpiredResurrection(t *testing.T) {
	ctx := context.Background()
	repo, user, key := inflightFixture(t, 10)
	owner := uuid.NewString()
	ok, err := repo.ReserveBillingInflight(ctx, user.ID, owner, 1, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	cmd := &service.UsageBillingCommand{RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, BalanceCost: 1}
	child, err := repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE billing_inflight_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, child)
	require.NoError(t, err)
	ok, err = repo.RenewBillingInflight(ctx, user.ID, owner, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	var expired bool
	require.NoError(t, integrationDB.QueryRow(`SELECT expires_at<=clock_timestamp() FROM billing_inflight_leases WHERE id=$1`, child).Scan(&expired))
	require.True(t, expired)
	_, err = integrationDB.Exec(`UPDATE billing_inflight_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1 OR owner_id=$1`, owner)
	require.NoError(t, err)
	ok, err = repo.ResizeBillingInflight(ctx, user.ID, owner, uuid.NewString(), 1, false, time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = repo.RenewBillingInflight(ctx, user.ID, owner, time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
	var n int
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE owner_id=$1 AND phase='attempt'`, owner).Scan(&n))
	require.Equal(t, 1, n)
	ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 10, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "dead owner TTL recovers admission")
	cmd.InflightObligationID = child
	result, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied, "late actual billing must still apply after TTL")
}

func TestBillingInflightPostgres_PartialAttemptInterleavings(t *testing.T) {
	for _, ordering := range []string{"old_settled", "old_pending", "old_cost_after_resize"} {
		t.Run(ordering, func(t *testing.T) {
			ctx := context.Background()
			repo, user, key := inflightFixture(t, 2)
			owner := uuid.NewString()
			ok, err := repo.ReserveBillingInflight(ctx, user.ID, owner, 0.5, false, time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			cmd := &service.UsageBillingCommand{RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, BalanceCost: 0.5}
			stage := func() {
				id, err := repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
				require.NoError(t, err)
				cmd.InflightObligationID = id
				require.NoError(t, repo.FinishBillingInflightAttempt(ctx, user.ID, owner, owner+":initial"))
			}
			if ordering != "old_cost_after_resize" {
				stage()
			}
			if ordering == "old_settled" {
				res, err := repo.Apply(ctx, cmd)
				require.NoError(t, err)
				require.True(t, res.Applied)
			}
			attempt := uuid.NewString()
			ok, err = repo.ResizeBillingInflight(ctx, user.ID, owner, attempt, 0.5, false, time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			if ordering == "old_cost_after_resize" {
				stage()
			}
			var newEstimate float64
			require.NoError(t, integrationDB.QueryRow(`SELECT amount FROM billing_inflight_leases WHERE id=$1`, attempt).Scan(&newEstimate))
			require.InDelta(t, 0.5, newEstimate, 1e-8, "old worker must never consume the new attempt estimate")
			ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 1.25, false, time.Minute)
			require.NoError(t, err)
			require.False(t, ok, "pending old charge plus new attempt, or settled balance plus new attempt, reserves the same funding")
			ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 1, false, time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
		})
	}
}

func TestBillingInflightPostgres_ArchivedDedupAndConflict(t *testing.T) {
	ctx := context.Background()
	repo, user, key := inflightFixture(t, 10)
	cmd := &service.UsageBillingCommand{RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, BalanceCost: 1}
	res, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, res.Applied)
	_, err = integrationDB.Exec(`UPDATE usage_billing_dedup SET created_at=clock_timestamp()-interval '400 days' WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, key.ID)
	require.NoError(t, err)
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(integrationDB).CleanupUsageBillingDedup(ctx, time.Now().AddDate(0, 0, -365)))
	owner := uuid.NewString()
	ok, err := repo.ReserveBillingInflight(ctx, user.ID, owner, 1, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	child, err := repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
	require.NoError(t, err)
	cmd.InflightObligationID = child
	res, err = repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, res.Applied)
	var n int
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, key.ID).Scan(&n))
	require.Zero(t, n, "archived key must not return to active dedup")
	var phase string
	require.NoError(t, integrationDB.QueryRow(`SELECT phase FROM billing_inflight_leases WHERE id=$1`, child).Scan(&phase))
	require.Equal(t, "settled", phase)
	cmd.BalanceCost = 2
	cmd.RequestFingerprint = ""
	cmd.Normalize()
	_, err = repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
	require.ErrorIs(t, err, service.ErrBillingInflightIdentity)
	_, err = repo.Apply(ctx, cmd)
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	var balance float64
	require.NoError(t, integrationDB.QueryRow(`SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&balance))
	require.InDelta(t, 9, balance, 1e-8)
}

func TestBillingInflightPostgres_CardWalletAndWindowBudget(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name                        string
		wallet, d, w, m, du, wu, mu float64
		estimate                    float64
		want                        error
	}{
		{"card_only", 0, 1, 10, 20, 0, 0, 0, 1, nil},
		{"negative_wallet_card", -5, 1, 10, 20, 0, 0, 0, 1, nil},
		{"card_plus_wallet", 1, 1, 10, 20, 0, 0, 0, 2, nil},
		{"daily_full_wallet_fallback", 1, 1, 10, 20, 1, 0, 0, 1, nil},
		{"daily_full", 0, 1, 10, 20, 1, 0, 0, 1, service.ErrDailyLimitExceeded},
		{"weekly_full", 0, 10, 1, 20, 0, 1, 0, 1, service.ErrWeeklyLimitExceeded},
		{"monthly_full", 0, 10, 20, 1, 0, 0, 1, 1, service.ErrMonthlyLimitExceeded},
		{"null_limits_safety_gate", 0, 0, 0, 0, 0, 0, 0, 1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, user, key := inflightFixture(t, tc.wallet)
			admissionCard(t, testEntClient(t), user.ID, 0, tc.d, tc.w, tc.m, tc.du, tc.wu, tc.mu)
			owner := uuid.NewString()
			ok, err := repo.ReserveBillingInflight(ctx, user.ID, owner, tc.estimate, false, time.Minute)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
				require.False(t, ok)
				return
			}
			require.NoError(t, err)
			if tc.name == "null_limits_safety_gate" {
				require.False(t, ok)
				return
			}
			require.True(t, ok)
			ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), tc.estimate, false, time.Minute)
			require.NoError(t, err)
			require.False(t, ok, "cross-group card funding must not be admitted twice")
			cmd := &service.UsageBillingCommand{RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, OfficialCost: tc.estimate, RateMultiplier: 1}
			child, err := repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
			require.NoError(t, err)
			cmd.InflightObligationID = child
			result, err := repo.Apply(ctx, cmd)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.NotNil(t, result.SubscriptionID)
			require.InDelta(t, 0, inflightHeld(t, user.ID), 1e-8, "card/wallet mutation and hold consumption commit together")
			wantWallet := tc.wallet
			if tc.name == "card_plus_wallet" || tc.name == "daily_full_wallet_fallback" {
				wantWallet = 0
			}
			require.InDelta(t, wantWallet, *result.NewBalance, 1e-8)
		})
	}
}

func TestBillingInflightPostgres_ResetExpiryRechargeAndExclusive(t *testing.T) {
	ctx := context.Background()
	repo, user, _ := inflightFixture(t, 0)
	admissionCard(t, testEntClient(t), user.ID, 0, 1, 10, 20, 1, 0, 0)
	_, err := integrationDB.Exec(`UPDATE user_subscriptions SET daily_window_start=clock_timestamp()-interval '2 days' WHERE user_id=$1`, user.ID)
	require.NoError(t, err)
	owner := uuid.NewString()
	ok, err := repo.ReserveBillingInflight(ctx, user.ID, owner, 1, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "same natural-window reset as SettleWindow")
	_, err = integrationDB.Exec(`UPDATE user_subscriptions SET expires_at=clock_timestamp()-interval '1 second' WHERE user_id=$1`, user.ID)
	require.NoError(t, err)
	ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 1, false, time.Minute)
	require.NoError(t, err)
	require.False(t, ok, "expired active card must not fund admission")
	_, err = integrationDB.Exec(`UPDATE users SET balance=3 WHERE id=$1`, user.ID)
	require.NoError(t, err)
	ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 2, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "recharge is read from authoritative PG rather than stale cache")
	repo, user, _ = inflightFixture(t, 10)
	owner = uuid.NewString()
	ok, err = repo.ReserveBillingInflight(ctx, user.ID, owner, 0, true, time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "first unknown-price request retains historical admission")
	ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 1, false, time.Minute)
	require.NoError(t, err)
	require.False(t, ok, "unknown-price hold cannot be bypassed by aliases")
	ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 0, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "known-free requests need no shared budget")
	require.NoError(t, repo.FinishBillingInflightAttempt(ctx, user.ID, owner, owner+":initial"))
	ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 10, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "proven no-charge clears the exclusive attempt")
}
