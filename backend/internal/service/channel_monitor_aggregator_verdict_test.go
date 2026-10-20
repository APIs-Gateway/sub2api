//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 卡片状态由主模型最近 3 次探测决定：
//   - 窗口内硬失败（error / failed）≥ 2 次 → 失败（沿用最近一次硬失败的状态）；
//   - 只有 1 次硬失败 → degraded；
//   - 没有硬失败 → 以最近一次探测为准。

const (
	stOK     = MonitorStatusOperational
	stSlow   = MonitorStatusDegraded
	stFailed = MonitorStatusFailed
	stError  = MonitorStatusError
)

func TestDeriveCardStatus(t *testing.T) {
	cases := []struct {
		name string
		in   []string // 最新在前
		want string
	}{
		{"没有历史", nil, ""},
		{"空切片", []string{}, ""},

		// 只有 1 条记录
		{"仅 1 次正常", []string{stOK}, stOK},
		{"仅 1 次降级", []string{stSlow}, stSlow},
		{"仅 1 次 error → 降级，不直接红", []string{stError}, stSlow},
		{"仅 1 次 failed → 降级，不直接红", []string{stFailed}, stSlow},

		// 窗口内 0 次硬失败：看最近一次
		{"三次都正常", []string{stOK, stOK, stOK}, stOK},
		{"最近一次降级", []string{stSlow, stOK, stOK}, stSlow},
		{"最近一次正常、之前降级", []string{stOK, stSlow, stSlow}, stOK},
		{"三次都降级", []string{stSlow, stSlow, stSlow}, stSlow},

		// 窗口内 1 次硬失败 → 降级（不论位置）
		{"最近一次硬失败，前两次正常", []string{stError, stOK, stOK}, stSlow},
		{"中间一次硬失败", []string{stOK, stFailed, stOK}, stSlow},
		{"最早一次硬失败", []string{stOK, stOK, stError}, stSlow},
		{"硬失败 + 降级 + 正常", []string{stSlow, stError, stOK}, stSlow},
		{"硬失败 + 两次降级", []string{stError, stSlow, stSlow}, stSlow},

		// 窗口内 ≥2 次硬失败 → 失败
		{"最近两次硬失败", []string{stError, stError, stOK}, stError},
		{"首尾硬失败", []string{stFailed, stOK, stFailed}, stFailed},
		{"最早两次硬失败", []string{stOK, stFailed, stFailed}, stFailed},
		{"三次都硬失败", []string{stError, stError, stError}, stError},
		{"两次硬失败 + 一次降级", []string{stSlow, stError, stError}, stError},
		{"沿用最近一次硬失败的状态（failed 在前）", []string{stFailed, stError, stOK}, stFailed},
		{"沿用最近一次硬失败的状态（error 在前）", []string{stError, stFailed, stFailed}, stError},
		{"最近一次不是硬失败时取窗口内最近的硬失败", []string{stOK, stFailed, stError}, stFailed},

		// 窗口只有 2 条
		{"只有 2 条，都硬失败", []string{stError, stFailed}, stError},
		{"只有 2 条，1 次硬失败", []string{stError, stOK}, stSlow},
		{"只有 2 条，都正常", []string{stOK, stOK}, stOK},

		// 只看最近 3 次：更早的记录不参与
		{"第 4、5 条的硬失败被忽略", []string{stOK, stOK, stOK, stError, stError}, stOK},
		{"第 4 条的硬失败被忽略，窗口内 1 次硬失败", []string{stOK, stError, stOK, stError}, stSlow},
		{"窗口外是正常也救不了窗口内的两次硬失败", []string{stError, stError, stOK, stOK, stOK}, stError},

		// 防御：未知状态不当成硬失败，原样返回最近一次
		{"未知状态", []string{"mystery", stOK, stOK}, "mystery"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveCardStatus(tc.in); got != tc.want {
				t.Fatalf("deriveCardStatus(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDeriveCardStatus_ExhaustiveWindowOfThree(t *testing.T) {
	// 穷举最近 3 次的所有组合（4^3 = 64 种），逐条核对硬失败计数与结论。
	all := []string{stOK, stSlow, stFailed, stError}
	for _, a := range all {
		for _, b := range all {
			for _, c := range all {
				in := []string{a, b, c}
				hard := 0
				for _, s := range in {
					if s == stFailed || s == stError {
						hard++
					}
				}
				got := deriveCardStatus(in)
				isRed := got == stFailed || got == stError
				switch {
				case hard >= 2 && !isRed:
					t.Errorf("%v: %d hard failures should be red, got %q", in, hard, got)
				case hard == 1 && got != stSlow:
					t.Errorf("%v: 1 hard failure should be degraded, got %q", in, got)
				case hard == 0 && got != a:
					t.Errorf("%v: no hard failure should follow the latest probe %q, got %q", in, a, got)
				}
			}
		}
	}
}

func TestIsHardFailureStatus(t *testing.T) {
	for st, want := range map[string]bool{
		stOK: false, stSlow: false, stFailed: true, stError: true, "": false, "unknown": false,
	} {
		if got := isHardFailureStatus(st); got != want {
			t.Errorf("isHardFailureStatus(%q) = %v, want %v", st, got, want)
		}
	}
}

func historyEntries(statuses ...string) []*ChannelMonitorHistoryEntry {
	out := make([]*ChannelMonitorHistoryEntry, 0, len(statuses))
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for i, s := range statuses {
		out = append(out, &ChannelMonitorHistoryEntry{Status: s, CheckedAt: at.Add(-time.Duration(i) * time.Minute)})
	}
	return out
}

func TestCardStatusFromHistory_OnlyLooksAtTheLatestThree(t *testing.T) {
	if got := cardStatusFromHistory(nil); got != "" {
		t.Errorf("no history should give empty status, got %q", got)
	}
	// 60 条时间线整体传进来：只有最近 3 条参与，第 4、5 条的硬失败不算。
	if got := cardStatusFromHistory(historyEntries(stOK, stOK, stOK, stError, stError)); got != stOK {
		t.Errorf("older failures must not count, got %q", got)
	}
	if got := cardStatusFromHistory(historyEntries(stError, stError, stOK, stOK)); got != stError {
		t.Errorf("two recent failures should be red, got %q", got)
	}
}

func TestBuildUserViewFromSummary_CardStatusUsesWindowButTimelineStaysRaw(t *testing.T) {
	m := &ChannelMonitor{ID: 1, Name: "n", Provider: MonitorProviderOpenAI, PrimaryModel: "gpt-x"}
	lat := 800
	// 最近一次探测是硬失败，但窗口里只有这 1 次 → 卡片降级；时间线每格仍是各自探测的原始状态。
	view := buildUserViewFromSummary(
		m,
		MonitorStatusSummary{PrimaryStatus: stError, PrimaryLatencyMs: &lat, Availability7d: 96.5},
		&ChannelMonitorLatest{Model: "gpt-x", Status: stError},
		historyEntries(stError, stOK, stOK),
	)
	if view.PrimaryStatus != stSlow {
		t.Errorf("card status = %q, want degraded", view.PrimaryStatus)
	}
	if view.PrimaryLatencyMs == nil || *view.PrimaryLatencyMs != 800 {
		t.Errorf("card latency should stay the latest probe's latency")
	}
	if view.Availability7d != 96.5 {
		t.Errorf("availability must not be touched, got %v", view.Availability7d)
	}
	if len(view.Timeline) != 3 {
		t.Fatalf("timeline length = %d, want 3 (one cell per probe)", len(view.Timeline))
	}
	for i, want := range []string{stError, stOK, stOK} {
		if view.Timeline[i].Status != want {
			t.Errorf("timeline[%d] = %q, want raw status %q", i, view.Timeline[i].Status, want)
		}
	}

	// 窗口内 2 次硬失败 → 卡片红。
	red := buildUserViewFromSummary(m, MonitorStatusSummary{PrimaryStatus: stError}, nil, historyEntries(stError, stFailed, stOK))
	if red.PrimaryStatus != stError {
		t.Errorf("two hard failures in window should be red, got %q", red.PrimaryStatus)
	}
}

func TestBuildUserViewFromSummary_NoTimelineFallsBackToLatest(t *testing.T) {
	m := &ChannelMonitor{ID: 1, PrimaryModel: "gpt-x"}
	view := buildUserViewFromSummary(m, MonitorStatusSummary{PrimaryStatus: stSlow}, nil, nil)
	if view.PrimaryStatus != stSlow {
		t.Errorf("without timeline the summary status should be kept, got %q", view.PrimaryStatus)
	}
	empty := buildUserViewFromSummary(m, MonitorStatusSummary{}, nil, nil)
	if empty.PrimaryStatus != "" {
		t.Errorf("no history at all should stay empty, got %q", empty.PrimaryStatus)
	}
}

// verdictRepoStub 只实现用户视图聚合用到的方法，其余方法嵌入 nil 接口，被调用即 panic。
type verdictRepoStub struct {
	ChannelMonitorRepository
	monitor    *ChannelMonitor
	history    []*ChannelMonitorHistoryEntry // 最新在前
	historyErr error
	limitSeen  int
}

func (r *verdictRepoStub) ListEnabled(context.Context) ([]*ChannelMonitor, error) {
	return []*ChannelMonitor{r.monitor}, nil
}

func (r *verdictRepoStub) GetByID(context.Context, int64) (*ChannelMonitor, error) {
	return r.monitor, nil
}

func (r *verdictRepoStub) ListLatestForMonitorIDs(context.Context, []int64) (map[int64][]*ChannelMonitorLatest, error) {
	return map[int64][]*ChannelMonitorLatest{r.monitor.ID: r.latest()}, nil
}

func (r *verdictRepoStub) ListLatestPerModel(context.Context, int64) ([]*ChannelMonitorLatest, error) {
	return r.latest(), nil
}

func (r *verdictRepoStub) latest() []*ChannelMonitorLatest {
	if len(r.history) == 0 {
		return nil
	}
	return []*ChannelMonitorLatest{{Model: r.monitor.PrimaryModel, Status: r.history[0].Status}}
}

func (r *verdictRepoStub) ComputeAvailabilityForMonitors(context.Context, []int64, int) (map[int64][]*ChannelMonitorAvailability, error) {
	return map[int64][]*ChannelMonitorAvailability{}, nil
}

func (r *verdictRepoStub) ComputeAvailability(context.Context, int64, int) ([]*ChannelMonitorAvailability, error) {
	return nil, nil
}

func (r *verdictRepoStub) ListRecentHistoryForMonitors(_ context.Context, _ []int64, _ map[int64]string, limit int) (map[int64][]*ChannelMonitorHistoryEntry, error) {
	r.limitSeen = limit
	if r.historyErr != nil {
		return nil, r.historyErr
	}
	rows := r.history
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return map[int64][]*ChannelMonitorHistoryEntry{r.monitor.ID: rows}, nil
}

func newVerdictRepo(statuses ...string) *verdictRepoStub {
	return &verdictRepoStub{
		monitor: &ChannelMonitor{ID: 9, Name: "n", Provider: MonitorProviderOpenAI, PrimaryModel: "gpt-x", ExtraModels: []string{"gpt-y"}, Enabled: true},
		history: historyEntries(statuses...),
	}
}

func TestListUserView_CardStatusFollowsTheWindow(t *testing.T) {
	cases := []struct {
		name     string
		statuses []string
		want     string
	}{
		{"全绿", []string{stOK, stOK, stOK}, stOK},
		{"有黄（慢）", []string{stSlow, stOK, stOK}, stSlow},
		{"1 次硬失败 → 黄", []string{stError, stOK, stOK}, stSlow},
		{"2 次硬失败 → 红", []string{stError, stOK, stFailed}, stError},
		{"全红", []string{stError, stError, stError}, stError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewChannelMonitorService(newVerdictRepo(tc.statuses...), nil)
			views, err := svc.ListUserView(context.Background())
			if err != nil {
				t.Fatalf("ListUserView: %v", err)
			}
			if len(views) != 1 || views[0].PrimaryStatus != tc.want {
				t.Fatalf("views = %+v, want primary status %q", views, tc.want)
			}
		})
	}
}

func TestBatchMonitorStatusSummary_AdminListKeepsLatestProbeStatus(t *testing.T) {
	// 后台管理列表看的是原始的最近一次探测，不套用 3 次窗口。
	repo := newVerdictRepo(stError, stOK, stOK)
	svc := NewChannelMonitorService(repo, nil)
	out := svc.BatchMonitorStatusSummary(context.Background(), []int64{9}, map[int64]string{9: "gpt-x"}, map[int64][]string{9: {"gpt-y"}})
	if got := out[9].PrimaryStatus; got != stError {
		t.Fatalf("admin list status = %q, want raw latest %q", got, stError)
	}
}

func TestGetUserDetail_PrimaryCardStatusMatchesCard(t *testing.T) {
	repo := newVerdictRepo(stError, stOK, stOK)
	svc := NewChannelMonitorService(repo, nil)

	detail, err := svc.GetUserDetail(context.Background(), 9)
	if err != nil {
		t.Fatalf("GetUserDetail: %v", err)
	}
	if len(detail.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(detail.Models))
	}
	primary := detail.Models[0]
	if primary.Model != "gpt-x" {
		t.Fatalf("primary model must come first, got %q", primary.Model)
	}
	if primary.CardStatus != stSlow {
		t.Errorf("primary card status = %q, want degraded", primary.CardStatus)
	}
	if primary.LatestStatus != stError {
		t.Errorf("latest status must stay the raw latest probe (admin view), got %q", primary.LatestStatus)
	}
	if detail.Models[1].CardStatus != "" {
		t.Errorf("extra models do not get a card status, got %q", detail.Models[1].CardStatus)
	}
	if repo.limitSeen != monitorVerdictWindow {
		t.Errorf("detail should only fetch the %d-probe window, asked for %d", monitorVerdictWindow, repo.limitSeen)
	}
}

func TestGetUserDetail_CardStatusErrorFallsBackQuietly(t *testing.T) {
	repo := newVerdictRepo(stOK)
	repo.historyErr = errors.New("db down")
	svc := NewChannelMonitorService(repo, nil)

	detail, err := svc.GetUserDetail(context.Background(), 9)
	if err != nil {
		t.Fatalf("a failed window query must not fail the detail request: %v", err)
	}
	if detail.Models[0].CardStatus != "" {
		t.Errorf("card status should be empty on query failure, got %q", detail.Models[0].CardStatus)
	}
	if detail.Models[0].LatestStatus != stOK {
		t.Errorf("latest status should still be filled, got %q", detail.Models[0].LatestStatus)
	}
}
