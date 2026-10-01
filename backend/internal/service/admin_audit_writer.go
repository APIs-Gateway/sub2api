package service

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// AdminAuditQueueSize is the number of audit rows that can wait for the
	// database. When it is full new rows are dropped (and counted), never
	// blocked on.
	AdminAuditQueueSize = 4096

	adminAuditBatchSize     = 100
	adminAuditFlushInterval = time.Second
	adminAuditWriteTimeout  = 10 * time.Second
	// After this many consecutive per-row failures in one batch the rest of
	// the batch is given up on: the database is down, not one bad row.
	adminAuditMaxConsecutiveRowFailures = 3
	// adminAuditStatsLogInterval is how often the counters are logged (only
	// when something was dropped, failed or unidentified since the last line).
	adminAuditStatsLogInterval = 5 * time.Minute
	// adminAuditUnidentifiedLogInterval rate-limits the per-request warning
	// for requests without a usable credential.
	adminAuditUnidentifiedLogInterval = 10 * time.Second
)

// AdminAuditStats is a snapshot of the writer's counters.
//
// Dropped counts every row that was lost, whatever the reason: the queue was
// full, the writer was stopped, or the database rejected the row (the latter
// is also counted in Failed). Unidentified counts state-changing requests that
// carried no usable credential; they are not written to the table.
type AdminAuditStats struct {
	QueueDepth    int    `json:"queue_depth"`
	QueueCapacity int    `json:"queue_capacity"`
	Enqueued      uint64 `json:"enqueued"`
	Dropped       uint64 `json:"dropped"`
	Written       uint64 `json:"written"`
	Failed        uint64 `json:"failed"`
	Unidentified  uint64 `json:"unidentified"`
	LastError     string `json:"last_error"`
}

// AdminAuditWriter persists audit rows asynchronously.
//
// Enqueue never blocks and never touches the database, so a request is never
// slowed down, or held in a transaction, by auditing; a database that is slow
// or down only makes the queue fill up, after which rows are dropped and
// counted. Rows are written in batches; if a batch is rejected (for example
// because one row violates a constraint) its rows are retried one by one so a
// single bad row cannot take its neighbours with it.
type AdminAuditWriter struct {
	repo AuditLogRepository

	queue         chan *AuditLog
	batchSize     int
	flushInterval time.Duration
	writeTimeout  time.Duration

	statsLogInterval time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	started atomic.Bool

	// sendMu orders Enqueue against Stop: Enqueue holds the read lock while it
	// checks "stopped" and sends, Stop takes the write lock to set it. Once
	// Stop has returned from that, no row can enter the queue any more, so the
	// final drain cannot miss one.
	sendMu  sync.RWMutex
	stopped bool

	enqueued     atomic.Uint64
	dropped      atomic.Uint64
	written      atomic.Uint64
	failed       atomic.Uint64
	unidentified atomic.Uint64
	lastError    atomic.Value // string

	// lastUnidentifiedLog is the unix nano time of the last warning for an
	// unidentified request; suppressedUnidentified counts the ones skipped
	// since.
	lastUnidentifiedLog    atomic.Int64
	suppressedUnidentified atomic.Uint64
}

