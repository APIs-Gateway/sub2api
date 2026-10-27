//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

// --- 测试替身 ---

// fbKeys 实现 keyFallbackKeys；可用分组与专属授权按 User.CanBindGroup 计算，
// 与真实 APIKeyService.GetAvailableGroups 同一个判定来源。
type fbKeys struct {
	keys   map[int64]*APIKey
	users  map[int64]*User
	groups map[int64]*Group
	rates  map[int64]map[int64]float64
}

func (k *fbKeys) GetByID(_ context.Context, id int64) (*APIKey, error) {
	key, ok := k.keys[id]
	if !ok {
		return nil, fmt.Errorf("get api key: %w", ErrAPIKeyNotFound)
	}
	cp := *key
	return &cp, nil
}

func (k *fbKeys) List(_ context.Context, userID int64, _ pagination.PaginationParams, _ APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
	var out []APIKey
	for _, key := range k.keys {
		if key.UserID == userID {
			out = append(out, *key)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, &pagination.PaginationResult{Total: int64(len(out))}, nil
}

func (k *fbKeys) GetAvailableGroups(_ context.Context, userID int64) ([]Group, error) {
	user := k.users[userID]
	var out []Group
	for _, g := range k.groups {
		if g.IsActive() && user.CanBindGroup(g.ID, g.IsExclusive) {
			out = append(out, *g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (k *fbKeys) GetUserGroupRates(_ context.Context, userID int64) (map[int64]float64, error) {
	return k.rates[userID], nil
}

type fbUsers struct{ users map[int64]*User }

func (u *fbUsers) GetByID(_ context.Context, id int64) (*User, error) {
	user, ok := u.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	cp := *user
	return &cp, nil
}

type fbPriceCall struct {
	model                   string
	primaryID, groupID, uid int64
}

type fbPricer struct {
	models map[string]string
	prices map[int64]KeyEditorReferencePrice
	calls  []fbPriceCall
}

func (p *fbPricer) ReferenceModel(_ context.Context, platform, override string) string {
	if override != "" {
		return override
	}
	return p.models[platform]
}
func (p *fbPricer) ReferenceModels(context.Context) map[string]string { return p.models }
func (p *fbPricer) SetReferenceModels(_ context.Context, m map[string]string) (map[string]string, error) {
	for k, v := range m {
		p.models[k] = v
	}
	return p.models, nil
}
func (p *fbPricer) ReferencePrice(_ context.Context, model string, primaryID, groupID, userID int64) KeyEditorReferencePrice {
	p.calls = append(p.calls, fbPriceCall{model, primaryID, groupID, userID})
	return p.prices[groupID]
}

type fbExtras struct {
	refs       []GroupRouteRef
	listLimit  int
	deletedKey []int64
	deleteErr  error
}

func (e *fbExtras) ListByGroup(_ context.Context, _ int64, limit int) ([]GroupRouteRef, error) {
	e.listLimit = limit
	if len(e.refs) > limit {
		return e.refs[:limit], nil
	}
	return e.refs, nil
}
func (e *fbExtras) DeleteByKey(_ context.Context, keyID int64) error {
	e.deletedKey = append(e.deletedKey, keyID)
	return e.deleteErr
}

type fbEnv struct {
	route  *routeTestEnv
	svc    *KeyFallbackService
	keys   *fbKeys
	users  *fbUsers
	pricer *fbPricer
	extras *fbExtras
}

// newFBEnv 搭一个贴近真实的环境：真实的 groupRouteService（PR1）+ 内存仓储 + 本文件的替身。
// 用户 1：Key 100/101（主分组 1，openai）、Key 300（未绑分组）、Key 400（主分组 22，anthropic）；用户 2：Key 200。
func newFBEnv(enabled bool) *fbEnv {
	route := newRouteTestEnv(enabled)
	users := map[int64]*User{1: {ID: 1}, 2: {ID: 2}}
	keys := &fbKeys{
		keys: map[int64]*APIKey{
			100: {ID: 100, UserID: 1, Name: "k100", GroupID: int64Ptr(1)},
			101: {ID: 101, UserID: 1, Name: "k101", GroupID: int64Ptr(1)},
			200: {ID: 200, UserID: 2, Name: "k200", GroupID: int64Ptr(1)},
			300: {ID: 300, UserID: 1, Name: "k300"},
			400: {ID: 400, UserID: 1, Name: "k400", GroupID: int64Ptr(22)},
		},
		users:  users,
		groups: route.groups.groups,
		rates:  map[int64]map[int64]float64{},
	}
	route.repo.primary = map[int64]int64{100: 1, 101: 1, 200: 1, 400: 22}
	env := &fbEnv{
		route: route,
		keys:  keys,
		users: &fbUsers{users: users},
		pricer: &fbPricer{
			models: map[string]string{PlatformOpenAI: "gpt-5.5", PlatformAnthropic: "claude-sonnet-4"},
			prices: map[int64]KeyEditorReferencePrice{},
		},
		extras: &fbExtras{},
	}
	env.svc = &KeyFallbackService{
		keys:     keys,
		users:    env.users,
		groups:   route.groups,
		routes:   route.svc,
		extras:   env.extras,
		pricer:   env.pricer,
		settings: routeTestSettings{enabled: enabled},
	}
	return env
}

func (e *fbEnv) seedAdmin(keyID, groupID int64, placement string, pos int, note string) {
	e.route.repo.seed(RouteItem{APIKeyID: keyID, GroupID: groupID, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: placement, Position: pos, Note: note})
}

func (e *fbEnv) seedUser(keyID, groupID int64, pos int) {
	e.route.repo.seed(RouteItem{APIKeyID: keyID, GroupID: groupID, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail, Position: pos})
}

func fbItemIDs(items []KeyFallbackItemView) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.GroupID)
	}
	return out
}

func fbAvailableIDs(items []KeyFallbackAvailableView) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.GroupID)
	}
	return out
}

func fbJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

// --- 用户端：归属、未绑分组 ---

