package service

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"
)

// W6 PR6：回放的调度、聚合与输出（设计 4.5）。
//
// 数据来源抽象成 PricingReplayDataSource（实现在 repository，只读连接、按 id 做 keyset 分页），
// 所以本文件可以用内存数据源做单元测试。输出两样东西：JSON 汇总（PricingReplaySummary）与差异 CSV。

// 默认参数（设计 4.5：每批 5000 行）。
const (
	DefaultPricingReplayBatchSize   = 5000
	DefaultPricingReplayMaxDiffRows = 200000
	DefaultPricingReplaySampleSize  = 20
	DefaultPricingReplayCalibration = 24 * time.Hour

	// 汇总里的体量上限：每个分组最多按多少个不同的请求模型分别计数（超出并入 replayOverflowModel），
	// 以及每个分组在汇总里最多列多少个有差异的模型。
	pricingReplayMaxModelsPerGroup = 2000
	pricingReplayMaxModelsListed   = 50
	pricingReplayMaxErrorSamples   = 20
	replayOverflowModel            = "(other)"
)

// PricingReplayRateKey 用户专属倍率的键。
type PricingReplayRateKey struct {
	UserID  int64
	GroupID int64
}

// PricingReplayBatchQuery 读取一批历史用量的条件：id 大于 AfterID、不大于 MaxID，时间在 [From, To) 内，
// 分组在 GroupIDs 里，按 id 升序，最多 Limit 行。
type PricingReplayBatchQuery struct {
	AfterID  int64
	MaxID    int64
	From, To time.Time
	GroupIDs []int64
	Limit    int
}

// PricingReplayFingerprint 回放开始与结束时各取一次的配置指纹，用来绑定结果（设计 4.3、4.5 的 S-8）。
type PricingReplayFingerprint struct {
	// ChannelConfigHash 是渠道配置相关全部表的内容摘要（渠道、分组关联、定价、区间、账号成本规则）。
	ChannelConfigHash string `json:"channel_config_hash"`
	// GroupMatrixHash 是各分组在库里已落库的矩阵行（配置、单元格、成本核算规则）的内容摘要。
	GroupMatrixHash map[int64]string `json:"group_matrix_hash"`
}

// PricingReplayDataSource 回放的数据来源，只读。
type PricingReplayDataSource interface {
	// UsageGroupCounts 返回时间窗口内每个分组的用量行数（含已软删的分组）。
	UsageGroupCounts(ctx context.Context, from, to time.Time) (map[int64]int64, error)
	// LoadGroups 返回这些分组里未软删的那些（计费需要的字段）。
	LoadGroups(ctx context.Context, ids []int64) (map[int64]*Group, error)
	// IDBounds 返回时间窗口内用量行 id 的最小值与最大值；窗口内没有行时都是 0。
	IDBounds(ctx context.Context, from, to time.Time) (minID, maxID int64, err error)
	// Batch 读取一批用量行（不带 Group 与 Multiplier）。
	Batch(ctx context.Context, q PricingReplayBatchQuery) ([]PricingReplayRow, error)
	// UserGroupRates 返回这些用户的专属分组倍率（只含非空的）。
	UserGroupRates(ctx context.Context, userIDs []int64) (map[PricingReplayRateKey]float64, error)
	// Fingerprint 计算配置指纹。
	Fingerprint(ctx context.Context, groupIDs []int64) (*PricingReplayFingerprint, error)
}

// PricingReplayDeriver 按渠道当前配置实时派生分组的矩阵状态并与库里现状对照，由 PricingDerivationService 实现。
type PricingReplayDeriver interface {
	ViewGroup(ctx context.Context, groupID int64) (*GroupDeriveView, error)
}

// PricingReplayOptions 一次回放的参数。
type PricingReplayOptions struct {
	From, To time.Time
	// GroupIDs 为空表示窗口内有用量的全部未软删分组。
	GroupIDs  []int64
	BatchSize int
	Workers   int
	// MaxDiffRows 是差异 CSV 的行数上限，超出的差异照常计数，只是不再写 CSV（汇总里标注截断）。
	MaxDiffRows int
	// SampleSize 是汇总里保留的前 N 条翻译差异样本。
	SampleSize int
	// CalibrationWindow 是校准取样的窗口：窗口末尾往前这么久的行（设计 4.5：最近 24 小时）。
	CalibrationWindow time.Duration

	// DiffCSV 为 nil 表示不写差异 CSV；Progress 为 nil 表示不打印进度。
	DiffCSV  io.Writer
	Progress io.Writer
	// Now 可注入，默认 time.Now。
	Now func() time.Time
}

