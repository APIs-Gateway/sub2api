//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 自定义 D+T 订阅单（无固定套餐，P5e 放开履约后）：validateSubOrder 必须产出**后端权威**报价，
// 成交价完全由 u(D) 公式决定、绝不信任前端 req.Amount（否则客户端可篡改价格 → 资损）；
// 自定义卡无 group 归属（groupID=0、plan=nil）。
func TestValidateSubOrder_CustomQuoteAuthoritative(t *testing.T) {
	ctx := context.Background()
	sub := &SubscriptionService{}
	svc := &PaymentService{subscriptionSvc: sub}

	bounds := sub.PricingBounds()
	d := bounds.DMin
	tt := bounds.TMin
	quote, err := sub.QuoteSubscription(d, tt)
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
	require.EqualValues(t, 0, spec.groupID, "自定义卡无 group 归属")
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

// D/T 越界必须在收款前被拒（INVALID_SUBSCRIPTION_PARAMS），绝不生成无法履约的订单。
func TestValidateSubOrder_CustomRejectsOutOfRange(t *testing.T) {
	ctx := context.Background()
	sub := &SubscriptionService{}
	svc := &PaymentService{subscriptionSvc: sub}
	bounds := sub.PricingBounds()

	_, err := svc.validateSubOrder(ctx, CreateOrderRequest{
		PlanID:         0,
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
