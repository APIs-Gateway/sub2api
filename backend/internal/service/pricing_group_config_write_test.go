//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
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
	res      *GroupConfigWriteResult
	err      error
	reqs     []GroupConfigWriteRequest
	planRes  *GroupConfigWriteResult
	planErr  error
	planReqs []GroupConfigWriteRequest
}

func (w *gcFakeWriter) ApplyTx(_ context.Context, _ MatrixTx, req GroupConfigWriteRequest) (*GroupConfigWriteResult, error) {
	w.reqs = append(w.reqs, req)
	return w.res, w.err
}

func (w *gcFakeWriter) PlanTx(_ context.Context, _ MatrixExecutor, req GroupConfigWriteRequest) (*GroupConfigWriteResult, error) {
	w.planReqs = append(w.planReqs, req)
	return w.planRes, w.planErr
}

func gcResult(changed, exposure bool, mode MatrixAccessMode) *GroupConfigWriteResult {
	return &GroupConfigWriteResult{
		Changed: changed, ExposureRelevant: exposure,
		After: StoredGroupConfig{GroupID: 1, MatrixGroupConfig: MatrixGroupConfig{AccessMode: mode}},
	}
}

// gcPriceResult 一次涉价的改动：计费来源从空（无渠道）改成 requested。
func gcPriceResult() *GroupConfigWriteResult {
	return &GroupConfigWriteResult{
		Changed: true, ExposureRelevant: true,
		Before: StoredGroupConfig{GroupID: 1, MatrixGroupConfig: MatrixGroupConfig{AccessMode: MatrixAccessOpen}},
		After: StoredGroupConfig{GroupID: 1, MatrixGroupConfig: MatrixGroupConfig{
			AccessMode: MatrixAccessOpen, BillingModelSource: gcStr(BillingModelSourceRequested)}},
	}
}

type gcFixture struct {
	svc    *GroupConfigService
	writer *gcFakeWriter
	reader *exFakeReader
	store  *pwFakeStore
	inv    *pwFakeInvalidator
	est    *pwFakeEstimator
}

func gcNewService(w *gcFakeWriter, r *exFakeReader) gcFixture {
	f := gcFixture{writer: w, reader: r, store: &pwFakeStore{}, inv: &pwFakeInvalidator{}, est: &pwFakeEstimator{}}
	guard := NewExposureGuard(r, NewExposureValidator(exOfficial, nil))
	f.svc = NewGroupConfigService(f.store, NewMatrixTxWriter(nil, w, guard), f.est, f.inv)
	f.svc.now = func() time.Time { return pwNow }
	return f
}

func gcCommit(req GroupConfigWriteRequest, approval int64, interactive bool) GroupConfigCommit {
	return GroupConfigCommit{ApprovalID: approval, Request: req, Confirm: true, Actor: PriceWriteActor{ID: 7, Interactive: interactive}}
}

