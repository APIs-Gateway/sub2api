//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type livenessTrackingCache struct {
	stubConcurrencyCacheForTest
	calls           []string
	heartbeatErr    error
	heartbeatPrefix string
	cleanupPrefix   string
}

func (c *livenessTrackingCache) HeartbeatProcess(_ context.Context, prefix string) error {
	c.calls = append(c.calls, "heartbeat")
	c.heartbeatPrefix = prefix
	return c.heartbeatErr
}

func (c *livenessTrackingCache) CleanupStaleProcessSlots(_ context.Context, prefix string) error {
	c.calls = append(c.calls, "cleanup")
	c.cleanupPrefix = prefix
	return nil
}

func TestCleanupStaleProcessSlotsRegistersHeartbeatBeforeCleanup(t *testing.T) {
	cache := &livenessTrackingCache{}
	require.NoError(t, NewConcurrencyService(cache).CleanupStaleProcessSlots(context.Background()))
	require.Equal(t, []string{"heartbeat", "cleanup"}, cache.calls)
	require.Equal(t, RequestIDPrefix(), cache.heartbeatPrefix)
	require.Equal(t, RequestIDPrefix(), cache.cleanupPrefix)
}

func TestCleanupStaleProcessSlotsSkipsCleanupWhenHeartbeatFails(t *testing.T) {
	cache := &livenessTrackingCache{heartbeatErr: errors.New("redis unavailable")}
	require.ErrorIs(t, NewConcurrencyService(cache).CleanupStaleProcessSlots(context.Background()), cache.heartbeatErr)
	require.Equal(t, []string{"heartbeat"}, cache.calls)
}
