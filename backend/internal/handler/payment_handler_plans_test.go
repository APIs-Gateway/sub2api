//go:build unit

package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

type paymentHandlerPlansSettingRepoStub struct{}

func (paymentHandlerPlansSettingRepoStub) Get(context.Context, string) (*service.Setting, error) {
	return nil, nil
}
func (paymentHandlerPlansSettingRepoStub) GetValue(context.Context, string) (string, error) {
	return "", nil
}
func (paymentHandlerPlansSettingRepoStub) Set(context.Context, string, string) error { return nil }
func (paymentHandlerPlansSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	return make(map[string]string, len(keys)), nil
}
func (paymentHandlerPlansSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}
func (paymentHandlerPlansSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (paymentHandlerPlansSettingRepoStub) Delete(context.Context, string) error { return nil }

func TestPaymentHandlerPlanEndpointsIncludeDisplayCurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()

	db, err := sql.Open("sqlite", "file:payment_handler_plan_currency?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	group, err := client.Group.Create().SetName("payment-plan-currency-group").SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	_, err = client.SubscriptionPlan.Create().
		SetGroupID(group.ID).
		SetName("payment-plan-currency").
		SetDailyAmountUsd(10).
		SetPrice(10).
		SetOriginalPrice(20).
		SetCurrency("USD").
		SetValidityDays(30).
		SetValidityUnit("day").
		SetForSale(true).
		Save(ctx)
	require.NoError(t, err)

	configSvc := service.NewPaymentConfigService(client, paymentHandlerPlansSettingRepoStub{}, nil)
	h := NewPaymentHandler(nil, configSvc)
	call := func(path string, fn gin.HandlerFunc) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, path, nil)
		fn(c)
		return recorder
	}

	plans := call("/api/v1/payment/plans", h.GetPlans)
	require.Equal(t, http.StatusOK, plans.Code)
	var plansResp struct {
		Code int `json:"code"`
		Data []struct {
			Currency string `json:"currency"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(plans.Body.Bytes(), &plansResp))
	require.Equal(t, 0, plansResp.Code)
	require.Len(t, plansResp.Data, 1)
	require.Equal(t, "USD", plansResp.Data[0].Currency)

	var checkoutResp struct {
		Code int `json:"code"`
		Data struct {
			Plans []struct {
				Currency string `json:"currency"`
			} `json:"plans"`
		} `json:"data"`
	}
	checkout := call("/api/v1/payment/checkout-info", h.GetCheckoutInfo)
	require.Equal(t, http.StatusOK, checkout.Code)
	require.NoError(t, json.Unmarshal(checkout.Body.Bytes(), &checkoutResp))
	require.Equal(t, 0, checkoutResp.Code)
	require.Len(t, checkoutResp.Data.Plans, 1)
	require.Equal(t, "USD", checkoutResp.Data.Plans[0].Currency)
}
