package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/enttest"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckOrderCreateRateLimit(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	svc := &PaymentService{entClient: client}
	userID := int64(100)

	t.Run("disabled rate limit allows all requests", func(t *testing.T) {
		cfg := &PaymentConfig{
			OrderCreateRateLimitEnabled: false,
			OrderCreateRateLimitMax:     5,
			OrderCreateRateLimitWindow:  1,
			OrderCreateRateLimitUnit:    "hour",
		}
		err := svc.checkOrderCreateRateLimit(ctx, userID, cfg)
		assert.NoError(t, err)
	})

	t.Run("zero max allows all requests", func(t *testing.T) {
		cfg := &PaymentConfig{
			OrderCreateRateLimitEnabled: true,
			OrderCreateRateLimitMax:     0,
			OrderCreateRateLimitWindow:  1,
			OrderCreateRateLimitUnit:    "hour",
		}
		err := svc.checkOrderCreateRateLimit(ctx, userID, cfg)
		assert.NoError(t, err)
	})

	t.Run("under limit allows request", func(t *testing.T) {
		testUserID := int64(200)
		cfg := &PaymentConfig{
			OrderCreateRateLimitEnabled: true,
			OrderCreateRateLimitMax:     5,
			OrderCreateRateLimitWindow:  1,
			OrderCreateRateLimitUnit:    "hour",
			OrderCreateRateLimitMode:    "rolling",
		}

		// Create 3 audit logs within the window
		operator := "user:200"
		for i := 0; i < 3; i++ {
			_, err := client.PaymentAuditLog.Create().
				SetOrderID(fmt.Sprintf("order_%d", i)).
				SetAction("ORDER_CREATED").
				SetOperator(operator).
				SetDetail("test order").
				SetCreatedAt(time.Now().Add(-10 * time.Minute)).
				Save(ctx)
			require.NoError(t, err)
		}

		err := svc.checkOrderCreateRateLimit(ctx, testUserID, cfg)
		assert.NoError(t, err)
	})

	t.Run("at limit rejects request", func(t *testing.T) {
		testUserID := int64(300)
		cfg := &PaymentConfig{
			OrderCreateRateLimitEnabled: true,
			OrderCreateRateLimitMax:     5,
			OrderCreateRateLimitWindow:  1,
			OrderCreateRateLimitUnit:    "hour",
			OrderCreateRateLimitMode:    "rolling",
		}

		// Create exactly 5 audit logs within the window
		operator := "user:300"
		for i := 0; i < 5; i++ {
			_, err := client.PaymentAuditLog.Create().
				SetOrderID(fmt.Sprintf("order_300_%d", i)).
				SetAction("ORDER_CREATED").
				SetOperator(operator).
				SetDetail("test order").
				SetCreatedAt(time.Now().Add(-10 * time.Minute)).
				Save(ctx)
			require.NoError(t, err)
		}

		err := svc.checkOrderCreateRateLimit(ctx, testUserID, cfg)
		assert.Error(t, err)
		assert.True(t, infraerrors.IsTooManyRequests(err))
		assert.Equal(t, "ORDER_CREATE_RATE_LIMITED", infraerrors.Reason(err))
	})

	t.Run("over limit rejects request", func(t *testing.T) {
		testUserID := int64(400)
		cfg := &PaymentConfig{
			OrderCreateRateLimitEnabled: true,
			OrderCreateRateLimitMax:     3,
			OrderCreateRateLimitWindow:  1,
			OrderCreateRateLimitUnit:    "hour",
			OrderCreateRateLimitMode:    "rolling",
		}

		// Create 5 audit logs within the window (over the limit of 3)
		operator := "user:400"
		for i := 0; i < 5; i++ {
			_, err := client.PaymentAuditLog.Create().
				SetOrderID(fmt.Sprintf("order_400_%d", i)).
				SetAction("ORDER_CREATED").
				SetOperator(operator).
				SetDetail("test order").
				SetCreatedAt(time.Now().Add(-10 * time.Minute)).
				Save(ctx)
			require.NoError(t, err)
		}

		err := svc.checkOrderCreateRateLimit(ctx, testUserID, cfg)
		assert.Error(t, err)
		assert.True(t, infraerrors.IsTooManyRequests(err))
		assert.Equal(t, "ORDER_CREATE_RATE_LIMITED", infraerrors.Reason(err))
	})

	t.Run("old records outside window do not count", func(t *testing.T) {
		testUserID := int64(500)
		cfg := &PaymentConfig{
			OrderCreateRateLimitEnabled: true,
			OrderCreateRateLimitMax:     3,
			OrderCreateRateLimitWindow:  1,
			OrderCreateRateLimitUnit:    "hour",
			OrderCreateRateLimitMode:    "rolling",
		}

		operator := "user:500"
		// Create 5 old records outside the window (2 hours ago)
		for i := 0; i < 5; i++ {
			_, err := client.PaymentAuditLog.Create().
				SetOrderID(fmt.Sprintf("order_500_old_%d", i)).
				SetAction("ORDER_CREATED").
				SetOperator(operator).
				SetDetail("old order").
				SetCreatedAt(time.Now().Add(-2 * time.Hour)).
				Save(ctx)
			require.NoError(t, err)
		}

		// Create 2 recent records within the window
		for i := 0; i < 2; i++ {
			_, err := client.PaymentAuditLog.Create().
				SetOrderID(fmt.Sprintf("order_500_recent_%d", i)).
				SetAction("ORDER_CREATED").
				SetOperator(operator).
				SetDetail("recent order").
				SetCreatedAt(time.Now().Add(-10 * time.Minute)).
				Save(ctx)
			require.NoError(t, err)
		}

		// Should pass because only 2 records are within the 1-hour window
		err := svc.checkOrderCreateRateLimit(ctx, testUserID, cfg)
		assert.NoError(t, err)
	})

	t.Run("fail closed on database error", func(t *testing.T) {
		// Close the client to simulate DB error
		testClient := enttest.Open(t, "sqlite3", "file:ent_test?mode=memory&cache=shared&_fk=1")
		testClient.Close()

		testSvc := &PaymentService{entClient: testClient}
		testUserID := int64(600)
		cfg := &PaymentConfig{
			OrderCreateRateLimitEnabled: true,
			OrderCreateRateLimitMax:     5,
			OrderCreateRateLimitWindow:  1,
			OrderCreateRateLimitUnit:    "hour",
		}

		err := testSvc.checkOrderCreateRateLimit(ctx, testUserID, cfg)
		assert.Error(t, err)
		assert.Equal(t, 503, infraerrors.Code(err))
		assert.Equal(t, "RATE_LIMIT_CHECK_FAILED", infraerrors.Reason(err))
	})
}

