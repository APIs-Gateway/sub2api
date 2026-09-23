//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type providerPricingSettingRepoStub struct {
	values map[string]string
	err    error
}

func (s *providerPricingSettingRepoStub) Get(context.Context, string) (*service.Setting, error) {
	panic("unexpected Get call")
}

func (s *providerPricingSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *providerPricingSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (s *providerPricingSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = s.values[key]
	}
	return out, nil
}

func (s *providerPricingSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *providerPricingSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *providerPricingSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

type providerPricingGroupRepoStub struct {
	service.GroupRepository
	groups []service.Group
	err    error
}

func (s *providerPricingGroupRepoStub) ListActive(context.Context) ([]service.Group, error) {
	return s.groups, s.err
}

func TestProviderPricingHandlerGetPricing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := &providerPricingSettingRepoStub{values: map[string]string{
		service.SettingBalanceRechargeMult: "0.5",
		service.SettingKeySiteName:         "Codex API",
		service.SettingKeyFrontendURL:      "https://codex.example.com",
	}}
	pricingSvc := service.NewPricingService(nil, nil)

	settingSvc := service.NewSettingService(repo, &config.Config{})
	groupRepo := &providerPricingGroupRepoStub{groups: []service.Group{{Name: "codex  plus", RateMultiplier: 1.4}}}
	h := NewProviderPricingHandler(service.NewPaymentConfigService(nil, repo, nil), pricingSvc, settingSvc, groupRepo)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/provider/pricing", nil)

	h.GetPricing(c)

	require.Equal(t, http.StatusOK, recorder.Code)

	var resp service.HvoyProviderPricingResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, "Codex API", resp.Data.SiteName)
	require.Equal(t, "codex.example.com", resp.Data.SiteDomain)
	require.Len(t, resp.Data.Models, 7)
	for _, model := range resp.Data.Models {
		require.Equal(t, "codex plus", model.GroupName)
		require.True(t, model.Enabled)
		require.Empty(t, model.Note)
	}
	// official USD/M × 1.4 (group rate) ÷ 0.5 (recharge multiplier)
	require.Equal(t, "gpt-5.5", resp.Data.Models[0].ModelName)
	require.Equal(t, 7.0, resp.Data.Models[0].InputPrice)
	require.Equal(t, "gpt-5.6-sol", resp.Data.Models[1].ModelName)
	require.Equal(t, 14.0, resp.Data.Models[1].InputPrice)
	require.Equal(t, "gpt-5.6-terra", resp.Data.Models[2].ModelName)
	require.Equal(t, 5.6, resp.Data.Models[2].InputPrice)
	require.Equal(t, "gpt-5.6-luna", resp.Data.Models[3].ModelName)
	require.Equal(t, 0.56, resp.Data.Models[3].InputPrice)
	require.Equal(t, "gpt-6-astra", resp.Data.Models[4].ModelName)
	require.Equal(t, 28.0, resp.Data.Models[4].InputPrice)
	require.Equal(t, "gpt-6-sol", resp.Data.Models[5].ModelName)
	require.Equal(t, 5.6, resp.Data.Models[5].InputPrice)
	require.Equal(t, "gpt-6-luna", resp.Data.Models[6].ModelName)
	require.Equal(t, 0.28, resp.Data.Models[6].InputPrice)
}

func TestProviderPricingHandlerGetPricingGroupError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := &providerPricingSettingRepoStub{values: map[string]string{service.SettingBalanceRechargeMult: "1"}}
	h := NewProviderPricingHandler(
		service.NewPaymentConfigService(nil, repo, nil),
		service.NewPricingService(nil, nil),
		service.NewSettingService(repo, &config.Config{}),
		&providerPricingGroupRepoStub{err: errors.New("db down")},
	)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/provider/pricing", nil)

	h.GetPricing(c)

	var resp service.HvoyProviderPricingResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.False(t, resp.Success)
	require.Equal(t, "failed to load groups", resp.Message)
}

func TestProviderPricingHandlerGetPricingConfigError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := &providerPricingSettingRepoStub{err: errors.New("boom")}
	h := NewProviderPricingHandler(
		service.NewPaymentConfigService(nil, repo, nil),
		service.NewPricingService(nil, nil),
		service.NewSettingService(repo, &config.Config{}),
		&providerPricingGroupRepoStub{},
	)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/provider/pricing", nil)

	h.GetPricing(c)

	require.Equal(t, http.StatusOK, recorder.Code)

	var resp service.HvoyProviderPricingResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.False(t, resp.Success)
	require.Equal(t, service.HvoyProviderPricingSchemaVersion, resp.SchemaVersion)
	require.Equal(t, "failed to load payment config", resp.Message)
}
