//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type subscriptionOrderTestLoadBalancer struct{}

func (subscriptionOrderTestLoadBalancer) GetInstanceConfig(context.Context, int64) (map[string]string, error) {
	return map[string]string{}, nil
}

func (subscriptionOrderTestLoadBalancer) SelectInstance(_ context.Context, providerKey string, paymentType payment.PaymentType, _ payment.Strategy, _ float64) (*payment.InstanceSelection, error) {
	return &payment.InstanceSelection{
		InstanceID:     "integration-test-instance",
		ProviderKey:    firstNonEmpty(providerKey, payment.TypeAlipay),
		SupportedTypes: string(paymentType),
		PaymentMode:    "redirect",
	}, nil
}

func makeSubscriptionService(t *testing.T) *service.SubscriptionService {
	t.Helper()
	client := testEntClient(t)
	return service.NewSubscriptionService(
		NewGroupRepository(client, integrationDB),
		NewUserSubscriptionRepository(client),
		nil,
		nil,
		nil,
		client,
		nil,
		nil,
	)
}

func makePaymentServiceForSubscriptionIntegration(t *testing.T) *service.PaymentService {
	t.Helper()
	client := testEntClient(t)
	groupRepo := NewGroupRepository(client, integrationDB)
	subscriptionSvc := service.NewSubscriptionService(
		groupRepo,
		NewUserSubscriptionRepository(client),
		nil,
		nil,
		nil,
		client,
		nil,
		nil,
	)
	return service.NewPaymentService(
		client,
		nil,
		nil,
		nil,
		subscriptionSvc,
		service.NewPaymentConfigService(client, nil, nil),
		nil,
		groupRepo,
		nil,
	)
}

func makeCreateOrderPaymentServiceForSubscriptionIntegration(t *testing.T) *service.PaymentService {
	t.Helper()
	client := testEntClient(t)
	groupRepo := NewGroupRepository(client, integrationDB)
	settingRepo := NewSettingRepository(client)
	require.NoError(t, settingRepo.Set(context.Background(), service.SettingPaymentEnabled, "true"))
	configSvc := service.NewPaymentConfigService(client, settingRepo, nil)
	subscriptionSvc := service.NewSubscriptionService(
		groupRepo,
		NewUserSubscriptionRepository(client),
		nil,
		nil,
		nil,
		client,
		nil,
		nil,
	)
	return service.NewPaymentService(
		client,
		nil,
		subscriptionOrderTestLoadBalancer{},
		nil,
		subscriptionSvc,
		configSvc,
		NewUserRepository(client, integrationDB),
		groupRepo,
		nil,
	)
}

