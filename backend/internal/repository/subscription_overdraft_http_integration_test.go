//go:build integration

package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionOverdraftHTTP_HeaderIdempotencyReplaysAfterDailyUsageRefillsPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(
		NewIdempotencyRepository(client, integrationDB),
		service.IdempotencyConfig{
			DefaultTTL:           service.DefaultIdempotencyConfig().DefaultTTL,
			SystemOperationTTL:   service.DefaultIdempotencyConfig().SystemOperationTTL,
			ProcessingTimeout:    service.DefaultIdempotencyConfig().ProcessingTimeout,
			FailedRetryBackoff:   service.DefaultIdempotencyConfig().FailedRetryBackoff,
			MaxStoredResponseLen: service.DefaultIdempotencyConfig().MaxStoredResponseLen,
			ObserveOnly:          false,
		},
	))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("overdraft-idem-header-%s@example.com", uuid.NewString()),
		Balance: 1000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "overdraft-idem-header-" + uuid.NewString()})
	card := mustCreateOverdraftReadyCard(t, user.ID, group.ID)
	router := subscriptionOverdraftRouter(t, user.ID)
	key := "overdraft-" + uuid.NewString()

	first := performOverdraftRequest(t, router, key, nil)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	firstEnv := decodeLifecycleEnvelope(t, first)
	firstData := requireLifecycleData(t, firstEnv)
	require.EqualValues(t, card.ID, firstData["subscription_id"])

	gotAfterFirst, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.InDelta(t, 0, gotAfterFirst.DailyUsageUSD, 1e-9)
	firstExpireDay := gotAfterFirst.ExpireDay
	gotUserAfterFirst, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 1, gotUserAfterFirst.MonthlyOverdraftCount)

	// 模拟首次成功后当天继续消费到日额度满。若幂等只靠 daily_usage=0，这个状态会让同 key 重放再次借天。
	_, err = client.UserSubscription.UpdateOneID(card.ID).SetDailyUsageUsd(10).Save(ctx)
	require.NoError(t, err)

	second := performOverdraftRequest(t, router, key, nil)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))

	gotAfterReplay, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, firstExpireDay, gotAfterReplay.ExpireDay, "same Idempotency-Key replay must not borrow another day")
	require.InDelta(t, 10, gotAfterReplay.DailyUsageUSD, 1e-9, "replay must not execute the overdraft side effect again")
	gotUserAfterReplay, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 1, gotUserAfterReplay.MonthlyOverdraftCount, "same Idempotency-Key replay must not increment monthly count again")
}

func TestSubscriptionOverdraftHTTP_BodyIdempotencyKeyReplaysAfterDailyUsageRefillsPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(
		NewIdempotencyRepository(client, integrationDB),
		service.IdempotencyConfig{
			DefaultTTL:           service.DefaultIdempotencyConfig().DefaultTTL,
			SystemOperationTTL:   service.DefaultIdempotencyConfig().SystemOperationTTL,
			ProcessingTimeout:    service.DefaultIdempotencyConfig().ProcessingTimeout,
			FailedRetryBackoff:   service.DefaultIdempotencyConfig().FailedRetryBackoff,
			MaxStoredResponseLen: service.DefaultIdempotencyConfig().MaxStoredResponseLen,
			ObserveOnly:          true,
		},
	))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("overdraft-idem-body-%s@example.com", uuid.NewString()),
		Balance: 1000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "overdraft-idem-body-" + uuid.NewString()})
	card := mustCreateOverdraftReadyCard(t, user.ID, group.ID)
	router := subscriptionOverdraftRouter(t, user.ID)
	bodyKey := "body-only-" + uuid.NewString()

	first := performOverdraftRequest(t, router, "", map[string]any{"idempotency_key": bodyKey})
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	gotAfterFirst, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	firstExpireDay := gotAfterFirst.ExpireDay
	_, err = client.UserSubscription.UpdateOneID(card.ID).SetDailyUsageUsd(10).Save(ctx)
	require.NoError(t, err)

	second := performOverdraftRequest(t, router, "", map[string]any{"idempotency_key": bodyKey})
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))

	gotAfterSecond, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, firstExpireDay, gotAfterSecond.ExpireDay, "body idempotency_key compatibility path must not borrow another day on replay")
}

