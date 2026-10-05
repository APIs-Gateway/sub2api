//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// W6 PR4b-2a：分组配置写入（规范化、套用补丁、服务层）。

func gcAccess(m MatrixAccessMode) *MatrixAccessMode { return &m }
func gcCost(m MatrixCostMode) *MatrixCostMode       { return &m }
func gcStr(s string) *string                        { return &s }

func gcBase() GroupConfigWriteRequest {
	return GroupConfigWriteRequest{GroupID: 1, BaselineRevision: 3, OperatorID: 7}
}

func TestNormalizeGroupConfigWrite_Rejects(t *testing.T) {
	long := strings.Repeat("a", matrixModelKeyMaxLen+1)
	manyEntries := make([]MatrixMappingEntry, MaxGroupMappingEntries+1)
	for i := range manyEntries {
		manyEntries[i] = MatrixMappingEntry{Src: strings.Repeat("a", 3) + string(rune('A'+i%26)) + strings.Repeat("b", i/26+1), Dst: "x"}
	}
	mapping := func(src, dst string) *[]MatrixMappingEntry { return &[]MatrixMappingEntry{{Src: src, Dst: dst}} }
	feat := func(m map[string]any) *map[string]any { return &m }

	cases := []struct {
		name   string
		mutate func(*GroupConfigWriteRequest)
		reason string
	}{
		{"group id", func(r *GroupConfigWriteRequest) { r.GroupID = 0; r.AccessMode = gcAccess(MatrixAccessOpen) }, ReasonGroupConfigInvalid},
		{"baseline", func(r *GroupConfigWriteRequest) { r.BaselineRevision = 0; r.AccessMode = gcAccess(MatrixAccessOpen) }, ReasonCellGroupBaselineMissing},
		{"empty", func(r *GroupConfigWriteRequest) {}, ReasonGroupConfigEmpty},
		{"access mode", func(r *GroupConfigWriteRequest) { r.AccessMode = gcAccess("closed") }, ReasonGroupConfigInvalid},
		{"cost mode", func(r *GroupConfigWriteRequest) { r.CostMode = gcCost("free") }, ReasonGroupConfigInvalid},
		{"billing source", func(r *GroupConfigWriteRequest) { r.BillingModelSource = gcStr("mapped") }, ReasonGroupConfigInvalid},
		{"billing source and clear", func(r *GroupConfigWriteRequest) {
			r.BillingModelSource = gcStr("upstream")
			r.ClearBillingModelSource = true
		}, ReasonGroupConfigInvalid},
		{"mapping empty source", func(r *GroupConfigWriteRequest) { r.ModelMapping = mapping("  ", "x") }, ReasonGroupConfigInvalid},
		{"mapping bare star", func(r *GroupConfigWriteRequest) { r.ModelMapping = mapping("*", "x") }, ReasonGroupConfigInvalid},
		{"mapping star in the middle", func(r *GroupConfigWriteRequest) { r.ModelMapping = mapping("a*b", "x") }, ReasonGroupConfigInvalid},
		{"mapping star in the target", func(r *GroupConfigWriteRequest) { r.ModelMapping = mapping("a", "x*") }, ReasonGroupConfigInvalid},
		{"mapping long name", func(r *GroupConfigWriteRequest) { r.ModelMapping = mapping(long, "x") }, ReasonGroupConfigInvalid},
		{"mapping duplicate", func(r *GroupConfigWriteRequest) {
			r.ModelMapping = &[]MatrixMappingEntry{{Src: "A", Dst: "x"}, {Src: " a ", Dst: "y"}}
		}, ReasonGroupConfigInvalid},
		{"mapping too many", func(r *GroupConfigWriteRequest) { r.ModelMapping = &manyEntries }, ReasonGroupConfigInvalid},
		{"features unknown key", func(r *GroupConfigWriteRequest) { r.Features = feat(map[string]any{"teleport": true}) }, ReasonGroupConfigInvalid},
		{"features web search shape", func(r *GroupConfigWriteRequest) { r.Features = feat(map[string]any{"web_search_emulation": true}) }, ReasonGroupConfigInvalid},
		{"features web search value", func(r *GroupConfigWriteRequest) {
			r.Features = feat(map[string]any{"web_search_emulation": map[string]any{"anthropic": "yes"}})
		}, ReasonGroupConfigInvalid},
		{"features web search platform", func(r *GroupConfigWriteRequest) {
			r.Features = feat(map[string]any{"web_search_emulation": map[string]any{" ": true}})
		}, ReasonGroupConfigInvalid},
		{"features bedrock shape", func(r *GroupConfigWriteRequest) { r.Features = feat(map[string]any{"bedrock_cc_compat": "on"}) }, ReasonGroupConfigInvalid},
		{"features bridge shape", func(r *GroupConfigWriteRequest) {
			r.Features = feat(map[string]any{"codex_image_generation_bridge": map[string]any{"openai": true}})
		}, ReasonGroupConfigInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := gcBase()
			tc.mutate(&req)
			_, err := NormalizeGroupConfigWrite(req)
			require.Equal(t, tc.reason, pwReason(t, err))
		})
	}
}

