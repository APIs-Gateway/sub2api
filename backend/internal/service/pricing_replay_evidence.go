package service

import (
	"context"
	"strings"
	"time"
)

// W6 PR7b：回放结果落库（pricing_replay_evidence），给阶段切换闸门读取（设计 4.3、4.5，S-8）。
// `pricing-replay --record` 在回放结束后，用一个可写连接把每个分组的结果各记一行；回放本身仍然是只读会话。

// PricingReplayEvidenceStore 回放证据的写口。
type PricingReplayEvidenceStore interface {
	RecordReplayEvidence(ctx context.Context, rows []ReplayEvidence) error
}

// ReplayEvidenceFromSummary 把一次回放的汇总拆成每个分组一行证据。
//
// 一个分组的 Passed 同时要求：翻译差异为 0、没有回放错误、整个回放期间渠道配置绑定稳定、该分组库里矩阵行的摘要
// 在回放开始与结束时一致、取得了派生 revision 且派生没有出错。任何一条不满足就记 false，闸门会要求重跑。
// 没有绑定信息的分组不记证据（没有 revision 的证据没有用处）。
func ReplayEvidenceFromSummary(s *PricingReplaySummary, recordedAt time.Time) []ReplayEvidence {
	if s == nil {
		return nil
	}
	bindings := make(map[int64]PricingReplayGroupBinding, len(s.Binding.Groups))
	for _, b := range s.Binding.Groups {
		bindings[b.GroupID] = b
	}
	out := make([]ReplayEvidence, 0, len(s.Groups))
	for _, g := range s.Groups {
		b, ok := bindings[g.GroupID]
		if !ok {
			continue
		}
		var translation, expected int64
		diffs := make([]PricingReplayDiffCount, 0, len(g.Diffs))
		for _, d := range g.Diffs {
			switch d.Class {
			case ShadowClassTranslation:
				translation += d.Count
			case ShadowClassExpected:
				expected += d.Count
			}
			diffs = append(diffs, d)
		}
		source := strings.TrimSpace(s.Meta["matrix_source"])
		if source != "stored" {
			source = "derived"
		}
		stable := s.Binding.Stable && b.MatrixHashBefore == b.MatrixHashAfter
		passed := translation == 0 && g.Errors == 0 && stable && b.Error == "" && b.DeriveRevision != ""
		out = append(out, ReplayEvidence{
			GroupID:           g.GroupID,
			RecordedAt:        recordedAt,
			WindowFrom:        s.WindowFrom,
			WindowTo:          s.WindowTo,
			MatrixSource:      source,
			Passed:            passed,
			BindingStable:     stable,
			RowsInWindow:      g.RowsInWindow,
			RowsReplayed:      g.RowsReplayed,
			RowsErrored:       g.Errors,
			TranslationDiffs:  translation,
			ExpectedDiffs:     expected,
			ChannelConfigHash: s.Binding.ChannelConfigHashAfter,
			DeriveRevision:    b.DeriveRevision,
			MatrixHash:        b.MatrixHashAfter,
			PricingDataSHA256: s.Meta["pricing_data_sha256"],
			ToolVersion:       s.Meta["version"],
			Diffs:             diffs,
		})
	}
	return out
}
