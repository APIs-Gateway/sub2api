//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type opsStatsRepositoryStub struct {
	AccountRepository
	listFn func(context.Context, string, *int64) ([]Account, error)
}

func (r *opsStatsRepositoryStub) ListOpsAccountsForStats(ctx context.Context, platform string, groupID *int64) ([]Account, error) {
	return r.listFn(ctx, platform, groupID)
}

type opsStatsFallbackRepositoryStub struct {
	AccountRepository
	listFn func(context.Context, pagination.PaginationParams, string, int64) ([]Account, *pagination.PaginationResult, error)
}

func (r *opsStatsFallbackRepositoryStub) ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, _, _, _ string, groupID int64, _ string) ([]Account, *pagination.PaginationResult, error) {
	return r.listFn(ctx, params, platform, groupID)
}

func TestListAllAccountsForOpsUsesStatsProjection(t *testing.T) {
	groupID := int64(42)
	repo := &opsStatsRepositoryStub{
		listFn: func(_ context.Context, platform string, gotGroupID *int64) ([]Account, error) {
			require.Equal(t, "openai", platform)
			require.NotNil(t, gotGroupID)
			require.Equal(t, groupID, *gotGroupID)
			return []Account{{ID: 1}}, nil
		},
	}

	accounts, err := (&OpsService{accountRepo: repo}).listAllAccountsForOps(context.Background(), "openai", &groupID)
	require.NoError(t, err)
	require.Equal(t, []Account{{ID: 1}}, accounts)
}

func TestListAllAccountsForOpsFallsBackToPagination(t *testing.T) {
	groupID := int64(42)
	calls := 0
	repo := &opsStatsFallbackRepositoryStub{
		listFn: func(_ context.Context, params pagination.PaginationParams, platform string, gotGroupID int64) ([]Account, *pagination.PaginationResult, error) {
			calls++
			require.Equal(t, 1, params.Page)
			require.Equal(t, opsAccountsPageSize, params.PageSize)
			require.Equal(t, "openai", platform)
			require.Equal(t, groupID, gotGroupID)
			return []Account{{ID: 1}}, &pagination.PaginationResult{Total: 1}, nil
		},
	}

	accounts, err := (&OpsService{accountRepo: repo}).listAllAccountsForOps(context.Background(), "openai", &groupID)
	require.NoError(t, err)
	require.Equal(t, []Account{{ID: 1}}, accounts)
	require.Equal(t, 1, calls)
}
