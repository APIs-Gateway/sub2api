package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type groupCapacityAccountRepoStub struct {
	AccountRepository
	rows      []GroupAccountCapacityRow
	requested []int64
	err       error
}

func (s *groupCapacityAccountRepoStub) ListSchedulableCapacityByGroupIDs(_ context.Context, groupIDs []int64) ([]GroupAccountCapacityRow, error) {
	s.requested = append([]int64(nil), groupIDs...)
	if s.err != nil {
		return nil, s.err
	}
	return append([]GroupAccountCapacityRow(nil), s.rows...), nil
}

type groupCapacityGroupRepoStub struct {
	GroupRepository
	groupIDs  []int64
	listCalls int
	err       error
}

func (s *groupCapacityGroupRepoStub) ListActiveIDs(context.Context) ([]int64, error) {
	s.listCalls++
	if s.err != nil {
		return nil, s.err
	}
	return append([]int64(nil), s.groupIDs...), nil
}

type groupCapacitySequentialAccountRepoStub struct {
	AccountRepository
	accountsByGroup map[int64][]Account
	errs            map[int64]error
}

func (s *groupCapacitySequentialAccountRepoStub) ListSchedulableByGroupID(_ context.Context, groupID int64) ([]Account, error) {
	if err := s.errs[groupID]; err != nil {
		return nil, err
	}
	return append([]Account(nil), s.accountsByGroup[groupID]...), nil
}

type groupCapacitySequentialGroupRepoStub struct {
	GroupRepository
	groups []Group
	err    error
}

func (s *groupCapacitySequentialGroupRepoStub) ListActive(context.Context) ([]Group, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]Group(nil), s.groups...), nil
}

type groupCapacityConcurrencyCacheStub struct {
	ConcurrencyCache
	counts    map[int64]int
	requested []int64
}

func (s *groupCapacityConcurrencyCacheStub) GetAccountConcurrencyBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	s.requested = append([]int64(nil), accountIDs...)
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

type groupCapacitySessionCacheStub struct {
	SessionLimitCache
	counts       map[int64]int
	requested    []int64
	idleTimeouts map[int64]time.Duration
}

func (s *groupCapacitySessionCacheStub) GetActiveSessionCountBatch(_ context.Context, accountIDs []int64, idleTimeouts map[int64]time.Duration) (map[int64]int, error) {
	s.requested = append([]int64(nil), accountIDs...)
	s.idleTimeouts = make(map[int64]time.Duration, len(idleTimeouts))
	for id, timeout := range idleTimeouts {
		s.idleTimeouts[id] = timeout
	}
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

type groupCapacityRPMCacheStub struct {
	RPMCache
	counts    map[int64]int
	requested []int64
}

func (s *groupCapacityRPMCacheStub) GetRPMBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	s.requested = append([]int64(nil), accountIDs...)
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

func TestGetAllGroupCapacityBatchAggregatesRuntimeAndLimits(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{
		rows: []GroupAccountCapacityRow{
			{
				GroupID:     10,
				AccountID:   1,
				Concurrency: 2,
				Extra: map[string]any{
					"max_sessions":                 3,
					"session_idle_timeout_minutes": 7,
					"base_rpm":                     11,
				},
			},
			{
				GroupID:     20,
				AccountID:   1,
				Concurrency: 2,
				Extra: map[string]any{
					"max_sessions":                 3,
					"session_idle_timeout_minutes": 7,
					"base_rpm":                     11,
				},
			},
			{
				GroupID:     20,
				AccountID:   2,
				Concurrency: 4,
				Extra: map[string]any{
					"max_sessions":                 1,
					"session_idle_timeout_minutes": 9,
					"base_rpm":                     13,
				},
			},
		},
	}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}
	concurrencyCache := &groupCapacityConcurrencyCacheStub{counts: map[int64]int{1: 1, 2: 2}}
	sessionCache := &groupCapacitySessionCacheStub{counts: map[int64]int{1: 2, 2: 1}}
	rpmCache := &groupCapacityRPMCacheStub{counts: map[int64]int{1: 5, 2: 7}}
	svc := NewGroupCapacityService(
		accountRepo,
		groupRepo,
		NewConcurrencyService(concurrencyCache),
		sessionCache,
		rpmCache,
	)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, groupRepo.listCalls)
	require.Equal(t, []int64{10, 20}, accountRepo.requested)
	require.Equal(t, []int64{1, 2}, concurrencyCache.requested)
	require.ElementsMatch(t, []int64{1, 2}, sessionCache.requested)
	require.ElementsMatch(t, []int64{1, 2}, rpmCache.requested)
	require.Equal(t, 7*time.Minute, sessionCache.idleTimeouts[1])
	require.Equal(t, 9*time.Minute, sessionCache.idleTimeouts[2])

	require.Equal(t, []GroupCapacitySummary{
		{
			GroupID:         10,
			ConcurrencyUsed: 1,
			ConcurrencyMax:  2,
			SessionsUsed:    2,
			SessionsMax:     3,
			RPMUsed:         5,
			RPMMax:          11,
		},
		{
			GroupID:         20,
			ConcurrencyUsed: 3,
			ConcurrencyMax:  6,
			SessionsUsed:    3,
			SessionsMax:     4,
			RPMUsed:         12,
			RPMMax:          24,
		},
	}, results)
}

