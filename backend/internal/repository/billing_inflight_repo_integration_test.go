//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
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

func TestBillingInflightPostgres_DeletedKeyLateSettlementAcrossGroups(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo, user, firstKey := inflightFixture(t, 2)
	group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformOpenAI})
	secondKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-" + uuid.NewString(), Name: "other-group"})
	owner := uuid.NewString()
	ok, err := repo.ReserveBillingInflight(ctx, user.ID, owner, 1, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, NewAPIKeyRepository(client, integrationDB).DeleteWithAudit(ctx, firstKey.ID))
	cmd := &service.UsageBillingCommand{UserID: user.ID, APIKeyID: firstKey.ID, RequestID: uuid.NewString(), OfficialCost: 1.5, RateMultiplier: 1}
	child, err := repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
	require.NoError(t, err)
	cmd.InflightObligationID = child
	require.InDelta(t, 1.5, inflightHeld(t, user.ID), 1e-8)
	ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 1, false, time.Minute)
	require.NoError(t, err)
	require.False(t, ok, "other group and key share the user's pending obligation")
	result, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 0.5, *result.NewBalance, 1e-8)
	require.Zero(t, inflightHeld(t, user.ID))
	result, err = repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result.Applied)
	var n int
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, firstKey.ID).Scan(&n))
	require.Equal(t, 1, n)
	newOwner := uuid.NewString()
	ok, err = repo.ReserveBillingInflight(ctx, user.ID, newOwner, 0.5, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	secondCmd := &service.UsageBillingCommand{UserID: user.ID, APIKeyID: secondKey.ID, RequestID: uuid.NewString(), OfficialCost: 0.5, RateMultiplier: 1}
	secondCmd.InflightObligationID, err = repo.StageBillingInflight(ctx, user.ID, newOwner, newOwner+":initial", secondCmd, time.Minute)
	require.NoError(t, err)
	result, err = repo.Apply(ctx, secondCmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Zero(t, *result.NewBalance)
	require.Zero(t, inflightHeld(t, user.ID))
}

func TestBillingInflightPostgres_RenewalAndAdmissionLockOrder(t *testing.T) {
	client := testEntClient(t)
	repo, user, _ := inflightFixture(t, 10)
	today := service.TodayEastDayNumber()
	card := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, DailyAmountUSD: 1000, TodayRemaining: 1000, TodayDay: today, StartDay: today, ExpireDay: today + 10, ExpiresAt: service.ExpireDayToExpiresAt(today + 10), Status: service.SubscriptionStatusActive})
	_, err := integrationDB.Exec(`UPDATE users SET concurrency=1 WHERE id=$1`, user.ID)
	require.NoError(t, err)
	svc := service.NewSubscriptionService(NewGroupRepository(client, integrationDB), NewUserSubscriptionRepository(client), NewUserRepository(client, integrationDB), nil, nil, client, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gate, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = gate.Rollback() }()
	_, err = gate.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, user.ID)
	require.NoError(t, err)
	reserveDone, renewDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 1, false, time.Minute)
		reserveDone <- err
	}()
	// Put admission first in the user lock queue. Renewal must obtain that same
	// user lock before card, not hold card while waiting on the user update.
	require.Eventually(t, func() bool {
		var n int
		err := integrationDB.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%SELECT balance FROM users%'`).Scan(&n)
		return err == nil && n > 0
	}, 3*time.Second, 10*time.Millisecond)
	go func() { _, err := svc.ApplyRenewFromOrder(ctx, card.ID, 30); renewDone <- err }()
	require.Eventually(t, func() bool {
		var n int
		err := integrationDB.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND (query LIKE '%UPDATE%users%' OR query LIKE '%SELECT%users%')`).Scan(&n)
		return err == nil && n >= 2
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, gate.Commit())
	require.NoError(t, <-reserveDone, "admission must not deadlock with renewal's concurrency update")
	require.NoError(t, <-renewDone)
	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+40, got.ExpireDay)
	u, err := NewUserRepository(client, integrationDB).GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, u.Concurrency, 100)
	require.InDelta(t, 10, u.Balance, 1e-8, "renewal still never charges wallet")
}

func TestBillingInflightPostgres_RedisLossAndConcurrentRecharge(t *testing.T) {
	ctx := context.Background()
	repo, user, _ := inflightFixture(t, 1)
	cache := NewBillingCache(testRedis(t))
	require.NoError(t, cache.SetUserBalance(ctx, user.ID, 100))
	ok, err := repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 1, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	// The harness owns this isolated Redis container. Flush cannot alter the PG
	// funding obligation or permit a second owner with the same cached wallet.
	require.NoError(t, integrationRedis.FlushDB(ctx).Err())
	ok, err = repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 1, false, time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
	users := NewUserRepository(testEntClient(t), integrationDB)
	start := make(chan struct{})
	startedAt := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, 21)
	admissions := make(chan bool, 20)
	wg.Add(1)
	go func() { defer wg.Done(); <-start; errs <- users.UpdateBalance(ctx, user.ID, 10) }()
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 1, false, time.Minute)
			errs <- err
			admissions <- ok
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(admissions)
	for err := range errs {
		require.NoError(t, err)
	}
	n := 0
	for ok := range admissions {
		if ok {
			n++
		}
	}
	require.LessOrEqual(t, n, 10, "concurrent recharge may be visible before/after admission, never counted twice")
	u, err := users.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 11, u.Balance, 1e-8)
	require.InDelta(t, float64(n+1), inflightHeld(t, user.ID), 1e-8)
	t.Logf("20 concurrent admission calls with one recharge: admitted=%d held=%.2f authoritative_wallet=%.2f elapsed=%s", n, inflightHeld(t, user.ID), u.Balance, time.Since(startedAt))
}

func TestBillingInflightPostgres_RefundConcurrentAdmissionAndLateActual(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo, user, key := inflightFixture(t, 0)
	group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString()})
	today := service.TodayEastDayNumber()
	d, w, m := 1.0, 10.0, 20.0
	card := mustCreateSubscription(t, client, &service.UserSubscription{UserID: user.ID, GroupID: group.ID, DailyAmountUSD: 1, TodayRemaining: 1, TodayDay: today, StartDay: today, ExpireDay: today + 12, ExpiresAt: service.ExpireDayToExpiresAt(today + 12), Status: service.SubscriptionStatusActive, DailyLimitUSD: &d, WeeklyLimitUSD: &w, MonthlyLimitUSD: &m})
	owner := uuid.NewString()
	ok, err := repo.ReserveBillingInflight(ctx, user.ID, owner, 1, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	order := createCompletedSubscriptionRefundOrderForIntegration(t, client, user, group.ID, 300, 30, "")
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)
	start := make(chan struct{})
	refundDone, reserveDone := make(chan error, 1), make(chan error, 1)
	go func() {
		<-start
		result, err := paymentSvc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 120, GatewayAmount: 120, Reason: "inflight regression", DeductionType: payment.DeductionTypeSubscription, SubscriptionID: card.ID, SubDaysToDeduct: 12, SubDaysToRestore: 13, SubExpireDayToRestore: today + 12, SubTodayRemainingToRestore: 1, SubTodayDayToRestore: today})
		if err == nil && !result.Success {
			err = fmt.Errorf("refund failed")
		}
		refundDone <- err
	}()
	go func() {
		<-start
		ok, err := repo.ReserveBillingInflight(ctx, user.ID, uuid.NewString(), 1, false, time.Minute)
		if err == nil && ok {
			err = fmt.Errorf("another owner was admitted using held/refunded card funds")
		}
		reserveDone <- err
	}()
	close(start)
	require.NoError(t, <-refundDone)
	require.NoError(t, <-reserveDone)
	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusExpired, got.Status)
	cmd := &service.UsageBillingCommand{UserID: user.ID, APIKeyID: key.ID, RequestID: uuid.NewString(), OfficialCost: 0.5, RateMultiplier: 1}
	cmd.InflightObligationID, err = repo.StageBillingInflight(ctx, user.ID, owner, owner+":initial", cmd, time.Minute)
	require.NoError(t, err)
	require.NoError(t, repo.FinishBillingInflightAttempt(ctx, user.ID, owner, owner+":initial"))
	result, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Nil(t, result.SubscriptionID, "refund preserves old actual settlement: expired card cannot cover paid work")
	require.InDelta(t, -0.5, *result.NewBalance, 1e-8)
	require.Zero(t, inflightHeld(t, user.ID))
}
