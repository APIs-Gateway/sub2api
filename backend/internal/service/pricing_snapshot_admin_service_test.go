//go:build unit

package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// ---- 夹具 ----

type pssChecker struct {
	err      error
	calls    int
	lastData map[string]*LiteLLMModelPricing
}

func (c *pssChecker) CheckSnapshotApproval(_ context.Context, _ MatrixExecutor, merged map[string]*LiteLLMModelPricing) error {
	c.calls++
	c.lastData = merged
	return c.err
}

type pssExec struct{}

func (pssExec) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("unused")
}
func (pssExec) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("unused")
}

// pssPayloadB 是候选：gpt-5.1 涨价、claude-sonnet-4 被移除、new-model 新增，gemini-2.5-pro 不变。
func pssPayloadB() []byte {
	return []byte(`{
  "sample_spec": {"input_cost_per_token": 0},
  "gpt-5.1": {"input_cost_per_token": 0.000002, "output_cost_per_token": 0.00001, "litellm_provider": "openai", "mode": "chat"},
  "gemini-2.5-pro": {"input_cost_per_token": 0.00000125, "output_cost_per_token": 0.00001, "litellm_provider": "vertex_ai", "mode": "chat"},
  "new-model": {"input_cost_per_token": 0.000004, "output_cost_per_token": 0.00002, "litellm_provider": "openai", "mode": "chat"}
}`)
}

type pssAdminEnv struct {
	svc     *PricingService
	store   *pssStore
	ps      *pssPubSub
	checker *pssChecker
	admin   *PricingSnapshotAdminService
	candID  int64
}

// newPSSAdminEnv 搭一个已经 pinned（生效快照 = pssPayload("0.000001")）、带一份候选 B 的环境。
func newPSSAdminEnv(t *testing.T) *pssAdminEnv {
	t.Helper()
	store := newPSSStore()
	store.setActive(1, pssPayload("0.000001"))
	store.addCandidate(2, pssPayloadB())
	store.recent = []string{"gpt-5.1", "gemini-2.5-pro", "no-such-model-xyz"}
	ps := &pssPubSub{}
	svc := newPSSService(t, t.TempDir())
	svc.ConfigureSnapshots(newPSSSettings("pinned"), store, ps)
	svc.snap.pollInterval = time.Hour
	require.NoError(t, svc.Initialize())
	t.Cleanup(svc.Stop)
	checker := &pssChecker{}
	return &pssAdminEnv{svc: svc, store: store, ps: ps, checker: checker, candID: 2,
		admin: NewPricingSnapshotAdminService(svc, store, checker, nil)}
}

// ---- 预览 ----

func TestPricingSnapshotAdmin_PreviewComputesDiffAndPlanHash(t *testing.T) {
	env := newPSSAdminEnv(t)
	plan, err := env.admin.Preview(context.Background(), env.candID, nil)
	require.NoError(t, err)

	require.Equal(t, SnapshotDiffStats{Added: 1, Removed: 1, Changed: 1, Unchanged: 1, Approved: 3}, plan.Stats)
	require.Equal(t, []string{"claude-sonnet-4", "gpt-5.1", "new-model"}, []string{plan.Entries[0].ModelKey, plan.Entries[1].ModelKey, plan.Entries[2].ModelKey})
	require.Equal(t, int64(1), plan.BaseSnapshot.ID)
	require.Equal(t, int64(2), plan.Candidate.ID)
	require.Equal(t, []string{}, plan.HeldModels)
	require.Len(t, plan.PlanHash, 64)
	require.Equal(t, pssSHA(plan.MergedPayload), plan.MergedSHA256)
	require.Equal(t, 3, plan.MergedModelCount, "gpt-5.1、gemini-2.5-pro、new-model")
	require.Empty(t, plan.ExposureError, "没有只读连接时不做预览校验")

	// 实际变价的名字：gpt-5.1 涨了；没有变化的和没有价格的名字不出现。
	require.Empty(t, plan.EffectiveChangesError)
	var names []string
	for _, c := range plan.EffectiveChanges {
		names = append(names, c.Model)
		if c.Model == "gpt-5.1" {
			require.InDelta(t, 1.0, c.OldInputPerMTok, 1e-9)
			require.InDelta(t, 2.0, c.NewInputPerMTok, 1e-9)
			require.False(t, c.OldMissing)
			require.False(t, c.NewMissing)
		}
	}
	require.Equal(t, []string{"gpt-5.1"}, names)

	// 同样的输入同样的哈希；搁置名单改变哈希。
	again, err := env.admin.Preview(context.Background(), env.candID, nil)
	require.NoError(t, err)
	require.Equal(t, plan.PlanHash, again.PlanHash)
	held, err := env.admin.Preview(context.Background(), env.candID, []string{"gpt-5.1"})
	require.NoError(t, err)
	require.NotEqual(t, plan.PlanHash, held.PlanHash)
	require.Equal(t, SnapshotDiffStats{Added: 1, Removed: 1, Changed: 1, Unchanged: 1, Approved: 2, Held: 1}, held.Stats)
	require.Equal(t, []string{"gpt-5.1"}, held.HeldModels)
	require.Empty(t, held.EffectiveChanges, "搁置之后 gpt-5.1 的价格不变")
	require.Equal(t, 0.000001, held.mergedData["gpt-5.1"].InputCostPerToken)
}

