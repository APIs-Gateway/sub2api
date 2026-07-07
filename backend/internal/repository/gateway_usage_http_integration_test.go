//go:build integration

package repository

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// GET /v1/usage 的 unrestricted 模式三窗口订阅展示(gateway_handler.usageUnrestricted 卡分支):
// 有生效卡 → 返回卡级日/周/月 usage-vs-limit + 到期信息。真实 DB + 真实 BillingCacheService。

func newGatewayHandlerForUsage(t *testing.T) *userhandler.GatewayHandler {
	t.Helper()
	client := testEntClient(t)
	billing := service.NewBillingCacheService(
		NewBillingCache(testRedis(t)),
		NewUserRepository(client, integrationDB),
		NewUserSubscriptionRepository(client),
		nil, nil, nil,
		&config.Config{},
		nil, nil,
	)
	t.Cleanup(billing.Stop)
	// usageUnrestricted 卡分支只用 billingCacheService;usageService 等 best-effort 依赖给 nil(已 nil-safe)。
	return userhandler.NewGatewayHandler(
		nil, nil, nil, nil, nil, nil,
		billing,
		nil, nil, nil, nil, nil, nil,
		&config.Config{},
		nil,
	)
}

func TestGatewayUsageHTTP_UnrestrictedShowsThreeWindowSubscriptionPostgres(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client := testEntClient(t)
	h := newGatewayHandlerForUsage(t)

	user := mustCreateUser(t, client, &service.User{Email: "usage-card-" + uuid.NewString() + "@example.com", Balance: 12})
	today := service.TodayEastDayNumber()
	d := 10.0
	w, m := service.DeriveWindowCaps(d, 30)
	now := time.Now()
	dUse := 3.0
	// 卡用无 group(GroupID=0):usageUnrestricted 卡分支只读卡的三窗口字段,planName 取自 apiKey.Group;
	// 不建 active 组可避免污染 TestListActiveByPlatform 计数断言(本文件名先于 group_repo 跑)。
	mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		GroupID:            0,
		DailyAmountUSD:     d,
		DailyLimitUSD:      &d,
		WeeklyLimitUSD:     &w,
		MonthlyLimitUSD:    &m,
		DailyUsageUSD:      dUse,
		DailyWindowStart:   &now,
		WeeklyWindowStart:  &now,
		MonthlyWindowStart: &now,
		TodayRemaining:     d,
		TodayDay:           today,
		StartDay:           today - 1,
		ExpireDay:          today + 20,
		ExpiresAt:          service.ExpireDayToExpiresAt(today + 20),
		Status:             service.SubscriptionStatusActive,
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	displayGroupID := int64(4242) // 仅用于 apiKey 内联展示(planName),不查 DB
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      1,
		Quota:   0, // 无总额度 + 无限流 → unrestricted 模式
		GroupID: &displayGroupID,
		Group:   &service.Group{ID: displayGroupID, Name: "三窗口卡"},
		User:    &service.User{ID: user.ID},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID})

	h.Usage(c)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.Bytes()
	require.Equal(t, "unrestricted", gjson.GetBytes(body, "mode").String())
	require.True(t, gjson.GetBytes(body, "isValid").Bool())
	require.InDelta(t, d, gjson.GetBytes(body, "subscription.daily_limit_usd").Float(), 1e-9)
	require.InDelta(t, dUse, gjson.GetBytes(body, "subscription.daily_usage_usd").Float(), 1e-9)
	require.InDelta(t, d-dUse, gjson.GetBytes(body, "subscription.daily_remaining_usd").Float(), 1e-9)
	require.InDelta(t, w, gjson.GetBytes(body, "subscription.weekly_limit_usd").Float(), 1e-9)
	require.InDelta(t, m, gjson.GetBytes(body, "subscription.monthly_limit_usd").Float(), 1e-9)
}
