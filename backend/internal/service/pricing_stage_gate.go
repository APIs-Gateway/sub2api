package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR7b：shadow 到 v2 的切换闸门（设计 4.3、4.5，S-8）。纯函数，没有 I/O，方便穷举各个分支。
//
// 闸门要同时满足（缺一不可）：
//  1. 分组现在是 shadow；
//  2. 影子观察不少于 72 小时，起点是「进入 shadow、分组配置最近一次变化、所属渠道最近一次保存」三者里最晚的那个
//     （渠道改过，观察期重新计时）；这段时间里 translation 类差异为 0；
//  3. 该分组最近一次回放（pricing-replay --record 写入）：通过（翻译差异为 0、没有回放错误、绑定稳定）、
//     窗口不短于 30 天、结束时间不早于 30 天前、用的是实时派生的矩阵；
//  4. 回放绑定的「渠道配置摘要」与「派生 revision」等于现在的值。渠道配置在回放之后改过，结果作废，要求重跑。
//
// 影子的样本数不是硬门槛：计数器在各实例的进程内、重启清零，加不起来；设计里「样本太少的分组以回放覆盖为准」，
// 所以样本数只作为信息给出，回放才是硬条件。

const (
	// PricingGateObservation 影子观察的最短时长。
	PricingGateObservation = 72 * time.Hour
	// PricingGateReplayWindow 回放窗口的最短长度。
	PricingGateReplayWindow = 30 * 24 * time.Hour
	// PricingGateReplayMaxAge 回放窗口结束时间距现在的上限：太旧的回放不代表现在的流量形态。
	PricingGateReplayMaxAge = 30 * 24 * time.Hour
	// PricingGateShadowRetention 影子差异样本的保留期是 14 天（pricing_shadow_diffs），统计窗口取 13 天，避开清理的边界。
	PricingGateShadowRetention = 13 * 24 * time.Hour
	// PricingGateExpectedTrafficWindow 预期差异里「近期有流量」的口径：7 天（设计 4.3）。
	PricingGateExpectedTrafficWindow = 7 * 24 * time.Hour
)

// 闸门不满足时的错误原因（HTTP 409，metadata.failures 带全部不满足的原因，用分号连接）。
const (
	ReasonPricingGateNotInShadow    = "PRICING_GATE_NOT_IN_SHADOW"
	ReasonPricingGateObservation    = "PRICING_GATE_OBSERVATION_SHORT"
	ReasonPricingGateShadowDiffs    = "PRICING_GATE_SHADOW_DIFFS"
	ReasonPricingGateReplayMissing  = "PRICING_GATE_REPLAY_MISSING"
	ReasonPricingGateReplayFailed   = "PRICING_GATE_REPLAY_FAILED"
	ReasonPricingGateReplayShort    = "PRICING_GATE_REPLAY_WINDOW_SHORT"
	ReasonPricingGateReplayTooOld   = "PRICING_GATE_REPLAY_TOO_OLD"
	ReasonPricingGateReplayStale    = "PRICING_GATE_REPLAY_STALE"
	ReasonPricingGateDeriveFailed   = "PRICING_GATE_DERIVE_FAILED"
	ReasonPricingStageChanged       = "PRICING_STAGE_CHANGED"
	ReasonPricingStageNeedsApproval = "PRICING_STAGE_APPROVAL_REQUIRED"
)

// PriceWriteKindStageSwitch 审批记录的种类：阶段切换（shadow 到 v2）。
const PriceWriteKindStageSwitch = "stage_switch"

