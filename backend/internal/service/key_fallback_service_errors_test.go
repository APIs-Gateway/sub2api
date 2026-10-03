//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

// 本文件补全 KeyFallbackService 的依赖失败路径：任何一个依赖报错，都必须原样向上返回，
// 不能吞掉、不能误报成 404，也不能留下半截写入。

var errFBBoom = errors.New("fallback test: injected failure")

// --- 可注入错误的测试替身（包在 key_fallback_service_test.go 的替身之上） ---

type fbFailKeys struct {
	*fbKeys
	getErr, listErr, availErr, ratesErr error
	// nilKey 让 GetByID 返回 (nil, nil)，模拟仓储返回空指针。
	nilKey bool
}

func (k *fbFailKeys) GetByID(ctx context.Context, id int64) (*APIKey, error) {
	if k.getErr != nil {
		return nil, k.getErr
	}
	if k.nilKey {
		return nil, nil
	}
	return k.fbKeys.GetByID(ctx, id)
}

func (k *fbFailKeys) List(ctx context.Context, userID int64, params pagination.PaginationParams, filters APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
	if k.listErr != nil {
		return nil, nil, k.listErr
	}
	return k.fbKeys.List(ctx, userID, params, filters)
}

func (k *fbFailKeys) GetAvailableGroups(ctx context.Context, userID int64) ([]Group, error) {
	if k.availErr != nil {
		return nil, k.availErr
	}
	return k.fbKeys.GetAvailableGroups(ctx, userID)
}

func (k *fbFailKeys) GetUserGroupRates(ctx context.Context, userID int64) (map[int64]float64, error) {
	if k.ratesErr != nil {
		return nil, k.ratesErr
	}
	return k.fbKeys.GetUserGroupRates(ctx, userID)
}

type fbFailUsers struct {
	inner *fbUsers
	err   error
}

func (u *fbFailUsers) GetByID(ctx context.Context, id int64) (*User, error) {
	if u.err != nil {
		return nil, u.err
	}
	return u.inner.GetByID(ctx, id)
}

// fbFailGroups 按分组 ID 注入错误。
type fbFailGroups struct {
	inner keyFallbackGroups
	errs  map[int64]error
}

func (g *fbFailGroups) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	if err, ok := g.errs[id]; ok {
		return nil, err
	}
	return g.inner.GetByIDLite(ctx, id)
}

type fbFailRoutes struct {
	GroupRouteService
	userErr, hiddenErr, resolveErr error
}

func (r *fbFailRoutes) GetUserChain(ctx context.Context, keyID int64) ([]RouteItem, error) {
	if r.userErr != nil {
		return nil, r.userErr
	}
	return r.GroupRouteService.GetUserChain(ctx, keyID)
}

func (r *fbFailRoutes) GetHiddenChain(ctx context.Context, keyID int64) (head, tail []RouteItem, err error) {
	if r.hiddenErr != nil {
		return nil, nil, r.hiddenErr
	}
	return r.GroupRouteService.GetHiddenChain(ctx, keyID)
}

func (r *fbFailRoutes) ResolveEffectiveChain(ctx context.Context, key *APIKey, user *User, opt ResolveOptions) (Chain, error) {
	if r.resolveErr != nil {
		return Chain{}, r.resolveErr
	}
	return r.GroupRouteService.ResolveEffectiveChain(ctx, key, user, opt)
}

type fbFailExtras struct {
	APIKeyGroupRouteExtras
	err error
}

func (e *fbFailExtras) ListByGroup(ctx context.Context, groupID int64, limit int) ([]GroupRouteRef, error) {
	if e.err != nil {
		return nil, e.err
	}
	return e.APIKeyGroupRouteExtras.ListByGroup(ctx, groupID, limit)
}

// fbFailEnv 是 newFBEnv 的变体：所有依赖都套上了可注入错误的包装，默认不报错。
type fbFailEnv struct {
	*fbEnv
	failKeys   *fbFailKeys
	failUsers  *fbFailUsers
	failGroups *fbFailGroups
	failRoutes *fbFailRoutes
}

func newFBFailEnv() *fbFailEnv {
	env := newFBEnv(true)
	f := &fbFailEnv{
		fbEnv:      env,
		failKeys:   &fbFailKeys{fbKeys: env.keys},
		failUsers:  &fbFailUsers{inner: env.users},
		failGroups: &fbFailGroups{inner: env.route.groups, errs: map[int64]error{}},
		failRoutes: &fbFailRoutes{GroupRouteService: env.route.svc},
	}
	env.svc.keys = f.failKeys
	env.svc.users = f.failUsers
	env.svc.groups = f.failGroups
	env.svc.routes = f.failRoutes
	return f
}

// --- 构造函数 ---

