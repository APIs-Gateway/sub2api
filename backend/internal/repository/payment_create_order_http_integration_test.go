//go:build integration

package repository

import (
	"net/http"
	"testing"

	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// CreateOrder 的 HTTP 分发层(PaymentHandler.CreateOrder)端到端:鉴权 → ShouldBindJSON →
// 把 handler 层 CreateOrderRequest 逐字段映射到 service.CreateOrderRequest → 调 paymentService.CreateOrder。
// 复用 makeCreateOrderPaymentServiceForSubscriptionIntegration:真实 DB 落单后于 provider 空配置失败,
// 让我们既验证 handler 字段映射执行,又验证下单仍落库(不丢单)。

func createOrderHTTPRouter(t *testing.T, userID int64, paySvc *service.PaymentService) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if userID > 0 {
		router.Use(func(c *gin.Context) {
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID})
			c.Next()
		})
	}
	h := userhandler.NewPaymentHandler(paySvc, nil, nil)
	router.POST("/api/v1/payment/orders", h.CreateOrder)
	return router
}

func TestPaymentCreateOrderHTTP_RenewDispatchesAndPersistsPostgres(t *testing.T) {
	client := testEntClient(t)
	paySvc := makeCreateOrderPaymentServiceForSubscriptionIntegration(t)

	user := mustCreateUser(t, client, &service.User{
		Email:    "create-http-renew-" + uuid.NewString() + "@example.com",
		Username: "create-http-renew",
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "create-http-renew-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	d := 10.0
	w, m := service.DeriveWindowCaps(d, 30)
	mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  d,
		DailyLimitUSD:   &d,
		WeeklyLimitUSD:  &w,
		MonthlyLimitUSD: &m,
		TodayRemaining:  d,
		TodayDay:        today,
		StartDay:        today - 3,
		ExpireDay:       today + 5,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 5),
		Status:          service.SubscriptionStatusActive,
	})

	router := createOrderHTTPRouter(t, user.ID, paySvc)

	// 非可见方法(easypay)绕开可见方法闸 → 走完 handler 映射 + createOrderInTx 落单,再于 provider 失败。
	rec := performLifecycleRequest(t, router, http.MethodPost, "/api/v1/payment/orders", map[string]any{
		"payment_type":        payment.TypeEasyPay,
		"order_type":          payment.OrderTypeSubscription,
		"subscription_intent": service.SubscriptionIntentRenew,
		"validity_days":       30,
	})
	// provider 空配置必失败 → handler 走 ErrorFrom(非 2xx);但订单已按权威 spec 落库。
	require.NotEqual(t, http.StatusOK, rec.Code)

	order := findSingleSubscriptionOrder(t, client, user.ID)
	require.Equal(t, service.OrderStatusFailed, order.Status, "handler 分发后下单仍落库,不丢单")
	intent, _ := snapshotIntent(t, order)
	require.Equal(t, service.SubscriptionIntentRenew, intent)
}

func TestPaymentCreateOrderHTTP_RequiresAuthPostgres(t *testing.T) {
	paySvc := makeCreateOrderPaymentServiceForSubscriptionIntegration(t)
	router := createOrderHTTPRouter(t, 0, paySvc) // 无鉴权中间件

	rec := performLifecycleRequest(t, router, http.MethodPost, "/api/v1/payment/orders", map[string]any{
		"payment_type": payment.TypeAlipay,
	})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestPaymentCreateOrderHTTP_BadRequestOnMissingPaymentTypePostgres(t *testing.T) {
	paySvc := makeCreateOrderPaymentServiceForSubscriptionIntegration(t)
	router := createOrderHTTPRouter(t, 12345, paySvc)

	// 缺 payment_type(binding:"required")→ ShouldBindJSON 失败 → 400。
	rec := performLifecycleRequest(t, router, http.MethodPost, "/api/v1/payment/orders", map[string]any{
		"order_type": payment.OrderTypeSubscription,
	})
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
