//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type openAISelectionProbeTrackingCache struct {
	ConcurrencyCache
	acquireCalls map[int64]int
	releaseCalls map[int64]int
	acquire      bool
}

func (c *openAISelectionProbeTrackingCache) AcquireAccountSlot(_ context.Context, accountID int64, _ int, _ string) (bool, error) {
	if c.acquireCalls == nil {
		c.acquireCalls = make(map[int64]int)
	}
	c.acquireCalls[accountID]++
	return c.acquire, nil
}

func (c *openAISelectionProbeTrackingCache) ReleaseAccountSlot(_ context.Context, accountID int64, _ string) error {
	if c.releaseCalls == nil {
		c.releaseCalls = make(map[int64]int)
	}
	c.releaseCalls[accountID]++
	return nil
}

func probeBudgetTestAccount(id int64, concurrency int) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: concurrency,
	}
}

func candidateIDs(candidates []openAIAccountCandidateScore) []int64 {
	ids := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.account != nil {
			ids = append(ids, candidate.account.ID)
		}
	}
	return ids
}

func TestOpenAISelectionProbeBudgetLimitsAcquireAndRecheck(t *testing.T) {
	budget := newOpenAISelectionProbeBudget()
	budget.enableLimit()

	for i := 0; i < openAIAccountSelectionProbeLimit; i++ {
		require.True(t, budget.recordAcquire(int64(i+1)))
	}
	require.False(t, budget.recordAcquire(999))

	for i := 0; i < openAIAccountSelectionProbeLimit; i++ {
		require.True(t, budget.recordRecheck())
	}
	require.False(t, budget.recordRecheck())

	unlimited := newOpenAISelectionProbeBudget()
	require.True(t, unlimited.recordAcquire(1))
	require.True(t, unlimited.recordRecheck())
}

func TestBuildOpenAISelectionOrderAddsCostOverflowWithoutChangingNormalTopK(t *testing.T) {
	scheduler := &defaultOpenAIAccountScheduler{}
	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 1}, loadInfo: &AccountLoadInfo{}, score: 3},
		{account: &Account{ID: 2}, loadInfo: &AccountLoadInfo{}, score: 2},
		{account: &Account{ID: 3}, loadInfo: &AccountLoadInfo{}, score: 1},
	}

	normal := scheduler.buildOpenAISelectionOrder(OpenAIAccountScheduleRequest{}, openAIAccountLoadPlan{
		candidates: candidates,
		topK:       1,
	})
	require.Len(t, normal, 1)
	require.Equal(t, []int64{1}, candidateIDs(normal))

	costAware := scheduler.buildOpenAISelectionOrder(OpenAIAccountScheduleRequest{}, openAIAccountLoadPlan{
		candidates:              candidates,
		topK:                    1,
		includeOverflowFallback: true,
	})
	require.Equal(t, []int64{1, 2, 3}, candidateIDs(costAware))
}

func TestBuildOpenAISelectionOrderKeepsCompactTiersWhenAddingOverflow(t *testing.T) {
	scheduler := &defaultOpenAIAccountScheduler{}
	supported := &Account{ID: 1, Platform: PlatformOpenAI, Extra: map[string]any{"openai_compact_supported": true}}
	supportedOverflow := &Account{ID: 2, Platform: PlatformOpenAI, Extra: map[string]any{"openai_compact_supported": true}}
	unknown := &Account{ID: 3, Platform: PlatformOpenAI, Extra: map[string]any{}}
	candidates := []openAIAccountCandidateScore{
		{account: supported, loadInfo: &AccountLoadInfo{}, score: 3},
		{account: supportedOverflow, loadInfo: &AccountLoadInfo{}, score: 2},
		{account: unknown, loadInfo: &AccountLoadInfo{}, score: 1},
	}

	ordered := scheduler.buildOpenAISelectionOrder(OpenAIAccountScheduleRequest{RequireCompact: true}, openAIAccountLoadPlan{
		allCandidates:           candidates,
		candidates:              candidates,
		topK:                    1,
		includeOverflowFallback: true,
	})
	require.Equal(t, []int64{1, 2, 3}, candidateIDs(ordered))
}

