//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbgroup "github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const subscriptionFulfillmentLeaseWindow = 5 * time.Minute

type subscriptionFulfillmentRecoveryFixtures struct {
	client          *dbent.Client
	orderIDs        []int64
	userIDs         []int64
	subscriptionIDs []int64
	groupIDs        []int64
}

func newSubscriptionFulfillmentRecoveryFixtures(t *testing.T, client *dbent.Client) *subscriptionFulfillmentRecoveryFixtures {
	t.Helper()
	fixtures := &subscriptionFulfillmentRecoveryFixtures{client: client}
	t.Cleanup(func() { fixtures.cleanup(t) })
	return fixtures
}

func (f *subscriptionFulfillmentRecoveryFixtures) cleanup(t *testing.T) {
	t.Helper()
	ctx := mixins.SkipSoftDelete(context.Background())
	if len(f.orderIDs) > 0 {
		orderIDs := make([]string, 0, len(f.orderIDs))
		for _, id := range f.orderIDs {
			orderIDs = append(orderIDs, fmt.Sprint(id))
		}
		_, err := f.client.PaymentAuditLog.Delete().Where(paymentauditlog.OrderIDIn(orderIDs...)).Exec(ctx)
		require.NoError(t, err)
		_, err = f.client.PaymentOrder.Delete().Where(paymentorder.IDIn(f.orderIDs...)).Exec(ctx)
		require.NoError(t, err)
	}
	if len(f.subscriptionIDs) > 0 {
		_, err := f.client.UserSubscription.Delete().Where(usersubscription.IDIn(f.subscriptionIDs...)).Exec(ctx)
		require.NoError(t, err)
	}
	if len(f.groupIDs) > 0 {
		_, err := f.client.Group.Delete().Where(dbgroup.IDIn(f.groupIDs...)).Exec(ctx)
		require.NoError(t, err)
	}
	if len(f.userIDs) > 0 {
		_, err := f.client.User.Delete().Where(dbuser.IDIn(f.userIDs...)).Exec(ctx)
		require.NoError(t, err)
	}
}

func TestSubscriptionFulfillment_RecoversLegacyAssignedCardWithoutDuplicatePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	fixtures := newSubscriptionFulfillmentRecoveryFixtures(t, client)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)
	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("subscription-lease-legacy-%s@example.com", uuid.NewString()),
	})
	fixtures.userIDs = append(fixtures.userIDs, user.ID)

	order := createSubscriptionRecoveryOrder(t, client, user, service.OrderStatusRecharging, staleSubscriptionFulfillmentTime())
	fixtures.orderIDs = append(fixtures.orderIDs, order.ID)
	dailyAmount := 18.0
	weeklyLimit, monthlyLimit := service.DeriveWindowCaps(dailyAmount, 30)
	today := service.TodayEastDayNumber()
	expireDay := today + 29
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         0,
		DailyAmountUSD:  dailyAmount,
		DailyLimitUSD:   &dailyAmount,
		WeeklyLimitUSD:  &weeklyLimit,
		MonthlyLimitUSD: &monthlyLimit,
		TodayRemaining:  dailyAmount,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       expireDay,
		ExpiresAt:       service.ExpireDayToExpiresAt(expireDay),
		Status:          service.SubscriptionStatusActive,
		Notes:           fmt.Sprintf("manual note\npayment order %d\nretained note", order.ID),
	})
	fixtures.subscriptionIDs = append(fixtures.subscriptionIDs, card.ID)

	require.Zero(t, paymentAuditActionCount(t, client, order.ID, "SUBSCRIPTION_SUCCESS"))
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, order.ID))
	requireSubscriptionRecoveryCompleted(t, client, order.ID, user.ID, card.ID, expireDay)

	// Replaying the same stale order must use the durable success audit and leave
	// the recovered entitlement untouched.
	_, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetStatus(service.OrderStatusRecharging).
		SetUpdatedAt(staleSubscriptionFulfillmentTime()).
		ClearCompletedAt().
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, order.ID))
	requireSubscriptionRecoveryCompleted(t, client, order.ID, user.ID, card.ID, expireDay)
}

func TestSubscriptionFulfillment_StaleRenewReplayDoesNotExtendAgainPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	fixtures := newSubscriptionFulfillmentRecoveryFixtures(t, client)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)
	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("subscription-lease-renew-%s@example.com", uuid.NewString()),
	})
	fixtures.userIDs = append(fixtures.userIDs, user.ID)
	group := mustCreateGroup(t, client, &service.Group{Name: "subscription-lease-renew-" + uuid.NewString()})
	fixtures.groupIDs = append(fixtures.groupIDs, group.ID)

	today := service.TodayEastDayNumber()
	dailyAmount := 12.0
	weeklyLimit, monthlyLimit := service.DeriveWindowCaps(dailyAmount, 30)
	originalExpireDay := today + 3
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  dailyAmount,
		DailyLimitUSD:   &dailyAmount,
		WeeklyLimitUSD:  &weeklyLimit,
		MonthlyLimitUSD: &monthlyLimit,
		TodayRemaining:  dailyAmount,
		TodayDay:        today,
		StartDay:        today - 2,
		ExpireDay:       originalExpireDay,
		ExpiresAt:       service.ExpireDayToExpiresAt(originalExpireDay),
		Status:          service.SubscriptionStatusActive,
	})
	fixtures.subscriptionIDs = append(fixtures.subscriptionIDs, card.ID)

	orderID := createPaidLifecycleOrderForIntegration(t, client, user, service.SubscriptionIntentRenew, card.ID, dailyAmount, 30, 120)
	fixtures.orderIDs = append(fixtures.orderIDs, orderID)
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))
	afterFirst, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, originalExpireDay+30, afterFirst.ExpireDay)

	_, err = client.PaymentOrder.UpdateOneID(orderID).
		SetStatus(service.OrderStatusRecharging).
		SetUpdatedAt(staleSubscriptionFulfillmentTime()).
		ClearCompletedAt().
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, paymentSvc.RetryFulfillment(ctx, orderID))

	afterReplay, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, afterFirst.ExpireDay, afterReplay.ExpireDay, "stale replay must not extend the card twice")
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))
	require.Equal(t, 1, paymentAuditActionCount(t, client, orderID, "SUBSCRIPTION_SUCCESS"))

	// A normal success-state replay remains a no-op.
	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))
	afterCompletedReplay, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, afterFirst.ExpireDay, afterCompletedReplay.ExpireDay)
}