func TestGroupConfigService_Commit(t *testing.T) {
	ctx := context.Background()
	req := gcBase()
	req.OperatorID = 5
	req.AccessMode = gcAccess(MatrixAccessAllowlist)

	// 白名单且涉准入：写入之后校验；通过就提交并失效缓存。
	r := &exFakeReader{modes: map[int64]MatrixAccessMode{1: MatrixAccessAllowlist}, cells: []ExposureCell{exCell(1, "priced", MatrixPriceInherit)}}
	f := gcNewService(&gcFakeWriter{res: gcResult(true, true, MatrixAccessAllowlist)}, r)
	res, err := f.svc.Commit(ctx, gcCommit(req, 0, false))
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.Equal(t, [][]int64{{1}}, r.cellCalls)
	require.Equal(t, [][]int64{{1}}, f.inv.calls)
	require.Zero(t, f.store.txRollbacks)
	require.Len(t, f.writer.reqs, 1)
	require.Equal(t, int64(7), f.writer.reqs[0].OperatorID, "操作人取自提交者，不取自请求体")

	// 违规：整个事务回滚，不失效缓存。
	r = &exFakeReader{modes: map[int64]MatrixAccessMode{1: MatrixAccessAllowlist}, cells: []ExposureCell{exCell(1, "nothing", MatrixPriceInherit)}}
	f = gcNewService(&gcFakeWriter{res: gcResult(true, true, MatrixAccessAllowlist)}, r)
	_, err = f.svc.Commit(ctx, gcCommit(req, 0, false))
	require.Equal(t, ReasonExposureUnpriced, pwReason(t, err))
	require.Equal(t, 1, f.store.txRollbacks)
	require.Empty(t, f.inv.calls)

	// 不需要校验的情形：没有变化、不涉准入、改成开放分组。
	for _, res := range []*GroupConfigWriteResult{
		gcResult(false, false, MatrixAccessAllowlist),
		gcResult(true, false, MatrixAccessAllowlist),
		gcResult(true, true, MatrixAccessOpen),
	} {
		r = &exFakeReader{modes: map[int64]MatrixAccessMode{1: MatrixAccessAllowlist}, cells: []ExposureCell{exCell(1, "nothing", MatrixPriceInherit)}}
		f = gcNewService(&gcFakeWriter{res: res}, r)
		_, err = f.svc.Commit(ctx, gcCommit(req, 0, false))
		require.NoError(t, err)
		require.Empty(t, r.modeCalls)
		require.Equal(t, res.Changed, len(f.inv.calls) == 1, "没有变化就不失效缓存")
	}

	// 前置条件与写入器错误。
	f = gcNewService(&gcFakeWriter{}, &exFakeReader{})
	in := gcCommit(req, 0, false)
	in.Actor.ID = 0
	_, err = f.svc.Commit(ctx, in)
	require.Equal(t, ReasonPriceWriteActorRequired, pwReason(t, err))
	in = gcCommit(req, 0, false)
	in.Confirm = false
	_, err = f.svc.Commit(ctx, in)
	require.Equal(t, ReasonPriceWriteConfirm, pwReason(t, err))
	_, err = f.svc.Commit(ctx, gcCommit(gcBase(), 0, false))
	require.Equal(t, ReasonGroupConfigEmpty, pwReason(t, err))
	require.Zero(t, f.store.txRuns, "请求不合法时不开事务")

	f = gcNewService(&gcFakeWriter{err: errors.New("db down")}, &exFakeReader{})
	_, err = f.svc.Commit(ctx, gcCommit(req, 0, false))
	require.EqualError(t, err, "db down")
	require.Equal(t, 1, f.store.txRollbacks)
	require.Empty(t, f.inv.calls)

	// 没有配置保存时校验：涉准入的白名单写入失败关闭。
	store, inv := &pwFakeStore{}, &pwFakeInvalidator{}
	svc := NewGroupConfigService(store, NewMatrixTxWriter(nil, &gcFakeWriter{res: gcResult(true, true, MatrixAccessAllowlist)}, nil), nil, inv)
	_, err = svc.Commit(ctx, gcCommit(req, 0, false))
	require.Equal(t, ReasonExposureGuardMissing, pwReason(t, err))
	require.Equal(t, 1, store.txRollbacks)
}

