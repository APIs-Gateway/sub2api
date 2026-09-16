package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIProxyStreamCircuitCollapsesBurstAndRetainsDistinctFailures(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 2,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		collapseInterval: 3 * time.Second,
		maxEntries:       16,
	})

	tripped, _ := circuit.recordFailure(1, base)
	require.False(t, tripped)
	tripped, _ = circuit.recordFailure(1, base.Add(time.Second))
	require.False(t, tripped, "one multiplexed disconnect burst counts once")

	tripped, until := circuit.recordFailure(1, base.Add(5*time.Second))
	require.True(t, tripped, "a distinct later disconnect still trips the circuit")
	require.True(t, circuit.isBlocked(1, until.Add(-time.Nanosecond)))
}

func TestOpenAIProxyStreamCircuitDisabledAndFailOpenContext(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	disabled := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		disabled:         true,
		failureThreshold: 1,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		maxEntries:       16,
	})
	tripped, _ := disabled.recordFailure(1, base)
	require.False(t, tripped)
	require.False(t, disabled.isBlocked(1, base))

	proxyID := int64(7)
	svc := &OpenAIGatewayService{openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 1,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		maxEntries:       16,
	})}
	svc.openaiProxyStreamCircuit.recordFailure(proxyID, time.Now())
	account := &Account{ID: 1, Platform: PlatformOpenAI, ProxyID: &proxyID}
	require.True(t, svc.isOpenAIProxyStreamQuarantined(context.Background(), account))
	require.False(t, svc.isOpenAIProxyStreamQuarantined(withOpenAIProxyStreamQuarantineBypass(context.Background()), account))
}