func TestOrderCreateRateLimitWindowStart(t *testing.T) {
	now := time.Now()

	t.Run("rolling window - hour", func(t *testing.T) {
		cfg := &PaymentConfig{
			OrderCreateRateLimitWindow: 2,
			OrderCreateRateLimitUnit:   "hour",
			OrderCreateRateLimitMode:   "rolling",
		}
		result := orderCreateRateLimitWindowStart(cfg)
		expected := now.Add(-2 * time.Hour)
		assert.WithinDuration(t, expected, result, time.Second)
	})

	t.Run("rolling window - day", func(t *testing.T) {
		cfg := &PaymentConfig{
			OrderCreateRateLimitWindow: 3,
			OrderCreateRateLimitUnit:   "day",
			OrderCreateRateLimitMode:   "rolling",
		}
		result := orderCreateRateLimitWindowStart(cfg)
		expected := now.AddDate(0, 0, -3)
		assert.WithinDuration(t, expected, result, time.Second)
	})

	t.Run("rolling window - minute", func(t *testing.T) {
		cfg := &PaymentConfig{
			OrderCreateRateLimitWindow: 30,
			OrderCreateRateLimitUnit:   "minute",
			OrderCreateRateLimitMode:   "rolling",
		}
		result := orderCreateRateLimitWindowStart(cfg)
		expected := now.Add(-30 * time.Minute)
		assert.WithinDuration(t, expected, result, time.Second)
	})

	t.Run("fixed window - hour", func(t *testing.T) {
		cfg := &PaymentConfig{
			OrderCreateRateLimitWindow: 2,
			OrderCreateRateLimitUnit:   "hour",
			OrderCreateRateLimitMode:   "fixed",
		}
		result := orderCreateRateLimitWindowStart(cfg)
		expected := now.Truncate(time.Hour).Add(-1 * time.Hour)
		assert.WithinDuration(t, expected, result, time.Second)
	})

	t.Run("fixed window - day", func(t *testing.T) {
		cfg := &PaymentConfig{
			OrderCreateRateLimitWindow: 2,
			OrderCreateRateLimitUnit:   "day",
			OrderCreateRateLimitMode:   "fixed",
		}
		result := orderCreateRateLimitWindowStart(cfg)
		y, m, d := now.Date()
		expected := time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -1)
		assert.WithinDuration(t, expected, result, time.Second)
	})

	t.Run("defaults to hour when unit is empty", func(t *testing.T) {
		cfg := &PaymentConfig{
			OrderCreateRateLimitWindow: 1,
			OrderCreateRateLimitUnit:   "",
			OrderCreateRateLimitMode:   "rolling",
		}
		result := orderCreateRateLimitWindowStart(cfg)
		expected := now.Add(-1 * time.Hour)
		assert.WithinDuration(t, expected, result, time.Second)
	})

	t.Run("defaults to 1 when window is zero", func(t *testing.T) {
		cfg := &PaymentConfig{
			OrderCreateRateLimitWindow: 0,
			OrderCreateRateLimitUnit:   "hour",
			OrderCreateRateLimitMode:   "rolling",
		}
		result := orderCreateRateLimitWindowStart(cfg)
		expected := now.Add(-1 * time.Hour)
		assert.WithinDuration(t, expected, result, time.Second)
	})
}