func TestPricingSnapshotAdmin_PreviewFailures(t *testing.T) {
	ctx := context.Background()
	env := newPSSAdminEnv(t)

	_, err := env.admin.Preview(ctx, 999, nil)
	require.ErrorIs(t, err, ErrPricingSnapshotNotFound)
	_, err = env.admin.Preview(ctx, 1, nil) // 1 是生效快照，不是候选
	require.ErrorIs(t, err, ErrPricingSnapshotNotCandidate)
	_, err = env.admin.Preview(ctx, env.candID, []string{"no-such"})
	require.ErrorIs(t, err, ErrPricingSnapshotUnknownHold)

	env.store.getPayloadErr = errors.New("db down")
	_, err = env.admin.Preview(ctx, env.candID, nil)
	require.Error(t, err)
	env.store.getPayloadErr = nil

	env.store.payloads[2] = []byte("tampered")
	_, err = env.admin.Preview(ctx, env.candID, nil)
	require.Error(t, err, "候选 payload 与哈希对不上")
	env.store.addCandidate(3, []byte("not json"))
	_, err = env.admin.Preview(ctx, 3, nil)
	require.Error(t, err, "候选不是合法的价格 JSON")

	env.store.payloads[1] = []byte("tampered")
	_, err = env.admin.Preview(ctx, env.candID, nil)
	require.Error(t, err, "基线 payload 与哈希对不上")

	empty := newPSSStore()
	adminNoActive := NewPricingSnapshotAdminService(env.svc, empty, env.checker, nil)
	_, err = adminNoActive.Preview(ctx, 2, nil)
	require.ErrorIs(t, err, ErrPricingNotPinned)
	empty.getActiveErr = errors.New("db down")
	_, err = adminNoActive.Preview(ctx, 2, nil)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrPricingNotPinned)
}

func TestPricingSnapshotAdmin_PreviewAnnotationsAreBestEffort(t *testing.T) {
	env := newPSSAdminEnv(t)
	env.store.recentErr = errors.New("usage_logs timeout")
	env.admin.readExec = pssExec{}
	env.checker.err = errors.New("group 3: gpt-5.1 would be unpriced")

	plan, err := env.admin.Preview(context.Background(), env.candID, nil)
	require.NoError(t, err, "两项提示失败都不阻止预览")
	require.Contains(t, plan.EffectiveChangesError, "usage_logs timeout")
	require.Empty(t, plan.EffectiveChanges)
	require.Equal(t, "group 3: gpt-5.1 would be unpriced", plan.ExposureError)
	require.Equal(t, 1, env.checker.calls)
	require.Contains(t, env.checker.lastData, "new-model", "校验用的是批准之后的数据")
	require.NotContains(t, env.checker.lastData, "claude-sonnet-4")

	// 基线在预览途中读不出来：同样只记录。
	env.store.recentErr = nil
	env.checker.err = nil
	plan2 := &SnapshotApprovalPlan{BaseSnapshot: PricingSnapshotMeta{ID: 12345}}
	env.admin.annotatePlan(context.Background(), plan2)
	require.NotEmpty(t, plan2.EffectiveChangesError)
}

