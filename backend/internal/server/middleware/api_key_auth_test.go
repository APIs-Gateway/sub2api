//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSimpleModeBypassesQuotaCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)

	limit := 1.0
	group := &service.Group{
		ID:               42,
		Name:             "sub",
		Status:           service.StatusActive,
		Hydrated:         true,
		SubscriptionType: service.SubscriptionTypeSubscription,
		DailyLimitUSD:    &limit,
	}
	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     100,
		UserID: user.ID,
		Key:    "test-key",
		Status: service.StatusActive,
		User:   user,
		Group:  group,
	}
	apiKey.GroupID = &group.ID

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	t.Run("standard_mode_subscription_group_allowed_by_balance", func(t *testing.T) {
		// 钱包仍可在订阅窗口耗尽时兜底放行。
		cfg := &config.Config{RunMode: config.RunModeStandard}
		apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
		subscriptionService := service.NewSubscriptionService(nil, &stubUserSubscriptionRepo{}, nil, nil, nil, nil, nil, cfg)
		t.Cleanup(subscriptionService.Stop)

		router := newAuthTestRouter(apiKeyService, subscriptionService, cfg)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("x-api-key", apiKey.Key)
		router.ServeHTTP(w, req)

		// user.Balance = 10 > 0 → 放行
		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("standard_mode_revalidates_cas_loser_from_database", func(t *testing.T) {
		cfg := &config.Config{RunMode: config.RunModeStandard}
		zeroBalanceUser := *user
		zeroBalanceUser.Balance = 0
		zeroKey := *apiKey
		zeroKey.User = &zeroBalanceUser
		zeroKey.Key = "stale-window-key"
		zeroRepo := &stubApiKeyRepo{
			getByKey: func(context.Context, string) (*service.APIKey, error) {
				clone := zeroKey
				return &clone, nil
			},
		}
		apiKeyService := service.NewAPIKeyService(zeroRepo, nil, nil, nil, nil, nil, cfg)

		limit := 1.0
		oldWindowStart := time.Now().Add(-48 * time.Hour)
		currentWindowStart := time.Now()
		stale := &service.UserSubscription{
			ID:               56,
			UserID:           zeroBalanceUser.ID,
			GroupID:          group.ID,
			Status:           service.SubscriptionStatusActive,
			StartsAt:         time.Now().Add(-72 * time.Hour),
			ExpiresAt:        time.Now().Add(72 * time.Hour),
			DailyLimitUSD:    &limit,
			DailyWindowStart: &oldWindowStart,
			DailyUsageUSD:    limit,
		}
		fresh := *stale
		fresh.DailyWindowStart = &currentWindowStart
		fresh.DailyUsageUSD = limit
		subscriptionRepo := &stubUserSubscriptionRepo{
			getActive: func(context.Context, int64, int64) (*service.UserSubscription, error) {
				clone := *stale
				return &clone, nil
			},
			getByID: func(context.Context, int64) (*service.UserSubscription, error) {
				clone := fresh
				return &clone, nil
			},
			resetDaily: func(context.Context, int64, time.Time) error {
				return nil // another request already advanced the window
			},
		}
		subscriptionService := service.NewSubscriptionService(nil, subscriptionRepo, nil, nil, nil, nil, nil, cfg)
		t.Cleanup(subscriptionService.Stop)
		router := newAuthTestRouter(apiKeyService, subscriptionService, cfg)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("x-api-key", zeroKey.Key)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusTooManyRequests, w.Code)
		requireAPIKeyAuthError(t, w, "USAGE_LIMIT_EXCEEDED", service.ErrDailyLimitExceeded.Error())
	})

	t.Run("simple_mode_bypasses_quota_check", func(t *testing.T) {
		cfg := &config.Config{RunMode: config.RunModeSimple}
		apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
		subscriptionService := service.NewSubscriptionService(nil, &stubUserSubscriptionRepo{}, nil, nil, nil, nil, nil, cfg)
		router := newAuthTestRouter(apiKeyService, subscriptionService, cfg)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("x-api-key", apiKey.Key)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("simple_mode_accepts_lowercase_bearer", func(t *testing.T) {
		cfg := &config.Config{RunMode: config.RunModeSimple}
		apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
		subscriptionService := service.NewSubscriptionService(nil, &stubUserSubscriptionRepo{}, nil, nil, nil, nil, nil, cfg)
		router := newAuthTestRouter(apiKeyService, subscriptionService, cfg)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("Authorization", "bearer "+apiKey.Key)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("standard_mode_zero_balance_passes_middleware", func(t *testing.T) {
		// per-day：中间件不再按 user.Balance<=0 早拒（套餐额度在卡 today_remaining、不在钱包）。
		// 0 余额请求放行到 handler，余额/准入由 handler 的 CheckBillingEligibility(per-day Admit) 判定。
		// 本测试路由无 handler 级准入，故 0 余额请求应放行（200），而非中间件 403。
		cfg := &config.Config{RunMode: config.RunModeStandard}

		zeroBalanceUser := *user
		zeroBalanceUser.Balance = 0
		zeroKey := *apiKey
		zeroKey.User = &zeroBalanceUser
		zeroKey.Key = "zero-balance-key"
		zeroRepo := &stubApiKeyRepo{
			getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
				if key != zeroKey.Key {
					return nil, service.ErrAPIKeyNotFound
				}
				clone := zeroKey
				return &clone, nil
			},
		}
		apiKeyService := service.NewAPIKeyService(zeroRepo, nil, nil, nil, nil, nil, cfg)
		subscriptionService := service.NewSubscriptionService(nil, &stubUserSubscriptionRepo{}, nil, nil, nil, nil, nil, cfg)
		router := newAuthTestRouter(apiKeyService, subscriptionService, cfg)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("x-api-key", zeroKey.Key)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
	})
}

func TestAPIKeyAuthReadOnlyWhamUsageSkipsBillingEnforcement(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     0,
		Concurrency: 3,
	}

	tests := []struct {
		name       string
		path       string
		status     string
		wantStatus int
	}{
		{
			name:       "quota exhausted key can read wham usage",
			path:       "/backend-api/wham/usage",
			status:     service.StatusAPIKeyQuotaExhausted,
			wantStatus: http.StatusOK,
		},
		{
			name:       "quota exhausted key can read codex wham usage",
			path:       "/backend-api/codex/wham/usage",
			status:     service.StatusAPIKeyQuotaExhausted,
			wantStatus: http.StatusOK,
		},
		{
			name:       "expired key can read wham usage",
			path:       "/backend-api/wham/usage",
			status:     service.StatusAPIKeyExpired,
			wantStatus: http.StatusOK,
		},
		{
			name:       "expired key can read codex wham usage",
			path:       "/backend-api/codex/wham/usage",
			status:     service.StatusAPIKeyExpired,
			wantStatus: http.StatusOK,
		},
		{
			name:       "disabled key is still rejected",
			path:       "/backend-api/wham/usage",
			status:     service.StatusAPIKeyDisabled,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiKey := &service.APIKey{
				ID:     100,
				UserID: user.ID,
				Key:    "test-key",
				Status: tt.status,
				User:   user,
			}
			apiKeyRepo := &stubApiKeyRepo{
				getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
					if key != apiKey.Key {
						return nil, service.ErrAPIKeyNotFound
					}
					clone := *apiKey
					return &clone, nil
				},
			}

			cfg := &config.Config{RunMode: config.RunModeStandard}
			apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
			router := newAuthTestRouterForPath(tt.path, apiKeyService, nil, cfg)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("x-api-key", apiKey.Key)
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code)
			if tt.status == service.StatusAPIKeyDisabled {
				require.Contains(t, w.Body.String(), "API_KEY_DISABLED")
			}
		})
	}
}

func TestAPIKeyAuthSetsGroupContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	group := &service.Group{
		ID:       101,
		Name:     "g1",
		Status:   service.StatusActive,
		Platform: service.PlatformAnthropic,
		Hydrated: true,
	}
	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     100,
		UserID: user.ID,
		Key:    "test-key",
		Status: service.StatusActive,
		User:   user,
		Group:  group,
	}
	apiKey.GroupID = &group.ID

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, cfg)))
	router.GET("/t", func(c *gin.Context) {
		groupFromCtx, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group)
		if !ok || groupFromCtx == nil || groupFromCtx.ID != group.ID {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestAPIKeyAuthRejectsExclusiveGroupWhenUserNoLongerAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)

	group := &service.Group{
		ID:          202,
		Name:        "exclusive",
		Status:      service.StatusActive,
		IsExclusive: true,
		Hydrated:    true,
	}
	user := &service.User{
		ID:            7,
		Role:          service.RoleUser,
		Status:        service.StatusActive,
		Balance:       10,
		Concurrency:   3,
		AllowedGroups: []int64{},
	}
	apiKey := &service.APIKey{
		ID:     100,
		UserID: user.ID,
		Key:    "test-key",
		Status: service.StatusActive,
		User:   user,
		Group:  group,
	}
	apiKey.GroupID = &group.ID

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := newAuthTestRouter(apiKeyService, nil, cfg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "GROUP_NOT_ALLOWED")
}

