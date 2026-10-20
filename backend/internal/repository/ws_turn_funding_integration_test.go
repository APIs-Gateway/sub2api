//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func wsTurnFundingCard(t *testing.T, f *wsInflightFixture, limit float64) int64 {
	t.Helper()
	now := time.Now()
	day, week, month := timezone.StartOfDay(now), timezone.StartOfWeek(now), timezone.StartOfMonth(now)
	card := mustCreateSubscription(t, inflightTestEntClient(t), &service.UserSubscription{
		UserID: f.userID, GroupID: f.groupID, Status: service.SubscriptionStatusActive,
		ExpiresAt: now.Add(24 * time.Hour), DailyLimitUSD: &limit, WeeklyLimitUSD: &limit, MonthlyLimitUSD: &limit,
		DailyWindowStart: &day, WeeklyWindowStart: &week, MonthlyWindowStart: &month,
	})
	return card.ID
}

// If OLD forwards the forbidden second turn, release the actual provider so the
// failure records a real response.completed instead of a test-created timeout.
func wsTurnFundingReadDenied(t *testing.T, f *wsInflightFixture, conn *coderws.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type received struct {
		body []byte
		err  error
	}
	result := make(chan received, 1)
	go func() {
		_, body, err := conn.Read(ctx)
		result <- received{body: body, err: err}
	}()
	for {
		select {
		case unexpected := <-f.provider.turns:
			close(unexpected.release)
		case event := <-result:
			require.NoError(t, event.err, "the gateway must send the actual billing refusal envelope")
			require.Equal(t, "error", gjson.GetBytes(event.body, "type").String(), "actual downstream wire: %s", event.body)
			_, _, err := conn.Read(ctx)
			require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err), "actual connection close: %v", err)
			return
		case <-ctx.Done():
			t.Fatal("neither a gateway refusal nor a real provider completion was received")
		}
	}
}

func wsTurnFundingCount(t *testing.T, f *wsInflightFixture, table string) int {
	t.Helper()
	var count int
	query := "SELECT COUNT(*) FROM usage_logs WHERE user_id=$1"
	if table == "dedup" {
		query = "SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1"
		require.NoError(t, inflightTestDB(t).QueryRow(query, f.key.ID).Scan(&count))
		return count
	}
	require.NoError(t, inflightTestDB(t).QueryRow(query, f.userID).Scan(&count))
	return count
}

func wsTurnFundingCommand(t *testing.T, f *wsInflightFixture, price, rate float64) service.UsageBillingCommand {
	t.Helper()
	select {
	case command := <-f.billingRepo.commands:
		require.Equal(t, f.userID, command.UserID)
		require.Equal(t, f.key.ID, command.APIKeyID)
		require.NotEmpty(t, command.RequestID)
		require.InDelta(t, price, command.OfficialCost, 1e-9)
		require.InDelta(t, rate, command.RateMultiplier, 1e-9)
		return command
	case <-time.After(5 * time.Second):
		t.Fatal("the actual settled turn did not emit a billing command")
		return service.UsageBillingCommand{}
	}
}

func wsTurnFundingCardUsage(t *testing.T, cardID int64) [3]float64 {
	t.Helper()
	var usage [3]float64
	require.NoError(t, inflightTestDB(t).QueryRow("SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE id=$1", cardID).Scan(&usage[0], &usage[1], &usage[2]))
	return usage
}

func wsTurnFundingSlotsReleased(t *testing.T, f *wsInflightFixture) {
	t.Helper()
	cache := NewConcurrencyCache(f.rdb, 15, 30)
	require.Eventually(t, func() bool {
		user, userErr := cache.GetUserConcurrency(context.Background(), f.userID)
		account, accountErr := cache.GetAccountConcurrency(context.Background(), f.accountID)
		return userErr == nil && accountErr == nil && user == 0 && account == 0 && f.held(t) == 0
	}, 5*time.Second, 20*time.Millisecond, "actual Redis user/account slots and PostgreSQL holds must be released")
}

func TestWSTurnFundingWS_ActualSettlementThenAdmission(t *testing.T) {
	cases := []string{
		"negative_wallet_paid", "zero_wallet_paid", "negative_wallet_free", "zero_wallet_free",
		"card_exhausted", "card_expired", "positive_wallet_paid", "negative_wallet_card_added",
		"negative_wallet_card_remaining", "user_disabled", "key_deleted", "key_expired", "key_quota_exhausted",
		"changed_group_quote", "key_group_moved_connection_group_kept", "simple_mode",
	}
	for _, mode := range []string{"native", "passthrough", "bridge"} {
		for _, name := range cases {
			t.Run(mode+"/hold_off/"+name, func(t *testing.T) {
				wsTurnFundingScenario(t, mode, name, false)
			})
		}
		for _, name := range []string{"negative_wallet_paid", "negative_wallet_free"} {
			t.Run(mode+"/hold_on/"+name, func(t *testing.T) {
				wsTurnFundingScenario(t, mode, name, true)
			})
		}
	}
}

