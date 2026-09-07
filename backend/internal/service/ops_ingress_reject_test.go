package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type ingressRejectRepoStub struct {
	mu        sync.Mutex
	failCount int
	calls     int
	requests  int64
	lastItems []*OpsIngressRejectAggregate
}

func (r *ingressRejectRepoStub) BatchUpsertIngressRejects(_ context.Context, items []*OpsIngressRejectAggregate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.failCount > 0 {
		r.failCount--
		return errors.New("temporary database failure")
	}
	r.lastItems = items
	for _, item := range items {
		if item != nil {
			r.requests += item.RequestCount
		}
	}
	return nil
}

func (r *ingressRejectRepoStub) ListIngressRejects(context.Context, *OpsIngressRejectFilter) (*OpsIngressRejectList, error) {
	return &OpsIngressRejectList{}, nil
}

func TestMaskClientIPTruncatesToNetworkPrefix(t *testing.T) {
	require.Equal(t, "203.0.113.0/24", maskClientIP("203.0.113.42"))
	require.Equal(t, "2001:db8::/64", maskClientIP("2001:db8::1234:5678:9abc:def0"))
	require.Equal(t, "203.0.113.0/24", maskClientIP("203.0.113.42:54321"), "must strip a trailing port before masking")
	require.Equal(t, "unknown", maskClientIP(""))
	require.Equal(t, "unknown", maskClientIP("   "))
	require.Equal(t, "unknown", maskClientIP("not-an-ip"))
}

func TestMaskClientIPNeverReturnsAFullyPreciseAddress(t *testing.T) {
	// Regression guard for the security-baseline requirement: the mask must always
	// collapse distinct host addresses within the same subnet to the same value.
	require.Equal(t, maskClientIP("203.0.113.1"), maskClientIP("203.0.113.254"))
	require.NotEqual(t, maskClientIP("203.0.113.1"), "203.0.113.1")
}

func TestOpsIngressRejectAggregatorCapsGlobalCardinalityAndOverflows(t *testing.T) {
	repo := &ingressRejectRepoStub{}
	a := NewOpsIngressRejectAggregator(repo)
	a.Start()

	// 8192 distinct /24 prefixes ("10.X.Y.0/24" for X in [0,31], Y in [0,255]) exactly
	// fill the aggregator's minute-bucket dimension budget.
	for i := 0; i < ingressRejectMaxEntries; i++ {
		ip := fmt.Sprintf("10.%d.%d.77", (i/256)%256, i%256)
		a.RecordIngressReject("invalid_api_key", "messages", "anthropic", ip, 0, 0)
	}
	require.Equal(t, int64(ingressRejectMaxEntries), a.Health().Cardinality)
	require.Zero(t, a.Health().Overflowed)

	// One more distinct dimension beyond capacity must overflow rather than grow
	// cardinality further.
	a.RecordIngressReject("invalid_api_key", "messages", "anthropic", "10.200.200.1", 0, 0)
	health := a.Health()
	require.Equal(t, int64(ingressRejectMaxEntries), health.Cardinality)
	require.Greater(t, health.Overflowed, uint64(0))

	a.snapshotAndEnqueue(false)
	require.Equal(t, int64(ingressRejectMaxEntries), a.Health().Cardinality, "periodic flush must retain the minute budget")
	a.Stop()
}

func TestOpsIngressRejectAggregatorConcurrentCountAndStopFlush(t *testing.T) {
	repo := &ingressRejectRepoStub{}
	a := NewOpsIngressRejectAggregator(repo)
	a.Start()

	const goroutines = 32
	const perGoroutine = 200
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				a.RecordIngressReject("invalid_api_key", "responses", "openai", "192.0.2.10", 0, 0)
			}
		}()
	}
	wg.Wait()
	a.Stop()

	repo.mu.Lock()
	require.Equal(t, int64(goroutines*perGoroutine), repo.requests)
	repo.mu.Unlock()
	require.False(t, a.Health().Accepting)
	// After Stop(), RecordIngressReject must be a safe no-op.
	a.RecordIngressReject("invalid_api_key", "responses", "openai", "192.0.2.10", 0, 0)
}

func TestOpsIngressRejectAggregatorRetriesBoundedPendingBatch(t *testing.T) {
	repo := &ingressRejectRepoStub{failCount: 1}
	a := NewOpsIngressRejectAggregator(repo)
	a.accepting.Store(true)
	a.RecordIngressReject("group_deleted", "messages", "anthropic", "192.0.2.20", 1, 2)
	a.snapshotAndEnqueue(false)
	a.flushPending()
	health := a.Health()
	require.Equal(t, 1, health.PendingBatches)
	require.Equal(t, uint64(1), health.FlushFailures)
	require.Equal(t, uint64(0), health.Dropped)

	a.flushPending()
	require.Equal(t, 0, a.Health().PendingBatches)
	a.Stop()
	repo.mu.Lock()
	require.Equal(t, int64(1), repo.requests)
	require.GreaterOrEqual(t, repo.calls, 2)
	repo.mu.Unlock()
}