func TestKeyFallback_IDOR_OtherUsersKeyAndMissingKeyAreIndistinguishable(t *testing.T) {
	env := newFBEnv(true)
	ctx := context.Background()
	env.seedUser(200, 2, 0) // 用户 2 自己的链

	// 用户 1 访问用户 2 的 Key（存在）与根本不存在的 Key：必须是同一个错误。
	_, errForeign := env.svc.GetUserChain(ctx, 1, 200, "")
	_, errMissing := env.svc.GetUserChain(ctx, 1, 9999, "")
	require.Equal(t, "API_KEY_NOT_FOUND", routeCode(t, errForeign))
	require.Equal(t, errMissing, errForeign)
	require.ErrorIs(t, errForeign, ErrAPIKeyNotFound)

	// 写同理，并且不得动到别人的链。
	_, err := env.svc.ReplaceUserChain(ctx, 1, 200, []int64{3}, "")
	require.Equal(t, errMissing, err)
	_, err = env.svc.ReplaceUserChain(ctx, 1, 9999, []int64{3}, "")
	require.Equal(t, errMissing, err)
	require.Equal(t, []int64{2}, routeGroupIDs(env.route.repo.bySource(200, RouteSourceUser)))

	// 即使把请求体写成会触发其它校验错误的内容，也先返回 404（不泄露 Key 是否存在）。
	_, err = env.svc.ReplaceUserChain(ctx, 1, 200, []int64{1, 1}, "")
	require.Equal(t, errMissing, err)

	// 参考模型名的校验只看请求本身，不泄露任何 Key 信息，所以可以排在归属校验之前。
	_, err = env.svc.GetUserChain(ctx, 1, 200, "bad model")
	require.Equal(t, "FALLBACK_INVALID_MODEL", routeCode(t, err))
}

func TestKeyFallback_KeyWithoutGroupIsRejected(t *testing.T) {
	env := newFBEnv(true)
	ctx := context.Background()

	_, err := env.svc.GetUserChain(ctx, 1, 300, "")
	require.Equal(t, "FALLBACK_KEY_NOT_GROUPED", routeCode(t, err))
	_, err = env.svc.ReplaceUserChain(ctx, 1, 300, []int64{2}, "")
	require.Equal(t, "FALLBACK_KEY_NOT_GROUPED", routeCode(t, err))
	require.Empty(t, env.route.repo.bySource(300, RouteSourceUser))

	// 批量接口不列出未绑分组的 Key。
	overview, err := env.svc.ListUserChains(ctx, 1)
	require.NoError(t, err)
	for _, section := range overview.Platforms {
		for _, key := range section.Keys {
			require.NotEqual(t, int64(300), key.KeyID)
		}
	}
}

// --- 用户端：不泄露隐藏链 ---

func TestKeyFallback_UserViewNeverContainsAdminItems(t *testing.T) {
	env := newFBEnv(true)
	ctx := context.Background()
	env.seedUser(100, 2, 0)
	env.seedAdmin(100, 5, RoutePlacementHead, 0, "secret-note-513")
	env.seedAdmin(100, 6, RoutePlacementTail, 0, "secret-note-513")

	view, err := env.svc.GetUserChain(ctx, 1, 100, "")
	require.NoError(t, err)

	// 链上只有主分组 + 用户自己的项。
	require.Equal(t, []int64{1, 2}, fbItemIDs(view.Items))
	require.Equal(t, KeyFallbackRolePrimary, view.Items[0].Role)
	require.Equal(t, KeyFallbackRoleFallback, view.Items[1].Role)
	// 隐藏链里的分组仍然出现在 available（不因隐藏链而缺项，否则可以通过「缺席」推断）。
	require.Subset(t, fbAvailableIDs(view.Available), []int64{5, 6})

	raw := fbJSON(t, view)
	for _, forbidden := range []string{"secret-note-513", `"note"`, "admin", "hidden", "head", "created_by", "placement", "source"} {
		require.NotContains(t, raw, forbidden, "user response must not mention hidden chain: %s", forbidden)
	}

	// 批量接口同样不含隐藏项。
	overview, err := env.svc.ListUserChains(ctx, 1)
	require.NoError(t, err)
	rawOverview := fbJSON(t, overview)
	for _, forbidden := range []string{"secret-note-513", `"note"`, "admin", "hidden", "head", "created_by"} {
		require.NotContains(t, rawOverview, forbidden)
	}
	for _, section := range overview.Platforms {
		for _, key := range section.Keys {
			if key.KeyID == 100 {
				require.Equal(t, []int64{1, 2}, fbItemIDs(key.Items))
			}
		}
	}
}

func TestKeyFallback_PutDuplicatingHiddenChainLooksLikeOrdinarySuccess(t *testing.T) {
	ctx := context.Background()
	// 两把 Key 配置完全一样，唯一区别：Key 100 的隐藏链里已有用户马上要提交的分组 3、4。
	env := newFBEnv(true)
	env.seedAdmin(100, 3, RoutePlacementTail, 0, "pinned")
	env.seedAdmin(100, 4, RoutePlacementHead, 0, "pinned")

	withHidden, err := env.svc.ReplaceUserChain(ctx, 1, 100, []int64{3, 4, 5}, "")
	require.NoError(t, err)
	plain, err := env.svc.ReplaceUserChain(ctx, 1, 101, []int64{3, 4, 5}, "")
	require.NoError(t, err)

	// 除 key_id 外逐字节一致（items、available、reference_model、max_fallbacks……）。
	withHidden.KeyID, plain.KeyID = 0, 0
	require.Equal(t, fbJSON(t, plain), fbJSON(t, withHidden))
	require.Equal(t, []int64{1, 3, 4, 5}, fbItemIDs(withHidden.Items))

	// 用户行照常保存，管理员行原封不动。
	require.Equal(t, []int64{3, 4, 5}, routeGroupIDs(env.route.repo.bySource(100, RouteSourceUser)))
	require.Len(t, env.route.repo.bySource(100, RouteSourceAdmin), 2)
}

func TestKeyFallback_AvailableDoesNotShrinkBecauseOfHiddenChain(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	env.seedAdmin(100, 7, RoutePlacementTail, 0, "pinned")
	env.seedAdmin(100, 8, RoutePlacementHead, 0, "pinned")

	withHidden, err := env.svc.GetUserChain(ctx, 1, 100, "")
	require.NoError(t, err)
	plain, err := env.svc.GetUserChain(ctx, 1, 101, "")
	require.NoError(t, err)

	require.Equal(t, fbAvailableIDs(plain.Available), fbAvailableIDs(withHidden.Available))
	require.Contains(t, fbAvailableIDs(withHidden.Available), int64(7))
	// 同平台、有权限、非主分组：2..10 共 9 个（专属 20、停用 21、跨平台 22 都不在内）。
	require.Equal(t, []int64{2, 3, 4, 5, 6, 7, 8, 9, 10}, fbAvailableIDs(withHidden.Available))
}

