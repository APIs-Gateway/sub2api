//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 编译期断言：stub 必须完整实现仓储接口，接口新增方法时这里会第一时间报错。
var _ AffiliateRepository = (*weeklyInviteAffRepoStub)(nil)

// weeklyInviteAffRepoStub 只实现邀请码周上限用到的两个方法，
// 其余接口方法一律 panic —— 这样一旦实现意外调用了别的仓储方法，测试会立刻炸出来，
// 而不是悄悄放行。
type weeklyInviteAffRepoStub struct {
	isAdmin     bool
	adminErr    error
	count       int
	countErr    error
	countCalls  int
	adminCalls  int
	lastSince   time.Time
	lastInviter int64
}

func (r *weeklyInviteAffRepoStub) IsUserAdmin(_ context.Context, userID int64) (bool, error) {
	r.adminCalls++
	r.lastInviter = userID
	if r.adminErr != nil {
		return false, r.adminErr
	}
	return r.isAdmin, nil
}

func (r *weeklyInviteAffRepoStub) CountInviteesRegisteredSince(_ context.Context, inviterID int64, since time.Time) (int, error) {
	r.countCalls++
	r.lastInviter = inviterID
	r.lastSince = since
	if r.countErr != nil {
		return 0, r.countErr
	}
	return r.count, nil
}

func (r *weeklyInviteAffRepoStub) EnsureUserAffiliate(context.Context, int64) (*AffiliateSummary, error) {
	panic("unexpected EnsureUserAffiliate call")
}
func (r *weeklyInviteAffRepoStub) GetAffiliateByCode(context.Context, string) (*AffiliateSummary, error) {
	panic("unexpected GetAffiliateByCode call")
}
func (r *weeklyInviteAffRepoStub) BindInviter(context.Context, int64, int64) (bool, error) {
	panic("unexpected BindInviter call")
}
func (r *weeklyInviteAffRepoStub) ListInvitees(context.Context, int64, int) ([]AffiliateInvitee, error) {
	panic("unexpected ListInvitees call")
}
func (r *weeklyInviteAffRepoStub) UpdateUserAffCode(context.Context, int64, string) error {
	panic("unexpected UpdateUserAffCode call")
}
func (r *weeklyInviteAffRepoStub) ResetUserAffCode(context.Context, int64) (string, error) {
	panic("unexpected ResetUserAffCode call")
}
func (r *weeklyInviteAffRepoStub) SetUserRebateRate(context.Context, int64, *float64) error {
	panic("unexpected SetUserRebateRate call")
}
func (r *weeklyInviteAffRepoStub) BatchSetUserRebateRate(context.Context, []int64, *float64) error {
	panic("unexpected BatchSetUserRebateRate call")
}
func (r *weeklyInviteAffRepoStub) ListUsersWithCustomSettings(context.Context, AffiliateAdminFilter) ([]AffiliateAdminEntry, int64, error) {
	panic("unexpected ListUsersWithCustomSettings call")
}
func (r *weeklyInviteAffRepoStub) ListAffiliateInviteRecords(context.Context, AffiliateRecordFilter) ([]AffiliateInviteRecord, int64, error) {
	panic("unexpected ListAffiliateInviteRecords call")
}
func (r *weeklyInviteAffRepoStub) ListAffiliateRebateRecords(context.Context, AffiliateRecordFilter) ([]AffiliateRebateRecord, int64, error) {
	panic("unexpected ListAffiliateRebateRecords call")
}
func (r *weeklyInviteAffRepoStub) GetAffiliateUserOverview(context.Context, int64) (*AffiliateUserOverview, error) {
	panic("unexpected GetAffiliateUserOverview call")
}

func weeklyLimitSettingService(limit string) *SettingService {
	return &SettingService{settingRepo: signupSourceSettingRepoStub{
		values: map[string]string{SettingKeyAffiliateWeeklyInviteLimit: limit},
	}}
}

// TestGetAffiliateWeeklyInviteLimit 覆盖读取与钳制。
// 关键约定：任何解析不出来的配置都回退到"不限"而不是"限死"，
// 因为一个写坏的配置不应该把整站的邀请注册全部卡住。
func TestGetAffiliateWeeklyInviteLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// 键不存在 → 默认不限
	missing := &SettingService{settingRepo: signupSourceSettingRepoStub{}}
	require.Equal(t, AffiliateWeeklyInviteLimitDefault, missing.GetAffiliateWeeklyInviteLimit(ctx))

	require.Equal(t, 5, weeklyLimitSettingService("5").GetAffiliateWeeklyInviteLimit(ctx))
	require.Equal(t, 0, weeklyLimitSettingService("0").GetAffiliateWeeklyInviteLimit(ctx))
	require.Equal(t, 7, weeklyLimitSettingService("  7  ").GetAffiliateWeeklyInviteLimit(ctx))

	// 负数 / 非数字 → 回退默认（不限），不能变成"限 0 人"把注册全挡死
	require.Equal(t, AffiliateWeeklyInviteLimitDefault, weeklyLimitSettingService("-3").GetAffiliateWeeklyInviteLimit(ctx))
	require.Equal(t, AffiliateWeeklyInviteLimitDefault, weeklyLimitSettingService("abc").GetAffiliateWeeklyInviteLimit(ctx))

	// 超出上限 → 钳到 Max
	require.Equal(t, AffiliateWeeklyInviteLimitMax, weeklyLimitSettingService("999999").GetAffiliateWeeklyInviteLimit(ctx))
}