func TestGroupConfigService_CommitPriceTouchingNeedsApproval(t *testing.T) {
	ctx := context.Background()
	req := gcBase()
	req.BillingModelSource = gcStr(BillingModelSourceRequested)
	norm, err := NormalizeGroupConfigWrite(req)
	require.NoError(t, err)
	hash := GroupConfigPlanHash(norm)

	// 涉价但没有凭证：拒绝，事务回滚，缓存不失效。
	f := gcNewService(&gcFakeWriter{res: gcPriceResult()}, &exFakeReader{})
	_, err = f.svc.Commit(ctx, gcCommit(req, 0, true))
	require.Equal(t, ReasonPriceWriteApproval, pwReason(t, err))
	require.Equal(t, 1, f.store.txRollbacks)
	require.Empty(t, f.store.consumeCalls)
	require.Empty(t, f.inv.calls)

	// 带凭证：在写入的同一个事务里消耗，种类是 group_config，指纹与预览一致。
	f = gcNewService(&gcFakeWriter{res: gcPriceResult()}, &exFakeReader{})
	f.store.consumeRes = &PriceWriteApproval{ID: 7, TouchesPrice: true, Delta: PriceDeltaUp}
	_, err = f.svc.Commit(ctx, gcCommit(req, 7, true))
	require.NoError(t, err)
	require.Equal(t, []pwConsumeCall{{id: 7, hash: hash, kind: PriceWriteKindGroupConfig, approver: 7, now: pwNow}}, f.store.consumeCalls)
	require.Equal(t, [][]int64{{1}}, f.inv.calls)

	// 方向不是 none：机器令牌不行；方向是 none：可以。
	for _, tc := range []struct {
		delta       PriceDelta
		interactive bool
		reason      string
	}{
		{PriceDeltaUp, false, ReasonPriceWriteInteractive},
		{PriceDeltaDown, false, ReasonPriceWriteInteractive},
		{PriceDeltaUnknown, false, ReasonPriceWriteInteractive},
		{PriceDeltaUp, true, ""},
		{PriceDeltaNone, false, ""},
	} {
		f = gcNewService(&gcFakeWriter{res: gcPriceResult()}, &exFakeReader{})
		f.store.consumeRes = &PriceWriteApproval{TouchesPrice: true, Delta: tc.delta}
		_, err = f.svc.Commit(ctx, gcCommit(req, 7, tc.interactive))
		if tc.reason == "" {
			require.NoError(t, err, "%s", tc.delta)
			continue
		}
		require.Equal(t, tc.reason, pwReason(t, err), "%s", tc.delta)
		require.Equal(t, 1, f.store.txRollbacks)
		require.Empty(t, f.inv.calls)
	}

	// 预览时不涉价、提交时涉价：要重新预览。
	f = gcNewService(&gcFakeWriter{res: gcPriceResult()}, &exFakeReader{})
	f.store.consumeRes = &PriceWriteApproval{TouchesPrice: false, Delta: PriceDeltaNone}
	_, err = f.svc.Commit(ctx, gcCommit(req, 7, true))
	require.Equal(t, ReasonPriceWritePlanChanged, pwReason(t, err))

	// 凭证无效（过期、已用、种类或指纹不符）：整个事务回滚。
	f = gcNewService(&gcFakeWriter{res: gcPriceResult()}, &exFakeReader{})
	f.store.consumeErr = infraerrors.Conflict(ReasonApprovalMismatch, "mismatch")
	_, err = f.svc.Commit(ctx, gcCommit(req, 7, true))
	require.Equal(t, ReasonApprovalMismatch, pwReason(t, err))
	require.Equal(t, 1, f.store.txRollbacks)

	// 不涉价的写入带着凭证也能提交（凭证照常被消耗）。
	f = gcNewService(&gcFakeWriter{res: gcResult(true, true, MatrixAccessOpen)}, &exFakeReader{})
	f.store.consumeRes = &PriceWriteApproval{TouchesPrice: false, Delta: PriceDeltaNone}
	_, err = f.svc.Commit(ctx, gcCommit(req, 7, false))
	require.NoError(t, err)
	require.Len(t, f.store.consumeCalls, 1)
}

