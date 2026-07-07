//go:build unit

package service

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestValidateRefundRequestRejectsLegacyGuessedProviderInstance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-legacy@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-legacy-user").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-refund-instance").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetAllowUserRefund(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("REFUND-LEGACY-ORDER").
		SetOutTradeNo("sub2_refund_legacy_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-legacy-refund").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient: client,
	}

	_, err = svc.validateRefundRequest(ctx, order.ID, user.ID)
	require.Error(t, err)
	require.Equal(t, "USER_REFUND_DISABLED", infraerrors.Reason(err))
}

func TestPrepareRefundRejectsLegacyGuessedProviderInstance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-legacy-admin@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-legacy-admin-user").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-refund-admin-instance").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetAllowUserRefund(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(188).
		SetPayAmount(188).
		SetFeeRate(0).
		SetRechargeCode("REFUND-LEGACY-ADMIN-ORDER").
		SetOutTradeNo("sub2_refund_legacy_admin_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-legacy-admin-refund").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient: client,
	}

	plan, result, err := svc.PrepareRefund(ctx, order.ID, 0, "", false, false)
	require.Nil(t, plan)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "REFUND_DISABLED", infraerrors.Reason(err))
}

func TestGwRefundRejectsAlipayMerchantIdentitySnapshotMismatch(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-snapshot-mismatch@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-snapshot-mismatch-user").
		Save(ctx)
	require.NoError(t, err)

	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-refund-mismatch-instance").
		SetConfig(encryptWebhookProviderConfig(t, map[string]string{
			"appId":      "runtime-alipay-app",
			"privateKey": "runtime-private-key",
		})).
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	instID := strconv.FormatInt(inst.ID, 10)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("REFUND-SNAPSHOT-MISMATCH-ORDER").
		SetOutTradeNo("sub2_refund_snapshot_mismatch_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-refund-snapshot-mismatch").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instID).
		SetProviderKey(payment.TypeAlipay).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": instID,
			"provider_key":         payment.TypeAlipay,
			"merchant_app_id":      "expected-alipay-app",
		}).
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient:    client,
		loadBalancer: newWebhookProviderTestLoadBalancer(client),
	}

	_, err = svc.gwRefund(ctx, &RefundPlan{
		OrderID:       order.ID,
		Order:         order,
		RefundAmount:  order.Amount,
		GatewayAmount: order.Amount,
		Reason:        "snapshot mismatch",
	})
	require.ErrorContains(t, err, "alipay app_id mismatch")
}

type refundUserSubRepoStub struct {
	userSubRepoNoop
	active            *UserSubscription
	byID              map[int64]*UserSubscription
	closeDeleteRow    *bool
	closeSubscription int64
	grantDays         []int
	updatedStatuses   []string
}

func newRefundUserSubRepoStub(active *UserSubscription) *refundUserSubRepoStub {
	byID := make(map[int64]*UserSubscription)
	if active != nil {
		cp := *active
		byID[cp.ID] = &cp
		active = &cp
	}
	return &refundUserSubRepoStub{active: active, byID: byID}
}

func (r *refundUserSubRepoStub) GetActiveByUserID(_ context.Context, userID int64) (*UserSubscription, error) {
	if r.active == nil || r.active.UserID != userID {
		return nil, ErrSubscriptionNotFound
	}
	cp := *r.active
	return &cp, nil
}

func (r *refundUserSubRepoStub) GetLatestActiveStatusByUserID(ctx context.Context, userID int64) (*UserSubscription, error) {
	return r.GetActiveByUserID(ctx, userID)
}

func (r *refundUserSubRepoStub) GetLatestActiveStatusForUpdate(ctx context.Context, userID int64) (*UserSubscription, error) {
	return r.GetActiveByUserID(ctx, userID)
}

func (r *refundUserSubRepoStub) ApplyManualOverdraft(ctx context.Context, sub *UserSubscription) error {
	return nil
}

func (r *refundUserSubRepoStub) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	sub := r.byID[id]
	if sub == nil {
		return nil, ErrSubscriptionNotFound
	}
	cp := *sub
	return &cp, nil
}