func TestKeyFallback_ErrorCodesDoNotDependOnHiddenChain(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		ids  []int64
		want string
	}{
		{"duplicate", []int64{2, 2}, "FALLBACK_GROUP_DUPLICATE"},
		{"primary", []int64{1}, "FALLBACK_GROUP_IS_PRIMARY"},
		{"too long", []int64{2, 3, 4, 5, 6, 7}, "FALLBACK_CHAIN_TOO_LONG"},
		{"platform mismatch", []int64{22}, "FALLBACK_GROUP_PLATFORM_MISMATCH"},
		{"exclusive without grant", []int64{20}, "FALLBACK_GROUP_NOT_ALLOWED"},
		{"not found", []int64{999}, "FALLBACK_GROUP_NOT_FOUND"},
		{"non-positive id", []int64{0}, "FALLBACK_GROUP_NOT_FOUND"},
		{"disabled", []int64{21}, "FALLBACK_GROUP_UNAVAILABLE"},
	}
	for _, withHidden := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/hidden=%v", tc.name, withHidden), func(t *testing.T) {
				env := newFBEnv(true)
				env.seedUser(100, 9, 0)
				if withHidden {
					// 隐藏链里放着用户提交的每一个分组：错误码不能因此变化。
					for i, gid := range []int64{2, 3, 20, 22, 21} {
						env.seedAdmin(100, gid, RoutePlacementTail, i, "pinned")
					}
				}
				_, err := env.svc.ReplaceUserChain(ctx, 1, 100, tc.ids, "")
				require.Equal(t, tc.want, routeCode(t, err))
				// 失败不改库：原来的用户链仍是 [9]。
				require.Equal(t, []int64{9}, routeGroupIDs(env.route.repo.bySource(100, RouteSourceUser)))
			})
		}
	}
}

// fbErrGroupID 取校验错误 metadata 里的 group_id。
func fbErrGroupID(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	var appErr *infraerrors.ApplicationError
	require.True(t, errors.As(err, &appErr))
	return appErr.Metadata["group_id"]
}

// 链里原来就有、后来才变得不可用的分组：整条重新提交（例如拖动其他项排序）时可以保留。
func TestKeyFallback_PutKeepsExistingItemsThatBecameUnusable(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	env.seedUser(100, 2, 0)
	env.seedUser(100, 21, 1) // 后来被停用
	env.seedUser(100, 20, 2) // 专属，用户已失去授权
	env.seedUser(100, 22, 3) // 平台与主分组不一致
	env.seedUser(100, 3, 4)

	// 拖动：把 3 和 2 换到前面，不可用的三项原样带着。
	view, err := env.svc.ReplaceUserChain(ctx, 1, 100, []int64{3, 2, 21, 20, 22}, "")
	require.NoError(t, err)
	require.Equal(t, []int64{1, 3, 2, 21, 20, 22}, fbItemIDs(view.Items))
	require.Equal(t, []int64{3, 2, 21, 20, 22}, routeGroupIDs(env.route.repo.bySource(100, RouteSourceUser)))
	got := map[int64]bool{}
	for _, it := range view.Items {
		got[it.GroupID] = it.Usable
	}
	require.True(t, got[3] && got[2])
	require.False(t, got[21] || got[20] || got[22], "不可用的项照常展示，运行时跳过")

	// 保留的豁免只放宽「停用 / 失去授权 / 平台不一致」：重复、含主分组、超长仍然严格。
	_, err = env.svc.ReplaceUserChain(ctx, 1, 100, []int64{21, 21}, "")
	require.Equal(t, "FALLBACK_GROUP_DUPLICATE", routeCode(t, err))
	_, err = env.svc.ReplaceUserChain(ctx, 1, 100, []int64{1, 21}, "")
	require.Equal(t, "FALLBACK_GROUP_IS_PRIMARY", routeCode(t, err))
	_, err = env.svc.ReplaceUserChain(ctx, 1, 100, []int64{3, 2, 21, 20, 22, 4}, "")
	require.Equal(t, "FALLBACK_CHAIN_TOO_LONG", routeCode(t, err))
}

// 新加一个已停用（或没有授权、跨平台）的分组仍然被拒；原来链里没有它，就不在豁免之列。
func TestKeyFallback_PutRejectsNewlyAddedUnusableGroups(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	env.seedUser(100, 2, 0)
	env.seedUser(100, 21, 1) // 原来就有，后来停用

	for _, tc := range []struct {
		name string
		ids  []int64
		want string
		gid  string
	}{
		{"new platform mismatch", []int64{2, 21, 22}, "FALLBACK_GROUP_PLATFORM_MISMATCH", "22"},
		{"new exclusive without grant", []int64{2, 21, 20}, "FALLBACK_GROUP_NOT_ALLOWED", "20"},
		{"new missing", []int64{2, 21, 999}, "FALLBACK_GROUP_NOT_FOUND", "999"},
	} {
		_, err := env.svc.ReplaceUserChain(ctx, 1, 100, tc.ids, "")
		require.Equal(t, tc.want, routeCode(t, err), tc.name)
		require.Equal(t, tc.gid, fbErrGroupID(t, err), tc.name)
	}

	// 一条新加的已停用分组：另一把没有该项的 Key 上直接被拒，metadata 指向它。
	env.route.groups.groups[23] = &Group{ID: 23, Name: "off2", Platform: PlatformOpenAI, Status: StatusDisabled}
	_, err := env.svc.ReplaceUserChain(ctx, 1, 100, []int64{2, 21, 23}, "")
	require.Equal(t, "FALLBACK_GROUP_UNAVAILABLE", routeCode(t, err))
	require.Equal(t, "23", fbErrGroupID(t, err))
	require.Equal(t, []int64{2, 21}, routeGroupIDs(env.route.repo.bySource(100, RouteSourceUser)), "失败不改库")

	// 把原来的停用项删掉之后再加回去，就成了「新增」，被拒。
	_, err = env.svc.ReplaceUserChain(ctx, 1, 100, []int64{2}, "")
	require.NoError(t, err)
	_, err = env.svc.ReplaceUserChain(ctx, 1, 100, []int64{2, 21}, "")
	require.Equal(t, "FALLBACK_GROUP_UNAVAILABLE", routeCode(t, err))
	require.Equal(t, "21", fbErrGroupID(t, err))
}