func TestSubscriptionOverdraftHTTP_ConcurrentDifferentKeysOnlyOneBorrowPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(
		NewIdempotencyRepository(client, integrationDB),
		service.IdempotencyConfig{
			DefaultTTL:           service.DefaultIdempotencyConfig().DefaultTTL,
			SystemOperationTTL:   service.DefaultIdempotencyConfig().SystemOperationTTL,
			ProcessingTimeout:    service.DefaultIdempotencyConfig().ProcessingTimeout,
			FailedRetryBackoff:   service.DefaultIdempotencyConfig().FailedRetryBackoff,
			MaxStoredResponseLen: service.DefaultIdempotencyConfig().MaxStoredResponseLen,
			ObserveOnly:          false,
		},
	))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("overdraft-concurrent-%s@example.com", uuid.NewString()),
		Balance: 1000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "overdraft-concurrent-" + uuid.NewString()})
	card := mustCreateOverdraftReadyCard(t, user.ID, group.ID)
	startExpireDay := card.ExpireDay
	router := subscriptionOverdraftRouter(t, user.ID)

	const n = 5
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			rec := performOverdraftRequest(t, router, fmt.Sprintf("overdraft-concurrent-%d-%s", i, uuid.NewString()), nil)
			results <- rec.Code
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	okCount := 0
	badRequestCount := 0
	for code := range results {
		switch code {
		case http.StatusOK:
			okCount++
		case http.StatusBadRequest:
			badRequestCount++
		default:
			t.Fatalf("unexpected status code from concurrent overdraft: %d", code)
		}
	}
	require.Equal(t, 1, okCount, "only one concurrent overdraft should pass after the first request clears daily_usage")
	require.Equal(t, n-1, badRequestCount)

	gotSub, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, startExpireDay-1, gotSub.ExpireDay)
	require.InDelta(t, 0, gotSub.DailyUsageUSD, 1e-9)
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 1, gotUser.MonthlyOverdraftCount)
}

func TestSubscriptionOverdraftHTTP_MonthlyLimitFailureRollsBackPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(
		NewIdempotencyRepository(client, integrationDB),
		service.DefaultIdempotencyConfig(),
	))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("overdraft-month-limit-%s@example.com", uuid.NewString()),
		Balance: 1000,
	})
	_, err := client.User.UpdateOneID(user.ID).
		SetMonthlyOverdraftCount(service.MaxMonthlyOverdraftUses).
		SetMonthlyOverdraftMonth(service.CurrentEastMonthKey()).
		Save(ctx)
	require.NoError(t, err)
	group := mustCreateGroup(t, client, &service.Group{Name: "overdraft-month-limit-" + uuid.NewString()})
	card := mustCreateOverdraftReadyCard(t, user.ID, group.ID)
	router := subscriptionOverdraftRouter(t, user.ID)

	rec := performOverdraftRequest(t, router, "overdraft-month-limit-"+uuid.NewString(), nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "OVERDRAFT_MONTHLY_LIMIT")

	gotSub, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, card.ExpireDay, gotSub.ExpireDay)
	require.InDelta(t, 10, gotSub.DailyUsageUSD, 1e-9)
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, service.MaxMonthlyOverdraftUses, gotUser.MonthlyOverdraftCount)
}

func TestSubscriptionOverdraftHTTP_NoFutureDayFailureRollsBackPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(
		NewIdempotencyRepository(client, integrationDB),
		service.DefaultIdempotencyConfig(),
	))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("overdraft-no-future-%s@example.com", uuid.NewString()),
		Balance: 1000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "overdraft-no-future-" + uuid.NewString()})
	card := mustCreateOverdraftReadyCard(t, user.ID, group.ID)
	today := service.TodayEastDayNumber()
	_, err := client.UserSubscription.UpdateOneID(card.ID).
		SetExpireDay(today).
		SetExpiresAt(service.ExpireDayToExpiresAt(today)).
		Save(ctx)
	require.NoError(t, err)
	router := subscriptionOverdraftRouter(t, user.ID)

	rec := performOverdraftRequest(t, router, "overdraft-no-future-"+uuid.NewString(), nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "OVERDRAFT_NO_FUTURE_DAY")

	gotSub, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.Equal(t, today, gotSub.ExpireDay)
	require.InDelta(t, 10, gotSub.DailyUsageUSD, 1e-9)
	gotUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 0, gotUser.MonthlyOverdraftCount)
}

