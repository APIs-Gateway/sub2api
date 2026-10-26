//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeInsertRecorder 记录 BulkInsertInitial 调用，实现 UserPlatformQuotaRepository port。
type fakeInsertRecorder struct {
	records []UserPlatformQuotaRecord
	err     error
	calls   int
}

func (f *fakeInsertRecorder) GetByUserPlatform(_ context.Context, _ int64, _ string) (*UserPlatformQuotaRecord, error) {
	return nil, nil
}

func (f *fakeInsertRecorder) BulkInsertInitial(_ context.Context, recs []UserPlatformQuotaRecord) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.records = append(f.records, recs...)
	return nil
}

func (f *fakeInsertRecorder) IncrementUsageWithReset(_ context.Context, _ int64, _ string, _ float64, _ time.Time) error {
	return nil
}

func (f *fakeInsertRecorder) ListByUser(_ context.Context, _ int64) ([]UserPlatformQuotaRecord, error) {
	return nil, nil
}

func (f *fakeInsertRecorder) UpsertForUser(_ context.Context, _ int64, _ []UserPlatformQuotaRecord) error {
	return nil
}

func (f *fakeInsertRecorder) ResetExpiredWindow(_ context.Context, _ int64, _ string, _ string, _ time.Time) error {
	return nil
}

func (f *fakeInsertRecorder) BatchSnapshotUsage(_ context.Context, _ []UserPlatformQuotaSnapshot, _ time.Time) error {
	return nil
}

// TestSnapshotPlatformQuotaDefaults_PassesToRepoBulkInsert 锁定不变式：只有至少配置了一档限额的
// 平台才建行；三档全空（含 nil 条目）的平台不进入 BulkInsertInitial。
func TestSnapshotPlatformQuotaDefaults_PassesToRepoBulkInsert(t *testing.T) {
	fakeRepo := &fakeInsertRecorder{}
	s := &AuthService{userPlatformQuotaRepo: fakeRepo}

	five := 5.0
	zero := 0.0
	plan := &signupGrantPlan{
		PlatformQuotas: map[string]*DefaultPlatformQuotaSetting{
			"anthropic":   {DailyLimitUSD: &five},
			"grok":        {MonthlyLimitUSD: &zero}, // 0 = 显式禁用，属于已配置
			"openai":      {},
			"gemini":      {},
			"antigravity": nil,
		},
	}
	if err := s.snapshotPlatformQuotaDefaults(context.Background(), 999, plan); err != nil {
		t.Fatal(err)
	}
	if len(fakeRepo.records) != 2 {
		t.Fatalf("expected 2 records (anthropic, grok), got %d: %+v", len(fakeRepo.records), fakeRepo.records)
	}
	byPlatform := map[string]UserPlatformQuotaRecord{}
	for _, r := range fakeRepo.records {
		if r.UserID != 999 {
			t.Errorf("unexpected user id %d", r.UserID)
		}
		byPlatform[r.Platform] = r
	}
	if r, ok := byPlatform["anthropic"]; !ok || r.DailyLimitUSD == nil || *r.DailyLimitUSD != 5 {
		t.Error("anthropic daily = 5 not snapshotted")
	}
	if r, ok := byPlatform["grok"]; !ok || r.MonthlyLimitUSD == nil || *r.MonthlyLimitUSD != 0 {
		t.Error("grok monthly = 0 (explicit disable) must be snapshotted")
	}
	for _, p := range []string{"openai", "gemini", "antigravity"} {
		if _, ok := byPlatform[p]; ok {
			t.Errorf("platform %q has no configured limit and must not get a row", p)
		}
	}
}

// TestSnapshotPlatformQuotaDefaults_AllUnlimitedSkipsInsert 锁定：没有任何平台配置限额时，
// 不调用 BulkInsertInitial（不产生全空的占位行）。
func TestSnapshotPlatformQuotaDefaults_AllUnlimitedSkipsInsert(t *testing.T) {
	fakeRepo := &fakeInsertRecorder{}
	s := &AuthService{userPlatformQuotaRepo: fakeRepo}

	plan := &signupGrantPlan{
		PlatformQuotas: map[string]*DefaultPlatformQuotaSetting{
			"anthropic": {},
			"openai":    {},
			"gemini":    nil,
		},
	}
	if err := s.snapshotPlatformQuotaDefaults(context.Background(), 999, plan); err != nil {
		t.Fatal(err)
	}
	if fakeRepo.calls != 0 {
		t.Fatalf("BulkInsertInitial must not be called when no platform has a configured limit, got %d calls", fakeRepo.calls)
	}
	if len(fakeRepo.records) != 0 {
		t.Errorf("expected no records, got %d", len(fakeRepo.records))
	}
}

func TestSnapshotPlatformQuotaDefaults_NilPlanIsNoop(t *testing.T) {
	fakeRepo := &fakeInsertRecorder{}
	s := &AuthService{userPlatformQuotaRepo: fakeRepo}
	if err := s.snapshotPlatformQuotaDefaults(context.Background(), 1, nil); err != nil {
		t.Errorf("nil plan should be noop, got %v", err)
	}
	if len(fakeRepo.records) != 0 {
		t.Errorf("expected no records, got %d", len(fakeRepo.records))
	}
}

func TestSnapshotPlatformQuotaDefaults_RepoErrorFailsOpen(t *testing.T) {
	fakeRepo := &fakeInsertRecorder{err: fmt.Errorf("db down")}
	s := &AuthService{userPlatformQuotaRepo: fakeRepo}
	five := 5.0
	plan := &signupGrantPlan{
		PlatformQuotas: map[string]*DefaultPlatformQuotaSetting{
			"anthropic": {DailyLimitUSD: &five},
		},
	}
	if err := s.snapshotPlatformQuotaDefaults(context.Background(), 1, plan); err != nil {
		t.Errorf("fail-open: expected nil even on repo error, got %v", err)
	}
}