func TestGetAllGroupCapacityBatchKeepsEmptyGroupRows(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{
		rows: []GroupAccountCapacityRow{
			{GroupID: 20, AccountID: 2, Concurrency: 4},
		},
	}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}
	svc := NewGroupCapacityService(accountRepo, groupRepo, nil, nil, nil)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)

	require.Equal(t, []GroupCapacitySummary{
		{GroupID: 10},
		{GroupID: 20, ConcurrencyMax: 4},
	}, results)
}

func TestGetAllGroupCapacityReturnsListerErrors(t *testing.T) {
	t.Run("active groups", func(t *testing.T) {
		want := errors.New("groups unavailable")
		svc := NewGroupCapacityService(
			&groupCapacityAccountRepoStub{},
			&groupCapacityGroupRepoStub{err: want},
			nil, nil, nil,
		)

		_, err := svc.GetAllGroupCapacity(context.Background())
		require.ErrorIs(t, err, want)
	})

	t.Run("capacity rows", func(t *testing.T) {
		want := errors.New("accounts unavailable")
		svc := NewGroupCapacityService(
			&groupCapacityAccountRepoStub{err: want},
			&groupCapacityGroupRepoStub{groupIDs: []int64{10}},
			nil, nil, nil,
		)

		_, err := svc.GetAllGroupCapacity(context.Background())
		require.ErrorIs(t, err, want)
	})
}

func TestGetAllGroupCapacityBatchSkipsInvalidAndDuplicateRows(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{rows: []GroupAccountCapacityRow{
		{GroupID: 10, AccountID: 1, Concurrency: 2},
		{GroupID: 10, AccountID: 1, Concurrency: 99},
		{GroupID: 10, AccountID: 0, Concurrency: 99},
		{GroupID: 999, AccountID: 2, Concurrency: 99},
	}}
	svc := NewGroupCapacityService(
		accountRepo,
		&groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}},
		NewConcurrencyService(&groupCapacityConcurrencyCacheStub{counts: map[int64]int{1: 1}}),
		nil, nil,
	)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)
	require.Equal(t, []GroupCapacitySummary{
		{GroupID: 10, ConcurrencyUsed: 1, ConcurrencyMax: 2},
		{GroupID: 20},
	}, results)
}

func TestGetAllGroupCapacityFallsBackToSequentialRepositories(t *testing.T) {
	accountRepo := &groupCapacitySequentialAccountRepoStub{
		accountsByGroup: map[int64][]Account{
			10: []Account{{ID: 1, Concurrency: 3}},
			20: []Account{{ID: 2, Concurrency: 4}},
		},
		errs: map[int64]error{30: errors.New("skip broken group")},
	}
	svc := NewGroupCapacityService(
		accountRepo,
		&groupCapacitySequentialGroupRepoStub{groups: []Group{{ID: 10}, {ID: 20}, {ID: 30}}},
		NewConcurrencyService(&groupCapacityConcurrencyCacheStub{counts: map[int64]int{1: 1, 2: 2}}),
		nil, nil,
	)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)
	require.Equal(t, []GroupCapacitySummary{
		{GroupID: 10, ConcurrencyUsed: 1, ConcurrencyMax: 3},
		{GroupID: 20, ConcurrencyUsed: 2, ConcurrencyMax: 4},
	}, results)
}

func TestAccountIDsForGroupsWithLimitDeduplicatesAndFilters(t *testing.T) {
	ids := accountIDsForGroupsWithLimit(
		[]groupCapacityAccountRef{{groupID: 10, accountID: 1}, {groupID: 10, accountID: 1}, {groupID: 20, accountID: 1}, {groupID: 20, accountID: 2}, {groupID: 999, accountID: 3}},
		map[int64]int{10: 0, 20: 1},
		[]GroupCapacitySummary{{SessionsMax: 1}, {SessionsMax: 0}},
		func(summary GroupCapacitySummary) bool { return summary.SessionsMax > 0 },
	)
	require.Equal(t, []int64{1}, ids)
}
