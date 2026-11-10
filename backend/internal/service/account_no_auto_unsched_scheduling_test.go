//go:build unit

package service

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 带「不自动暂停调度」标记的账号：上游失败之后，下一次选号仍然能选中它。
// 每个场景都配一个不带标记的对照账号，证明失败确实把普通账号踢出了调度（避免测试空转）。

const nostopTestModel = "gpt-6-luna"

// nostopWriteRepo behaves like the real repository WITHOUT the write-side gate: every automatic
// pause write lands on the stored account. That is exactly the state a legacy row / another
// (older) instance / a freshly toggled flag leaves behind, so selection must tolerate it.
type nostopWriteRepo struct {
	*mockAccountRepoForGemini
	mu   sync.Mutex
	rows []*Account

	rateLimitCalls  int
	overloadCalls   int
	tempCalls       int
	modelLimitCalls int
	setErrorCalls   int
}

func newNostopWriteRepo(accounts ...Account) *nostopWriteRepo {
	repo := &nostopWriteRepo{mockAccountRepoForGemini: &mockAccountRepoForGemini{}}
	for i := range accounts {
		acc := accounts[i]
		repo.rows = append(repo.rows, &acc)
	}
	return repo
}

func (r *nostopWriteRepo) find(id int64) *Account {
	for _, acc := range r.rows {
		if acc.ID == id {
			return acc
		}
	}
	return nil
}

func (r *nostopWriteRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if acc := r.find(id); acc != nil {
		cp := *acc
		return &cp, nil
	}
	return nil, ErrAccountNotFound
}

func (r *nostopWriteRepo) snapshot() []Account {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Account, 0, len(r.rows))
	for _, acc := range r.rows {
		out = append(out, *acc)
	}
	return out
}

func (r *nostopWriteRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, platform string) ([]Account, error) {
	var out []Account
	for _, acc := range r.snapshot() {
		if acc.Platform == platform {
			out = append(out, acc)
		}
	}
	return out, nil
}

func (r *nostopWriteRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(ctx, 0, platform)
}

func (r *nostopWriteRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(ctx, 0, platform)
}

func (r *nostopWriteRepo) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rateLimitCalls++
	if acc := r.find(id); acc != nil {
		acc.RateLimitResetAt = &resetAt
	}
	return nil
}

func (r *nostopWriteRepo) SetOverloaded(_ context.Context, id int64, until time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overloadCalls++
	if acc := r.find(id); acc != nil {
		acc.OverloadUntil = &until
	}
	return nil
}

func (r *nostopWriteRepo) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tempCalls++
	if acc := r.find(id); acc != nil {
		acc.TempUnschedulableUntil = &until
		acc.TempUnschedulableReason = reason
	}
	return nil
}

func (r *nostopWriteRepo) SetModelRateLimit(_ context.Context, id int64, scope string, resetAt time.Time, _ ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modelLimitCalls++
	acc := r.find(id)
	if acc == nil {
		return nil
	}
	extra := map[string]any{}
	for k, v := range acc.Extra {
		extra[k] = v
	}
	limits := map[string]any{}
	if existing, ok := extra[modelRateLimitsKey].(map[string]any); ok {
		for k, v := range existing {
			limits[k] = v
		}
	}
	limits[scope] = map[string]any{
		"rate_limited_at":     time.Now().UTC().Format(time.RFC3339),
		"rate_limit_reset_at": resetAt.UTC().Format(time.RFC3339),
	}
	extra[modelRateLimitsKey] = limits
	acc.Extra = extra
	return nil
}

func (r *nostopWriteRepo) SetError(_ context.Context, id int64, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setErrorCalls++
	if acc := r.find(id); acc != nil {
		acc.Status = StatusError
		acc.Schedulable = false
	}
	return nil
}

func (r *nostopWriteRepo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }

type nostopSettingRepoStub struct {
	SettingRepository
	values map[string]string
}