func (r *refundUserSubRepoStub) CloseSubscriptionWithReclaim(_ context.Context, subID int64, _ time.Time, deleteRow bool) (int64, float64, error) {
	sub := r.byID[subID]
	if sub == nil {
		return 0, 0, nil
	}
	r.closeSubscription = subID
	r.closeDeleteRow = &deleteRow
	cp := *sub
	cp.Status = SubscriptionStatusExpired
	cp.TodayRemaining = 0
	r.byID[subID] = &cp
	r.active = nil
	return sub.UserID, 0, nil
}

func (r *refundUserSubRepoStub) GrantSubscriptionDays(_ context.Context, subID int64, addDays int, _ time.Time, now time.Time) (int64, float64, error) {
	sub := r.byID[subID]
	if sub == nil {
		return 0, 0, ErrSubscriptionNotFound
	}
	r.grantDays = append(r.grantDays, addDays)
	cp := *sub
	today := EastDayNumber(now)
	base := cp.ExpireDay
	if base < today-1 {
		base = today - 1
	}
	cp.ExpireDay = ClampExpireDay(base + addDays)
	cp.ExpiresAt = ExpireDayToExpiresAt(cp.ExpireDay)
	r.byID[subID] = &cp
	return cp.UserID, 0, nil
}

func (r *refundUserSubRepoStub) UpdateStatus(_ context.Context, subID int64, status string) error {
	sub := r.byID[subID]
	if sub == nil {
		return ErrSubscriptionNotFound
	}
	r.updatedStatuses = append(r.updatedStatuses, status)
	cp := *sub
	cp.Status = status
	r.byID[subID] = &cp
	if status == SubscriptionStatusActive {
		r.active = &cp
	}
	return nil
}

func (r *refundUserSubRepoStub) Update(_ context.Context, sub *UserSubscription) error {
	if sub == nil {
		return ErrSubscriptionNilInput
	}
	if _, ok := r.byID[sub.ID]; !ok {
		return ErrSubscriptionNotFound
	}
	cp := *sub
	r.byID[sub.ID] = &cp
	if cp.Status == SubscriptionStatusActive {
		r.active = &cp
	}
	return nil
}

// prepDeductBalanceUserRepoStub 嵌入 UserRepository 接口(仅覆盖 GetByID);prepDeduct 只调 GetByID。
type prepDeductBalanceUserRepoStub struct {
	UserRepository
	user *User
}

func (s prepDeductBalanceUserRepoStub) GetByID(ctx context.Context, id int64) (*User, error) {
	return s.user, nil
}

// P2#11:管理员充值单退款须复制用户侧「余额够才退」闸——已消费/透支的充值额不是可原路退的法币。
func TestPrepDeductBalanceRefundClampsToRecoverable(t *testing.T) {
	ctx := context.Background()
	mk := func(balance float64) *PaymentService {
		return &PaymentService{userRepo: prepDeductBalanceUserRepoStub{user: &User{ID: 1, Balance: balance}}}
	}
	o := &dbent.PaymentOrder{OrderType: payment.OrderTypeBalance, UserID: 1}

	// 余额充足:全额可追回,deduct=退款额,不要求 force。
	p := &RefundPlan{RefundAmount: 100}
	require.Nil(t, mk(200).prepDeduct(ctx, o, p, false))
	require.InDelta(t, 100, p.BalanceToDeduct, 1e-9)

	// 余额不足(充值额已花掉一部分):非 force → RequireForce,不静默原路退多。
	p2 := &RefundPlan{RefundAmount: 100}
	res2 := mk(50).prepDeduct(ctx, o, p2, false)
	require.NotNil(t, res2)
	require.True(t, res2.RequireForce)
	require.False(t, res2.Success)

	// 余额不足 + force:只从钱包扣可追回部分(50)。
	p3 := &RefundPlan{RefundAmount: 100}
	require.Nil(t, mk(50).prepDeduct(ctx, o, p3, true))
	require.InDelta(t, 50, p3.BalanceToDeduct, 1e-9)

	// 余额为负(透支花超)+ force:可追回=0,扣减夹到 0,绝不为负(根治旧 min(amt,负余额)=负数)。
	p4 := &RefundPlan{RefundAmount: 100}
	require.Nil(t, mk(-20).prepDeduct(ctx, o, p4, true))
	require.InDelta(t, 0, p4.BalanceToDeduct, 1e-9)
}

