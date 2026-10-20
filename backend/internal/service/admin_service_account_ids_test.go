//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type accountRepoStubForAccountIDs struct {
	accountRepoStub

	list  *AccountIDList
	err   error
	calls int
	got   struct {
		platform    string
		accountType string
		status      string
		search      string
		groupID     int64
		privacyMode string
		limit       int
	}
}

func (s *accountRepoStubForAccountIDs) ListIDsWithFilters(_ context.Context, platform, accountType, status, search string, groupID int64, privacyMode string, limit int) (*AccountIDList, error) {
	s.calls++
	s.got.platform = platform
	s.got.accountType = accountType
	s.got.status = status
	s.got.search = search
	s.got.groupID = groupID
	s.got.privacyMode = privacyMode
	s.got.limit = limit
	return s.list, s.err
}

func TestListAccountIDsPassesFiltersAndLimitToRepo(t *testing.T) {
	repo := &accountRepoStubForAccountIDs{
		list: &AccountIDList{IDs: []int64{3, 5}, Total: 2, Platforms: []string{"openai"}, Types: []string{"apikey"}},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	list, err := svc.ListAccountIDs(context.Background(), "openai", "apikey", "active", "relay", 9, "training_set")

	require.NoError(t, err)
	require.Equal(t, repo.list, list)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, "openai", repo.got.platform)
	require.Equal(t, "apikey", repo.got.accountType)
	require.Equal(t, "active", repo.got.status)
	require.Equal(t, "relay", repo.got.search)
	require.Equal(t, int64(9), repo.got.groupID)
	require.Equal(t, "training_set", repo.got.privacyMode)
	require.Equal(t, AccountIDsMaxLimit, repo.got.limit)
	require.Equal(t, 5000, AccountIDsMaxLimit)
}

func TestListAccountIDsRejectsMoreThanLimit(t *testing.T) {
	repo := &accountRepoStubForAccountIDs{
		// 仓储在超限时只返回总数，不返回 ID。
		list: &AccountIDList{IDs: []int64{}, Total: AccountIDsMaxLimit + 1},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	list, err := svc.ListAccountIDs(context.Background(), "", "", "", "", 0, "")

	require.Nil(t, list)
	require.Error(t, err)
	require.Equal(t, 400, infraerrors.Code(err))
	require.Equal(t, AccountIDsLimitExceededReason, infraerrors.Reason(err))
	metadata := infraerrors.FromError(err).Metadata
	require.Equal(t, "5001", metadata["total"])
	require.Equal(t, "5000", metadata["limit"])
}

func TestListAccountIDsAllowsExactlyTheLimit(t *testing.T) {
	ids := make([]int64, AccountIDsMaxLimit)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	repo := &accountRepoStubForAccountIDs{list: &AccountIDList{IDs: ids, Total: AccountIDsMaxLimit}}
	svc := &adminServiceImpl{accountRepo: repo}

	list, err := svc.ListAccountIDs(context.Background(), "", "", "", "", 0, "")

	require.NoError(t, err)
	require.Len(t, list.IDs, AccountIDsMaxLimit)
}

func TestListAccountIDsPropagatesRepoError(t *testing.T) {
	boom := errors.New("db down")
	svc := &adminServiceImpl{accountRepo: &accountRepoStubForAccountIDs{err: boom}}

	list, err := svc.ListAccountIDs(context.Background(), "", "", "", "", 0, "")

	require.Nil(t, list)
	require.ErrorIs(t, err, boom)
}

func TestListAccountIDsNilResultMeansEmpty(t *testing.T) {
	svc := &adminServiceImpl{accountRepo: &accountRepoStubForAccountIDs{}}

	list, err := svc.ListAccountIDs(context.Background(), "", "", "", "", 0, "")

	require.NoError(t, err)
	require.NotNil(t, list)
	require.Empty(t, list.IDs)
}
