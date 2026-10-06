//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func newSummaryService(groups ...GroupPricingSummary) (*PricingDerivationService, *mxFakeMatrixRepo) {
	repo := &mxFakeMatrixRepo{summaries: groups}
	return NewPricingDerivationService(repo, nil, nil), repo
}

func TestGroupSummaries_ByIDsReportsMissingAndUsesOneQuery(t *testing.T) {
	svc, repo := newSummaryService(
		GroupPricingSummary{GroupID: 1, Name: "a", Stage: PricingStageLegacy},
		GroupPricingSummary{GroupID: 2, Name: "b", Stage: PricingStageV2, HasConfig: true, Revision: 4,
			CostRules: CostRuleSummary{Total: 3, Enabled: 2, Manual: 1, LegacyDerived: 2}},
	)
	res, err := svc.GroupSummaries(context.Background(), []int64{9, 2, 2, 1, -5, 7})
	require.NoError(t, err)
	require.Len(t, repo.summaryCalls, 1, "批量读取只发一次仓储查询")
	require.Equal(t, []int64{1, 2, 7, 9}, repo.summaryCalls[0].ids, "ids 去重、去掉非正数并排序")
	require.Len(t, res.Items, 2)
	require.Equal(t, int64(4), res.Items[1].Revision)
	require.Equal(t, 3, res.Items[1].CostRules.Total)
	require.Equal(t, []int64{7, 9}, res.MissingIDs)
	require.False(t, res.Truncated)
}

func TestGroupSummaries_AllVisibleIsCapped(t *testing.T) {
	var groups []GroupPricingSummary
	for i := 1; i <= MaxGroupPricingSummaries+3; i++ {
		groups = append(groups, GroupPricingSummary{GroupID: int64(i)})
	}
	svc, repo := newSummaryService(groups...)
	res, err := svc.GroupSummaries(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, MaxGroupPricingSummaries+1, repo.summaryCalls[0].limit, "多取一行用来判断是否被截断")
	require.Len(t, res.Items, MaxGroupPricingSummaries)
	require.True(t, res.Truncated)
	require.Empty(t, res.MissingIDs)

	svc, _ = newSummaryService(groups[:3]...)
	res, err = svc.GroupSummaries(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, res.Items, 3)
	require.False(t, res.Truncated)
}

func TestGroupSummaries_EmptyAndErrors(t *testing.T) {
	svc, _ := newSummaryService()
	res, err := svc.GroupSummaries(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, res.Items)
	require.NotNil(t, res.MissingIDs)
	require.Empty(t, res.Items)

	ids := make([]int64, 0, MaxGroupPricingSummaries+1)
	for i := 1; i <= MaxGroupPricingSummaries+1; i++ {
		ids = append(ids, int64(i))
	}
	svc, repo := newSummaryService()
	_, err = svc.GroupSummaries(context.Background(), ids)
	require.Equal(t, ReasonGroupSummaryTooMany, infraerrors.Reason(err))
	require.Empty(t, repo.summaryCalls, "超过上限不查库")

	boom := errors.New("db down")
	svc, repo = newSummaryService()
	repo.summaryErr = boom
	_, err = svc.GroupSummaries(context.Background(), []int64{1})
	require.ErrorIs(t, err, boom)
}