// ---- 批准 ----

func TestPricingSnapshotAdmin_ApproveSwitchesActiveSnapshotAndPrices(t *testing.T) {
	ctx := context.Background()
	env := newPSSAdminEnv(t)
	require.InDelta(t, 0.000001, pssInputCost(t, env.svc), 1e-12)
	before := env.svc.ActiveSnapshotID()
	require.Equal(t, int64(1), before)

	plan, err := env.admin.Preview(ctx, env.candID, []string{"new-model"})
	require.NoError(t, err)
	meta, err := env.admin.Approve(ctx, env.candID, []string{"new-model"}, plan.PlanHash, 42)
	require.NoError(t, err)
	require.Equal(t, PricingSnapshotSourceMerged, meta.Source)

	require.Len(t, env.store.applied, 1)
	req := env.store.applied[0]
	require.Equal(t, int64(1), req.ExpectedActiveID)
	require.Equal(t, env.store.metas[1].ContentSHA256, req.ExpectedActiveSHA)
	require.False(t, req.ConsumeCandidate, "有搁置的模型时候选留着等下次")
	require.Equal(t, plan.MergedSHA256, req.New.ContentSHA256)
	require.Equal(t, plan.MergedPayload, req.New.Payload)
	require.Equal(t, int64(1), *req.New.ParentSnapshotID)
	require.Equal(t, int64(2), *req.New.CandidateSnapshotID)
	require.Equal(t, int64(42), *req.New.ApprovedBy)
	require.Contains(t, req.New.Note, `"held_models":["new-model"]`)
	require.Contains(t, req.New.Note, plan.PlanHash)
	require.Len(t, req.Diffs, 3)
	decisions := map[string]string{}
	for _, d := range req.Diffs {
		decisions[d.ModelKey] = d.Decision
	}
	require.Equal(t, map[string]string{"claude-sonnet-4": "approve", "gpt-5.1": "approve", "new-model": "hold"}, decisions)
	require.Equal(t, 1, env.checker.calls, "保存时校验在批准事务里运行了一次")

	// 本实例立即切到新快照，并通知了其他实例；搁置的 new-model 没有进入生效数据。
	require.NotEqual(t, before, env.svc.ActiveSnapshotID())
	require.InDelta(t, 0.000002, pssInputCost(t, env.svc), 1e-12)
	require.Equal(t, 1, env.ps.notifications())
	require.NotContains(t, env.checker.lastData, "new-model")
	require.Equal(t, PricingSnapshotStatusCandidate, env.store.metas[2].Status)
}

func TestPricingSnapshotAdmin_ApproveWithoutHoldsConsumesCandidate(t *testing.T) {
	env := newPSSAdminEnv(t)
	plan, err := env.admin.Preview(context.Background(), env.candID, nil)
	require.NoError(t, err)
	_, err = env.admin.Approve(context.Background(), env.candID, nil, plan.PlanHash, 0)
	require.NoError(t, err)
	require.True(t, env.store.applied[0].ConsumeCandidate)
	require.Nil(t, env.store.applied[0].New.ApprovedBy, "没有管理员 id 时不写批准人")
	require.Equal(t, PricingSnapshotStatusSuperseded, env.store.metas[2].Status)
}

