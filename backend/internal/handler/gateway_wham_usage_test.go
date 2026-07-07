//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWhamUsageReturnsFreshWalletBalanceForAPIKeyOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)

	today := service.TodayEastDayNumber()
	userRepo := &whamUsageUserRepoStub{
		user: &service.User{
			ID:      77,
			Status:  service.StatusActive,
			Balance: 55.75,
		},
	}
	subRepo := &whamUsageSubscriptionRepoStub{
		subs: []service.UserSubscription{
			{
				Status:          service.SubscriptionStatusActive,
				DailyAmountUSD:  20,
				TodayRemaining:  5,
				TodayDay:        today,
				StartDay:        today - 1,
				ExpireDay:       today + 3,
				ExpiresAt:       service.ExpireDayToExpiresAt(today + 3),
				DailySpentDay:   today,
				DailySpentUSD:   15,
				GrantedTotalUSD: 100,
			},
		},
	}
	h := NewGatewayHandler(
		nil, nil, nil,
		service.NewUserService(userRepo, nil, nil, nil),
		service.NewSubscriptionService(nil, subRepo, nil, nil, nil, nil, nil, &config.Config{}),
		nil, nil, nil, nil, nil, nil, nil, nil,
		&config.Config{},
		nil,
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/backend-api/wham/usage", nil)
	c.Request.Header.Set("ChatGPT-Account-Id", "ignored-client-account")
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:     1001,
		UserID: 77,
		User:   &service.User{ID: 77, Status: service.StatusActive, Balance: 1},
	})

	h.WhamUsage(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(77), userRepo.requestedUserID)
	require.Equal(t, int64(77), subRepo.requestedUserID)

	var resp whamUsageResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.RateLimit.PrimaryWindow)
	require.Equal(t, 75.0, resp.RateLimit.PrimaryWindow.UsedPercent)
	require.Equal(t, int64(86400), resp.RateLimit.PrimaryWindow.LimitWindowSeconds)
	require.Equal(t, 20.0, resp.Sub2API.DailyLimitUSD)
	require.Equal(t, 15.0, resp.Sub2API.DailyUsedUSD)
	require.Equal(t, 5.0, resp.Sub2API.DailyRemainingUSD)
	require.Equal(t, 55.75, resp.Sub2API.WalletBalanceUSD)
	require.Equal(t, 1, resp.Sub2API.ActiveSubscriptionCount)
}

func TestWhamUsageUsesEmbeddedUserWhenAPIKeyUserIDIsMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	userRepo := &whamUsageUserRepoStub{
		user: &service.User{ID: 88, Status: service.StatusActive, Balance: 12.5},
	}
	subRepo := &whamUsageSubscriptionRepoStub{}
	h := whamUsageTestHandler(userRepo, subRepo)
	rec, c := whamUsageTestContext(&service.APIKey{
		ID:   1002,
		User: &service.User{ID: 88, Status: service.StatusActive, Balance: 1},
	})

	h.WhamUsage(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(88), userRepo.requestedUserID)
	require.Equal(t, int64(88), subRepo.requestedUserID)

	var resp whamUsageResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 12.5, resp.Sub2API.WalletBalanceUSD)
	require.Nil(t, resp.RateLimit.PrimaryWindow)
}

func TestWhamUsageRejectsMissingAPIKeyContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c := whamUsageTestContext(nil)
	(&GatewayHandler{}).WhamUsage(c)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid API key")
}

func TestWhamUsageRejectsAPIKeyWithoutUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c := whamUsageTestContext(&service.APIKey{ID: 1003})
	(&GatewayHandler{}).WhamUsage(c)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid API key")
}

func TestWhamUsageRequiresUsageServices(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec, c := whamUsageTestContext(&service.APIKey{ID: 1004, UserID: 7})
	(&GatewayHandler{}).WhamUsage(c)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "Usage service unavailable")
}

func TestWhamUsageReturnsUserLookupError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	userRepo := &whamUsageUserRepoStub{err: errors.New("db unavailable")}
	h := whamUsageTestHandler(userRepo, &whamUsageSubscriptionRepoStub{})
	rec, c := whamUsageTestContext(&service.APIKey{ID: 1005, UserID: 7})

	h.WhamUsage(c)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "Failed to get user info")
}

