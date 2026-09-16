package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

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

func TestOpenAIProxyStreamCircuitCollapsesBurstFailures(t *testing.T) {
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
	require.False(t, tripped, "burst failures inside the collapse interval must merge")
	tripped, _ = circuit.recordFailure(1, base.Add(2*time.Second))
	require.False(t, tripped, "burst failures inside the collapse interval must merge")
	require.False(t, circuit.isBlocked(1, base.Add(2*time.Second)))

	tripped, _ = circuit.recordFailure(1, base.Add(5*time.Second))
	require.True(t, tripped)
	require.True(t, circuit.isBlocked(1, base.Add(5*time.Second)))
}

func TestOpenAIProxyStreamCircuitDisabled(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		disabled:         true,
		failureThreshold: 1,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		maxEntries:       16,
	})
	tripped, _ := circuit.recordFailure(1, base)
	require.False(t, tripped)
	require.False(t, circuit.isBlocked(1, base))
	require.Equal(t, 0, circuit.activeBlockCount(base))
}

func TestOpenAIProxyStreamCircuitActiveBlockCount(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 1,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		maxEntries:       16,
	})

	require.Equal(t, 0, circuit.activeBlockCount(base))
	tripped, until := circuit.recordFailure(1, base)
	require.True(t, tripped)
	circuit.recordFailure(2, base)
	require.Equal(t, 2, circuit.activeBlockCount(base.Add(time.Second)))
	require.Equal(t, 0, circuit.activeBlockCount(until), "expired quarantines must not count")
}

func TestOpenAIProxyStreamQuarantineBypassContext(t *testing.T) {
	proxyID := int64(7)
	account := &Account{ID: 1, Platform: PlatformOpenAI, ProxyID: &proxyID}
	svc := &OpenAIGatewayService{}
	svc.openaiProxyStreamCircuit = newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 1,
		failureWindow:    time.Minute,
		quarantineTTL:    10 * time.Minute,
		maxEntries:       16,
	})
	svc.openaiProxyStreamCircuit.recordFailure(proxyID, time.Now())

	ctx := context.Background()
	require.True(t, svc.isOpenAIProxyStreamQuarantined(ctx, account))
	require.False(t, svc.isOpenAIProxyStreamQuarantined(withOpenAIProxyStreamQuarantineBypass(ctx), account))
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

func TestOpenAIProxyStreamCircuitSettingsAndGuards(t *testing.T) {
	defaults := resolveOpenAIProxyStreamCircuitSettings(nil)
	require.Equal(t, defaultOpenAIProxyStreamFailureThreshold, defaults.failureThreshold)
	require.Equal(t, defaultOpenAIProxyStreamFailureWindow, defaults.failureWindow)

	cfg := &config.Config{}
	cfg.Gateway.OpenAIProxyStreamCircuit.Disabled = true
	cfg.Gateway.OpenAIProxyStreamCircuit.FailureThreshold = 3
	cfg.Gateway.OpenAIProxyStreamCircuit.WindowSeconds = 2
	cfg.Gateway.OpenAIProxyStreamCircuit.TTLSeconds = 4
	svc := &OpenAIGatewayService{cfg: cfg}
	settings := resolveOpenAIProxyStreamCircuitSettings(svc)
	require.True(t, settings.disabled)
	require.Equal(t, 3, settings.failureThreshold)
	require.Equal(t, 2*time.Second, settings.failureWindow)
	require.Equal(t, 4*time.Second, settings.quarantineTTL)

	normalized := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{collapseInterval: -time.Second})
	require.Equal(t, defaultOpenAIProxyStreamFailureThreshold, normalized.settings.failureThreshold)
	require.Equal(t, time.Duration(0), normalized.settings.collapseInterval)
	require.Nil(t, (*OpenAIGatewayService)(nil).getOpenAIProxyStreamCircuit())
	require.Same(t, svc.getOpenAIProxyStreamCircuit(), svc.getOpenAIProxyStreamCircuit())

	base := time.Unix(1_800_000_000, 0)
	tripped, _ := normalized.recordFailure(1, base)
	require.False(t, tripped)
	tripped, until := normalized.recordFailure(1, base.Add(time.Second))
	require.True(t, tripped)
	tripped, sameUntil := normalized.recordFailure(1, base.Add(2*time.Second))
	require.False(t, tripped)
	require.Equal(t, until, sameUntil)
	require.False(t, normalized.recordSuccess(999))
	require.False(t, normalized.recordSuccess(0))
	require.False(t, normalized.isBlocked(0, base))
	require.False(t, (*openAIProxyStreamCircuit)(nil).isBlocked(1, base))
}

func TestOpenAIProxyStreamCircuitPrunesAndClassifiesDisconnects(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 1,
		failureWindow:    time.Minute,
		quarantineTTL:    time.Minute,
		maxEntries:       2,
	})
	circuit.entries[1] = openAIProxyStreamCircuitEntry{lastTouched: base.Add(-2 * time.Minute)}
	circuit.entries[2] = openAIProxyStreamCircuitEntry{blockedUntil: base}
	circuit.recordFailure(3, base)
	require.Len(t, circuit.entries, 1)
	require.True(t, circuit.isBlocked(3, base))

	proxyID := int64(3)
	account := &Account{ID: 3, Platform: PlatformOpenAI, ProxyID: &proxyID}
	gotID, ok := openAIProxyStreamCircuitProxyID(account)
	require.True(t, ok)
	require.Equal(t, proxyID, gotID)
	_, ok = openAIProxyStreamCircuitProxyID(&Account{Platform: PlatformAnthropic, ProxyID: &proxyID})
	require.False(t, ok)
	_, ok = openAIProxyStreamCircuitProxyID(nil)
	require.False(t, ok)

	svc := &OpenAIGatewayService{openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
		failureThreshold: 1, failureWindow: time.Minute, quarantineTTL: time.Minute, maxEntries: 2,
	})}
	svc.recordOpenAIProxyStreamDisconnect(account, context.Canceled, "rid")
	require.False(t, svc.isOpenAIProxyStreamQuarantined(context.Background(), account))
	svc.recordOpenAIProxyStreamDisconnect(account, errors.New("connection reset"), "rid")
	require.True(t, svc.isOpenAIProxyStreamQuarantined(context.Background(), account))
	svc.clearOpenAIProxyStreamDisconnect(account)
	require.False(t, svc.isOpenAIProxyStreamQuarantined(context.Background(), account))
	require.False(t, openAIProxyStreamQuarantineBypassed(nil))
}
