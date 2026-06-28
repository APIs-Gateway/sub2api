//go:build integration

package repository

import (
	"net/http"
	"testing"

	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 用户订阅 HTTP 只读/报价端点(List/Active/Progress/Pricing/Quote/Summary/退役透支开关)的真实 DB 端到端,
// 覆盖 SubscriptionHandler 各方法 + 驱动 service 的 ListActiveUserSubscriptions/GetSubscriptionProgress/
// QuoteSubscription/PricingBounds/MonthlyOverdraftRemaining。

func subscriptionEndpointsRouter(t *testing.T, userID int64) *gin.Engine {
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
	g := router.Group("/api/v1/subscriptions")
	g.GET("", h.List)
	g.GET("/active", h.GetActive)
	g.GET("/progress", h.GetProgress)
	g.GET("/pricing", h.PricingBounds)
	g.POST("/quote", h.Quote)
	g.POST("/renew/quote", h.RenewQuote)
	g.POST("/change-plan/quote", h.ChangePlanQuote)
	g.GET("/summary", h.GetSummary)
	g.PUT("/:id/overdraft", h.SetOverdraftDays)
	return router
}

func TestSubscriptionEndpointsHTTP_ReadAndQuotePostgres(t *testing.T) {
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "sub-endpoints-" + uuid.NewString() + "@example.com"})
	group := mustCreateGroup(t, client, &service.Group{Name: "sub-endpoints-" + uuid.NewString()})
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
		StartDay:        today - 1,
		ExpireDay:       today + 20,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 20),
		Status:          service.SubscriptionStatusActive,
	})
	router := subscriptionEndpointsRouter(t, user.ID)

	// 列表 / 生效列表 / 进度 / 摘要:有卡用户均 200。
	for _, path := range []string{"", "/active", "/progress", "/summary"} {
		rec := performLifecycleRequest(t, router, http.MethodGet, "/api/v1/subscriptions"+path, nil)
		require.Equalf(t, http.StatusOK, rec.Code, "GET %s", path)
	}

	// 定价区间:无需鉴权数据,200。
	rec := performLifecycleRequest(t, router, http.MethodGet, "/api/v1/subscriptions/pricing", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	// 报价:合法 D/T → 200。
	rec = performLifecycleRequest(t, router, http.MethodPost, "/api/v1/subscriptions/quote",
		map[string]any{"daily_amount_usd": 10, "validity_days": 30})
	require.Equal(t, http.StatusOK, rec.Code)

	// 报价:越界 D(0)→ 业务错误(非 2xx)。
	rec = performLifecycleRequest(t, router, http.MethodPost, "/api/v1/subscriptions/quote",
		map[string]any{"daily_amount_usd": 0, "validity_days": 30})
	require.NotEqual(t, http.StatusOK, rec.Code)

	// 续费报价:有生效卡 + 合法 T → 200(只读预览,不下单)。
	rec = performLifecycleRequest(t, router, http.MethodPost, "/api/v1/subscriptions/renew/quote",
		map[string]any{"validity_days": 30})
	require.Equal(t, http.StatusOK, rec.Code)

	// 续费报价:缺 validity_days(binding required)→ 400。
	rec = performLifecycleRequest(t, router, http.MethodPost, "/api/v1/subscriptions/renew/quote",
		map[string]any{})
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// 转套餐报价:升档(新 D 高于旧卡)→ diff>0 → 200。
	rec = performLifecycleRequest(t, router, http.MethodPost, "/api/v1/subscriptions/change-plan/quote",
		map[string]any{"daily_amount_usd": 20, "validity_days": 30})
	require.Equal(t, http.StatusOK, rec.Code)

	// 转套餐报价:缺必填字段 → 400。
	rec = performLifecycleRequest(t, router, http.MethodPost, "/api/v1/subscriptions/change-plan/quote",
		map[string]any{})
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// 退役的 per-day 透支开关:合法 id → 410 Gone。
	rec = performLifecycleRequest(t, router, http.MethodPut, "/api/v1/subscriptions/5/overdraft", map[string]any{})
	require.Equal(t, http.StatusGone, rec.Code)

	// 退役开关:非法 id → 400。
	rec = performLifecycleRequest(t, router, http.MethodPut, "/api/v1/subscriptions/abc/overdraft", map[string]any{})
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// 未鉴权(无 AuthSubject)→ 各端点 401。
func TestSubscriptionEndpointsHTTP_RequiresAuthPostgres(t *testing.T) {
	router := subscriptionEndpointsRouter(t, 0)
	for _, path := range []string{"", "/active", "/progress", "/summary"} {
		rec := performLifecycleRequest(t, router, http.MethodGet, "/api/v1/subscriptions"+path, nil)
		require.Equalf(t, http.StatusUnauthorized, rec.Code, "GET %s without auth", path)
	}
	// 续费 / 转套餐报价端点同样要求鉴权 → 401(在 bind 之前先拦)。
	for _, path := range []string{"/renew/quote", "/change-plan/quote"} {
		rec := performLifecycleRequest(t, router, http.MethodPost, "/api/v1/subscriptions"+path,
			map[string]any{"daily_amount_usd": 10, "validity_days": 30})
		require.Equalf(t, http.StatusUnauthorized, rec.Code, "POST %s without auth", path)
	}
}
