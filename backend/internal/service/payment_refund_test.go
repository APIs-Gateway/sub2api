//go:build unit

package service

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
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
	shortenDays       []int
	grantDays         []int
	updatedStatuses   []string
	getErr            error
	shortenErr        error
	updateStatusErr   error
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
	if r.getErr != nil {
		return nil, r.getErr
	}
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

func (r *refundUserSubRepoStub) ShortenSubscriptionWithReclaim(_ context.Context, subID int64, reduceDays int, _ time.Time, now time.Time) (int64, float64, error) {
	if r.shortenErr != nil {
		return 0, 0, r.shortenErr
	}
	sub := r.byID[subID]
	if sub == nil {
		return 0, 0, ErrSubscriptionNotFound
	}
	r.shortenDays = append(r.shortenDays, reduceDays)
	cp := *sub
	newExpireDay := cp.ExpireDay - reduceDays
	if floor := EastDayNumber(now) - 1; newExpireDay < floor {
		newExpireDay = floor
	}
	cp.ExpireDay = newExpireDay
	cp.ExpiresAt = ExpireDayToExpiresAt(newExpireDay)
	r.byID[subID] = &cp
	if cp.Status == SubscriptionStatusActive {
		r.active = &cp
	}
	return cp.UserID, 0, nil
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
	if r.updateStatusErr != nil {
		return r.updateStatusErr
	}
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

func TestReadRefundSubscriptionIDPrefersRenewTarget(t *testing.T) {
	order := &dbent.PaymentOrder{
		ProviderSnapshot: map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"intent":                 SubscriptionIntentRenew,
				"target_subscription_id": 22.0,
				"subscription_id":        33.0,
			},
		},
	}
	got, ok := readRefundSubscriptionID(order)
	require.True(t, ok)
	require.Equal(t, int64(22), got)

	purchase := &dbent.PaymentOrder{
		ProviderSnapshot: map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"intent":          SubscriptionIntentPurchase,
				"subscription_id": 44.0,
			},
		},
	}
	got, ok = readRefundSubscriptionID(purchase)
	require.True(t, ok)
	require.Equal(t, int64(44), got)
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

func TestPrepareRefundRenewUsesTargetSubscriptionAndRenewDays(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("refund-renew-target@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-renew-target-user").
		Save(ctx)
	require.NoError(t, err)
	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-renew-refund-target").
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
		SetAmount(600).
		SetPayAmount(600).
		SetFeeRate(0).
		SetRechargeCode("REFUND-RENEW-TARGET").
		SetOutTradeNo("sub2_refund_renew_target").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-renew-target").
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instID).
		SetProviderKey(payment.TypeAlipay).
		SetSubscriptionDays(60).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": instID,
			"provider_key":         payment.TypeAlipay,
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd":       10.0,
				"validity_days":          60.0,
				"intent":                 SubscriptionIntentRenew,
				"target_subscription_id": 11.0,
				"subscription_id":        99.0,
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
		ExpireDay:      today + 25,
		ExpiresAt:      ExpireDayToExpiresAt(today + 25),
	})
	repo.byID[11] = &UserSubscription{
		ID:             11,
		UserID:         user.ID,
		GroupID:        7,
		Status:         SubscriptionStatusActive,
		DailyAmountUSD: 10,
		TodayRemaining: 4,
		TodayDay:       today,
		StartDay:       today - 5,
		ExpireDay:      today + 75,
		ExpiresAt:      ExpireDayToExpiresAt(today + 75),
	}
	repo.byID[99] = &UserSubscription{
		ID:             99,
		UserID:         user.ID,
		GroupID:        9,
		Status:         SubscriptionStatusActive,
		DailyAmountUSD: 30,
		TodayRemaining: 30,
		TodayDay:       today,
		StartDay:       today,
		ExpireDay:      today + 99,
		ExpiresAt:      ExpireDayToExpiresAt(today + 99),
	}
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{entClient: client, subscriptionSvc: subSvc}

	plan, result, err := svc.PrepareRefund(ctx, order.ID, 0, "", false, false)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, int64(11), plan.SubscriptionID)
	require.Equal(t, 60, plan.SubDaysToDeduct)
	require.Equal(t, 60, plan.SubDaysToRestore)
	require.Equal(t, today+75, plan.SubExpireDayToRestore)
}