func TestWhamUsageReturnsSubscriptionLookupError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := whamUsageTestHandler(
		&whamUsageUserRepoStub{user: &service.User{ID: 7, Status: service.StatusActive, Balance: 1}},
		&whamUsageSubscriptionRepoStub{err: errors.New("db unavailable")},
	)
	rec, c := whamUsageTestContext(&service.APIKey{ID: 1006, UserID: 7})

	h.WhamUsage(c)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "Failed to get subscription usage")
}

func TestWhamUsageJSONKeepsEmptyRateLimitObjectWithoutActivePerDayCards(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	body, err := json.Marshal(buildWhamUsageResponse(9.5, nil, now))
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &raw))
	require.JSONEq(t, `{}`, string(raw["rate_limit"]))

	var sub2api map[string]any
	require.NoError(t, json.Unmarshal(raw["sub2api"], &sub2api))
	require.Equal(t, 9.5, sub2api["wallet_balance_usd"])
	require.Equal(t, float64(0), sub2api["active_subscription_count"])
	require.Equal(t, float64(0), sub2api["daily_limit_usd"])
}

func TestWhamUsageJSONDoesNotInventSecondaryWindow(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	today := service.EastDayNumber(now)

	body, err := json.Marshal(buildWhamUsageResponse(0, []service.UserSubscription{
		whamUsageTestSub(today, 30, 12),
	}, now))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	rateLimit, ok := raw["rate_limit"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, rateLimit, "primary_window")
	require.NotContains(t, rateLimit, "secondary_window")
}

func TestBuildWhamUsageResponseSkipsExpiredCards(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	today := service.EastDayNumber(now)

	statusActiveExpiredAt := whamUsageTestSub(today, 30, 12)
	statusActiveExpiredAt.ExpiresAt = now.Add(-time.Second)

	lazyExpired := whamUsageTestSub(today-1, 30, 12)
	lazyExpired.ExpireDay = today - 1
	lazyExpired.ExpiresAt = time.Time{}

	resp := buildWhamUsageResponse(3, []service.UserSubscription{
		statusActiveExpiredAt,
		lazyExpired,
		whamUsageTestSub(today, 10, 7),
	}, now)

	require.NotNil(t, resp.RateLimit.PrimaryWindow)
	require.Equal(t, 10.0, resp.Sub2API.DailyLimitUSD)
	require.Equal(t, 7.0, resp.Sub2API.DailyRemainingUSD)
	require.Equal(t, 3.0, resp.Sub2API.DailyUsedUSD)
	require.Equal(t, 1, resp.Sub2API.ActiveSubscriptionCount)
}

func whamUsageTestHandler(userRepo *whamUsageUserRepoStub, subRepo *whamUsageSubscriptionRepoStub) *GatewayHandler {
	return NewGatewayHandler(
		nil, nil, nil,
		service.NewUserService(userRepo, nil, nil, nil),
		service.NewSubscriptionService(nil, subRepo, nil, nil, nil, nil, nil, &config.Config{}),
		nil, nil, nil, nil, nil, nil, nil, nil,
		&config.Config{},
		nil,
	)
}

func whamUsageTestContext(apiKey *service.APIKey) (*httptest.ResponseRecorder, *gin.Context) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/backend-api/wham/usage", nil)
	if apiKey != nil {
		c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	}
	return rec, c
}

type whamUsageUserRepoStub struct {
	service.UserRepository
	user            *service.User
	err             error
	requestedUserID int64
}

func (r *whamUsageUserRepoStub) GetByID(_ context.Context, id int64) (*service.User, error) {
	r.requestedUserID = id
	if r.err != nil {
		return nil, r.err
	}
	if r.user == nil {
		return nil, errors.New("missing user")
	}
	cloned := *r.user
	return &cloned, nil
}

func (r *whamUsageUserRepoStub) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

type whamUsageSubscriptionRepoStub struct {
	service.UserSubscriptionRepository
	subs            []service.UserSubscription
	err             error
	requestedUserID int64
}

func (r *whamUsageSubscriptionRepoStub) ListActiveByUserID(_ context.Context, userID int64) ([]service.UserSubscription, error) {
	r.requestedUserID = userID
	if r.err != nil {
		return nil, r.err
	}
	out := make([]service.UserSubscription, len(r.subs))
	copy(out, r.subs)
	return out, nil
}

func (r *whamUsageSubscriptionRepoStub) ListByGroupID(context.Context, int64, pagination.PaginationParams) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}