func TestSubscriptionOverdraftHTTP_LegacyToggleEndpointRetiredPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("overdraft-legacy-toggle-%s@example.com", uuid.NewString()),
		Balance: 1000,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "overdraft-legacy-toggle-" + uuid.NewString()})
	card := mustCreateOverdraftReadyCard(t, user.ID, group.ID)
	router := subscriptionOverdraftRouter(t, user.ID)

	body := bytes.NewReader([]byte(`{"max_overdraft_days":3}`))
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/subscriptions/%d/overdraft", card.ID), body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusGone, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "SUBSCRIPTION_OVERDRAFT_TOGGLE_RETIRED")

	var overdraftOn bool
	var maxOverdraftDays *int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT overdraft_on, max_overdraft_days
		FROM user_subscriptions WHERE id = $1
	`, card.ID).Scan(&overdraftOn, &maxOverdraftDays))
	require.False(t, overdraftOn)
	require.Nil(t, maxOverdraftDays)
}

func TestSubscriptionListHTTP_IncludesMonthlyOverdraftRemainingPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	month := service.CurrentEastMonthKey()
	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("overdraft-list-%s@example.com", uuid.NewString()),
		Balance: 1000,
	})
	_, err := client.User.UpdateOneID(user.ID).
		SetMonthlyOverdraftCount(4).
		SetMonthlyOverdraftMonth(month).
		Save(ctx)
	require.NoError(t, err)
	group := mustCreateGroup(t, client, &service.Group{Name: "overdraft-list-" + uuid.NewString()})
	mustCreateOverdraftReadyCard(t, user.ID, group.ID)

	router := subscriptionOverdraftRouter(t, user.ID)
	rec := performSubscriptionGET(t, router, "/api/v1/subscriptions")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var env struct {
		Code int `json:"code"`
		Data []struct {
			MonthlyOverdraftRemaining *int `json:"monthly_overdraft_remaining"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Equal(t, 0, env.Code)
	require.Len(t, env.Data, 1)
	require.NotNil(t, env.Data[0].MonthlyOverdraftRemaining)
	require.Equal(t, 1, *env.Data[0].MonthlyOverdraftRemaining)
}

func TestSubscriptionSummaryHTTP_UsesCardLimitsNotGroupLimitsPostgres(t *testing.T) {
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("overdraft-summary-%s@example.com", uuid.NewString()),
		Balance: 1000,
	})
	groupDaily, groupWeekly, groupMonthly := 1.0, 2.0, 3.0
	group := mustCreateGroup(t, client, &service.Group{
		Name:            "overdraft-summary-" + uuid.NewString(),
		DailyLimitUSD:   &groupDaily,
		WeeklyLimitUSD:  &groupWeekly,
		MonthlyLimitUSD: &groupMonthly,
	})
	mustCreateOverdraftReadyCard(t, user.ID, group.ID)

	router := subscriptionOverdraftRouter(t, user.ID)
	rec := performSubscriptionGET(t, router, "/api/v1/subscriptions/summary")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var env struct {
		Code int `json:"code"`
		Data struct {
			Subscriptions []struct {
				DailyLimitUSD   float64 `json:"daily_limit_usd"`
				WeeklyLimitUSD  float64 `json:"weekly_limit_usd"`
				MonthlyLimitUSD float64 `json:"monthly_limit_usd"`
			} `json:"subscriptions"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Equal(t, 0, env.Code)
	require.Len(t, env.Data.Subscriptions, 1)
	require.InDelta(t, 10, env.Data.Subscriptions[0].DailyLimitUSD, 1e-9)
	require.InDelta(t, 70, env.Data.Subscriptions[0].WeeklyLimitUSD, 1e-9)
	require.InDelta(t, 300, env.Data.Subscriptions[0].MonthlyLimitUSD, 1e-9)
}

func mustCreateOverdraftReadyCard(t *testing.T, userID, groupID int64) *service.UserSubscription {
	t.Helper()
	client := testEntClient(t)
	now := time.Now()
	dayStart := timezone.StartOfDay(now)
	weekStart := timezone.StartOfWeek(now)
	monthStart := timezone.StartOfMonth(now)
	expiresAt := service.ExpireDayToExpiresAt(service.TodayEastDayNumber() + 10)
	daily, weekly, monthly := 10.0, 70.0, 300.0
	return mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             userID,
		GroupID:            groupID,
		DailyAmountUSD:     10,
		GrantedTotalUSD:    300,
		TodayRemaining:     0,
		TodayDay:           service.TodayEastDayNumber(),
		StartDay:           service.TodayEastDayNumber(),
		ExpireDay:          service.TodayEastDayNumber() + 10,
		ExpiresAt:          expiresAt,
		Status:             service.SubscriptionStatusActive,
		DailyLimitUSD:      &daily,
		WeeklyLimitUSD:     &weekly,
		MonthlyLimitUSD:    &monthly,
		DailyUsageUSD:      10,
		WeeklyUsageUSD:     10,
		MonthlyUsageUSD:    10,
		DailyWindowStart:   &dayStart,
		WeeklyWindowStart:  &weekStart,
		MonthlyWindowStart: &monthStart,
	})
}

func subscriptionOverdraftRouter(t *testing.T, userID int64) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	router := gin.New()
	if userID > 0 {
		router.Use(func(c *gin.Context) {
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID})
			c.Next()
		})
	}

	h := userhandler.NewSubscriptionHandler(makeSubscriptionService(t))
	subscriptions := router.Group("/api/v1/subscriptions")
	subscriptions.GET("", h.List)
	subscriptions.GET("/summary", h.GetSummary)
	subscriptions.PUT("/:id/overdraft", h.SetOverdraftDays)
	subscriptions.POST("/overdraft", h.Overdraft)
	return router
}

func performOverdraftRequest(t *testing.T, router *gin.Engine, idemKey string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/overdraft", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func performSubscriptionGET(t *testing.T, router *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