func TestPricingSnapshotAdmin_ApproveRefusals(t *testing.T) {
	ctx := context.Background()

	t.Run("plan hash mismatch", func(t *testing.T) {
		env := newPSSAdminEnv(t)
		_, err := env.admin.Approve(ctx, env.candID, nil, "deadbeef", 1)
		require.ErrorIs(t, err, ErrPricingSnapshotPlanMismatch)
		require.Empty(t, env.store.applied)
	})

	t.Run("plan hash from a different hold list", func(t *testing.T) {
		env := newPSSAdminEnv(t)
		plan, err := env.admin.Preview(ctx, env.candID, nil)
		require.NoError(t, err)
		_, err = env.admin.Approve(ctx, env.candID, []string{"gpt-5.1"}, plan.PlanHash, 1)
		require.ErrorIs(t, err, ErrPricingSnapshotPlanMismatch)
	})

	t.Run("everything held", func(t *testing.T) {
		env := newPSSAdminEnv(t)
		all := []string{"claude-sonnet-4", "gpt-5.1", "new-model"}
		plan, err := env.admin.Preview(ctx, env.candID, all)
		require.NoError(t, err)
		_, err = env.admin.Approve(ctx, env.candID, all, plan.PlanHash, 1)
		require.ErrorIs(t, err, ErrPricingSnapshotNothingToApprove)
		require.Empty(t, env.store.applied)
	})

	t.Run("not pinned", func(t *testing.T) {
		env := newPSSAdminEnv(t)
		env.svc.mu.Lock()
		env.svc.snap.pinned = false
		env.svc.mu.Unlock()
		_, err := env.admin.Approve(ctx, env.candID, nil, "x", 1)
		require.ErrorIs(t, err, ErrPricingNotPinned)
	})

	t.Run("candidate gone", func(t *testing.T) {
		env := newPSSAdminEnv(t)
		_, err := env.admin.Approve(ctx, 999, nil, "x", 1)
		require.ErrorIs(t, err, ErrPricingSnapshotNotFound)
	})

	t.Run("save-time check is not configured: fail closed", func(t *testing.T) {
		env := newPSSAdminEnv(t)
		admin := NewPricingSnapshotAdminService(env.svc, env.store, nil, nil)
		plan, err := admin.Preview(ctx, env.candID, nil)
		require.NoError(t, err)
		_, err = admin.Approve(ctx, env.candID, nil, plan.PlanHash, 1)
		require.ErrorIs(t, err, ErrPricingSnapshotExposureUnavailable)
		require.Empty(t, env.store.applied)
		require.InDelta(t, 0.000001, pssInputCost(t, env.svc), 1e-12)
	})

	t.Run("save-time check rejects: nothing changes", func(t *testing.T) {
		env := newPSSAdminEnv(t)
		env.checker.err = errors.New("EXPOSURE_UNPRICED")
		plan, err := env.admin.Preview(ctx, env.candID, nil)
		require.NoError(t, err)
		_, err = env.admin.Approve(ctx, env.candID, nil, plan.PlanHash, 1)
		require.EqualError(t, err, "EXPOSURE_UNPRICED")
		require.Equal(t, int64(1), env.store.active.ID, "批准整体回滚，生效快照不变")
		require.InDelta(t, 0.000001, pssInputCost(t, env.svc), 1e-12)
		require.Zero(t, env.ps.notifications())
	})

	t.Run("baseline changed between preview and approve", func(t *testing.T) {
		env := newPSSAdminEnv(t)
		plan, err := env.admin.Preview(ctx, env.candID, nil)
		require.NoError(t, err)
		env.store.applyErr = ErrPricingSnapshotBaselineChanged
		_, err = env.admin.Approve(ctx, env.candID, nil, plan.PlanHash, 1)
		require.ErrorIs(t, err, ErrPricingSnapshotBaselineChanged)
	})

	t.Run("active snapshot replaced after the preview: plan hash no longer matches", func(t *testing.T) {
		env := newPSSAdminEnv(t)
		plan, err := env.admin.Preview(ctx, env.candID, nil)
		require.NoError(t, err)
		env.store.setActive(7, pssPayload("0.000003"))
		_, err = env.admin.Approve(ctx, env.candID, nil, plan.PlanHash, 1)
		require.ErrorIs(t, err, ErrPricingSnapshotPlanMismatch)
	})
}