func TestOpsIngressRejectAggregatorStoresMaskedIPNeverRaw(t *testing.T) {
	repo := &ingressRejectRepoStub{}
	a := NewOpsIngressRejectAggregator(repo)
	a.accepting.Store(true)
	a.RecordIngressReject("invalid_api_key", "messages", "anthropic", "203.0.113.77", 7, 9)
	a.snapshotAndEnqueue(true)
	a.flushPending()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.lastItems, 1)
	require.Equal(t, "203.0.113.0/24", repo.lastItems[0].ClientIP)
	require.NotEqual(t, "203.0.113.77", repo.lastItems[0].ClientIP)
}

func TestOpsIngressRejectAggregatorNilRepoIsInert(t *testing.T) {
	var a *OpsIngressRejectAggregator
	a.Start()
	a.Stop()
	a.RecordIngressReject("invalid_api_key", "messages", "anthropic", "203.0.113.1", 0, 0)
	require.Equal(t, ingressRejectMaxEntries, a.Health().Capacity)

	a = NewOpsIngressRejectAggregator(nil)
	a.Start()
	a.RecordIngressReject("invalid_api_key", "messages", "anthropic", "203.0.113.1", 0, 0)
	require.Zero(t, a.Health().Cardinality)
}

func TestOpsServiceIngressRejectGlueMethods(t *testing.T) {
	svc := &OpsService{}
	// Before wiring, all glue methods must be safe no-ops / defaults.
	svc.RecordIngressReject("invalid_api_key", "messages", "anthropic", "203.0.113.1", 0, 0)
	require.Equal(t, ingressRejectMaxEntries, svc.GetIngressRejectHealth().Capacity)

	repo := &ingressRejectRepoStub{}
	agg := NewOpsIngressRejectAggregator(repo)
	agg.accepting.Store(true)
	svc.SetIngressRejectAggregator(agg)

	svc.RecordIngressReject("invalid_api_key", "messages", "anthropic", "203.0.113.1", 0, 0)
	agg.snapshotAndEnqueue(true)
	agg.flushPending()

	repo.mu.Lock()
	require.Equal(t, int64(1), repo.requests)
	repo.mu.Unlock()

	health := svc.GetIngressRejectHealth()
	require.True(t, health.Accepting)

	var nilSvc *OpsService
	nilSvc.SetIngressRejectAggregator(agg)
	nilSvc.RecordIngressReject("x", "y", "z", "203.0.113.1", 0, 0)
	require.Equal(t, ingressRejectMaxEntries, nilSvc.GetIngressRejectHealth().Capacity)
}

func TestOpsServiceListIngressRejectsFallsBackWithoutRepoSupport(t *testing.T) {
	svc := &OpsService{}
	list, err := svc.ListIngressRejects(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, list)
	require.Empty(t, list.Items)

	var nilSvc *OpsService
	_, err = nilSvc.ListIngressRejects(context.Background(), nil)
	require.ErrorIs(t, err, ErrOpsDisabled)
}

// ingressRejectCapableOpsRepoMock composes the broad OpsRepository test double
// (opsRepoMock, defined in ops_repo_mock_test.go) with an OpsIngressRejectRepository
// implementation, mirroring how *repository.opsRepository satisfies both interfaces
// in production.
type ingressRejectCapableOpsRepoMock struct {
	*opsRepoMock
	*ingressRejectRepoStub
}

func TestProvideOpsIngressRejectAggregatorWiresIntoOpsServiceAndStops(t *testing.T) {
	svc := &OpsService{}
	mock := &ingressRejectCapableOpsRepoMock{opsRepoMock: &opsRepoMock{}, ingressRejectRepoStub: &ingressRejectRepoStub{}}

	agg := ProvideOpsIngressRejectAggregator(mock, svc)
	require.NotNil(t, agg)
	require.True(t, svc.GetIngressRejectHealth().Accepting)

	svc.RecordIngressReject("invalid_api_key", "messages", "anthropic", "203.0.113.1", 0, 0)
	agg.snapshotAndEnqueue(true)
	agg.flushPending()
	mock.ingressRejectRepoStub.mu.Lock()
	require.Equal(t, int64(1), mock.ingressRejectRepoStub.requests)
	mock.ingressRejectRepoStub.mu.Unlock()

	agg.Stop()
	require.False(t, svc.GetIngressRejectHealth().Accepting)
}

func TestProvideOpsIngressRejectAggregatorToleratesNilOpsService(t *testing.T) {
	mock := &ingressRejectCapableOpsRepoMock{opsRepoMock: &opsRepoMock{}, ingressRejectRepoStub: &ingressRejectRepoStub{}}
	agg := ProvideOpsIngressRejectAggregator(mock, nil)
	require.NotNil(t, agg)
	t.Cleanup(agg.Stop)
}

func TestProvideOpsIngressRejectAggregatorToleratesRepoWithoutIngressRejectSupport(t *testing.T) {
	svc := &OpsService{}
	agg := ProvideOpsIngressRejectAggregator(&opsRepoMock{}, svc)
	require.NotNil(t, agg)
	// repo assertion failed -> aggregator stays inert, never panics on record/health.
	svc.RecordIngressReject("invalid_api_key", "messages", "anthropic", "203.0.113.1", 0, 0)
	require.False(t, svc.GetIngressRejectHealth().Accepting)
	t.Cleanup(agg.Stop)
}