func countUserSubscriptionsByStatus(t *testing.T, userID int64, status string) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM user_subscriptions WHERE user_id = $1 AND status = $2 AND deleted_at IS NULL`,
		userID, status).Scan(&count))
	return count
}

func createPaidSubscriptionOrderForIntegration(t *testing.T, client *dbent.Client, user *service.User, groupID int64, dailyAmount float64, days int) int64 {
	t.Helper()
	now := time.Now()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(545).
		SetPayAmount(545).
		SetFeeRate(0).
		SetRechargeCode("PAY-SINGLE-" + uuid.NewString()).
		SetOutTradeNo("single_card_" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("provider-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(1).
		SetSubscriptionGroupID(groupID).
		SetSubscriptionDays(days).
		SetProviderSnapshot(map[string]any{
			"subscription": map[string]any{
				"daily_amount_usd": dailyAmount,
				"validity_days":    float64(days),
				"unit_price":       1.0,
				"price":            545.0,
				"formula_version":  1,
				"currency":         payment.DefaultPaymentCurrency,
			},
		}).
		SetStatus(service.OrderStatusPaid).
		SetPaidAt(now).
		SetExpiresAt(now.Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(context.Background())
	require.NoError(t, err)
	return order.ID
}

func createCompletedSubscriptionRefundOrderForIntegration(t *testing.T, client *dbent.Client, user *service.User, groupID int64, amount float64, days int, tradeNo string) *dbent.PaymentOrder {
	t.Helper()
	now := time.Now()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(amount).
		SetPayAmount(amount).
		SetFeeRate(0).
		SetRechargeCode("PAY-REFUND-" + uuid.NewString()).
		SetOutTradeNo("single_card_refund_" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo(tradeNo).
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(1).
		SetSubscriptionGroupID(groupID).
		SetSubscriptionDays(days).
		SetProviderSnapshot(map[string]any{
			"subscription": map[string]any{
				"daily_amount_usd": 10.0,
				"validity_days":    float64(days),
				"unit_price":       1.0,
				"price":            amount,
				"formula_version":  1,
				"currency":         payment.DefaultPaymentCurrency,
			},
		}).
		SetStatus(service.OrderStatusCompleted).
		SetPaidAt(now).
		SetCompletedAt(now).
		SetExpiresAt(now.Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(context.Background())
	require.NoError(t, err)
	return order
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func TestSubscriptionServiceAssignOrExtend_BlocksWhenActiveCardExistsPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("single-card-block-%s@example.com", uuid.NewString()),
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "single-card-block-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	existing := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        group.ID,
		DailyAmountUSD: 10,
		TodayRemaining: 10,
		TodayDay:       today,
		StartDay:       today,
		ExpireDay:      today + 10,
		ExpiresAt:      service.ExpireDayToExpiresAt(today + 10),
		Status:         service.SubscriptionStatusActive,
	})

	_, _, err := svc.AssignOrExtendSubscription(ctx, &service.AssignSubscriptionInput{
		UserID:         user.ID,
		GroupID:        group.ID,
		ValidityDays:   30,
		DailyAmountUSD: 20,
		Notes:          "second purchase",
	})
	require.Error(t, err)
	require.Equal(t, "ACTIVE_SUBSCRIPTION_EXISTS", infraerrors.Reason(err))
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))

	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, existing.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)
	require.InDelta(t, 10, got.TodayRemaining, 1e-9)
}

func TestSubscriptionServiceAssignOrExtend_ExpiresStaleActiveBeforeCreatePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("single-card-stale-%s@example.com", uuid.NewString()),
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "single-card-stale-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	stale := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        group.ID,
		DailyAmountUSD: 10,
		TodayRemaining: 7,
		TodayDay:       today - 1,
		StartDay:       today - 10,
		ExpireDay:      today - 1,
		ExpiresAt:      service.ExpireDayToExpiresAt(today - 1),
		Status:         service.SubscriptionStatusActive,
	})

	created, reused, err := svc.AssignOrExtendSubscription(ctx, &service.AssignSubscriptionInput{
		UserID:         user.ID,
		GroupID:        group.ID,
		ValidityDays:   30,
		DailyAmountUSD: 20,
		Notes:          "new after stale",
	})
	require.NoError(t, err)
	require.False(t, reused)
	require.NotNil(t, created)
	require.NotEqual(t, stale.ID, created.ID)
	require.NotNil(t, created.DailyLimitUSD)
	require.NotNil(t, created.WeeklyLimitUSD)
	require.NotNil(t, created.MonthlyLimitUSD)
	require.InDelta(t, 20, *created.DailyLimitUSD, 1e-9)
	require.InDelta(t, 140, *created.WeeklyLimitUSD, 1e-9)
	require.InDelta(t, 600, *created.MonthlyLimitUSD, 1e-9)
	require.InDelta(t, 0, created.DailyUsageUSD, 1e-9)
	require.InDelta(t, 0, created.WeeklyUsageUSD, 1e-9)
	require.InDelta(t, 0, created.MonthlyUsageUSD, 1e-9)
	require.NotNil(t, created.DailyWindowStart)
	require.NotNil(t, created.WeeklyWindowStart)
	require.NotNil(t, created.MonthlyWindowStart)
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusExpired))

	gotStale, err := NewUserSubscriptionRepository(client).GetByID(ctx, stale.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusExpired, gotStale.Status)
	require.InDelta(t, 0, gotStale.TodayRemaining, 1e-9)
}

func TestPaymentSubscriptionFulfillment_CreatesThreeWindowCardAndBillsPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)
	billingRepo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("single-card-fulfillment-window-%s@example.com", uuid.NewString()),
		Balance: 1000,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     "single-card-fulfillment-window-" + uuid.NewString(),
		Platform: service.PlatformAnthropic,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-single-card-fulfillment-window-" + uuid.NewString(),
		Name:    "single-card-fulfillment-window",
	})
	orderID := createPaidSubscriptionOrderForIntegration(t, client, user, group.ID, 12, 45)

	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID))
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))

	sub, err := NewUserSubscriptionRepository(client).GetActiveByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.NotNil(t, sub.DailyLimitUSD)
	require.NotNil(t, sub.WeeklyLimitUSD)
	require.NotNil(t, sub.MonthlyLimitUSD)
	require.InDelta(t, 12, *sub.DailyLimitUSD, 1e-9)
	require.InDelta(t, 84, *sub.WeeklyLimitUSD, 1e-9)
	require.InDelta(t, 360, *sub.MonthlyLimitUSD, 1e-9)
	require.InDelta(t, 0, sub.DailyUsageUSD, 1e-9)
	require.InDelta(t, 0, sub.WeeklyUsageUSD, 1e-9)
	require.InDelta(t, 0, sub.MonthlyUsageUSD, 1e-9)
	require.NotNil(t, sub.DailyWindowStart)
	require.NotNil(t, sub.WeeklyWindowStart)
	require.NotNil(t, sub.MonthlyWindowStart)

	result, err := billingRepo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:      uuid.NewString(),
		APIKeyID:       apiKey.ID,
		UserID:         user.ID,
		SubscriptionID: &sub.ID,
		OfficialCost:   5,
		RateMultiplier: 2,
	})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.NotNil(t, result.SubscriptionID)
	require.Equal(t, sub.ID, *result.SubscriptionID)
	require.NotNil(t, result.WalletDebit)
	require.InDelta(t, 0, *result.WalletDebit, 1e-9, "履约新卡必须带三窗口限额，首笔请求应由订阅覆盖")

	gotSub, err := NewUserSubscriptionRepository(client).GetByID(ctx, sub.ID)
	require.NoError(t, err)
	require.InDelta(t, 5, gotSub.DailyUsageUSD, 1e-9)
	require.InDelta(t, 5, gotSub.WeeklyUsageUSD, 1e-9)
	require.InDelta(t, 5, gotSub.MonthlyUsageUSD, 1e-9)
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 1000, gotUser.Balance, 1e-9)
}

func TestPaymentSubscriptionFulfillment_CustomOrderCreatesNoGroupThreeWindowCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("custom-sub-fulfillment-%s@example.com", uuid.NewString()),
		Balance: 1000,
	})

	const dailyAmount = 7.5
	const validityDays = 30
	quote, err := service.DefaultSubscriptionPricingConfig().Quote(dailyAmount, validityDays)
	require.NoError(t, err)
	weeklyLimit, monthlyLimit := service.DeriveWindowCaps(dailyAmount, validityDays)

	now := time.Now()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(quote.Price).
		SetPayAmount(quote.Price).
		SetFeeRate(0).
		SetRechargeCode("PAY-CUSTOM-" + uuid.NewString()).
		SetOutTradeNo("custom_sub_" + uuid.NewString()).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("provider-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).
		SetSubscriptionDays(validityDays).
		SetProviderSnapshot(map[string]any{
			"subscription": map[string]any{
				"daily_amount_usd": dailyAmount,
				"validity_days":    float64(validityDays),
				"unit_price":       quote.UnitPrice,
				"price":            quote.Price,
				"formula_version":  service.SubscriptionFormulaVersion,
				"currency":         payment.DefaultPaymentCurrency,
			},
		}).
		SetStatus(service.OrderStatusPaid).
		SetPaidAt(now).
		SetExpiresAt(now.Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	require.Nil(t, order.SubscriptionGroupID, "自定义订阅订单不应写 subscription_group_id")
	require.Nil(t, order.PlanID, "自定义订阅订单不应写 plan_id")

	require.NoError(t, paymentSvc.ExecuteSubscriptionFulfillment(ctx, order.ID))

	gotOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, gotOrder.Status)

	sub, err := NewUserSubscriptionRepository(client).GetActiveByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, sub.GroupID, "无 group 自定义卡在 domain 层映射为 group_id=0")
	require.NotNil(t, sub.DailyLimitUSD)
	require.NotNil(t, sub.WeeklyLimitUSD)
	require.NotNil(t, sub.MonthlyLimitUSD)
	require.InDelta(t, dailyAmount, *sub.DailyLimitUSD, 1e-9)
	require.InDelta(t, weeklyLimit, *sub.WeeklyLimitUSD, 1e-9)
	require.InDelta(t, monthlyLimit, *sub.MonthlyLimitUSD, 1e-9)
	require.InDelta(t, 0, sub.DailyUsageUSD, 1e-9)
	require.InDelta(t, 0, sub.WeeklyUsageUSD, 1e-9)
	require.InDelta(t, 0, sub.MonthlyUsageUSD, 1e-9)
	require.NotNil(t, sub.DailyWindowStart)
	require.NotNil(t, sub.WeeklyWindowStart)
	require.NotNil(t, sub.MonthlyWindowStart)
	require.Equal(t, service.SubscriptionStatusActive, sub.Status)

	var rawGroupID *int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT group_id FROM user_subscriptions WHERE id = $1`, sub.ID).Scan(&rawGroupID))
	require.Nil(t, rawGroupID, "DB 层 user_subscriptions.group_id 必须为 NULL")

	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 1000, gotUser.Balance, 1e-9, "自定义订阅履约只建卡，不给钱包充值")
}