type refundBalanceUserRepoStub struct {
	UserRepository
	balance       float64
	deductCalls   int
	updateCalls   int
	lastDeduct    float64
	lastUpdateAdd float64
}

func (s *refundBalanceUserRepoStub) DeductBalance(_ context.Context, _ int64, amount float64) error {
	s.deductCalls++
	s.lastDeduct = amount
	s.balance -= amount
	return nil
}

func (s *refundBalanceUserRepoStub) UpdateBalance(_ context.Context, _ int64, amount float64) error {
	s.updateCalls++
	s.lastUpdateAdd = amount
	s.balance += amount
	return nil
}

func TestPaymentServiceRefundFeeRateUsesConfiguredValue(t *testing.T) {
	ctx := context.Background()
	svc := &PaymentService{
		configService: &PaymentConfigService{
			settingRepo: &paymentConfigSettingRepoStub{values: map[string]string{
				SettingRefundFeeRate: "1.75",
			}},
		},
	}

	require.InDelta(t, 1.75, svc.refundFeeRate(ctx), 1e-9)
	require.Zero(t, (&PaymentService{}).refundFeeRate(ctx))
}

func TestSubscriptionOrderOriginalDaysUsesSnapshotThenLegacyField(t *testing.T) {
	legacyDays := 30
	snapshotOrder := &dbent.PaymentOrder{
		SubscriptionDays: &legacyDays,
		ProviderSnapshot: map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd": 10.0,
				"validity_days":    60.0,
			},
		},
	}
	got, err := subscriptionOrderOriginalDays(snapshotOrder)
	require.NoError(t, err)
	require.Equal(t, 60, got)

	legacyOrder := &dbent.PaymentOrder{SubscriptionDays: &legacyDays}
	got, err = subscriptionOrderOriginalDays(legacyOrder)
	require.NoError(t, err)
	require.Equal(t, 30, got)

	_, err = subscriptionOrderOriginalDays(&dbent.PaymentOrder{})
	require.ErrorContains(t, err, "missing original validity days")
}

func TestSubscriptionForRefundRejectsMissingServiceAndSnapshotUserMismatch(t *testing.T) {
	ctx := context.Background()
	_, err := (&PaymentService{}).subscriptionForRefund(ctx, &dbent.PaymentOrder{UserID: 1})
	require.ErrorContains(t, err, "subscription service not configured")

	today := TodayEastDayNumber()
	repo := newRefundUserSubRepoStub(&UserSubscription{
		ID:             11,
		UserID:         2,
		Status:         SubscriptionStatusActive,
		DailyAmountUSD: 30,
		TodayRemaining: 30,
		TodayDay:       today,
		StartDay:       today,
		ExpireDay:      today + 29,
		ExpiresAt:      ExpireDayToExpiresAt(today + 29),
	})
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{subscriptionSvc: subSvc}

	_, err = svc.subscriptionForRefund(ctx, &dbent.PaymentOrder{
		UserID: 1,
		ProviderSnapshot: map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"subscription_id": 11.0,
			},
		},
	})
	require.ErrorContains(t, err, "does not belong to order user")
}

func TestRefundAttemptAuditActionIncludesPrefix(t *testing.T) {
	action := refundAttemptAuditAction("REFUND_GATEWAY_FAILED")
	require.True(t, strings.HasPrefix(action, "REFUND_GATEWAY_FAILED_"), action)
	require.Greater(t, len(action), len("REFUND_GATEWAY_FAILED_"))
}

