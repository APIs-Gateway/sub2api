//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type unpricedOpsRepoStub struct {
	OpsRepository
	rows       []*OpsUnpricedBillingRow
	err        error
	calls      int
	lastFilter *OpsUnpricedBillingFilter
}

func (s *unpricedOpsRepoStub) ListUnpricedBillingUsage(_ context.Context, filter *OpsUnpricedBillingFilter) ([]*OpsUnpricedBillingRow, error) {
	s.calls++
	s.lastFilter = filter
	if s.err != nil {
		return nil, s.err
	}
	return s.rows, nil
}

type unpricedSettingRepoStub struct {
	SettingRepository
	values map[string]string
}

func (s *unpricedSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

func unpricedRow(groupID int64, model string, rows int64) *OpsUnpricedBillingRow {
	row := &OpsUnpricedBillingRow{Model: model, Rows: rows}
	if groupID > 0 {
		id := groupID
		row.GroupID = &id
	}
	return row
}

func TestCollectUnpricedBilling_MarksKnownFreeAndSorts(t *testing.T) {
	repo := &unpricedOpsRepoStub{rows: []*OpsUnpricedBillingRow{
		unpricedRow(33, "free-model", 40),
		unpricedRow(16, "gpt-b", 3),
		unpricedRow(18, "gpt-a", 3),
		unpricedRow(27, "gpt-c", 9),
		nil,
	}}
	settings := &unpricedSettingRepoStub{values: map[string]string{
		SettingKeyBillingKnownFreeList: `[{"group_id":33,"model":"free-model"}]`,
	}}

	rows, knownFree, err := collectUnpricedBilling(context.Background(), repo, settings, &OpsUnpricedBillingFilter{})
	require.NoError(t, err)
	require.Equal(t, []BillingKnownFreeEntry{{GroupID: 33, Model: "free-model"}}, knownFree)

	order := make([]string, 0, len(rows))
	for _, row := range rows {
		if row != nil {
			order = append(order, row.Model)
		}
	}
	// 不在名单里的先列，行数降序，行数相同按分组；名单里的排最后。
	require.Equal(t, []string{"gpt-c", "gpt-b", "gpt-a", "free-model"}, order)
	require.True(t, rows[3].KnownFree)
	require.False(t, rows[0].KnownFree)
}

func TestCollectUnpricedBilling_UnsupportedRepository(t *testing.T) {
	_, _, err := collectUnpricedBilling(context.Background(), nil, nil, &OpsUnpricedBillingFilter{})
	require.ErrorIs(t, err, errOpsUnpricedBillingUnsupported)

	_, _, err = collectUnpricedBilling(context.Background(), &opsRepoMock{}, nil, &OpsUnpricedBillingFilter{})
	require.ErrorIs(t, err, errOpsUnpricedBillingUnsupported)
}

func TestCollectUnpricedBilling_RepositoryErrorIsReturned(t *testing.T) {
	boom := errors.New("db down")
	_, _, err := collectUnpricedBilling(context.Background(), &unpricedOpsRepoStub{err: boom}, nil, &OpsUnpricedBillingFilter{})
	require.ErrorIs(t, err, boom)
}

func TestLoadBillingKnownFreeList_BadConfigFallsBackToEmpty(t *testing.T) {
	ctx := context.Background()
	require.Empty(t, loadBillingKnownFreeList(ctx, nil))
	require.Empty(t, loadBillingKnownFreeList(ctx, &unpricedSettingRepoStub{}), "键不存在是空名单")
	require.Empty(t, loadBillingKnownFreeList(ctx, &unpricedSettingRepoStub{values: map[string]string{
		SettingKeyBillingKnownFreeList: `not json`,
	}}), "写坏的名单按空处理，宁可多告警")
}

func TestLoadBillingKnownFreeList_UnknownFieldDropsWholeList(t *testing.T) {
	settings := &unpricedSettingRepoStub{values: map[string]string{
		SettingKeyBillingKnownFreeList: `[{"group_id":16,"model":"ok-model"},{"groupId":16,"model":"typo-model"}]`,
	}}
	require.Empty(t, loadBillingKnownFreeList(context.Background(), settings), "字段名写错时整份名单作废，包括写对的那一项")
}

func TestComputeRuleMetric_MisspelledKnownFreeListDoesNotMaskRows(t *testing.T) {
	repo := &unpricedOpsRepoStub{rows: []*OpsUnpricedBillingRow{
		unpricedRow(16, "free-model", 4),
		unpricedRow(18, "free-model", 6),
	}}
	svc := &OpsAlertEvaluatorService{
		opsRepo: repo,
		opsService: &OpsService{settingRepo: &unpricedSettingRepoStub{values: map[string]string{
			// group_id 写成了 groupId：旧实现会把它当成「任意分组」，把两个分组的同名模型都盖住。
			SettingKeyBillingKnownFreeList: `[{"groupId":16,"model":"free-model"}]`,
		}}},
	}

	value, ok := svc.computeRuleMetric(context.Background(), &OpsAlertRule{MetricType: OpsAlertMetricUnpricedBillingRows},
		nil, time.Now().Add(-5*time.Minute), time.Now(), "", nil)
	require.True(t, ok)
	require.InDelta(t, 10.0, value, 1e-9, "名单写坏时按空名单处理，只会多告警")
}

func TestLoadBillingKnownFreeList_ReadErrorFallsBackToEmpty(t *testing.T) {
	require.Empty(t, loadBillingKnownFreeList(context.Background(), &unpricedFailingSettingRepo{}))
}

type unpricedFailingSettingRepo struct {
	SettingRepository
}

func (s *unpricedFailingSettingRepo) GetValue(context.Context, string) (string, error) {
	return "", errors.New("settings unavailable")
}

func TestComputeRuleMetric_UnpricedBillingRowsExcludesKnownFree(t *testing.T) {
	repo := &unpricedOpsRepoStub{rows: []*OpsUnpricedBillingRow{
		unpricedRow(16, "gpt-a", 5),
		unpricedRow(33, "free-model", 40),
		unpricedRow(18, "gpt-b", 2),
	}}
	svc := &OpsAlertEvaluatorService{
		opsRepo: repo,
		opsService: &OpsService{settingRepo: &unpricedSettingRepoStub{values: map[string]string{
			SettingKeyBillingKnownFreeList: `[{"group_id":33,"model":"free-model"}]`,
		}}},
	}
	start := time.Now().UTC().Add(-5 * time.Minute)
	end := time.Now().UTC()
	groupID := int64(16)

	value, ok := svc.computeRuleMetric(context.Background(), &OpsAlertRule{MetricType: OpsAlertMetricUnpricedBillingRows},
		nil, start, end, " OpenAI ", &groupID)
	require.True(t, ok)
	require.InDelta(t, 7.0, value, 1e-9, "只数不在已知免费名单里的行")

	require.NotNil(t, repo.lastFilter)
	require.Equal(t, start, repo.lastFilter.StartTime)
	require.Equal(t, end, repo.lastFilter.EndTime)
	require.Equal(t, "openai", repo.lastFilter.Platform, "规则的 platform 作用域被规整后传入")
	require.NotNil(t, repo.lastFilter.GroupID)
	require.EqualValues(t, 16, *repo.lastFilter.GroupID)
}

func TestComputeRuleMetric_UnpricedBillingRowsNoRowsIsZero(t *testing.T) {
	svc := &OpsAlertEvaluatorService{opsRepo: &unpricedOpsRepoStub{}}
	value, ok := svc.computeRuleMetric(context.Background(), &OpsAlertRule{MetricType: OpsAlertMetricUnpricedBillingRows},
		nil, time.Now().Add(-5*time.Minute), time.Now(), "", nil)
	require.True(t, ok)
	require.Zero(t, value)
}

func TestComputeRuleMetric_UnpricedBillingRowsUnavailableWhenQueryFails(t *testing.T) {
	ctx := context.Background()
	start, end := time.Now().Add(-5*time.Minute), time.Now()
	rule := &OpsAlertRule{MetricType: OpsAlertMetricUnpricedBillingRows}

	failing := &OpsAlertEvaluatorService{opsRepo: &unpricedOpsRepoStub{err: errors.New("db down")}}
	_, ok := failing.computeRuleMetric(ctx, rule, nil, start, end, "", nil)
	require.False(t, ok)

	unsupported := &OpsAlertEvaluatorService{opsRepo: &opsRepoMock{}}
	_, ok = unsupported.computeRuleMetric(ctx, rule, nil, start, end, "", nil)
	require.False(t, ok)

	var nilSvc *OpsAlertEvaluatorService
	_, ok = nilSvc.computeUnpricedBillingRows(ctx, start, end, "", nil)
	require.False(t, ok)
}

func TestDescribeUnpricedBillingAlert_ListsTopOffendersOnly(t *testing.T) {
	rows := []*OpsUnpricedBillingRow{
		unpricedRow(16, "m1", 9),
		unpricedRow(16, "m2", 8),
		unpricedRow(18, "m3", 7),
		unpricedRow(18, "m4", 6),
		unpricedRow(27, "m5", 5),
		unpricedRow(27, "m6", 4),
		unpricedRow(33, "free-model", 100),
	}
	svc := &OpsAlertEvaluatorService{
		opsRepo: &unpricedOpsRepoStub{rows: rows},
		opsService: &OpsService{settingRepo: &unpricedSettingRepoStub{values: map[string]string{
			SettingKeyBillingKnownFreeList: `[{"group_id":33,"model":"free-model"}]`,
		}}},
	}
	got := svc.describeUnpricedBillingAlert(context.Background(), time.Now().Add(-5*time.Minute), time.Now(), "", nil)
	require.Equal(t, "; top: group 16 / m1 x9, group 16 / m2 x8, group 18 / m3 x7, group 18 / m4 x6, group 27 / m5 x5", got)

	onlyFree := &OpsAlertEvaluatorService{
		opsRepo:    &unpricedOpsRepoStub{rows: []*OpsUnpricedBillingRow{unpricedRow(33, "free-model", 3)}},
		opsService: svc.opsService,
	}
	require.Equal(t, "", onlyFree.describeUnpricedBillingAlert(context.Background(), time.Now(), time.Now(), "", nil),
		"只剩已知免费行时不追加详情")

	failing := &OpsAlertEvaluatorService{opsRepo: &unpricedOpsRepoStub{err: errors.New("db down")}}
	require.Equal(t, "", failing.describeUnpricedBillingAlert(context.Background(), time.Now(), time.Now(), "", nil))

	var nilSvc *OpsAlertEvaluatorService
	require.Equal(t, "", nilSvc.describeUnpricedBillingAlert(context.Background(), time.Now(), time.Now(), "", nil))
}

func TestGetUnpricedBillingReport_SummarisesAndTruncates(t *testing.T) {
	resetUnpricedBillingCountersForTest()
	incrementUnpricedBillingCounter("openai", 16, "gpt-a", UnpricedBillingReasonMissingPrice)

	rows := []*OpsUnpricedBillingRow{
		unpricedRow(16, "gpt-a", 5),
		unpricedRow(18, "gpt-b", 4),
		unpricedRow(33, "free-model", 40),
	}
	repo := &unpricedOpsRepoStub{rows: rows}
	svc := &OpsService{
		opsRepo: repo,
		settingRepo: &unpricedSettingRepoStub{values: map[string]string{
			SettingKeyBillingKnownFreeList: `[{"group_id":33,"model":"free-model","note":"promo"}]`,
		}},
	}

	groupID := int64(16)
	report, err := svc.GetUnpricedBillingReport(context.Background(), "", " OpenAI ", &groupID, 2)
	require.NoError(t, err)
	require.Equal(t, OpsUnpricedBillingWindow24h, report.Window, "默认 24 小时")
	require.Equal(t, 24*time.Hour, report.EndTime.Sub(report.StartTime))
	require.Equal(t, "openai", report.Platform)
	require.EqualValues(t, 49, report.TotalRows)
	require.EqualValues(t, 40, report.KnownFreeRows)
	require.EqualValues(t, 9, report.UnpricedRows, "与告警指标同一个口径")
	require.True(t, report.Truncated)
	require.Len(t, report.Items, 2)
	require.Equal(t, "gpt-a", report.Items[0].Model)
	require.Equal(t, []BillingKnownFreeEntry{{GroupID: 33, Model: "free-model", Note: "promo"}}, report.KnownFreeList)
	require.Len(t, report.ProcessCounters, 1)
	require.EqualValues(t, 1, report.ProcessCounters[0].Count)

	require.NotNil(t, repo.lastFilter)
	require.Equal(t, "openai", repo.lastFilter.Platform)
	require.EqualValues(t, 16, *repo.lastFilter.GroupID)
}

func TestGetUnpricedBillingReport_SevenDayWindowAndLimitBounds(t *testing.T) {
	repo := &unpricedOpsRepoStub{}
	svc := &OpsService{opsRepo: repo}

	report, err := svc.GetUnpricedBillingReport(context.Background(), OpsUnpricedBillingWindow7d, "", nil, 100000)
	require.NoError(t, err)
	require.Equal(t, OpsUnpricedBillingWindow7d, report.Window)
	require.Equal(t, 7*24*time.Hour, report.EndTime.Sub(report.StartTime))
	require.Empty(t, report.Items)
	require.NotNil(t, report.Items, "无数据时 items 是空数组而不是 null")
	require.NotNil(t, report.KnownFreeList)
	require.Zero(t, report.UnpricedRows)
	require.False(t, report.Truncated)
}

func TestGetUnpricedBillingReport_LargeResultIsCappedAtMaxLimit(t *testing.T) {
	rows := make([]*OpsUnpricedBillingRow, 0, opsUnpricedBillingMaxLimit+10)
	for i := 0; i < opsUnpricedBillingMaxLimit+10; i++ {
		rows = append(rows, unpricedRow(int64(i+1), "gpt-a", 1))
	}
	svc := &OpsService{opsRepo: &unpricedOpsRepoStub{rows: rows}}

	report, err := svc.GetUnpricedBillingReport(context.Background(), "", "", nil, 100000)
	require.NoError(t, err)
	require.Len(t, report.Items, opsUnpricedBillingMaxLimit)
	require.True(t, report.Truncated)
	require.EqualValues(t, opsUnpricedBillingMaxLimit+10, report.TotalRows, "汇总数覆盖全部命中行，不受 limit 影响")

	defaulted, err := svc.GetUnpricedBillingReport(context.Background(), "", "", nil, 0)
	require.NoError(t, err)
	require.Len(t, defaulted.Items, opsUnpricedBillingDefaultLimit)
}

func TestGetUnpricedBillingReport_Errors(t *testing.T) {
	ctx := context.Background()

	_, err := (&OpsService{opsRepo: &unpricedOpsRepoStub{}}).GetUnpricedBillingReport(ctx, "30d", "", nil, 0)
	require.Error(t, err, "只支持 24h 与 7d")

	_, err = (&OpsService{opsRepo: &unpricedOpsRepoStub{err: errors.New("db down")}}).GetUnpricedBillingReport(ctx, "", "", nil, 0)
	require.Error(t, err)

	disabled := &OpsService{opsRepo: &unpricedOpsRepoStub{}, cfg: &config.Config{}}
	_, err = disabled.GetUnpricedBillingReport(ctx, "", "", nil, 0)
	require.ErrorIs(t, err, ErrOpsDisabled)

	var nilSvc *OpsService
	_, err = nilSvc.GetUnpricedBillingReport(ctx, "", "", nil, 0)
	require.ErrorIs(t, err, ErrOpsDisabled)

	unsupported := &OpsService{opsRepo: &opsRepoMock{}}
	report, err := unsupported.GetUnpricedBillingReport(ctx, "", "", nil, 0)
	require.NoError(t, err, "仓库不支持时返回空报告而不是报错")
	require.Empty(t, report.Items)
}