func TestSubscriptionServiceAssignOrExtend_ConcurrentCreatesSingleActiveCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("single-card-concurrent-%s@example.com", uuid.NewString()),
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "single-card-concurrent-" + uuid.NewString()})

	const attempts = 8
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, err := svc.AssignOrExtendSubscription(ctx, &service.AssignSubscriptionInput{
				UserID:         user.ID,
				GroupID:        group.ID,
				ValidityDays:   30,
				DailyAmountUSD: 10 + float64(i),
				Notes:          fmt.Sprintf("concurrent-%d", i),
			})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	var success, conflicts int
	for err := range errs {
		if err == nil {
			success++
			continue
		}
		require.Equal(t, "ACTIVE_SUBSCRIPTION_EXISTS", infraerrors.Reason(err))
		conflicts++
	}
	require.Equal(t, 1, success)
	require.Equal(t, attempts-1, conflicts)
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))

	var activeCount int
	var minD, maxD float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*), MIN(daily_amount_usd), MAX(daily_amount_usd)
		FROM user_subscriptions
		WHERE user_id = $1 AND status = $2 AND deleted_at IS NULL
	`, user.ID, service.SubscriptionStatusActive).Scan(&activeCount, &minD, &maxD))
	require.Equal(t, 1, activeCount)
	require.GreaterOrEqual(t, minD, 10.0)
	require.LessOrEqual(t, maxD, 17.0)
}

func TestPaymentSubscriptionFulfillment_DoesNotCreateSecondActiveCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("single-card-payment-%s@example.com", uuid.NewString()),
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "single-card-payment-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	existing := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        group.ID,
		DailyAmountUSD: 10,
		TodayRemaining: 10,
		TodayDay:       today,
		StartDay:       today,
		ExpireDay:      today + 10,
		ExpiresAt:      service.ExpireDayToExpiresAt(today + 10),
		Status:         service.SubscriptionStatusActive,
	})
	orderID := createPaidSubscriptionOrderForIntegration(t, client, user, group.ID, 20, 30)

	err := paymentSvc.ExecuteSubscriptionFulfillment(ctx, orderID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ACTIVE_SUBSCRIPTION_EXISTS")
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))

	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, existing.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)

	order, err := client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusFailed, order.Status)
}

func TestPaymentCreateOrder_BlocksExistingActiveCardBeforeProviderPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makeCreateOrderPaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("single-card-create-order-%s@example.com", uuid.NewString()),
		Username: "single-card-create-order",
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "single-card-create-order-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	_ = mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        group.ID,
		DailyAmountUSD: 10,
		TodayRemaining: 10,
		TodayDay:       today,
		DailySpentDay:  today,
		StartDay:       today,
		ExpireDay:      today + 10,
		ExpiresAt:      service.ExpireDayToExpiresAt(today + 10),
		Status:         service.SubscriptionStatusActive,
	})
	plan, err := client.SubscriptionPlan.Create().
		SetGroupID(group.ID).
		SetName("single-card-create-order-plan-" + uuid.NewString()).
		SetDailyAmountUsd(20).
		SetPrice(545).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetForSale(true).
		Save(ctx)
	require.NoError(t, err)

	_, err = paymentSvc.CreateOrder(ctx, service.CreateOrderRequest{
		UserID:      user.ID,
		PaymentType: payment.TypeAlipay,
		OrderType:   payment.OrderTypeSubscription,
		PlanID:      plan.ID,
		ClientIP:    "127.0.0.1",
		SrcHost:     "api.example.com",
	})
	require.Error(t, err)
	require.Equal(t, "ACTIVE_SUBSCRIPTION_EXISTS", infraerrors.Reason(err))

	var orderCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM payment_orders WHERE user_id = $1`, user.ID).Scan(&orderCount))
	require.Equal(t, 0, orderCount, "持卡用户新购应在 provider 调用前失败，不能产生待支付订单")
}