func TestOpenAISelectionSkipsKnownFullAndSharesAcquireBudget(t *testing.T) {
	cache := &openAISelectionProbeTrackingCache{acquire: false}
	service := &OpenAIGatewayService{
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(cache),
	}
	scheduler := &defaultOpenAIAccountScheduler{service: service}
	full := probeBudgetTestAccount(1, 1)
	selected, _, err := scheduler.tryAcquireOpenAISelectionOrderWithBudget(
		context.Background(), OpenAIAccountScheduleRequest{RequiredTransport: OpenAIUpstreamTransportAny},
		[]openAIAccountCandidateScore{{account: full, loadKnown: true, loadInfo: &AccountLoadInfo{CurrentConcurrency: 1}}},
		newOpenAISelectionProbeBudget(),
	)
	require.NoError(t, err)
	require.Nil(t, selected)
	require.Empty(t, cache.acquireCalls)

	budget := newOpenAISelectionProbeBudget()
	budget.enableLimit()
	selectionOrder := make([]openAIAccountCandidateScore, 0, openAIAccountSelectionProbeLimit+1)
	for id := int64(1); id <= openAIAccountSelectionProbeLimit+1; id++ {
		selectionOrder = append(selectionOrder, openAIAccountCandidateScore{account: probeBudgetTestAccount(id, 1)})
	}
	_, _, err = scheduler.tryAcquireOpenAISelectionOrderWithBudget(
		context.Background(), OpenAIAccountScheduleRequest{RequiredTransport: OpenAIUpstreamTransportAny}, selectionOrder, budget,
	)
	require.NoError(t, err)
	_, _, err = scheduler.tryAcquireOpenAISelectionOrderWithBudget(
		context.Background(), OpenAIAccountScheduleRequest{RequiredTransport: OpenAIUpstreamTransportAny}, selectionOrder, budget,
	)
	require.NoError(t, err)
	require.Len(t, cache.acquireCalls, openAIAccountSelectionProbeLimit)
	var acquireTotal int
	for _, calls := range cache.acquireCalls {
		acquireTotal += calls
	}
	require.Equal(t, openAIAccountSelectionProbeLimit, acquireTotal)
}

func TestOpenAISelectionReleasesSlotWhenRecheckFails(t *testing.T) {
	candidate := probeBudgetTestAccount(1, 1)
	cache := &openAISelectionProbeTrackingCache{acquire: true}
	service := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{},
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(cache),
		schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
			accountsByID: map[int64]*Account{candidate.ID: candidate},
		}},
	}
	scheduler := &defaultOpenAIAccountScheduler{service: service}

	selection, compactBlocked, err := scheduler.tryAcquireOpenAISelectionOrderWithBudget(
		context.Background(), OpenAIAccountScheduleRequest{RequiredTransport: OpenAIUpstreamTransportAny},
		[]openAIAccountCandidateScore{{account: candidate}}, newOpenAISelectionProbeBudget(),
	)
	require.NoError(t, err)
	require.False(t, compactBlocked)
	require.Nil(t, selection)
	require.Equal(t, 1, cache.acquireCalls[candidate.ID])
	require.Equal(t, 1, cache.releaseCalls[candidate.ID])
}

func TestOpenAISelectionReleasesSlotWhenRecheckBudgetExhausts(t *testing.T) {
	candidate := probeBudgetTestAccount(1, 1)
	cache := &openAISelectionProbeTrackingCache{acquire: true}
	service := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{},
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(cache),
		schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
			accountsByID: map[int64]*Account{candidate.ID: candidate},
		}},
	}
	scheduler := &defaultOpenAIAccountScheduler{service: service}
	budget := newOpenAISelectionProbeBudget()
	budget.enableLimit()
	for i := 0; i < openAIAccountSelectionProbeLimit; i++ {
		require.True(t, budget.recordRecheck())
	}

	selection, _, err := scheduler.tryAcquireOpenAISelectionOrderWithBudget(
		context.Background(), OpenAIAccountScheduleRequest{RequiredTransport: OpenAIUpstreamTransportAny},
		[]openAIAccountCandidateScore{{account: candidate}}, budget,
	)
	require.NoError(t, err)
	require.Nil(t, selection)
	require.Equal(t, 1, cache.acquireCalls[candidate.ID])
	require.Equal(t, 1, cache.releaseCalls[candidate.ID])
}