func TestPricingSnapshotAdmin_ApproveReloadFailureOnlyWarns(t *testing.T) {
	env := newPSSAdminEnv(t)
	plan, err := env.admin.Preview(context.Background(), env.candID, nil)
	require.NoError(t, err)
	// 批准已在数据库提交，本实例随后的重载读不到 payload：保留旧数据并告警，批准本身成功。
	env.store.checkHook = func() { env.store.getPayloadErr = errors.New("db down") }
	_, err = env.admin.Approve(context.Background(), env.candID, nil, plan.PlanHash, 1)
	require.NoError(t, err)
	require.InDelta(t, 0.000001, pssInputCost(t, env.svc), 1e-12)
	require.Equal(t, 1, env.ps.notifications(), "仍然通知其他实例")
}

// ---- 总览、拒绝、拉取 ----

func TestPricingSnapshotAdmin_OverviewAndReject(t *testing.T) {
	ctx := context.Background()
	env := newPSSAdminEnv(t)
	env.store.addCandidate(3, pssPayload("0.000001")) // 与生效快照内容相同：不算待批准
	out, err := env.admin.Overview(ctx)
	require.NoError(t, err)
	require.Equal(t, PricingSnapshotModePinned, out.Mode)
	require.Equal(t, int64(1), out.Active.ID)
	require.Len(t, out.PendingCandidates, 1)
	require.Equal(t, int64(2), out.PendingCandidates[0].ID)
	require.Len(t, out.History, 3)

	require.NoError(t, env.admin.Reject(ctx, 2))
	require.ErrorIs(t, env.admin.Reject(ctx, 2), ErrPricingSnapshotNotCandidate)
	out, err = env.admin.Overview(ctx)
	require.NoError(t, err)
	require.Empty(t, out.PendingCandidates)

	// 没有生效快照（还没固定）：mode 为 auto，候选全部算待批准。
	empty := newPSSStore()
	empty.addCandidate(5, pssPayloadB())
	auto := newPSSService(t, t.TempDir())
	auto.ConfigureSnapshots(newPSSSettings(""), empty, nil)
	out, err = NewPricingSnapshotAdminService(auto, empty, nil, nil).Overview(ctx)
	require.NoError(t, err)
	require.Equal(t, PricingSnapshotModeAuto, out.Mode)
	require.Nil(t, out.Active)
	require.Len(t, out.PendingCandidates, 1)

	empty.getActiveErr = errors.New("db down")
	_, err = NewPricingSnapshotAdminService(auto, empty, nil, nil).Overview(ctx)
	require.Error(t, err)
}

func TestPricingSnapshotAdmin_PinAndFetchDelegate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	pssWriteFile(t, dir, pssPayload("0.000001"))
	store := newPSSStore()
	client := pricingRemoteClientStub{
		hash: func(context.Context) (string, error) { return "", errors.New("no hash") },
		json: func(context.Context) ([]byte, error) { return pssPayloadB(), nil },
	}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		DataDir: dir, RemoteURL: "https://example.com/pricing.json", UpdateIntervalHours: 24 * 365,
	}}, client)
	svc.ConfigureSnapshots(newPSSSettings(""), store, nil)
	require.NoError(t, svc.Initialize())
	defer svc.Stop()
	admin := NewPricingSnapshotAdminService(svc, store, nil, nil)

	_, err := admin.FetchCandidate(ctx, 9)
	require.ErrorIs(t, err, ErrPricingNotPinned, "没有固定之前不能拉取候选")

	res, err := admin.Pin(ctx, 9)
	require.NoError(t, err)
	require.True(t, res.Created)

	fetched, err := admin.FetchCandidate(ctx, 9)
	require.NoError(t, err)
	require.True(t, fetched.Created)
	require.Equal(t, int64(9), *store.inserted[0].FetchedBy)
}

