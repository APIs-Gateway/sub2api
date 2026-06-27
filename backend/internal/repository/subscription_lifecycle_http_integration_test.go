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

func TestSubscriptionLifecycleHTTP_RenewPostgres(t *testing.T) {
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
	plan := mustCreateChangePlanPlan(t, client, group.ID, 10, 30)

	router := subscriptionLifecycleRouter(t, user.ID)
	rec := performLifecycleRequest(t, router, http.MethodPost, "/api/v1/subscriptions/renew", map[string]any{"plan_id": plan.ID})
	require.Equal(t, http.StatusOK, rec.Code)

	env := decodeLifecycleEnvelope(t, rec)
	require.EqualValues(t, 0, env.Code)
	data := requireLifecycleData(t, env)
	require.EqualValues(t, card.ID, data["subscription_id"])
	require.EqualValues(t, 30, data["added_days"])
	require.InDelta(t, cfg.Price(10, 30), data["price"], 1e-6)
	require.EqualValues(t, today+40, data["new_expire_day"])

	gotSub, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today+40, gotSub.ExpireDay)
	require.InDelta(t, 10, gotSub.DailyAmountUSD, 1e-9)

	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100000-cfg.Price(10, 30), gotUser.Balance, 1e-6)
}

func TestSubscriptionLifecycleHTTP_ChangePlanPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
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
		TodayRemaining:  2, // 今天已从旧卡套餐额度花掉 8。
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 29,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 29),
		Status:          service.SubscriptionStatusActive,
	})
	newPlan := mustCreateChangePlanPlan(t, client, group.ID, 20, 30)

	router := subscriptionLifecycleRouter(t, user.ID)
	rec := performLifecycleRequest(t, router, http.MethodPost, "/api/v1/subscriptions/change-plan", map[string]any{"plan_id": newPlan.ID})
	require.Equal(t, http.StatusOK, rec.Code)

	env := decodeLifecycleEnvelope(t, rec)
	require.EqualValues(t, 0, env.Code)
	data := requireLifecycleData(t, env)
	require.EqualValues(t, old.ID, data["old_subscription_id"])
	require.InDelta(t, 12, data["new_card_today_balance"], 1e-9)
	require.EqualValues(t, today+29, data["new_expire_day"])

	subRepo := NewUserSubscriptionRepository(client)
	gotOld, err := subRepo.GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusExpired, gotOld.Status)

	newID := int64(data["new_subscription_id"].(float64))
	gotNew, err := subRepo.GetByID(ctx, newID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, gotNew.Status)
	require.InDelta(t, 20, gotNew.DailyAmountUSD, 1e-9)
	require.InDelta(t, 12, gotNew.TodayRemaining, 1e-9)
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, user.ID, service.SubscriptionStatusActive))
}

func TestSubscriptionLifecycleHTTP_RejectsMissingAuthAndInvalidPlanID(t *testing.T) {
	routerNoAuth := subscriptionLifecycleRouter(t, 0)
	rec := performLifecycleRequest(t, routerNoAuth, http.MethodPost, "/api/v1/subscriptions/renew", map[string]any{"plan_id": 1})
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
		{name: "renew missing plan", body: map[string]any{}, path: "/api/v1/subscriptions/renew"},
		{name: "renew zero plan", body: map[string]any{"plan_id": 0}, path: "/api/v1/subscriptions/renew"},
		{name: "change negative plan", body: map[string]any{"plan_id": -1}, path: "/api/v1/subscriptions/change-plan"},
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
	subscriptions.POST("/renew", h.Renew)
	subscriptions.POST("/change-plan", h.ChangePlan)
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
