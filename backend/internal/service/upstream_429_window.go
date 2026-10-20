package service

import (
	"sync"
	"time"
)

const (
	upstream429Window              = 30 * time.Second
	upstream429MinAttempts         = 10
	upstream429RatioThreshold      = 0.5
	upstream429SwitchDecisionTTL   = 5 * time.Second
	upstream429MaxEventsPerAccount = 256
	upstream429RatioAttemptLimit   = upstream429MinAttempts * 2
)

type upstream429EventKind uint8

const (
	upstream429Attempt upstream429EventKind = iota
	upstream429Hit
)

type upstream429Event struct {
	at   time.Time
	kind upstream429EventKind
}

type upstream429Decision struct {
	allowSwitch bool
	forceSwitch bool
	expiresAt   time.Time
}

type upstream429Tracker struct {
	mu        sync.Mutex
	events    map[int64][]upstream429Event
	decisions map[int64]upstream429Decision
	now       func() time.Time
}

func newUpstream429Tracker() *upstream429Tracker {
	return &upstream429Tracker{
		events:    make(map[int64][]upstream429Event),
		decisions: make(map[int64]upstream429Decision),
		now:       time.Now,
	}
}

var defaultUpstream429Tracker = newUpstream429Tracker()

func recordUpstream429Attempt(accountID int64) {
	defaultUpstream429Tracker.record(accountID, upstream429Attempt)
}

func recordUpstream429AndShouldSwitch(accountID int64, forceSwitch bool) bool {
	return defaultUpstream429Tracker.record429(accountID, forceSwitch)
}

func ShouldSwitchAccountOn429(accountID int64) bool {
	return defaultUpstream429Tracker.shouldSwitch(accountID)
}

func resetUpstream429TrackerForTest() {
	defaultUpstream429Tracker = newUpstream429Tracker()
}

func (t *upstream429Tracker) record(accountID int64, kind upstream429EventKind) {
	if accountID <= 0 {
		return
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()

	events := t.pruneLocked(accountID, now)
	events = append(events, upstream429Event{at: now, kind: kind})
	if len(events) > upstream429MaxEventsPerAccount {
		events = events[len(events)-upstream429MaxEventsPerAccount:]
	}
	t.events[accountID] = events
	t.refreshRatioDecisionLocked(accountID, now, events)
}

func (t *upstream429Tracker) record429(accountID int64, forceSwitch bool) bool {
	if accountID <= 0 {
		return false
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()

	events := t.pruneLocked(accountID, now)
	events = append(events, upstream429Event{at: now, kind: upstream429Hit})
	if len(events) > upstream429MaxEventsPerAccount {
		events = events[len(events)-upstream429MaxEventsPerAccount:]
	}
	t.events[accountID] = events

	allow := forceSwitch || t.ratioExceededLocked(events)
	existingForce := false
	if existing, ok := t.decisions[accountID]; ok && existing.forceSwitch && now.Before(existing.expiresAt) {
		allow = true
		existingForce = true
	}
	t.decisions[accountID] = upstream429Decision{
		allowSwitch: allow,
		forceSwitch: forceSwitch || existingForce,
		expiresAt:   now.Add(upstream429SwitchDecisionTTL),
	}
	return allow
}

func (t *upstream429Tracker) shouldSwitch(accountID int64) bool {
	if accountID <= 0 {
		return false
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()

	decision, ok := t.decisions[accountID]
	if !ok || now.After(decision.expiresAt) {
		delete(t.decisions, accountID)
		return false
	}
	return decision.allowSwitch
}

func (t *upstream429Tracker) refreshRatioDecisionLocked(accountID int64, now time.Time, events []upstream429Event) {
	decision, ok := t.decisions[accountID]
	if !ok {
		return
	}
	if now.After(decision.expiresAt) {
		delete(t.decisions, accountID)
		return
	}
	if decision.forceSwitch || decision.allowSwitch {
		return
	}
	if !t.ratioExceededLocked(events) {
		delete(t.decisions, accountID)
	}
}

func (t *upstream429Tracker) pruneLocked(accountID int64, now time.Time) []upstream429Event {
	events := t.events[accountID]
	if len(events) == 0 {
		return nil
	}
	cutoff := now.Add(-upstream429Window)
	keepFrom := 0
	for keepFrom < len(events) && events[keepFrom].at.Before(cutoff) {
		keepFrom++
	}
	if keepFrom > 0 {
		events = append([]upstream429Event(nil), events[keepFrom:]...)
	}
	return events
}

func (t *upstream429Tracker) ratioExceededLocked(events []upstream429Event) bool {
	events = recentUpstream429AttemptWindow(events, upstream429RatioAttemptLimit)
	attempts := 0
	hits := 0
	for _, event := range events {
		switch event.kind {
		case upstream429Attempt:
			attempts++
		case upstream429Hit:
			hits++
		}
	}
	if attempts < upstream429MinAttempts || hits == 0 {
		return false
	}
	return float64(hits)/float64(attempts) >= upstream429RatioThreshold
}

func recentUpstream429AttemptWindow(events []upstream429Event, maxAttempts int) []upstream429Event {
	if len(events) == 0 || maxAttempts <= 0 {
		return nil
	}
	attempts := 0
	start := 0
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].kind != upstream429Attempt {
			continue
		}
		attempts++
		if attempts >= maxAttempts {
			start = i
			break
		}
	}
	return events[start:]
}