func TestPrepDeductRenewRequiresOriginalDaysUnlessForced(t *testing.T) {
	ctx := context.Background()
	today := TodayEastDayNumber()
	repo := newRefundUserSubRepoStub(&UserSubscription{
		ID:             11,
		UserID:         513,
		GroupID:        7,
		Status:         SubscriptionStatusActive,
		DailyAmountUSD: 10,
		TodayRemaining: 4,
		TodayDay:       today,
		StartDay:       today - 5,
		ExpireDay:      today + 75,
		ExpiresAt:      ExpireDayToExpiresAt(today + 75),
	})
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{subscriptionSvc: subSvc}
	order := &dbent.PaymentOrder{
		UserID:    513,
		OrderType: payment.OrderTypeSubscription,
		ProviderSnapshot: map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd":       10.0,
				"intent":                 SubscriptionIntentRenew,
				"target_subscription_id": 11.0,
			},
		},
	}

	plan := &RefundPlan{RefundAmount: 600}
	result := svc.prepDeduct(ctx, order, plan, false)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.True(t, result.RequireForce)
	require.Contains(t, result.Warning, "invalid subscription snapshot")

	forcedPlan := &RefundPlan{RefundAmount: 600}
	result = svc.prepDeduct(ctx, order, forcedPlan, true)
	require.Nil(t, result)
	require.Equal(t, int64(11), forcedPlan.SubscriptionID)
	require.Zero(t, forcedPlan.SubDaysToDeduct)
	require.Zero(t, forcedPlan.SubDaysToRestore)
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

func TestExecuteRefundRenewShortensInsteadOfClosing(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("refund-renew-shorten@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-renew-shorten-user").
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(600).
		SetPayAmount(600).
		SetFeeRate(0).
		SetRechargeCode("REFUND-RENEW-SHORTEN").
		SetOutTradeNo("sub2_refund_renew_shorten").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetSubscriptionDays(60).
		SetProviderSnapshot(map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd":       10.0,
				"validity_days":          60.0,
				"intent":                 SubscriptionIntentRenew,
				"target_subscription_id": 77.0,
			},
		}).
		Save(ctx)
	require.NoError(t, err)

	today := TodayEastDayNumber()
	repo := newRefundUserSubRepoStub(&UserSubscription{
		ID:        77,
		UserID:    user.ID,
		GroupID:   8,
		Status:    SubscriptionStatusActive,
		ExpireDay: today + 70,
		ExpiresAt: ExpireDayToExpiresAt(today + 70),
	})
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{entClient: client, subscriptionSvc: subSvc}

	result, err := svc.ExecuteRefund(ctx, &RefundPlan{
		OrderID:          order.ID,
		Order:            order,
		RefundAmount:     600,
		GatewayAmount:    600,
		Reason:           "renew refund",
		DeductionType:    payment.DeductionTypeSubscription,
		SubscriptionID:   77,
		SubDaysToDeduct:  60,
		SubDaysToRestore: 60,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Nil(t, repo.closeDeleteRow, "renew refund must not close the card while previous service days remain")
	require.Equal(t, []int{60}, repo.shortenDays)
	require.Equal(t, today+10, repo.byID[77].ExpireDay)
	require.Equal(t, SubscriptionStatusActive, repo.byID[77].Status)
}

func TestRevokeRenewalDaysForRefundBranches(t *testing.T) {
	ctx := context.Background()
	today := TodayEastDayNumber()

	t.Run("rejects non-positive days", func(t *testing.T) {
		repo := newRefundUserSubRepoStub(&UserSubscription{
			ID:        77,
			UserID:    513,
			GroupID:   8,
			Status:    SubscriptionStatusActive,
			ExpireDay: today + 10,
			ExpiresAt: ExpireDayToExpiresAt(today + 10),
		})
		subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)

		err := subSvc.revokeRenewalDaysForRefund(ctx, 77, 0)
		require.Error(t, err)
		require.Equal(t, "INVALID_RENEW_DAYS", infraerrors.Reason(err))
		require.Empty(t, repo.shortenDays)
	})

	t.Run("closes card when renewal fully consumes remaining service", func(t *testing.T) {
		repo := newRefundUserSubRepoStub(&UserSubscription{
			ID:        78,
			UserID:    513,
			GroupID:   8,
			Status:    SubscriptionStatusActive,
			ExpireDay: today + 3,
			ExpiresAt: ExpireDayToExpiresAt(today + 3),
		})
		subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)

		require.NoError(t, subSvc.revokeRenewalDaysForRefund(ctx, 78, 10))
		require.Equal(t, int64(78), repo.closeSubscription)
		require.NotNil(t, repo.closeDeleteRow)
		require.False(t, *repo.closeDeleteRow)
		require.Empty(t, repo.shortenDays)
	})

	t.Run("caps very large renewal days and reactivates expired card", func(t *testing.T) {
		repo := newRefundUserSubRepoStub(&UserSubscription{
			ID:        79,
			UserID:    513,
			GroupID:   8,
			Status:    SubscriptionStatusExpired,
			ExpireDay: today + MaxValidityDays + 5,
			ExpiresAt: ExpireDayToExpiresAt(today + MaxValidityDays + 5),
		})
		subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)

		require.NoError(t, subSvc.revokeRenewalDaysForRefund(ctx, 79, MaxValidityDays+100))
		require.Equal(t, []int{MaxValidityDays}, repo.shortenDays)
		require.Equal(t, []string{SubscriptionStatusActive}, repo.updatedStatuses)
		require.Equal(t, SubscriptionStatusActive, repo.byID[79].Status)
	})
}