func TestPaymentRefundSubscription_ClosesCardWithoutDeletingPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("single-card-refund-close-%s@example.com", uuid.NewString()),
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "single-card-refund-close-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        group.ID,
		DailyAmountUSD: 10,
		TodayRemaining: 6,
		TodayDay:       today,
		DailySpentDay:  today,
		StartDay:       today - 5,
		ExpireDay:      today + 12,
		ExpiresAt:      service.ExpireDayToExpiresAt(today + 12),
		Status:         service.SubscriptionStatusActive,
	})
	order := createCompletedSubscriptionRefundOrderForIntegration(t, client, user, group.ID, 300, 30, "")

	result, err := paymentSvc.ExecuteRefund(ctx, &service.RefundPlan{
		OrderID:                    order.ID,
		Order:                      order,
		RefundAmount:               120,
		GatewayAmount:              120,
		Reason:                     "subscription refund",
		DeductionType:              payment.DeductionTypeSubscription,
		SubscriptionID:             sub.ID,
		SubDaysToDeduct:            12,
		SubDaysToRestore:           13,
		SubExpireDayToRestore:      today + 12,
		SubTodayRemainingToRestore: 6,
		SubTodayDayToRestore:       today,
	})
	require.NoError(t, err)
	require.True(t, result.Success)

	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, sub.ID)
	require.NoError(t, err, "退款关闭必须保留订阅行")
	require.Equal(t, service.SubscriptionStatusExpired, got.Status)
	require.Equal(t, today-1, got.ExpireDay)
	require.InDelta(t, 0, got.TodayRemaining, 1e-9)

	reloadedOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusPartiallyRefunded, reloadedOrder.Status)
}