func TestNormalizeGroupConfigWrite_Normalizes(t *testing.T) {
	req := gcBase()
	req.ModelMapping = &[]MatrixMappingEntry{{Src: " b-* ", Dst: " x "}, {Src: "B-long", Dst: "y"}, {Src: "a", Dst: ""}}
	req.Features = &map[string]any{
		"codex_image_generation_bridge": nil, // null 视为没设
		"bedrock_cc_compat":             false,
		"web_search_emulation":          map[string]any{"anthropic": true},
	}
	req.BillingModelSource = gcStr("upstream")
	norm, err := NormalizeGroupConfigWrite(req)
	require.NoError(t, err)
	require.Equal(t, []MatrixMappingEntry{{Src: "a", Dst: ""}, {Src: "B-long", Dst: "y"}, {Src: "b-*", Dst: "x"}}, *norm.ModelMapping)
	require.Equal(t, map[string]any{"bedrock_cc_compat": false, "web_search_emulation": map[string]any{"anthropic": true}}, *norm.Features)
	require.Len(t, *req.ModelMapping, 3, "不改入参")

	// 空映射、空功能开关是合法的目标态（清空）。
	req = gcBase()
	req.ModelMapping = &[]MatrixMappingEntry{}
	req.Features = &map[string]any{}
	norm, err = NormalizeGroupConfigWrite(req)
	require.NoError(t, err)
	require.Empty(t, *norm.ModelMapping)
	require.NotNil(t, *norm.ModelMapping)
	require.Empty(t, *norm.Features)
}

func TestApplyGroupConfigPatchAndChange(t *testing.T) {
	bms := "requested"
	cur := MatrixGroupConfig{
		AccessMode: MatrixAccessOpen, BillingModelSource: &bms, CostMode: MatrixCostAccountRate,
		ModelMapping: []MatrixMappingEntry{{Src: "a", Dst: "b"}}, Features: map[string]any{"bedrock_cc_compat": true},
	}

	// 只套带了的字段。
	req := gcBase()
	req.AccessMode = gcAccess(MatrixAccessAllowlist)
	got := ApplyGroupConfigPatch(cur, req)
	require.Equal(t, MatrixAccessAllowlist, got.AccessMode)
	require.Equal(t, cur.ModelMapping, got.ModelMapping)
	require.Equal(t, cur.Features, got.Features)
	require.Equal(t, MatrixAccessOpen, cur.AccessMode, "不改入参")
	changed, exposure := GroupConfigChange(cur, got)
	require.True(t, changed)
	require.True(t, exposure)

	// 清空计费来源、替换映射与功能开关。
	req = gcBase()
	req.ClearBillingModelSource = true
	req.ModelMapping = &[]MatrixMappingEntry{}
	req.Features = &map[string]any{}
	got = ApplyGroupConfigPatch(cur, req)
	require.Nil(t, got.BillingModelSource)
	require.Empty(t, got.ModelMapping)
	require.Empty(t, got.Features)
	require.NotNil(t, cur.BillingModelSource)

	// 设置计费来源时复制值，不与入参共享指针。
	req = gcBase()
	v := "upstream"
	req.BillingModelSource = &v
	got = ApplyGroupConfigPatch(cur, req)
	v = "changed-later"
	require.Equal(t, "upstream", *got.BillingModelSource)

	// 只改成本模式、功能开关：有变化，但不涉准入。
	req = gcBase()
	req.CostMode = gcCost(MatrixCostFollowBilling)
	req.Features = &map[string]any{"bedrock_cc_compat": false}
	changed, exposure = GroupConfigChange(cur, ApplyGroupConfigPatch(cur, req))
	require.True(t, changed)
	require.False(t, exposure)

	// 内容相同：没有变化（映射顺序规范后相同的写法也算相同）。
	req = gcBase()
	req.AccessMode = gcAccess(MatrixAccessOpen)
	req.ModelMapping = &[]MatrixMappingEntry{{Src: "a", Dst: "b"}}
	changed, exposure = GroupConfigChange(cur, ApplyGroupConfigPatch(cur, req))
	require.False(t, changed)
	require.False(t, exposure)
}

type gcFakeWriter struct {
	res  *GroupConfigWriteResult
	err  error
	reqs []GroupConfigWriteRequest
}