// StageGateFailure 一条不满足的原因。Code 是稳定的机器可读码，Message 是英文说明（界面按 Code 展示）。
type StageGateFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// StageGateConfig 是 group_model_config 里与闸门有关的字段。
type StageGateConfig struct {
	GroupID        int64        `json:"group_id"`
	Stage          PricingStage `json:"stage"`
	Revision       int64        `json:"revision"`
	StageChangedAt *time.Time   `json:"stage_changed_at,omitempty"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

// ReplayEvidence 一次回放里一个分组的结果（pricing_replay_evidence 的一行）。
type ReplayEvidence struct {
	ID                int64                    `json:"id"`
	GroupID           int64                    `json:"group_id"`
	RecordedAt        time.Time                `json:"recorded_at"`
	WindowFrom        time.Time                `json:"window_from"`
	WindowTo          time.Time                `json:"window_to"`
	MatrixSource      string                   `json:"matrix_source"`
	Passed            bool                     `json:"passed"`
	BindingStable     bool                     `json:"binding_stable"`
	RowsInWindow      int64                    `json:"rows_in_window"`
	RowsReplayed      int64                    `json:"rows_replayed"`
	RowsErrored       int64                    `json:"rows_errored"`
	TranslationDiffs  int64                    `json:"translation_diffs"`
	ExpectedDiffs     int64                    `json:"expected_diffs"`
	ChannelConfigHash string                   `json:"channel_config_hash"`
	DeriveRevision    string                   `json:"derive_revision"`
	MatrixHash        string                   `json:"matrix_hash,omitempty"`
	PricingDataSHA256 string                   `json:"pricing_data_sha256,omitempty"`
	ToolVersion       string                   `json:"tool_version,omitempty"`
	Diffs             []PricingReplayDiffCount `json:"diffs"`
}

// ShadowEvidence 影子比对留在库里的差异样本（统计窗口见 WindowFrom）。
type ShadowEvidence struct {
	// WindowFrom 统计窗口的起点：观察起点与样本保留期里较晚的那个。
	WindowFrom       time.Time `json:"window_from"`
	TranslationDiffs int64     `json:"translation_diffs"`
	ExpectedDiffs    int64     `json:"expected_diffs"`
	// ExpectedModels 近 7 天出现过预期差异样本的模型（它们是真实请求触发的，所以有流量）。
	ExpectedModels []string `json:"expected_models"`
	// ComparedInProcess 本实例进程内的比对次数，仅供参考（多实例各算各的，重启清零）。
	ComparedInProcess int64 `json:"compared_in_process"`
}

// StageGateFacts 闸门要看的、从库里读到的事实。
type StageGateFacts struct {
	Config StageGateConfig `json:"config"`
	// OwnerChannelUpdatedAt 分组所属渠道最近一次保存的时间；没有渠道为 nil。
	OwnerChannelUpdatedAt *time.Time `json:"owner_channel_updated_at,omitempty"`
	Shadow                ShadowEvidence
	// Replay 该分组最近一次回放；从没有过为 nil。
	Replay *ReplayEvidence
}

// StageObservedSince 影子观察期的起点：进入 shadow、分组配置最近一次变化、所属渠道最近一次保存，取最晚的。
func StageObservedSince(cfg StageGateConfig, ownerChannelUpdatedAt *time.Time) time.Time {
	since := cfg.UpdatedAt
	if cfg.StageChangedAt != nil && cfg.StageChangedAt.After(since) {
		since = *cfg.StageChangedAt
	}
	if ownerChannelUpdatedAt != nil && ownerChannelUpdatedAt.After(since) {
		since = *ownerChannelUpdatedAt
	}
	return since
}

// StageGateInput 评估闸门的输入。
type StageGateInput struct {
	Facts *StageGateFacts
	Now   time.Time
	// CurrentDeriveRevision 与 CurrentChannelConfigHash 是「现在」按渠道当前配置派生的 revision 与渠道配置摘要。
	CurrentDeriveRevision    string
	CurrentChannelConfigHash string
	// EvidenceErr 取不到「现在」的 revision 或摘要时的错误：闸门不放行。
	EvidenceErr error
}

// StageGateObservation 影子观察期。
type StageGateObservation struct {
	Since         time.Time `json:"since"`
	ObservedHours float64   `json:"observed_hours"`
	RequiredHours float64   `json:"required_hours"`
	// EligibleAt 观察期满的时刻（已满足时早于现在）。
	EligibleAt time.Time `json:"eligible_at"`
	Satisfied  bool      `json:"satisfied"`
}

// StageGateReplay 回放证据的评估结果。
type StageGateReplay struct {
	Present          bool       `json:"present"`
	ID               int64      `json:"id,omitempty"`
	RecordedAt       *time.Time `json:"recorded_at,omitempty"`
	WindowFrom       *time.Time `json:"window_from,omitempty"`
	WindowTo         *time.Time `json:"window_to,omitempty"`
	RowsReplayed     int64      `json:"rows_replayed"`
	TranslationDiffs int64      `json:"translation_diffs"`
	ExpectedDiffs    int64      `json:"expected_diffs"`
	RowsErrored      int64      `json:"rows_errored"`
	Passed           bool       `json:"passed"`
	// BindingCurrent 回放绑定的派生 revision 与渠道配置摘要都等于现在的值。
	BindingCurrent         bool   `json:"binding_current"`
	DeriveRevision         string `json:"derive_revision,omitempty"`
	CurrentDeriveRevision  string `json:"current_derive_revision,omitempty"`
	ChannelConfigHashMatch bool   `json:"channel_config_hash_match"`
}

// StageGateReport 闸门的评估结果，原样给界面展示，也是审计里的闸门证据。
type StageGateReport struct {
	Required    bool                 `json:"required"`
	Passed      bool                 `json:"passed"`
	Failures    []StageGateFailure   `json:"failures"`
	Observation StageGateObservation `json:"observation"`
	Shadow      ShadowEvidence       `json:"shadow"`
	Replay      StageGateReplay      `json:"replay"`
}

// EvaluateStageGate 评估 shadow 到 v2 的闸门。所有不满足的原因都列出来，不在第一条就停。
func EvaluateStageGate(in StageGateInput) StageGateReport {
	report := StageGateReport{Required: true, Failures: []StageGateFailure{}}
	facts := in.Facts
	if facts == nil {
		report.Failures = append(report.Failures, StageGateFailure{
			Code: ReasonPricingGateDeriveFailed, Message: "the gate evidence could not be loaded"})
		return report
	}
	fail := func(code, msg string) {
		report.Failures = append(report.Failures, StageGateFailure{Code: code, Message: msg})
	}

	report.Shadow = facts.Shadow
	if report.Shadow.ExpectedModels == nil {
		report.Shadow.ExpectedModels = []string{}
	}
	since := StageObservedSince(facts.Config, facts.OwnerChannelUpdatedAt)
	observed := in.Now.Sub(since)
	if observed < 0 {
		observed = 0
	}
	report.Observation = StageGateObservation{
		Since:         since,
		ObservedHours: observed.Hours(),
		RequiredHours: PricingGateObservation.Hours(),
		EligibleAt:    since.Add(PricingGateObservation),
		Satisfied:     observed >= PricingGateObservation,
	}

	if facts.Config.Stage != PricingStageShadow {
		fail(ReasonPricingGateNotInShadow, "the group must be in the shadow stage before it can move to v2")
	}
	if !report.Observation.Satisfied {
		fail(ReasonPricingGateObservation, fmt.Sprintf("shadow observation is %.1f hours, %.0f required (the clock restarts when the group or its channel changes)",
			report.Observation.ObservedHours, report.Observation.RequiredHours))
	}
	if facts.Shadow.TranslationDiffs > 0 {
		fail(ReasonPricingGateShadowDiffs, fmt.Sprintf("%d translation differences in the shadow comparison", facts.Shadow.TranslationDiffs))
	}

	report.Replay = evaluateStageReplay(facts.Replay, in, fail)
	report.Passed = len(report.Failures) == 0
	return report
}

func evaluateStageReplay(ev *ReplayEvidence, in StageGateInput, fail func(code, msg string)) StageGateReplay {
	out := StageGateReplay{CurrentDeriveRevision: in.CurrentDeriveRevision}
	if in.EvidenceErr != nil || in.CurrentDeriveRevision == "" || in.CurrentChannelConfigHash == "" {
		fail(ReasonPricingGateDeriveFailed, "the current derive revision or channel configuration digest could not be computed")
	}
	if ev == nil {
		fail(ReasonPricingGateReplayMissing, "no 30-day replay has been recorded for this group (run pricing-replay --record)")
		return out
	}
	recorded, from, to := ev.RecordedAt, ev.WindowFrom, ev.WindowTo
	out.Present = true
	out.ID = ev.ID
	out.RecordedAt, out.WindowFrom, out.WindowTo = &recorded, &from, &to
	out.RowsReplayed, out.TranslationDiffs, out.ExpectedDiffs, out.RowsErrored = ev.RowsReplayed, ev.TranslationDiffs, ev.ExpectedDiffs, ev.RowsErrored
	out.Passed = ev.Passed
	out.DeriveRevision = ev.DeriveRevision
	out.ChannelConfigHashMatch = in.CurrentChannelConfigHash != "" && ev.ChannelConfigHash == in.CurrentChannelConfigHash
	out.BindingCurrent = out.ChannelConfigHashMatch && in.CurrentDeriveRevision != "" && ev.DeriveRevision == in.CurrentDeriveRevision

	if !ev.Passed || !ev.BindingStable || ev.TranslationDiffs > 0 || ev.RowsErrored > 0 {
		fail(ReasonPricingGateReplayFailed, fmt.Sprintf("the latest replay did not pass (translation differences %d, errors %d, binding stable %t)",
			ev.TranslationDiffs, ev.RowsErrored, ev.BindingStable))
	}
	if ev.MatrixSource != "derived" {
		fail(ReasonPricingGateReplayFailed, "the replay must run against the derived matrix (matrix source derived)")
	}
	if ev.WindowTo.Sub(ev.WindowFrom) < PricingGateReplayWindow {
		fail(ReasonPricingGateReplayShort, "the replay window is shorter than 30 days")
	}
	if in.Now.Sub(ev.WindowTo) > PricingGateReplayMaxAge {
		fail(ReasonPricingGateReplayTooOld, "the replay window ended more than 30 days ago, run it again")
	}
	if !out.BindingCurrent && in.EvidenceErr == nil && in.CurrentDeriveRevision != "" && in.CurrentChannelConfigHash != "" {
		fail(ReasonPricingGateReplayStale, "the channel configuration or the derived matrix changed after the replay, run it again")
	}
	return out
}

// StageGateError 把不满足的闸门变成 409。reason 是第一条不满足的原因，metadata.failures 带全部原因。
func StageGateError(groupID int64, report StageGateReport) error {
	if len(report.Failures) == 0 {
		return nil
	}
	codes := make([]string, 0, len(report.Failures))
	for _, f := range report.Failures {
		codes = append(codes, f.Code)
	}
	first := report.Failures[0]
	return infraerrors.Conflict(first.Code, first.Message).WithMetadata(map[string]string{
		"group_id": strconv.FormatInt(groupID, 10),
		"failures": strings.Join(codes, ";"),
	})
}

// AcceptedDifference 切换时被接受的差异：切到 v2 的那一刻起行为会变的地方。
type AcceptedDifference struct {
	// Source：replay（回放里的预期差异）、shadow（影子样本里近 7 天的预期差异）、catalog（目录 draft 与 retired 且近 7 天有流量）。
	Source     string     `json:"source"`
	Kind       string     `json:"kind,omitempty"`
	Reason     string     `json:"reason"`
	Model      string     `json:"model,omitempty"`
	Count      int64      `json:"count"`
	PriceDelta PriceDelta `json:"price_delta"`
}

// 切换预览里目录与影子来源的差异原因。
const (
	AcceptedReasonShadowExpected = "shadow_expected_difference"
	AcceptedReasonCatalogDraft   = "catalog_draft"
	AcceptedReasonCatalogRetired = "catalog_retired"
)

// replayReasonDelta 回放里预期差异的价格方向。证明不了的一律 unknown：
//   - unpriced_zero_equivalent：legacy 与 v2 最终记的费用逐位相同，没有价格变化；
//   - untrimmed_model_name：legacy 当成另一个名字（零费用），v2 去空白后按价收费，用户实付只会变多；
//   - 其余（含 closed_in_group：v2 新增的例外关闭，请求会被挡）算 unknown。
func replayReasonDelta(reason string) PriceDelta {
	switch reason {
	case PricingReplayReasonUnpricedZero:
		return PriceDeltaNone
	case PricingReplayReasonUntrimmedModel:
		return PriceDeltaUp
	default:
		return PriceDeltaUnknown
	}
}

// StageSwitchAccepted 汇总切到 v2 时被接受的差异，并给出价格方向（price_delta）。
// 方向为 none 必须有证据：回放存在、翻译差异为 0、影子翻译差异为 0、没有任何被接受的差异（或它们都被证明不改价）。
// 没有回放、或翻译差异不为 0，方向是 unknown。catalog 是目录里 draft、retired 且近 7 天有流量的模型，由调用方查出。
func StageSwitchAccepted(replay *ReplayEvidence, shadow ShadowEvidence, catalog []AcceptedDifference) (PriceDelta, []AcceptedDifference) {
	accepted := []AcceptedDifference{}
	deltas := []PriceDelta{}
	if replay == nil || replay.TranslationDiffs > 0 || shadow.TranslationDiffs > 0 {
		deltas = append(deltas, PriceDeltaUnknown)
	}
	if replay != nil {
		for _, d := range replay.Diffs {
			if d.Class == ShadowClassTranslation || d.Count <= 0 {
				continue
			}
			delta := replayReasonDelta(d.Reason)
			accepted = append(accepted, AcceptedDifference{Source: "replay", Kind: d.Kind, Reason: d.Reason, Count: d.Count, PriceDelta: delta})
			deltas = append(deltas, delta)
		}
	}
	for _, m := range shadow.ExpectedModels {
		accepted = append(accepted, AcceptedDifference{Source: "shadow", Reason: AcceptedReasonShadowExpected, Model: m, PriceDelta: PriceDeltaUnknown})
		deltas = append(deltas, PriceDeltaUnknown)
	}
	for _, c := range catalog {
		accepted = append(accepted, c)
		deltas = append(deltas, c.PriceDelta)
	}
	sort.SliceStable(accepted, func(i, j int) bool {
		a, b := accepted[i], accepted[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Model < b.Model
	})
	return CombinePriceDeltas(deltas...), accepted
}

// StageSwitchPlan 一次切换的计划指纹：预览与提交算出同一个值才放行。
// 它把「分组配置版本、派生 revision、渠道配置摘要、所依据的回放记录」都绑进来，
// 预览之后这四者里任何一个变了，提交都会被拒绝，必须重新预览。
type StageSwitchPlan struct {
	GroupID           int64        `json:"group_id"`
	From              PricingStage `json:"from"`
	To                PricingStage `json:"to"`
	ConfigRevision    int64        `json:"config_revision"`
	DeriveRevision    string       `json:"derive_revision"`
	ChannelConfigHash string       `json:"channel_config_hash"`
	ReplayID          int64        `json:"replay_id"`
}

// StageSwitchPlanHash 计划指纹（SHA-256 的十六进制，64 位，等于审批表 plan_hash 的宽度）。
func StageSwitchPlanHash(p StageSwitchPlan) string {
	sum := sha256.Sum256([]byte(matrixCanonicalJSON(p)))
	return hex.EncodeToString(sum[:])
}

// stageRank 阶段的先后：legacy 在前，v2 在后。
func stageRank(s PricingStage) int {
	switch s {
	case PricingStageShadow:
		return 1
	case PricingStageV2:
		return 2
	default:
		return 0
	}
}