func TestSubscriptionFulfillment_FreshRechargingRejectsRecoveryPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	fixtures := newSubscriptionFulfillmentRecoveryFixtures(t, client)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)
	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("subscription-lease-fresh-%s@example.com", uuid.NewString()),
	})
	fixtures.userIDs = append(fixtures.userIDs, user.ID)
	order := createSubscriptionRecoveryOrder(t, client, user, service.OrderStatusRecharging, time.Now().UTC())
	fixtures.orderIDs = append(fixtures.orderIDs, order.ID)

	err := paymentSvc.ExecuteSubscriptionFulfillment(ctx, order.ID)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
	err = paymentSvc.RetryFulfillment(ctx, order.ID)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusRecharging, reloaded.Status)
	require.Zero(t, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))
	require.Zero(t, paymentAuditActionCount(t, client, order.ID, "SUBSCRIPTION_SUCCESS"))
}

func TestSubscriptionFulfillment_DoesNotRecoverMismatchedCustomCardNotePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	fixtures := newSubscriptionFulfillmentRecoveryFixtures(t, client)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)
	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("subscription-lease-mismatch-%s@example.com", uuid.NewString()),
	})
	fixtures.userIDs = append(fixtures.userIDs, user.ID)
	order := createSubscriptionRecoveryOrder(t, client, user, service.OrderStatusRecharging, staleSubscriptionFulfillmentTime())
	fixtures.orderIDs = append(fixtures.orderIDs, order.ID)

	wrongDaily := 17.0
	wrongWeekly, wrongMonthly := service.DeriveWindowCaps(wrongDaily, 30)
	today := service.TodayEastDayNumber()
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         0,
		DailyAmountUSD:  wrongDaily,
		DailyLimitUSD:   &wrongDaily,
		WeeklyLimitUSD:  &wrongWeekly,
		MonthlyLimitUSD: &wrongMonthly,
		TodayRemaining:  wrongDaily,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 29,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 29),
		Status:          service.SubscriptionStatusActive,
		Notes:           fmt.Sprintf("payment order %d", order.ID),
	})
	fixtures.subscriptionIDs = append(fixtures.subscriptionIDs, card.ID)

	err := paymentSvc.ExecuteSubscriptionFulfillment(ctx, order.ID)
	require.ErrorContains(t, err, "does not match payment order")
	reloaded, getErr := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, getErr)
	require.Equal(t, service.OrderStatusFailed, reloaded.Status)
	require.Zero(t, paymentAuditActionCount(t, client, order.ID, "SUBSCRIPTION_SUCCESS"))
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))
	snapshot, ok := reloaded.ProviderSnapshot["subscription"].(map[string]any)
	require.True(t, ok)
	require.Nil(t, snapshot["subscription_id"])
}

func createSubscriptionRecoveryOrder(
	t *testing.T,
	client *dbent.Client,
	user *service.User,
	status string,
	updatedAt time.Time,
) *dbent.PaymentOrder {
	t.Helper()
	now := time.Now()
	weeklyLimit, monthlyLimit := service.DeriveWindowCaps(18, 30)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(90).
		SetPayAmount(90).
		SetFeeRate(0).
		SetRechargeCode("PAY-SUB-LEASE-" + uuid.NewString()).
		SetOutTradeNo("subscription_lease_" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("provider-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).
		SetSubscriptionDays(30).
		SetProviderSnapshot(map[string]any{
			"subscription": map[string]any{
				"daily_amount_usd":  18.0,
				"validity_days":     30.0,
				"weekly_limit_usd":  weeklyLimit,
				"monthly_limit_usd": monthlyLimit,
				"formula_version":   service.SubscriptionFormulaVersion,
				"currency":          payment.DefaultPaymentCurrency,
			},
		}).
		SetStatus(status).
		SetPaidAt(now.Add(-time.Hour)).
		SetExpiresAt(now.Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetUpdatedAt(updatedAt).
		Save(context.Background())
	require.NoError(t, err)
	return order
}

func staleSubscriptionFulfillmentTime() time.Time {
	return time.Now().UTC().Add(-subscriptionFulfillmentLeaseWindow - time.Minute)
}

func requireSubscriptionRecoveryCompleted(
	t *testing.T,
	client *dbent.Client,
	orderID int64,
	userID int64,
	subscriptionID int64,
	expireDay int,
) {
	t.Helper()
	ctx := context.Background()
	order, err := client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, order.Status)
	require.Equal(t, 1, paymentAuditActionCount(t, client, orderID, "SUBSCRIPTION_SUCCESS"))
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, userID, service.SubscriptionStatusActive))

	sub, err := NewUserSubscriptionRepository(client).GetByID(ctx, subscriptionID)
	require.NoError(t, err)
	require.Equal(t, expireDay, sub.ExpireDay)

	snapshot, ok := order.ProviderSnapshot["subscription"].(map[string]any)
	require.True(t, ok)
	require.NotNil(t, snapshot["subscription_id"], "recovered card id must be frozen back into the order snapshot")
}
