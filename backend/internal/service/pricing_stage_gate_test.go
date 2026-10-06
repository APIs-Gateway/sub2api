//go:build unit

package service

import (
	"errors"
	"sort"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

var sgNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// sgPassing 是一份全部满足的闸门输入；各个用例在它的基础上改一处。
func sgPassing() StageGateInput {
	changed := sgNow.Add(-100 * time.Hour)
	return StageGateInput{
		Now: sgNow,
		Facts: &StageGateFacts{
			Config: StageGateConfig{GroupID: 7, Stage: PricingStageShadow, Revision: 3, StageChangedAt: &changed, UpdatedAt: changed},
			Replay: &ReplayEvidence{
				ID: 11, GroupID: 7, RecordedAt: sgNow.Add(-2 * time.Hour),
				WindowFrom: sgNow.Add(-31 * 24 * time.Hour), WindowTo: sgNow.Add(-3 * time.Hour),
				MatrixSource: "derived", Passed: true, BindingStable: true, RowsReplayed: 1000,
				ChannelConfigHash: "chan-1", DeriveRevision: "rev-1",
			},
		},
		CurrentDeriveRevision:    "rev-1",
		CurrentChannelConfigHash: "chan-1",
	}
}

func sgCodes(r StageGateReport) []string {
	out := []string{}
	for _, f := range r.Failures {
		out = append(out, f.Code)
	}
	sort.Strings(out)
	return out
}

func TestEvaluateStageGate_PassesWhenEverythingHolds(t *testing.T) {
	r := EvaluateStageGate(sgPassing())
	require.True(t, r.Passed)
	require.Empty(t, r.Failures)
	require.True(t, r.Required)
	require.True(t, r.Observation.Satisfied)
	require.True(t, r.Replay.Present)
	require.True(t, r.Replay.BindingCurrent)
	require.True(t, r.Replay.ChannelConfigHashMatch)
}

func TestEvaluateStageGate_Branches(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(in *StageGateInput)
		want   []string
	}{
		{"legacy group", func(in *StageGateInput) { in.Facts.Config.Stage = PricingStageLegacy }, []string{ReasonPricingGateNotInShadow}},
		{"already v2", func(in *StageGateInput) { in.Facts.Config.Stage = PricingStageV2 }, []string{ReasonPricingGateNotInShadow}},
		{"observation 71h", func(in *StageGateInput) {
			t := sgNow.Add(-71 * time.Hour)
			in.Facts.Config.StageChangedAt, in.Facts.Config.UpdatedAt = &t, t
		}, []string{ReasonPricingGateObservation}},
		{"observation exactly 72h", func(in *StageGateInput) {
			t := sgNow.Add(-72 * time.Hour)
			in.Facts.Config.StageChangedAt, in.Facts.Config.UpdatedAt = &t, t
		}, nil},
		{"channel saved recently restarts the clock", func(in *StageGateInput) {
			t := sgNow.Add(-time.Hour)
			in.Facts.OwnerChannelUpdatedAt = &t
		}, []string{ReasonPricingGateObservation}},
		{"config edited recently restarts the clock", func(in *StageGateInput) {
			in.Facts.Config.UpdatedAt = sgNow.Add(-time.Minute)
		}, []string{ReasonPricingGateObservation}},
		{"shadow translation differences", func(in *StageGateInput) { in.Facts.Shadow.TranslationDiffs = 1 }, []string{ReasonPricingGateShadowDiffs}},
		{"in-process translation differences (lossy shadow table)", func(in *StageGateInput) { in.Facts.Shadow.TranslationDiffsInProcess = 2 },
			[]string{ReasonPricingGateShadowDiffsInProcess}},
		{"replay has no rows and the window has no traffic: exempt", func(in *StageGateInput) {
			in.Facts.Replay.RowsReplayed, in.Facts.Replay.RowsInWindow = 0, 0
		}, nil},
		{"replay has no rows but the window has traffic", func(in *StageGateInput) {
			in.Facts.Replay.RowsReplayed, in.Facts.Replay.RowsInWindow = 0, 12
		}, []string{ReasonPricingGateReplayEmpty}},
		{"replay covered part of the traffic", func(in *StageGateInput) {
			in.Facts.Replay.RowsReplayed, in.Facts.Replay.RowsInWindow = 3, 12
		}, nil},
		{"shadow expected differences are fine", func(in *StageGateInput) { in.Facts.Shadow.ExpectedDiffs = 50 }, nil},
		{"no replay", func(in *StageGateInput) { in.Facts.Replay = nil }, []string{ReasonPricingGateReplayMissing}},
		{"replay not passed", func(in *StageGateInput) { in.Facts.Replay.Passed = false }, []string{ReasonPricingGateReplayFailed}},
		{"replay binding unstable", func(in *StageGateInput) { in.Facts.Replay.BindingStable = false }, []string{ReasonPricingGateReplayFailed}},
		{"replay translation differences", func(in *StageGateInput) { in.Facts.Replay.TranslationDiffs = 2 }, []string{ReasonPricingGateReplayFailed}},
		{"replay errors", func(in *StageGateInput) { in.Facts.Replay.RowsErrored = 1 }, []string{ReasonPricingGateReplayFailed}},
		{"replay against stored matrix", func(in *StageGateInput) { in.Facts.Replay.MatrixSource = "stored" }, []string{ReasonPricingGateReplayFailed}},
		{"replay window 29 days", func(in *StageGateInput) {
			in.Facts.Replay.WindowFrom = in.Facts.Replay.WindowTo.Add(-29 * 24 * time.Hour)
		}, []string{ReasonPricingGateReplayShort}},
		{"replay window exactly 30 days", func(in *StageGateInput) {
			in.Facts.Replay.WindowFrom = in.Facts.Replay.WindowTo.Add(-30 * 24 * time.Hour)
		}, nil},
		{"replay window ended 31 days ago", func(in *StageGateInput) {
			in.Facts.Replay.WindowTo = sgNow.Add(-31 * 24 * time.Hour)
			in.Facts.Replay.WindowFrom = in.Facts.Replay.WindowTo.Add(-30 * 24 * time.Hour)
		}, []string{ReasonPricingGateReplayTooOld}},
		{"channel config changed after the replay (S-8)", func(in *StageGateInput) { in.CurrentChannelConfigHash = "chan-2" }, []string{ReasonPricingGateReplayStale}},
		{"derived matrix changed after the replay (S-8)", func(in *StageGateInput) { in.CurrentDeriveRevision = "rev-2" }, []string{ReasonPricingGateReplayStale}},
		{"current evidence unavailable", func(in *StageGateInput) {
			in.EvidenceErr = errors.New("boom")
			in.CurrentDeriveRevision, in.CurrentChannelConfigHash = "", ""
		}, []string{ReasonPricingGateDeriveFailed}},
		{"several failures are all listed", func(in *StageGateInput) {
			in.Facts.Config.Stage = PricingStageLegacy
			in.Facts.Shadow.TranslationDiffs = 3
			in.Facts.Replay = nil
		}, []string{ReasonPricingGateNotInShadow, ReasonPricingGateReplayMissing, ReasonPricingGateShadowDiffs}},
	}
	for _, c := range cases {
		in := sgPassing()
		c.mutate(&in)
		r := EvaluateStageGate(in)
		want := append([]string{}, c.want...)
		sort.Strings(want)
		if len(want) == 0 {
			want = []string{}
		}
		require.Equal(t, want, sgCodes(r), c.name)
		require.Equal(t, len(want) == 0, r.Passed, c.name)
	}
}

