//go:build integration

package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 支付只读端点(在售套餐列表 / 限额 / 结账信息)的真实 DB 端到端,覆盖 PaymentHandler 的
// GetPlans(含 group 平台富化)/GetLimits/GetCheckoutInfo + 驱动 PaymentConfigService。

func TestPaymentHandlerEndpoints_ReadOnlyPostgres(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	client := testEntClient(t)
	settingRepo := NewSettingRepository(client)
	require.NoError(t, settingRepo.Set(ctx, service.SettingPaymentEnabled, "true"))
	configSvc := service.NewPaymentConfigService(client, settingRepo, nil)
	h := userhandler.NewPaymentHandler(nil, configSvc)

	// 一个在售套餐(D>0)供 GetPlans 列出 + 平台富化。
	group := mustCreateGroup(t, client, &service.Group{Name: "pay-plans-" + uuid.NewString()})
	_, err := client.SubscriptionPlan.Create().
		SetGroupID(group.ID).
		SetName("visible-plan-" + uuid.NewString()).
		SetDailyAmountUsd(10).
		SetPrice(545).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetForSale(true).
		SetSortOrder(1).
		Save(ctx)
	require.NoError(t, err)

	call := func(method, path string, fn gin.HandlerFunc) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(method, path, nil)
		fn(c)
		return rec
	}

	// 在售套餐列表 → 200。
	rec := call(http.MethodGet, "/api/v1/payment/plans", h.GetPlans)
	require.Equal(t, http.StatusOK, rec.Code)

	// 可用方法限额 → 200。
	rec = call(http.MethodGet, "/api/v1/payment/limits", h.GetLimits)
	require.Equal(t, http.StatusOK, rec.Code)

	// 结账信息(限额 + 支付配置)→ 200。
	rec = call(http.MethodGet, "/api/v1/payment/checkout-info", h.GetCheckoutInfo)
	require.Equal(t, http.StatusOK, rec.Code)
}