// 「原来就有」只以用户链为准：分组恰好在隐藏链里时，响应（成功或错误，含 metadata）与没有隐藏链时完全一致。
func TestKeyFallback_PutExemptionNeverConsultsHiddenChain(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		ids  []int64
		want string
	}{
		{"hidden disabled group", []int64{21}, "FALLBACK_GROUP_UNAVAILABLE"},
		{"hidden exclusive group without grant", []int64{20}, "FALLBACK_GROUP_NOT_ALLOWED"},
		{"hidden cross platform group", []int64{22}, "FALLBACK_GROUP_PLATFORM_MISMATCH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain := newFBEnv(true)
			hidden := newFBEnv(true)
			hidden.seedAdmin(100, 21, RoutePlacementHead, 0, "pinned")
			hidden.seedAdmin(100, 20, RoutePlacementTail, 0, "pinned")
			hidden.seedAdmin(100, 22, RoutePlacementTail, 1, "pinned")

			_, errPlain := plain.svc.ReplaceUserChain(ctx, 1, 100, tc.ids, "")
			_, errHidden := hidden.svc.ReplaceUserChain(ctx, 1, 100, tc.ids, "")
			require.Equal(t, tc.want, routeCode(t, errPlain))
			require.Equal(t, tc.want, routeCode(t, errHidden))
			require.Equal(t, fbErrGroupID(t, errPlain), fbErrGroupID(t, errHidden))
			require.Equal(t, fmt.Sprint(tc.ids[0]), fbErrGroupID(t, errHidden))
			require.Empty(t, hidden.route.repo.bySource(100, RouteSourceUser), "隐藏链不能让保存成功")
		})
	}

	// 只在隐藏链里的可用分组：照常保存，响应与普通成功逐字节一致。
	plain := newFBEnv(true)
	hidden := newFBEnv(true)
	hidden.seedAdmin(100, 3, RoutePlacementTail, 0, "pinned")
	okPlain, err := plain.svc.ReplaceUserChain(ctx, 1, 100, []int64{3}, "")
	require.NoError(t, err)
	okHidden, err := hidden.svc.ReplaceUserChain(ctx, 1, 100, []int64{3}, "")
	require.NoError(t, err)
	require.Equal(t, fbJSON(t, okPlain), fbJSON(t, okHidden))
}

// 校验失败的错误 metadata 只带用户自己提交的那个分组 ID。
func TestKeyFallback_ValidationErrorsCarryGroupID(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		ids  []int64
		want string
		gid  string
	}{
		{[]int64{2, 3, 2}, "FALLBACK_GROUP_DUPLICATE", "2"},
		{[]int64{2, 1}, "FALLBACK_GROUP_IS_PRIMARY", "1"},
		{[]int64{2, 22}, "FALLBACK_GROUP_PLATFORM_MISMATCH", "22"},
		{[]int64{2, 20}, "FALLBACK_GROUP_NOT_ALLOWED", "20"},
		{[]int64{2, 21}, "FALLBACK_GROUP_UNAVAILABLE", "21"},
		{[]int64{2, 999}, "FALLBACK_GROUP_NOT_FOUND", "999"},
		{[]int64{2, 0}, "FALLBACK_GROUP_NOT_FOUND", "0"},
	} {
		env := newFBEnv(true)
		_, err := env.svc.ReplaceUserChain(ctx, 1, 100, tc.ids, "")
		require.Equal(t, tc.want, routeCode(t, err))
		require.Equal(t, tc.gid, fbErrGroupID(t, err), tc.want)
	}
}

func TestKeyFallback_KeyChangedConflict(t *testing.T) {
	env := newFBEnv(true)
	// 保存期间 Key 的主分组被并发改走：仓储锁住 Key 行后核对不一致。
	env.route.repo.primary[100] = 2
	_, err := env.svc.ReplaceUserChain(context.Background(), 1, 100, []int64{3}, "")
	require.Equal(t, "FALLBACK_KEY_CHANGED", routeCode(t, err))
}

func TestKeyFallback_ExclusiveGroupRequiresGrant(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)

	// 没有授权：既不能选，也不在 available 里。
	view, err := env.svc.GetUserChain(ctx, 1, 100, "")
	require.NoError(t, err)
	require.NotContains(t, fbAvailableIDs(view.Available), int64(20))
	_, err = env.svc.ReplaceUserChain(ctx, 1, 100, []int64{20}, "")
	require.Equal(t, "FALLBACK_GROUP_NOT_ALLOWED", routeCode(t, err))

	// 授权之后：available 里出现，也能保存。可用分组与保存校验用的是同一个 User.CanBindGroup。
	env.users.users[1].AllowedGroups = []int64{20}
	view, err = env.svc.GetUserChain(ctx, 1, 100, "")
	require.NoError(t, err)
	require.Contains(t, fbAvailableIDs(view.Available), int64(20))
	saved, err := env.svc.ReplaceUserChain(ctx, 1, 100, []int64{20}, "")
	require.NoError(t, err)
	require.Equal(t, []int64{1, 20}, fbItemIDs(saved.Items))
	require.NotContains(t, fbAvailableIDs(saved.Available), int64(20), "已在链里的不再出现在 available")

	// 授权被收回：链项仍然展示（让用户能删除），但标成不可用。
	env.users.users[1].AllowedGroups = nil
	view, err = env.svc.GetUserChain(ctx, 1, 100, "")
	require.NoError(t, err)
	require.Equal(t, KeyFallbackStatusUnavailable, view.Items[1].Status)
	require.False(t, view.Items[1].Usable)
}

func TestKeyFallback_ItemStatuses(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	env.seedUser(100, 2, 0)  // 正常
	env.seedUser(100, 21, 1) // 已停用
	env.seedUser(100, 22, 2) // 平台与主分组不一致
	env.seedUser(100, 20, 3) // 专属且无授权

	view, err := env.svc.GetUserChain(ctx, 1, 100, "")
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 21, 22, 20}, fbItemIDs(view.Items))
	got := map[int64]string{}
	usable := map[int64]bool{}
	for _, it := range view.Items {
		got[it.GroupID], usable[it.GroupID] = it.Status, it.Usable
	}
	require.Equal(t, KeyFallbackStatusActive, got[1])
	require.Equal(t, KeyFallbackStatusActive, got[2])
	require.Equal(t, KeyFallbackStatusDisabled, got[21])
	require.Equal(t, KeyFallbackStatusUnavailable, got[22])
	require.Equal(t, KeyFallbackStatusUnavailable, got[20])
	require.True(t, usable[1] && usable[2])
	require.False(t, usable[21] || usable[22] || usable[20])
	// 位置：主分组 0，兜底项从 1 起。
	for i, it := range view.Items {
		require.Equal(t, i, it.Position)
	}
}