func TestEvaluateStageGate_NilFactsDoNotPass(t *testing.T) {
	r := EvaluateStageGate(StageGateInput{Now: sgNow})
	require.False(t, r.Passed)
	require.Equal(t, []string{ReasonPricingGateDeriveFailed}, sgCodes(r))
}

func TestStageObservedSince_TakesTheLatest(t *testing.T) {
	a, b, c := sgNow.Add(-10*time.Hour), sgNow.Add(-5*time.Hour), sgNow.Add(-8*time.Hour)
	cfg := StageGateConfig{StageChangedAt: &a, UpdatedAt: c}
	require.Equal(t, c, StageObservedSince(cfg, nil))
	require.Equal(t, b, StageObservedSince(cfg, &b))
	cfg.StageChangedAt = &b
	require.Equal(t, b, StageObservedSince(cfg, &a))
	cfg.StageChangedAt = nil
	require.Equal(t, c, StageObservedSince(cfg, nil))
}

func TestStageGateError_ReasonAndMetadata(t *testing.T) {
	require.NoError(t, StageGateError(7, StageGateReport{}))
	err := StageGateError(7, StageGateReport{Failures: []StageGateFailure{
		{Code: ReasonPricingGateReplayStale, Message: "stale"}, {Code: ReasonPricingGateObservation, Message: "short"}}})
	require.Error(t, err)
	require.Equal(t, ReasonPricingGateReplayStale, infraerrors.Reason(err))
	require.Equal(t, 409, int(infraerrors.Code(err)))
	meta := infraerrors.FromError(err).Metadata
	require.Equal(t, "7", meta["group_id"])
	require.Equal(t, ReasonPricingGateReplayStale+";"+ReasonPricingGateObservation, meta["failures"])
}