func TestGroupConfigService_Propose(t *testing.T) {
	ctx := context.Background()
	req := gcBase()
	req.BillingModelSource = gcStr(BillingModelSourceRequested)

	// 涉价：估算器给方向，登记审批行，summary 存前后对比与操作人。
	f := gcNewService(&gcFakeWriter{planRes: gcPriceResult()}, &exFakeReader{})
	f.est.delta = PriceDeltaUp
	ticket, err := f.svc.Propose(ctx, req)
	require.NoError(t, err)
	norm, err := NormalizeGroupConfigWrite(req)
	require.NoError(t, err)
	require.Len(t, f.store.inserted, 1)
	a := f.store.inserted[0]
	require.Equal(t, PriceWriteKindGroupConfig, a.Kind)
	require.Equal(t, GroupConfigPlanHash(norm), a.PlanHash)
	require.True(t, a.TouchesPrice)
	require.Equal(t, PriceDeltaUp, a.Delta)
	require.Equal(t, []int64{1}, a.GroupIDs)
	require.Equal(t, int64(7), a.PreviewedBy)
	require.Equal(t, pwNow.Add(PriceWriteApprovalTTL), a.ExpiresAt)
	var summary map[string]any
	require.NoError(t, json.Unmarshal(a.Summary, &summary))
	require.EqualValues(t, 7, summary["operator_id"])
	require.EqualValues(t, 1, summary["group_id"])
	require.Equal(t, "up", summary["price_delta"])
	before, after := summary["before"].(map[string]any), summary["after"].(map[string]any)
	require.Nil(t, before["billing_model_source"])
	require.Equal(t, BillingModelSourceRequested, after["billing_model_source"])
	require.Equal(t, int64(101), ticket.ApprovalID)
	require.True(t, ticket.TouchesPrice)
	require.Equal(t, PriceDeltaUp, ticket.Delta)
	require.Equal(t, a.ExpiresAt, ticket.ExpiresAt)
	require.Len(t, f.est.groupCalls, 1)
	require.Equal(t, int64(1), f.est.groupCalls[0].groupID)
	require.Nil(t, f.est.groupCalls[0].before.BillingModelSource)
	require.Equal(t, BillingModelSourceRequested, *f.est.groupCalls[0].after.BillingModelSource)
	require.Equal(t, []time.Time{pwNow.Add(-priceWriteStaleAfter)}, f.store.purgedBefore)

	// 估算器出错或返回不认识的值：unknown。
	f = gcNewService(&gcFakeWriter{planRes: gcPriceResult()}, &exFakeReader{})
	f.est.err = errors.New("quote failed")
	ticket, err = f.svc.Propose(ctx, req)
	require.NoError(t, err)
	require.Equal(t, PriceDeltaUnknown, ticket.Delta)
	f = gcNewService(&gcFakeWriter{planRes: gcPriceResult()}, &exFakeReader{})
	f.est.delta = "sideways"
	ticket, err = f.svc.Propose(ctx, req)
	require.NoError(t, err)
	require.Equal(t, PriceDeltaUnknown, ticket.Delta)

	// 没有估算器：unknown。
	f = gcNewService(&gcFakeWriter{planRes: gcPriceResult()}, &exFakeReader{})
	f.svc.estimator = nil
	ticket, err = f.svc.Propose(ctx, req)
	require.NoError(t, err)
	require.Equal(t, PriceDeltaUnknown, ticket.Delta)

	// 不涉价（只改准入）、没有变化：不登记审批行，也不用估算器。
	for _, res := range []*GroupConfigWriteResult{gcResult(true, true, MatrixAccessOpen), gcResult(false, false, MatrixAccessOpen)} {
		f = gcNewService(&gcFakeWriter{planRes: res}, &exFakeReader{})
		ticket, err = f.svc.Propose(ctx, req)
		require.NoError(t, err)
		require.Zero(t, ticket.ApprovalID)
		require.False(t, ticket.TouchesPrice)
		require.Equal(t, PriceDeltaNone, ticket.Delta)
		require.Equal(t, res.Changed, ticket.Changed)
		require.Empty(t, f.store.inserted)
		require.Empty(t, f.est.groupCalls)
	}

	// 改成白名单：预览就按「假如已经是白名单」校验现有的 open 单元格。
	r := &exFakeReader{cells: []ExposureCell{exCell(1, "nothing", MatrixPriceInherit)}}
	f = gcNewService(&gcFakeWriter{planRes: gcResult(true, true, MatrixAccessAllowlist)}, r)
	_, err = f.svc.Propose(ctx, req)
	require.Equal(t, ReasonExposureUnpriced, pwReason(t, err))
	require.Equal(t, [][]int64{{1}}, r.cellCalls)
	require.Empty(t, f.store.inserted)

	// 前置条件与错误。
	f = gcNewService(&gcFakeWriter{planRes: gcPriceResult()}, &exFakeReader{})
	noOperator := req
	noOperator.OperatorID = 0
	_, err = f.svc.Propose(ctx, noOperator)
	require.Equal(t, ReasonPriceWriteActorRequired, pwReason(t, err))
	_, err = f.svc.Propose(ctx, gcBase())
	require.Equal(t, ReasonGroupConfigEmpty, pwReason(t, err))
	require.Empty(t, f.writer.planReqs, "请求不合法时不读库")

	f = gcNewService(&gcFakeWriter{planErr: infraerrors.Conflict(ReasonPriceBaselineChanged, "stale")}, &exFakeReader{})
	_, err = f.svc.Propose(ctx, req)
	require.Equal(t, ReasonPriceBaselineChanged, pwReason(t, err))

	f = gcNewService(&gcFakeWriter{planRes: gcPriceResult()}, &exFakeReader{})
	f.store.insertErr = errors.New("db down")
	_, err = f.svc.Propose(ctx, req)
	require.EqualError(t, err, "db down")

	f = gcNewService(&gcFakeWriter{planRes: gcPriceResult()}, &exFakeReader{})
	f.store.purgeErr = errors.New("purge failed")
	_, err = f.svc.Propose(ctx, req)
	require.NoError(t, err, "清理过期预览失败不影响预览本身")
}