func TestKeyFallback_MultipliersIncludeUserRate(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	env.route.groups.groups[2].RateMultiplier = 1.8
	env.route.groups.groups[1].RateMultiplier = 1
	env.keys.rates[1] = map[int64]float64{2: 1.5}
	env.seedUser(100, 2, 0)

	view, err := env.svc.GetUserChain(ctx, 1, 100, "")
	require.NoError(t, err)
	require.Nil(t, view.Items[0].UserRateMultiplier)
	require.Equal(t, 1.0, view.Items[0].EffectiveMultiplier)
	require.Equal(t, 1.8, view.Items[1].RateMultiplier)
	require.Equal(t, 1.5, *view.Items[1].UserRateMultiplier)
	require.Equal(t, 1.5, view.Items[1].EffectiveMultiplier)
	// 没有专属倍率时 JSON 里是显式的 null（前端据此判断），而不是缺字段。
	require.Contains(t, fbJSON(t, view.Items[0]), `"user_rate_multiplier":null`)
}

// --- 用户端：参考价 ---

func TestKeyFallback_ReferencePrices(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	in, out := 1.875, 15.0
	env.pricer.prices[2] = KeyEditorReferencePrice{Priced: true, InputUSDPerMTok: &in, OutputUSDPerMTok: &out, CNY: &KeyEditorCNYPrice{InputPerMTok: 0.1442, OutputPerMTok: 1.1538}}
	env.seedUser(100, 2, 0)

	view, err := env.svc.GetUserChain(ctx, 1, 100, "")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.5", view.ReferenceModel)
	require.Equal(t, MaxUserFallbackItems, view.MaxFallbacks)
	require.True(t, view.Items[1].ReferencePrice.Priced)
	require.Equal(t, 0.1442, view.Items[1].ReferencePrice.CNY.InputPerMTok)

	// 主分组没有定价 → 降级为 priced=false（不报错、不出现价格字段）；available 同理。
	require.False(t, view.Items[0].ReferencePrice.Priced)
	for _, a := range view.Available {
		require.False(t, a.ReferencePrice.Priced)
	}
	require.NotContains(t, fbJSON(t, view.Items[0].ReferencePrice), "usd_per_mtok")

	// 报价按「主分组 + 目标分组 + 当前用户」请求，且每个展示项只报一次。
	require.Contains(t, env.pricer.calls, fbPriceCall{"gpt-5.5", 1, 2, 1})
	require.Contains(t, env.pricer.calls, fbPriceCall{"gpt-5.5", 1, 1, 1})
	for _, c := range env.pricer.calls {
		require.Equal(t, int64(1), c.uid)
		require.Equal(t, int64(1), c.primaryID)
	}
	require.Len(t, env.pricer.calls, len(view.Items)+len(view.Available))
}

func TestKeyFallback_ReferenceModelOverrideAndValidation(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)

	view, err := env.svc.GetUserChain(ctx, 1, 100, " gpt-5.4 ")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", view.ReferenceModel)
	for _, c := range env.pricer.calls {
		require.Equal(t, "gpt-5.4", c.model)
	}

	// anthropic Key 取 anthropic 的默认参考模型。
	view, err = env.svc.GetUserChain(ctx, 1, 400, "")
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4", view.ReferenceModel)
	require.Equal(t, PlatformAnthropic, view.Platform)

	env.pricer.calls = nil
	_, err = env.svc.GetUserChain(ctx, 1, 100, "bad model!")
	require.Equal(t, "FALLBACK_INVALID_MODEL", routeCode(t, err))
	_, err = env.svc.ReplaceUserChain(ctx, 1, 100, []int64{2}, "bad model!")
	require.Equal(t, "FALLBACK_INVALID_MODEL", routeCode(t, err))
	require.Empty(t, env.pricer.calls)
	require.Empty(t, env.route.repo.bySource(100, RouteSourceUser), "invalid model must not write the chain")
}

// --- 总开关 ---

func TestKeyFallback_SwitchOffStillSavesButDoesNotTakeEffect(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(false)

	view, err := env.svc.ReplaceUserChain(ctx, 1, 100, []int64{2, 3}, "")
	require.NoError(t, err)
	require.False(t, view.Enabled)
	require.Equal(t, []int64{1, 2, 3}, fbItemIDs(view.Items))
	require.Equal(t, []int64{2, 3}, routeGroupIDs(env.route.repo.bySource(100, RouteSourceUser)))

	// 运行时不生效：有效链为空（调用方走原逻辑）。
	chain, err := env.route.svc.ResolveEffectiveChain(ctx, &APIKey{ID: 100, UserID: 1, GroupID: int64Ptr(1)}, env.users.users[1], ResolveOptions{})
	require.NoError(t, err)
	require.Empty(t, chain.Hops)

	overview, err := env.svc.ListUserChains(ctx, 1)
	require.NoError(t, err)
	require.False(t, overview.Enabled)

	on := newFBEnv(true)
	view, err = on.svc.GetUserChain(ctx, 1, 100, "")
	require.NoError(t, err)
	require.True(t, view.Enabled)
}

// --- 批量接口 ---

func TestKeyFallback_ListUserChainsGroupsByPlatform(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	env.seedUser(100, 2, 0)
	// Key 101 把同平台所有可选分组都放进链里：没有可添加的分组。
	for i, gid := range []int64{2, 3, 4, 5, 6} {
		env.seedUser(101, gid, i)
	}

	overview, err := env.svc.ListUserChains(ctx, 1)
	require.NoError(t, err)
	require.Len(t, overview.Platforms, 2)
	require.Equal(t, PlatformOpenAI, overview.Platforms[0].Platform)
	require.Equal(t, PlatformAnthropic, overview.Platforms[1].Platform)

	openai := overview.Platforms[0].Keys
	require.Len(t, openai, 2)
	require.Equal(t, int64(100), openai[0].KeyID)
	require.Equal(t, "k100", openai[0].Name)
	require.Equal(t, []int64{1, 2}, fbItemIDs(openai[0].Items))
	require.True(t, openai[0].HasAvailable)
	require.Equal(t, int64(101), openai[1].KeyID)
	require.True(t, openai[1].HasAvailable, "仍有 7..10 可选")

	anthropic := overview.Platforms[1].Keys
	require.Len(t, anthropic, 1)
	require.Equal(t, int64(400), anthropic[0].KeyID)
	require.False(t, anthropic[0].HasAvailable, "只有一个 anthropic 分组，已是主分组")

	// 批量接口不带价格（量大），也不带 available 明细。
	raw := fbJSON(t, overview)
	require.NotContains(t, raw, "reference_price")
	require.NotContains(t, raw, `"available"`)
	require.Empty(t, env.pricer.calls)

	// 别人的 Key 不会出现。
	require.NotContains(t, raw, `"key_id":200`)

	empty := newFBEnv(true)
	empty.keys.keys = map[int64]*APIKey{}
	overview, err = empty.svc.ListUserChains(ctx, 1)
	require.NoError(t, err)
	require.NotNil(t, overview.Platforms)
	require.Equal(t, `{"enabled":true,"platforms":[]}`, fbJSON(t, overview))
}

