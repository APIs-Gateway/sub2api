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

// W6 PR4b-2b-2：目录状态转换、已知免费名单、成本核算规则、开放时预检、W5 登记项、装配。

func w2Reason(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	return infraerrors.Reason(err)
}

// ---- 目录状态转换 ----

type w2CatalogStore struct {
	entry      *ModelCatalogEntry
	getErr     error
	usage      CatalogUsage
	usageErr   error
	usageReqs  [][]string
	usageSince []time.Time
	updateOK   bool
	updateErr  error
	updates    [][3]string
}

func (s *w2CatalogStore) GetByID(_ context.Context, _ int64) (*ModelCatalogEntry, error) {
	return s.entry, s.getErr
}

func (s *w2CatalogStore) UpdateStatus(_ context.Context, _ int64, from, to ModelCatalogStatus) (bool, error) {
	s.updates = append(s.updates, [3]string{"", string(from), string(to)})
	return s.updateOK, s.updateErr
}

func (s *w2CatalogStore) CountModelUsageSince(_ context.Context, models []string, since time.Time) (CatalogUsage, error) {
	s.usageReqs = append(s.usageReqs, models)
	s.usageSince = append(s.usageSince, since)
	return s.usage, s.usageErr
}

type w2CatalogRepo struct {
	created []*ModelCatalogEntry
	list    []ModelCatalogEntry
}

func (r *w2CatalogRepo) List(_ context.Context, _ ModelCatalogFilter) ([]ModelCatalogEntry, error) {
	return r.list, nil
}

func (r *w2CatalogRepo) Create(_ context.Context, e *ModelCatalogEntry) error {
	r.created = append(r.created, e)
	return nil
}

func w2NewTransition(store *w2CatalogStore) (*ModelCatalogTransitionService, *w2CatalogRepo) {
	repo := &w2CatalogRepo{}
	svc := NewModelCatalogTransitionService(store, NewModelCatalogService(repo))
	svc.now = func() time.Time { return pwNow }
	return svc, repo
}

func TestModelCatalogTransition_PreviewAndTransition(t *testing.T) {
	ctx := context.Background()
	last := pwNow.Add(-time.Hour)
	entry := &ModelCatalogEntry{ID: 4, ModelKey: "gpt-x", Aliases: []string{"gpt-x-latest"}, Status: ModelCatalogActive}

	t.Run("blocking target with recent usage needs confirmation", func(t *testing.T) {
		store := &w2CatalogStore{entry: entry, usage: CatalogUsage{Requests: 12, LastUsedAt: &last}, updateOK: true}
		svc, _ := w2NewTransition(store)
		p, err := svc.Preview(ctx, 4, ModelCatalogRetired)
		require.NoError(t, err)
		require.True(t, p.ConfirmRequired)
		require.Equal(t, int64(12), p.Usage.Requests)
		require.Equal(t, 7, p.Usage.WindowDays)
		require.Equal(t, []string{"gpt-x", "gpt-x-latest"}, store.usageReqs[0])
		require.Equal(t, pwNow.Add(-CatalogUsageWindow), store.usageSince[0])

		_, err = svc.Transition(ctx, 4, ModelCatalogRetired, false)
		require.Equal(t, ReasonCatalogUsageConfirm, w2Reason(t, err))
		require.Equal(t, "12", infraerrors.FromError(err).Metadata["requests"])
		require.Empty(t, store.updates)

		out, err := svc.Transition(ctx, 4, ModelCatalogRetired, true)
		require.NoError(t, err)
		require.Equal(t, ModelCatalogRetired, out.Status)
		require.Equal(t, pwNow, out.UpdatedAt, "返回的 updated_at 是转换时间，不是旧值")
		require.Equal(t, [][3]string{{"", "active", "retired"}}, store.updates)
	})

	t.Run("no recent usage and non blocking targets skip confirmation", func(t *testing.T) {
		store := &w2CatalogStore{entry: entry, updateOK: true}
		svc, _ := w2NewTransition(store)
		p, err := svc.Preview(ctx, 4, ModelCatalogDraft)
		require.NoError(t, err)
		require.False(t, p.ConfirmRequired)
		_, err = svc.Transition(ctx, 4, ModelCatalogDraft, false)
		require.NoError(t, err)

		store.usageReqs = nil
		p, err = svc.Preview(ctx, 4, ModelCatalogActive)
		require.NoError(t, err)
		require.False(t, p.ConfirmRequired)
		require.Empty(t, store.usageReqs, "转成 active 不查用量")
	})

	t.Run("same status is a no-op", func(t *testing.T) {
		store := &w2CatalogStore{entry: entry}
		svc, _ := w2NewTransition(store)
		out, err := svc.Transition(ctx, 4, ModelCatalogActive, false)
		require.NoError(t, err)
		require.Equal(t, ModelCatalogActive, out.Status)
		require.Empty(t, store.updates)
		require.Empty(t, store.usageReqs)
	})

	t.Run("preview of the current status still reports usage", func(t *testing.T) {
		store := &w2CatalogStore{entry: entry, usage: CatalogUsage{Requests: 5}}
		svc, _ := w2NewTransition(store)
		p, err := svc.Preview(ctx, 4, ModelCatalogActive)
		require.NoError(t, err)
		require.Equal(t, int64(5), p.Usage.Requests, "目标等于当前状态时也带近 7 天用量")
		require.Equal(t, 7, p.Usage.WindowDays)
		require.False(t, p.ConfirmRequired, "空操作不需要确认")
		require.Len(t, store.usageReqs, 1)

		retired := &ModelCatalogEntry{ID: 4, ModelKey: "gpt-x", Status: ModelCatalogRetired}
		store = &w2CatalogStore{entry: retired, usage: CatalogUsage{Requests: 5}}
		svc, _ = w2NewTransition(store)
		p, err = svc.Preview(ctx, 4, ModelCatalogRetired)
		require.NoError(t, err)
		require.Equal(t, int64(5), p.Usage.Requests)
		require.False(t, p.ConfirmRequired, "已是 retired：再转 retired 不会新挡流量，不要求确认")
	})

	t.Run("concurrent change is reported", func(t *testing.T) {
		store := &w2CatalogStore{entry: entry, updateOK: false}
		svc, _ := w2NewTransition(store)
		_, err := svc.Transition(ctx, 4, ModelCatalogDraft, true)
		require.Equal(t, ReasonCatalogStatusChanged, w2Reason(t, err))
	})

	t.Run("errors", func(t *testing.T) {
		svc, _ := w2NewTransition(&w2CatalogStore{entry: entry})
		_, err := svc.Preview(ctx, 4, ModelCatalogStatus("bogus"))
		require.Equal(t, ErrModelCatalogInvalid.Reason, w2Reason(t, err))

		svc, _ = w2NewTransition(&w2CatalogStore{getErr: ErrModelCatalogNotFound})
		_, err = svc.Preview(ctx, 4, ModelCatalogDraft)
		require.Equal(t, ErrModelCatalogNotFound.Reason, w2Reason(t, err))

		svc, _ = w2NewTransition(&w2CatalogStore{})
		_, err = svc.Preview(ctx, 4, ModelCatalogDraft)
		require.Equal(t, ErrModelCatalogNotFound.Reason, w2Reason(t, err))

		boom := errors.New("boom")
		svc, _ = w2NewTransition(&w2CatalogStore{entry: entry, usageErr: boom})
		_, err = svc.Preview(ctx, 4, ModelCatalogDraft)
		require.ErrorIs(t, err, boom)
		_, err = svc.Transition(ctx, 4, ModelCatalogDraft, true)
		require.ErrorIs(t, err, boom)

		svc, _ = w2NewTransition(&w2CatalogStore{entry: entry, updateErr: boom})
		_, err = svc.Transition(ctx, 4, ModelCatalogRetired, true)
		require.ErrorIs(t, err, boom)
	})
}