func wsTurnFundingScenario(t *testing.T, mode, name string, hold bool) {
	t.Helper()
	firstPrice := .5
	switch name {
	case "negative_wallet_paid", "negative_wallet_free", "negative_wallet_card_added", "card_exhausted":
		firstPrice = 1
	case "zero_wallet_paid", "zero_wallet_free":
		firstPrice = .75
	}
	f := newWSInflightFixture(t, mode, service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": firstPrice, "gpt-5.1": 0})
	// Configure the existing deployment policy before either HTTP or WS starts.
	f.cfg.Billing.InflightReservation.Enabled = hold
	ctx := context.Background()
	exec := func(query string, args ...any) {
		_, err := inflightTestDB(t).ExecContext(ctx, query, args...)
		require.NoError(t, err)
	}
	var cardID int64
	switch name {
	case "card_exhausted":
		exec("UPDATE users SET balance=0 WHERE id=$1", f.userID)
		cardID = wsTurnFundingCard(t, f, .75)
	case "card_expired":
		exec("UPDATE users SET balance=0 WHERE id=$1", f.userID)
		cardID = wsTurnFundingCard(t, f, 2)
	case "negative_wallet_card_remaining":
		exec("UPDATE users SET balance=-0.25 WHERE id=$1", f.userID)
		cardID = wsTurnFundingCard(t, f, 2)
	case "simple_mode":
		f.cfg.RunMode = config.RunModeSimple
		exec("UPDATE users SET balance=-0.25 WHERE id=$1", f.userID)
	}
	conn := f.dial(t)
	wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"synthetic first turn","max_output_tokens":8}`)
	first := f.provider.next(t)
	close(first.release)
	wsInflightReadCompleted(t, conn)
	f.waitUsage(t, 1)
	firstBalance := f.wallet(t)
	var firstCommand service.UsageBillingCommand
	var firstCardUsage [3]float64
	require.EqualValues(t, 1, f.provider.calls.Load())
	if name == "simple_mode" {
		require.InDelta(t, -.25, firstBalance, 1e-9)
		require.Zero(t, wsTurnFundingCount(t, f, "dedup"))
	} else {
		require.Equal(t, 1, wsTurnFundingCount(t, f, "dedup"), "first actual settlement must commit before the next client turn")
		require.EqualValues(t, 1, f.billingRepo.calls.Load())
		firstCommand = wsTurnFundingCommand(t, f, firstPrice, 1)
		var firstActualCost float64
		require.NoError(t, inflightTestDB(t).QueryRow("SELECT actual_cost FROM usage_logs WHERE user_id=$1", f.userID).Scan(&firstActualCost))
		require.InDelta(t, firstPrice, firstActualCost, 1e-9)
		if cardID != 0 {
			firstCardUsage = wsTurnFundingCardUsage(t, cardID)
			expectedCard := firstPrice
			if name == "card_exhausted" {
				expectedCard = .75
			}
			for _, amount := range firstCardUsage {
				require.InDelta(t, expectedCard, amount, 1e-9)
			}
		}
		if name == "negative_wallet_paid" || name == "negative_wallet_free" || name == "negative_wallet_card_added" || name == "card_exhausted" {
			require.InDelta(t, -.25, firstBalance, 1e-9, "first real settlement, not a simulated negative balance")
		}
		if name == "zero_wallet_paid" || name == "zero_wallet_free" || name == "card_expired" {
			require.InDelta(t, 0, firstBalance, 1e-9)
		}
	}
	t.Logf("ACTUAL first settlement observed: mode=%s hold=%t case=%s wallet=%v logs=%d dedup=%d provider=%d", mode, hold, name, firstBalance, wsTurnFundingCount(t, f, "usage"), wsTurnFundingCount(t, f, "dedup"), f.provider.calls.Load())
	// A deliberately positive real Redis balance must not override PostgreSQL.
	balanceCache := NewBillingCache(f.rdb)
	require.NoError(t, balanceCache.SetUserBalance(ctx, f.userID, 100))
	cached, err := balanceCache.GetUserBalance(ctx, f.userID)
	require.NoError(t, err)
	require.Greater(t, cached, 0.0)
	allow := false
	expectedFinal := firstBalance
	switch name {
	case "positive_wallet_paid":
		allow, expectedFinal = true, firstBalance-firstPrice
	case "negative_wallet_card_added":
		cardID = wsTurnFundingCard(t, f, 2)
		allow = true
	case "negative_wallet_card_remaining":
		allow = true
	case "card_expired":
		exec("UPDATE user_subscriptions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", cardID)
	case "user_disabled":
		exec("UPDATE users SET status='disabled' WHERE id=$1", f.userID)
	case "key_deleted":
		require.NoError(t, NewAPIKeyRepository(inflightTestEntClient(t), inflightTestDB(t)).DeleteWithAudit(ctx, f.key.ID))
	case "key_expired":
		exec("UPDATE api_keys SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", f.key.ID)
	case "key_quota_exhausted":
		exec("UPDATE api_keys SET quota=1, quota_used=1 WHERE id=$1", f.key.ID)
	case "changed_group_quote":
		exec("UPDATE groups SET rate_multiplier=2 WHERE id=$1", f.groupID)
		f.apiKeys.InvalidateAuthCacheByKey(ctx, f.key.Key)
		allow, expectedFinal = true, firstBalance-firstPrice*2
	case "key_group_moved_connection_group_kept":
		other := mustCreateGroup(t, inflightTestEntClient(t), &service.Group{Name: uuid.NewString(), Platform: service.PlatformOpenAI, RateMultiplier: 7})
		exec("UPDATE api_keys SET group_id=$1 WHERE id=$2", other.ID, f.key.ID)
		f.apiKeys.InvalidateAuthCacheByKey(ctx, f.key.Key)
		allow, expectedFinal = true, firstBalance-firstPrice
	case "simple_mode":
		allow = true
	}
	secondModel := "gpt-5.4"
	if name == "negative_wallet_free" || name == "zero_wallet_free" {
		secondModel = "gpt-5.1"
	}
	wsInflightWrite(t, conn, fmt.Sprintf(`{"type":"response.create","model":%q,"input":"synthetic subsequent turn","max_output_tokens":8}`, secondModel))
	if !allow {
		wsTurnFundingReadDenied(t, f, conn)
		require.EqualValues(t, 1, f.provider.calls.Load(), "denied next turn must never reach the actual upstream")
		require.EqualValues(t, 1, f.billingRepo.calls.Load())
		require.Equal(t, 1, wsTurnFundingCount(t, f, "usage"))
		require.Equal(t, 1, wsTurnFundingCount(t, f, "dedup"))
		require.InDelta(t, firstBalance, f.wallet(t), 1e-9)
		if cardID != 0 {
			require.Equal(t, firstCardUsage, wsTurnFundingCardUsage(t, cardID), "denied turn cannot consume any card window")
		}
		require.Empty(t, f.billingRepo.commands, "denied turn cannot enqueue another billing command")
		wsTurnFundingSlotsReleased(t, f)
		return
	}
	second := f.provider.next(t)
	close(second.release)
	wsInflightReadCompleted(t, conn)
	f.waitUsage(t, 2)
	require.EqualValues(t, 2, f.provider.calls.Load())
	require.InDelta(t, expectedFinal, f.wallet(t), 1e-9, "existing card, wallet and per-turn quote settlement remains unchanged")
	if name == "simple_mode" {
		require.Zero(t, wsTurnFundingCount(t, f, "dedup"))
		require.Zero(t, f.billingRepo.calls.Load())
	} else {
		require.Equal(t, 2, wsTurnFundingCount(t, f, "dedup"))
		require.EqualValues(t, 2, f.billingRepo.calls.Load())
		rate := 1.0
		if name == "changed_group_quote" {
			rate = 2
		}
		secondCommand := wsTurnFundingCommand(t, f, firstPrice, rate)
		require.NotEqual(t, firstCommand.RequestID, secondCommand.RequestID, "distinct real turns must retain distinct billing identities")
	}
	if name == "negative_wallet_card_added" || name == "negative_wallet_card_remaining" {
		usage := wsTurnFundingCardUsage(t, cardID)
		expectedCard := firstPrice
		if name == "negative_wallet_card_remaining" {
			expectedCard = firstPrice * 2
		}
		for _, amount := range usage {
			require.InDelta(t, expectedCard, amount, 1e-9)
		}
	}
	if name == "changed_group_quote" {
		var rate float64
		require.NoError(t, inflightTestDB(t).QueryRow("SELECT rate_multiplier FROM usage_logs WHERE user_id=$1 ORDER BY id DESC LIMIT 1", f.userID).Scan(&rate))
		require.InDelta(t, 2, rate, 1e-9)
	}
	require.NoError(t, conn.Close(coderws.StatusNormalClosure, "positive control complete"))
	wsTurnFundingSlotsReleased(t, f)
}