func TestNewKeyFallbackService_SettingsAreOptional(t *testing.T) {
	svc := NewKeyFallbackService(nil, nil, nil, nil, nil, nil, nil)
	require.NotNil(t, svc)
	require.Nil(t, svc.settings)
	require.False(t, svc.enabled(context.Background()), "没有注入设置时视为总开关关闭")

	withSettings := NewKeyFallbackService(nil, nil, nil, nil, nil, nil, &SettingService{})
	require.NotNil(t, withSettings)
	require.NotNil(t, withSettings.settings)
}

// --- 用户端：依赖失败 ---

func TestKeyFallbackErrors_UserViewPropagatesDependencyFailures(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(f *fbFailEnv)
	}{
		{"load key", func(f *fbFailEnv) { f.failKeys.getErr = errFBBoom }},
		{"load primary group", func(f *fbFailEnv) { f.failGroups.errs[1] = errFBBoom }},
		{"load user", func(f *fbFailEnv) { f.failUsers.err = errFBBoom }},
		{"load group rates", func(f *fbFailEnv) { f.failKeys.ratesErr = errFBBoom }},
		{"load user chain", func(f *fbFailEnv) { f.failRoutes.userErr = errFBBoom }},
		{"load available groups", func(f *fbFailEnv) { f.failKeys.availErr = errFBBoom }},
		{"load chain item group", func(f *fbFailEnv) {
			f.seedUser(100, 2, 0)
			f.failGroups.errs[2] = errFBBoom
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFBFailEnv()
			tc.setup(f)
			view, err := f.svc.GetUserChain(ctx, 1, 100, "")
			require.ErrorIs(t, err, errFBBoom)
			require.Nil(t, view)
		})
	}
}

func TestKeyFallbackErrors_ReplaceUserChainLoadUserFailsWithoutWriting(t *testing.T) {
	f := newFBFailEnv()
	f.failUsers.err = errFBBoom

	_, err := f.svc.ReplaceUserChain(context.Background(), 1, 100, []int64{2}, "")
	require.ErrorIs(t, err, errFBBoom)
	require.Empty(t, f.route.repo.bySource(100, RouteSourceUser))
}

func TestKeyFallbackErrors_UserChainSkipsItemsWhoseGroupIsGone(t *testing.T) {
	env := newFBEnv(true)
	env.seedUser(100, 999, 0) // 分组刚被删除：并发窗口里读到的残留项
	env.seedUser(100, 2, 1)

	view, err := env.svc.GetUserChain(context.Background(), 1, 100, "")
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, fbItemIDs(view.Items))
	require.Equal(t, 1, view.Items[1].Position, "被跳过的项不占位置")
}

func TestKeyFallbackErrors_NoPricerDegradesToEmptyPrices(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	env.seedUser(100, 2, 0)
	env.svc.pricer = nil

	view, err := env.svc.GetUserChain(ctx, 1, 100, "")
	require.NoError(t, err)
	require.Empty(t, view.ReferenceModel)
	require.Equal(t, []int64{1, 2}, fbItemIDs(view.Items))
	for _, it := range view.Items {
		require.NotNil(t, it.ReferencePrice)
		require.False(t, it.ReferencePrice.Priced)
	}
	for _, a := range view.Available {
		require.False(t, a.ReferencePrice.Priced)
	}

	// 请求里带的参考模型仍然生效。
	view, err = env.svc.GetUserChain(ctx, 1, 100, "gpt-5.4")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", view.ReferenceModel)
}

// --- 批量接口：依赖失败 ---

func TestKeyFallbackErrors_ListUserChainsPropagatesDependencyFailures(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(f *fbFailEnv)
	}{
		{"list keys", func(f *fbFailEnv) { f.failKeys.listErr = errFBBoom }},
		{"load user", func(f *fbFailEnv) { f.failUsers.err = errFBBoom }},
		{"load group rates", func(f *fbFailEnv) { f.failKeys.ratesErr = errFBBoom }},
		{"load available groups", func(f *fbFailEnv) { f.failKeys.availErr = errFBBoom }},
		{"load primary group", func(f *fbFailEnv) { f.failGroups.errs[1] = errFBBoom }},
		{"load user chain", func(f *fbFailEnv) { f.failRoutes.userErr = errFBBoom }},
		{"load chain item group", func(f *fbFailEnv) {
			f.seedUser(100, 2, 0)
			f.failGroups.errs[2] = errFBBoom
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFBFailEnv()
			tc.setup(f)
			overview, err := f.svc.ListUserChains(ctx, 1)
			require.ErrorIs(t, err, errFBBoom)
			require.Nil(t, overview)
		})
	}
}