func TestModelCatalogTransition_CreateChecked(t *testing.T) {
	ctx := context.Background()
	in := CreateModelCatalogInput{ModelKey: "New-Model", Platform: "openai"}

	// 默认 draft：名字近 7 天有用量时必须确认。
	store := &w2CatalogStore{usage: CatalogUsage{Requests: 3}}
	svc, repo := w2NewTransition(store)
	_, err := svc.CreateChecked(ctx, in, false)
	require.Equal(t, ReasonCatalogUsageConfirm, w2Reason(t, err))
	require.Empty(t, repo.created)
	require.Equal(t, []string{"new-model"}, store.usageReqs[0])

	out, err := svc.CreateChecked(ctx, in, true)
	require.NoError(t, err)
	require.Equal(t, ModelCatalogDraft, out.Status)
	require.Len(t, repo.created, 1)

	// 没有用量、或者直接建成 active：不挡。
	svc, repo = w2NewTransition(&w2CatalogStore{})
	_, err = svc.CreateChecked(ctx, in, false)
	require.NoError(t, err)
	active := in
	active.Status = ModelCatalogActive
	store = &w2CatalogStore{usage: CatalogUsage{Requests: 99}}
	svc, repo = w2NewTransition(store)
	_, err = svc.CreateChecked(ctx, active, false)
	require.NoError(t, err)
	require.Empty(t, store.usageReqs)
	require.Len(t, repo.created, 1)

	// 非法输入与用量读取失败。
	_, err = svc.CreateChecked(ctx, CreateModelCatalogInput{Platform: "openai"}, false)
	require.Error(t, err)
	boom := errors.New("boom")
	svc, _ = w2NewTransition(&w2CatalogStore{usageErr: boom})
	_, err = svc.CreateChecked(ctx, in, false)
	require.ErrorIs(t, err, boom)
}

// ---- 已知免费名单 ----

type w2FreeStore struct {
	raw      string
	getErr   error
	setErr   error
	cellsErr error
	cells    []ExposureCell
	sets     []string
	locks    []bool
	reads    int // 不加锁读的次数
}

func (s *w2FreeStore) GetKnownFreeList(_ context.Context, _ MatrixExecutor) (string, error) {
	s.reads++
	return s.raw, s.getErr
}

func (s *w2FreeStore) GetKnownFreeListTx(_ context.Context, _ MatrixExecutor) (string, error) {
	return s.raw, s.getErr
}

func (s *w2FreeStore) SetKnownFreeListTx(_ context.Context, _ MatrixTx, raw string) error {
	s.sets = append(s.sets, raw)
	return s.setErr
}

func (s *w2FreeStore) AllowlistOpenCellsTx(_ context.Context, _ MatrixExecutor, lock bool) ([]ExposureCell, error) {
	s.locks = append(s.locks, lock)
	return s.cells, s.cellsErr
}