func TestOpenAISelectionReleasesSlotWhenCompactIsUnsupported(t *testing.T) {
	candidate := probeBudgetTestAccount(1, 1)
	candidate.Extra = map[string]any{"openai_compact_supported": false}
	cache := &openAISelectionProbeTrackingCache{acquire: true}
	service := &OpenAIGatewayService{
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(cache),
	}
	scheduler := &defaultOpenAIAccountScheduler{service: service}

	selection, compactBlocked, err := scheduler.tryAcquireOpenAISelectionOrderWithBudget(
		context.Background(), OpenAIAccountScheduleRequest{
			RequireCompact:    true,
			RequiredTransport: OpenAIUpstreamTransportAny,
		},
		[]openAIAccountCandidateScore{{account: candidate}}, newOpenAISelectionProbeBudget(),
	)
	require.NoError(t, err)
	require.True(t, compactBlocked)
	require.Nil(t, selection)
	require.Equal(t, 1, cache.acquireCalls[candidate.ID])
	require.Equal(t, 1, cache.releaseCalls[candidate.ID])
}

func TestOpenAISelectionReacquireStopsAtAcquireBudget(t *testing.T) {
	candidate := probeBudgetTestAccount(1, 1)
	latest := probeBudgetTestAccount(1, 2)
	cache := &openAISelectionProbeTrackingCache{acquire: true}
	service := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{*latest}},
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(cache),
		schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
			accountsByID: map[int64]*Account{candidate.ID: candidate},
		}},
	}
	scheduler := &defaultOpenAIAccountScheduler{service: service}
	budget := newOpenAISelectionProbeBudget()
	budget.enableLimit()
	for i := 0; i < openAIAccountSelectionProbeLimit-1; i++ {
		require.True(t, budget.recordAcquire(int64(i+1)))
	}

	selection, _, err := scheduler.tryAcquireOpenAISelectionOrderWithBudget(
		context.Background(), OpenAIAccountScheduleRequest{RequiredTransport: OpenAIUpstreamTransportAny},
		[]openAIAccountCandidateScore{{account: candidate}}, budget,
	)
	require.NoError(t, err)
	require.Nil(t, selection)
	require.Equal(t, 1, cache.acquireCalls[candidate.ID])
	require.Equal(t, 1, cache.releaseCalls[candidate.ID])
}

func TestBuildOpenAIAccountLoadPlanTracksKnownLoad(t *testing.T) {
	account := probeBudgetTestAccount(1, 2)
	scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{cfg: &config.Config{}}}

	known := scheduler.buildOpenAIAccountLoadPlan(
		context.Background(), OpenAIAccountScheduleRequest{}, []*Account{account},
		map[int64]*AccountLoadInfo{account.ID: {AccountID: account.ID, CurrentConcurrency: 1}},
	)
	require.True(t, known.candidates[0].loadKnown)
	require.Equal(t, 1, known.candidates[0].loadInfo.CurrentConcurrency)

	unknown := scheduler.buildOpenAIAccountLoadPlan(
		context.Background(), OpenAIAccountScheduleRequest{}, []*Account{account}, map[int64]*AccountLoadInfo{},
	)
	require.False(t, unknown.candidates[0].loadKnown)
	require.Zero(t, unknown.candidates[0].loadInfo.CurrentConcurrency)
}

func TestOpenAISelectionReacquiresWhenConcurrencyChangesAfterRecheck(t *testing.T) {
	candidate := probeBudgetTestAccount(1, 1)
	latest := probeBudgetTestAccount(1, 2)
	cache := &openAISelectionProbeTrackingCache{acquire: true}
	service := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{*latest}},
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(cache),
		schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
			accountsByID: map[int64]*Account{candidate.ID: candidate},
		}},
	}
	scheduler := &defaultOpenAIAccountScheduler{service: service}

	selection, _, err := scheduler.tryAcquireOpenAISelectionOrderWithBudget(
		context.Background(), OpenAIAccountScheduleRequest{RequiredTransport: OpenAIUpstreamTransportAny},
		[]openAIAccountCandidateScore{{account: candidate}}, newOpenAISelectionProbeBudget(),
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, latest.ID, selection.Account.ID)
	require.Equal(t, 2, cache.acquireCalls[candidate.ID])
	require.Equal(t, 1, cache.releaseCalls[candidate.ID])
	selection.ReleaseFunc()
	require.Equal(t, 2, cache.releaseCalls[candidate.ID])
}