func TestFetchCandidateSnapshot(t *testing.T) {
	ctx := context.Background()
	newSvc := func(t *testing.T, remote func() ([]byte, error), store *pssStore) *PricingService {
		client := pricingRemoteClientStub{
			hash: func(context.Context) (string, error) { return "", errors.New("no hash") },
			json: func(context.Context) ([]byte, error) { return remote() },
		}
		svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
			DataDir: t.TempDir(), RemoteURL: "https://example.com/pricing.json", UpdateIntervalHours: 24 * 365,
		}}, client)
		svc.ConfigureSnapshots(newPSSSettings("pinned"), store, nil)
		require.NoError(t, svc.Initialize())
		t.Cleanup(svc.Stop)
		return svc
	}
	active := pssPayload("0.000001")
	newStore := func() *pssStore {
		st := newPSSStore()
		st.setActive(1, active)
		return st
	}

	t.Run("saves a candidate and dedupes by content hash", func(t *testing.T) {
		store := newStore()
		svc := newSvc(t, func() ([]byte, error) { return pssPayloadB(), nil }, store)
		res, err := svc.FetchCandidateSnapshot(ctx, nil)
		require.NoError(t, err)
		require.True(t, res.Created)
		require.False(t, res.Unchanged)
		require.Equal(t, PricingSnapshotSourceRemote, store.inserted[0].Source)
		require.Equal(t, "https://example.com/pricing.json", store.inserted[0].SourceURL)
		require.Equal(t, int64(1), *store.inserted[0].ParentSnapshotID)
		require.Equal(t, 3, store.inserted[0].ModelCount)
		require.Nil(t, store.inserted[0].FetchedBy)

		again, err := svc.FetchCandidateSnapshot(ctx, nil)
		require.NoError(t, err)
		require.False(t, again.Created, "同一内容的候选不重复保存")
		require.Equal(t, res.Candidate.ID, again.Candidate.ID)
		require.Len(t, store.inserted, 1)
	})

	t.Run("remote equal to the active snapshot saves nothing", func(t *testing.T) {
		store := newStore()
		svc := newSvc(t, func() ([]byte, error) { return active, nil }, store)
		res, err := svc.FetchCandidateSnapshot(ctx, nil)
		require.NoError(t, err)
		require.True(t, res.Unchanged)
		require.Nil(t, res.Candidate)
		require.Empty(t, store.inserted)
	})

	t.Run("failures", func(t *testing.T) {
		store := newStore()
		svc := newSvc(t, func() ([]byte, error) { return nil, errors.New("404") }, store)
		_, err := svc.FetchCandidateSnapshot(ctx, nil)
		require.Error(t, err)

		svc = newSvc(t, func() ([]byte, error) { return []byte("not json"), nil }, store)
		_, err = svc.FetchCandidateSnapshot(ctx, nil)
		require.Error(t, err)

		svc = newSvc(t, func() ([]byte, error) { return pssPayloadB(), nil }, store)
		store.insertErr = errors.New("db down")
		_, err = svc.FetchCandidateSnapshot(ctx, nil)
		require.Error(t, err)
		store.insertErr = nil

		store.mu.Lock()
		store.active = nil
		store.mu.Unlock()
		_, err = svc.FetchCandidateSnapshot(ctx, nil)
		require.ErrorIs(t, err, ErrPricingNotPinned)
		store.getActiveErr = errors.New("db down")
		_, err = svc.FetchCandidateSnapshot(ctx, nil)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrPricingNotPinned)
	})

	t.Run("unavailable and not pinned", func(t *testing.T) {
		var nilSvc *PricingService
		_, err := nilSvc.FetchCandidateSnapshot(ctx, nil)
		require.ErrorIs(t, err, ErrPricingSnapshotsUnavailable)
		_, err = newPSSService(t, t.TempDir()).FetchCandidateSnapshot(ctx, nil)
		require.ErrorIs(t, err, ErrPricingSnapshotsUnavailable)

		dir := t.TempDir()
		pssWriteFile(t, dir, active)
		auto := newPSSService(t, dir)
		auto.ConfigureSnapshots(newPSSSettings(""), newPSSStore(), nil)
		require.NoError(t, auto.Initialize())
		defer auto.Stop()
		_, err = auto.FetchCandidateSnapshot(ctx, nil)
		require.ErrorIs(t, err, ErrPricingNotPinned)
	})

	t.Run("remote source not configured", func(t *testing.T) {
		store := newStore()
		svc := newPSSService(t, t.TempDir())
		svc.ConfigureSnapshots(newPSSSettings("pinned"), store, nil)
		require.NoError(t, svc.Initialize())
		defer svc.Stop()
		_, err := svc.FetchCandidateSnapshot(ctx, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not configured")
	})
}

