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
)

// AdminAuditStats is a snapshot of the writer's counters.
type AdminAuditStats struct {
	QueueDepth    int    `json:"queue_depth"`
	QueueCapacity int    `json:"queue_capacity"`
	Enqueued      uint64 `json:"enqueued"`
	Dropped       uint64 `json:"dropped"`
	Written       uint64 `json:"written"`
	Failed        uint64 `json:"failed"`
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

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	started atomic.Bool
	stopped atomic.Bool

	enqueued  atomic.Uint64
	dropped   atomic.Uint64
	written   atomic.Uint64
	failed    atomic.Uint64
	lastError atomic.Value // string
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
		ctx:           ctx,
		cancel:        cancel,
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
// timeout per batch) and waits for the loop to exit. It is safe to call more
// than once.
func (w *AdminAuditWriter) Stop() {
	if w == nil {
		return
	}
	w.stopped.Store(true)
	w.cancel()
	w.wg.Wait()
}

// Enqueue hands an audit row to the writer without blocking. It returns false
// (and counts a drop) when the writer is stopped, the row is nil or the queue
// is full.
func (w *AdminAuditWriter) Enqueue(entry *AuditLog) bool {
	if w == nil || entry == nil {
		return false
	}
	if w.stopped.Load() {
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
		LastError:     lastError,
	}
}

func (w *AdminAuditWriter) run() {
	defer w.wg.Done()

	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()

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
		}
	}
}

// flush writes one batch; it never panics and never returns an error (failures
// are counted and logged).
func (w *AdminAuditWriter) flush(batch []*AuditLog) {
	defer func() {
		if r := recover(); r != nil {
			w.failed.Add(uint64(len(batch)))
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
	slog.Warn("admin audit: batch insert failed, retried row by row",
		"error", err, "rows", len(batch), "written", ok, "failed", bad)
}
