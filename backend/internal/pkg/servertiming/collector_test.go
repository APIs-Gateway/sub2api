package servertiming

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCollectorHeaderValueAggregatesIntervals(t *testing.T) {
	startedAt := time.Unix(100, 0)
	collector := New(startedAt)
	collector.Record(MetricDatabase, startedAt.Add(10*time.Millisecond), startedAt.Add(40*time.Millisecond), 2)
	collector.Record(MetricRedis, startedAt.Add(30*time.Millisecond), startedAt.Add(50*time.Millisecond), 3)
	collector.Record(dependencyMetricName("openai"), startedAt.Add(70*time.Millisecond), startedAt.Add(100*time.Millisecond), 1)
	collector.Record(dependencyMetricName("github"), startedAt.Add(60*time.Millisecond), startedAt.Add(90*time.Millisecond), 1)

	got := collector.HeaderValue(startedAt.Add(120*time.Millisecond), "miss")
	want := `total;dur=120.0, app;dur=40.0, db;dur=30.0;desc="queries=2", redis;dur=20.0;desc="commands=3", cache;desc="miss", deps;dur=40.0;desc="calls=2", dep_github;dur=30.0;desc="calls=1", dep_openai;dur=30.0;desc="calls=1"`
	if got != want {
		t.Fatalf("HeaderValue() = %q, want %q", got, want)
	}
}

func TestRecordIntervalDoesNotIncrementCount(t *testing.T) {
	startedAt := time.Unix(200, 0)
	collector := New(startedAt)
	ctx := WithCollector(context.Background(), collector)

	Record(ctx, MetricDatabase, startedAt.Add(10*time.Millisecond), startedAt.Add(20*time.Millisecond), 1)
	RecordInterval(ctx, MetricDatabase, startedAt.Add(30*time.Millisecond), startedAt.Add(40*time.Millisecond))

	header := HeaderValue(ctx, startedAt.Add(100*time.Millisecond), "hit")
	if !strings.Contains(header, `db;dur=20.0;desc="queries=1"`) {
		t.Fatalf("header %q does not contain one query with both blocking intervals", header)
	}
	if !strings.Contains(header, "app;dur=80.0") {
		t.Fatalf("header %q does not subtract the interval union from app time", header)
	}
}

func TestCollectorCacheStatusFallback(t *testing.T) {
	startedAt := time.Unix(300, 0)
	collector := New(startedAt)
	ctx := WithCollector(context.Background(), collector)

	SetCacheStatus(ctx, " HIT ")
	if got := HeaderValue(ctx, startedAt.Add(time.Millisecond), "invalid"); !strings.Contains(got, `cache;desc="hit"`) {
		t.Fatalf("HeaderValue() = %q, want stored cache hit", got)
	}

	other := New(startedAt)
	if got := other.HeaderValue(startedAt.Add(time.Millisecond), "invalid"); !strings.Contains(got, `cache;desc="bypass"`) {
		t.Fatalf("HeaderValue() = %q, want cache bypass", got)
	}
}

func TestCollectorSanitizesDependencyMetric(t *testing.T) {
	startedAt := time.Unix(400, 0)
	collector := New(startedAt)
	ctx := WithCollector(context.Background(), collector)
	RecordDependency(ctx, "GitHub API\r\nInjected;dur=999", startedAt, startedAt.Add(time.Millisecond))

	header := HeaderValue(ctx, startedAt.Add(2*time.Millisecond), "bypass")
	if strings.ContainsAny(header, "\r\n") || strings.Contains(header, ";dur=999") {
		t.Fatalf("unsafe metric content reached header: %q", header)
	}
	if !strings.Contains(header, "dep_githubapiinjecteddur999;dur=1.0") {
		t.Fatalf("sanitized dependency metric missing from header: %q", header)
	}
}

