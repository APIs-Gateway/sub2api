//go:build unit

package service

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 自定义 D+T 订阅单（无固定套餐，P5e 放开履约后）：validateSubOrder 必须产出**后端权威**报价，
// 成交价完全由 u(D) 公式决定、绝不信任前端 req.Amount（否则客户端可篡改价格 → 资损）；
// 自定义卡是用户级套餐，全分组通用，默认不归属具体 group（plan=nil）。
func TestValidateSubOrder_CustomQuoteAuthoritative(t *testing.T) {
	ctx := context.Background()
	sub := &SubscriptionService{}
	svc := &PaymentService{
		subscriptionSvc: sub,
	}

	bounds := sub.PricingBounds(ctx)
	d := bounds.DMin
	tt := bounds.TMin
	quote, err := sub.QuoteSubscription(ctx, d, tt)
	require.NoError(t, err)
	require.Greater(t, quote.Price, 0.0)

	// 前端谎报一个离谱的 Amount，后端必须无视它、按公式定价。
	spec, err := svc.validateSubOrder(ctx, CreateOrderRequest{
		PlanID:         0,
		DailyAmountUSD: d,
		ValidityDays:   tt,
		Amount:         999999,
	})
	require.NoError(t, err)
	require.Nil(t, spec.plan, "自定义单无固定套餐")
	require.EqualValues(t, 0, spec.groupID, "自定义卡默认不绑定分组")
	require.InDelta(t, d, spec.dailyAmount, 1e-9)
	require.Equal(t, tt, spec.validityDays)
	require.InDelta(t, quote.Price, spec.price, 1e-9, "成交价=后端权威报价，忽略前端 Amount")
	require.InDelta(t, quote.UnitPrice, spec.unitPrice, 1e-9)

	// 下单冻结快照同源于 spec：履约严格按此发卡。
	snap := buildSubscriptionOrderSnapshot(spec, map[string]any{"currency": "CNY"})
	require.InDelta(t, quote.Price, snap["price"].(float64), 1e-9)
	require.InDelta(t, d, snap["daily_amount_usd"].(float64), 1e-9)
	require.Equal(t, tt, snap["validity_days"])
	require.Equal(t, SubscriptionFormulaVersion, snap["formula_version"])
}

// 订单快照必须把 W/M 与 D/T/u/price 一并冻结（spec §2：订单存 D/W/M/T/u/price/formula_version/currency），
// 且能被 readSubscriptionSnapshotWM 原样读回——发卡按冻结值、不按履约时派生系数重算。
func TestSubscriptionSnapshot_FreezesWeeklyMonthlyLimits(t *testing.T) {
	ctx := context.Background()
	sub := &SubscriptionService{}
	svc := &PaymentService{
		subscriptionSvc: sub,
		groupRepo:       &subscriptionGroupRepoStub{group: &Group{ID: 1, Status: payment.EntityStatusActive}},
	}
	bounds := sub.PricingBounds(ctx)
	d, tt := bounds.DMin, bounds.TMin

	spec, err := svc.validateSubOrder(context.Background(), CreateOrderRequest{
		DailyAmountUSD: d, ValidityDays: tt,
	})
	require.NoError(t, err)

	wantW, wantM := DeriveWindowCaps(d, tt)
	snap := buildSubscriptionOrderSnapshot(spec, map[string]any{"currency": "CNY"})
	require.InDelta(t, wantW, snap["weekly_limit_usd"].(float64), 1e-9, "W 必须冻结进快照")
	require.InDelta(t, wantM, snap["monthly_limit_usd"].(float64), 1e-9, "M 必须冻结进快照")

	// 读回：新订单快照含 W/M。
	order := &dbent.PaymentOrder{ProviderSnapshot: map[string]any{subscriptionSnapshotKey: snap}}
	w, m, ok := readSubscriptionSnapshotWM(order)
	require.True(t, ok, "新订单应能读回 W/M")
	require.InDelta(t, wantW, w, 1e-9)
	require.InDelta(t, wantM, m, 1e-9)

	// 老订单（快照无 W/M）→ ok=false，调用方回退按 D/T 派生（向后兼容）。
	legacy := &dbent.PaymentOrder{ProviderSnapshot: map[string]any{subscriptionSnapshotKey: map[string]any{
		"daily_amount_usd": d, "validity_days": tt,
	}}}
	_, _, ok = readSubscriptionSnapshotWM(legacy)
	require.False(t, ok, "老订单无 W/M 快照应回 ok=false")
}

func TestPaymentOrderProductName_DistinguishesSubscriptionIntents(t *testing.T) {
	newOrder := func(intent string) *dbent.PaymentOrder {
		return &dbent.PaymentOrder{
			OrderType: payment.OrderTypeSubscription,
			ProviderSnapshot: map[string]any{
				subscriptionSnapshotKey: map[string]any{
					"intent":           intent,
					"daily_amount_usd": 30.0,
					"validity_days":    30.0,
				},
			},
		}
	}

	require.Equal(t, "购买套餐 每日$30 / 30天", PaymentOrderProductName(newOrder(SubscriptionIntentPurchase)))
	require.Equal(t, "续费套餐 每日$30 / 30天", PaymentOrderProductName(newOrder(SubscriptionIntentRenew)))
	require.Equal(t, "转套餐 每日$30 / 30天", PaymentOrderProductName(newOrder(SubscriptionIntentChangePlan)))
}

func TestPaymentOrderProductName_BalanceUsesCreditedUSD(t *testing.T) {
	order := &dbent.PaymentOrder{OrderType: payment.OrderTypeBalance, Amount: 140}
	require.Equal(t, "余额充值 $140", PaymentOrderProductName(order))
}

// D/T 越界必须在收款前被拒（INVALID_SUBSCRIPTION_PARAMS），绝不生成无法履约的订单。
func TestValidateSubOrder_CustomRejectsOutOfRange(t *testing.T) {
	ctx := context.Background()
	sub := &SubscriptionService{}
	svc := &PaymentService{
		subscriptionSvc: sub,
		groupRepo:       &subscriptionGroupRepoStub{group: &Group{ID: 1, Status: payment.EntityStatusActive}},
	}
	bounds := sub.PricingBounds(ctx)

	_, err := svc.validateSubOrder(ctx, CreateOrderRequest{
		PlanID:         0,
		GroupID:        1,
		DailyAmountUSD: bounds.DMax + 1000,
		ValidityDays:   bounds.TMin,
	})
	require.Error(t, err)
	require.Equal(t, "INVALID_SUBSCRIPTION_PARAMS", infraerrors.Reason(err))

	// 既无 plan 又无合法 D/T → 要求选套餐。
	_, err = svc.validateSubOrder(ctx, CreateOrderRequest{PlanID: 0})
	require.Error(t, err)
	require.Equal(t, "INVALID_INPUT", infraerrors.Reason(err))
}

func TestValidateSubOrder_CustomAcceptsLegacyGroupID(t *testing.T) {
	ctx := context.Background()
	sub := &SubscriptionService{}
	svc := &PaymentService{
		subscriptionSvc: sub,
		groupRepo:       &subscriptionGroupRepoStub{group: &Group{ID: 7, Status: payment.EntityStatusActive}},
	}
	bounds := sub.PricingBounds(ctx)

	spec, err := svc.validateSubOrder(ctx, CreateOrderRequest{
		GroupID:        7,
		DailyAmountUSD: bounds.DMin,
		ValidityDays:   bounds.TMin,
	})
	require.NoError(t, err)
	require.EqualValues(t, 7, spec.groupID)
}