func TestRevokeRenewalDaysForRefundErrorBranches(t *testing.T) {
	ctx := context.Background()
	today := TodayEastDayNumber()

	t.Run("get failure", func(t *testing.T) {
		repo := newRefundUserSubRepoStub(&UserSubscription{
			ID:        80,
			UserID:    513,
			GroupID:   8,
			Status:    SubscriptionStatusActive,
			ExpireDay: today + 10,
			ExpiresAt: ExpireDayToExpiresAt(today + 10),
		})
		repo.getErr = assertErr("get failed")
		subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)

		require.ErrorContains(t, subSvc.revokeRenewalDaysForRefund(ctx, 80, 1), "get failed")
	})

	t.Run("shorten failure", func(t *testing.T) {
		repo := newRefundUserSubRepoStub(&UserSubscription{
			ID:        81,
			UserID:    513,
			GroupID:   8,
			Status:    SubscriptionStatusActive,
			ExpireDay: today + 10,
			ExpiresAt: ExpireDayToExpiresAt(today + 10),
		})
		repo.shortenErr = assertErr("shorten failed")
		subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)

		require.ErrorContains(t, subSvc.revokeRenewalDaysForRefund(ctx, 81, 1), "shorten failed")
	})

	t.Run("reactivation failure", func(t *testing.T) {
		repo := newRefundUserSubRepoStub(&UserSubscription{
			ID:        82,
			UserID:    513,
			GroupID:   8,
			Status:    SubscriptionStatusExpired,
			ExpireDay: today + 10,
			ExpiresAt: ExpireDayToExpiresAt(today + 10),
		})
		repo.updateStatusErr = assertErr("update failed")
		subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)

		require.ErrorContains(t, subSvc.revokeRenewalDaysForRefund(ctx, 82, 1), "update failed")
	})
}

func TestIsRenewSubscriptionRefundPlanGuards(t *testing.T) {
	require.False(t, isRenewSubscriptionRefundPlan(nil))
	require.False(t, isRenewSubscriptionRefundPlan(&RefundPlan{}))
	require.False(t, isRenewSubscriptionRefundPlan(&RefundPlan{Order: &dbent.PaymentOrder{
		OrderType: payment.OrderTypeBalance,
	}}))
	require.True(t, isRenewSubscriptionRefundPlan(&RefundPlan{Order: &dbent.PaymentOrder{
		OrderType: payment.OrderTypeSubscription,
		ProviderSnapshot: map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"intent": SubscriptionIntentRenew,
			},
		},
	}}))
}

func TestHandleKyrenRefundWebhookRenewBadSnapshotReturnsError(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("kyren-renew-bad-snapshot@example.com").
		SetPasswordHash("hash").
		SetUsername("kyren-renew-bad-snapshot-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(600).
		SetPayAmount(600).
		SetFeeRate(0).
		SetRechargeCode("KYREN-RENEW-BAD-SNAPSHOT").
		SetOutTradeNo("sub2_kyren_renew_bad_snapshot").
		SetPaymentType(payment.TypeEasyPay).
		SetPaymentTradeNo("kyren_trade_renew_bad_snapshot").
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderSnapshot(map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd":       10.0,
				"intent":                 SubscriptionIntentRenew,
				"target_subscription_id": 77.0,
			},
		}).
		Save(ctx)
	require.NoError(t, err)

	today := TodayEastDayNumber()
	repo := newRefundUserSubRepoStub(&UserSubscription{
		ID:        77,
		UserID:    user.ID,
		GroupID:   8,
		Status:    SubscriptionStatusActive,
		ExpireDay: today + 70,
		ExpiresAt: ExpireDayToExpiresAt(today + 70),
	})
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{entClient: client, subscriptionSvc: subSvc}

	err = svc.HandleKyrenRefundWebhook(ctx, &payment.KyrenRefundData{
		OrderID:      "sub2_kyren_renew_bad_snapshot",
		RefundID:     "rf_renew_bad_snapshot",
		RefundStatus: "FULL",
	}, "evt_renew_bad_snapshot")
	require.ErrorContains(t, err, "read renew refund days")
	require.Empty(t, repo.shortenDays)

	updated, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, updated.Status)
}

