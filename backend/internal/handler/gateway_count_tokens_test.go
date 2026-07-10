//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type countTokensModelAvailabilityAccountRepo struct {
	service.AccountRepository
	accounts []service.Account
}

type countTokensModelAvailabilityGroupRepo struct {
	service.GroupRepository
	group *service.Group
}

func (r countTokensModelAvailabilityGroupRepo) GetByID(context.Context, int64) (*service.Group, error) {
	return r.group, nil
}

func (r countTokensModelAvailabilityGroupRepo) GetByIDLite(context.Context, int64) (*service.Group, error) {
	return r.group, nil
}

func (r countTokensModelAvailabilityAccountRepo) ListSchedulableByPlatforms(_ context.Context, platforms []string) ([]service.Account, error) {
	return r.accountsForPlatforms(platforms, true), nil
}

func (r countTokensModelAvailabilityAccountRepo) ListSchedulableByGroupIDAndPlatforms(_ context.Context, _ int64, platforms []string) ([]service.Account, error) {
	return r.accountsForPlatforms(platforms, true), nil
}

func (r countTokensModelAvailabilityAccountRepo) ListByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatforms([]string{platform}, false), nil
}

func (r countTokensModelAvailabilityAccountRepo) accountsForPlatforms(platforms []string, schedulableOnly bool) []service.Account {
	out := make([]service.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if schedulableOnly && !account.Schedulable {
			continue
		}
		for _, platform := range platforms {
			if account.Platform == platform {
				out = append(out, account)
				break
			}
		}
	}
	return out
}

func TestGatewayHandlerCountTokens_ModelAvailabilityClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		model        string
		accounts     []service.Account
		wantStatus   int
		wantErrType  string
		wantCapacity bool
	}{
		{
			name:  "unsupported model returns not found without capacity mark",
			model: "claude-missing",
			accounts: []service.Account{{
				ID:          1,
				Platform:    service.PlatformAnthropic,
				Type:        service.AccountTypeOAuth,
				Status:      service.StatusActive,
				Schedulable: true,
				Credentials: map[string]any{"model_mapping": map[string]any{"claude-supported": "claude-supported"}},
			}},
			wantStatus:  http.StatusNotFound,
			wantErrType: "model_not_found",
		},
		{
			name:  "temporarily unavailable supported model stays service unavailable",
			model: "claude-supported",
			accounts: []service.Account{{
				ID:          2,
				Platform:    service.PlatformAnthropic,
				Type:        service.AccountTypeOAuth,
				Status:      service.StatusActive,
				Schedulable: false,
				Credentials: map[string]any{"model_mapping": map[string]any{"claude-supported": "claude-supported"}},
			}},
			wantStatus:   http.StatusServiceUnavailable,
			wantErrType:  "api_error",
			wantCapacity: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newCountTokensModelAvailabilityHandler(t, tt.accounts)
			groupID := int64(13)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(
				http.MethodPost,
				"/v1/messages/count_tokens",
				strings.NewReader(`{"model":"`+tt.model+`","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`),
			)
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
				ID:      11,
				GroupID: &groupID,
				Group:   &service.Group{ID: 13, Platform: service.PlatformAnthropic},
				User:    &service.User{ID: 12},
			})
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 12})
			handler.CountTokens(c)

			require.Equal(t, tt.wantStatus, rec.Code)
			require.Equal(t, "error", gjson.GetBytes(rec.Body.Bytes(), "type").String())
			require.Equal(t, tt.wantErrType, gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			require.Equal(t, tt.wantCapacity, isOpsRoutingCapacityLimited(c))
		})
	}
}

func newCountTokensModelAvailabilityHandler(t *testing.T, accounts []service.Account) *GatewayHandler {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	accountRepo := countTokensModelAvailabilityAccountRepo{accounts: accounts}
	groupRepo := countTokensModelAvailabilityGroupRepo{group: &service.Group{ID: 13, Platform: service.PlatformAnthropic}}
	gatewayService := service.NewGatewayService(
		accountRepo,
		groupRepo, nil, nil, nil, nil, nil, nil,
		cfg,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billingService.Stop)

	return NewGatewayHandler(
		gatewayService,
		nil, nil, nil, nil, nil,
		billingService,
		nil, nil, nil, nil, nil, nil,
		cfg,
		nil,
	)
}