func TestStageSwitchAccepted_PriceDirection(t *testing.T) {
	good := &ReplayEvidence{ID: 1, RowsInWindow: 10, RowsReplayed: 10}
	// 没有任何差异、证据齐全：none。
	delta, accepted := StageSwitchAccepted(good, ShadowEvidence{}, nil)
	require.Equal(t, PriceDeltaNone, delta)
	require.Empty(t, accepted)

	// 没有回放：证明不了，unknown。
	delta, _ = StageSwitchAccepted(nil, ShadowEvidence{}, nil)
	require.Equal(t, PriceDeltaUnknown, delta)

	// 回放 0 行（含没有流量的分组）：什么也没比较，证明不了价格不变，unknown。
	delta, _ = StageSwitchAccepted(&ReplayEvidence{ID: 1}, ShadowEvidence{}, nil)
	require.Equal(t, PriceDeltaUnknown, delta)

	// 本实例进程内的翻译差异计数大于 0：unknown。
	delta, _ = StageSwitchAccepted(good, ShadowEvidence{TranslationDiffsInProcess: 1}, nil)
	require.Equal(t, PriceDeltaUnknown, delta)

	// 翻译差异不为 0：unknown。
	delta, _ = StageSwitchAccepted(&ReplayEvidence{RowsReplayed: 10, TranslationDiffs: 1}, ShadowEvidence{}, nil)
	require.Equal(t, PriceDeltaUnknown, delta)
	delta, _ = StageSwitchAccepted(good, ShadowEvidence{TranslationDiffs: 1}, nil)
	require.Equal(t, PriceDeltaUnknown, delta)

	// 附录 A 第 23 项：legacy 空 custom 记 0 元，v2 inherit 无价也记 0 元，最终 ActualCost 逐位相同，单独列出、方向 none。
	withZero := &ReplayEvidence{RowsReplayed: 10, Diffs: []PricingReplayDiffCount{
		{Kind: ShadowKindCost, Class: ShadowClassExpected, Reason: PricingReplayReasonUnpricedZero, Count: 40},
		{Kind: ShadowKindAccess, Class: ShadowClassTranslation, Count: 0},
	}}
	delta, accepted = StageSwitchAccepted(withZero, ShadowEvidence{}, nil)
	require.Equal(t, PriceDeltaNone, delta)
	require.Equal(t, []AcceptedDifference{{Source: "replay", Kind: ShadowKindCost, Reason: PricingReplayReasonUnpricedZero, Count: 40, PriceDelta: PriceDeltaNone}}, accepted)

	// 名字带空白：v2 按价收费，只会变多。
	up := &ReplayEvidence{RowsReplayed: 10, Diffs: []PricingReplayDiffCount{{Kind: ShadowKindCost, Class: ShadowClassExpected, Reason: PricingReplayReasonUntrimmedModel, Count: 1}}}
	delta, _ = StageSwitchAccepted(up, ShadowEvidence{}, nil)
	require.Equal(t, PriceDeltaUp, delta)

	// 关闭例外：请求会被挡，unknown。
	closed := &ReplayEvidence{RowsReplayed: 10, Diffs: []PricingReplayDiffCount{{Kind: ShadowKindAccess, Class: ShadowClassExpected, Reason: "closed_in_group", Count: 5}}}
	delta, _ = StageSwitchAccepted(closed, ShadowEvidence{}, nil)
	require.Equal(t, PriceDeltaUnknown, delta)

	// 影子的预期差异与目录来源都按 unknown 计，并稳定排序。
	delta, accepted = StageSwitchAccepted(good, ShadowEvidence{ExpectedModels: []string{"b", "a"}},
		[]AcceptedDifference{{Source: "catalog", Reason: AcceptedReasonCatalogDraft, Model: "m", Count: 2, PriceDelta: PriceDeltaUnknown}})
	require.Equal(t, PriceDeltaUnknown, delta)
	require.Len(t, accepted, 3)
	require.Equal(t, "catalog", accepted[0].Source)
	require.Equal(t, "a", accepted[1].Model)
	require.Equal(t, "b", accepted[2].Model)
}