// ---------------------------------------------------------------------------
// 汇总
// ---------------------------------------------------------------------------

// PricingReplayDiffCount 一个（类别、分类、原因）的差异次数。
type PricingReplayDiffCount struct {
	Kind   string `json:"kind"`
	Class  string `json:"class"`
	Reason string `json:"reason,omitempty"`
	Count  int64  `json:"count"`
}

// PricingReplayModelSummary 一个分组里有差异的请求模型。
type PricingReplayModelSummary struct {
	Model       string `json:"model"`
	Rows        int64  `json:"rows"`
	Translation int64  `json:"translation_diffs"`
	Expected    int64  `json:"expected_diffs"`
}

// PricingReplayGroupSummary 一个分组的结果。
type PricingReplayGroupSummary struct {
	GroupID  int64  `json:"group_id"`
	Platform string `json:"platform"`
	// RowsInWindow 是窗口内这个分组的用量行数；RowsReplayed 是实际参与比较的行数。
	RowsInWindow int64                    `json:"rows_in_window"`
	RowsReplayed int64                    `json:"rows_replayed"`
	Uncovered    int64                    `json:"rows_uncovered"`
	Errors       int64                    `json:"rows_errored"`
	Diffs        []PricingReplayDiffCount `json:"diffs"`
	// LegacyActualSum 与 V2ActualSum 是两侧结算后的 ActualCost 之和（信息性，量级用，不是对账数）。
	LegacyActualSum float64                     `json:"legacy_actual_cost_sum"`
	V2ActualSum     float64                     `json:"v2_actual_cost_sum"`
	ModelsWithDiffs []PricingReplayModelSummary `json:"models_with_diffs"`
	ModelsTotal     int                         `json:"models_total"`
}

// PricingReplayAccountSummary 一个上游账号的结果（按 account_id 拆开，避免全池聚合掩盖个别账号的差异）。
type PricingReplayAccountSummary struct {
	AccountID       int64                    `json:"account_id"`
	RowsReplayed    int64                    `json:"rows_replayed"`
	Diffs           []PricingReplayDiffCount `json:"diffs,omitempty"`
	LegacyActualSum float64                  `json:"legacy_actual_cost_sum"`
	V2ActualSum     float64                  `json:"v2_actual_cost_sum"`
}

// PricingReplayCalibration 校准（信息性，不作为通过条件）：窗口末尾一段时间里 legacy 引擎重算的 ActualCost
// 与历史上实际记的 actual_cost 的吻合率，用来发现输入重建本身的错误。时间越久配置漂移越大，所以只取最近一段。
type PricingReplayCalibration struct {
	WindowHours float64 `json:"window_hours"`
	Rows        int64   `json:"rows"`
	Matched     int64   `json:"matched"`
	MatchRate   float64 `json:"match_rate"`
}

// PricingReplayGroupBinding 一个分组的绑定信息（设计 4.3 的 S-8）：切换到 v2 时重新派生一次，
// 派生 revision 与这里记录的一致才放行。
type PricingReplayGroupBinding struct {
	GroupID   int64  `json:"group_id"`
	Platform  string `json:"platform"`
	ChannelID int64  `json:"channel_id"`
	// DeriveRevision 是按渠道当前配置实时派生的 revision（配置、单元格、成本核算规则与所用官方价事实的摘要）。
	DeriveRevision string `json:"derive_revision"`
	// StoredInSync 为 true 表示库里已落库的矩阵行与实时派生的结果一致（再跑一次钩子不会写任何东西）。
	StoredInSync         bool   `json:"stored_in_sync"`
	StoredStage          string `json:"stored_stage"`
	StoredConfigRevision int64  `json:"stored_config_revision"`
	// MatrixHashBefore 与 MatrixHashAfter 是回放开始与结束时库里矩阵行的内容摘要。
	MatrixHashBefore string `json:"matrix_hash_before"`
	MatrixHashAfter  string `json:"matrix_hash_after"`
	Error            string `json:"error,omitempty"`
}

// PricingReplayBinding 结果绑定的配置。
type PricingReplayBinding struct {
	ChannelConfigHashBefore string `json:"channel_config_hash_before"`
	ChannelConfigHashAfter  string `json:"channel_config_hash_after"`
	// Stable 为 false 表示回放期间渠道配置或矩阵行变过，这次结果作废。
	Stable bool                        `json:"stable"`
	Groups []PricingReplayGroupBinding `json:"groups"`
}