func TestHandleKyrenRefundWebhookRenewShortensCard(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("kyren-renew-shortens@example.com").
		SetPasswordHash("hash").
		SetUsername("kyren-renew-shortens-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(600).
		SetPayAmount(600).
		SetFeeRate(0).
		SetRechargeCode("KYREN-RENEW-SHORTENS").
		SetOutTradeNo("sub2_kyren_renew_shortens").
		SetPaymentType(payment.TypeEasyPay).
		SetPaymentTradeNo("kyren_trade_renew_shortens").
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderSnapshot(map[string]any{
			subscriptionSnapshotKey: map[string]any{
				"daily_amount_usd":       10.0,
				"validity_days":          60.0,
				"intent":                 SubscriptionIntentRenew,
				"target_subscription_id": 77.0,
			},
		}).
		Save(ctx)
	require.NoError(t, err)

	today := TodayEastDayNumber()
	repo := newRefundUserSubRepoStub(&UserSubscription{
		ID:        77,
		UserID:    user.ID,
		GroupID:   8,
		Status:    SubscriptionStatusActive,
		ExpireDay: today + 70,
		ExpiresAt: ExpireDayToExpiresAt(today + 70),
	})
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	svc := &PaymentService{entClient: client, subscriptionSvc: subSvc}

	err = svc.HandleKyrenRefundWebhook(ctx, &payment.KyrenRefundData{
		OrderID:      "sub2_kyren_renew_shortens",
		RefundID:     "rf_renew_shortens",
		RefundStatus: payment.KyrenRefundStatusFull,
	}, "evt_renew_shortens")
	require.NoError(t, err)
	require.Equal(t, []int{60}, repo.shortenDays)
	require.Equal(t, today+10, repo.byID[77].ExpireDay)

	updated, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, updated.Status)
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

func TestFinishRefundPendingMarksOrderPendingAndRollsBackDeduction(t *testing.T) {
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
		SetRechargeCode("REFUND-PENDING-ORDER").
		SetOutTradeNo("sub2_refund_pending_order").
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("pi_refund_pending").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusRefunding).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	var rolledBack float64
	userRepo := &mockUserRepo{}
	userRepo.updateBalanceFn = func(ctx context.Context, id int64, amount float64) error {
		require.Equal(t, user.ID, id)
		rolledBack += amount
		return nil
	}
	svc := &PaymentService{
		entClient: client,
		userRepo:  userRepo,
	}
	plan := &RefundPlan{
		OrderID:         order.ID,
		Order:           order,
		RefundAmount:    40,
		GatewayAmount:   40,
		Reason:          "gateway accepted but not final",
		Force:           true,
		DeductionType:   payment.DeductionTypeBalance,
		BalanceToDeduct: 40,
	}

	result, err := svc.finishRefund(ctx, plan, &payment.RefundResponse{Status: payment.ProviderStatusPending})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.Contains(t, result.Warning, "pending confirmation")
	require.Equal(t, 40.0, rolledBack)
	require.Zero(t, plan.BalanceToDeduct)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	require.Equal(t, 40.0, reloaded.RefundAmount)
	require.NotNil(t, reloaded.RefundReason)
	require.Equal(t, "gateway accepted but not final", *reloaded.RefundReason)
	require.Nil(t, reloaded.RefundAt)

	pendingAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, pendingAudits)
	successAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, successAudits)
}