func TestGroupConfigTouchesPriceAndPlanHash(t *testing.T) {
	bms := func(v string) *string { return &v }
	base := MatrixGroupConfig{AccessMode: MatrixAccessOpen, ModelMapping: []MatrixMappingEntry{{Src: "a", Dst: "b"}}}

	same := base
	require.False(t, GroupConfigTouchesPrice(base, same))
	access := base
	access.AccessMode = MatrixAccessAllowlist
	require.False(t, GroupConfigTouchesPrice(base, access), "改准入不涉价")
	cost := base
	cost.CostMode = MatrixCostFollowBilling
	cost.Features = map[string]any{"bedrock_cc_compat": true}
	require.False(t, GroupConfigTouchesPrice(base, cost), "改成本模式与功能开关不涉价")
	source := base
	source.BillingModelSource = bms("upstream")
	require.True(t, GroupConfigTouchesPrice(base, source))
	require.True(t, GroupConfigTouchesPrice(source, base))
	mapping := base
	mapping.ModelMapping = []MatrixMappingEntry{{Src: "a", Dst: "c"}}
	require.True(t, GroupConfigTouchesPrice(base, mapping))

	// 指纹绑定基线与目标态，不含操作人。
	req := gcBase()
	req.AccessMode = gcAccess(MatrixAccessAllowlist)
	n1, err := NormalizeGroupConfigWrite(req)
	require.NoError(t, err)
	other := req
	other.OperatorID = 99
	n2, err := NormalizeGroupConfigWrite(other)
	require.NoError(t, err)
	require.Equal(t, GroupConfigPlanHash(n1), GroupConfigPlanHash(n2))
	moved := req
	moved.BaselineRevision = 4
	n3, err := NormalizeGroupConfigWrite(moved)
	require.NoError(t, err)
	require.NotEqual(t, GroupConfigPlanHash(n1), GroupConfigPlanHash(n3))
	changed := req
	changed.AccessMode = gcAccess(MatrixAccessOpen)
	n4, err := NormalizeGroupConfigWrite(changed)
	require.NoError(t, err)
	require.NotEqual(t, GroupConfigPlanHash(n1), GroupConfigPlanHash(n4))
}
