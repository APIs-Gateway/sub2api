//go:build unit

package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionConcurrencyForDailyAmount(t *testing.T) {
	require.Equal(t, 0, subscriptionConcurrencyForDailyAmount(0))
	require.Equal(t, 1, subscriptionConcurrencyForDailyAmount(1))
	require.Equal(t, 1, subscriptionConcurrencyForDailyAmount(10))
	require.Equal(t, 2, subscriptionConcurrencyForDailyAmount(10.01))
	require.Equal(t, 6, subscriptionConcurrencyForDailyAmount(60))
	require.Equal(t, 0, subscriptionConcurrencyForDailyAmount(math.NaN()))
}