func TestPrepareRefundSubscriptionDefaultsToPerDayRefundAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("refund-subscription@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-subscription-user").
		Save(ctx)
	require.NoError(t, err)
	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-subscription-refund").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	instID := strconv.FormatInt(inst.ID, 10)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(300).
		SetPayAmount(300).
		SetFeeRate(0).
		SetRechargeCode("REFUND-SUBSCRIPTION-ORDER").
		SetOutTradeNo("sub2_refund_subscription_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-subscription-refund").
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instID).
		SetProviderKey(payment.TypeAlipay).
		SetSubscriptionDays(30).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": instID,
			"provider_key":         payment.TypeAlipay,
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd": 10.0,
				"validity_days":    30.0,
			},
		}).
		Save(ctx)
	require.NoError(t, err)

	today := TodayEastDayNumber()
	repo := newRefundUserSubRepoStub(&UserSubscription{
		ID:             42,
		UserID:         user.ID,
		GroupID:        7,
		Status:         SubscriptionStatusActive,
		DailyAmountUSD: 10,
		TodayRemaining: 10,
		TodayDay:       today,
		StartDay:       today - 14,
		ExpireDay:      today + 15,
		ExpiresAt:      ExpireDayToExpiresAt(today + 15),
	})
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{entClient: client, subscriptionSvc: subSvc}

	plan, result, err := svc.PrepareRefund(ctx, order.ID, 0, "", false, true)
	require.NoError(t, err)
	require.Nil(t, result)
	require.NotNil(t, plan)
	require.InDelta(t, 150, plan.RefundAmount, 1e-9)
	require.InDelta(t, 150, plan.GatewayAmount, 1e-9)
	require.Equal(t, int64(42), plan.SubscriptionID)
	require.Equal(t, 15, plan.SubDaysToDeduct)
	require.Equal(t, 16, plan.SubDaysToRestore)
	require.Equal(t, today+15, plan.SubExpireDayToRestore)
	require.InDelta(t, 10, plan.SubTodayRemainingToRestore, 1e-9)
	require.Equal(t, today, plan.SubTodayDayToRestore)
}

func TestRequestRefundAllowsSubscriptionOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("request-refund-subscription@example.com").
		SetPasswordHash("hash").
		SetUsername("request-refund-subscription-user").
		Save(ctx)
	require.NoError(t, err)
	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-request-subscription-refund").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetAllowUserRefund(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	instID := strconv.FormatInt(inst.ID, 10)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(300).
		SetPayAmount(300).
		SetFeeRate(0).
		SetRechargeCode("REQUEST-REFUND-SUBSCRIPTION").
		SetOutTradeNo("sub2_request_refund_subscription").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-request-subscription-refund").
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instID).
		SetProviderKey(payment.TypeAlipay).
		SetSubscriptionDays(30).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": instID,
			"provider_key":         payment.TypeAlipay,
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd": 10.0,
				"validity_days":    30.0,
			},
		}).
		Save(ctx)
	require.NoError(t, err)

	today := TodayEastDayNumber()
	repo := newRefundUserSubRepoStub(&UserSubscription{
		ID:             43,
		UserID:         user.ID,
		GroupID:        7,
		Status:         SubscriptionStatusActive,
		DailyAmountUSD: 10,
		TodayRemaining: 10,
		TodayDay:       today,
		StartDay:       today - 14,
		ExpireDay:      today + 15,
		ExpiresAt:      ExpireDayToExpiresAt(today + 15),
	})
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{entClient: client, subscriptionSvc: subSvc}

	require.NoError(t, svc.RequestRefund(ctx, order.ID, user.ID, "subscription refund request"))

	updated, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundRequested, updated.Status)
	require.InDelta(t, 150, updated.RefundAmount, 1e-9)
	require.NotNil(t, updated.RefundRequestReason)
	require.Equal(t, "subscription refund request", *updated.RefundRequestReason)
}

