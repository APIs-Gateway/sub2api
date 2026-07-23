package service

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIProxyStreamCircuitSettingsDefaultsAndOverrides(t *testing.T) {
	defaults := resolveOpenAIProxyStreamCircuitSettings(nil)
	require.Equal(t, defaultOpenAIProxyStreamFailureThreshold, defaults.failureThreshold)
	require.Equal(t, defaultOpenAIProxyStreamFailureWindow, defaults.failureWindow)
	require.Equal(t, defaultOpenAIProxyStreamQuarantineTTL, defaults.quarantineTTL)
	require.Equal(t, defaultOpenAIProxyStreamCircuitMaxEntries, defaults.maxEntries)

	serviceDefaults := (&OpenAIGatewayService{}).getOpenAIProxyStreamCircuit()
	require.NotNil(t, serviceDefaults)
	require.Equal(t, defaultOpenAIProxyStreamFailureWindow, serviceDefaults.settings.failureWindow)

	service := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		OpenAIProxyStreamCircuit: config.GatewayOpenAIProxyStreamCircuitConfig{
			FailureThreshold: 3,
			WindowSeconds:    90,
			TTLSeconds:       420,
		},
	}}}
	overrides := resolveOpenAIProxyStreamCircuitSettings(service)
	require.Equal(t, 3, overrides.failureThreshold)
	require.Equal(t, 90*time.Second, overrides.failureWindow)
	require.Equal(t, 420*time.Second, overrides.quarantineTTL)

	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{})
	require.Equal(t, defaultOpenAIProxyStreamFailureThreshold, circuit.settings.failureThreshold)
	require.Equal(t, defaultOpenAIProxyStreamFailureWindow, circuit.settings.failureWindow)
	require.Equal(t, defaultOpenAIProxyStreamQuarantineTTL, circuit.settings.quarantineTTL)
	require.Equal(t, defaultOpenAIProxyStreamCircuitMaxEntries, circuit.settings.maxEntries)
}

func TestOpenAIProxyStreamCircuitIgnoresInvalidAndBlockedFailures(t *testing.T) {
	var nilCircuit *openAIProxyStreamCircuit
	base := time.Unix(1_800_000_000, 0)
	require.False(t, nilCircuit.isBlocked(1, base))
	require.False(t, nilCircuit.recordSuccess(1))
	tripped, until := nilCircuit.recordFailure(1, base)
	require.False(t, tripped)
	require.Zero(t, until)

	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 1,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		maxEntries:       4,
	})
	require.False(t, circuit.isBlocked(0, base))
	require.False(t, circuit.recordSuccess(0))
	require.False(t, circuit.recordSuccess(99))

	tripped, until = circuit.recordFailure(1, base)
	require.True(t, tripped)
	require.True(t, circuit.isBlocked(1, base.Add(time.Second)))
	tripped, blockedUntil := circuit.recordFailure(1, base.Add(2*time.Second))
	require.False(t, tripped)
	require.Equal(t, until, blockedUntil)
	require.True(t, circuit.recordSuccess(1))
	require.False(t, circuit.isBlocked(1, base.Add(2*time.Second)))

	// A timestamp before the current window must start a fresh observation.
	window := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 2,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		maxEntries:       4,
	})
	tripped, _ = window.recordFailure(2, base.Add(10*time.Second))
	require.False(t, tripped)
	tripped, _ = window.recordFailure(2, base)
	require.False(t, tripped)
}

