package service

import (
	"encoding/json"
	"math"
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

func TestParseUpstreamBillingProbeResponseRejectsMalformedAndIncompleteResponses(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]any
	}{
		{name: "invalid json"},
		{name: "unexpected object", overrides: map[string]any{"object": "other"}},
		{name: "unexpected schema", overrides: map[string]any{"schema_version": 2}},
		{name: "unexpected scope", overrides: map[string]any{"billing_scope": "request"}},
		{name: "missing group rate", overrides: map[string]any{"group_rate_multiplier": nil}},
		{name: "missing resolved rate", overrides: map[string]any{"resolved_rate_multiplier": nil}},
		{name: "missing peak flag", overrides: map[string]any{"peak_rate_enabled": nil}},
		{name: "missing effective rate", overrides: map[string]any{"effective_rate_multiplier": nil}},
		{name: "invalid group rate", overrides: map[string]any{"group_rate_multiplier": -1}},
		{name: "invalid resolved rate", overrides: map[string]any{"resolved_rate_multiplier": -1}},
		{name: "invalid effective rate", overrides: map[string]any{"effective_rate_multiplier": -1}},
		{name: "invalid user rate", overrides: map[string]any{"user_rate_multiplier": -1}},
		{name: "invalid observed time", overrides: map[string]any{"observed_at": "invalid"}},
		{name: "zero observed time", overrides: map[string]any{"observed_at": "0001-01-01T00:00:00Z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			if tc.name == "invalid json" {
				body = []byte("{")
			} else {
				body = makeUpstreamBillingProbeBody(t, tc.overrides)
			}
			_, err := parseUpstreamBillingProbeResponse(body)
			require.Error(t, err)
		})
	}
}

func TestParseUpstreamBillingProbeResponseValidatesPeakDetails(t *testing.T) {
	base := map[string]any{
		"peak_rate_enabled":         true,
		"peak_start":                "09:00",
		"peak_end":                  "18:00",
		"peak_rate_multiplier":      2,
		"applied_peak_multiplier":   2,
		"effective_rate_multiplier": 0.8,
		"timezone":                  "Asia/Shanghai",
	}
	cases := []struct {
		name      string
		overrides map[string]any
	}{
		{name: "missing peak start", overrides: map[string]any{"peak_start": nil}},
		{name: "empty peak end", overrides: map[string]any{"peak_end": ""}},
		{name: "missing timezone", overrides: map[string]any{"timezone": nil}},
		{name: "invalid peak multiplier", overrides: map[string]any{"peak_rate_multiplier": -1}},
		{name: "invalid applied multiplier", overrides: map[string]any{"applied_peak_multiplier": -1}},
		{name: "invalid clock window", overrides: map[string]any{"peak_start": "18:00", "peak_end": "09:00"}},
		{name: "invalid timezone", overrides: map[string]any{"timezone": "Not/A_Timezone"}},
		{name: "inconsistent applied peak", overrides: map[string]any{"applied_peak_multiplier": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			overrides := make(map[string]any, len(base)+len(tc.overrides))
			for key, value := range base {
				overrides[key] = value
			}
			for key, value := range tc.overrides {
				overrides[key] = value
			}
			_, err := parseUpstreamBillingProbeResponse(makeUpstreamBillingProbeBody(t, overrides))
			require.Error(t, err)
		})
	}

	_, err := parseUpstreamBillingProbeResponse(makeUpstreamBillingProbeBody(t, map[string]any{
		"peak_rate_enabled":         false,
		"applied_peak_multiplier":   2,
		"effective_rate_multiplier": 0.4,
	}))
	require.Error(t, err)
}

