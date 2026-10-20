package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 用量列表里的 actual_cost 是站内额度，「扣了 5」既不是 5 美元也不是 5 元。
// 这组测试盯住 handler 这一层的装配：折算器是否按配置构造、每条记录是否都带上
// 法币口径、以及依赖缺失时会不会把整个列表接口拖垮。

type creditFiatSettingRepoStub struct {
	service.SettingRepository
	values map[string]string
}

func (r *creditFiatSettingRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if v, ok := r.values[key]; ok {
			out[key] = v
		}
	}
	return out, nil
}

func newCreditFiatSettingService(multiplier string) *service.SettingService {
	repo := &creditFiatSettingRepoStub{
		values: map[string]string{service.SettingBalanceRechargeMult: multiplier},
	}
	return service.NewSettingService(repo, &config.Config{})
}

func newCreditFiatContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)
	return c
}

// 没有 settingService 时倍率退化为 1，折算等于不折算——展示回到老口径，
// 而不是让请求失败或把金额算成 0。
func TestUsageHandler_CreditFiatRate_WithoutSettingService(t *testing.T) {
	h := NewUsageHandler(nil, nil, nil, nil)

	rate := h.creditFiatRate(newCreditFiatContext(), 7, nil)

	require.NotNil(t, rate)
	require.InDelta(t, 1.0, rate.FiatPerCredit(service.BillingTypeBalance, 0), 1e-9)
	require.InDelta(t, 5.0, rate.Convert(5, service.BillingTypeBalance, 0), 1e-9)
}

// 配了倍率就按倍率折算。usageService 为 nil 时走不查库的那条装配路径。
func TestUsageHandler_CreditFiatRate_UsesConfiguredMultiplier(t *testing.T) {
	h := NewUsageHandler(nil, nil, nil, newCreditFiatSettingService("13"))

	rate := h.creditFiatRate(newCreditFiatContext(), 7, nil)

	require.InDelta(t, 1.0/13.0, rate.FiatPerCredit(service.BillingTypeBalance, 0), 1e-9)
}

// 有 usageService 时交给它装配（它会去查这批记录涉及的订阅卡）。
// 这里 entClient 为 nil，卡查不到，订阅记录回落到钱包单价——关键是不能 panic。
func TestUsageHandler_CreditFiatRate_DelegatesToUsageService(t *testing.T) {
	usageSvc := service.NewUsageService(nil, nil, nil, nil)
	h := NewUsageHandler(usageSvc, nil, nil, newCreditFiatSettingService("13"))

	subID := int64(202)
	records := []service.UsageLog{
		{BillingType: service.BillingTypeSubscription, SubscriptionID: &subID, ActualCost: 5},
	}
	rate := h.creditFiatRate(newCreditFiatContext(), 7, records)

	require.InDelta(t, 1.0/13.0, rate.FiatPerCredit(service.BillingTypeSubscription, subID), 1e-9)
}

type creditFiatUsageRepoStub struct {
	service.UsageLogRepository
	logs []service.UsageLog
}

func (s *creditFiatUsageRepoStub) ListWithFilters(
	ctx context.Context,
	params pagination.PaginationParams,
	filters usagestats.UsageLogFilters,
) ([]service.UsageLog, *pagination.PaginationResult, error) {
	return s.logs, &pagination.PaginationResult{Total: int64(len(s.logs))}, nil
}

type creditFiatListResponse struct {
	Data struct {
		Items []struct {
			ActualCost    float64 `json:"actual_cost"`
			BillingType   int8    `json:"billing_type"`
			FiatCost      float64 `json:"fiat_cost"`
			FiatPerCredit float64 `json:"fiat_per_credit"`
		} `json:"items"`
	} `json:"data"`
}

// 列表里每一条都要带上法币口径，钱包记录和订阅记录都不能漏。
// 订阅卡查不到时回落到钱包单价，这条路径上两类记录的单价相同，
// 但字段本身必须存在——前端就是靠它显示「¥」的。
func TestUsageHandler_List_AttachesFiatFieldsToEveryRecord(t *testing.T) {
	subID := int64(202)
	repo := &creditFiatUsageRepoStub{logs: []service.UsageLog{
		{BillingType: service.BillingTypeBalance, ActualCost: 6.5},
		{BillingType: service.BillingTypeSubscription, SubscriptionID: &subID, ActualCost: 13},
	}}
	usageSvc := service.NewUsageService(repo, nil, nil, nil)
	h := NewUsageHandler(usageSvc, nil, nil, newCreditFiatSettingService("13"))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
		c.Next()
	})
	router.GET("/usage", h.List)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/usage", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var body creditFiatListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data.Items, 2)

	for _, item := range body.Data.Items {
		require.InDelta(t, 1.0/13.0, item.FiatPerCredit, 1e-9)
		require.InDelta(t, item.ActualCost/13.0, item.FiatCost, 1e-6)
	}
	// 13 个额度正好是 1 元——这就是用户该看到的锚点。
	require.InDelta(t, 1.0, body.Data.Items[1].FiatCost, 1e-6)
}