func TestCollectorBoundsHeaderLength(t *testing.T) {
	startedAt := time.Unix(500, 0)
	collector := New(startedAt)
	for i := 0; i < 300; i++ {
		collector.Record(
			dependencyMetricName(fmt.Sprintf("module_%03d_with_a_deliberately_long_name", i)),
			startedAt,
			startedAt.Add(time.Millisecond),
			1,
		)
	}

	header := collector.HeaderValue(startedAt.Add(2*time.Millisecond), "bypass")
	if len(header) > maxHeaderLength {
		t.Fatalf("header length = %d, want <= %d", len(header), maxHeaderLength)
	}
	if !strings.Contains(header, "total;dur=2.0") || !strings.Contains(header, "deps;dur=1.0") {
		t.Fatalf("bounded header lost fixed metrics: %q", header)
	}
}

func TestCollectorConcurrentRecording(t *testing.T) {
	startedAt := time.Now()
	collector := New(startedAt)
	ctx := WithCollector(context.Background(), collector)

	const workers = 25
	const recordsPerWorker = 100
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < recordsPerWorker; j++ {
				Record(ctx, MetricDatabase, startedAt, startedAt.Add(time.Microsecond), 1)
			}
		}()
	}
	wg.Wait()

	header := HeaderValue(ctx, startedAt.Add(time.Millisecond), "bypass")
	want := fmt.Sprintf(`queries=%d`, workers*recordsPerWorker)
	if !strings.Contains(header, want) {
		t.Fatalf("header %q does not contain %q", header, want)
	}
}

func TestContextHelpersHandleMissingCollector(t *testing.T) {
	if Active(context.TODO()) || Active(context.Background()) {
		t.Fatal("context without collector reported active")
	}
	if got := HeaderValue(context.Background(), time.Now(), "hit"); got != "" {
		t.Fatalf("HeaderValue() = %q without collector, want empty", got)
	}
	if got := WithCollector(context.TODO(), nil); got == nil {
		t.Fatal("WithCollector(nil, nil) returned nil context")
	}
}

func TestObserveRecordsElapsedTimeOnce(t *testing.T) {
	startedAt := time.Now()
	collector := New(startedAt)
	ctx := WithCollector(context.Background(), collector)

	done := Observe(ctx, "custom-op")
	done()
	done() // second call must be a no-op (sync.Once)

	// A bare (non-reserved, non-"dep_"-prefixed) metric name is folded into
	// the blocked-time union but is not surfaced as its own header entry.
	header := HeaderValue(ctx, time.Now().Add(time.Second), "bypass")
	if strings.Contains(header, "customop;") {
		t.Fatalf("expected no standalone header entry for non-reserved metrics, got %q", header)
	}
}

func TestObserveNoopWithoutCollector(t *testing.T) {
	done := Observe(context.Background(), "custom-op")
	done() // must not panic without an attached collector
}

func TestObserveDependencyUsesDependencyPrefix(t *testing.T) {
	startedAt := time.Now()
	collector := New(startedAt)
	ctx := WithCollector(context.Background(), collector)

	done := ObserveDependency(ctx, "payments")
	time.Sleep(time.Millisecond)
	done()

	header := HeaderValue(ctx, time.Now(), "bypass")
	if !strings.Contains(header, "dep_payments;") {
		t.Fatalf("expected dep_payments entry in header, got %q", header)
	}
}

func TestDependencyMetricNameDefaultsWhenEmpty(t *testing.T) {
	if got := dependencyMetricName(""); got != "dep_http" {
		t.Fatalf("dependencyMetricName(%q) = %q, want %q", "", got, "dep_http")
	}
	if got := dependencyMetricName(dependencyPrefix + "openai"); got != "dep_openai" {
		t.Fatalf("dependencyMetricName(%q) = %q, want %q", dependencyPrefix+"openai", got, "dep_openai")
	}
}

