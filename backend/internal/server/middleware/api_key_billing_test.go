//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthBillingInfoSkipsBillingEnforcementAndLastUsedTouch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	group := &service.Group{
		ID:       7,
		Status:   service.StatusActive,
		Platform: service.PlatformOpenAI,
		Hydrated: true,
	}
	user := &service.User{ID: 11, Role: service.RoleUser, Status: service.StatusActive, Concurrency: 2}

	for _, status := range []string{service.StatusAPIKeyExpired, service.StatusAPIKeyQuotaExhausted} {
		t.Run(status, func(t *testing.T) {
			key := &service.APIKey{ID: 19, UserID: user.ID, Key: "billing-key-" + status, Status: status, User: user, Group: group, GroupID: &group.ID}
			touchCalls := 0
			repo := &stubApiKeyRepo{
				getByKey: func(context.Context, string) (*service.APIKey, error) {
					clone := *key
					return &clone, nil
				},
				updateLastUsed: func(context.Context, int64, time.Time) error {
					touchCalls++
					return nil
				},
			}
			apiKeyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeStandard})
			router := newAuthTestRouterForPath("/v1/sub2api/billing", apiKeyService, nil, &config.Config{RunMode: config.RunModeStandard})

			request := httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil)
			request.Header.Set("x-api-key", key.Key)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Zero(t, touchCalls)
		})
	}
}

func TestAPIKeyAuthReadOnlyPathClassificationIncludesBillingInfo(t *testing.T) {
	require.True(t, isAPIKeyAuthReadOnlyUsagePath("/v1/sub2api/billing"))
	require.True(t, isAPIKeyBillingInfoPath("/v1/sub2api/billing"))
	require.False(t, isAPIKeyBillingInfoPath("/v1/usage"))
}
