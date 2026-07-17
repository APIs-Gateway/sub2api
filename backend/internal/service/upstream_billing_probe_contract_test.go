package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseUpstreamBillingProbeResponseNormalizesTokenRate(t *testing.T) {
	data, err := parseUpstreamBillingProbeResponse([]byte(`{
		"object":"sub2api.key_billing",
		"schema_version":1,
		"billing_scope":"token",
		"group_rate_multiplier":0.8,
		"user_rate_multiplier":0.5,
		"resolved_rate_multiplier":0.5,
		"peak_rate_enabled":false,
		"effective_rate_multiplier":0.5,
		"observed_at":"2026-07-13T01:00:00+08:00"
	}`))

	require.NoError(t, err)
	require.Equal(t, "2026-07-12T17:00:00Z", data["observed_at"])
	rate, ok := upstreamBillingRateAt(data, time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC))
	require.True(t, ok)
	require.InDelta(t, 0.5, rate, 1e-12)
}

func TestParseUpstreamBillingProbeResponseRejectsInconsistentRates(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "resolved rate",
			body: `{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":0.8,"user_rate_multiplier":0.5,"resolved_rate_multiplier":0.8,"peak_rate_enabled":false,"effective_rate_multiplier":0.8,"observed_at":"2026-07-13T01:00:00Z"}`,
		},
		{
			name: "effective rate",
			body: `{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":0.8,"resolved_rate_multiplier":0.8,"peak_rate_enabled":false,"effective_rate_multiplier":0.5,"observed_at":"2026-07-13T01:00:00Z"}`,
		},
		{
			name: "observed time",
			body: `{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":0.8,"resolved_rate_multiplier":0.8,"peak_rate_enabled":false,"effective_rate_multiplier":0.8,"observed_at":"invalid"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseUpstreamBillingProbeResponse([]byte(tc.body))
			require.Error(t, err)
		})
	}
}

func TestParseUpstreamBillingProbeResponseValidatesPeakWindow(t *testing.T) {
	body := `{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":0.5,"resolved_rate_multiplier":0.5,"peak_rate_enabled":true,"peak_start":"09:00","peak_end":"18:00","peak_rate_multiplier":2,"applied_peak_multiplier":2,"effective_rate_multiplier":1,"timezone":"Asia/Shanghai","observed_at":"2026-07-13T10:00:00+08:00"}`
	data, err := parseUpstreamBillingProbeResponse([]byte(body))
	require.NoError(t, err)

	peakRate, ok := upstreamBillingRateAt(data, time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC))
	require.True(t, ok)
	require.InDelta(t, 1.0, peakRate, 1e-12)
	offPeakRate, ok := upstreamBillingRateAt(data, time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))
	require.True(t, ok)
	require.InDelta(t, 0.5, offPeakRate, 1e-12)

	bad := map[string]any{}
	for key, value := range data {
		bad[key] = value
	}
	bad["peak_end"] = "08:00"
	_, ok = upstreamBillingRateAt(bad, time.Now())
	require.False(t, ok)
}

func TestDecodeUpstreamBillingProbeSnapshotRejectsUnknownStatus(t *testing.T) {
	received := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	snapshot := decodeUpstreamBillingProbeSnapshot(map[string]any{
		UpstreamBillingProbeExtraKey: map[string]any{
			"status":          UpstreamBillingProbeStatusOK,
			"received_at":     received,
			"last_attempt_at": received,
			"next_probe_at":   received.Add(time.Hour),
		},
	})
	require.NotNil(t, snapshot)
	require.Equal(t, UpstreamBillingProbeStatusOK, snapshot.Status)
	require.Equal(t, received, *snapshot.ReceivedAt)

	require.Nil(t, decodeUpstreamBillingProbeSnapshot(map[string]any{
		UpstreamBillingProbeExtraKey: map[string]any{"status": "unknown"},
	}))
}

func TestIsUpstreamBillingProbeAccountOnlyAcceptsOpenAIAPIKeys(t *testing.T) {
	require.True(t, isUpstreamBillingProbeAccount(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}))
	require.False(t, isUpstreamBillingProbeAccount(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}))
	require.False(t, isUpstreamBillingProbeAccount(&Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}))
	require.False(t, isUpstreamBillingProbeAccount(nil))
}