func TestAPIKeyAuthOverwritesInvalidContextGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	group := &service.Group{
		ID:       101,
		Name:     "g1",
		Status:   service.StatusActive,
		Platform: service.PlatformAnthropic,
		Hydrated: true,
	}
	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     100,
		UserID: user.ID,
		Key:    "test-key",
		Status: service.StatusActive,
		User:   user,
		Group:  group,
	}
	apiKey.GroupID = &group.ID

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, cfg)))

	invalidGroup := &service.Group{
		ID:       group.ID,
		Platform: group.Platform,
		Status:   group.Status,
	}
	router.GET("/t", func(c *gin.Context) {
		groupFromCtx, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group)
		if !ok || groupFromCtx == nil || groupFromCtx.ID != group.ID || !groupFromCtx.Hydrated || groupFromCtx == invalidGroup {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, invalidGroup))
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestAPIKeyAuthRejectsUnavailableGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(101)
	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}

	tests := []struct {
		name       string
		group      *service.Group
		wantStatus int
		wantCode   string
		wantMarked bool
	}{
		{
			name: "active group passes",
			group: &service.Group{
				ID:       groupID,
				Name:     "active",
				Status:   service.StatusActive,
				Platform: service.PlatformAnthropic,
				Hydrated: true,
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "disabled group is forbidden",
			group: &service.Group{
				ID:       groupID,
				Name:     "disabled",
				Status:   service.StatusDisabled,
				Platform: service.PlatformAnthropic,
				Hydrated: true,
			},
			wantStatus: http.StatusForbidden,
			wantCode:   "GROUP_DISABLED",
			wantMarked: true,
		},
		{
			name: "deleted status group is forbidden",
			group: &service.Group{
				ID:       groupID,
				Name:     "deleted",
				Status:   "deleted",
				Platform: service.PlatformAnthropic,
				Hydrated: true,
			},
			wantStatus: http.StatusForbidden,
			wantCode:   "GROUP_DELETED",
			wantMarked: true,
		},
		{
			name:       "missing group edge is forbidden",
			group:      nil,
			wantStatus: http.StatusForbidden,
			wantCode:   "GROUP_DELETED",
			wantMarked: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiKey := &service.APIKey{
				ID:      100,
				UserID:  user.ID,
				GroupID: &groupID,
				Key:     "test-key",
				Status:  service.StatusActive,
				User:    user,
				Group:   tt.group,
			}
			apiKeyRepo := &stubApiKeyRepo{
				getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
					if key != apiKey.Key {
						return nil, service.ErrAPIKeyNotFound
					}
					clone := *apiKey
					return &clone, nil
				},
			}
			cfg := &config.Config{RunMode: config.RunModeStandard}
			apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
			router := gin.New()
			var markedBusinessLimited bool
			var businessLimitedReason string
			router.Use(func(c *gin.Context) {
				c.Next()
				markedBusinessLimited = service.HasOpsClientBusinessLimited(c)
				if v, ok := c.Get(service.OpsClientBusinessLimitedReasonKey); ok {
					businessLimitedReason, _ = v.(string)
				}
			})
			router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, cfg)))
			router.GET("/t", func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/t", nil)
			req.Header.Set("x-api-key", apiKey.Key)
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code)
			if tt.wantCode != "" {
				require.Contains(t, w.Body.String(), tt.wantCode)
			}
			require.Equal(t, tt.wantMarked, markedBusinessLimited)
			if tt.wantMarked {
				require.Equal(t, service.OpsClientBusinessLimitedReasonAPIKeyGroupUnavailable, businessLimitedReason)
			}
		})
	}
}