func TestUpstreamBillingRateAtRejectsInvalidData(t *testing.T) {
	valid := map[string]any{
		"billing_scope":            "token",
		"resolved_rate_multiplier": 0.5,
		"peak_rate_enabled":        false,
	}
	cases := []struct {
		name string
		data map[string]any
	}{
		{name: "wrong scope", data: map[string]any{"billing_scope": "request", "resolved_rate_multiplier": 1, "peak_rate_enabled": false}},
		{name: "missing base rate", data: map[string]any{"billing_scope": "token", "peak_rate_enabled": false}},
		{name: "negative base rate", data: map[string]any{"billing_scope": "token", "resolved_rate_multiplier": -1, "peak_rate_enabled": false}},
		{name: "nan base rate", data: map[string]any{"billing_scope": "token", "resolved_rate_multiplier": math.NaN(), "peak_rate_enabled": false}},
		{name: "missing peak flag", data: map[string]any{"billing_scope": "token", "resolved_rate_multiplier": 1}},
		{name: "invalid peak data", data: map[string]any{"billing_scope": "token", "resolved_rate_multiplier": 1, "peak_rate_enabled": true, "peak_start": "09:00", "peak_end": "18:00", "peak_rate_multiplier": -1, "timezone": "UTC"}},
		{name: "overflow after peak", data: map[string]any{"billing_scope": "token", "resolved_rate_multiplier": math.MaxFloat64, "peak_rate_enabled": true, "peak_start": "00:00", "peak_end": "23:59", "peak_rate_multiplier": math.MaxFloat64, "timezone": "UTC"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := upstreamBillingRateAt(tc.data, time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))
			require.False(t, ok)
		})
	}

	rate, ok := upstreamBillingRateAt(valid, time.Now())
	require.True(t, ok)
	require.InDelta(t, 0.5, rate, 1e-12)
}

func TestUpstreamBillingPeakMultiplierAtRejectsInvalidWindows(t *testing.T) {
	valid := map[string]any{
		"peak_rate_enabled":    true,
		"peak_start":           "09:00",
		"peak_end":             "18:00",
		"peak_rate_multiplier": 2,
		"timezone":             "UTC",
	}
	cases := []struct {
		name string
		data map[string]any
	}{
		{name: "missing enabled", data: map[string]any{}},
		{name: "wrong enabled type", data: map[string]any{"peak_rate_enabled": "true"}},
		{name: "missing start", data: map[string]any{"peak_rate_enabled": true, "peak_end": "18:00", "peak_rate_multiplier": 2, "timezone": "UTC"}},
		{name: "invalid start", data: map[string]any{"peak_rate_enabled": true, "peak_start": "24:00", "peak_end": "18:00", "peak_rate_multiplier": 2, "timezone": "UTC"}},
		{name: "invalid end", data: map[string]any{"peak_rate_enabled": true, "peak_start": "09:00", "peak_end": "18:60", "peak_rate_multiplier": 2, "timezone": "UTC"}},
		{name: "same start and end", data: map[string]any{"peak_rate_enabled": true, "peak_start": "09:00", "peak_end": "09:00", "peak_rate_multiplier": 2, "timezone": "UTC"}},
		{name: "invalid timezone", data: map[string]any{"peak_rate_enabled": true, "peak_start": "09:00", "peak_end": "18:00", "peak_rate_multiplier": 2, "timezone": "Invalid/Zone"}},
		{name: "negative multiplier", data: map[string]any{"peak_rate_enabled": true, "peak_start": "09:00", "peak_end": "18:00", "peak_rate_multiplier": -1, "timezone": "UTC"}},
		{name: "nan multiplier", data: map[string]any{"peak_rate_enabled": true, "peak_start": "09:00", "peak_end": "18:00", "peak_rate_multiplier": math.NaN(), "timezone": "UTC"}},
		{name: "infinite multiplier", data: map[string]any{"peak_rate_enabled": true, "peak_start": "09:00", "peak_end": "18:00", "peak_rate_multiplier": math.Inf(1), "timezone": "UTC"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := upstreamBillingPeakMultiplierAt(tc.data, time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))
			require.False(t, ok)
		})
	}

	rate, ok := upstreamBillingPeakMultiplierAt(map[string]any{"peak_rate_enabled": false}, time.Now())
	require.True(t, ok)
	require.Equal(t, float64(1), rate)
	rate, ok = upstreamBillingPeakMultiplierAt(valid, time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC))
	require.True(t, ok)
	require.Equal(t, float64(2), rate)
	rate, ok = upstreamBillingPeakMultiplierAt(valid, time.Date(2026, 7, 13, 20, 0, 0, 0, time.UTC))
	require.True(t, ok)
	require.Equal(t, float64(1), rate)
}