func TestAutoFetchSnapshotCandidate(t *testing.T) {
	calls := 0
	fail := false
	client := pricingRemoteClientStub{
		hash: func(context.Context) (string, error) { return "", errors.New("no hash") },
		json: func(context.Context) ([]byte, error) {
			calls++
			if fail {
				return nil, errors.New("404")
			}
			return pssPayloadB(), nil
		},
	}
	store := newPSSStore()
	store.setActive(1, pssPayload("0.000001"))
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		DataDir: t.TempDir(), RemoteURL: "https://example.com/pricing.json", UpdateIntervalHours: 24 * 365,
	}}, client)
	svc.ConfigureSnapshots(newPSSSettings("pinned"), store, nil)
	require.NoError(t, svc.Initialize())
	defer svc.Stop()
	ctx := context.Background()

	// 失败不计入「已拉取」，下个周期会再试。
	fail = true
	svc.autoFetchSnapshotCandidate(ctx)
	require.Equal(t, 1, calls)
	require.Zero(t, svc.snap.lastAutoFetch.Load())
	require.Empty(t, store.deleted)

	fail = false
	svc.autoFetchSnapshotCandidate(ctx)
	require.Equal(t, 2, calls)
	require.Len(t, store.inserted, 1)
	require.Len(t, store.deleted, 1)
	require.WithinDuration(t, time.Now().Add(-pricingSnapshotCandidateRetention), store.deleted[0], time.Minute)

	// 24 小时内不再拉取。
	svc.autoFetchSnapshotCandidate(ctx)
	require.Equal(t, 2, calls)

	// 过了一天再拉：内容没变，不重复保存；清理失败只告警。
	svc.snap.lastAutoFetch.Store(time.Now().Add(-25 * time.Hour).Unix())
	store.deleteErr = errors.New("db down")
	svc.autoFetchSnapshotCandidate(ctx)
	require.Equal(t, 3, calls)
	require.Len(t, store.inserted, 1)
	require.Len(t, store.deleted, 2)

	// 远程与生效快照相同：没有候选可存，照样记为已拉取。
	store.setActive(9, pssPayloadB())
	svc.snap.lastAutoFetch.Store(0)
	require.NoError(t, svc.refreshSnapshotState(ctx))
	svc.autoFetchSnapshotCandidate(ctx)
	require.NotZero(t, svc.snap.lastAutoFetch.Load())
}

func TestActiveSnapshotID(t *testing.T) {
	var nilSvc *PricingService
	require.Zero(t, nilSvc.ActiveSnapshotID())
	require.Zero(t, newPSSService(t, t.TempDir()).ActiveSnapshotID())

	dir := t.TempDir()
	pssWriteFile(t, dir, pssPayload("0.000001"))
	auto := newPSSService(t, dir)
	auto.ConfigureSnapshots(newPSSSettings(""), newPSSStore(), nil)
	require.NoError(t, auto.Initialize())
	defer auto.Stop()
	require.Zero(t, auto.ActiveSnapshotID(), "auto 模式恒为 0")

	// 报价缓存键带上它。
	p := &PriceQuoter{billing: NewBillingService(nil, auto)}
	require.Zero(t, p.PricingSnapshotID())
	require.Zero(t, (*PriceQuoter)(nil).PricingSnapshotID())
	require.Zero(t, (&PriceQuoter{}).PricingSnapshotID())
}