func TestStageSwitchPlanHash_BindsEveryInput(t *testing.T) {
	base := StageSwitchPlan{GroupID: 7, From: PricingStageShadow, To: PricingStageV2, ConfigRevision: 3, DeriveRevision: "r", ChannelConfigHash: "c", ReplayID: 9}
	h := StageSwitchPlanHash(base)
	require.Len(t, h, 64)
	require.Equal(t, h, StageSwitchPlanHash(base))
	for name, mutate := range map[string]func(p *StageSwitchPlan){
		"group":           func(p *StageSwitchPlan) { p.GroupID = 8 },
		"config revision": func(p *StageSwitchPlan) { p.ConfigRevision = 4 },
		"derive revision": func(p *StageSwitchPlan) { p.DeriveRevision = "r2" },
		"channel hash":    func(p *StageSwitchPlan) { p.ChannelConfigHash = "c2" },
		"replay":          func(p *StageSwitchPlan) { p.ReplayID = 10 },
		"target":          func(p *StageSwitchPlan) { p.To = PricingStageShadow },
	} {
		p := base
		mutate(&p)
		require.NotEqual(t, h, StageSwitchPlanHash(p), name)
	}
}

func TestStageRank(t *testing.T) {
	require.Less(t, stageRank(PricingStageLegacy), stageRank(PricingStageShadow))
	require.Less(t, stageRank(PricingStageShadow), stageRank(PricingStageV2))
}

func TestReplayEvidenceFromSummary(t *testing.T) {
	require.Nil(t, ReplayEvidenceFromSummary(nil, sgNow))
	from, to := sgNow.Add(-30*24*time.Hour), sgNow
	sum := &PricingReplaySummary{
		WindowFrom: from, WindowTo: to,
		Meta: map[string]string{"matrix_source": "derived", "pricing_data_sha256": "sha", "version": "1.2.3"},
		Binding: PricingReplayBinding{
			ChannelConfigHashBefore: "c1", ChannelConfigHashAfter: "c1", Stable: true,
			Groups: []PricingReplayGroupBinding{
				{GroupID: 1, DeriveRevision: "r1", MatrixHashBefore: "m", MatrixHashAfter: "m"},
				{GroupID: 2, DeriveRevision: "r2", MatrixHashBefore: "m", MatrixHashAfter: "m"},
				{GroupID: 3, DeriveRevision: "r3", MatrixHashBefore: "m", MatrixHashAfter: "other"},
				{GroupID: 4, DeriveRevision: "", MatrixHashBefore: "m", MatrixHashAfter: "m"},
				{GroupID: 5, DeriveRevision: "r5", MatrixHashBefore: "m", MatrixHashAfter: "m", Error: "derive failed"},
			},
		},
		Groups: []PricingReplayGroupSummary{
			{GroupID: 1, RowsInWindow: 10, RowsReplayed: 10, Diffs: []PricingReplayDiffCount{{Kind: ShadowKindCost, Class: ShadowClassExpected, Reason: PricingReplayReasonUnpricedZero, Count: 4}}},
			{GroupID: 2, RowsInWindow: 10, RowsReplayed: 10, Diffs: []PricingReplayDiffCount{{Kind: ShadowKindCost, Class: ShadowClassTranslation, Count: 1}}},
			{GroupID: 3, RowsReplayed: 1},
			{GroupID: 4, RowsReplayed: 1},
			{GroupID: 5, RowsReplayed: 1},
			{GroupID: 6, RowsReplayed: 1, Errors: 2}, // 没有绑定信息：不记
		},
	}
	rows := ReplayEvidenceFromSummary(sum, sgNow)
	byGroup := map[int64]ReplayEvidence{}
	for _, r := range rows {
		byGroup[r.GroupID] = r
	}
	require.Len(t, rows, 5)
	require.True(t, byGroup[1].Passed)
	require.EqualValues(t, 4, byGroup[1].ExpectedDiffs)
	require.Equal(t, "derived", byGroup[1].MatrixSource)
	require.Equal(t, "c1", byGroup[1].ChannelConfigHash)
	require.Equal(t, "r1", byGroup[1].DeriveRevision)
	require.Equal(t, "sha", byGroup[1].PricingDataSHA256)
	require.Equal(t, "1.2.3", byGroup[1].ToolVersion)
	require.Equal(t, sgNow, byGroup[1].RecordedAt)
	require.False(t, byGroup[2].Passed, "translation differences")
	require.EqualValues(t, 1, byGroup[2].TranslationDiffs)
	require.False(t, byGroup[3].Passed, "matrix rows changed during the replay")
	require.False(t, byGroup[3].BindingStable)
	require.False(t, byGroup[4].Passed, "no derive revision")
	require.False(t, byGroup[5].Passed, "derive error")

	sum.Binding.Stable = false
	require.False(t, ReplayEvidenceFromSummary(sum, sgNow)[0].Passed, "channel configuration changed during the replay")
	sum.Meta["matrix_source"] = "stored"
	require.Equal(t, "stored", ReplayEvidenceFromSummary(sum, sgNow)[0].MatrixSource)
}