func TestDecodeUpstreamBillingProbeSnapshotHandlesInvalidValues(t *testing.T) {
	received := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	valid := func(status string) map[string]any {
		return map[string]any{
			UpstreamBillingProbeExtraKey: map[string]any{
				"status":          status,
				"last_attempt_at": received,
				"next_probe_at":   received.Add(time.Hour),
			},
		}
	}
	require.Nil(t, decodeUpstreamBillingProbeSnapshot(nil))
	require.Nil(t, decodeUpstreamBillingProbeSnapshot(map[string]any{}))
	require.Nil(t, decodeUpstreamBillingProbeSnapshot(map[string]any{UpstreamBillingProbeExtraKey: func() {}}))
	require.Nil(t, decodeUpstreamBillingProbeSnapshot(map[string]any{UpstreamBillingProbeExtraKey: "invalid"}))
	require.Nil(t, decodeUpstreamBillingProbeSnapshot(map[string]any{UpstreamBillingProbeExtraKey: map[string]any{}}))
	require.NotNil(t, decodeUpstreamBillingProbeSnapshot(valid(UpstreamBillingProbeStatusUnsupported)))
	require.NotNil(t, decodeUpstreamBillingProbeSnapshot(valid(UpstreamBillingProbeStatusFailed)))
}

func TestResolveUpstreamBillingNumberSupportsJSONNumberTypes(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  float64
	}{
		{name: "float64", value: float64(1.1), want: 1.1},
		{name: "float32", value: float32(1.2), want: float64(float32(1.2))},
		{name: "int", value: int(2), want: 2},
		{name: "int64", value: int64(3), want: 3},
		{name: "json number", value: json.Number("4.5"), want: 4.5},
		{name: "string", value: " 5.5 ", want: 5.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, ok := resolveUpstreamBillingNumber(map[string]any{"rate": tc.value}, "rate")
			require.True(t, ok)
			require.InDelta(t, tc.want, value, 1e-6)
		})
	}
	value, ok := resolveUpstreamBillingNumber(map[string]any{"first": "invalid", "second": 6}, "first", "second")
	require.True(t, ok)
	require.Equal(t, float64(6), value)
	for _, invalid := range []any{nil, true, "invalid", json.Number("invalid")} {
		_, ok := resolveUpstreamBillingNumber(map[string]any{"rate": invalid}, "rate")
		require.False(t, ok)
	}
	_, ok = resolveUpstreamBillingNumber(map[string]any{}, "missing")
	require.False(t, ok)
}

func TestUpstreamBillingHelpersValidateClocksAndMultipliers(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
		valid bool
	}{
		{value: "00:00", want: 0, valid: true},
		{value: "23:59", want: 1439, valid: true},
		{value: "1", valid: false},
		{value: "x:00", valid: false},
		{value: "24:00", valid: false},
		{value: "12:60", valid: false},
	} {
		minute, ok := parseUpstreamBillingClockMinutes(tc.value)
		require.Equal(t, tc.valid, ok, tc.value)
		require.Equal(t, tc.want, minute, tc.value)
	}
	for _, tc := range []struct {
		left, right float64
		equal       bool
	}{
		{left: 1, right: 1, equal: true},
		{left: 1, right: 1 + 1e-10, equal: true},
		{left: 1, right: 1.1, equal: false},
		{left: math.NaN(), right: 1, equal: false},
		{left: 1, right: math.Inf(1), equal: false},
	} {
		require.Equal(t, tc.equal, equalUpstreamBillingMultiplier(tc.left, tc.right))
	}
	for _, tc := range []struct {
		value float64
		valid bool
	}{
		{value: 0, valid: true},
		{value: 1, valid: true},
		{value: -1, valid: false},
		{value: math.NaN(), valid: false},
		{value: math.Inf(1), valid: false},
	} {
		require.Equal(t, tc.valid, validUpstreamBillingMultiplier(tc.value))
	}
}

func makeUpstreamBillingProbeBody(t *testing.T, overrides map[string]any) []byte {
	t.Helper()
	payload := map[string]any{
		"object":                    "sub2api.key_billing",
		"schema_version":            1,
		"billing_scope":             "token",
		"group_rate_multiplier":     0.8,
		"user_rate_multiplier":      0.5,
		"resolved_rate_multiplier":  0.5,
		"peak_rate_enabled":         false,
		"effective_rate_multiplier": 0.5,
		"observed_at":               "2026-07-13T01:00:00Z",
	}
	for key, value := range overrides {
		payload[key] = value
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	return body
}