func TestPrepareRefundSubscriptionUsesSnapshotSubscriptionID(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("refund-subscription-id@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-subscription-id-user").
		Save(ctx)
	require.NoError(t, err)
	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-subscription-refund-id").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	instID := strconv.FormatInt(inst.ID, 10)
	today := TodayEastDayNumber()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(300).
		SetPayAmount(300).
		SetFeeRate(0).
		SetRechargeCode("REFUND-SUBSCRIPTION-ID").
		SetOutTradeNo("sub2_refund_subscription_id").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-subscription-refund-id").
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instID).
		SetProviderKey(payment.TypeAlipay).
		SetSubscriptionDays(30).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": instID,
			"provider_key":         payment.TypeAlipay,
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd": 10.0,
				"validity_days":    30.0,
				"subscription_id":  11.0,
			},
		}).
		Save(ctx)
	require.NoError(t, err)

	repo := newRefundUserSubRepoStub(&UserSubscription{
		ID:             22,
		UserID:         user.ID,
		GroupID:        8,
		Status:         SubscriptionStatusActive,
		DailyAmountUSD: 20,
		TodayRemaining: 20,
		TodayDay:       today,
		StartDay:       today,
		ExpireDay:      today + 20,
		ExpiresAt:      ExpireDayToExpiresAt(today + 20),
	})
	repo.byID[11] = &UserSubscription{
		ID:             11,
		UserID:         user.ID,
		GroupID:        7,
		Status:         SubscriptionStatusActive,
		DailyAmountUSD: 10,
		TodayRemaining: 4,
		TodayDay:       today,
		StartDay:       today - 20,
		ExpireDay:      today + 10,
		ExpiresAt:      ExpireDayToExpiresAt(today + 10),
	}
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{entClient: client, subscriptionSvc: subSvc}

	plan, result, err := svc.PrepareRefund(ctx, order.ID, 0, "", false, true)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, int64(11), plan.SubscriptionID)
	require.InDelta(t, 100, plan.RefundAmount, 1e-9)
	require.Equal(t, today+10, plan.SubExpireDayToRestore)
	require.InDelta(t, 4, plan.SubTodayRemainingToRestore, 1e-9)
}

func TestExecuteRefundSubscriptionClosesCardWithoutDeleting(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("refund-close-subscription@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-close-subscription-user").
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(300).
		SetPayAmount(300).
		SetFeeRate(0).
		SetRechargeCode("REFUND-SUBSCRIPTION-CLOSE").
		SetOutTradeNo("sub2_refund_subscription_close").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	today := TodayEastDayNumber()
	repo := newRefundUserSubRepoStub(&UserSubscription{
		ID:        77,
		UserID:    user.ID,
		GroupID:   8,
		Status:    SubscriptionStatusActive,
		ExpireDay: today + 10,
		ExpiresAt: ExpireDayToExpiresAt(today + 10),
	})
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{entClient: client, subscriptionSvc: subSvc}

	result, err := svc.ExecuteRefund(ctx, &RefundPlan{
		OrderID:          order.ID,
		Order:            order,
		RefundAmount:     300,
		GatewayAmount:    300,
		Reason:           "subscription refund",
		DeductionType:    payment.DeductionTypeSubscription,
		SubscriptionID:   77,
		SubDaysToDeduct:  10,
		SubDaysToRestore: 11,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.NotNil(t, repo.closeDeleteRow)
	require.False(t, *repo.closeDeleteRow, "退款关闭卡必须保留行，便于网关失败回滚")
	require.Equal(t, int64(77), repo.closeSubscription)
}

func TestRollbackRefundSubscriptionRestoresClosedCard(t *testing.T) {
	ctx := context.Background()
	today := TodayEastDayNumber()
	repo := newRefundUserSubRepoStub(&UserSubscription{
		ID:        88,
		UserID:    99,
		GroupID:   8,
		Status:    SubscriptionStatusExpired,
		ExpireDay: today - 1,
		ExpiresAt: ExpireDayToExpiresAt(today - 1),
	})
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{subscriptionSvc: subSvc}

	ok := svc.RollbackRefund(ctx, &RefundPlan{
		OrderID:                    123,
		Order:                      &dbent.PaymentOrder{UserID: 99},
		DeductionType:              payment.DeductionTypeSubscription,
		SubscriptionID:             88,
		SubDaysToDeduct:            15,
		SubDaysToRestore:           16,
		SubExpireDayToRestore:      today + 15,
		SubTodayRemainingToRestore: 4.5,
		SubTodayDayToRestore:       today,
	}, assertErr("gateway failed"))
	require.True(t, ok)
	require.Empty(t, repo.grantDays)
	require.Empty(t, repo.updatedStatuses)
	require.Equal(t, SubscriptionStatusActive, repo.byID[88].Status)
	require.Equal(t, today+15, repo.byID[88].ExpireDay)
	require.InDelta(t, 4.5, repo.byID[88].TodayRemaining, 1e-9)
	require.Equal(t, today, repo.byID[88].TodayDay)
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

type refundQueryTestProvider struct {
	refundStatus string
	queryStatus  string
	refundID     string
}

func (p *refundQueryTestProvider) Name() string { return "refund-query-test-provider" }

func (p *refundQueryTestProvider) ProviderKey() string { return payment.TypeAlipay }

func (p *refundQueryTestProvider) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeAlipay}
}

func (p *refundQueryTestProvider) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	panic("unexpected CreatePayment call")
}

