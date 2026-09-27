//go:build unit

package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestModelRateLimitResetFromExtra(t *testing.T) {
	const scope = "openai:image_generation"
	for _, tc := range []struct {
		name  string
		extra string
		want  string
	}{
		{name: "missing", extra: `{}`},
		{name: "invalid", extra: `{"model_rate_limits":{"openai:image_generation":{"rate_limit_reset_at":"tomorrow"}}}`},
		{name: "bad sibling ignored", extra: `{"model_rate_limits":{"other":42,"openai:image_generation":{"rate_limit_reset_at":"2026-09-29T01:02:03+08:00"}}}`, want: "2026-09-28T17:02:03Z"},
		{name: "offset", extra: `{"model_rate_limits":{"openai:image_generation":{"rate_limit_reset_at":"2026-09-29T01:02:03+08:00"}}}`, want: "2026-09-28T17:02:03Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := modelRateLimitResetFromExtra([]byte(tc.extra), scope)
			if tc.want == "" {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tc.want, got.UTC().Format(time.RFC3339))
		})
	}
}