func TestOpenAIProxyStreamCircuitEvictsStaleAndExpiredEntries(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 2,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		maxEntries:       2,
	})
	circuit.recordFailure(1, base)
	circuit.recordFailure(2, base)
	circuit.recordFailure(3, base.Add(2*time.Minute))

	circuit.mu.Lock()
	_, staleOnePresent := circuit.entries[1]
	_, staleTwoPresent := circuit.entries[2]
	_, freshPresent := circuit.entries[3]
	circuit.mu.Unlock()
	require.False(t, staleOnePresent)
	require.False(t, staleTwoPresent)
	require.True(t, freshPresent)

	blocked := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 1,
		failureWindow:    time.Minute,
		quarantineTTL:    time.Minute,
		maxEntries:       2,
	})
	blocked.recordFailure(1, base)
	blocked.recordFailure(2, base)
	blocked.recordFailure(3, base.Add(2*time.Minute))
	require.False(t, blocked.isBlocked(1, base.Add(2*time.Minute)))
	require.False(t, blocked.isBlocked(2, base.Add(2*time.Minute)))
	require.True(t, blocked.isBlocked(3, base.Add(2*time.Minute)))
}

func TestOpenAIProxyStreamCircuitServiceGuards(t *testing.T) {
	var nilService *OpenAIGatewayService
	require.Nil(t, nilService.getOpenAIProxyStreamCircuit())

	proxyID := int64(4777)
	account := &Account{ID: 4778, Platform: PlatformOpenAI, ProxyID: &proxyID}
	service := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		OpenAIProxyStreamCircuit: config.GatewayOpenAIProxyStreamCircuitConfig{FailureThreshold: 1},
	}}}
	service.recordOpenAIProxyStreamDisconnect(nil, io.ErrUnexpectedEOF, "")
	service.recordOpenAIProxyStreamDisconnect(account, nil, "")
	service.recordOpenAIProxyStreamDisconnect(account, context.Canceled, "")
	service.recordOpenAIProxyStreamDisconnect(account, context.DeadlineExceeded, "")
	service.recordOpenAIProxyStreamDisconnect(account, errors.New("disconnect"), "rid")
	require.True(t, service.isOpenAIProxyStreamQuarantined(account))
	service.clearOpenAIProxyStreamDisconnect(account)
	require.False(t, service.isOpenAIProxyStreamQuarantined(account))

	otherPlatform := &Account{ID: 4779, Platform: PlatformGrok, ProxyID: &proxyID}
	service.recordOpenAIProxyStreamDisconnect(otherPlatform, errors.New("disconnect"), "rid")
	require.False(t, service.isOpenAIProxyStreamQuarantined(otherPlatform))
}

func TestOpenAIProxyStreamCircuitThresholdTTLAndSuccessReset(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 2,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		maxEntries:       16,
	})

	tripped, _ := circuit.recordFailure(1, base)
	require.False(t, tripped)
	require.False(t, circuit.isBlocked(1, base))
	require.True(t, circuit.recordSuccess(1))

	tripped, _ = circuit.recordFailure(1, base.Add(10*time.Second))
	require.False(t, tripped, "success must clear the previous failure observation")
	tripped, until := circuit.recordFailure(1, base.Add(20*time.Second))
	require.True(t, tripped)
	require.Equal(t, base.Add(20*time.Second+10*time.Minute), until)
	require.True(t, circuit.isBlocked(1, until.Add(-time.Nanosecond)))
	require.False(t, circuit.isBlocked(1, until), "TTL expiry must re-admit the proxy")

	tripped, _ = circuit.recordFailure(2, base)
	require.False(t, tripped)
	tripped, _ = circuit.recordFailure(2, base.Add(2*time.Minute))
	require.False(t, tripped, "failures outside the window must not accumulate")
}

func TestOpenAIProxyStreamCircuitBoundsEntries(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 1,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		maxEntries:       2,
	})

	circuit.recordFailure(1, base)
	circuit.recordFailure(2, base.Add(time.Second))
	circuit.recordFailure(3, base.Add(2*time.Second))

	circuit.mu.Lock()
	defer circuit.mu.Unlock()
	require.Len(t, circuit.entries, 2)
	_, oldestRetained := circuit.entries[1]
	require.False(t, oldestRetained, "the oldest entry must be evicted at the bound")
}
