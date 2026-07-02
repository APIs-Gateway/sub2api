package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func countUpstream429EventsForTest(accountID int64) (attempts int, hits int) {
	defaultUpstream429Tracker.mu.Lock()
	defer defaultUpstream429Tracker.mu.Unlock()
	for _, event := range defaultUpstream429Tracker.events[accountID] {
		switch event.kind {
		case upstream429Attempt:
			attempts++
		case upstream429Hit:
			hits++
		}
	}
	return attempts, hits
}

func TestUpstream429TrackerIgnoresInvalidAccountIDs(t *testing.T) {
	resetUpstream429TrackerForTest()

	recordUpstream429Attempt(0)
	recordUpstream429Attempt(-1)
	require.False(t, recordUpstream429AndShouldSwitch(0, false))
	require.False(t, recordUpstream429AndShouldSwitch(-1, true))
	require.False(t, ShouldSwitchAccountOn429(0))
	require.False(t, ShouldSwitchAccountOn429(-1))

	require.Empty(t, defaultUpstream429Tracker.events)
	require.Empty(t, defaultUpstream429Tracker.decisions)
}

func TestUpstream429TrackerPrunesExpiredEvents(t *testing.T) {
	tracker := newUpstream429Tracker()
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }

	tracker.record(1, upstream429Attempt)
	now = now.Add(upstream429Window + time.Second)
	tracker.record(1, upstream429Attempt)

	require.Len(t, tracker.events[1], 1)
	require.Equal(t, now, tracker.events[1][0].at)
}

func TestUpstream429TrackerTrimsHitEvents(t *testing.T) {
	tracker := newUpstream429Tracker()

	for i := 0; i < upstream429MaxEventsPerAccount+1; i++ {
		tracker.record429(1, false)
	}

	require.Len(t, tracker.events[1], upstream429MaxEventsPerAccount)
}

func TestUpstream429TrackerKeepsPositiveRatioDecisionDuringTTL(t *testing.T) {
	tracker := newUpstream429Tracker()
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }

	const accountID int64 = 1
	for i := 0; i < upstream429MinAttempts; i++ {
		tracker.record(accountID, upstream429Attempt)
	}
	for i := 0; i < upstream429MinAttempts/2-1; i++ {
		require.False(t, tracker.record429(accountID, false))
	}
	require.True(t, tracker.record429(accountID, false))
	require.True(t, tracker.shouldSwitch(accountID))

	for i := 0; i < upstream429MinAttempts; i++ {
		tracker.record(accountID, upstream429Attempt)
	}

	require.True(t, tracker.shouldSwitch(accountID))

	now = now.Add(upstream429SwitchDecisionTTL + time.Millisecond)
	require.False(t, tracker.shouldSwitch(accountID))
}

func TestRecentUpstream429AttemptWindowEmptyInputs(t *testing.T) {
	require.Nil(t, recentUpstream429AttemptWindow(nil, upstream429MinAttempts))
	require.Nil(t, recentUpstream429AttemptWindow([]upstream429Event{{kind: upstream429Attempt}}, 0))
}