func TestAPIKeyAuthSetsOpsFallbackKeyOnEarlyAbort(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(101)
	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:      100,
		UserID:  user.ID,
		GroupID: &groupID,
		Key:     "test-key",
		Status:  service.StatusActive,
		User:    user,
		Group: &service.Group{
			ID:       groupID,
			Name:     "disabled",
			Status:   service.StatusDisabled,
			Platform: service.PlatformAnthropic,
			Hydrated: true,
		},
	}
	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)

	router := gin.New()
	var fallback *service.APIKey
	var fallbackOK bool
	router.Use(func(c *gin.Context) {
		c.Next()
		fallback, fallbackOK = GetOpsFallbackAPIKey(c)
	})
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, cfg)))
	router.GET("/t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	// 分组停用 → 早退中断，但 ops fallback key 仍应写入，含 user/group/platform。
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "GROUP_DISABLED")
	require.True(t, fallbackOK, "鉴权早退时也应写入 ops fallback api key")
	require.NotNil(t, fallback)
	require.Equal(t, apiKey.ID, fallback.ID)
	require.NotNil(t, fallback.User)
	require.Equal(t, user.ID, fallback.User.ID)
	require.NotNil(t, fallback.GroupID)
	require.Equal(t, groupID, *fallback.GroupID)
	require.NotNil(t, fallback.Group)
	require.Equal(t, service.PlatformAnthropic, fallback.Group.Platform)
}

func TestAPIKeyAuthGoogleSetsOpsFallbackKeyOnEarlyAbort(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(202)
	user := &service.User{
		ID:          9,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:      200,
		UserID:  user.ID,
		GroupID: &groupID,
		Key:     "g-key",
		Status:  service.StatusActive,
		User:    user,
		Group: &service.Group{
			ID:       groupID,
			Name:     "disabled",
			Status:   service.StatusDisabled,
			Platform: service.PlatformGemini,
			Hydrated: true,
		},
	}
	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)

	router := gin.New()
	var fallback *service.APIKey
	var fallbackOK bool
	router.Use(func(c *gin.Context) {
		c.Next()
		fallback, fallbackOK = GetOpsFallbackAPIKey(c)
	})
	router.Use(gin.HandlerFunc(APIKeyAuthWithSubscriptionGoogle(apiKeyService, nil, cfg)))
	router.GET("/t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-goog-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.True(t, fallbackOK, "Google 鉴权早退时也应写入 ops fallback api key")
	require.NotNil(t, fallback)
	require.Equal(t, apiKey.ID, fallback.ID)
	require.NotNil(t, fallback.User)
	require.Equal(t, user.ID, fallback.User.ID)
}

func TestRequireGroupAssignmentMarksUngroupedKeyBusinessLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)

	settingService := service.NewSettingService(fakeSettingRepo{
		values: map[string]string{
			service.SettingKeyAllowUngroupedKeyScheduling: "false",
		},
	}, &config.Config{})
	apiKey := &service.APIKey{
		ID:     100,
		Key:    "ungrouped-key",
		Status: service.StatusActive,
	}

	router := gin.New()
	var markedBusinessLimited bool
	var businessLimitedReason string
	router.Use(func(c *gin.Context) {
		c.Next()
		markedBusinessLimited = service.HasOpsClientBusinessLimited(c)
		if v, ok := c.Get(service.OpsClientBusinessLimitedReasonKey); ok {
			businessLimitedReason, _ = v.(string)
		}
	})
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), apiKey)
		c.Next()
	})
	router.Use(RequireGroupAssignment(settingService, AnthropicErrorWriter))
	router.GET("/t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "not assigned to any group")
	require.True(t, markedBusinessLimited)
	require.Equal(t, service.OpsClientBusinessLimitedReasonAPIKeyGroupUnassigned, businessLimitedReason)
}

func TestAPIKeyAuthIPRestrictionDoesNotTrustForwardedClientIPByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:          100,
		UserID:      user.ID,
		Key:         "test-key",
		Status:      service.StatusActive,
		User:        user,
		IPWhitelist: []string{"1.2.3.4"},
	}

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	var markedBusinessLimited bool
	var businessLimitedReason string
	router.Use(func(c *gin.Context) {
		c.Next()
		markedBusinessLimited = service.HasOpsClientBusinessLimited(c)
		if v, ok := c.Get(service.OpsClientBusinessLimitedReasonKey); ok {
			businessLimitedReason, _ = v.(string)
		}
	})
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, cfg)))
	router.GET("/t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.RemoteAddr = "9.9.9.9:12345"
	req.Header.Set("x-api-key", apiKey.Key)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Real-IP", "1.2.3.4")
	req.Header.Set("CF-Connecting-IP", "1.2.3.4")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	requireAPIKeyAuthError(t, w, "ACCESS_DENIED", "Access denied. Your IP is 9.9.9.9")
	require.True(t, markedBusinessLimited)
	require.Equal(t, service.OpsClientBusinessLimitedReasonIPRestriction, businessLimitedReason)
}

