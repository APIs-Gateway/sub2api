//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseOpenCodeGoUsageLimitResetDuration(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    time.Duration
	}{
		{name: "no reset phrase", message: "Weekly usage limit reached.", want: 0},
		{name: "reset phrase without duration", message: "Weekly usage limit reached. Resets in a moment.", want: 0},
		{name: "seconds", message: "Resets in 30s.", want: 30 * time.Second},
		{name: "secs word", message: "Resets in 45 secs", want: 45 * time.Second},
		{name: "minutes", message: "Resets in 5 minutes.", want: 5 * time.Minute},
		{name: "hrs", message: "Resets in 3 hrs", want: 3 * time.Hour},
		{name: "fractional hours", message: "Resets in 1.5 hours", want: 90 * time.Minute},
		{name: "day singular", message: "Resets in 1 day.", want: 24 * time.Hour},
		{name: "weeks", message: "Resets in 2 weeks.", want: 14 * 24 * time.Hour},
		{name: "case insensitive composite", message: "RESETS IN 1W 2D 3H 4M 5S", want: 7*24*time.Hour + 2*24*time.Hour + 3*time.Hour + 4*time.Minute + 5*time.Second},
		{name: "zero value rejected", message: "Resets in 0 days.", want: 0},
		{name: "sub-nanosecond part rejected", message: "Resets in 0.0000000001s", want: 0},
		{name: "single part overflow rejected", message: "Resets in 99999999999 weeks", want: 0},
		{name: "composite overflow rejected", message: "Resets in 10000 weeks 10000 weeks", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parseOpenCodeGoUsageLimitResetDuration(tt.message))
		})
	}
}

func TestOpenCodeGoUsageLimitDurationUnit(t *testing.T) {
	require.Equal(t, time.Second, openCodeGoUsageLimitDurationUnit("SECONDS"))
	require.Equal(t, time.Minute, openCodeGoUsageLimitDurationUnit("min"))
	require.Equal(t, time.Hour, openCodeGoUsageLimitDurationUnit("hr"))
	require.Equal(t, 24*time.Hour, openCodeGoUsageLimitDurationUnit("days"))
	require.Equal(t, 7*24*time.Hour, openCodeGoUsageLimitDurationUnit("w"))
	require.Equal(t, time.Duration(0), openCodeGoUsageLimitDurationUnit("fortnight"))
}

func TestParseOpenAIRateLimitResetTime_OpenCodeGoWithoutParsableReset(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"GoUsageLimitError","message":"Weekly usage limit reached."}}`)

	require.Nil(t, parseOpenAIRateLimitResetTime(body))
}