func (w *gcFakeWriter) ApplyTx(_ context.Context, _ MatrixTx, req GroupConfigWriteRequest) (*GroupConfigWriteResult, error) {
	w.reqs = append(w.reqs, req)
	return w.res, w.err
}

func gcResult(changed, exposure bool, mode MatrixAccessMode) *GroupConfigWriteResult {
	return &GroupConfigWriteResult{
		Changed: changed, ExposureRelevant: exposure,
		After: StoredGroupConfig{GroupID: 1, MatrixGroupConfig: MatrixGroupConfig{AccessMode: mode}},
	}
}

func TestGroupConfigService_Apply(t *testing.T) {
	ctx := context.Background()
	newSvc := func(w *gcFakeWriter, r *exFakeReader) (*GroupConfigService, *pwFakeStore, *pwFakeInvalidator) {
		store, inv := &pwFakeStore{}, &pwFakeInvalidator{}
		return NewGroupConfigService(store, w, NewExposureGuard(r, NewExposureValidator(exOfficial, nil)), inv), store, inv
	}
	req := gcBase()
	req.AccessMode = gcAccess(MatrixAccessAllowlist)

	// 白名单且涉准入：写入之后校验；通过就提交并失效缓存。
	w := &gcFakeWriter{res: gcResult(true, true, MatrixAccessAllowlist)}
	r := &exFakeReader{modes: map[int64]MatrixAccessMode{1: MatrixAccessAllowlist}, cells: []ExposureCell{exCell(1, "priced", MatrixPriceInherit)}}
	svc, store, inv := newSvc(w, r)
	res, err := svc.Apply(ctx, req)
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.Equal(t, [][]int64{{1}}, r.cellCalls)
	require.Equal(t, [][]int64{{1}}, inv.calls)
	require.Zero(t, store.txRollbacks)
	require.Len(t, w.reqs, 1)

	// 违规：整个事务回滚，不失效缓存。
	r = &exFakeReader{modes: map[int64]MatrixAccessMode{1: MatrixAccessAllowlist}, cells: []ExposureCell{exCell(1, "nothing", MatrixPriceInherit)}}
	svc, store, inv = newSvc(&gcFakeWriter{res: gcResult(true, true, MatrixAccessAllowlist)}, r)
	_, err = svc.Apply(ctx, req)
	require.Equal(t, ReasonExposureUnpriced, pwReason(t, err))
	require.Equal(t, 1, store.txRollbacks)
	require.Empty(t, inv.calls)

	// 不需要校验的情形：没有变化、不涉准入、改成开放分组。
	for _, res := range []*GroupConfigWriteResult{
		gcResult(false, false, MatrixAccessAllowlist),
		gcResult(true, false, MatrixAccessAllowlist),
		gcResult(true, true, MatrixAccessOpen),
	} {
		r = &exFakeReader{modes: map[int64]MatrixAccessMode{1: MatrixAccessAllowlist}, cells: []ExposureCell{exCell(1, "nothing", MatrixPriceInherit)}}
		svc, _, inv = newSvc(&gcFakeWriter{res: res}, r)
		_, err = svc.Apply(ctx, req)
		require.NoError(t, err)
		require.Empty(t, r.modeCalls)
		require.Equal(t, res.Changed, len(inv.calls) == 1, "没有变化就不失效缓存")
	}

	// 前置条件与写入器错误。
	svc, store, _ = newSvc(&gcFakeWriter{}, &exFakeReader{})
	noOperator := req
	noOperator.OperatorID = 0
	_, err = svc.Apply(ctx, noOperator)
	require.Equal(t, ReasonPriceWriteActorRequired, pwReason(t, err))
	_, err = svc.Apply(ctx, gcBase())
	require.Equal(t, ReasonGroupConfigEmpty, pwReason(t, err))
	require.Zero(t, store.txRuns, "请求不合法时不开事务")

	svc, store, inv = newSvc(&gcFakeWriter{err: errors.New("db down")}, &exFakeReader{})
	_, err = svc.Apply(ctx, req)
	require.EqualError(t, err, "db down")
	require.Equal(t, 1, store.txRollbacks)
	require.Empty(t, inv.calls)

	// 没有配置保存时校验：涉准入的白名单写入失败关闭。
	store, inv = &pwFakeStore{}, &pwFakeInvalidator{}
	svc = NewGroupConfigService(store, &gcFakeWriter{res: gcResult(true, true, MatrixAccessAllowlist)}, nil, inv)
	_, err = svc.Apply(ctx, req)
	require.Equal(t, ReasonExposureGuardMissing, pwReason(t, err))
	require.Equal(t, 1, store.txRollbacks)
}