func TestAPIKeyAuthIPRestrictionIncludesClientIPForBlacklistDenial(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:          100,
		UserID:      user.ID,
		Key:         "test-key",
		Status:      service.StatusActive,
		User:        user,
		IPBlacklist: []string{"9.9.9.9"},
	}

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, cfg)))
	router.GET("/t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.RemoteAddr = "9.9.9.9:12345"
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	requireAPIKeyAuthError(t, w, "ACCESS_DENIED", "Access denied. Your IP is 9.9.9.9")
}

func TestAPIKeyAuthIPRestrictionCanTrustForwardedClientIPForReverseProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:          100,
		UserID:      user.ID,
		Key:         "test-key",
		Status:      service.StatusActive,
		User:        user,
		IPWhitelist: []string{"1.2.3.4"},
	}

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.SetTrustForwardedIPForAPIKeyACL(true)
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, cfg)))
	router.GET("/t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.RemoteAddr = "9.9.9.9:12345"
	req.Header.Set("x-api-key", apiKey.Key)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Real-IP", "1.2.3.4")
	req.Header.Set("CF-Connecting-IP", "1.2.3.4")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestAPIKeyAuthIPRestrictionUsesForwardedClientIPInDenialWhenTrusted(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:          100,
		UserID:      user.ID,
		Key:         "test-key",
		Status:      service.StatusActive,
		User:        user,
		IPWhitelist: []string{"9.9.9.9"},
	}

	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.SetTrustForwardedIPForAPIKeyACL(true)
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, nil, cfg)))
	router.GET("/t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.RemoteAddr = "9.9.9.9:12345"
	req.Header.Set("x-api-key", apiKey.Key)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Real-IP", "1.2.3.4")
	req.Header.Set("CF-Connecting-IP", "1.2.3.4")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	requireAPIKeyAuthError(t, w, "ACCESS_DENIED", "Access denied. Your IP is 1.2.3.4")
}

func TestAPIKeyAuthTouchesLastUsedOnSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     100,
		UserID: user.ID,
		Key:    "touch-ok",
		Status: service.StatusActive,
		User:   user,
	}

	var touchedID int64
	var touchedAt time.Time
	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
		updateLastUsed: func(ctx context.Context, id int64, usedAt time.Time) error {
			touchedID = id
			touchedAt = usedAt
			return nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := newAuthTestRouter(apiKeyService, nil, cfg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, apiKey.ID, touchedID)
	require.False(t, touchedAt.IsZero(), "expected touch timestamp")
}

func TestAPIKeyAuthTouchLastUsedFailureDoesNotBlock(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          8,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     101,
		UserID: user.ID,
		Key:    "touch-fail",
		Status: service.StatusActive,
		User:   user,
	}

	touchCalls := 0
	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
		updateLastUsed: func(ctx context.Context, id int64, usedAt time.Time) error {
			touchCalls++
			return errors.New("db unavailable")
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := newAuthTestRouter(apiKeyService, nil, cfg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "touch failure should not block request")
	require.Equal(t, 1, touchCalls)
}

func TestAPIKeyAuthTouchesLastUsedInStandardMode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{
		ID:          9,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     102,
		UserID: user.ID,
		Key:    "touch-standard",
		Status: service.StatusActive,
		User:   user,
	}

	touchCalls := 0
	apiKeyRepo := &stubApiKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
		updateLastUsed: func(ctx context.Context, id int64, usedAt time.Time) error {
			touchCalls++
			return nil
		},
	}

	cfg := &config.Config{RunMode: config.RunModeStandard}
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
	router := newAuthTestRouter(apiKeyService, nil, cfg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, touchCalls)
}

func newAuthTestRouter(apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService, cfg *config.Config) *gin.Engine {
	return newAuthTestRouterForPath("/t", apiKeyService, subscriptionService, cfg)
}

func newAuthTestRouterForPath(path string, apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService, cfg *config.Config) *gin.Engine {
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, subscriptionService, nil, cfg)))
	router.GET(path, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

func requireAPIKeyAuthError(t *testing.T, w *httptest.ResponseRecorder, code, message string) {
	t.Helper()

	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, code, resp.Code)
	require.Equal(t, message, resp.Message)
}

