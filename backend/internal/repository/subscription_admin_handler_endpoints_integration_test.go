//go:build integration

package repository

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// admin 订阅管理 handler 的真实 DB 端到端:Assign(单发)/BulkAssign(批量)/ResetQuota(三窗口清零)/
// Revoke(吊销)。覆盖 handler 的请求绑定 + DailyAmountUSD 字段映射 + 调 SubscriptionService 写库。
// 文件名以 subscription_ 开头(排在 group_repo 之后)→ mustCreateGroup 建的 active 组不污染 TestListActiveByPlatform。

// adminSubCtx 造一个带 admin 鉴权主体 + 可选 JSON body / :id 参数的 gin 测试上下文。
func adminSubCtx(t *testing.T, adminID int64, body any, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/subscriptions", rdr)
	c.Request.Header.Set("Content-Type", "application/json")
	if adminID > 0 {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: adminID})
	}
	c.Params = params
	return c, rec
}

func TestAdminSubscriptionHandler_AssignBulkResetRevokePostgres(t *testing.T) {
	client := testEntClient(t)
	h := adminhandler.NewSubscriptionHandler(makeSubscriptionService(t))

	admin := mustCreateUser(t, client, &service.User{Email: "admin-sub-" + uuid.NewString() + "@example.com"})
	group := mustCreateGroup(t, client, &service.Group{Name: "admin-sub-" + uuid.NewString()})

	// Assign:给单用户发卡(显式 DailyAmountUSD → 覆盖 152-157 字段映射)。
	u1 := mustCreateUser(t, client, &service.User{Email: "admin-assign-" + uuid.NewString() + "@example.com"})
	c, rec := adminSubCtx(t, admin.ID, map[string]any{
		"user_id":          u1.ID,
		"group_id":         group.ID,
		"validity_days":    30,
		"daily_amount_usd": 10.0,
		"notes":            "assign-test",
	}, nil)
	h.Assign(c)
	require.Equalf(t, http.StatusOK, rec.Code, "Assign body=%s", rec.Body.String())
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, u1.ID, service.SubscriptionStatusActive))

	// BulkAssign:给多用户批量发卡(覆盖 180-185 字段映射)。
	u2 := mustCreateUser(t, client, &service.User{Email: "admin-bulk2-" + uuid.NewString() + "@example.com"})
	u3 := mustCreateUser(t, client, &service.User{Email: "admin-bulk3-" + uuid.NewString() + "@example.com"})
	c, rec = adminSubCtx(t, admin.ID, map[string]any{
		"user_ids":         []int64{u2.ID, u3.ID},
		"group_id":         group.ID,
		"validity_days":    30,
		"daily_amount_usd": 10.0,
		"notes":            "bulk-test",
	}, nil)
	h.BulkAssign(c)
	require.Equalf(t, http.StatusOK, rec.Code, "BulkAssign body=%s", rec.Body.String())
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, u2.ID, service.SubscriptionStatusActive))
	require.Equal(t, 1, countUserSubscriptionsByStatus(t, u3.ID, service.SubscriptionStatusActive))

	// ResetQuota:对已发卡用户清零三窗口用量 → 200。
	u4 := mustCreateUser(t, client, &service.User{Email: "admin-reset-" + uuid.NewString() + "@example.com"})
	today := service.TodayEastDayNumber()
	d := 10.0
	w, m := service.DeriveWindowCaps(d, 30)
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          u4.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  d,
		DailyLimitUSD:   &d,
		WeeklyLimitUSD:  &w,
		MonthlyLimitUSD: &m,
		DailyUsageUSD:   7,
		TodayRemaining:  d,
		TodayDay:        today,
		StartDay:        today - 1,
		ExpireDay:       today + 20,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 20),
		Status:          service.SubscriptionStatusActive,
	})
	c, rec = adminSubCtx(t, admin.ID, map[string]any{"daily": true, "weekly": true, "monthly": true},
		gin.Params{{Key: "id", Value: strconv.FormatInt(card.ID, 10)}})
	h.ResetQuota(c)
	require.Equalf(t, http.StatusOK, rec.Code, "ResetQuota body=%s", rec.Body.String())

	// Revoke:吊销该卡 → 200,卡不再 active。
	c, rec = adminSubCtx(t, admin.ID, nil, gin.Params{{Key: "id", Value: strconv.FormatInt(card.ID, 10)}})
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/subscriptions/"+strconv.FormatInt(card.ID, 10), nil)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: admin.ID})
	h.Revoke(c)
	require.Equalf(t, http.StatusOK, rec.Code, "Revoke body=%s", rec.Body.String())
	require.Equal(t, 0, countUserSubscriptionsByStatus(t, u4.ID, service.SubscriptionStatusActive))
}

// Assign 缺必填字段(无 user_id/group_id)→ 400(绑定失败分支)。
func TestAdminSubscriptionHandler_AssignBadRequestPostgres(t *testing.T) {
	h := adminhandler.NewSubscriptionHandler(makeSubscriptionService(t))
	c, rec := adminSubCtx(t, 1, map[string]any{"validity_days": 30}, nil)
	h.Assign(c)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