func TestPaymentRefundSubscription_GatewayFailureRestoresCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	paymentSvc := makePaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("single-card-refund-rollback-%s@example.com", uuid.NewString()),
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "single-card-refund-rollback-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	originalExpireDay := today + 15
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:         user.ID,
		GroupID:        group.ID,
		DailyAmountUSD: 10,
		TodayRemaining: 4.5,
		TodayDay:       today,
		DailySpentDay:  today,
		StartDay:       today - 3,
		ExpireDay:      originalExpireDay,
		ExpiresAt:      service.ExpireDayToExpiresAt(originalExpireDay),
		Status:         service.SubscriptionStatusActive,
	})
	order := createCompletedSubscriptionRefundOrderForIntegration(t, client, user, group.ID, 300, 30, "provider-trade-"+uuid.NewString())

	result, err := paymentSvc.ExecuteRefund(ctx, &service.RefundPlan{
		OrderID:                    order.ID,
		Order:                      order,
		RefundAmount:               150,
		GatewayAmount:              150,
		Reason:                     "subscription refund",
		DeductionType:              payment.DeductionTypeSubscription,
		SubscriptionID:             sub.ID,
		SubDaysToDeduct:            15,
		SubDaysToRestore:           16,
		SubExpireDayToRestore:      originalExpireDay,
		SubTodayRemainingToRestore: 4.5,
		SubTodayDayToRestore:       today,
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Warning, "rolled back")

	got, err := NewUserSubscriptionRepository(client).GetByID(ctx, sub.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)
	require.Equal(t, originalExpireDay, got.ExpireDay)
	require.True(t, got.ExpiresAt.Equal(service.ExpireDayToExpiresAt(originalExpireDay)))
	require.InDelta(t, 4.5, got.TodayRemaining, 1e-9)
	require.Equal(t, today, got.TodayDay)

	reloadedOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusCompleted, reloadedOrder.Status)
}