// PricingReplaySkippedGroup 没有参与回放的分组。
type PricingReplaySkippedGroup struct {
	GroupID int64  `json:"group_id"`
	Rows    int64  `json:"rows_in_window"`
	Reason  string `json:"reason"`
}

// PricingReplaySample 一条差异样本（只含金额、模式、模型名，不含用户信息）。
type PricingReplaySample struct {
	UsageLogID     int64           `json:"usage_log_id"`
	CreatedAt      time.Time       `json:"created_at"`
	GroupID        int64           `json:"group_id"`
	AccountID      int64           `json:"account_id"`
	Kind           string          `json:"kind"`
	Class          string          `json:"class"`
	Reason         string          `json:"reason,omitempty"`
	Model          string          `json:"model"`
	RequestedModel string          `json:"requested_model"`
	Legacy         json.RawMessage `json:"legacy_view"`
	V2             json.RawMessage `json:"v2_view"`
}

// PricingReplayVerdict 通过判定：翻译差异为 0、没有回放错误、配置在回放期间没变、确实回放了行。
type PricingReplayVerdict struct {
	Pass             bool     `json:"pass"`
	TranslationDiffs int64    `json:"translation_diffs"`
	ExpectedDiffs    int64    `json:"expected_diffs"`
	Errors           int64    `json:"errors"`
	Reasons          []string `json:"fail_reasons,omitempty"`
}

