//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpsMetricsCollector_CollectSystemStats_PopulatesMemoryFromCgroupOrHost
// exercises the glue added in item11 between collectSystemStats and
// resolveMemoryStats (readCgroupMemoryBytes + the host-metrics fallback
// query + assigning the resolved trio onto the result). resolveMemoryStats
// itself already has exhaustive table coverage in
// ops_metrics_collector_memory_test.go; this test only needs to prove the
// wiring in collectSystemStats actually calls it and returns something
// self-consistent.
//
// Assertions are deliberately loose about *which* source (cgroup vs host)
// served the reading, since that depends on the CI container's cgroup
// availability — but "used" must always be populated one way or another, and
// "total"/"percent" must be either both present or both absent (never mixed).
func TestOpsMetricsCollector_CollectSystemStats_PopulatesMemoryFromCgroupOrHost(t *testing.T) {
	c := &OpsMetricsCollector{}

	stats, err := c.collectSystemStats(context.Background())

	require.NoError(t, err)
	require.NotNil(t, stats)
	require.NotNil(t, stats.memoryUsedMB, "memoryUsedMB should be populated from either cgroup or host metrics")
	if stats.memoryTotalMB != nil {
		require.NotNil(t, stats.memoryUsagePercent, "a resolved total must come with a resolved percent")
	} else {
		require.Nil(t, stats.memoryUsagePercent, "an absent total should not leave a stale percent")
	}
}

// TestOpsMetricsCollector_CollectSystemStats_NilContextDoesNotPanic covers
// the ctx==nil guard at the top of collectSystemStats, which the memory/CPU
// gopsutil calls in the same method depend on.
func TestOpsMetricsCollector_CollectSystemStats_NilContextDoesNotPanic(t *testing.T) {
	c := &OpsMetricsCollector{}

	require.NotPanics(t, func() {
		_, err := c.collectSystemStats(nil) //nolint:staticcheck // deliberately exercising the nil-ctx guard
		require.NoError(t, err)
	})
}