func TestRecordIgnoresInvalidIntervals(t *testing.T) {
	startedAt := time.Unix(600, 0)
	collector := New(startedAt)

	// End before start: ignored.
	collector.Record(MetricDatabase, startedAt.Add(2*time.Millisecond), startedAt.Add(time.Millisecond), 1)
	// Zero start time: ignored.
	collector.Record(MetricDatabase, time.Time{}, startedAt.Add(time.Millisecond), 1)
	// Empty/whitespace name: ignored.
	collector.Record("   ", startedAt, startedAt.Add(time.Millisecond), 1)

	header := collector.HeaderValue(startedAt.Add(10*time.Millisecond), "bypass")
	if !strings.Contains(header, `db;dur=0.0;desc="queries=0"`) {
		t.Fatalf("expected no database spans recorded, got %q", header)
	}
}

// --- Coverage gap follow-ups (see PR discussion): defensive nil/zero-value
// branches and interval-clipping edge cases that the tests above did not
// exercise. ---

func TestNewDefaultsZeroStartTime(t *testing.T) {
	before := time.Now()
	collector := New(time.Time{})
	after := time.Now()

	if collector.startedAt.Before(before) || collector.startedAt.After(after) {
		t.Fatalf("New(time.Time{}).startedAt = %v, want between %v and %v", collector.startedAt, before, after)
	}
}

func TestWithCollectorHandlesNilContext(t *testing.T) {
	var nilCtx context.Context
	collector := New(time.Unix(800, 0))

	ctx := WithCollector(nilCtx, collector)
	if ctx == nil {
		t.Fatal("WithCollector(nil, collector) returned a nil context")
	}
	got, ok := FromContext(ctx)
	if !ok || got != collector {
		t.Fatalf("FromContext() = (%v, %v), want (%v, true)", got, ok, collector)
	}
}

func TestFromContextAndActiveHandleNilContext(t *testing.T) {
	var nilCtx context.Context

	if got, ok := FromContext(nilCtx); got != nil || ok {
		t.Fatalf("FromContext(nil) = (%v, %v), want (nil, false)", got, ok)
	}
	if Active(nilCtx) {
		t.Fatal("Active(nil) reported true")
	}
}

func TestRecordAndRecordIntervalNoopWithoutCollector(t *testing.T) {
	ctx := context.Background() // deliberately no collector attached

	// Must not panic and must be pure no-ops.
	Record(ctx, MetricDatabase, time.Now(), time.Now().Add(time.Millisecond), 1)
	RecordInterval(ctx, MetricDatabase, time.Now(), time.Now().Add(time.Millisecond))

	if Active(ctx) {
		t.Fatal("context unexpectedly reports an active collector")
	}
}

func TestCollectorRecordClampsNonPositiveCount(t *testing.T) {
	startedAt := time.Unix(900, 0)
	collector := New(startedAt)

	collector.Record(MetricDatabase, startedAt, startedAt.Add(time.Millisecond), 0)
	collector.Record(MetricDatabase, startedAt.Add(time.Millisecond), startedAt.Add(2*time.Millisecond), -5)

	header := collector.HeaderValue(startedAt.Add(3*time.Millisecond), "bypass")
	if !strings.Contains(header, `db;dur=2.0;desc="queries=2"`) {
		t.Fatalf("header %q: want each non-positive count clamped to 1 (2 total)", header)
	}
}

func TestRecordClampsNegativeCountDirectly(t *testing.T) {
	startedAt := time.Unix(950, 0)
	collector := New(startedAt)

	// Bypass the public Record wrapper (which floors non-positive counts to
	// 1) to exercise record()'s own defensive clamp for negative counts.
	collector.record(MetricDatabase, startedAt, startedAt.Add(time.Millisecond), -3)

	header := collector.HeaderValue(startedAt.Add(2*time.Millisecond), "bypass")
	if !strings.Contains(header, `db;dur=1.0;desc="queries=0"`) {
		t.Fatalf("header %q: want negative count clamped to 0 while the interval is still recorded", header)
	}
}

