//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writerTestRepo is an AuditLogRepository whose behaviour tests control.
type writerTestRepo struct {
	mu      sync.Mutex
	rows    []*AuditLog
	batches int

	batchErr error         // returned by BatchInsert when set
	badRow   string        // an entry with this Action makes Insert fail
	block    chan struct{} // BatchInsert waits for this to close when non-nil
	entered  chan struct{} // receives once BatchInsert has been entered
	calls    atomic.Int64
}

func (r *writerTestRepo) Insert(_ context.Context, entry *AuditLog) error {
	r.calls.Add(1)
	if r.badRow != "" && entry.Action == r.badRow {
		return errors.New("bad row")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, entry)
	return nil
}

func (r *writerTestRepo) BatchInsert(_ context.Context, entries []*AuditLog) (int64, error) {
	r.calls.Add(1)
	if r.entered != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
	}
	if r.block != nil {
		<-r.block
	}
	if r.batchErr != nil {
		return 0, r.batchErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches++
	r.rows = append(r.rows, entries...)
	return int64(len(entries)), nil
}

func (r *writerTestRepo) List(context.Context, *AuditLogFilter) (*AuditLogList, error) {
	return &AuditLogList{}, nil
}

func (r *writerTestRepo) DeleteBefore(context.Context, time.Time, int) (int64, error) { return 0, nil }

func (r *writerTestRepo) rowCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

func TestAdminAuditWriterWritesQueuedRows(t *testing.T) {
	repo := &writerTestRepo{}
	w := NewAdminAuditWriter(repo, 64)
	w.Start()

	for i := 0; i < 10; i++ {
		require.True(t, w.Enqueue(&AuditLog{Action: "POST /x"}))
	}
	w.Stop() // drains the queue

	require.Equal(t, 10, repo.rowCount())
	stats := w.Stats()
	require.Equal(t, uint64(10), stats.Enqueued)
	require.Equal(t, uint64(10), stats.Written)
	require.Zero(t, stats.Dropped)
	require.Zero(t, stats.Failed)
	require.Equal(t, 64, stats.QueueCapacity)
}

func TestAdminAuditWriterNeverBlocksWhenQueueIsFull(t *testing.T) {
	// The database is "down": the first batch hangs, so nothing is consumed
	// after the writer picks up the first row.
	repo := &writerTestRepo{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	w := NewAdminAuditWriter(repo, 5)
	w.flushInterval = time.Millisecond
	w.batchSize = 1
	w.Start()

	require.True(t, w.Enqueue(&AuditLog{Action: "first"}))
	select {
	case <-repo.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer never reached the repository")
	}

	// 5 rows fit in the queue, everything after that is dropped.
	accepted, dropped := 0, 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			if w.Enqueue(&AuditLog{Action: "burst"}) {
				accepted++
			} else {
				dropped++
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}

	require.Equal(t, 5, accepted)
	require.Equal(t, 995, dropped)
	require.Equal(t, uint64(995), w.Stats().Dropped)

	close(repo.block) // the database comes back
	w.Stop()
	require.Equal(t, 6, repo.rowCount(), "accepted rows are all written, dropped rows are gone")
}

func TestAdminAuditWriterRetriesRowByRowAfterBatchFailure(t *testing.T) {
	repo := &writerTestRepo{batchErr: errors.New("batch rejected"), badRow: "bad"}
	w := NewAdminAuditWriter(repo, 16)
	w.Start()

	require.True(t, w.Enqueue(&AuditLog{Action: "ok-1"}))
	require.True(t, w.Enqueue(&AuditLog{Action: "bad"}))
	require.True(t, w.Enqueue(&AuditLog{Action: "ok-2"}))
	w.Stop()

	require.Equal(t, 2, repo.rowCount(), "one bad row must not take its neighbours with it")
	stats := w.Stats()
	require.Equal(t, uint64(2), stats.Written)
	require.Equal(t, uint64(1), stats.Failed)
	require.NotEmpty(t, stats.LastError)
}

func TestAdminAuditWriterStoppedOrNilNeverPanics(t *testing.T) {
	var nilWriter *AdminAuditWriter
	require.False(t, nilWriter.Enqueue(&AuditLog{}))
	require.Equal(t, AdminAuditStats{}, nilWriter.Stats())
	nilWriter.Start()
	nilWriter.Stop()

	w := NewAdminAuditWriter(&writerTestRepo{}, 4)
	require.False(t, w.Enqueue(nil))
	w.Start()
	w.Start() // second start is a no-op
	w.Stop()
	w.Stop() // and so is a second stop
	require.False(t, w.Enqueue(&AuditLog{}), "a stopped writer drops rows")
	require.Equal(t, uint64(1), w.Stats().Dropped)

	// Without a repository the writer is inert but safe.
	inert := NewAdminAuditWriter(nil, 4)
	inert.Start()
	require.True(t, inert.Enqueue(&AuditLog{}))
	inert.Stop()
}
