//go:build unit

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthMarksExpectedIngressRejects(t *testing.T) {
	gin.SetMode(gin.TestMode)

	activeUser := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}

	tests := []struct {
		name       string
		target     string
		apiKey     *service.APIKey
		repoErr    error
		wantStatus int
		wantReason IngressRejectReason
		configure  func(*http.Request)
	}{
		{
			name:       "query key",
			target:     "/t?key=legacy",
			wantStatus: http.StatusBadRequest,
			wantReason: IngressRejectQueryAPIKeyDeprecated,
		},
		{
			name:       "missing key",
			target:     "/t",
			wantStatus: http.StatusUnauthorized,
			wantReason: IngressRejectAPIKeyRequired,
		},
		{
			name:       "invalid key",
			target:     "/t",
			repoErr:    service.ErrAPIKeyNotFound,
			wantStatus: http.StatusUnauthorized,
			wantReason: IngressRejectInvalidAPIKey,
			configure: func(req *http.Request) {
				req.Header.Set("x-api-key", "invalid-key")
			},
		},
		{
			name:   "disabled key",
			target: "/t",
			apiKey: &service.APIKey{
				ID: 100, Key: "disabled-key", Status: service.StatusAPIKeyDisabled, User: activeUser,
			},
			wantStatus: http.StatusUnauthorized,
			wantReason: IngressRejectAPIKeyDisabled,
			configure: func(req *http.Request) {
				req.Header.Set("x-api-key", "disabled-key")
			},
		},
		{
			name:   "inactive user",
			target: "/t",
			apiKey: &service.APIKey{
				ID: 101, Key: "inactive-user-key", Status: service.StatusActive,
				User: &service.User{ID: 8, Role: service.RoleUser, Status: service.StatusDisabled},
			},
			wantStatus: http.StatusUnauthorized,
			wantReason: IngressRejectUserInactive,
			configure: func(req *http.Request) {
				req.Header.Set("x-api-key", "inactive-user-key")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			repo := &stubApiKeyRepo{
				getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
					called = true
					if tt.repoErr != nil {
						return nil, tt.repoErr
					}
					return tt.apiKey, nil
				},
			}
			apiKeyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})
			r := gin.New()
			var reason IngressRejectReason
			r.Use(func(c *gin.Context) {
				c.Next()
				reason, _ = GetIngressRejectReason(c)
			})
			r.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, &config.Config{RunMode: config.RunModeSimple})))
			r.GET("/t", func(c *gin.Context) { c.Status(http.StatusOK) })

			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if tt.configure != nil {
				tt.configure(req)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code)
			require.Equal(t, tt.wantReason, reason)
			if tt.name == "query key" || tt.name == "missing key" {
				require.False(t, called, "authentication repository should not be queried")
			}
		})
	}
}

func TestAPIKeyAuthDoesNotMarkInternalValidationError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	apiKeyService := service.NewAPIKeyService(&stubApiKeyRepo{
		getByKey: func(context.Context, string) (*service.APIKey, error) {
			return nil, errors.New("database unavailable")
		},
	}, nil, nil, nil, nil, nil, &config.Config{})
	var marked bool
	r.Use(func(c *gin.Context) {
		c.Next()
		_, marked = GetIngressRejectReason(c)
	})
	r.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, &config.Config{RunMode: config.RunModeSimple})))
	r.GET("/t", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", "valid-looking-key")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.False(t, marked)
}
