//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type accountWaitRegistrationCache struct {
	ConcurrencyCache
	registered bool
	err        error
	releases   atomic.Int32
	canceled   atomic.Bool
}

func (c *accountWaitRegistrationCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	return c.registered, c.err
}

func (c *accountWaitRegistrationCache) DecrementAccountWaitCount(ctx context.Context, _ int64) error {
	c.canceled.Store(ctx.Err() != nil)
	c.releases.Add(1)
	return nil
}

func TestAccountWaitRegistration_Ownership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cache   *accountWaitRegistrationCache
		allowed bool
		owned   bool
	}{
		{name: "confirmed", cache: &accountWaitRegistrationCache{registered: true}, allowed: true, owned: true},
		{name: "full", cache: &accountWaitRegistrationCache{}, allowed: false},
		{name: "failed_open", cache: &accountWaitRegistrationCache{err: errors.New("unconfirmed Redis increment")}, allowed: true},
		{name: "nil_cache", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cache ConcurrencyCache
			if tc.cache != nil {
				cache = tc.cache
			}
			ctx, cancel := context.WithCancel(context.Background())
			allowed, release := NewConcurrencyService(cache).RegisterAccountWait(ctx, 42, 1)
			require.Equal(t, tc.allowed, allowed)
			cancel()
			if !tc.owned {
				require.Nil(t, release)
				if tc.cache != nil {
					require.Zero(t, tc.cache.releases.Load())
				}
				return
			}
			require.NotNil(t, release)
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					release()
				}()
			}
			wg.Wait()
			require.EqualValues(t, 1, tc.cache.releases.Load())
			require.False(t, tc.cache.canceled.Load(), "cleanup uses the existing bounded background context")
		})
	}
}