func TestFinishRefundSuccessStatusesFinalize(t *testing.T) {
	for _, status := range []string{payment.ProviderStatusSuccess, payment.ProviderStatusRefunded} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)

			user, err := client.User.Create().
				SetEmail("refund-success-" + status + "@example.com").
				SetPasswordHash("hash").
				SetUsername("refund-success-" + status).
				Save(ctx)
			require.NoError(t, err)

			order, err := client.PaymentOrder.Create().
				SetUserID(user.ID).
				SetUserEmail(user.Email).
				SetUserName(user.Username).
				SetAmount(100).
				SetPayAmount(100).
				SetFeeRate(0).
				SetRechargeCode("REFUND-SUCCESS-" + status).
				SetOutTradeNo("sub2_refund_success_" + status).
				SetPaymentType(payment.TypeStripe).
				SetPaymentTradeNo("pi_refund_success_" + status).
				SetOrderType(payment.OrderTypeBalance).
				SetStatus(OrderStatusRefunding).
				SetExpiresAt(time.Now().Add(time.Hour)).
				SetPaidAt(time.Now()).
				SetClientIP("127.0.0.1").
				SetSrcHost("api.example.com").
				Save(ctx)
			require.NoError(t, err)

			svc := &PaymentService{entClient: client}
			plan := &RefundPlan{
				OrderID:         order.ID,
				Order:           order,
				RefundAmount:    100,
				GatewayAmount:   100,
				Reason:          "final success",
				DeductionType:   payment.DeductionTypeBalance,
				BalanceToDeduct: 100,
			}

			result, err := svc.finishRefund(ctx, plan, &payment.RefundResponse{Status: status})
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.Success)
			require.Equal(t, 100.0, result.BalanceDeducted)

			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefunded, reloaded.Status)
			require.NotNil(t, reloaded.RefundAt)

			successAudits, err := client.PaymentAuditLog.Query().
				Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
				Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, successAudits)
			pendingAudits, err := client.PaymentAuditLog.Query().
				Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
				Count(ctx)
			require.NoError(t, err)
			require.Zero(t, pendingAudits)
		})
	}
}

func TestQueryAndFinalizeRefundFinalizesProviderStatuses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		wantStatus string
		wantDeduct float64
	}{
		{name: "success", status: payment.ProviderStatusSuccess, wantStatus: OrderStatusRefunded, wantDeduct: 100},
		{name: "failed", status: payment.ProviderStatusFailed, wantStatus: OrderStatusRefundFailed},
		{name: "pending", status: payment.ProviderStatusPending, wantStatus: OrderStatusRefundPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "query-finalize-"+tc.name)

			var deducted float64
			svc := &PaymentService{
				entClient:    client,
				loadBalancer: &captureLoadBalancer{},
				userRepo: &mockUserRepo{deductBalanceFn: func(ctx context.Context, id int64, amount float64) error {
					deducted += amount
					return nil
				}},
			}
			restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
				refundResponse: &payment.RefundResponse{RefundID: "rf_test", Status: tc.status},
			})
			defer restore()

			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tc.status == payment.ProviderStatusSuccess, result.Success)
			require.Equal(t, tc.wantDeduct, deducted)

			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, reloaded.Status)
		})
	}
}

func TestQueryAndFinalizeRefundUnsupportedProviderReturnsClearError(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "query-finalize-unsupported")
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	restore := replacePaymentProviderFactoryForTest(t, refundProviderTestDouble{})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "REFUND_QUERY_UNSUPPORTED", infraerrors.Reason(err))
}

func createPendingRefundOrderForTest(t *testing.T, ctx context.Context, client *dbent.Client, suffix string) *dbent.PaymentOrder {
	t.Helper()

	user, err := client.User.Create().
		SetEmail(suffix + "@example.com").
		SetPasswordHash("hash").
		SetUsername(suffix).
		Save(ctx)
	require.NoError(t, err)

	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName(suffix + "-provider").
		SetConfig("{}").
		SetSupportedTypes("stripe").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-" + suffix).
		SetOutTradeNo("sub2_" + suffix).
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("pi_" + suffix).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusRefundPending).
		SetRefundAmount(100).
		SetRefundReason("pending refund").
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(strconv.FormatInt(inst.ID, 10)).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("REFUND_PENDING").
		SetOperator("admin").
		SetDetail(`{"refundID":"rf_test","deductionRollbackOK":true}`).
		Save(ctx)
	require.NoError(t, err)
	return order
}

func replacePaymentProviderFactoryForTest(t *testing.T, prov payment.Provider) func() {
	t.Helper()
	original := createPaymentProviderFromInstance
	createPaymentProviderFromInstance = func(providerKey, instanceID string, config map[string]string) (payment.Provider, error) {
		return prov, nil
	}
	return func() { createPaymentProviderFromInstance = original }
}

type refundProviderTestDouble struct{}

func (refundProviderTestDouble) Name() string { return "refund-test" }
func (refundProviderTestDouble) ProviderKey() string {
	return payment.TypeStripe
}
func (refundProviderTestDouble) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeStripe}
}
func (refundProviderTestDouble) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	return nil, nil
}
func (refundProviderTestDouble) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	return nil, nil
}
func (refundProviderTestDouble) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	return nil, nil
}
func (refundProviderTestDouble) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	return nil, nil
}

type refundQueryProviderTestDouble struct {
	refundProviderTestDouble
	refundResponse *payment.RefundResponse
}

func (p *refundQueryProviderTestDouble) QueryRefund(context.Context, payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	return p.refundResponse, nil
}