func (r *nostopSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

// nostopHarness wires the real RateLimitService + OpenAIGatewayService on top of nostopWriteRepo.
type nostopHarness struct {
	repo    *nostopWriteRepo
	svc     *OpenAIGatewayService
	rl      *RateLimitService
	groupID int64
}

func newNostopHarness(t *testing.T, advancedScheduler bool, accounts ...Account) *nostopHarness {
	t.Helper()
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	resetUpstream429TrackerForTest()
	t.Cleanup(func() {
		resetOpenAIAdvancedSchedulerSettingCacheForTest()
		resetUpstream429TrackerForTest()
		SetNoAutoUnschedConfigIDs(nil)
	})

	values := map[string]string{
		SettingKeyStreamTimeoutSettings: `{"enabled":true,"action":"temp_unsched","temp_unsched_minutes":5,"threshold_count":1,"threshold_window_minutes":10}`,
	}
	if advancedScheduler {
		values[openAIAdvancedSchedulerSettingKey] = "true"
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	repo := newNostopWriteRepo(accounts...)
	rl := NewRateLimitService(repo, nil, cfg, nil, nil)
	rl.SetSettingService(NewSettingService(&nostopSettingRepoStub{values: values}, cfg))
	svc := &OpenAIGatewayService{
		accountRepo:        repo,
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		rateLimitService:   rl,
	}
	rl.SetAccountRuntimeBlocker(svc)
	return &nostopHarness{repo: repo, svc: svc, rl: rl, groupID: 27}
}

func (h *nostopHarness) account(t *testing.T, id int64) *Account {
	t.Helper()
	acc, err := h.repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	return acc
}

// selectable reports whether the scheduler can hand out the account for the test model right now.
func (h *nostopHarness) selectable(id int64) bool {
	selection, _, err := h.svc.SelectAccountWithScheduler(context.Background(), &h.groupID, "", "", nostopTestModel, nil, OpenAIUpstreamTransportAny, false)
	if err != nil || selection == nil || selection.Account == nil {
		return false
	}
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
	return selection.Account.ID == id
}

func nostopAPIKeyAccount(id int64, flagged bool, mutate ...func(*Account)) Account {
	acc := Account{
		ID:          id,
		Name:        "nostop-test",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 5,
		Priority:    1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://upstream.example"},
		Extra:       map[string]any{},
	}
	if flagged {
		acc.Extra[AccountExtraKeyNoAutoUnsched] = true
	}
	for _, fn := range mutate {
		fn(&acc)
	}
	return acc
}

const (
	nostopFlaggedID = int64(3230)
	nostopControlID = int64(3231)
)

type nostopScenario struct {
	name string
	// trigger drives the real failure-handling code against the account.
	trigger func(t *testing.T, h *nostopHarness, accountID int64)
	// accountMutate customises both accounts (e.g. temp-unschedulable rules).
	accountMutate func(*Account)
	// recorded asserts the failure path really wrote the pause state for the control account.
	recorded func(t *testing.T, h *nostopHarness)
}

func nostopUpstreamError(t *testing.T, h *nostopHarness, accountID int64, status int, body string, times int) {
	t.Helper()
	for i := 0; i < times; i++ {
		acc := h.account(t, accountID)
		_ = h.svc.handleOpenAIAccountUpstreamError(context.Background(), acc, status, http.Header{}, []byte(body), nostopTestModel)
	}
}

func nostopScenarios() []nostopScenario {
	tempRule := func(acc *Account) {
		acc.Credentials["temp_unschedulable_enabled"] = true
		acc.Credentials["temp_unschedulable_rules"] = []any{
			map[string]any{
				"error_code":       float64(502),
				"keywords":         []any{"bad gateway from upstream"},
				"duration_minutes": float64(10),
				"description":      "502 rule",
			},
		}
	}
	return []nostopScenario{
		{
			name: "429_without_reset_time",
			trigger: func(t *testing.T, h *nostopHarness, id int64) {
				// 无 reset 时间的 429 只有近 30 秒 429 占比达标才会走 15s 默认冷却（线上故障就是这条路径）。
				for i := 0; i < 12; i++ {
					recordUpstream429Attempt(id)
				}
				nostopUpstreamError(t, h, id, http.StatusTooManyRequests, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`, 12)
			},
			recorded: func(t *testing.T, h *nostopHarness) { require.Positive(t, h.repo.rateLimitCalls) },
		},
		{
			name: "429_with_retry_after",
			trigger: func(t *testing.T, h *nostopHarness, id int64) {
				acc := h.account(t, id)
				headers := http.Header{}
				headers.Set("Retry-After", "120")
				_ = h.svc.handleOpenAIAccountUpstreamError(context.Background(), acc, http.StatusTooManyRequests, headers, []byte(`{"error":{"message":"rate limited"}}`), nostopTestModel)
			},
			recorded: func(t *testing.T, h *nostopHarness) { require.Positive(t, h.repo.rateLimitCalls) },
		},
		{
			name: "529_overloaded",
			trigger: func(t *testing.T, h *nostopHarness, id int64) {
				nostopUpstreamError(t, h, id, 529, `{"error":{"message":"overloaded"}}`, 1)
			},
			recorded: func(t *testing.T, h *nostopHarness) { require.Positive(t, h.repo.overloadCalls) },
		},
		{
			name: "401_unauthorized",
			trigger: func(t *testing.T, h *nostopHarness, id int64) {
				nostopUpstreamError(t, h, id, http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`, 1)
			},
			recorded: func(t *testing.T, h *nostopHarness) { require.Positive(t, h.repo.setErrorCalls) },
		},
		{
			name: "403_forbidden",
			trigger: func(t *testing.T, h *nostopHarness, id int64) {
				nostopUpstreamError(t, h, id, http.StatusForbidden, `{"error":{"message":"forbidden"}}`, 1)
			},
			recorded: func(t *testing.T, h *nostopHarness) { require.Positive(t, h.repo.setErrorCalls) },
		},
		{
			name:          "temp_unschedulable_rule_hit",
			accountMutate: tempRule,
			trigger: func(t *testing.T, h *nostopHarness, id int64) {
				nostopUpstreamError(t, h, id, http.StatusBadGateway, `bad gateway from upstream`, 1)
			},
			recorded: func(t *testing.T, h *nostopHarness) {
				// 规则命中后按账号级（或带模型时的单模型）冷却落库，两者必居其一。
				require.Positive(t, h.repo.tempCalls+h.repo.modelLimitCalls)
			},
		},
		{
			name: "stream_timeout",
			trigger: func(t *testing.T, h *nostopHarness, id int64) {
				acc := h.account(t, id)
				h.rl.HandleStreamTimeout(context.Background(), acc, nostopTestModel)
			},
			recorded: func(t *testing.T, h *nostopHarness) { require.Positive(t, h.repo.tempCalls) },
		},
		{
			name: "single_model_rate_limit_persisted",
			trigger: func(t *testing.T, h *nostopHarness, id int64) {
				require.NoError(t, h.repo.SetModelRateLimit(context.Background(), id, nostopTestModel, time.Now().Add(10*time.Minute), "test"))
			},
			recorded: func(t *testing.T, h *nostopHarness) { require.Positive(t, h.repo.modelLimitCalls) },
		},
		{
			name: "single_model_transient_cooldown",
			trigger: func(t *testing.T, h *nostopHarness, id int64) {
				nostopUpstreamError(t, h, id, http.StatusServiceUnavailable, `{"error":{"message":"service unavailable"}}`, 5)
			},
			recorded: func(t *testing.T, h *nostopHarness) {
				require.True(t, h.svc.isOpenAIAccountModelRuntimeBlocked(h.account(t, nostopControlID), nostopTestModel))
			},
		},
		{
			name: "runtime_block",
			trigger: func(t *testing.T, h *nostopHarness, id int64) {
				h.svc.BlockAccountScheduling(h.account(t, id), time.Now().Add(time.Minute), "429")
			},
			recorded: func(t *testing.T, h *nostopHarness) {
				require.True(t, h.svc.isOpenAIAccountRuntimeBlocked(h.account(t, nostopControlID)))
			},
		},
	}
}

func TestNoAutoUnsched_AfterUpstreamFailureNextSelectionStillPicksAccount(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		advancedName := "legacy_scheduler"
		if advanced {
			advancedName = "advanced_scheduler"
		}
		for _, sc := range nostopScenarios() {
			sc := sc
			t.Run(advancedName+"/"+sc.name+"/flagged_via_extra", func(t *testing.T) {
				mutate := sc.accountMutate
				if mutate == nil {
					mutate = func(*Account) {}
				}
				// 单账号分组：只有被标记的账号。
				h := newNostopHarness(t, advanced, nostopAPIKeyAccount(nostopFlaggedID, true, mutate))
				require.True(t, h.selectable(nostopFlaggedID), "precondition: selectable before any failure")

				sc.trigger(t, h, nostopFlaggedID)

				require.True(t, h.selectable(nostopFlaggedID), "flagged account must stay selectable after %s", sc.name)
			})
			t.Run(advancedName+"/"+sc.name+"/flagged_via_config_list", func(t *testing.T) {
				mutate := sc.accountMutate
				if mutate == nil {
					mutate = func(*Account) {}
				}
				h := newNostopHarness(t, advanced, nostopAPIKeyAccount(nostopFlaggedID, false, mutate))
				SetNoAutoUnschedConfigIDs([]int64{nostopFlaggedID})
				h.svc.cfg.Gateway.NoAutoUnschedAccountIDs = []int64{nostopFlaggedID}
				h.rl.cfg.Gateway.NoAutoUnschedAccountIDs = []int64{nostopFlaggedID}
				require.True(t, h.selectable(nostopFlaggedID))

				sc.trigger(t, h, nostopFlaggedID)

				require.True(t, h.selectable(nostopFlaggedID), "config-listed account must stay selectable after %s", sc.name)
			})
			t.Run(advancedName+"/"+sc.name+"/control_without_flag_is_still_paused", func(t *testing.T) {
				mutate := sc.accountMutate
				if mutate == nil {
					mutate = func(*Account) {}
				}
				h := newNostopHarness(t, advanced, nostopAPIKeyAccount(nostopControlID, false, mutate))
				require.True(t, h.selectable(nostopControlID), "precondition: selectable before any failure")

				sc.trigger(t, h, nostopControlID)

				sc.recorded(t, h)
				require.False(t, h.selectable(nostopControlID), "unflagged account must keep the old behaviour after %s", sc.name)
			})
		}
	}
}

// 单号分组里，除了被标记的账号还有别的号时，别的号照常参与；被标记账号不会因为自己「冷却」让位。
func TestNoAutoUnsched_FlaggedAccountCoexistsWithRegularAccounts(t *testing.T) {
	h := newNostopHarness(t, false,
		nostopAPIKeyAccount(nostopFlaggedID, true),
		nostopAPIKeyAccount(nostopControlID, false),
	)
	for _, id := range []int64{nostopFlaggedID, nostopControlID} {
		for i := 0; i < 12; i++ {
			recordUpstream429Attempt(id)
		}
		nostopUpstreamError(t, h, id, http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`, 12)
	}

	require.True(t, h.account(t, nostopFlaggedID).IsSchedulable())
	require.False(t, h.account(t, nostopControlID).IsSchedulable())
	require.True(t, h.selectable(nostopFlaggedID))
}

// 仍然生效的停调度条件：被标记的账号也不能绕过它们（人工停用、过期、配额都不是「因出错自动产生」）。
func TestNoAutoUnsched_ManualAndNonFailurePausesStillApply(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cases := []struct {
		name   string
		mutate func(*Account)
	}{
		{"manual_schedulable_false", func(a *Account) { a.Schedulable = false }},
		{"manual_status_disabled", func(a *Account) { a.Status = StatusDisabled }},
		{"manual_disabled_and_unschedulable", func(a *Account) { a.Status = StatusDisabled; a.Schedulable = false }},
		{"expired_auto_pause", func(a *Account) { a.AutoPauseOnExpired = true; a.ExpiresAt = &past }},
		{"apikey_quota_exceeded", func(a *Account) {
			a.Extra["quota_limit"] = float64(10)
			a.Extra["quota_used"] = float64(11)
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for _, advanced := range []bool{false, true} {
				h := newNostopHarness(t, advanced, nostopAPIKeyAccount(nostopFlaggedID, true, tc.mutate))
				require.False(t, h.account(t, nostopFlaggedID).IsSchedulable())
				require.False(t, h.selectable(nostopFlaggedID), "advanced=%v", advanced)
			}
		})
	}

	t.Run("model_not_supported_is_not_account_state", func(t *testing.T) {
		h := newNostopHarness(t, false, nostopAPIKeyAccount(nostopFlaggedID, true, func(a *Account) {
			a.Credentials["model_mapping"] = map[string]any{"gpt-5.1": "gpt-5.1"}
		}))
		require.False(t, h.selectable(nostopFlaggedID))
	})
}

// 出错自动产生的状态（含历史遗留的 status=error + schedulable=false）下，带标记账号仍能被选中；
// 同样状态的普通账号照旧选不中。
func TestNoAutoUnsched_FailureDrivenStatesStillSelectable(t *testing.T) {
	future := time.Now().Add(time.Hour)
	cases := []struct {
		name   string
		mutate func(*Account)
	}{
		{"status_error_with_schedulable_false", func(a *Account) { a.Status = StatusError; a.Schedulable = false }},
		{"status_error_only", func(a *Account) { a.Status = StatusError }},
		{"rate_limit_window", func(a *Account) { a.RateLimitResetAt = &future }},
		{"overload_window", func(a *Account) { a.OverloadUntil = &future }},
		{"temp_unschedulable_window", func(a *Account) { a.TempUnschedulableUntil = &future }},
		{"everything_automatic_at_once", func(a *Account) {
			a.Status = StatusError
			a.Schedulable = false
			a.RateLimitResetAt = &future
			a.OverloadUntil = &future
			a.TempUnschedulableUntil = &future
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for _, advanced := range []bool{false, true} {
				flagged := newNostopHarness(t, advanced, nostopAPIKeyAccount(nostopFlaggedID, true, tc.mutate))
				require.True(t, flagged.account(t, nostopFlaggedID).IsSchedulable())
				require.True(t, flagged.selectable(nostopFlaggedID), "advanced=%v", advanced)

				plain := newNostopHarness(t, advanced, nostopAPIKeyAccount(nostopControlID, false, tc.mutate))
				require.False(t, plain.account(t, nostopControlID).IsSchedulable())
				require.False(t, plain.selectable(nostopControlID), "advanced=%v", advanced)
			}
		})
	}
}

// 开关关闭时 IsSchedulable 与原逻辑完全一致（逐项对照原实现）。
func TestNoAutoUnsched_FlagOffMatchesLegacyIsSchedulable(t *testing.T) {
	legacy := func(a *Account) bool {
		if !a.IsActive() || !a.Schedulable {
			return false
		}
		now := time.Now()
		if a.AutoPauseOnExpired && a.ExpiresAt != nil && !now.Before(*a.ExpiresAt) {
			return false
		}
		if a.OverloadUntil != nil && now.Before(*a.OverloadUntil) {
			return false
		}
		if a.RateLimitResetAt != nil && now.Before(*a.RateLimitResetAt) {
			return false
		}
		if a.TempUnschedulableUntil != nil && now.Before(*a.TempUnschedulableUntil) {
			return false
		}
		if a.IsAPIKeyOrBedrock() && a.IsQuotaExceeded() {
			return false
		}
		return true
	}
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	for _, status := range []string{StatusActive, StatusDisabled, StatusError} {
		for _, schedulable := range []bool{true, false} {
			for _, mask := range []int{0, 1, 2, 4, 8, 16, 31} {
				acc := nostopAPIKeyAccount(1, false)
				acc.Status = status
				acc.Schedulable = schedulable
				if mask&1 != 0 {
					acc.RateLimitResetAt = &future
				}
				if mask&2 != 0 {
					acc.OverloadUntil = &future
				}
				if mask&4 != 0 {
					acc.TempUnschedulableUntil = &future
				}
				if mask&8 != 0 {
					acc.AutoPauseOnExpired = true
					acc.ExpiresAt = &past
				}
				if mask&16 != 0 {
					acc.Extra["quota_limit"] = float64(1)
					acc.Extra["quota_used"] = float64(2)
				}
				require.Equal(t, legacy(&acc), acc.IsSchedulable(), "status=%s schedulable=%v mask=%d", status, schedulable, mask)
			}
		}
	}
}

func TestNoAutoUnsched_IsSchedulableIgnoresAutomaticWindowsOnly(t *testing.T) {
	future := time.Now().Add(30 * time.Minute)
	base := func(flagged bool) *Account {
		acc := nostopAPIKeyAccount(1, flagged)
		return &acc
	}
	for _, flagged := range []bool{false, true} {
		acc := base(flagged)
		acc.RateLimitResetAt = &future
		require.Equal(t, flagged, acc.IsSchedulable(), "rate limit window flagged=%v", flagged)

		acc = base(flagged)
		acc.OverloadUntil = &future
		require.Equal(t, flagged, acc.IsSchedulable(), "overload window flagged=%v", flagged)

		acc = base(flagged)
		acc.TempUnschedulableUntil = &future
		require.Equal(t, flagged, acc.IsSchedulable(), "temp unschedulable window flagged=%v", flagged)
	}

	// 窗口字段本身仍保留可见，只是不影响选号。
	acc := base(true)
	acc.RateLimitResetAt = &future
	require.True(t, acc.IsRateLimited())
	require.True(t, acc.IsSchedulable())
}

func TestNoAutoUnsched_ModelRateLimitIgnoredOnlyForFlaggedAccount(t *testing.T) {
	reset := time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339)
	limits := map[string]any{
		nostopTestModel: map[string]any{"rate_limited_at": time.Now().UTC().Format(time.RFC3339), "rate_limit_reset_at": reset},
	}
	plain := nostopAPIKeyAccount(1, false, func(a *Account) { a.Extra[modelRateLimitsKey] = limits })
	flagged := nostopAPIKeyAccount(2, true, func(a *Account) { a.Extra[modelRateLimitsKey] = limits })

	require.False(t, plain.IsSchedulableForModel(nostopTestModel))
	require.Positive(t, plain.GetRateLimitRemainingTime(nostopTestModel))
	require.True(t, flagged.IsSchedulableForModel(nostopTestModel))
	require.Zero(t, flagged.GetRateLimitRemainingTime(nostopTestModel))
	require.False(t, shouldClearStickySession(&flagged, nostopTestModel))
	require.True(t, shouldClearStickySession(&plain, nostopTestModel))
}

func TestNoAutoUnsched_RuntimeBlockNotRecordedForFlaggedAccount(t *testing.T) {
	h := newNostopHarness(t, false,
		nostopAPIKeyAccount(nostopFlaggedID, true),
		nostopAPIKeyAccount(nostopControlID, false),
	)
	flagged := h.account(t, nostopFlaggedID)
	control := h.account(t, nostopControlID)

	h.svc.BlockAccountScheduling(flagged, time.Now().Add(time.Minute), "429")
	h.svc.BlockAccountScheduling(control, time.Now().Add(time.Minute), "429")

	_, flaggedStored := h.svc.openaiAccountRuntimeBlockUntil.Load(flagged.ID)
	_, controlStored := h.svc.openaiAccountRuntimeBlockUntil.Load(control.ID)
	require.False(t, flaggedStored, "no runtime block entry for exempt account")
	require.True(t, controlStored)

	// 开关在冷却写入之后才打开：读侧也立即放行。
	late := h.account(t, nostopControlID)
	require.True(t, h.svc.isOpenAIAccountRuntimeBlocked(late))
	late.Extra = map[string]any{AccountExtraKeyNoAutoUnsched: true}
	require.False(t, h.svc.isOpenAIAccountRuntimeBlocked(late))
}

func TestNoAutoUnsched_ProxyStreamQuarantineSkipsFlaggedAccount(t *testing.T) {
	proxyID := int64(9)
	flagged := nostopAPIKeyAccount(nostopFlaggedID, true, func(a *Account) { a.ProxyID = &proxyID })
	plain := nostopAPIKeyAccount(nostopControlID, false, func(a *Account) { a.ProxyID = &proxyID })
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	circuit := svc.getOpenAIProxyStreamCircuit()
	circuit.settings.collapseInterval = 0
	for i := 0; i < 3; i++ {
		circuit.recordFailure(proxyID, time.Now())
	}
	require.True(t, svc.isOpenAIProxyStreamQuarantined(context.Background(), &plain))
	require.False(t, svc.isOpenAIProxyStreamQuarantined(context.Background(), &flagged))
}

func TestNoAutoUnsched_TempUnschedRuleStillFailsOverTheCurrentRequest(t *testing.T) {
	rule := func(a *Account) {
		a.Credentials["temp_unschedulable_enabled"] = true
		a.Credentials["temp_unschedulable_rules"] = []any{
			map[string]any{"error_code": float64(502), "keywords": []any{"bad gateway from upstream"}, "duration_minutes": float64(10)},
		}
	}
	h := newNostopHarness(t, false, nostopAPIKeyAccount(nostopFlaggedID, true, rule))
	acc := h.account(t, nostopFlaggedID)

	// 返回 true = 本次请求应换号/重试（单号分组下耗尽后把上游错误原样交给用户），但不落任何暂停状态。
	require.True(t, h.svc.handleOpenAIAccountUpstreamError(context.Background(), acc, http.StatusBadGateway, http.Header{}, []byte(`bad gateway from upstream`), nostopTestModel))
	require.Zero(t, h.repo.tempCalls)
	require.Zero(t, h.repo.modelLimitCalls)
}

func TestNoAutoUnsched_AccountMatrix(t *testing.T) {
	SetNoAutoUnschedConfigIDs([]int64{7})
	t.Cleanup(func() { SetNoAutoUnschedConfigIDs(nil) })

	require.True(t, (&Account{ID: 7}).NoAutoUnsched())
	require.False(t, (&Account{ID: 8}).NoAutoUnsched())
	require.True(t, (&Account{ID: 8, Extra: map[string]any{AccountExtraKeyNoAutoUnsched: true}}).NoAutoUnsched())
	require.False(t, (&Account{ID: 8, Extra: map[string]any{AccountExtraKeyNoAutoUnsched: "true"}}).NoAutoUnsched())
	require.False(t, (*Account)(nil).NoAutoUnsched())

	SetNoAutoUnschedConfigIDs(nil)
	require.False(t, (&Account{ID: 7}).NoAutoUnsched())
	require.ElementsMatch(t, []int64{9, 10}, func() []int64 { SetNoAutoUnschedConfigIDs([]int64{9, 10, 0, -1}); return NoAutoUnschedConfigIDs() }())
}

func TestWithManualAccountState(t *testing.T) {
	require.False(t, IsManualAccountState(context.Background()))
	require.True(t, IsManualAccountState(WithManualAccountState(context.Background())))
}

type nostopCounterCache struct {
	mu     sync.Mutex
	counts map[int64]int64
}

func (c *nostopCounterCache) inc(id int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = map[int64]int64{}
	}
	c.counts[id]++
	return c.counts[id]
}

func (c *nostopCounterCache) IncrementTimeoutCount(_ context.Context, id int64, _ int) (int64, error) {
	return c.inc(id), nil
}
func (c *nostopCounterCache) GetTimeoutCount(context.Context, int64) (int64, error) { return 0, nil }
func (c *nostopCounterCache) ResetTimeoutCount(_ context.Context, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.counts, id)
	return nil
}
func (c *nostopCounterCache) GetTimeoutCountTTL(context.Context, int64) (time.Duration, error) {
	return 0, nil
}

type nostopOpenAI403Counter struct{ nostopCounterCache }

func (c *nostopOpenAI403Counter) IncrementOpenAI403Count(_ context.Context, id int64, _ int) (int64, error) {
	return c.inc(id), nil
}
func (c *nostopOpenAI403Counter) ResetOpenAI403Count(context.Context, int64) error { return nil }

// 开关打开的账号在短时间内连续失败很多次（远超任何现有窗口阈值：流超时 3 次/10 分钟、连续 403、
// 429 占比、单模型连续失败、代理流隔离 2 次/60 秒……）之后，下一次选号仍能选中它；
// 同样的失败洪水会把没开开关的对照账号踢出调度。
func TestNoAutoUnsched_ErrorBurstOverEveryWindowThresholdStillSelectable(t *testing.T) {
	const burst = 60
	fire := func(t *testing.T, h *nostopHarness, id int64) {
		proxy := int64(77)
		for i := 0; i < burst; i++ {
			recordUpstream429Attempt(id)
			for _, status := range []int{http.StatusTooManyRequests, 529, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusForbidden, http.StatusUnauthorized} {
				nostopUpstreamError(t, h, id, status, `{"error":{"message":"burst failure"}}`, 1)
			}
			h.rl.HandleStreamTimeout(context.Background(), h.account(t, id), nostopTestModel)
			h.svc.BlockAccountScheduling(h.account(t, id), time.Now().Add(time.Minute), "burst")
			if circuit := h.svc.getOpenAIProxyStreamCircuit(); circuit != nil {
				circuit.settings.collapseInterval = 0
				circuit.recordFailure(proxy, time.Now())
			}
		}
	}
	prepare := func(h *nostopHarness, advanced bool) {
		counter := &nostopCounterCache{}
		h.rl.SetTimeoutCounterCache(counter)
		h.rl.SetOpenAI403CounterCache(&nostopOpenAI403Counter{})
		values := map[string]string{
			SettingKeyStreamTimeoutSettings: `{"enabled":true,"action":"temp_unsched","temp_unsched_minutes":5,"threshold_count":3,"threshold_window_minutes":10}`,
		}
		if advanced {
			values[openAIAdvancedSchedulerSettingKey] = "true"
		}
		h.rl.settingService = NewSettingService(&nostopSettingRepoStub{values: values}, h.svc.cfg)
	}
	withProxy := func(a *Account) { p := int64(77); a.ProxyID = &p }

	for _, advanced := range []bool{false, true} {
		h := newNostopHarness(t, advanced, nostopAPIKeyAccount(nostopFlaggedID, true, withProxy))
		prepare(h, advanced)
		require.True(t, h.selectable(nostopFlaggedID))
		fire(t, h, nostopFlaggedID)
		require.True(t, h.selectable(nostopFlaggedID), "flagged account must stay selectable after %d-round error burst (advanced=%v)", burst, advanced)
		// 账号本体没有被打上任何自动暂停状态。
		stored := h.account(t, nostopFlaggedID)
		require.Equal(t, StatusActive, stored.Status)
		require.True(t, stored.Schedulable)

		control := newNostopHarness(t, advanced, nostopAPIKeyAccount(nostopControlID, false, withProxy))
		prepare(control, advanced)
		fire(t, control, nostopControlID)
		require.False(t, control.selectable(nostopControlID), "the same burst must still take an unflagged account out (advanced=%v)", advanced)
	}
}