// NewAdminAuditWriter creates a writer with the given queue capacity (a
// non-positive value selects AdminAuditQueueSize). Call Start to begin
// writing.
func NewAdminAuditWriter(repo AuditLogRepository, queueSize int) *AdminAuditWriter {
	if queueSize <= 0 {
		queueSize = AdminAuditQueueSize
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &AdminAuditWriter{
		repo:          repo,
		queue:         make(chan *AuditLog, queueSize),
		batchSize:     adminAuditBatchSize,
		flushInterval: adminAuditFlushInterval,
		writeTimeout:  adminAuditWriteTimeout,

		statsLogInterval: adminAuditStatsLogInterval,
		ctx:              ctx,
		cancel:           cancel,
	}
	w.lastError.Store("")
	return w
}

// ProvideAdminAuditWriter builds the writer and starts its background loop.
func ProvideAdminAuditWriter(repo AuditLogRepository) *AdminAuditWriter {
	w := NewAdminAuditWriter(repo, AdminAuditQueueSize)
	w.Start()
	return w
}

// Start launches the background loop. It is a no-op without a repository or
// when already started.
func (w *AdminAuditWriter) Start() {
	if w == nil || w.repo == nil || !w.started.CompareAndSwap(false, true) {
		return
	}
	w.wg.Add(1)
	go w.run()
}

// Stop stops accepting rows, writes what is queued (bounded by the write
// timeout per batch) and waits for the loop to exit, or until ctx is done,
// whichever comes first (the remaining rows are then lost with the process).
// It is safe to call more than once.
func (w *AdminAuditWriter) Stop(ctx context.Context) {
	if w == nil {
		return
	}
	w.sendMu.Lock()
	w.stopped = true
	w.sendMu.Unlock()
	w.cancel()

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	if ctx == nil {
		<-done
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("admin audit: stopped before the queue was fully written",
			"queue_depth", len(w.queue), "error", ctx.Err())
	}
}

// Enqueue hands an audit row to the writer without blocking. It returns false
// (and counts a drop) when the writer is stopped, the row is nil or the queue
// is full.
func (w *AdminAuditWriter) Enqueue(entry *AuditLog) bool {
	if w == nil || entry == nil {
		return false
	}
	w.sendMu.RLock()
	defer w.sendMu.RUnlock()
	if w.stopped {
		w.dropped.Add(1)
		return false
	}
	select {
	case w.queue <- entry:
		w.enqueued.Add(1)
		return true
	default:
		// Log the first drop and then every 1000th so an overloaded database
		// does not also flood the log.
		if dropped := w.dropped.Add(1); dropped == 1 || dropped%1000 == 0 {
			slog.Warn("admin audit: queue full, dropping audit log", "dropped_total", dropped,
				"queue_capacity", cap(w.queue))
		}
		return false
	}
}

// RecordUnidentified counts a state-changing admin request that carried no
// usable credential (missing, unknown or malformed). No row is written, so
// scanning the internet cannot fill the table, but the attempt is not
// invisible: it is counted and a warning with client IP, route and user agent
// is logged, at most once per adminAuditUnidentifiedLogInterval.
func (w *AdminAuditWriter) RecordUnidentified(clientIP, route, userAgent string) {
	if w == nil {
		return
	}
	total := w.unidentified.Add(1)
	now := time.Now().UnixNano()
	last := w.lastUnidentifiedLog.Load()
	if now-last < int64(adminAuditUnidentifiedLogInterval) || !w.lastUnidentifiedLog.CompareAndSwap(last, now) {
		w.suppressedUnidentified.Add(1)
		return
	}
	slog.Warn("admin audit: state-changing admin request without a usable credential (not recorded)",
		"client_ip", clientIP, "route", route, "user_agent", truncateAuditString(userAgent, 200),
		"unidentified_total", total, "suppressed_since_last_log", w.suppressedUnidentified.Swap(0))
}

// Stats returns a snapshot of the counters.
func (w *AdminAuditWriter) Stats() AdminAuditStats {
	if w == nil {
		return AdminAuditStats{}
	}
	lastError, _ := w.lastError.Load().(string)
	return AdminAuditStats{
		QueueDepth:    len(w.queue),
		QueueCapacity: cap(w.queue),
		Enqueued:      w.enqueued.Load(),
		Dropped:       w.dropped.Load(),
		Written:       w.written.Load(),
		Failed:        w.failed.Load(),
		Unidentified:  w.unidentified.Load(),
		LastError:     lastError,
	}
}

func (w *AdminAuditWriter) run() {
	defer w.wg.Done()

	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()
	statsTicker := time.NewTicker(w.statsLogInterval)
	defer statsTicker.Stop()
	var reported AdminAuditStats

	batch := make([]*AuditLog, 0, w.batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		w.flush(batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-w.ctx.Done():
			// Drain whatever is still queued, then exit.
			for {
				select {
				case entry := <-w.queue:
					batch = append(batch, entry)
					if len(batch) >= w.batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case entry := <-w.queue:
			batch = append(batch, entry)
			if len(batch) >= w.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-statsTicker.C:
			reported = w.logCounters(reported)
		}
	}
}

// logCounters writes one summary line when the loss counters moved since the
// previous line (so a healthy writer stays silent) and returns the snapshot it
// reported.
func (w *AdminAuditWriter) logCounters(previous AdminAuditStats) AdminAuditStats {
	current := w.Stats()
	if current.Dropped != previous.Dropped || current.Failed != previous.Failed ||
		current.Unidentified != previous.Unidentified {
		slog.Warn("admin audit: counters",
			"enqueued", current.Enqueued, "written", current.Written,
			"dropped", current.Dropped, "failed", current.Failed,
			"unidentified", current.Unidentified, "queue_depth", current.QueueDepth,
			"last_error", current.LastError)
	}
	return current
}

// flush writes one batch; it never panics and never returns an error (failures
// are counted and logged).
func (w *AdminAuditWriter) flush(batch []*AuditLog) {
	defer func() {
		if r := recover(); r != nil {
			w.failed.Add(uint64(len(batch)))
			w.dropped.Add(uint64(len(batch)))
			w.lastError.Store("panic while writing audit logs")
			slog.Error("admin audit: panic while writing audit logs", "panic", r, "rows", len(batch))
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), w.writeTimeout)
	defer cancel()

	inserted, err := w.repo.BatchInsert(ctx, batch)
	if err == nil {
		w.written.Add(uint64(inserted))
		w.lastError.Store("")
		return
	}

	// The batch was rejected as a whole; find out which rows are at fault.
	w.lastError.Store(err.Error())
	if len(batch) == 1 {
		w.failed.Add(1)
		w.dropped.Add(1)
		slog.Warn("admin audit: failed to write audit log", "error", err)
		return
	}
	var ok, bad uint64
	consecutive := 0
	for index, entry := range batch {
		if consecutive >= adminAuditMaxConsecutiveRowFailures {
			bad += uint64(len(batch) - index)
			break
		}
		rowCtx, rowCancel := context.WithTimeout(context.Background(), w.writeTimeout)
		rowErr := w.repo.Insert(rowCtx, entry)
		rowCancel()
		if rowErr != nil {
			bad++
			consecutive++
			w.lastError.Store(rowErr.Error())
			continue
		}
		ok++
		consecutive = 0
	}
	w.written.Add(ok)
	w.failed.Add(bad)
	w.dropped.Add(bad)
	slog.Warn("admin audit: batch insert failed, retried row by row",
		"error", err, "rows", len(batch), "written", ok, "failed", bad)
}
