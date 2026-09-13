package admin

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestUsageStatsCacheKey_StableAndDistinct(t *testing.T) {
	start := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	base := usagestats.UsageLogFilters{StartTime: &start, EndTime: &end, Model: "claude-3"}

	k1 := usageStatsCacheKey(base)
	k2 := usageStatsCacheKey(base)
	require.NotEmpty(t, k1)
	require.Equal(t, k1, k2, "same filters must produce same key")

	other := base
	other.Model = "gpt-4o"
	require.NotEqual(t, k1, usageStatsCacheKey(other), "different model must change key")

	withUser := base
	withUser.UserID = 7
	require.NotEqual(t, k1, usageStatsCacheKey(withUser), "different user must change key")
}

func TestUsageStatsCacheKey_DistinctByUpstreamModelMismatch(t *testing.T) {
	start := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	base := usagestats.UsageLogFilters{StartTime: &start, EndTime: &end, Model: "claude-3"}

	trueVal := true
	falseVal := false

	nilFilters := base
	trueFilters := base
	trueFilters.UpstreamModelMismatch = &trueVal
	falseFilters := base
	falseFilters.UpstreamModelMismatch = &falseVal

	keyNil := usageStatsCacheKey(nilFilters)
	keyTrue := usageStatsCacheKey(trueFilters)
	keyFalse := usageStatsCacheKey(falseFilters)

	require.NotEqual(t, keyNil, keyTrue, "nil vs true must change key")
	require.NotEqual(t, keyNil, keyFalse, "nil vs false must change key")
	require.NotEqual(t, keyTrue, keyFalse, "true vs false must change key")

	// Same filters (including UpstreamModelMismatch) must still produce a stable, equal key.
	require.Equal(t, keyNil, usageStatsCacheKey(base))
	require.Equal(t, keyTrue, usageStatsCacheKey(trueFilters))
	require.Equal(t, keyFalse, usageStatsCacheKey(falseFilters))
}