func (p *refundQueryTestProvider) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	panic("unexpected QueryOrder call")
}

func (p *refundQueryTestProvider) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	panic("unexpected VerifyNotification call")
}

func (p *refundQueryTestProvider) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	status := p.refundStatus
	if status == "" {
		status = payment.ProviderStatusSuccess
	}
	refundID := p.refundID
	if refundID == "" {
		refundID = "refund-test-id"
	}
	return &payment.RefundResponse{RefundID: refundID, Status: status}, nil
}

func (p *refundQueryTestProvider) QueryRefund(context.Context, string) (*payment.RefundResponse, error) {
	status := p.queryStatus
	if status == "" {
		status = payment.ProviderStatusPending
	}
	refundID := p.refundID
	if refundID == "" {
		refundID = "refund-test-id"
	}
	return &payment.RefundResponse{RefundID: refundID, Status: status}, nil
}

func TestExecuteRefundMarksProviderPendingAsRefundPending(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("refund-pending@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-pending-user").
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-PENDING").
		SetOutTradeNo("sub2_refund_pending").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-refund-pending").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient:              client,
		refundProviderOverride: &refundQueryTestProvider{refundStatus: payment.ProviderStatusPending, refundID: "refund-pending-id"},
	}

	result, err := svc.ExecuteRefund(ctx, &RefundPlan{
		OrderID:       order.ID,
		Order:         order,
		RefundAmount:  100,
		GatewayAmount: 100,
		Reason:        "pending refund",
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "refund pending", result.Message)

	updated, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, updated.Status)
	require.Nil(t, updated.RefundAt)
}

func TestQueryAndFinalizeRefundOnlyFinalizesProviderSuccess(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("refund-query@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-query-user").
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-QUERY").
		SetOutTradeNo("sub2_refund_query").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-refund-query").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusRefundPending).
		SetRefundAmount(100).
		SetRefundReason("query refund").
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient:              client,
		refundProviderOverride: &refundQueryTestProvider{queryStatus: payment.ProviderStatusPending, refundID: "refund-query-id"},
	}
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID, "refund-query-id")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "refund pending", result.Message)
	updated, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, updated.Status)

	svc.refundProviderOverride = &refundQueryTestProvider{queryStatus: payment.ProviderStatusSuccess, refundID: "refund-query-id"}
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID, "refund-query-id")
	require.NoError(t, err)
	require.True(t, result.Success)
	updated, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, updated.Status)
	require.NotNil(t, updated.RefundAt)
}