// PricingReplaySummary 一次回放的 JSON 汇总。
type PricingReplaySummary struct {
	Tool           string    `json:"tool"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	ElapsedSeconds float64   `json:"elapsed_seconds"`
	WindowFrom     time.Time `json:"window_from"`
	WindowTo       time.Time `json:"window_to"`
	BatchSize      int       `json:"batch_size"`
	Workers        int       `json:"workers"`
	// Meta 由调用方填写运行环境（库名、价格数据摘要、矩阵来源、版本等）。
	Meta map[string]string `json:"meta,omitempty"`

	RowsInWindow  int64   `json:"rows_in_window"`
	RowsSelected  int64   `json:"rows_selected"`
	RowsReplayed  int64   `json:"rows_replayed"`
	RowsUncovered int64   `json:"rows_uncovered"`
	RowsErrored   int64   `json:"rows_errored"`
	RowsPerSecond float64 `json:"rows_per_second"`
	// Coverage 是参与比较的行数占已选分组行数的比例（信息性）。
	Coverage float64 `json:"coverage"`

	Verdict       PricingReplayVerdict          `json:"verdict"`
	Binding       PricingReplayBinding          `json:"binding"`
	Diffs         []PricingReplayDiffCount      `json:"diffs"`
	GroupChecks   []PricingReplaySample         `json:"group_checks,omitempty"`
	Groups        []PricingReplayGroupSummary   `json:"groups"`
	Accounts      []PricingReplayAccountSummary `json:"accounts"`
	SkippedGroups []PricingReplaySkippedGroup   `json:"skipped_groups,omitempty"`
	Calibration   PricingReplayCalibration      `json:"calibration"`

	FirstTranslationDiffs []PricingReplaySample `json:"first_translation_diffs"`
	ErrorSamples          []string              `json:"error_samples,omitempty"`
	DiffCSVRows           int64                 `json:"diff_csv_rows"`
	DiffCSVTruncated      bool                  `json:"diff_csv_truncated"`
}

// ---------------------------------------------------------------------------
// 聚合
// ---------------------------------------------------------------------------

type replayDiffKey struct{ Kind, Class, Reason string }

type replayModelAgg struct{ rows, translation, expected int64 }

type replayGroupAgg struct {
	group      *Group
	inWindow   int64
	replayed   int64
	uncovered  int64
	errored    int64
	diffs      map[replayDiffKey]int64
	legacySum  float64
	v2Sum      float64
	models     map[string]*replayModelAgg
	calibRows  int64
	calibMatch int64
}

type replayAccountAgg struct {
	replayed  int64
	diffs     map[replayDiffKey]int64
	legacySum float64
	v2Sum     float64
}

func sortedDiffCounts(m map[replayDiffKey]int64) []PricingReplayDiffCount {
	out := make([]PricingReplayDiffCount, 0, len(m))
	for k, n := range m {
		out = append(out, PricingReplayDiffCount{Kind: k.Kind, Class: k.Class, Reason: k.Reason, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Reason < b.Reason
	})
	return out
}

// replayCostMatches 校准用的数值比较：历史 actual_cost 以固定小数位落库，容忍舍入。
func replayCostMatches(recomputed, stored float64) bool {
	diff := math.Abs(recomputed - stored)
	return diff <= 1e-8 || diff <= 1e-6*math.Abs(stored)
}

// replayRun 一次回放运行里的可变状态，只由调度协程访问。
type replayRun struct {
	opts      PricingReplayOptions
	groups    map[int64]*replayGroupAgg
	accounts  map[int64]*replayAccountAgg
	totals    map[replayDiffKey]int64
	samples   []PricingReplaySample
	errSample []string
	csvw      *csv.Writer
	csvRows   int64
	csvCut    bool
	calibFrom time.Time
	replayed  int64
	uncovered int64
	errored   int64
}

func newReplayRun(opts PricingReplayOptions) *replayRun {
	run := &replayRun{
		opts:      opts,
		groups:    make(map[int64]*replayGroupAgg),
		accounts:  make(map[int64]*replayAccountAgg),
		totals:    make(map[replayDiffKey]int64),
		calibFrom: opts.To.Add(-opts.CalibrationWindow),
	}
	if opts.DiffCSV != nil {
		run.csvw = csv.NewWriter(opts.DiffCSV)
		_ = run.csvw.Write([]string{
			"usage_log_id", "created_at", "group_id", "account_id", "kind", "class", "reason",
			"model", "requested_model", "legacy_view", "v2_view",
		})
	}
	return run
}

func (run *replayRun) groupAgg(g *Group) *replayGroupAgg {
	agg := run.groups[g.ID]
	if agg == nil {
		agg = &replayGroupAgg{group: g, diffs: make(map[replayDiffKey]int64), models: make(map[string]*replayModelAgg)}
		run.groups[g.ID] = agg
	}
	return agg
}

func (agg *replayGroupAgg) modelAgg(model string) *replayModelAgg {
	m := agg.models[model]
	if m == nil {
		if len(agg.models) >= pricingReplayMaxModelsPerGroup {
			model = replayOverflowModel
			if m = agg.models[model]; m != nil {
				return m
			}
		}
		m = &replayModelAgg{}
		agg.models[model] = m
	}
	return m
}

func (run *replayRun) accountAgg(id int64) *replayAccountAgg {
	agg := run.accounts[id]
	if agg == nil {
		agg = &replayAccountAgg{diffs: make(map[replayDiffKey]int64)}
		run.accounts[id] = agg
	}
	return agg
}

// addDiff 登记一条差异：总数、分组、账号计数，写 CSV，保留前 N 条翻译差异样本。
func (run *replayRun) addDiff(row *PricingReplayRow, g *replayGroupAgg, d PricingReplayDiff) {
	key := replayDiffKey{Kind: d.Kind, Class: d.Class, Reason: d.Reason}
	run.totals[key]++
	g.diffs[key]++
	if row != nil {
		run.accountAgg(row.AccountID).diffs[key]++
	}

	wantSample := d.Class == ShadowClassTranslation && len(run.samples) < run.opts.SampleSize
	wantCSV := run.csvw != nil && run.csvRows < int64(run.opts.MaxDiffRows)
	if run.csvw != nil && !wantCSV {
		run.csvCut = true
	}
	if !wantSample && !wantCSV {
		return
	}
	sample := PricingReplaySample{GroupID: g.group.ID, Kind: d.Kind, Class: d.Class, Reason: d.Reason, Model: d.Model}
	if row != nil {
		sample.UsageLogID, sample.CreatedAt, sample.AccountID = row.ID, row.CreatedAt, row.AccountID
		sample.RequestedModel = row.RequestedModel
		if sample.RequestedModel == "" {
			sample.RequestedModel = row.Model
		}
	}
	sample.Legacy, _ = json.Marshal(d.Legacy)
	sample.V2, _ = json.Marshal(d.V2)
	if wantSample {
		run.samples = append(run.samples, sample)
	}
	if wantCSV {
		_ = run.csvw.Write([]string{
			strconv.FormatInt(sample.UsageLogID, 10), sample.CreatedAt.UTC().Format(time.RFC3339Nano),
			strconv.FormatInt(sample.GroupID, 10), strconv.FormatInt(sample.AccountID, 10),
			sample.Kind, sample.Class, sample.Reason, sample.Model, sample.RequestedModel,
			string(sample.Legacy), string(sample.V2),
		})
		run.csvRows++
	}
}

// addRow 登记一行的结果。
func (run *replayRun) addRow(row *PricingReplayRow, out PricingReplayOutcome) {
	g := run.groupAgg(row.Group)
	switch {
	case out.Err != "":
		g.errored++
		run.errored++
		if len(run.errSample) < pricingReplayMaxErrorSamples {
			run.errSample = append(run.errSample, fmt.Sprintf("usage_log %d: %s", row.ID, out.Err))
		}
		return
	case !out.Covered:
		g.uncovered++
		run.uncovered++
		return
	}
	g.replayed++
	run.replayed++
	g.legacySum += out.LegacyActualCost
	g.v2Sum += out.V2ActualCost
	acct := run.accountAgg(row.AccountID)
	acct.replayed++
	acct.legacySum += out.LegacyActualCost
	acct.v2Sum += out.V2ActualCost

	model := row.RequestedModel
	if model == "" {
		model = row.Model
	}
	m := g.modelAgg(model)
	m.rows++
	for _, d := range out.Diffs {
		run.addDiff(row, g, d)
		if d.Class == ShadowClassTranslation {
			m.translation++
		} else {
			m.expected++
		}
	}
	if !row.CreatedAt.Before(run.calibFrom) {
		g.calibRows++
		if !out.LegacyCostFailed && replayCostMatches(out.LegacyActualCost, row.StoredActualCost) {
			g.calibMatch++
		}
	}
}

// ---------------------------------------------------------------------------
// 运行
// ---------------------------------------------------------------------------

// normalize 补默认值并校验。
func (o *PricingReplayOptions) normalize() error {
	if o.From.IsZero() || o.To.IsZero() || !o.From.Before(o.To) {
		return fmt.Errorf("replay window is empty: from %s, to %s", o.From, o.To)
	}
	if o.BatchSize <= 0 {
		o.BatchSize = DefaultPricingReplayBatchSize
	}
	if o.Workers <= 0 {
		o.Workers = 1
	}
	if o.MaxDiffRows <= 0 {
		o.MaxDiffRows = DefaultPricingReplayMaxDiffRows
	}
	if o.SampleSize <= 0 {
		o.SampleSize = DefaultPricingReplaySampleSize
	}
	if o.CalibrationWindow <= 0 {
		o.CalibrationWindow = DefaultPricingReplayCalibration
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return nil
}

// selectGroups 决定回放哪些分组：显式指定的取交集（未软删），没指定的取窗口内有用量的全部未软删分组。
// 返回选中的分组、窗口内每个分组的行数、没有参与回放的分组。
func (r *PricingReplayer) selectGroups(ctx context.Context, src PricingReplayDataSource, opts PricingReplayOptions) (map[int64]*Group, map[int64]int64, []PricingReplaySkippedGroup, error) {
	counts, err := src.UsageGroupCounts(ctx, opts.From, opts.To)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("count usage rows by group: %w", err)
	}
	var candidates []int64
	seen := make(map[int64]struct{})
	if len(opts.GroupIDs) == 0 {
		for id := range counts {
			candidates = append(candidates, id)
		}
	}
	for _, id := range opts.GroupIDs {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			candidates = append(candidates, id)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	live, err := src.LoadGroups(ctx, candidates)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load groups: %w", err)
	}

	var skipped []PricingReplaySkippedGroup
	selected := make(map[int64]*Group, len(live))
	for _, id := range candidates {
		if g, ok := live[id]; ok && g != nil {
			g.ID = id
			selected[id] = g
			continue
		}
		skipped = append(skipped, PricingReplaySkippedGroup{GroupID: id, Rows: counts[id], Reason: "soft_deleted_or_missing"})
	}
	if len(opts.GroupIDs) > 0 {
		chosen := make(map[int64]struct{}, len(opts.GroupIDs))
		for _, id := range opts.GroupIDs {
			chosen[id] = struct{}{}
		}
		var rest []int64
		for id := range counts {
			if _, ok := chosen[id]; !ok {
				rest = append(rest, id)
			}
		}
		sort.Slice(rest, func(i, j int) bool { return rest[i] < rest[j] })
		for _, id := range rest {
			skipped = append(skipped, PricingReplaySkippedGroup{GroupID: id, Rows: counts[id], Reason: "not_selected"})
		}
	}
	return selected, counts, skipped, nil
}

// replayBatch 用 workers 个协程并行回放一批行；结果按行的顺序放回，所以输出与并行度无关。
func (r *PricingReplayer) replayBatch(ctx context.Context, rows []PricingReplayRow, workers int) []PricingReplayOutcome {
	outs := make([]PricingReplayOutcome, len(rows))
	if workers <= 1 || len(rows) < 2*workers {
		for i := range rows {
			outs[i] = r.Replay(ctx, &rows[i])
		}
		return outs
	}
	chunk := (len(rows) + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < len(rows); lo += chunk {
		hi := min(lo+chunk, len(rows))
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				outs[i] = r.Replay(ctx, &rows[i])
			}
		}(lo, hi)
	}
	wg.Wait()
	return outs
}

// attachInputs 给一批行补上 Group 与 Multiplier：专属倍率优先，其次分组倍率（与结算一致，额外倍率在成本函数里另乘）。
// 补不上分组的行（理论上不会发生，因为查询已按分组过滤）保持 Group 为 nil，由调用方当作未覆盖。
func attachReplayInputs(rows []PricingReplayRow, groups map[int64]*Group, rates map[PricingReplayRateKey]float64) {
	for i := range rows {
		row := &rows[i]
		g := groups[row.EffectiveGroupID()]
		if g == nil {
			// 服务分组不在选中集合里（例如已被软删）：退回主分组，仍然两侧一致。
			g = groups[row.GroupID]
		}
		row.Group = g
		if g == nil {
			continue
		}
		row.Multiplier = g.RateMultiplier
		if rate, ok := rates[PricingReplayRateKey{UserID: row.UserID, GroupID: g.ID}]; ok {
			row.Multiplier = rate
		}
	}
}

// Run 执行一次回放。deriver 可以为 nil（汇总里就没有派生 revision，不能用于切换门槛）。
func (r *PricingReplayer) Run(ctx context.Context, src PricingReplayDataSource, deriver PricingReplayDeriver, opts PricingReplayOptions) (*PricingReplaySummary, error) {
	if err := opts.normalize(); err != nil {
		return nil, err
	}
	started := opts.Now()

	selected, counts, skipped, err := r.selectGroups(ctx, src, opts)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var inWindow, selectedRows int64
	for id, n := range counts {
		inWindow += n
		if _, ok := selected[id]; ok {
			selectedRows += n
		}
	}

	run := newReplayRun(opts)
	for _, id := range ids {
		g := selected[id]
		agg := run.groupAgg(g)
		agg.inWindow = counts[id]
	}

	before, err := src.Fingerprint(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("fingerprint before replay: %w", err)
	}
	bindings := r.collectBindings(ctx, deriver, selected, ids, before)

	// 每个分组一次的功能类比较。
	for _, id := range ids {
		for _, d := range r.CheckGroup(ctx, selected[id]) {
			run.addDiff(nil, run.groupAgg(selected[id]), d)
		}
	}
	groupChecks := append([]PricingReplaySample(nil), run.samples...)
	run.samples = nil

	if err := r.scan(ctx, src, run, selected, ids); err != nil {
		return nil, err
	}

	after, err := src.Fingerprint(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("fingerprint after replay: %w", err)
	}
	if run.csvw != nil {
		run.csvw.Flush()
		if err := run.csvw.Error(); err != nil {
			return nil, fmt.Errorf("write diff csv: %w", err)
		}
	}

	finished := opts.Now()
	sum := &PricingReplaySummary{
		Tool: "pricing-replay", StartedAt: started, FinishedAt: finished,
		ElapsedSeconds: finished.Sub(started).Seconds(),
		WindowFrom:     opts.From, WindowTo: opts.To, BatchSize: opts.BatchSize, Workers: opts.Workers,
		RowsInWindow: inWindow, RowsSelected: selectedRows,
		RowsReplayed: run.replayed, RowsUncovered: run.uncovered, RowsErrored: run.errored,
		SkippedGroups: skipped, GroupChecks: groupChecks,
		FirstTranslationDiffs: run.samples, ErrorSamples: run.errSample,
		DiffCSVRows: run.csvRows, DiffCSVTruncated: run.csvCut,
		Diffs: sortedDiffCounts(run.totals),
	}
	if sum.FirstTranslationDiffs == nil {
		sum.FirstTranslationDiffs = []PricingReplaySample{}
	}
	if sec := sum.ElapsedSeconds; sec > 0 {
		sum.RowsPerSecond = float64(run.replayed) / sec
	}
	if selectedRows > 0 {
		sum.Coverage = float64(run.replayed) / float64(selectedRows)
	}
	sum.Binding = finishBindings(before, after, bindings)
	sum.Groups = run.groupSummaries(ids)
	sum.Accounts = run.accountSummaries()
	sum.Calibration = run.calibration()
	sum.Verdict = replayVerdict(sum)
	return sum, nil
}

// scan 按 id 做 keyset 分页读取并回放。
func (r *PricingReplayer) scan(ctx context.Context, src PricingReplayDataSource, run *replayRun, selected map[int64]*Group, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	opts := run.opts
	minID, maxID, err := src.IDBounds(ctx, opts.From, opts.To)
	if err != nil {
		return fmt.Errorf("read id bounds: %w", err)
	}
	if maxID == 0 {
		return nil
	}
	rates := make(map[PricingReplayRateKey]float64)
	seenUsers := make(map[int64]struct{})
	afterID := minID - 1
	var batches int
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := src.Batch(ctx, PricingReplayBatchQuery{
			AfterID: afterID, MaxID: maxID, From: opts.From, To: opts.To, GroupIDs: ids, Limit: opts.BatchSize,
		})
		if err != nil {
			return fmt.Errorf("read batch after id %d: %w", afterID, err)
		}
		if len(rows) == 0 {
			return nil
		}

		var fresh []int64
		for i := range rows {
			uid := rows[i].UserID
			if _, ok := seenUsers[uid]; !ok {
				seenUsers[uid] = struct{}{}
				fresh = append(fresh, uid)
			}
		}
		if len(fresh) > 0 {
			got, err := src.UserGroupRates(ctx, fresh)
			if err != nil {
				return fmt.Errorf("read user group rates: %w", err)
			}
			for k, v := range got {
				rates[k] = v
			}
		}
		attachReplayInputs(rows, selected, rates)
		outs := r.replayBatch(ctx, rows, opts.Workers)
		for i := range rows {
			if rows[i].Group == nil {
				// 查询按分组过滤，这里不会发生；防御性地当作没覆盖，避免空指针。
				run.uncovered++
				continue
			}
			run.addRow(&rows[i], outs[i])
		}

		afterID = rows[len(rows)-1].ID
		batches++
		if opts.Progress != nil && batches%20 == 0 {
			_, _ = fmt.Fprintf(opts.Progress, "replayed %d rows (last id %d), translation diffs so far %d\n",
				run.replayed, afterID, run.translationTotal())
		}
		if len(rows) < opts.BatchSize || afterID >= maxID {
			return nil
		}
	}
}

func (run *replayRun) translationTotal() int64 {
	var n int64
	for k, c := range run.totals {
		if k.Class == ShadowClassTranslation {
			n += c
		}
	}
	return n
}

// collectBindings 为每个分组取实时派生的 revision 与库里现状的对照。派生失败只记在 Error 里，不中断回放
// （但绑定不完整，通过判定会要求 revision 非空）。
func (r *PricingReplayer) collectBindings(ctx context.Context, deriver PricingReplayDeriver, selected map[int64]*Group, ids []int64, before *PricingReplayFingerprint) []PricingReplayGroupBinding {
	out := make([]PricingReplayGroupBinding, 0, len(ids))
	for _, id := range ids {
		b := PricingReplayGroupBinding{GroupID: id, Platform: selected[id].Platform}
		if before != nil {
			b.MatrixHashBefore = before.GroupMatrixHash[id]
		}
		if deriver == nil {
			b.Error = "no deriver"
		} else if view, err := deriver.ViewGroup(ctx, id); err != nil {
			b.Error = err.Error()
		} else if view != nil {
			b.ChannelID = view.Derived.ChannelID
			b.DeriveRevision = view.Derived.Revision
			b.StoredInSync = view.InSync
			if view.StoredConfig != nil {
				b.StoredStage = string(view.StoredConfig.PricingStage)
				b.StoredConfigRevision = view.StoredConfig.Revision
			} else {
				b.StoredStage = string(PricingStageLegacy)
			}
		}
		out = append(out, b)
	}
	return out
}

// finishBindings 比对回放开始与结束时的指纹：任何一处不同，这次回放就不能作为切换依据。
func finishBindings(before, after *PricingReplayFingerprint, groups []PricingReplayGroupBinding) PricingReplayBinding {
	b := PricingReplayBinding{Groups: groups, Stable: true}
	if before != nil {
		b.ChannelConfigHashBefore = before.ChannelConfigHash
	}
	if after != nil {
		b.ChannelConfigHashAfter = after.ChannelConfigHash
	}
	if b.ChannelConfigHashBefore != b.ChannelConfigHashAfter {
		b.Stable = false
	}
	for i := range b.Groups {
		g := &b.Groups[i]
		if after != nil {
			g.MatrixHashAfter = after.GroupMatrixHash[g.GroupID]
		}
		if g.MatrixHashBefore != g.MatrixHashAfter {
			b.Stable = false
		}
	}
	return b
}

func (run *replayRun) groupSummaries(ids []int64) []PricingReplayGroupSummary {
	out := make([]PricingReplayGroupSummary, 0, len(ids))
	for _, id := range ids {
		agg := run.groups[id]
		if agg == nil {
			continue
		}
		s := PricingReplayGroupSummary{
			GroupID: id, Platform: agg.group.Platform,
			RowsInWindow: agg.inWindow, RowsReplayed: agg.replayed, Uncovered: agg.uncovered, Errors: agg.errored,
			Diffs:           sortedDiffCounts(agg.diffs),
			LegacyActualSum: agg.legacySum, V2ActualSum: agg.v2Sum,
			ModelsWithDiffs: []PricingReplayModelSummary{},
			ModelsTotal:     len(agg.models),
		}
		for model, m := range agg.models {
			if m.translation+m.expected > 0 {
				s.ModelsWithDiffs = append(s.ModelsWithDiffs, PricingReplayModelSummary{
					Model: model, Rows: m.rows, Translation: m.translation, Expected: m.expected,
				})
			}
		}
		sort.Slice(s.ModelsWithDiffs, func(i, j int) bool {
			a, b := s.ModelsWithDiffs[i], s.ModelsWithDiffs[j]
			if a.Translation != b.Translation {
				return a.Translation > b.Translation
			}
			if a.Expected != b.Expected {
				return a.Expected > b.Expected
			}
			return a.Model < b.Model
		})
		if len(s.ModelsWithDiffs) > pricingReplayMaxModelsListed {
			s.ModelsWithDiffs = s.ModelsWithDiffs[:pricingReplayMaxModelsListed]
		}
		out = append(out, s)
	}
	return out
}

func (run *replayRun) accountSummaries() []PricingReplayAccountSummary {
	out := make([]PricingReplayAccountSummary, 0, len(run.accounts))
	for id, agg := range run.accounts {
		out = append(out, PricingReplayAccountSummary{
			AccountID: id, RowsReplayed: agg.replayed, Diffs: sortedDiffCounts(agg.diffs),
			LegacyActualSum: agg.legacySum, V2ActualSum: agg.v2Sum,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

func (run *replayRun) calibration() PricingReplayCalibration {
	c := PricingReplayCalibration{WindowHours: run.opts.CalibrationWindow.Hours()}
	for _, g := range run.groups {
		c.Rows += g.calibRows
		c.Matched += g.calibMatch
	}
	if c.Rows > 0 {
		c.MatchRate = float64(c.Matched) / float64(c.Rows)
	}
	return c
}

// replayVerdict 给出通过判定与未通过的原因。
func replayVerdict(sum *PricingReplaySummary) PricingReplayVerdict {
	v := PricingReplayVerdict{Errors: sum.RowsErrored}
	for _, d := range sum.Diffs {
		if d.Class == ShadowClassTranslation {
			v.TranslationDiffs += d.Count
		} else {
			v.ExpectedDiffs += d.Count
		}
	}
	if v.TranslationDiffs > 0 {
		v.Reasons = append(v.Reasons, fmt.Sprintf("%d translation diffs", v.TranslationDiffs))
	}
	if v.Errors > 0 {
		v.Reasons = append(v.Reasons, fmt.Sprintf("%d rows failed to replay", v.Errors))
	}
	if !sum.Binding.Stable {
		v.Reasons = append(v.Reasons, "channel configuration or matrix rows changed during the replay")
	}
	for _, g := range sum.Binding.Groups {
		if g.DeriveRevision == "" {
			v.Reasons = append(v.Reasons, fmt.Sprintf("group %d has no derive revision (%s)", g.GroupID, g.Error))
		}
	}
	if sum.RowsReplayed == 0 {
		v.Reasons = append(v.Reasons, "no rows were replayed")
	}
	v.Pass = len(v.Reasons) == 0
	return v
}
