//go:build unit

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidatePricingGateObservationHours(t *testing.T) {
	for _, ok := range []int{0, 1, 72, 720} {
		require.NoError(t, validatePricingGateObservationHours(ok), "%d", ok)
	}
	for _, bad := range []int{-1, 721, 100000} {
		require.ErrorContains(t, validatePricingGateObservationHours(bad), "pricing.gate_observation_hours", "%d", bad)
	}
}
