//go:build integration

package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 生命周期 HTTP（per-day redesign §5/§7）：续费/转套餐改走法币支付网关，这两个端点只返回**报价**预览
// （不改状态、不扣余额）；实际下单走 POST /payment/orders（intent=renew|change_plan），见 money-path 集成测试。

func TestSubscriptionLifecycleHTTP_RenewQuotePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	cfg := service.DefaultSubscriptionPricingConfig()
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("lifecycle-http-renew-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "lifecycle-http-renew-" + uuid.NewString()})
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  10,
		GrantedTotalUSD: 300,
		TodayRemaining:  10,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 10,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 10),
		Status:          service.SubscriptionStatusActive,
	})
	router := subscriptionLifecycleRouter(t, user.ID)
	rec := performLifecycleRequest(t, router, http.MethodPost, "/api/v1/subscriptions/renew/quote", map[string]any{"validity_days": 30})
	require.Equal(t, http.StatusOK, rec.Code)

	env := decodeLifecycleEnvelope(t, rec)
	require.EqualValues(t, 0, env.Code)
	data := requireLifecycleData(t, env)
	require.EqualValues(t, card.ID, data["subscription_id"])
	require.EqualValues(t, 30, data["added_days"])
	require.InDelta(t, 10, data["daily_amount_usd"], 1e-9)
	require.InDelta(t, cfg.Price(10, 30), data["price"], 1e-6)

	// 报价不改状态：卡未延长、余额未动。
	gotSub, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+10, gotSub.ExpireDay)
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100000, gotUser.Balance, 1e-9)
}

func TestSubscriptionLifecycleHTTP_ChangePlanQuotePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	cfg := service.DefaultSubscriptionPricingConfig()
	today := service.TodayEastDayNumber()

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("lifecycle-http-change-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "lifecycle-http-change-" + uuid.NewString()})
	old := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  10,
		GrantedTotalUSD: 300,
		TodayRemaining:  2,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 29,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 29),
		Status:          service.SubscriptionStatusActive,
	})
	newPlan := mustCreateChangePlanPlan(t, client, group.ID, 20, 30)

	router := subscriptionLifecycleRouter(t, user.ID)
	rec := performLifecycleRequest(t, router, http.MethodPost, "/api/v1/subscriptions/change-plan/quote", map[string]any{"daily_amount_usd": newPlan.DailyAmountUsd, "validity_days": newPlan.ValidityDays})
	require.Equal(t, http.StatusOK, rec.Code)

	env := decodeLifecycleEnvelope(t, rec)
	require.EqualValues(t, 0, env.Code)
	data := requireLifecycleData(t, env)
	require.EqualValues(t, old.ID, data["old_subscription_id"])
	require.InDelta(t, 20, data["daily_amount_usd"], 1e-9)
	require.EqualValues(t, 30, data["validity_days"])
	require.InDelta(t, cfg.Price(20, 30), data["new_plan_price"], 1e-6)
	require.InDelta(t, cfg.Price(10, 29), data["old_remaining_value"], 1e-6)
	require.Greater(t, data["diff"].(float64), 0.0, "升档应 diff>0")

	// 报价不改状态：旧卡仍 active、余额未动。
	gotOld, err := NewUserSubscriptionRepository(client).GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, gotOld.Status)
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100000, gotUser.Balance, 1e-9)
}

func TestSubscriptionLifecycleHTTP_RejectsMissingAuthAndInvalidParams(t *testing.T) {
	routerNoAuth := subscriptionLifecycleRouter(t, 0)
	rec := performLifecycleRequest(t, routerNoAuth, http.MethodPost, "/api/v1/subscriptions/renew/quote", map[string]any{"validity_days": 30})
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	user := mustCreateUser(t, testEntClient(t), &service.User{
		Email:   fmt.Sprintf("lifecycle-http-invalid-%s@example.com", uuid.NewString()),
		Balance: 100000,
	})
	router := subscriptionLifecycleRouter(t, user.ID)
	for _, tc := range []struct {
		name string
		body map[string]any
		path string
	}{
		{name: "renew missing validity", body: map[string]any{}, path: "/api/v1/subscriptions/renew/quote"},
		{name: "renew zero validity", body: map[string]any{"validity_days": 0}, path: "/api/v1/subscriptions/renew/quote"},
		{name: "change missing fields", body: map[string]any{}, path: "/api/v1/subscriptions/change-plan/quote"},
		{name: "change negative validity", body: map[string]any{"daily_amount_usd": 20, "validity_days": -1}, path: "/api/v1/subscriptions/change-plan/quote"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := performLifecycleRequest(t, router, http.MethodPost, tc.path, tc.body)
			require.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func subscriptionLifecycleRouter(t *testing.T, userID int64) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	router := gin.New()
	if userID > 0 {
		router.Use(func(c *gin.Context) {
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID})
			c.Next()
		})
	}

	h := userhandler.NewSubscriptionHandler(makeSubscriptionService(t))
	subscriptions := router.Group("/api/v1/subscriptions")
	subscriptions.POST("/renew/quote", h.RenewQuote)
	subscriptions.POST("/change-plan/quote", h.ChangePlanQuote)
	return router
}

func performLifecycleRequest(t *testing.T, router *gin.Engine, method, path string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

type lifecycleEnvelope struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Reason  string         `json:"reason"`
	Data    map[string]any `json:"data"`
}

func decodeLifecycleEnvelope(t *testing.T, rec *httptest.ResponseRecorder) lifecycleEnvelope {
	t.Helper()
	var env lifecycleEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	return env
}

func requireLifecycleData(t *testing.T, env lifecycleEnvelope) map[string]any {
	t.Helper()
	require.NotNil(t, env.Data)
	return env.Data
}
