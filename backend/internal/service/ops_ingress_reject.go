package service

import (
	"context"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// This file implements the persistence + query side of the ops_ingress_reject
// aggregation subsystem (issue #846). It intentionally does not depend on the
// in-memory capture buffer added by issue #548's Phase A
// (internal/server/middleware/ingress_reject_capture.go) because that branch had not
// merged yet when this was written; RecordIngressReject below is the integration
// point a future #548 follow-up PR can call from the capture buffer's drain loop.
//
// Only aggregate counters are stored: reject_reason / route_family / protocol /
// client_ip / user_id / api_key_id / request_count within a one-minute bucket. No
// request bodies, headers, or credentials are ever captured here, and client_ip is
// masked to a coarse network prefix (see maskClientIP) before it is even aggregated
// in memory, so a raw per-device address never exists anywhere in this subsystem.

const (
	ingressRejectShardCount        = 16
	ingressRejectMaxEntries        = 8192
	ingressRejectMaxPendingBatches = 4
	ingressRejectBucketSize        = time.Minute
	ingressRejectFlushInterval     = 5 * time.Second
	ingressRejectFlushTimeout      = 5 * time.Second
)

// OpsIngressRejectAggregate is one (bucket_start, reject_reason, route_family,
// protocol, client_ip, user_id, api_key_id) row: a minute-bucketed request count,
// never a raw request record.
type OpsIngressRejectAggregate struct {
	ID           int64     `json:"id"`
	BucketStart  time.Time `json:"bucket_start"`
	RejectReason string    `json:"reject_reason"`
	RouteFamily  string    `json:"route_family"`
	Protocol     string    `json:"protocol"`
	ClientIP     string    `json:"client_ip"`
	UserID       *int64    `json:"user_id,omitempty"`
	APIKeyID     *int64    `json:"api_key_id,omitempty"`
	RequestCount int64     `json:"request_count"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
}

type OpsIngressRejectFilter struct {
	StartTime    *time.Time
	EndTime      *time.Time
	RejectReason string
	RouteFamily  string
	Protocol     string
	ClientIP     string
	UserID       *int64
	APIKeyID     *int64
	Page         int
	PageSize     int
}

type OpsIngressRejectList struct {
	Items    []*OpsIngressRejectAggregate `json:"items"`
	Total    int                          `json:"total"`
	Page     int                          `json:"page"`
	PageSize int                          `json:"page_size"`
}

type OpsIngressRejectHealth struct {
	Cardinality    int64  `json:"cardinality"`
	Capacity       int    `json:"capacity"`
	PendingBatches int    `json:"pending_batches"`
	PendingRows    int    `json:"pending_rows"`
	Overflowed     uint64 `json:"overflowed_count"`
	Dropped        uint64 `json:"dropped_count"`
	Flushed        uint64 `json:"flushed_request_count"`
	FlushFailures  uint64 `json:"flush_failure_count"`
	Accepting      bool   `json:"accepting"`
	LastError      string `json:"last_error,omitempty"`
}

// OpsIngressRejectRepository is implemented by repository.opsRepository (see
// internal/repository/ops_ingress_reject_repo.go). It is intentionally kept separate
// from the broad OpsRepository interface (rather than extending it) so adding this
// capability does not ripple through every existing OpsRepository test double; the
// concrete *opsRepository already satisfies both, so the type assertion in
// OpsService.ListIngressRejects below succeeds at runtime.
type OpsIngressRejectRepository interface {
	BatchUpsertIngressRejects(ctx context.Context, items []*OpsIngressRejectAggregate) error
	ListIngressRejects(ctx context.Context, filter *OpsIngressRejectFilter) (*OpsIngressRejectList, error)
}

type ingressRejectKey struct {
	reason      string
	routeFamily string
	protocol    string
	clientIP    string
	userID      int64
	apiKeyID    int64
}

type ingressRejectShard struct {
	mu    sync.Mutex
	items map[ingressRejectKey]*OpsIngressRejectAggregate
}

// OpsIngressRejectAggregator buffers ingress-reject events in memory, aggregates them
// per minute bucket across a bounded set of dimension keys, and periodically flushes
// deltas to the repository. It is intentionally process-local (like
// invalidAuthAbuseLimiter and AuthCacheInvalidationWorker elsewhere in this package):
// losing an in-flight minute of counters on crash/restart is an acceptable trade-off
// for avoiding a synchronous DB write on the request-rejection hot path.
type OpsIngressRejectAggregator struct {
	repo OpsIngressRejectRepository

	shards      [ingressRejectShardCount]ingressRejectShard
	recordMu    sync.RWMutex
	snapshotMu  sync.Mutex
	bucket      atomic.Int64
	cardinality atomic.Int64
	overflowMu  sync.Mutex
	overflow    *OpsIngressRejectAggregate

	pendingMu sync.Mutex
	pending   [][]*OpsIngressRejectAggregate

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	flushCh   chan struct{}
	started   atomic.Bool
	accepting atomic.Bool
	stopOnce  sync.Once

	overflowed    atomic.Uint64
	dropped       atomic.Uint64
	flushed       atomic.Uint64
	flushFailures atomic.Uint64
	lastError     atomic.Value
}

func NewOpsIngressRejectAggregator(repo OpsIngressRejectRepository) *OpsIngressRejectAggregator {
	ctx, cancel := context.WithCancel(context.Background())
	a := &OpsIngressRejectAggregator{
		repo: repo, ctx: ctx, cancel: cancel, flushCh: make(chan struct{}, 1),
	}
	for i := range a.shards {
		a.shards[i].items = make(map[ingressRejectKey]*OpsIngressRejectAggregate)
	}
	a.lastError.Store("")
	return a
}

// Start begins the periodic flush loop. A nil repo (e.g. the OpsIngressRejectRepository
// type assertion failed for a non-Postgres/test opsRepo) leaves the aggregator inert:
// RecordIngressReject becomes a no-op so callers never need a nil check.
func (a *OpsIngressRejectAggregator) Start() {
	if a == nil || a.repo == nil || !a.started.CompareAndSwap(false, true) {
		return
	}
	a.accepting.Store(true)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ticker := time.NewTicker(ingressRejectFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
				a.snapshotAndEnqueue(false)
				a.flushPending()
			case <-a.flushCh:
				a.flushPending()
			}
		}
	}()
}

func (a *OpsIngressRejectAggregator) Stop() {
	if a == nil {
		return
	}
	a.stopOnce.Do(func() {
		a.accepting.Store(false)
		a.recordMu.Lock()
		a.cancel()
		a.recordMu.Unlock()
		a.wg.Wait()
		a.snapshotAndEnqueue(false)
		a.flushPending()
	})
}

// RecordIngressReject records one rejected request. clientIP is masked to a network
// prefix (see maskClientIP) before it ever becomes part of an aggregation key, so no
// raw per-device address is retained even transiently in the shard maps.
func (a *OpsIngressRejectAggregator) RecordIngressReject(reason, routeFamily, protocol, clientIP string, userID, apiKeyID int64) {
	if a == nil || a.repo == nil || !a.accepting.Load() {
		return
	}
	now := time.Now().UTC()
	bucket := now.Truncate(ingressRejectBucketSize)
	key := ingressRejectKey{
		reason: boundedDimension(reason, "unknown"), routeFamily: boundedDimension(routeFamily, "other"),
		protocol: boundedDimension(protocol, "other"), clientIP: maskClientIP(clientIP),
		userID: userID, apiKeyID: apiKeyID,
	}
	for {
		a.ensureBucket(bucket)
		a.recordMu.RLock()
		if !a.accepting.Load() {
			a.recordMu.RUnlock()
			return
		}
		bucketUnix := a.bucket.Load()
		if bucketUnix != bucket.Unix() {
			a.recordMu.RUnlock()
			continue
		}
		shard := &a.shards[ingressRejectHash(key)%ingressRejectShardCount]
		shard.mu.Lock()
		if item := shard.items[key]; item != nil {
			item.RequestCount++
			item.LastSeen = now
			shard.mu.Unlock()
			a.recordMu.RUnlock()
			return
		}
		if a.reserveDimension() {
			shard.items[key] = aggregateFromKey(key, bucket, now)
			shard.mu.Unlock()
			a.recordMu.RUnlock()
			return
		}
		shard.mu.Unlock()
		a.recordOverflow(bucket, now)
		a.recordMu.RUnlock()
		return
	}
}

func (a *OpsIngressRejectAggregator) ensureBucket(bucket time.Time) {
	if a.bucket.Load() == bucket.Unix() {
		return
	}
	a.snapshotMu.Lock()
	defer a.snapshotMu.Unlock()
	a.recordMu.Lock()
	defer a.recordMu.Unlock()
	if a.bucket.Load() == bucket.Unix() {
		return
	}
	items := a.snapshotLocked(true)
	a.enqueue(items)
	a.bucket.Store(bucket.Unix())
	a.cardinality.Store(0)
	select {
	case a.flushCh <- struct{}{}:
	default:
	}
}

func (a *OpsIngressRejectAggregator) reserveDimension() bool {
	for {
		current := a.cardinality.Load()
		if current >= ingressRejectMaxEntries-1 {
			return false
		}
		if a.cardinality.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (a *OpsIngressRejectAggregator) recordOverflow(bucket, now time.Time) {
	a.overflowed.Add(1)
	a.overflowMu.Lock()
	defer a.overflowMu.Unlock()
	if a.overflow == nil || !a.overflow.BucketStart.Equal(bucket) {
		a.overflow = &OpsIngressRejectAggregate{
			BucketStart: bucket, RejectReason: "other", RouteFamily: "other", Protocol: "other",
			ClientIP: "unknown", RequestCount: 1, FirstSeen: now, LastSeen: now,
		}
		return
	}
	a.overflow.RequestCount++
	a.overflow.LastSeen = now
}

func (a *OpsIngressRejectAggregator) snapshotAndEnqueue(reset bool) {
	if a == nil || a.repo == nil {
		return
	}
	a.snapshotMu.Lock()
	items := a.snapshotLocked(reset)
	a.snapshotMu.Unlock()
	a.enqueue(items)
}

// snapshotLocked captures counter deltas. When reset is false the dimension keys
// remain resident for the whole minute, so periodic flushes cannot reset the budget.
func (a *OpsIngressRejectAggregator) snapshotLocked(reset bool) []*OpsIngressRejectAggregate {
	items := make([]*OpsIngressRejectAggregate, 0)
	for i := range a.shards {
		shard := &a.shards[i]
		shard.mu.Lock()
		for key, item := range shard.items {
			if item.RequestCount > 0 {
				copyItem := *item
				items = append(items, &copyItem)
				item.RequestCount = 0
			}
			if reset {
				delete(shard.items, key)
			}
		}
		shard.mu.Unlock()
	}
	a.overflowMu.Lock()
	if a.overflow != nil && a.overflow.RequestCount > 0 {
		copyItem := *a.overflow
		items = append(items, &copyItem)
		a.overflow.RequestCount = 0
	}
	if reset {
		a.overflow = nil
	}
	a.overflowMu.Unlock()
	return items
}

func (a *OpsIngressRejectAggregator) enqueue(items []*OpsIngressRejectAggregate) {
	if len(items) == 0 {
		return
	}
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	if len(a.pending) >= ingressRejectMaxPendingBatches {
		a.dropped.Add(batchRequestCount(items))
		return
	}
	a.pending = append(a.pending, items)
}

func (a *OpsIngressRejectAggregator) flushPending() {
	for {
		a.pendingMu.Lock()
		if len(a.pending) == 0 {
			a.pendingMu.Unlock()
			return
		}
		batch := a.pending[0]
		a.pendingMu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), ingressRejectFlushTimeout)
		err := a.repo.BatchUpsertIngressRejects(ctx, batch)
		cancel()
		if err != nil {
			a.flushFailures.Add(1)
			a.lastError.Store(err.Error())
			log.Printf("[OpsIngressRejectAggregator] flush failed: %v", err)
			return
		}
		a.pendingMu.Lock()
		if len(a.pending) > 0 {
			a.pending = a.pending[1:]
		}
		a.pendingMu.Unlock()
		a.flushed.Add(batchRequestCount(batch))
		a.lastError.Store("")
	}
}

func batchRequestCount(items []*OpsIngressRejectAggregate) uint64 {
	var count uint64
	for _, item := range items {
		if item != nil && item.RequestCount > 0 {
			count += uint64(item.RequestCount)
		}
	}
	return count
}

func (a *OpsIngressRejectAggregator) Health() OpsIngressRejectHealth {
	h := OpsIngressRejectHealth{Capacity: ingressRejectMaxEntries}
	if a == nil {
		return h
	}
	h.Cardinality = a.cardinality.Load()
	a.overflowMu.Lock()
	if a.overflow != nil {
		h.Cardinality++
	}
	a.overflowMu.Unlock()
	a.pendingMu.Lock()
	h.PendingBatches = len(a.pending)
	for _, batch := range a.pending {
		h.PendingRows += len(batch)
	}
	a.pendingMu.Unlock()
	h.Overflowed = a.overflowed.Load()
	h.Dropped = a.dropped.Load()
	h.Flushed = a.flushed.Load()
	h.FlushFailures = a.flushFailures.Load()
	h.Accepting = a.accepting.Load()
	if v := a.lastError.Load(); v != nil {
		h.LastError, _ = v.(string)
	}
	return h
}

func boundedDimension(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len(value) > 64 {
		return value[:64]
	}
	return value
}

// maskClientIP truncates a client IP down to a coarse network prefix (IPv4 /24, IPv6
// /64) before it is used as an aggregation dimension or persisted. Unlike
// per-request logs (e.g. ops_error_logs.client_ip), ops_ingress_reject_aggregates is
// a long-lived rollup table with no per-request retention policy, so this security
// baseline deliberately never stores an exact, per-device client address here — only
// the network-level granularity needed for abuse/DDoS pattern triage is retained.
func maskClientIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return "unknown"
	}
	addr = addr.Unmap()
	bits := 24
	if addr.Is6() {
		bits = 64
	}
	prefix, err := addr.Prefix(bits)
	if err != nil {
		return "unknown"
	}
	// Prefix already zeroes the host bits, but Masked() is called explicitly here so
	// this never regresses into leaking host-bit precision regardless of stdlib
	// version behavior.
	return prefix.Masked().String()
}

func ingressRejectHash(k ingressRejectKey) int {
	h := uint64(1469598103934665603)
	for _, value := range []string{k.reason, k.routeFamily, k.protocol, k.clientIP} {
		for i := 0; i < len(value); i++ {
			h ^= uint64(value[i])
			h *= 1099511628211
		}
	}
	h ^= uint64(k.userID)
	h *= 1099511628211
	h ^= uint64(k.apiKeyID)
	return int(h & 0x7fffffff)
}

func aggregateFromKey(k ingressRejectKey, bucket, now time.Time) *OpsIngressRejectAggregate {
	item := &OpsIngressRejectAggregate{
		BucketStart: bucket, RejectReason: k.reason, RouteFamily: k.routeFamily, Protocol: k.protocol,
		ClientIP: k.clientIP, RequestCount: 1, FirstSeen: now, LastSeen: now,
	}
	if k.userID > 0 {
		value := k.userID
		item.UserID = &value
	}
	if k.apiKeyID > 0 {
		value := k.apiKeyID
		item.APIKeyID = &value
	}
	return item
}

// SetIngressRejectAggregator is called by wire.go's ProvideOpsIngressRejectAggregator
// once the aggregator has been constructed, following the same construction-order
// decoupling as SetCleanupReloader (OpsService must not import the aggregator's
// wiring dependencies directly, and the aggregator's Start()/Stop() lifecycle is owned
// by cmd/server/wire.go's provideCleanup, not by OpsService).
func (s *OpsService) SetIngressRejectAggregator(a *OpsIngressRejectAggregator) {
	if s != nil {
		s.ingressRejectAggregator = a
	}
}

// RecordIngressReject is the integration point for the ingress-rejection hot path
// (and, in the future, for issue #548's capture buffer drain loop) to record a
// rejected request without blocking on a DB write.
func (s *OpsService) RecordIngressReject(reason, routeFamily, protocol, clientIP string, userID, apiKeyID int64) {
	if s != nil && s.ingressRejectAggregator != nil {
		s.ingressRejectAggregator.RecordIngressReject(reason, routeFamily, protocol, clientIP, userID, apiKeyID)
	}
}

func (s *OpsService) GetIngressRejectHealth() OpsIngressRejectHealth {
	if s == nil || s.ingressRejectAggregator == nil {
		return OpsIngressRejectHealth{Capacity: ingressRejectMaxEntries}
	}
	return s.ingressRejectAggregator.Health()
}

// ListIngressRejects returns paginated, already-aggregated ingress-reject counters for
// the read-only admin API. It never returns raw request bodies, headers, or
// credentials: only the bucketed dimensions/counters described on
// OpsIngressRejectAggregate.
func (s *OpsService) ListIngressRejects(ctx context.Context, filter *OpsIngressRejectFilter) (*OpsIngressRejectList, error) {
	if s == nil {
		return nil, ErrOpsDisabled
	}
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	repo, ok := s.opsRepo.(OpsIngressRejectRepository)
	if !ok || repo == nil {
		return &OpsIngressRejectList{Items: []*OpsIngressRejectAggregate{}, Page: 1, PageSize: 50}, nil
	}
	return repo.ListIngressRejects(ctx, filter)
}