func TestSetCacheStatusNoopWithoutCollector(t *testing.T) {
	ctx := context.Background() // deliberately no collector attached
	SetCacheStatus(ctx, "hit")  // must not panic
	if Active(ctx) {
		t.Fatal("context unexpectedly reports an active collector")
	}
}

func TestSetCacheStatusIgnoresInvalidStatus(t *testing.T) {
	startedAt := time.Unix(1000, 0)
	collector := New(startedAt)
	ctx := WithCollector(context.Background(), collector)

	SetCacheStatus(ctx, "not-a-real-status")

	if got := HeaderValue(ctx, startedAt.Add(time.Millisecond), ""); !strings.Contains(got, `cache;desc="bypass"`) {
		t.Fatalf("HeaderValue() = %q, want the invalid status left unset (bypass fallback)", got)
	}
}

func TestHeaderValueNilCollectorReceiver(t *testing.T) {
	var collector *Collector
	if got := collector.HeaderValue(time.Now(), "hit"); got != "" {
		t.Fatalf("HeaderValue() on nil collector = %q, want empty", got)
	}
}

func TestHeaderValueDefaultsZeroEndedAt(t *testing.T) {
	startedAt := time.Now().Add(-5 * time.Millisecond)
	collector := New(startedAt)

	header := collector.HeaderValue(time.Time{}, "bypass")
	if strings.Contains(header, "total;dur=0.0") {
		t.Fatalf("header %q: zero endedAt was not defaulted to time.Now()", header)
	}
	if !strings.Contains(header, "total;dur=") {
		t.Fatalf("header %q missing total entry", header)
	}
}

func TestHeaderValueClampsEndedAtBeforeStart(t *testing.T) {
	startedAt := time.Unix(1100, 0)
	collector := New(startedAt)

	header := collector.HeaderValue(startedAt.Add(-10*time.Millisecond), "bypass")
	if !strings.Contains(header, "total;dur=0.0") {
		t.Fatalf("header %q: want endedAt before startedAt clamped to a zero total duration", header)
	}
}

func TestNormalizeMetricNameTruncatesLongNames(t *testing.T) {
	longName := strings.Repeat("a", maxMetricNameLength+20)
	got := normalizeMetricName(longName)
	if len(got) != maxMetricNameLength {
		t.Fatalf("normalizeMetricName(long) length = %d, want %d", len(got), maxMetricNameLength)
	}
	if got != strings.Repeat("a", maxMetricNameLength) {
		t.Fatalf("normalizeMetricName(long) = %q, want %d 'a' characters", got, maxMetricNameLength)
	}
}

func TestUnionDurationClipsIntervalsToRequestWindow(t *testing.T) {
	startedAt := time.Unix(1200, 0)
	collector := New(startedAt)

	// Starts before the window: clipped to startedAt, remains a valid span.
	collector.Record("early", startedAt.Add(-20*time.Millisecond), startedAt.Add(10*time.Millisecond), 1)
	// Ends after the window: clipped to the HeaderValue endedAt below.
	collector.Record("late", startedAt.Add(50*time.Millisecond), startedAt.Add(500*time.Millisecond), 1)
	// Entirely before the window: clipped start/end collapse and is dropped.
	collector.Record("stale", startedAt.Add(-50*time.Millisecond), startedAt.Add(-30*time.Millisecond), 1)

	header := collector.HeaderValue(startedAt.Add(100*time.Millisecond), "bypass")
	if !strings.Contains(header, "total;dur=100.0") {
		t.Fatalf("header %q: want total;dur=100.0", header)
	}
	if !strings.Contains(header, "app;dur=40.0") {
		t.Fatalf("header %q: want app;dur=40.0 (100ms window minus 60ms of clipped blocked time)", header)
	}
}

func TestFormatDurationClampsNegativeDirectly(t *testing.T) {
	if got := formatDuration(-5 * time.Millisecond); got != "0.0" {
		t.Fatalf("formatDuration(negative) = %q, want %q", got, "0.0")
	}
}
