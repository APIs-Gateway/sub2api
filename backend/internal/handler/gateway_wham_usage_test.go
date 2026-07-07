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

type whamUsageUserRepoStub struct {
	service.UserRepository
	user            *service.User
	requestedUserID int64
}

func (r *whamUsageUserRepoStub) GetByID(_ context.Context, id int64) (*service.User, error) {
	r.requestedUserID = id
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
	requestedUserID int64
}

func (r *whamUsageSubscriptionRepoStub) ListActiveByUserID(_ context.Context, userID int64) ([]service.UserSubscription, error) {
	r.requestedUserID = userID
	out := make([]service.UserSubscription, len(r.subs))
	copy(out, r.subs)
	return out, nil
}

func (r *whamUsageSubscriptionRepoStub) ListByGroupID(context.Context, int64, pagination.PaginationParams) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}
