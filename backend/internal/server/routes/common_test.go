//go:build unit

package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type commonRoutesSettingRepoStub struct {
	values map[string]string
}

func (s *commonRoutesSettingRepoStub) Get(context.Context, string) (*service.Setting, error) {
	panic("unexpected Get call")
}

func (s *commonRoutesSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *commonRoutesSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (s *commonRoutesSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = s.values[key]
	}
	return out, nil
}

func (s *commonRoutesSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *commonRoutesSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *commonRoutesSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func TestCommonRoutesRegistersProviderPricing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := &commonRoutesSettingRepoStub{values: map[string]string{
		service.SettingBalanceRechargeMult: "1",
		service.SettingKeySiteName:         "Codex API",
	}}
	settingSvc := service.NewSettingService(repo, &config.Config{})
	router := gin.New()
	RegisterCommonRoutes(router, &handler.Handlers{
		ProviderPricing: handler.NewProviderPricingHandler(
			service.NewPaymentConfigService(nil, repo, nil),
			service.NewPricingService(nil, nil),
			settingSvc,
		),
	})

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/provider/pricing", nil))

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"schema_version":"1.1"`)
	require.Contains(t, w.Body.String(), `"site_name":"Codex API"`)
}
