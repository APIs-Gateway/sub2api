//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Fast sources expose readers arriving after a flight completes. The existing
// blocked-source test primarily exercises readers that join an active flight.
func TestMatrixPolicy_ConcurrentFastSnapshotReads(t *testing.T) {
	for _, mode := range []string{"cold", "expired", "invalidated", "cold_error", "expired_error", "invalidated_error"} {
		t.Run(mode, func(t *testing.T) {
			for round := 0; round < 8; round++ {
				src := newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: mpAllowlistSnapshot("gpt-5.4")})
				p, clock := newMPForTest(src, nil)
				warm := mode != "cold" && mode != "cold_error"
				failed := mode == "cold_error" || mode == "expired_error" || mode == "invalidated_error"
				if warm {
					require.Equal(t, PricingStageV2, p.snapshot(context.Background(), 1).stage)
				}
				switch mode {
				case "expired", "expired_error":
					clock.Advance(matrixSnapshotTTL + time.Second)
				case "invalidated", "invalidated_error":
					p.InvalidateGroups(1)
				}
				if failed {
					src.setErrors(nil, errors.New("snapshot source unavailable"))
				}
				before := src.snapCalls.Load()
				const readers = 32
				start := make(chan struct{})
				var ready, done sync.WaitGroup
				ready.Add(readers)
				done.Add(readers)
				results := make([]*matrixSnapshot, readers)
				for i := range results {
					go func(i int) {
						defer done.Done()
						ready.Done()
						<-start
						results[i] = p.snapshot(context.Background(), 1)
					}(i)
				}
				ready.Wait()
				close(start)
				done.Wait()
				require.Equal(t, before+1, src.snapCalls.Load(), "round %d: one fresh load, including late readers", round)
				for i, snap := range results {
					require.Same(t, results[0], snap, "round %d reader %d reuses the cached snapshot", round, i)
				}
				if mode == "cold_error" {
					require.Error(t, results[0].loadErr)
				} else {
					require.NoError(t, results[0].loadErr)
					require.Equal(t, PricingStageV2, results[0].stage)
					require.Equal(t, failed, results[0].stale)
				}
				// Both the normal TTL and error TTL must prevent another query.
				require.Same(t, results[0], p.snapshot(context.Background(), 1))
				require.Equal(t, before+1, src.snapCalls.Load())
			}
		})
	}
}