// --- 管理端 ---

func TestKeyFallback_AdminGetChainShowsHiddenChainAndDryRun(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(false) // 开关关闭时 dry-run 仍给出有效链
	createdBy := int64(1)
	env.route.repo.seed(RouteItem{APIKeyID: 100, GroupID: 5, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementHead, Position: 0, Note: "513 专属号", CreatedBy: &createdBy})
	env.seedAdmin(100, 6, RoutePlacementTail, 0, "513 专属号")
	env.seedUser(100, 2, 0)
	env.seedUser(100, 21, 1) // 停用，被跳过

	view, err := env.svc.AdminGetChain(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, int64(100), view.KeyID)
	require.Equal(t, int64(1), view.UserID)
	require.Equal(t, PlatformOpenAI, view.Platform)
	require.False(t, view.Enabled)
	require.Equal(t, &KeyFallbackGroupRef{GroupID: 1, Name: "main"}, view.PrimaryGroup)

	require.Len(t, view.HiddenHead, 1)
	require.Equal(t, int64(5), view.HiddenHead[0].GroupID)
	require.Equal(t, "513 专属号", view.HiddenHead[0].Note)
	require.Equal(t, createdBy, *view.HiddenHead[0].CreatedBy)
	require.Len(t, view.HiddenTail, 1)
	require.Len(t, view.UserItems, 2)

	var sources []string
	var ids []int64
	for i, hop := range view.Effective {
		require.Equal(t, i, hop.Hop)
		require.True(t, hop.Eligible)
		sources = append(sources, hop.Source)
		ids = append(ids, hop.GroupID)
	}
	require.Equal(t, []int64{5, 1, 2, 6}, ids)
	require.Equal(t, []string{"admin_head", "primary", "user", "admin_tail"}, sources)
	require.Equal(t, []KeyFallbackSkippedHop{{GroupID: 21, Name: "off", Source: RouteSourceUser, SkipReason: RouteSkipGroupInactive}}, view.Skipped)
	require.False(t, view.Truncated)
}

func TestKeyFallback_AdminGetChainForUngroupedAndMissingKey(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)

	view, err := env.svc.AdminGetChain(ctx, 300)
	require.NoError(t, err)
	require.Nil(t, view.PrimaryGroup)
	require.Empty(t, view.Effective)
	require.Equal(t, `[]`, fbJSON(t, view.Effective))

	_, err = env.svc.AdminGetChain(ctx, 9999)
	require.Equal(t, "API_KEY_NOT_FOUND", routeCode(t, err))
}

func TestKeyFallback_AdminReplaceAndClearHiddenChain(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	env.seedUser(100, 2, 0)

	// 有 head 必须写 note。
	_, err := env.svc.AdminReplaceHiddenChain(ctx, 9, 100, []int64{5}, nil, "")
	require.Equal(t, "FALLBACK_NOTE_REQUIRED", routeCode(t, err))
	require.Empty(t, env.route.repo.bySource(100, RouteSourceAdmin))

	view, err := env.svc.AdminReplaceHiddenChain(ctx, 9, 100, []int64{5}, []int64{6, 20}, "513 专属号")
	require.NoError(t, err)
	require.Len(t, view.HiddenHead, 1)
	require.Len(t, view.HiddenTail, 2)
	require.Equal(t, int64(9), *view.HiddenHead[0].CreatedBy)
	// 管理员项只豁免 CanBindGroup：专属分组 20 用户没授权也能放进隐藏链。
	require.Equal(t, int64(20), view.HiddenTail[1].GroupID)
	// 用户链不受影响。
	require.Equal(t, []int64{2}, routeGroupIDs(env.route.repo.bySource(100, RouteSourceUser)))

	// 其它校验照做：停用分组、跨平台、含主分组。
	_, err = env.svc.AdminReplaceHiddenChain(ctx, 9, 100, nil, []int64{21}, "")
	require.Equal(t, "FALLBACK_GROUP_UNAVAILABLE", routeCode(t, err))
	_, err = env.svc.AdminReplaceHiddenChain(ctx, 9, 100, nil, []int64{22}, "")
	require.Equal(t, "FALLBACK_GROUP_PLATFORM_MISMATCH", routeCode(t, err))
	_, err = env.svc.AdminReplaceHiddenChain(ctx, 9, 100, nil, []int64{1}, "")
	require.Equal(t, "FALLBACK_GROUP_IS_PRIMARY", routeCode(t, err))
	_, err = env.svc.AdminReplaceHiddenChain(ctx, 9, 100, []int64{2, 2}, nil, "x")
	require.Equal(t, "FALLBACK_GROUP_DUPLICATE", routeCode(t, err))
	_, err = env.svc.AdminReplaceHiddenChain(ctx, 9, 100, nil, nil, strings.Repeat("好", MaxRouteNoteLen+1))
	require.Equal(t, "FALLBACK_NOTE_TOO_LONG", routeCode(t, err))
	_, err = env.svc.AdminReplaceHiddenChain(ctx, 9, 9999, nil, []int64{6}, "")
	require.Equal(t, "API_KEY_NOT_FOUND", routeCode(t, err))
	_, err = env.svc.AdminReplaceHiddenChain(ctx, 9, 300, nil, []int64{6}, "")
	require.Equal(t, "FALLBACK_KEY_NOT_GROUPED", routeCode(t, err))
	// 失败没有改动已保存的隐藏链。
	require.Len(t, env.route.repo.bySource(100, RouteSourceAdmin), 3)

	cleared, err := env.svc.AdminClearHiddenChain(ctx, 9, 100)
	require.NoError(t, err)
	require.Empty(t, cleared.HiddenHead)
	require.Empty(t, cleared.HiddenTail)
	require.Equal(t, `[]`, fbJSON(t, cleared.HiddenHead))
	require.Equal(t, []int64{2}, routeGroupIDs(env.route.repo.bySource(100, RouteSourceUser)), "清空隐藏链不动用户链")
}