// TestEnsureWeeklyInviteQuota_DisabledPaths 验证所有"不该查库"的短路分支：
// 依赖缺失、上限为 0、邀请人 ID 非法时都必须直接放行且不产生任何仓储调用。
func TestEnsureWeeklyInviteQuota_DisabledPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// settingService 为 nil（配置不全的环境）→ 放行，不 panic
	repo := &weeklyInviteAffRepoStub{}
	require.NoError(t, (&AffiliateService{repo: repo}).ensureWeeklyInviteQuota(ctx, 1))
	require.Zero(t, repo.adminCalls)
	require.Zero(t, repo.countCalls)

	// 上限为 0（默认）→ 放行，且不应查管理员或计数
	repo = &weeklyInviteAffRepoStub{}
	svc := &AffiliateService{repo: repo, settingService: weeklyLimitSettingService("0")}
	require.NoError(t, svc.ensureWeeklyInviteQuota(ctx, 42))
	require.Zero(t, repo.adminCalls)
	require.Zero(t, repo.countCalls)

	// 邀请人 ID 非法 → 放行
	repo = &weeklyInviteAffRepoStub{}
	svc = &AffiliateService{repo: repo, settingService: weeklyLimitSettingService("3")}
	require.NoError(t, svc.ensureWeeklyInviteQuota(ctx, 0))
	require.Zero(t, repo.adminCalls)
}

// TestEnsureWeeklyInviteQuota_AdminExempt 验证管理员豁免：
// 命中管理员就直接放行，连本周计数都不用查。
func TestEnsureWeeklyInviteQuota_AdminExempt(t *testing.T) {
	t.Parallel()

	repo := &weeklyInviteAffRepoStub{isAdmin: true, count: 999}
	svc := &AffiliateService{repo: repo, settingService: weeklyLimitSettingService("1")}

	require.NoError(t, svc.ensureWeeklyInviteQuota(context.Background(), 7))
	require.Equal(t, 1, repo.adminCalls)
	require.Zero(t, repo.countCalls, "admin should short-circuit before counting")
}

// TestEnsureWeeklyInviteQuota_LimitBoundary 验证边界：
// 已用数严格小于上限才放行，等于或超过一律拒绝。
func TestEnsureWeeklyInviteQuota_LimitBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// 已邀请 2 人、上限 3 → 还剩一个名额，放行
	under := &weeklyInviteAffRepoStub{count: 2}
	require.NoError(t, (&AffiliateService{
		repo:           under,
		settingService: weeklyLimitSettingService("3"),
	}).ensureWeeklyInviteQuota(ctx, 11))
	require.Equal(t, 1, under.countCalls)
	require.Equal(t, int64(11), under.lastInviter)

	// 已邀请 3 人、上限 3 → 正好用满，拒绝
	exact := &weeklyInviteAffRepoStub{count: 3}
	err := (&AffiliateService{
		repo:           exact,
		settingService: weeklyLimitSettingService("3"),
	}).ensureWeeklyInviteQuota(ctx, 11)
	require.ErrorIs(t, err, ErrAffiliateWeeklyInviteLimitReached)

	// 历史数据导致超额 → 同样拒绝
	over := &weeklyInviteAffRepoStub{count: 10}
	err = (&AffiliateService{
		repo:           over,
		settingService: weeklyLimitSettingService("3"),
	}).ensureWeeklyInviteQuota(ctx, 11)
	require.ErrorIs(t, err, ErrAffiliateWeeklyInviteLimitReached)
}

// TestEnsureWeeklyInviteQuota_UsesCalendarWeekStart 验证统计窗口取的是
// 自然周起点（周一 00:00），与订阅计费的周窗口同口径，而不是"最近 7 天"滚动窗。
func TestEnsureWeeklyInviteQuota_UsesCalendarWeekStart(t *testing.T) {
	t.Parallel()

	repo := &weeklyInviteAffRepoStub{count: 0}
	svc := &AffiliateService{repo: repo, settingService: weeklyLimitSettingService("5")}
	require.NoError(t, svc.ensureWeeklyInviteQuota(context.Background(), 3))

	require.Equal(t, 1, repo.countCalls)
	since := repo.lastSince
	require.Equal(t, time.Monday, since.Weekday(), "week must start on Monday")
	require.Zero(t, since.Hour())
	require.Zero(t, since.Minute())
	require.Zero(t, since.Second())
	require.False(t, since.After(time.Now()), "week start must not be in the future")
}

// TestEnsureWeeklyInviteQuota_ErrorsPropagate 验证仓储错误向上传播而不是被吞掉。
// 查不出配额时宁可让注册失败，也不能默认放行——否则数据库抖动就成了绕过上限的口子。
func TestEnsureWeeklyInviteQuota_ErrorsPropagate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sentinel := errors.New("db down")

	adminFail := &weeklyInviteAffRepoStub{adminErr: sentinel}
	require.ErrorIs(t, (&AffiliateService{
		repo:           adminFail,
		settingService: weeklyLimitSettingService("2"),
	}).ensureWeeklyInviteQuota(ctx, 5), sentinel)

	countFail := &weeklyInviteAffRepoStub{countErr: sentinel}
	require.ErrorIs(t, (&AffiliateService{
		repo:           countFail,
		settingService: weeklyLimitSettingService("2"),
	}).ensureWeeklyInviteQuota(ctx, 5), sentinel)
}