func TestNormalizeKnownFreeList(t *testing.T) {
	out, err := NormalizeKnownFreeList([]BillingKnownFreeEntry{
		{GroupID: 2, Model: " b "}, {GroupID: 0, Model: "Z", Note: " n "}, {GroupID: 2, Model: "A"},
	})
	require.NoError(t, err)
	require.Equal(t, []BillingKnownFreeEntry{{GroupID: 0, Model: "Z", Note: "n"}, {GroupID: 2, Model: "A"}, {GroupID: 2, Model: "b"}}, out)

	many := make([]BillingKnownFreeEntry, MaxKnownFreeListEntries+1)
	for name, in := range map[string][]BillingKnownFreeEntry{
		"empty model":    {{Model: "  "}},
		"long model":     {{Model: strings.Repeat("a", 201)}},
		"long note":      {{Model: "a", Note: strings.Repeat("n", 501)}},
		"negative group": {{Model: "a", GroupID: -1}},
		"duplicate":      {{Model: "a", GroupID: 1}, {Model: "A", GroupID: 1}},
		"too many":       many,
	} {
		_, err := NormalizeKnownFreeList(in)
		require.Equal(t, ReasonKnownFreeListInvalid, w2Reason(t, err), name)
	}
}

func w2FreeService(store *w2FreeStore) (*KnownFreeListService, *pwFakeStore) {
	ps := &pwFakeStore{}
	return NewKnownFreeListService(ps, store, exOfficial), ps
}

func TestKnownFreeListService_CurrentNeverConfiguredIsEmptyArray(t *testing.T) {
	store := &w2FreeStore{raw: ""}
	svc, _ := w2FreeService(store)
	cur, err := svc.Current(context.Background())
	require.NoError(t, err)
	require.NotNil(t, cur, "没配置过时返回空切片，JSON 才是 [] 而不是 null")
	require.Empty(t, cur)
	b, err := json.Marshal(map[string]any{"entries": cur})
	require.NoError(t, err)
	require.JSONEq(t, `{"entries":[]}`, string(b))
	require.Equal(t, 1, store.reads, "读接口不加锁")
	require.Empty(t, store.locks)
}

func TestKnownFreeListService_CurrentAndPreview(t *testing.T) {
	ctx := context.Background()
	store := &w2FreeStore{
		raw:   `[{"group_id":1,"model":"zero","note":"x"},{"group_id":0,"model":"gone"}]`,
		cells: []ExposureCell{exCell(1, "zero", MatrixPriceInherit), exCell(1, "priced", MatrixPriceInherit)},
	}
	svc, _ := w2FreeService(store)
	cur, err := svc.Current(ctx)
	require.NoError(t, err)
	require.Len(t, cur, 2)

	// 删掉 zero 的名单项：zero 在白名单分组 1 里是开放的，改后变成 0 元违规。
	change, err := svc.Preview(ctx, []BillingKnownFreeEntry{{Model: "gone"}})
	require.NoError(t, err)
	require.True(t, change.Changed)
	require.Equal(t, []BillingKnownFreeEntry{{GroupID: 1, Model: "zero", Note: "x"}}, change.Removed)
	require.Len(t, change.NewViolations, 1)
	require.Equal(t, "zero", change.NewViolations[0].ModelKey)
	require.Equal(t, []bool{false}, store.locks)

	// 加条目只会减少违规；不变的名单 Changed 为 false。
	change, err = svc.Preview(ctx, []BillingKnownFreeEntry{{GroupID: 1, Model: "zero", Note: "x"}, {GroupID: 0, Model: "gone"}})
	require.NoError(t, err)
	require.False(t, change.Changed)
	require.Empty(t, change.NewViolations)
	require.Empty(t, change.Removed)

	// 写坏的现有名单按空名单看待，Current 把错误交给管理员。
	svc, _ = w2FreeService(&w2FreeStore{raw: `not json`})
	_, err = svc.Current(ctx)
	require.Error(t, err)
	change, err = svc.Preview(ctx, []BillingKnownFreeEntry{{Model: "m"}})
	require.NoError(t, err)
	require.True(t, change.Changed)
	require.Empty(t, change.Before)

	// 各种错误。
	boom := errors.New("boom")
	svc, _ = w2FreeService(&w2FreeStore{getErr: boom})
	_, err = svc.Current(ctx)
	require.ErrorIs(t, err, boom)
	_, err = svc.Preview(ctx, nil)
	require.ErrorIs(t, err, boom)
	svc, _ = w2FreeService(&w2FreeStore{cellsErr: boom})
	_, err = svc.Preview(ctx, nil)
	require.ErrorIs(t, err, boom)
	_, err = svc.Preview(ctx, []BillingKnownFreeEntry{{Model: ""}})
	require.Equal(t, ReasonKnownFreeListInvalid, w2Reason(t, err))
}