func TestKeyFallback_AdminRoutesByGroup(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	env.extras.refs = []GroupRouteRef{{KeyID: 100, UserID: 1, Source: RouteSourceUser, Placement: RoutePlacementTail}, {KeyID: 101, UserID: 1, Source: RouteSourceAdmin, Placement: RoutePlacementHead}}

	view, err := env.svc.AdminRoutesByGroup(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), view.GroupID)
	require.False(t, view.Truncated)
	require.Equal(t, []KeyFallbackRouteRefView{{KeyID: 100, UserID: 1, Source: "user", Placement: "tail"}, {KeyID: 101, UserID: 1, Source: "admin", Placement: "head"}}, view.Items)
	// 只返回 ID，不含 Key 明文。
	require.NotContains(t, fbJSON(t, view), "sk-")

	// 超过上限：多取一条判断截断。
	env.extras.refs = make([]GroupRouteRef, keyFallbackRoutesByGroupLimit+5)
	view, err = env.svc.AdminRoutesByGroup(ctx, 2)
	require.NoError(t, err)
	require.True(t, view.Truncated)
	require.Len(t, view.Items, keyFallbackRoutesByGroupLimit)
	require.Equal(t, keyFallbackRoutesByGroupLimit+1, env.extras.listLimit)

	_, err = env.svc.AdminRoutesByGroup(ctx, 999)
	require.Equal(t, "FALLBACK_GROUP_NOT_FOUND", routeCode(t, err))
	_, err = env.svc.AdminRoutesByGroup(ctx, 0)
	require.Equal(t, "FALLBACK_GROUP_NOT_FOUND", routeCode(t, err))

	env.svc.extras = nil
	_, err = env.svc.AdminRoutesByGroup(ctx, 2)
	require.Equal(t, "FALLBACK_UNAVAILABLE", routeCode(t, err))
}

func TestKeyFallback_AdminReferenceModels(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	require.Equal(t, "gpt-5.5", env.svc.AdminReferenceModels(ctx)[PlatformOpenAI])
	got, err := env.svc.AdminSetReferenceModels(ctx, map[string]string{PlatformOpenAI: "gpt-5.6-sol"})
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-sol", got[PlatformOpenAI])

	env.svc.pricer = nil
	require.Empty(t, env.svc.AdminReferenceModels(ctx))
	_, err = env.svc.AdminSetReferenceModels(ctx, map[string]string{})
	require.Equal(t, "FALLBACK_UNAVAILABLE", routeCode(t, err))
}

// 管理端请求体里的字段名不能被审计脱敏：head / tail / note / group_ids。
func TestKeyFallback_AuditBodyKeepsFallbackFieldNames(t *testing.T) {
	adminBody := `{"head":[88],"tail":[5],"note":"513 专属号替代 openai_forced_account_routes"}`
	redacted := RedactAuditBody([]byte(adminBody), "application/json")
	require.NotContains(t, redacted, auditRedactedPlaceholder)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(redacted), &decoded))
	require.Equal(t, []any{float64(88)}, decoded["head"])
	require.Equal(t, []any{float64(5)}, decoded["tail"])
	require.Equal(t, "513 专属号替代 openai_forced_account_routes", decoded["note"])

	userBody := `{"group_ids":[21,30]}`
	redacted = RedactAuditBody([]byte(userBody), "application/json")
	require.NotContains(t, redacted, auditRedactedPlaceholder)
	require.JSONEq(t, userBody, redacted)

	// 反例：含 apikey 的字段名会被整体脱敏，所以接口契约里不能用。
	// 用 api_key（精确命中脱敏表，与 #1483 的「keyids 等安全后缀」放行规则无关）。
	require.Contains(t, RedactAuditBody([]byte(`{"api_key":"x"}`), "application/json"), auditRedactedPlaceholder)

	// feat/admin-tokens-audit 合入后脱敏子串表会更宽（含 key / credential / private 等）：
	// 这里按那张更宽的表再核对一遍，字段名在两版里都不能命中。
	broader := []string{"password", "passwd", "secret", "token", "apikey", "key", "credential", "cookie", "authorization", "private", "serviceaccount", "totp", "otp"}
	for _, field := range []string{"head", "tail", "note", "group_ids", "models", "model"} {
		require.False(t, isAuditSensitiveKey(field), field)
		normalized := normalizeAuditBodyKey(field)
		for _, sub := range broader {
			require.NotContains(t, normalized, sub, "field %q would be redacted by substring %q", field, sub)
		}
	}
}

// --- 联动：改主分组 / 删除 Key ---

type fbHooksRecorder struct {
	changed    []int64 // 新主分组 ID
	changedKey []int64
	deleted    []int64
	changeErr  error
	deleteErr  error
}

func (h *fbHooksRecorder) OnPrimaryGroupChanged(_ context.Context, key *APIKey, g *Group) error {
	h.changedKey = append(h.changedKey, key.ID)
	h.changed = append(h.changed, g.ID)
	return h.changeErr
}
func (h *fbHooksRecorder) OnKeyDeleted(_ context.Context, keyID int64) error {
	h.deleted = append(h.deleted, keyID)
	return h.deleteErr
}

type fbUserRepoStub struct {
	UserRepository
	user *User
}

func (s *fbUserRepoStub) GetByID(context.Context, int64) (*User, error) { return s.user, nil }

type fbGroupRepoStub struct {
	GroupRepository
	group *Group
}

func (s *fbGroupRepoStub) GetByID(context.Context, int64) (*Group, error) {
	cp := *s.group
	return &cp, nil
}

func newFBKeyService(groupID *int64, newGroup *Group, hooks groupRouteKeyHooks) (*APIKeyService, *apiKeyUpdateRepoStub) {
	repo := &apiKeyUpdateRepoStub{apiKeyRepoStub: apiKeyRepoStub{apiKey: &APIKey{ID: 7, UserID: 11, Key: "sk-test", Status: StatusActive, GroupID: groupID}}}
	svc := NewAPIKeyService(repo, &fbUserRepoStub{user: &User{ID: 11}}, &fbGroupRepoStub{group: newGroup}, nil, nil, nil, nil)
	svc.groupRouteHooks = hooks
	return svc, repo
}

func TestAPIKeyServiceUpdate_PrimaryGroupChangeTriggersFallbackHook(t *testing.T) {
	ctx := context.Background()
	hooks := &fbHooksRecorder{}
	svc, repo := newFBKeyService(int64Ptr(1), &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}, hooks)

	_, err := svc.Update(ctx, 7, 11, UpdateAPIKeyRequest{GroupID: int64Ptr(2)})
	require.NoError(t, err)
	require.Equal(t, int64(2), *repo.updated.GroupID)
	require.Equal(t, []int64{2}, hooks.changed)
	require.Equal(t, []int64{7}, hooks.changedKey)
}