func TestSnapshotPlatformQuotaDefaults_NilRepoIsNoop(t *testing.T) {
	s := &AuthService{userPlatformQuotaRepo: nil}
	five := 5.0
	plan := &signupGrantPlan{
		PlatformQuotas: map[string]*DefaultPlatformQuotaSetting{"a": {DailyLimitUSD: &five}},
	}
	if err := s.snapshotPlatformQuotaDefaults(context.Background(), 1, plan); err != nil {
		t.Errorf("nil repo should be noop, got %v", err)
	}
}

// resolveSignupGrantPlan 测试：依赖完整的 AuthService 构造，需要 SettingService（含 settingRepoStub）。
// settingRepoStub 已在 auth_service_register_test.go 中定义，同 package 可直接使用。
func TestResolveSignupGrantPlan_GlobalQuotaLoadedBeforeAuthSource(t *testing.T) {
	// 全局 quota JSON key（新格式）
	settings := map[string]string{
		SettingKeyRegistrationEnabled: "true",
		SettingKeyDefaultPlatformQuotas: `{
			"anthropic":   {"daily": 10, "weekly": 50, "monthly": 200},
			"openai":      {"daily": 5,  "weekly": 25, "monthly": 100},
			"gemini":      {"daily": 5,  "weekly": 25, "monthly": 100},
			"antigravity": {"daily": 5,  "weekly": 25, "monthly": 100}
		}`,
	}
	svc := newAuthService(nil, settings, nil, nil)
	plan := svc.resolveSignupGrantPlan(context.Background(), "email")
	if plan.PlatformQuotas == nil {
		t.Fatal("expected PlatformQuotas to be non-nil after loading global quota KVs")
	}
	q := plan.PlatformQuotas["anthropic"]
	if q == nil {
		t.Fatal("expected anthropic quota to be set")
	}
	if q.DailyLimitUSD == nil || *q.DailyLimitUSD != 10 {
		t.Errorf("expected anthropic daily=10, got %v", q.DailyLimitUSD)
	}
}

// TestResolveSignupGrantPlan_DisabledAuthSourceStillCarriesGlobalQuota 验证 P1 约束：
// !enabled 早退路径仍携带全局 quota（GetDefaultPlatformQuotas 在 ResolveAuthSourceGrantSettings 之前）。
func TestResolveSignupGrantPlan_DisabledAuthSourceStillCarriesGlobalQuota(t *testing.T) {
	settings := map[string]string{
		SettingKeyRegistrationEnabled: "true",
		// auth source 不配置（=> !enabled 路径）
		SettingKeyDefaultPlatformQuotas: `{"anthropic": {"daily": 10, "weekly": 50, "monthly": 200}}`,
	}
	svc := newAuthService(nil, settings, nil, nil)
	plan := svc.resolveSignupGrantPlan(context.Background(), "email")
	// !enabled 路径：plan.PlatformQuotas 应已含全局层（不是 nil）
	if plan.PlatformQuotas == nil {
		t.Fatal("P1 violated: PlatformQuotas is nil even with global quota KVs set")
	}
	// P1 核心断言：disabled auth source 路径不能丢失全局 quota
	if _, ok := plan.PlatformQuotas["anthropic"]; !ok {
		t.Error("P1 violated: disabled auth source path dropped global platform quota")
	}
}

// TestSnapshotPlatformQuotaDefaultsFiltersByWhitelist 钉住 2026-08-28 线上事故的那道防线。
//
// 当时建表迁移的 CHECK 约束不认 grok，而后台校验放行了它，于是注册事务里这批插入必然违反
// 约束。函数末尾那个「fail-open」在事务内是假的——PostgreSQL 一旦有语句失败就把整个事务标成
// aborted，后续语句一律被拒，所以真正的防线是根本不产生注定失败的语句：按权威白名单过滤。
func TestSnapshotPlatformQuotaDefaultsFiltersByWhitelist(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("白名单外的平台被跳过，白名单内的照常写入", func(t *testing.T) {
		repo := &fakeInsertRecorder{}
		s := &AuthService{userPlatformQuotaRepo: repo}
		ten := 10.0
		plan := &signupGrantPlan{PlatformQuotas: map[string]*DefaultPlatformQuotaSetting{
			PlatformGrok: {DailyLimitUSD: &ten},
			"wechat":     {},
		}}

		require.NoError(t, s.snapshotPlatformQuotaDefaults(ctx, 42, plan))
		require.Len(t, repo.records, 1, "只有白名单内的平台该写出去")
		require.Equal(t, PlatformGrok, repo.records[0].Platform)
		require.Equal(t, int64(42), repo.records[0].UserID)
		require.NotNil(t, repo.records[0].DailyLimitUSD)
		require.InDelta(t, ten, *repo.records[0].DailyLimitUSD, 1e-9)
	})

	t.Run("全部都是白名单外的平台时一次都不写库", func(t *testing.T) {
		// 过滤完为空还去 INSERT 一个空批次是没意义的，在事务里更没必要冒险
		repo := &fakeInsertRecorder{}
		s := &AuthService{userPlatformQuotaRepo: repo}
		plan := &signupGrantPlan{PlatformQuotas: map[string]*DefaultPlatformQuotaSetting{
			"wechat": {},
			"qq":     nil,
		}}

		require.NoError(t, s.snapshotPlatformQuotaDefaults(ctx, 42, plan))
		require.Zero(t, repo.calls, "没有可写的记录就不该调用仓储")
		require.Empty(t, repo.records)
	})
}