type stubApiKeyRepo struct {
	getByKey       func(ctx context.Context, key string) (*service.APIKey, error)
	updateLastUsed func(ctx context.Context, id int64, usedAt time.Time) error
}

func (r *stubApiKeyRepo) Create(ctx context.Context, key *service.APIKey) error {
	return errors.New("not implemented")
}

func (r *stubApiKeyRepo) GetByID(ctx context.Context, id int64) (*service.APIKey, error) {
	return nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) GetKeyAndOwnerID(ctx context.Context, id int64) (string, int64, error) {
	return "", 0, errors.New("not implemented")
}

func (r *stubApiKeyRepo) GetByKey(ctx context.Context, key string) (*service.APIKey, error) {
	if r.getByKey != nil {
		return r.getByKey(ctx, key)
	}
	return nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) GetByKeyForAuth(ctx context.Context, key string) (*service.APIKey, error) {
	return r.GetByKey(ctx, key)
}

func (r *stubApiKeyRepo) Update(ctx context.Context, key *service.APIKey) error {
	return errors.New("not implemented")
}

func (r *stubApiKeyRepo) Delete(ctx context.Context, id int64) error {
	return errors.New("not implemented")
}

func (r *stubApiKeyRepo) DeleteWithAudit(ctx context.Context, id int64) error {
	return errors.New("not implemented")
}

func (r *stubApiKeyRepo) ListByUserID(ctx context.Context, userID int64, params pagination.PaginationParams, _ service.APIKeyListFilters) ([]service.APIKey, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) VerifyOwnership(ctx context.Context, userID int64, apiKeyIDs []int64) ([]int64, error) {
	return nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) CountByUserID(ctx context.Context, userID int64) (int64, error) {
	return 0, errors.New("not implemented")
}

func (r *stubApiKeyRepo) ExistsByKey(ctx context.Context, key string) (bool, error) {
	return false, errors.New("not implemented")
}

func (r *stubApiKeyRepo) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]service.APIKey, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) SearchAPIKeys(ctx context.Context, userID int64, keyword string, limit int) ([]service.APIKey, error) {
	return nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) ClearGroupIDByGroupID(ctx context.Context, groupID int64) (int64, error) {
	return 0, errors.New("not implemented")
}

func (r *stubApiKeyRepo) UpdateGroupIDByUserAndGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (int64, error) {
	return 0, errors.New("not implemented")
}

func (r *stubApiKeyRepo) CountByGroupID(ctx context.Context, groupID int64) (int64, error) {
	return 0, errors.New("not implemented")
}

func (r *stubApiKeyRepo) ListKeysByUserID(ctx context.Context, userID int64) ([]string, error) {
	return nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) ListKeysByGroupID(ctx context.Context, groupID int64) ([]string, error) {
	return nil, errors.New("not implemented")
}

func (r *stubApiKeyRepo) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) (float64, error) {
	return 0, errors.New("not implemented")
}

func (r *stubApiKeyRepo) UpdateLastUsed(ctx context.Context, id int64, usedAt time.Time) error {
	if r.updateLastUsed != nil {
		return r.updateLastUsed(ctx, id, usedAt)
	}
	return nil
}

func (r *stubApiKeyRepo) IncrementRateLimitUsage(ctx context.Context, id int64, cost float64) error {
	return nil
}
func (r *stubApiKeyRepo) ResetRateLimitWindows(ctx context.Context, id int64) error {
	return nil
}
func (r *stubApiKeyRepo) GetRateLimitData(ctx context.Context, id int64) (*service.APIKeyRateLimitData, error) {
	return nil, nil
}

type stubUserSubscriptionRepo struct {
	getByID        func(ctx context.Context, id int64) (*service.UserSubscription, error)
	getActive      func(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error)
	updateStatus   func(ctx context.Context, subscriptionID int64, status string) error
	activateWindow func(ctx context.Context, id int64, start time.Time) error
	resetDaily     func(ctx context.Context, id int64, start time.Time) error
	resetWeekly    func(ctx context.Context, id int64, start time.Time) error
	resetMonthly   func(ctx context.Context, id int64, start time.Time) error
}

type fakeSettingRepo struct {
	values map[string]string
}

func (r fakeSettingRepo) Get(ctx context.Context, key string) (*service.Setting, error) {
	return nil, errors.New("not implemented")
}

func (r fakeSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}

