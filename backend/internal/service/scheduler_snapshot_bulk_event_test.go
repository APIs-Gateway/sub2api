//go:build unit

package service

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// bulkEventCaptureCache only records which buckets handleBulkAccountEvent asked
// to rebuild, then returns ErrSchedulerBucketRetired to short-circuit before the
// full write pipeline (already covered by scheduler_snapshot_full_rebuild_test.go
// and scheduler_snapshot_retirement_test.go).
type bulkEventCaptureCache struct {
	SchedulerCache

	mu       sync.Mutex
	captures []SchedulerBucket
}

func (c *bulkEventCaptureCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	c.captures = append(c.captures, bucket)
	c.mu.Unlock()
	return SchedulerBucketWriteToken{}, ErrSchedulerBucketRetired
}

func (c *bulkEventCaptureCache) SetAccount(context.Context, *Account) error { return nil }

func (c *bulkEventCaptureCache) DeleteAccount(context.Context, int64) error { return nil }

func (c *bulkEventCaptureCache) capturedBuckets() []SchedulerBucket {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]SchedulerBucket(nil), c.captures...)
}

func newBulkEventTestService(cache SchedulerCache, accounts AccountRepository) *SchedulerSnapshotService {
	return NewSchedulerSnapshotService(cache, nil, accounts, nil, &config.Config{RunMode: config.RunModeStandard})
}

func sortedBuckets(buckets []SchedulerBucket) []SchedulerBucket {
	out := append([]SchedulerBucket(nil), buckets...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Platform != out[j].Platform {
			return out[i].Platform < out[j].Platform
		}
		if out[i].GroupID != out[j].GroupID {
			return out[i].GroupID < out[j].GroupID
		}
		return out[i].Mode < out[j].Mode
	})
	return out
}

func TestHandleBulkAccountEvent_ScopesRebuildToAccountPlatform(t *testing.T) {
	cache := &bulkEventCaptureCache{}
	accounts := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, GroupIDs: []int64{10}},
	}}
	svc := newBulkEventTestService(cache, accounts)

	err := svc.handleBulkAccountEvent(context.Background(), map[string]any{
		"account_ids": []any{int64(1)},
	}, map[batchSeenKey]struct{}{})

	require.NoError(t, err)
	got := sortedBuckets(cache.capturedBuckets())
	want := sortedBuckets([]SchedulerBucket{
		{GroupID: 10, Platform: PlatformOpenAI, Mode: SchedulerModeSingle},
		{GroupID: 10, Platform: PlatformOpenAI, Mode: SchedulerModeForced},
	})
	require.Equal(t, want, got)
}

func TestHandleBulkAccountEvent_AntigravityAccountFansOutToCompatPlatforms(t *testing.T) {
	cache := &bulkEventCaptureCache{}
	accounts := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{
		2: {ID: 2, Platform: PlatformAntigravity, GroupIDs: []int64{20}},
	}}
	svc := newBulkEventTestService(cache, accounts)

	err := svc.handleBulkAccountEvent(context.Background(), map[string]any{
		"account_ids": []any{int64(2)},
	}, map[batchSeenKey]struct{}{})

	require.NoError(t, err)
	got := sortedBuckets(cache.capturedBuckets())
	want := sortedBuckets([]SchedulerBucket{
		{GroupID: 20, Platform: PlatformAntigravity, Mode: SchedulerModeSingle},
		{GroupID: 20, Platform: PlatformAntigravity, Mode: SchedulerModeForced},
		{GroupID: 20, Platform: PlatformAnthropic, Mode: SchedulerModeSingle},
		{GroupID: 20, Platform: PlatformAnthropic, Mode: SchedulerModeForced},
		{GroupID: 20, Platform: PlatformAnthropic, Mode: SchedulerModeMixed},
		{GroupID: 20, Platform: PlatformGemini, Mode: SchedulerModeSingle},
		{GroupID: 20, Platform: PlatformGemini, Mode: SchedulerModeForced},
		{GroupID: 20, Platform: PlatformGemini, Mode: SchedulerModeMixed},
	})
	require.Equal(t, want, got)
}

func TestHandleBulkAccountEvent_MissingAccountFallsBackToAllPlatforms(t *testing.T) {
	cache := &bulkEventCaptureCache{}
	// Account 3 isn't in the repo (e.g. already deleted), so its original
	// platform can't be determined -> must fall back to the blanket rebuild.
	accounts := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{}}
	svc := newBulkEventTestService(cache, accounts)

	err := svc.handleBulkAccountEvent(context.Background(), map[string]any{
		"account_ids": []any{int64(3)},
		"group_ids":   []any{int64(30)},
	}, map[batchSeenKey]struct{}{})

	require.NoError(t, err)
	got := cache.capturedBuckets()
	seenPlatforms := make(map[string]struct{}, len(got))
	for _, b := range got {
		require.Equal(t, int64(30), b.GroupID)
		seenPlatforms[b.Platform] = struct{}{}
	}
	gotPlatforms := make([]string, 0, len(seenPlatforms))
	for p := range seenPlatforms {
		gotPlatforms = append(gotPlatforms, p)
	}
	require.ElementsMatch(t,
		[]string{PlatformAnthropic, PlatformGemini, PlatformOpenAI, PlatformAntigravity, PlatformGrok},
		gotPlatforms)
}

func TestHandleBulkAccountEvent_PreloadGroupIDsOnlyExpandKnownPlatforms(t *testing.T) {
	cache := &bulkEventCaptureCache{}
	accounts := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{
		4: {ID: 4, Platform: PlatformOpenAI, GroupIDs: []int64{40}},
	}}
	svc := newBulkEventTestService(cache, accounts)

	err := svc.handleBulkAccountEvent(context.Background(), map[string]any{
		"account_ids": []any{int64(4)},
		"group_ids":   []any{int64(41)},
	}, map[batchSeenKey]struct{}{})

	require.NoError(t, err)
	got := cache.capturedBuckets()
	gotGroups := map[int64]struct{}{}
	for _, b := range got {
		require.Equal(t, PlatformOpenAI, b.Platform, "preload group must not fan out beyond the account's own platform")
		gotGroups[b.GroupID] = struct{}{}
	}
	require.Contains(t, gotGroups, int64(40))
	require.Contains(t, gotGroups, int64(41))
}
