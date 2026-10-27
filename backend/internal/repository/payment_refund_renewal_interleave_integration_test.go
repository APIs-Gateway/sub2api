//go:build integration

package repository

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type refundRenewalInterleaveLoadBalancer struct {
	payment.LoadBalancer
	onLookup func()
}

func (b refundRenewalInterleaveLoadBalancer) GetInstanceConfig(context.Context, int64) (map[string]string, error) {
	b.onLookup()
	return nil, fmt.Errorf("controlled provider construction failure after deduction")
}

// Unlike the private service transaction adapter, this uses the actual
// UserSubscriptionRepository and GrantSubscriptionDays SQL implementation.
// Failure is injected at provider configuration lookup, never a real payment.
func TestRefundSubscriptionActualRepo_PaidRenewalSurvivesProviderFailure(t *testing.T) {
	ctx := context.Background()
	c := testEntClient(t)
	u := mustCreateUser(t, c, &service.User{Email: uuid.NewString() + "@example.com", Username: "refund renewal"})
	g := mustCreateGroup(t, c, &service.Group{Name: uuid.NewString()})
	today := service.TodayEastDayNumber()
	d := 10.0
	w, m := service.DeriveWindowCaps(d, 30)
	card := mustCreateSubscription(t, c, &service.UserSubscription{UserID: u.ID, GroupID: g.ID, Status: service.SubscriptionStatusActive, StartDay: today - 10, ExpireDay: today + 40, ExpiresAt: service.ExpireDayToExpiresAt(today + 40), DailyAmountUSD: d, DailyLimitUSD: &d, WeeklyLimitUSD: &w, MonthlyLimitUSD: &m, TodayRemaining: 4.5, TodayDay: today})
	inst, err := c.PaymentProviderInstance.Create().SetProviderKey(payment.TypeStripe).SetName(uuid.NewString()).SetConfig("{}").SetSupportedTypes("stripe").SetEnabled(true).SetRefundEnabled(true).Save(ctx)
	require.NoError(t, err)
	o, err := c.PaymentOrder.Create().SetUserID(u.ID).SetUserEmail(u.Email).SetUserName(u.Username).SetAmount(10).SetPayAmount(10).SetFeeRate(0).SetRechargeCode(uuid.NewString()).SetOutTradeNo(uuid.NewString()).SetPaymentType(payment.TypeStripe).SetPaymentTradeNo("old-renewal").SetOrderType(payment.OrderTypeSubscription).SetStatus(service.OrderStatusCompleted).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("api.example.com").SetProviderInstanceID(strconv.FormatInt(inst.ID, 10)).SetSubscriptionDays(30).SetProviderSnapshot(map[string]any{"subscription": map[string]any{"daily_amount_usd": d, "validity_days": 30.0, "intent": service.SubscriptionIntentRenew, "target_subscription_id": card.ID}}).Save(ctx)
	require.NoError(t, err)
	groupRepo := NewGroupRepository(c, integrationDB)
	subSvc := service.NewSubscriptionService(groupRepo, NewUserSubscriptionRepository(c), NewUserRepository(c, integrationDB), nil, nil, c, nil, nil)
	called := 0
	lb := refundRenewalInterleaveLoadBalancer{onLookup: func() {
		called++
		before, err := NewUserSubscriptionRepository(c).GetByID(ctx, card.ID)
		require.NoError(t, err)
		require.Equal(t, today+10, before.ExpireDay, "refund actual deduction must commit before provider lookup")
		_, err = subSvc.ApplyRenewFromOrder(ctx, card.ID, 30)
		require.NoError(t, err)
	}}
	s := service.NewPaymentService(c, nil, lb, nil, subSvc, service.NewPaymentConfigService(c, nil, nil), NewUserRepository(c, integrationDB), groupRepo, nil)
	p, early, err := s.PrepareRefund(ctx, o.ID, 10, "old renewal refund", false, false)
	require.NoError(t, err)
	require.Nil(t, early)
	result, err := s.ExecuteRefund(ctx, p)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, 1, called)
	got, err := NewUserSubscriptionRepository(c).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+70, got.ExpireDay)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)
	require.InDelta(t, 4.5, got.TodayRemaining, 1e-9)
	user, err := NewUserRepository(c, integrationDB).GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Zero(t, user.Balance)
}
