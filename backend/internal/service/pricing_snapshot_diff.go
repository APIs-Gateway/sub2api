package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// PricingSnapshotDiffEntry 是候选对基线的单个模型差异。
type PricingSnapshotDiffEntry struct {
	ModelKey      string               `json:"model_key"`
	ChangeType    string               `json:"change_type"`
	ChangedFields []string             `json:"changed_fields"`
	Old           *LiteLLMModelPricing `json:"old,omitempty"`
	New           *LiteLLMModelPricing `json:"new,omitempty"`
	Decision      string               `json:"decision"`
}

// changedPricingFields 返回两条价格记录里取值不同的字段（按 JSON 字段名，顺序与结构体定义一致）。
func changedPricingFields(a, b *LiteLLMModelPricing) []string {
	va, vb := reflect.ValueOf(a).Elem(), reflect.ValueOf(b).Elem()
	t := va.Type()
	var out []string
	for i := 0; i < t.NumField(); i++ {
		if reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			continue
		}
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name == "" {
			name = t.Field(i).Name
		}
		out = append(out, name)
	}
	return out
}

// computePricingDiff 计算候选对基线的差异（按模型键排序）并返回两者相同的模型数。
// 新增 = 只在候选里有；移除 = 只在基线里有；变化 = 两边都有但至少一个价格字段不同。
// 所有条目的决定先置为 approve，由调用方按搁置名单改成 hold。
func computePricingDiff(base, candidate map[string]*LiteLLMModelPricing) (entries []PricingSnapshotDiffEntry, unchanged int) {
	for key, old := range base {
		next, ok := candidate[key]
		if !ok {
			entries = append(entries, PricingSnapshotDiffEntry{ModelKey: key, ChangeType: PricingDiffRemoved, ChangedFields: []string{}, Old: old, Decision: PricingDiffDecisionApprove})
			continue
		}
		fields := changedPricingFields(old, next)
		if len(fields) == 0 {
			unchanged++
			continue
		}
		entries = append(entries, PricingSnapshotDiffEntry{ModelKey: key, ChangeType: PricingDiffChanged, ChangedFields: fields, Old: old, New: next, Decision: PricingDiffDecisionApprove})
	}
	for key, next := range candidate {
		if _, ok := base[key]; !ok {
			entries = append(entries, PricingSnapshotDiffEntry{ModelKey: key, ChangeType: PricingDiffAdded, ChangedFields: []string{}, New: next, Decision: PricingDiffDecisionApprove})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ModelKey < entries[j].ModelKey })
	return entries, unchanged
}

// mergePricingPayload 在 LiteLLM JSON 原文层面合成 merged 快照：以基线为底，把 decision = approve 的差异套上去
// （新增与变化取候选里的原始条目，移除则删掉），hold 的模型保持基线里的旧值，其余条目（含没有价格的文档条目）原样保留。
// 输出键有序、紧凑、不转义 HTML，同样的输入永远得到同样的字节，所以哈希可复现。
func mergePricingPayload(basePayload, candidatePayload []byte, entries []PricingSnapshotDiffEntry) ([]byte, error) {
	var base, candidate map[string]json.RawMessage
	if err := json.Unmarshal(basePayload, &base); err != nil {
		return nil, fmt.Errorf("parse base payload: %w", err)
	}
	if err := json.Unmarshal(candidatePayload, &candidate); err != nil {
		return nil, fmt.Errorf("parse candidate payload: %w", err)
	}
	for _, e := range entries {
		if e.Decision != PricingDiffDecisionApprove {
			continue
		}
		if e.ChangeType == PricingDiffRemoved {
			delete(base, e.ModelKey)
			continue
		}
		raw, ok := candidate[e.ModelKey]
		if !ok {
			return nil, fmt.Errorf("candidate payload has no entry for %q", e.ModelKey)
		}
		base[e.ModelKey] = raw
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(base); err != nil {
		return nil, fmt.Errorf("encode merged payload: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// diffRecords 把差异条目转成 pricing_snapshot_diffs 的行。
func diffRecords(entries []PricingSnapshotDiffEntry) []PricingSnapshotDiffRecord {
	out := make([]PricingSnapshotDiffRecord, 0, len(entries))
	for _, e := range entries {
		rec := PricingSnapshotDiffRecord{ModelKey: e.ModelKey, ChangeType: e.ChangeType, ChangedFields: e.ChangedFields, Decision: e.Decision}
		if e.Old != nil {
			rec.OldPrice, _ = json.Marshal(e.Old)
		}
		if e.New != nil {
			rec.NewPrice, _ = json.Marshal(e.New)
		}
		out = append(out, rec)
	}
	return out
}