func TestAPIKeyServiceUpdate_HookSkippedWhenGroupUnchangedOrNotSent(t *testing.T) {
	ctx := context.Background()
	hooks := &fbHooksRecorder{}
	svc, _ := newFBKeyService(int64Ptr(2), &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}, hooks)

	_, err := svc.Update(ctx, 7, 11, UpdateAPIKeyRequest{GroupID: int64Ptr(2)}) // 同一个分组
	require.NoError(t, err)
	_, err = svc.Update(ctx, 7, 11, UpdateAPIKeyRequest{}) // 没传 group_id
	require.NoError(t, err)
	require.Empty(t, hooks.changed)

	// 原来未绑分组、现在绑定：算主分组变化。
	svc, _ = newFBKeyService(nil, &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}, hooks)
	_, err = svc.Update(ctx, 7, 11, UpdateAPIKeyRequest{GroupID: int64Ptr(2)})
	require.NoError(t, err)
	require.Equal(t, []int64{2}, hooks.changed)
}

func TestAPIKeyServiceUpdate_HookFailureDoesNotFailTheUpdate(t *testing.T) {
	hooks := &fbHooksRecorder{changeErr: errors.New("db down")}
	svc, repo := newFBKeyService(int64Ptr(1), &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}, hooks)

	_, err := svc.Update(context.Background(), 7, 11, UpdateAPIKeyRequest{GroupID: int64Ptr(2)})
	require.NoError(t, err)
	require.Equal(t, int64(2), *repo.updated.GroupID)
	require.Equal(t, []int64{2}, hooks.changed)
}

func TestAPIKeyServiceUpdate_NoHooksIsNoop(t *testing.T) {
	svc, repo := newFBKeyService(int64Ptr(1), &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}, nil)
	_, err := svc.Update(context.Background(), 7, 11, UpdateAPIKeyRequest{GroupID: int64Ptr(2)})
	require.NoError(t, err)
	require.Equal(t, int64(2), *repo.updated.GroupID)

	// SetGroupRouteHooks(nil) 不能留下「非 nil 接口里装着 nil 指针」。
	svc.SetGroupRouteHooks(nil)
	require.Nil(t, svc.groupRouteHooks)
}

func TestAPIKeyServiceDelete_CleansUpFallbackRoutes(t *testing.T) {
	ctx := context.Background()
	hooks := &fbHooksRecorder{}
	repo := &apiKeyRepoStub{apiKey: &APIKey{ID: 7, UserID: 11, Key: "sk-test"}}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, nil)
	svc.groupRouteHooks = hooks

	require.NoError(t, svc.Delete(ctx, 7, 11))
	require.Equal(t, []int64{7}, repo.deletedIDs)
	require.Equal(t, []int64{7}, hooks.deleted)

	// 清理失败不影响删除结果。
	hooks.deleteErr = errors.New("boom")
	require.NoError(t, svc.Delete(ctx, 7, 11))

	// 不是所有者：不删除，也不清理。
	hooks.deleted = nil
	require.Error(t, svc.Delete(ctx, 7, 12))
	require.Empty(t, hooks.deleted)
}

func TestAdminServiceUpdateAPIKeyGroupID_PrimaryGroupChangeTriggersFallbackHook(t *testing.T) {
	ctx := context.Background()
	hooks := &fbHooksRecorder{}
	newAdmin := func(current *int64) (*adminServiceImpl, *apiKeyRepoStubForGroupUpdate) {
		repo := &apiKeyRepoStubForGroupUpdate{key: &APIKey{ID: 7, UserID: 11, Key: "sk-test", GroupID: current}}
		groups := &groupRepoStubForGroupUpdate{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}}
		return &adminServiceImpl{apiKeyRepo: repo, groupRepo: groups, groupRouteHooks: hooks}, repo
	}

	svc, _ := newAdmin(int64Ptr(1))
	_, err := svc.AdminUpdateAPIKeyGroupID(ctx, 7, int64Ptr(2))
	require.NoError(t, err)
	require.Equal(t, []int64{2}, hooks.changed)

	// 同一个分组：不联动。
	hooks.changed = nil
	svc, _ = newAdmin(int64Ptr(2))
	_, err = svc.AdminUpdateAPIKeyGroupID(ctx, 7, int64Ptr(2))
	require.NoError(t, err)
	require.Empty(t, hooks.changed)

	// 解绑：不联动（链项原样保留，运行时对未绑分组的 Key 不启用链）。
	svc, _ = newAdmin(int64Ptr(1))
	_, err = svc.AdminUpdateAPIKeyGroupID(ctx, 7, int64Ptr(0))
	require.NoError(t, err)
	require.Empty(t, hooks.changed)

	// 联动失败不影响改分组本身。
	hooks.changeErr = errors.New("db down")
	svc, _ = newAdmin(int64Ptr(1))
	_, err = svc.AdminUpdateAPIKeyGroupID(ctx, 7, int64Ptr(2))
	require.NoError(t, err)

	// 没有注入联动入口：照常工作。
	svc, _ = newAdmin(int64Ptr(1))
	svc.groupRouteHooks = nil
	_, err = svc.AdminUpdateAPIKeyGroupID(ctx, 7, int64Ptr(2))
	require.NoError(t, err)
}

func TestGroupRouteKeyHooks_DelegatesAndIsNilSafe(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	hooks := NewGroupRouteKeyHooks(env.route.svc, env.extras)

	key := &APIKey{ID: 100, UserID: 1, GroupID: int64Ptr(2)}
	require.NoError(t, hooks.OnPrimaryGroupChanged(ctx, key, &Group{ID: 2, Platform: PlatformOpenAI}))
	require.Equal(t, []applyCall{{keyID: 100, groupID: 2, platform: PlatformOpenAI}}, env.route.repo.applied)

	require.NoError(t, hooks.OnKeyDeleted(ctx, 100))
	require.Equal(t, []int64{100}, env.extras.deletedKey)
	require.NoError(t, hooks.OnKeyDeleted(ctx, 0), "无效 ID 不触发删除")
	require.Equal(t, []int64{100}, env.extras.deletedKey)

	var nilHooks *GroupRouteKeyHooks
	require.NoError(t, nilHooks.OnPrimaryGroupChanged(ctx, key, &Group{ID: 2}))
	require.NoError(t, nilHooks.OnKeyDeleted(ctx, 100))
	require.NoError(t, NewGroupRouteKeyHooks(nil, nil).OnPrimaryGroupChanged(ctx, key, &Group{ID: 2}))
	require.NoError(t, NewGroupRouteKeyHooks(nil, nil).OnKeyDeleted(ctx, 100))
}
