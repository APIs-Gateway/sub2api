//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type batchLimitsUserRepoStub struct {
	*userRepoStub
	userIDs     []int64
	concurrency *int
	rpmLimit    *int
	affected    int
}

func (s *batchLimitsUserRepoStub) BatchUpdateLimits(_ context.Context, userIDs []int64, concurrency, rpmLimit *int) (int, error) {
	s.userIDs = append([]int64(nil), userIDs...)
	if concurrency != nil {
		value := *concurrency
		s.concurrency = &value
	}
	if rpmLimit != nil {
		value := *rpmLimit
		s.rpmLimit = &value
	}
	return s.affected, nil
}

func TestAdminService_BatchUpdateLimitsCleansIDsAndInvalidatesAuthCache(t *testing.T) {
	concurrency := 0
	rpmLimit := 60
	repo := &batchLimitsUserRepoStub{
		userRepoStub: &userRepoStub{},
		affected:     2,
	}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		authCacheInvalidator: invalidator,
	}

	affected, err := svc.BatchUpdateLimits(context.Background(), []int64{4, 0, 4, -1, 9}, &concurrency, &rpmLimit)

	require.NoError(t, err)
	require.Equal(t, 2, affected)
	require.Equal(t, []int64{4, 9}, repo.userIDs)
	require.Equal(t, &concurrency, repo.concurrency)
	require.Equal(t, &rpmLimit, repo.rpmLimit)
	require.Equal(t, []int64{4, 9}, invalidator.userIDs)
}

func TestAdminService_BatchUpdateLimitsRequiresAField(t *testing.T) {
	repo := &batchLimitsUserRepoStub{userRepoStub: &userRepoStub{}}
	svc := &adminServiceImpl{userRepo: repo}

	_, err := svc.BatchUpdateLimits(context.Background(), []int64{1}, nil, nil)

	require.EqualError(t, err, "at least one of concurrency or rpm_limit is required")
	require.Empty(t, repo.userIDs)
}