func (r fakeSettingRepo) Set(ctx context.Context, key, value string) error {
	return errors.New("not implemented")
}

func (r fakeSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	return nil, errors.New("not implemented")
}

func (r fakeSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	return errors.New("not implemented")
}

func (r fakeSettingRepo) GetAll(ctx context.Context) (map[string]string, error) {
	return nil, errors.New("not implemented")
}

func (r fakeSettingRepo) Delete(ctx context.Context, key string) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) Create(ctx context.Context, sub *service.UserSubscription) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) GetByID(ctx context.Context, id int64) (*service.UserSubscription, error) {
	if r.getByID != nil {
		return r.getByID(ctx, id)
	}
	return nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) GetByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	return nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) GetActiveByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	if r.getActive != nil {
		return r.getActive(ctx, userID, groupID)
	}
	return nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) GetActiveByUserID(ctx context.Context, userID int64) (*service.UserSubscription, error) {
	if r.getActive != nil {
		return r.getActive(ctx, userID, 0)
	}
	return nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) GetLatestActiveStatusByUserID(ctx context.Context, userID int64) (*service.UserSubscription, error) {
	if r.getActive != nil {
		return r.getActive(ctx, userID, 0)
	}
	return nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) GetLatestActiveStatusForUpdate(ctx context.Context, userID int64) (*service.UserSubscription, error) {
	return r.GetLatestActiveStatusByUserID(ctx, userID)
}

func (r *stubUserSubscriptionRepo) ApplyManualOverdraft(ctx context.Context, sub *service.UserSubscription) error {
	return nil
}

func (r *stubUserSubscriptionRepo) Update(ctx context.Context, sub *service.UserSubscription) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) Delete(ctx context.Context, id int64) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ListByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	return nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ListActiveByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	return nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) List(ctx context.Context, params pagination.PaginationParams, userID, groupID *int64, status, platform, sortBy, sortOrder string) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ExistsByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (bool, error) {
	return false, errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) UpdateStatus(ctx context.Context, subscriptionID int64, status string) error {
	if r.updateStatus != nil {
		return r.updateStatus(ctx, subscriptionID, status)
	}
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) UpdateNotes(ctx context.Context, subscriptionID int64, notes string) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ActivateWindows(ctx context.Context, id int64, start time.Time) error {
	if r.activateWindow != nil {
		return r.activateWindow(ctx, id, start)
	}
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ResetUsageWindows(context.Context, int64, bool, bool, bool, time.Time) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ResetDailyUsage(ctx context.Context, id int64, _ *time.Time, newWindowStart time.Time) error {
	if r.resetDaily != nil {
		return r.resetDaily(ctx, id, newWindowStart)
	}
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ResetWeeklyUsage(ctx context.Context, id int64, _ *time.Time, newWindowStart time.Time) error {
	if r.resetWeekly != nil {
		return r.resetWeekly(ctx, id, newWindowStart)
	}
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) ResetMonthlyUsage(ctx context.Context, id int64, _ *time.Time, newWindowStart time.Time) error {
	if r.resetMonthly != nil {
		return r.resetMonthly(ctx, id, newWindowStart)
	}
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) IncrementUsage(ctx context.Context, id int64, costUSD float64) error {
	return errors.New("not implemented")
}

func (r *stubUserSubscriptionRepo) BatchUpdateExpiredStatus(ctx context.Context) (int64, error) {
	return 0, errors.New("not implemented")
}
func (r *stubUserSubscriptionRepo) ForfeitExpiredSubscriptions(ctx context.Context, now time.Time, limit int) ([]int64, error) {
	return nil, nil
}
func (r *stubUserSubscriptionRepo) CloseSubscriptionWithReclaim(ctx context.Context, subID int64, now time.Time, deleteRow bool) (int64, float64, error) {
	return 0, 0, nil
}
func (r *stubUserSubscriptionRepo) ShortenSubscriptionWithReclaim(ctx context.Context, subID int64, reduceDays int, newExpiresAt, now time.Time) (int64, float64, error) {
	return 0, 0, nil
}
func (r *stubUserSubscriptionRepo) GrantSubscriptionDays(ctx context.Context, subID int64, addDays int, newExpiresAt, now time.Time) (int64, float64, error) {
	return 0, 0, nil
}

func (r *stubUserSubscriptionRepo) SetOverdraftDays(context.Context, int64, int64, *int) (bool, error) {
	return false, nil
}