func TestKnownFreeListService_Update(t *testing.T) {
	ctx := context.Background()
	admin := PriceWriteActor{ID: 9, Interactive: true}
	newList := []BillingKnownFreeEntry{{GroupID: 1, Model: "zero"}}

	cases := map[string]struct {
		actor   PriceWriteActor
		confirm bool
		want    string
	}{
		"no actor":        {PriceWriteActor{}, true, ReasonPriceWriteActorRequired},
		"machine token":   {PriceWriteActor{ID: 9}, true, ReasonPriceWriteInteractive},
		"no confirmation": {admin, false, ReasonPriceWriteConfirm},
	}
	for name, tc := range cases {
		store := &w2FreeStore{}
		svc, ps := w2FreeService(store)
		_, err := svc.Update(ctx, tc.actor, newList, tc.confirm)
		require.Equal(t, tc.want, w2Reason(t, err), name)
		require.Zero(t, ps.txRuns, name)
		require.Empty(t, store.sets, name)
	}

	// 写入：在事务里锁分组、写入。
	store := &w2FreeStore{raw: "[]", cells: []ExposureCell{exCell(1, "zero", MatrixPriceInherit)}}
	svc, ps := w2FreeService(store)
	change, err := svc.Update(ctx, admin, newList, true)
	require.NoError(t, err)
	require.True(t, change.Changed)
	require.Equal(t, []string{`[{"group_id":1,"model":"zero"}]`}, store.sets)
	require.Equal(t, []bool{true}, store.locks)
	require.Equal(t, 1, ps.txRuns)

	// 没有变化：不写。
	store = &w2FreeStore{raw: `[{"group_id":1,"model":"zero"}]`}
	svc, _ = w2FreeService(store)
	change, err = svc.Update(ctx, admin, newList, true)
	require.NoError(t, err)
	require.False(t, change.Changed)
	require.Empty(t, store.sets)

	// 删条目让白名单分组出现无价的开放项：阻止，事务回滚，名单不变。
	store = &w2FreeStore{raw: `[{"group_id":1,"model":"zero"}]`, cells: []ExposureCell{exCell(1, "zero", MatrixPriceInherit)}}
	svc, ps = w2FreeService(store)
	_, err = svc.Update(ctx, admin, nil, true)
	require.Equal(t, ReasonExposureUnpriced, w2Reason(t, err))
	require.Empty(t, store.sets)
	require.Equal(t, 1, ps.txRollbacks)

	// 存储错误与非法输入。
	boom := errors.New("boom")
	for _, st := range []*w2FreeStore{{getErr: boom}, {cellsErr: boom}, {setErr: boom}} {
		svc, _ = w2FreeService(st)
		_, err = svc.Update(ctx, admin, newList, true)
		require.ErrorIs(t, err, boom)
	}
	svc, _ = w2FreeService(&w2FreeStore{})
	_, err = svc.Update(ctx, admin, []BillingKnownFreeEntry{{Model: ""}}, true)
	require.Equal(t, ReasonKnownFreeListInvalid, w2Reason(t, err))
}

// ---- 成本核算规则 ----

type w2CostWriter struct {
	calls  []string
	err    error
	groups []int64
	base   []int64
	specs  []CostRuleSpec
}

func (w *w2CostWriter) res(groupID, ruleID int64) (*CostRuleWriteResult, error) {
	if w.err != nil {
		return nil, w.err
	}
	return &CostRuleWriteResult{GroupID: groupID, RuleID: ruleID, Revision: 9}, nil
}

func (w *w2CostWriter) CreateTx(_ context.Context, _ MatrixTx, groupID, baseline int64, spec CostRuleSpec) (*CostRuleWriteResult, error) {
	w.calls, w.groups, w.base, w.specs = append(w.calls, "create"), append(w.groups, groupID), append(w.base, baseline), append(w.specs, spec)
	return w.res(groupID, 11)
}

func (w *w2CostWriter) UpdateTx(_ context.Context, _ MatrixTx, groupID, baseline, ruleID int64, spec CostRuleSpec) (*CostRuleWriteResult, error) {
	w.calls, w.groups, w.base, w.specs = append(w.calls, "update"), append(w.groups, groupID), append(w.base, baseline), append(w.specs, spec)
	return w.res(groupID, ruleID)
}

func (w *w2CostWriter) DeleteTx(_ context.Context, _ MatrixTx, groupID, baseline, ruleID int64) (*CostRuleWriteResult, error) {
	w.calls, w.groups, w.base = append(w.calls, "delete"), append(w.groups, groupID), append(w.base, baseline)
	return w.res(groupID, ruleID)
}

func w2Spec() CostRuleSpec {
	return CostRuleSpec{
		Name: " rule ", GroupIDs: []int64{3, 1}, AccountIDs: []int64{2}, Enabled: true,
		Prices: []MatrixCostRulePrice{{
			Platform: "openai", Models: []string{"b", " a "},
			Price: MatrixCustomPrice{InputPrice: pwF(1e-6), OutputPrice: pwF(2e-6)},
		}},
	}
}