func TestKeyFallbackErrors_ListUserChainsSkipsKeysWhoseGroupIsGone(t *testing.T) {
	env := newFBEnv(true)
	// 两把 Key 指向同一个已不存在的分组：第一把查库后缓存「不存在」，第二把直接命中缓存。
	env.keys.keys[500] = &APIKey{ID: 500, UserID: 1, Name: "k500", GroupID: int64Ptr(999)}
	env.keys.keys[501] = &APIKey{ID: 501, UserID: 1, Name: "k501", GroupID: int64Ptr(999)}

	overview, err := env.svc.ListUserChains(context.Background(), 1)
	require.NoError(t, err)
	require.NotEmpty(t, overview.Platforms, "其余正常的 Key 照常列出")
	for _, section := range overview.Platforms {
		for _, key := range section.Keys {
			require.NotEqual(t, int64(500), key.KeyID)
			require.NotEqual(t, int64(501), key.KeyID)
		}
	}
}

// --- 纯函数 ---

func TestKeyFallbackHelpers_PlatformSortRank(t *testing.T) {
	order := []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformGrok, "some-other-platform"}
	for i, platform := range order {
		require.Equal(t, i, platformSortRank(platform), platform)
	}
}

func TestKeyFallbackHelpers_Statuses(t *testing.T) {
	require.Equal(t, KeyFallbackStatusUnavailable, groupStatus(nil))
	require.Equal(t, KeyFallbackStatusDisabled, groupStatus(&Group{Status: StatusDisabled}))
	require.Equal(t, KeyFallbackStatusActive, groupStatus(&Group{Status: StatusActive}))

	status, usable := fallbackItemStatus(nil, &Group{Platform: PlatformOpenAI}, &User{ID: 1})
	require.Equal(t, KeyFallbackStatusUnavailable, status)
	require.False(t, usable)
}

// --- 管理端：依赖失败 ---

func TestKeyFallbackErrors_AdminGetChainPropagatesDependencyFailures(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(f *fbFailEnv)
	}{
		{"load key", func(f *fbFailEnv) { f.failKeys.getErr = errFBBoom }},
		{"load key owner", func(f *fbFailEnv) { f.failUsers.err = errFBBoom }},
		{"load user chain", func(f *fbFailEnv) { f.failRoutes.userErr = errFBBoom }},
		{"load hidden chain", func(f *fbFailEnv) { f.failRoutes.hiddenErr = errFBBoom }},
		{"load primary group", func(f *fbFailEnv) { f.failGroups.errs[1] = errFBBoom }},
		{"resolve effective chain", func(f *fbFailEnv) { f.failRoutes.resolveErr = errFBBoom }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFBFailEnv()
			tc.setup(f)
			view, err := f.svc.AdminGetChain(ctx, 100)
			require.ErrorIs(t, err, errFBBoom)
			require.Nil(t, view)
		})
	}
}

func TestKeyFallbackErrors_AdminTreatsNilKeyAsNotFound(t *testing.T) {
	ctx := context.Background()
	f := newFBFailEnv()
	f.failKeys.nilKey = true

	_, err := f.svc.AdminGetChain(ctx, 100)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	_, err = f.svc.AdminReplaceHiddenChain(ctx, 1, 100, nil, nil, "")
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
}

func TestKeyFallbackErrors_AdminReplaceHiddenChainLoadKeyFails(t *testing.T) {
	f := newFBFailEnv()
	f.failKeys.getErr = errFBBoom

	_, err := f.svc.AdminReplaceHiddenChain(context.Background(), 1, 100, []int64{5}, nil, "pinned")
	require.ErrorIs(t, err, errFBBoom)
	require.Empty(t, f.route.repo.bySource(100, RouteSourceAdmin), "读 Key 失败时不写隐藏链")
}

func TestKeyFallbackErrors_AdminRoutesByGroupDependencyFailures(t *testing.T) {
	ctx := context.Background()
	f := newFBFailEnv()

	f.failGroups.errs[5] = errFBBoom
	_, err := f.svc.AdminRoutesByGroup(ctx, 5)
	require.ErrorIs(t, err, errFBBoom, "分组读取失败不能误报成「分组不存在」")

	f.svc.extras = &fbFailExtras{APIKeyGroupRouteExtras: f.extras, err: errFBBoom}
	_, err = f.svc.AdminRoutesByGroup(ctx, 2)
	require.ErrorIs(t, err, errFBBoom)
}

func TestKeyFallbackAdmin_ItemNamesAndTimestamps(t *testing.T) {
	env := newFBEnv(true)
	updated := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	env.route.repo.seed(
		RouteItem{APIKeyID: 100, GroupID: 2, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail, Position: 0, UpdatedAt: updated},
		RouteItem{APIKeyID: 100, GroupID: 999, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail, Position: 1},
	)

	view, err := env.svc.AdminGetChain(context.Background(), 100)
	require.NoError(t, err)
	require.Len(t, view.UserItems, 2)

	require.Equal(t, "b", view.UserItems[0].Name)
	require.NotNil(t, view.UserItems[0].UpdatedAt)
	require.True(t, view.UserItems[0].UpdatedAt.Equal(updated))

	// 分组已不存在：名字留空，不报错；没有更新时间就不输出。
	require.Empty(t, view.UserItems[1].Name)
	require.Nil(t, view.UserItems[1].UpdatedAt)
}