func TestQueryAndFinalizeRefundRollsBackPendingLocalDeductionOnProviderFailure(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("refund-query-failed@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-query-failed-user").
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-QUERY-FAILED").
		SetOutTradeNo("sub2_refund_query_failed").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-refund-query-failed").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	userRepo := &refundBalanceUserRepoStub{balance: 200}
	svc := &PaymentService{
		entClient:              client,
		userRepo:               userRepo,
		refundProviderOverride: &refundQueryTestProvider{refundStatus: payment.ProviderStatusPending, queryStatus: payment.ProviderStatusFailed, refundID: "refund-query-failed-id"},
	}

	result, err := svc.ExecuteRefund(ctx, &RefundPlan{
		OrderID:         order.ID,
		Order:           order,
		RefundAmount:    100,
		GatewayAmount:   100,
		Reason:          "pending refund then failed",
		DeductionType:   payment.DeductionTypeBalance,
		BalanceToDeduct: 80,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "refund pending", result.Message)
	require.Equal(t, 1, userRepo.deductCalls)
	require.InDelta(t, 80, userRepo.lastDeduct, 1e-9)
	require.InDelta(t, 120, userRepo.balance, 1e-9)

	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID, "refund-query-failed-id")
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, 1, userRepo.updateCalls)
	require.InDelta(t, 80, userRepo.lastUpdateAdd, 1e-9)
	require.InDelta(t, 200, userRepo.balance, 1e-9)

	updated, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, updated.Status)
}

func TestCalculateGatewayRefundAmountUsesCurrencyPrecision(t *testing.T) {
	require.InDelta(t, 6.173, calculateGatewayRefundAmount(100, 12.345, 50, "KWD"), 1e-12)
	require.InDelta(t, 12.345, calculateGatewayRefundAmount(100, 12.345, 100, "KWD"), 1e-12)
	require.InDelta(t, 52, calculateGatewayRefundAmount(100, 103, 50, "JPY"), 1e-12)
}

func TestCalculateGatewayRefundBreakdownAppliesRefundFee(t *testing.T) {
	base, fee, gateway := calculateGatewayRefundBreakdown(100, 100, 100, 0, "CNY")
	require.InDelta(t, 100, base, 1e-12)
	require.InDelta(t, 0, fee, 1e-12)
	require.InDelta(t, 100, gateway, 1e-12)

	base, fee, gateway = calculateGatewayRefundBreakdown(60, 118.78, 30, 1, "CNY")
	require.InDelta(t, 59.39, base, 1e-12)
	require.InDelta(t, 0.60, fee, 1e-12)
	require.InDelta(t, 58.79, gateway, 1e-12)

	base, fee, gateway = calculateGatewayRefundBreakdown(100, 100, 100, 100, "CNY")
	require.InDelta(t, 100, base, 1e-12)
	require.InDelta(t, 100, fee, 1e-12)
	require.InDelta(t, 0, gateway, 1e-12)
}

func TestCalculateGatewayPaymentAmountForCreditedValueUsesMultiplierAndCurrencyPrecision(t *testing.T) {
	require.InDelta(t, 59.99, calculateGatewayPaymentAmountForCreditedValue(118.78, 1.98, "CNY"), 1e-12)
	require.InDelta(t, 60, calculateGatewayPaymentAmountForCreditedValue(118.78, 1.98, "JPY"), 1e-12)
	require.InDelta(t, 118.78, calculateGatewayPaymentAmountForCreditedValue(118.78, 0, "CNY"), 1e-12)
}

func TestFormatGatewayRefundAmountUsesOrderCurrency(t *testing.T) {
	order := &dbent.PaymentOrder{
		ProviderSnapshot: map[string]any{
			"currency": "KWD",
		},
	}

	require.Equal(t, "12.345", formatGatewayRefundAmount(12.345, order))
}

func TestValidateRefundProviderResponseAcceptsPending(t *testing.T) {
	require.NoError(t, validateRefundProviderResponse(&payment.RefundResponse{Status: payment.ProviderStatusPending}))
	require.NoError(t, validateRefundProviderResponse(&payment.RefundResponse{Status: payment.ProviderStatusSuccess}))
	require.Error(t, validateRefundProviderResponse(&payment.RefundResponse{Status: payment.ProviderStatusFailed}))
	require.Error(t, validateRefundProviderResponse(nil))
}