func TestNormalizeCostRuleSpec(t *testing.T) {
	out, err := NormalizeCostRuleSpec(w2Spec())
	require.NoError(t, err)
	require.Equal(t, "rule", out.Name)
	require.Equal(t, []int64{1, 3}, out.GroupIDs)
	require.Equal(t, []string{"a", "b"}, out.Prices[0].Models)
	require.Equal(t, BillingModeToken, out.Prices[0].Price.BillingMode)

	mut := map[string]func(*CostRuleSpec){
		"empty name":       func(s *CostRuleSpec) { s.Name = " " },
		"long name":        func(s *CostRuleSpec) { s.Name = strings.Repeat("n", 101) },
		"dup group":        func(s *CostRuleSpec) { s.GroupIDs = []int64{1, 1} },
		"bad group":        func(s *CostRuleSpec) { s.GroupIDs = []int64{0} },
		"dup account":      func(s *CostRuleSpec) { s.AccountIDs = []int64{4, 4} },
		"no prices":        func(s *CostRuleSpec) { s.Prices = nil },
		"no models":        func(s *CostRuleSpec) { s.Prices[0].Models = nil },
		"blank model":      func(s *CostRuleSpec) { s.Prices[0].Models = []string{" "} },
		"long platform":    func(s *CostRuleSpec) { s.Prices[0].Platform = strings.Repeat("p", 33) },
		"unknown mode":     func(s *CostRuleSpec) { s.Prices[0].Price.BillingMode = "weird" },
		"per request bare": func(s *CostRuleSpec) { s.Prices[0].Price = MatrixCustomPrice{BillingMode: BillingModePerRequest} },
		"negative price":   func(s *CostRuleSpec) { s.Prices[0].Price.InputPrice = pwF(-1) },
		"token price cap":  func(s *CostRuleSpec) { s.Prices[0].Price.OutputPrice = pwF(0.011) },
		"cache price cap":  func(s *CostRuleSpec) { s.Prices[0].Price.CacheReadPrice = pwF(1) },
		"per request cap": func(s *CostRuleSpec) {
			s.Prices[0].Price = MatrixCustomPrice{BillingMode: BillingModePerRequest, PerRequestPrice: pwF(1000.01)}
		},
		"interval price cap": func(s *CostRuleSpec) {
			s.Prices[0].Price = MatrixCustomPrice{Intervals: []MatrixPriceInterval{{MinTokens: 0, InputPrice: pwF(5)}}}
		},
	}
	for name, f := range mut {
		s := w2Spec()
		f(&s)
		_, err := NormalizeCostRuleSpec(s)
		require.Equal(t, ReasonCostRuleInvalid, w2Reason(t, err), name)
	}
	// 名字按字符数算：100 个汉字可以，101 个不行。
	s0 := w2Spec()
	s0.Name = strings.Repeat("成", maxCostRuleNameLen)
	_, err = NormalizeCostRuleSpec(s0)
	require.NoError(t, err)
	s0.Name += "本"
	_, err = NormalizeCostRuleSpec(s0)
	require.Equal(t, ReasonCostRuleInvalid, w2Reason(t, err))

	// 价格超过上限时 metadata 带 field 与 reason；恰好等于上限放行。
	s0 = w2Spec()
	s0.Prices[0].Price.InputPrice = pwF(MaxCustomTokenPrice)
	_, err = NormalizeCostRuleSpec(s0)
	require.NoError(t, err)
	s0.Prices[0].Price.InputPrice = pwF(MaxCustomTokenPrice * 2)
	_, err = NormalizeCostRuleSpec(s0)
	require.Equal(t, ReasonCostRuleInvalid, w2Reason(t, err))
	require.Equal(t, "input_price", infraerrors.FromError(err).Metadata["field"])
	require.Equal(t, ReasonPriceTooHigh, infraerrors.FromError(err).Metadata["reason"])

	ids := make([]int64, maxCostRuleIDs+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	s := w2Spec()
	s.AccountIDs = ids
	_, err = NormalizeCostRuleSpec(s)
	require.Equal(t, ReasonCostRuleInvalid, w2Reason(t, err))
	s = w2Spec()
	s.Prices = make([]MatrixCostRulePrice, maxCostRulePrices+1)
	_, err = NormalizeCostRuleSpec(s)
	require.Equal(t, ReasonCostRuleInvalid, w2Reason(t, err))
}

func TestCostRuleService(t *testing.T) {
	ctx := context.Background()
	w := &w2CostWriter{}
	inv := &pwFakeInvalidator{}
	svc := NewCostRuleService(&pwFakeStore{}, w, inv)

	res, err := svc.Create(ctx, 7, 5, 3, w2Spec())
	require.NoError(t, err)
	require.Equal(t, int64(11), res.RuleID)
	require.Equal(t, "rule", w.specs[0].Name, "写入器拿到的是规范化之后的规则")
	res, err = svc.Update(ctx, 7, 5, 3, 11, w2Spec())
	require.NoError(t, err)
	require.Equal(t, int64(11), res.RuleID)
	_, err = svc.Delete(ctx, 7, 5, 3, 11)
	require.NoError(t, err)
	require.Equal(t, []string{"create", "update", "delete"}, w.calls)
	require.Equal(t, [][]int64{{5}, {5}, {5}}, inv.calls)

	// 没有缓存失效器也能写。
	_, err = NewCostRuleService(&pwFakeStore{}, w, nil).Create(ctx, 7, 5, 3, w2Spec())
	require.NoError(t, err)

	// 参数校验：不进事务。
	ps := &pwFakeStore{}
	svc = NewCostRuleService(ps, w, inv)
	_, err = svc.Create(ctx, 0, 5, 3, w2Spec())
	require.Equal(t, ReasonPriceWriteActorRequired, w2Reason(t, err))
	_, err = svc.Create(ctx, 7, 0, 3, w2Spec())
	require.Equal(t, ReasonCostRuleInvalid, w2Reason(t, err))
	_, err = svc.Create(ctx, 7, 5, 0, w2Spec())
	require.Equal(t, ReasonCostRuleInvalid, w2Reason(t, err))
	_, err = svc.Update(ctx, 7, 5, 3, 0, w2Spec())
	require.Equal(t, ReasonCostRuleInvalid, w2Reason(t, err))
	_, err = svc.Delete(ctx, 7, 5, 3, 0)
	require.Equal(t, ReasonCostRuleInvalid, w2Reason(t, err))
	bad := w2Spec()
	bad.Name = ""
	_, err = svc.Create(ctx, 7, 5, 3, bad)
	require.Equal(t, ReasonCostRuleInvalid, w2Reason(t, err))
	_, err = svc.Update(ctx, 7, 5, 3, 1, bad)
	require.Equal(t, ReasonCostRuleInvalid, w2Reason(t, err))
	require.Zero(t, ps.txRuns)

	// 没有配置写入器：失败关闭。
	_, err = NewCostRuleService(ps, nil, nil).Create(ctx, 7, 5, 3, w2Spec())
	require.Equal(t, ReasonPriceWriterMissing, w2Reason(t, err))
	var nilSvc *CostRuleService
	_, err = nilSvc.Delete(ctx, 7, 5, 3, 1)
	require.Equal(t, ReasonPriceWriterMissing, w2Reason(t, err))

	// 写入器与事务的错误原样返回，不失效缓存。
	boom := errors.New("boom")
	inv2 := &pwFakeInvalidator{}
	_, err = NewCostRuleService(&pwFakeStore{}, &w2CostWriter{err: boom}, inv2).Create(ctx, 7, 5, 3, w2Spec())
	require.ErrorIs(t, err, boom)
	require.Empty(t, inv2.calls)
	_, err = NewCostRuleService(&pwFakeStore{txErr: boom}, w, inv2).Delete(ctx, 7, 5, 3, 1)
	require.ErrorIs(t, err, boom)
}

// ---- 开放时预检 ----

type w2Source struct {
	snaps map[int64]GroupStateSnapshot
	err   error
	calls [][]int64
}

func (s *w2Source) LoadGroupSnapshots(_ context.Context, ids []int64) (map[int64]GroupStateSnapshot, error) {
	s.calls = append(s.calls, ids)
	return s.snaps, s.err
}

func w2Snap(mode MatrixAccessMode, stage PricingStage, mapping []MatrixMappingEntry, cells ...MatrixCell) GroupStateSnapshot {
	snap := GroupStateSnapshot{Config: &StoredGroupConfig{
		GroupID: 1, PricingStage: stage, MatrixGroupConfig: MatrixGroupConfig{AccessMode: mode, ModelMapping: mapping},
	}}
	for _, c := range cells {
		snap.Cells = append(snap.Cells, StoredMatrixCell{GroupID: 1, MatrixCell: c})
	}
	return snap
}

func w2Open(key string) MatrixCell {
	return MatrixCell{ModelKey: key, Open: true, PriceMode: MatrixPriceInherit}
}

func w2Prechecker(src *w2Source, settings SettingRepository) *OpenPrechecker {
	return NewOpenPrechecker(src, NewExposureValidator(exOfficial, settings), exOfficial)
}

func TestOpenPrechecker_Evaluate(t *testing.T) {
	ctx := context.Background()
	wild := MatrixCell{ModelKey: "gpt-*", IsPattern: true, Open: true, PriceMode: MatrixPriceInherit}
	closedWild := wild
	closedWild.Open = false

	t.Run("allowlist blocks unpriced, zero priced and wildcard cells", func(t *testing.T) {
		src := &w2Source{snaps: map[int64]GroupStateSnapshot{1: w2Snap(MatrixAccessAllowlist, PricingStageV2, nil,
			w2Open("priced"), w2Open("nothing"), w2Open("zero"), wild, closedWild)}}
		reps, err := w2Prechecker(src, nil).PrecheckGroups(ctx, []int64{1, 1, 0}, CellOverlay{})
		require.NoError(t, err)
		require.Len(t, reps, 1)
		require.True(t, reps[0].Applicable)
		require.Empty(t, reps[0].Warnings)
		reasons := map[string]string{}
		for _, is := range reps[0].Blocking {
			reasons[is.ModelKey] = is.Reason
		}
		require.Equal(t, map[string]string{
			"nothing": string(ExposureUnpriced), "zero": string(ExposureZeroPrice), "gpt-*": string(ExposureWildcardUnverifiable),
		}, reasons)
		require.Equal(t, [][]int64{{1}}, src.calls)
	})

	t.Run("open group only warns, and ignores wildcard cells", func(t *testing.T) {
		src := &w2Source{snaps: map[int64]GroupStateSnapshot{1: w2Snap(MatrixAccessOpen, PricingStageV2, nil, w2Open("nothing"), wild)}}
		reps, err := w2Prechecker(src, nil).PrecheckGroups(ctx, []int64{1}, CellOverlay{})
		require.NoError(t, err)
		require.Empty(t, reps[0].Blocking)
		require.Len(t, reps[0].Warnings, 1)
		require.Equal(t, "nothing", reps[0].Warnings[0].ModelKey)
	})

	t.Run("legacy and shadow groups, and groups without config, are not applicable", func(t *testing.T) {
		for _, stage := range []PricingStage{PricingStageLegacy, PricingStageShadow} {
			src := &w2Source{snaps: map[int64]GroupStateSnapshot{1: w2Snap(MatrixAccessAllowlist, stage, nil, w2Open("nothing"))}}
			reps, err := w2Prechecker(src, nil).PrecheckGroups(ctx, []int64{1}, CellOverlay{})
			require.NoError(t, err)
			require.False(t, reps[0].Applicable)
			require.Empty(t, reps[0].Blocking)
		}
		src := &w2Source{snaps: map[int64]GroupStateSnapshot{}}
		reps, err := w2Prechecker(src, nil).PrecheckGroups(ctx, []int64{1}, CellOverlay{})
		require.NoError(t, err)
		require.False(t, reps[0].Applicable)
	})

	t.Run("exact mapping onto an open cell needs a priced target", func(t *testing.T) {
		mapping := []MatrixMappingEntry{
			{Src: "priced", Dst: "priced"}, {Src: "priced", Dst: "nothing"}, {Src: "img-only", Dst: "zero"},
			{Src: "pri*", Dst: "nothing"}, {Src: "closed", Dst: "nothing"}, {Src: " ", Dst: "nothing"},
		}
		snap := w2Snap(MatrixAccessAllowlist, PricingStageV2, mapping,
			w2Open("priced"), w2Open("img-only"), MatrixCell{ModelKey: "closed", PriceMode: MatrixPriceInherit})
		src := &w2Source{snaps: map[int64]GroupStateSnapshot{1: snap}}
		reps, err := w2Prechecker(src, nil).PrecheckGroups(ctx, []int64{1}, CellOverlay{})
		require.NoError(t, err)
		got := map[string]string{}
		for _, is := range reps[0].Blocking {
			got[is.ModelKey+"->"+is.Target] = is.Reason
		}
		require.Equal(t, map[string]string{
			"priced->nothing": OpenIssueMappingTargetUnpriced,
			"img-only->zero":  OpenIssueMappingTargetZeroPrice,
		}, got)

		// 映射目标在已知免费名单里：放行。
		free := exFakeSettings{value: `[{"group_id":1,"model":"nothing"}]`}
		reps, err = w2Prechecker(src, free).PrecheckGroups(ctx, []int64{1}, CellOverlay{})
		require.NoError(t, err)
		require.Len(t, reps[0].Blocking, 1)
		require.Equal(t, "zero", reps[0].Blocking[0].Target)
	})

	t.Run("overlay replaces config and cells", func(t *testing.T) {
		src := &w2Source{snaps: map[int64]GroupStateSnapshot{1: w2Snap(MatrixAccessOpen, PricingStageV2, nil, w2Open("nothing"))}}
		p := w2Prechecker(src, nil)
		rep, err := p.PrecheckGroupConfig(ctx, 1, MatrixGroupConfig{AccessMode: MatrixAccessAllowlist})
		require.NoError(t, err)
		require.Len(t, rep.Blocking, 1)

		planned := []PlannedCellWrite{
			{Op: CellOp{GroupID: 1, ModelKey: "extra"}, Action: CellWriteCreate, After: &MatrixCell{ModelKey: "extra", Open: true, PriceMode: MatrixPriceInherit}},
			{Op: CellOp{GroupID: 1, ModelKey: "noop"}, Action: CellWriteNoop},
			{Op: CellOp{GroupID: 1, ModelKey: "gone"}, Action: CellWriteDelete},
		}
		reps, err := p.PrecheckPlanned(ctx, planned)
		require.NoError(t, err)
		require.Len(t, reps, 1)
		require.Len(t, reps[0].Warnings, 2, "nothing 与 extra 都是开放分组里的无价项")

		reps, err = p.PrecheckPlanned(ctx, planned[1:])
		require.NoError(t, err)
		require.Empty(t, reps, "没有 open 写入就不预检")

		rep, err = w2Prechecker(&w2Source{snaps: map[int64]GroupStateSnapshot{}}, nil).PrecheckGroupConfig(ctx, 1, MatrixGroupConfig{})
		require.NoError(t, err)
		require.False(t, rep.Applicable)
	})

	t.Run("not configured and source errors", func(t *testing.T) {
		var nilP *OpenPrechecker
		_, err := nilP.PrecheckGroups(ctx, []int64{1}, CellOverlay{})
		require.Equal(t, ReasonExposureGuardMissing, w2Reason(t, err))
		_, err = (&OpenPrechecker{}).PublishCheck(ctx, 1)
		require.Equal(t, ReasonExposureGuardMissing, w2Reason(t, err))
		boom := errors.New("boom")
		_, err = w2Prechecker(&w2Source{err: boom}, nil).PrecheckGroups(ctx, []int64{1}, CellOverlay{})
		require.ErrorIs(t, err, boom)
		reps, err := w2Prechecker(&w2Source{}, nil).PrecheckGroups(ctx, nil, CellOverlay{})
		require.NoError(t, err)
		require.Empty(t, reps)
	})
}

func TestOpenPrechecker_PublishCheckAndBlockingError(t *testing.T) {
	ctx := context.Background()
	bad := w2Snap(MatrixAccessAllowlist, PricingStageV2, nil, w2Open("nothing"))
	rep, err := w2Prechecker(&w2Source{snaps: map[int64]GroupStateSnapshot{1: bad}}, nil).PublishCheck(ctx, 1)
	require.Equal(t, ReasonGroupPublishBlocked, w2Reason(t, err))
	require.NotNil(t, rep)
	require.Contains(t, infraerrors.FromError(err).Metadata["issues"], "1:nothing:"+string(ExposureUnpriced))

	// 开放分组、legacy 分组与干净的白名单分组都不阻止。
	for _, snap := range []GroupStateSnapshot{
		w2Snap(MatrixAccessOpen, PricingStageV2, nil, w2Open("nothing")),
		w2Snap(MatrixAccessAllowlist, PricingStageLegacy, nil, w2Open("nothing")),
		w2Snap(MatrixAccessAllowlist, PricingStageV2, nil, w2Open("priced")),
	} {
		rep, err = w2Prechecker(&w2Source{snaps: map[int64]GroupStateSnapshot{1: snap}}, nil).PublishCheck(ctx, 1)
		require.NoError(t, err)
		require.Empty(t, rep.Blocking)
	}
	_, err = w2Prechecker(&w2Source{err: errors.New("boom")}, nil).PublishCheck(ctx, 1)
	require.Error(t, err)

	require.NoError(t, BlockingError(nil))
	require.NoError(t, BlockingError([]OpenPrecheckReport{{GroupID: 1}}))
	many := make([]OpenPrecheckIssue, maxOpenPrecheckIssuesListed+5)
	for i := range many {
		many[i] = OpenPrecheckIssue{GroupID: 1, ModelKey: "m", Reason: "r", Target: "t"}
	}
	err = BlockingError([]OpenPrecheckReport{{Blocking: many}})
	require.Equal(t, ReasonOpenPrecheckBlocked, w2Reason(t, err))
	md := infraerrors.FromError(err).Metadata
	require.Equal(t, "25", md["count"])
	require.Equal(t, maxOpenPrecheckIssuesListed, len(strings.Split(md["issues"], ";")))
}

// 预览路径上的预检：白名单分组的阻止项直接拒绝，开放分组的问题随凭证返回。
func TestGateAndGroupConfig_OpenPrecheckOnPropose(t *testing.T) {
	ctx := context.Background()
	planned := []PlannedCellWrite{{
		Op: CellOp{GroupID: 1, ModelKey: "nothing", Kind: CellOpUpsert}, Action: CellWriteCreate, TouchesPrice: true,
		After: &MatrixCell{ModelKey: "nothing", Open: true, PriceMode: MatrixPriceInherit},
	}}
	for _, tc := range []struct {
		mode  MatrixAccessMode
		block bool
	}{{MatrixAccessAllowlist, true}, {MatrixAccessOpen, false}} {
		src := &w2Source{snaps: map[int64]GroupStateSnapshot{1: w2Snap(tc.mode, PricingStageV2, nil)}}
		f := pwNewGate()
		f.writer.planned = planned
		f.gate.WithOpenPrecheck(w2Prechecker(src, nil))
		ticket, err := f.gate.Propose(ctx, PriceWriteProposal{Request: pwPriceRequest()})
		if tc.block {
			require.Equal(t, ReasonOpenPrecheckBlocked, w2Reason(t, err))
			require.Empty(t, f.store.inserted, "被阻止的预览不登记审批行")
			continue
		}
		require.NoError(t, err)
		require.Len(t, ticket.Precheck, 1)
		require.Len(t, ticket.Precheck[0].Warnings, 1)

		src.err = errors.New("boom")
		_, err = f.gate.Propose(ctx, PriceWriteProposal{Request: pwPriceRequest()})
		require.Error(t, err)
	}

	for _, tc := range []struct {
		mode  MatrixAccessMode
		block bool
	}{{MatrixAccessAllowlist, true}, {MatrixAccessOpen, false}} {
		src := &w2Source{snaps: map[int64]GroupStateSnapshot{1: w2Snap(MatrixAccessOpen, PricingStageV2, nil, w2Open("nothing"))}}
		w := &gcFakeWriter{planRes: &GroupConfigWriteResult{
			Changed: true, ExposureRelevant: true,
			After: StoredGroupConfig{GroupID: 1, MatrixGroupConfig: MatrixGroupConfig{AccessMode: tc.mode}},
		}}
		f := gcNewService(w, &exFakeReader{})
		f.svc.WithOpenPrecheck(w2Prechecker(src, nil))
		req := gcBase()
		req.AccessMode = gcAccess(tc.mode)
		ticket, err := f.svc.Propose(ctx, req)
		if tc.block {
			require.Equal(t, ReasonOpenPrecheckBlocked, w2Reason(t, err))
			continue
		}
		require.NoError(t, err)
		require.NotNil(t, ticket.Precheck)
		require.Len(t, ticket.Precheck.Warnings, 1)

		src.err = errors.New("boom")
		_, err = f.svc.Propose(ctx, req)
		require.Error(t, err)
	}
}

// ---- W5 登记项与装配 ----

func TestW6Registry(t *testing.T) {
	for _, r := range W6SettingRegistrations() {
		require.Equal(t, W5TierC, r.Tier)
		require.False(t, r.AIWritable, r.Key)
		require.True(t, IsW6ProtectedSettingKey(r.Key))
		require.True(t, IsW6ProtectedSettingKey(" "+strings.ToUpper(r.Key)+" "))
		for _, a := range r.Aliases {
			require.True(t, IsW6ProtectedSettingKey(a))
		}
	}
	require.False(t, IsW6ProtectedSettingKey("site_name"))
	require.True(t, IsW6GenericWriteBlocked(SettingKeyBillingKnownFreeList))
	require.True(t, IsW6GenericWriteBlocked("billing.unpriced_policy"))
	require.False(t, IsW6GenericWriteBlocked(SettingKeyPricingSnapshotMode), "固定当前价格经通用仓储写这个键")
	require.False(t, IsW6GenericWriteBlocked("site_name"))

	require.NoError(t, W6GenericWriteGuard("site_name", SettingKeyPricingSnapshotMode))
	err := W6GenericWriteGuard("site_name", SettingKeyPricingDefaultStage)
	require.Equal(t, ReasonSettingKeyProtected, w2Reason(t, err))
	require.Equal(t, SettingKeyPricingDefaultStage, infraerrors.FromError(err).Metadata["key"])

	acts := W6ActionRegistrations()
	require.Len(t, acts, 1)
	require.Equal(t, "pricing.stage_switch", acts[0].Name)
	require.True(t, acts[0].TouchesPrice)
	require.True(t, acts[0].TierFromPriceDelta)
	require.NotEqual(t, "price", acts[0].Category)
}

func TestProvidePricingWriteServices(t *testing.T) {
	s := ProvidePricingWriteServices(&pwFakeStore{}, &pwFakeWriter{}, &gcFakeWriter{}, &exFakeReader{}, &w2FreeStore{},
		&w2CatalogStore{}, &w2CostWriter{}, nil, nil, nil, nil, nil, NewModelCatalogService(&w2CatalogRepo{}))
	require.NotNil(t, s.Cells)
	require.NotNil(t, s.GroupCfg)
	require.NotNil(t, s.KnownFree)
	require.NotNil(t, s.Catalog)
	require.NotNil(t, s.CostRules)
	require.NotNil(t, s.Precheck)
}

func TestStageSwitchPriceDelta(t *testing.T) {
	require.Equal(t, PriceDeltaNone, StageSwitchPriceDelta(0, 0, PriceDeltaUp), "没有差异就是 none，不管 diff 给什么")
	require.Equal(t, PriceDeltaUp, StageSwitchPriceDelta(2, 0, PriceDeltaUp))
	require.Equal(t, PriceDeltaDown, StageSwitchPriceDelta(0, 1, PriceDeltaDown))
	require.Equal(t, PriceDeltaUnknown, StageSwitchPriceDelta(1, 0, PriceDeltaNone), "有差异却说 none：不可信")
	require.Equal(t, PriceDeltaUnknown, StageSwitchPriceDelta(1, 0, PriceDelta("sideways")))
	require.Equal(t, PriceDeltaUnknown, StageSwitchPriceDelta(1, 1, PriceDeltaUnknown))
}
