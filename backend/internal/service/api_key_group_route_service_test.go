//go:build unit

package service

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// --- 测试替身 ---

type routeTestGroupRepo struct {
	GroupRepository
	groups map[int64]*Group
}

func (r *routeTestGroupRepo) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	g, ok := r.groups[id]
	if !ok {
		return nil, ErrGroupNotFound
	}
	cp := *g
	return &cp, nil
}

// routeTestRepo 是内存版 APIKeyGroupRouteRepository，语义与真实仓储一致：
// 同 source 内 group 唯一；ReplaceChain 只替换指定 source；Key 当前主分组由 primary 给出。
type routeTestRepo struct {
	mu        sync.Mutex
	rows      []RouteItem
	nextID    int64
	primary   map[int64]int64
	listCalls int
	listErr   error
	replaceFn func(ReplaceRoutesParams) error
	applied   []applyCall
}

type applyCall struct {
	keyID, groupID int64
	platform       string
}

func (r *routeTestRepo) ListByKey(_ context.Context, keyID int64, source string) ([]RouteItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	var out []RouteItem
	for _, it := range r.rows {
		if it.APIKeyID == keyID && (source == "" || it.Source == source) {
			out = append(out, it)
		}
	}
	return out, nil
}

func (r *routeTestRepo) ReplaceChain(_ context.Context, p ReplaceRoutesParams) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.replaceFn != nil {
		if err := r.replaceFn(p); err != nil {
			return err
		}
	}
	if cur, ok := r.primary[p.APIKeyID]; ok && p.ExpectedPrimaryGroupID != 0 && cur != p.ExpectedPrimaryGroupID {
		return ErrFallbackKeyChanged
	}
	kept := r.rows[:0:0]
	for _, it := range r.rows {
		if it.APIKeyID == p.APIKeyID && it.Source == p.Source {
			continue
		}
		kept = append(kept, it)
	}
	seen := map[int64]bool{}
	for _, it := range p.Items {
		if seen[it.GroupID] {
			return errors.New("unique violation (api_key_id, source, group_id)")
		}
		seen[it.GroupID] = true
		r.nextID++
		it.ID = r.nextID
		it.APIKeyID = p.APIKeyID
		it.Source = p.Source
		kept = append(kept, it)
	}
	r.rows = kept
	return nil
}

func (r *routeTestRepo) ApplyPrimaryGroupChange(_ context.Context, keyID, newGroupID int64, newPlatform string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applied = append(r.applied, applyCall{keyID, newGroupID, newPlatform})
	return nil
}

func (r *routeTestRepo) seed(items ...RouteItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, it := range items {
		r.nextID++
		it.ID = r.nextID
		r.rows = append(r.rows, it)
	}
}

func (r *routeTestRepo) bySource(keyID int64, source string) []RouteItem {
	items, _ := r.ListByKey(context.Background(), keyID, source)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

type routeTestSettings struct{ enabled bool }

func (s routeTestSettings) GetGroupFallbackSettings(context.Context) GroupFallbackSettings {
	d := DefaultGroupFallbackSettings()
	d.Enabled = s.enabled
	return d
}

type routeTestEnv struct {
	svc    *groupRouteService
	repo   *routeTestRepo
	groups *routeTestGroupRepo
	now    *time.Time
}

func newRouteTestEnv(enabled bool) *routeTestEnv {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	env := &routeTestEnv{
		repo: &routeTestRepo{primary: map[int64]int64{}},
		groups: &routeTestGroupRepo{groups: map[int64]*Group{
			1:  {ID: 1, Name: "main", Platform: PlatformOpenAI, Status: StatusActive},
			2:  {ID: 2, Name: "b", Platform: PlatformOpenAI, Status: StatusActive},
			3:  {ID: 3, Name: "c", Platform: PlatformOpenAI, Status: StatusActive},
			4:  {ID: 4, Name: "d", Platform: PlatformOpenAI, Status: StatusActive},
			5:  {ID: 5, Name: "e", Platform: PlatformOpenAI, Status: StatusActive},
			6:  {ID: 6, Name: "f", Platform: PlatformOpenAI, Status: StatusActive},
			7:  {ID: 7, Name: "g", Platform: PlatformOpenAI, Status: StatusActive},
			8:  {ID: 8, Name: "h", Platform: PlatformOpenAI, Status: StatusActive},
			9:  {ID: 9, Name: "i", Platform: PlatformOpenAI, Status: StatusActive},
			10: {ID: 10, Name: "j", Platform: PlatformOpenAI, Status: StatusActive},
			20: {ID: 20, Name: "excl", Platform: PlatformOpenAI, Status: StatusActive, IsExclusive: true},
			21: {ID: 21, Name: "off", Platform: PlatformOpenAI, Status: StatusDisabled},
			22: {ID: 22, Name: "anth", Platform: PlatformAnthropic, Status: StatusActive},
		}},
		now: &now,
	}
	env.svc = newGroupRouteService(env.repo, env.groups, routeTestSettings{enabled: enabled}, func() time.Time { return *env.now })
	return env
}

func routeTestKey(id, userID, groupID int64) *APIKey {
	gid := groupID
	k := &APIKey{ID: id, UserID: userID, GroupID: &gid}
	return k
}

func routeCode(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	var appErr *infraerrors.ApplicationError
	require.True(t, errors.As(err, &appErr), "expected ApplicationError, got %T: %v", err, err)
	return appErr.Reason
}

func routeGroupIDs(items []RouteItem) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.GroupID)
	}
	return out
}

func hopIDs(c Chain) []int64 {
	out := make([]int64, 0, len(c.Hops))
	for _, h := range c.Hops {
		out = append(out, h.GroupID)
	}
	return out
}

func hopSources(c Chain) []string {
	out := make([]string, 0, len(c.Hops))
	for _, h := range c.Hops {
		out = append(out, h.RouteSource)
	}
	return out
}

// --- ReplaceUserChain ---

func TestReplaceUserChain_SavesInOrderWithPlatform(t *testing.T) {
	env := newRouteTestEnv(true)
	user := &User{ID: 9}
	key := routeTestKey(100, 9, 1)

	require.NoError(t, env.svc.ReplaceUserChain(context.Background(), user, key, []int64{3, 2}))

	got, err := env.svc.GetUserChain(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, []int64{3, 2}, routeGroupIDs(got))
	for i, it := range got {
		require.Equal(t, i, it.Position)
		require.Equal(t, RouteSourceUser, it.Source)
		require.Equal(t, RoutePlacementTail, it.Placement)
		require.Equal(t, PlatformOpenAI, it.Platform)
	}
}

func TestReplaceUserChain_EmptyClears(t *testing.T) {
	env := newRouteTestEnv(true)
	user := &User{ID: 9}
	key := routeTestKey(100, 9, 1)
	require.NoError(t, env.svc.ReplaceUserChain(context.Background(), user, key, []int64{2}))
	require.NoError(t, env.svc.ReplaceUserChain(context.Background(), user, key, nil))
	got, err := env.svc.GetUserChain(context.Background(), 100)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestReplaceUserChain_Validation(t *testing.T) {
	user := &User{ID: 9}
	cases := []struct {
		name string
		ids  []int64
		code string
	}{
		{"too long", []int64{2, 3, 4, 5, 6, 7}, "FALLBACK_CHAIN_TOO_LONG"},
		{"duplicate", []int64{2, 3, 2}, "FALLBACK_GROUP_DUPLICATE"},
		{"contains primary", []int64{2, 1}, "FALLBACK_GROUP_IS_PRIMARY"},
		{"cross platform", []int64{22}, "FALLBACK_GROUP_PLATFORM_MISMATCH"},
		{"exclusive without grant", []int64{20}, "FALLBACK_GROUP_NOT_ALLOWED"},
		{"disabled group", []int64{21}, "FALLBACK_GROUP_UNAVAILABLE"},
		{"missing group", []int64{999}, "FALLBACK_GROUP_NOT_FOUND"},
		{"non positive id", []int64{0}, "FALLBACK_GROUP_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newRouteTestEnv(true)
			key := routeTestKey(100, 9, 1)
			err := env.svc.ReplaceUserChain(context.Background(), user, key, tc.ids)
			require.Equal(t, tc.code, routeCode(t, err))
			require.Empty(t, env.repo.rows, "validation failure must not write")
		})
	}
}

func TestReplaceUserChain_ExclusiveWithGrantAllowed(t *testing.T) {
	env := newRouteTestEnv(true)
	user := &User{ID: 9, AllowedGroups: []int64{20}}
	require.NoError(t, env.svc.ReplaceUserChain(context.Background(), user, routeTestKey(100, 9, 1), []int64{20}))
}

func TestReplaceUserChain_KeyNotGroupedOrNotOwned(t *testing.T) {
	env := newRouteTestEnv(true)
	user := &User{ID: 9}

	ungrouped := &APIKey{ID: 100, UserID: 9}
	require.Equal(t, "FALLBACK_KEY_NOT_GROUPED", routeCode(t, env.svc.ReplaceUserChain(context.Background(), user, ungrouped, []int64{2})))

	err := env.svc.ReplaceUserChain(context.Background(), user, routeTestKey(100, 10, 1), []int64{2})
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	require.ErrorIs(t, env.svc.ReplaceUserChain(context.Background(), nil, routeTestKey(100, 9, 1), nil), ErrAPIKeyNotFound)
}

func TestReplaceUserChain_PrimaryChangedConcurrentlyIsConflict(t *testing.T) {
	env := newRouteTestEnv(true)
	env.repo.primary[100] = 3 // 库里的主分组已被改成 3，调用方手里的 key 还是 1
	err := env.svc.ReplaceUserChain(context.Background(), &User{ID: 9}, routeTestKey(100, 9, 1), []int64{2})
	require.Equal(t, "FALLBACK_KEY_CHANGED", routeCode(t, err))
}

// 用户提交的分组与管理员隐藏链撞了：照常保存，成功响应与没有隐藏链时完全一致，
// admin 行不受影响；用户链长度只统计 user 行。
func TestReplaceUserChain_CollisionWithHiddenChainIsInvisible(t *testing.T) {
	user := &User{ID: 9}

	plain := newRouteTestEnv(true)
	require.NoError(t, plain.svc.ReplaceUserChain(context.Background(), user, routeTestKey(100, 9, 1), []int64{2, 3, 4, 5, 6}))
	plainGot, err := plain.svc.GetUserChain(context.Background(), 100)
	require.NoError(t, err)

	hidden := newRouteTestEnv(true)
	hidden.repo.seed(
		RouteItem{APIKeyID: 100, GroupID: 2, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementHead, Position: 0, Note: "n"},
		RouteItem{APIKeyID: 100, GroupID: 3, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementTail, Position: 0, Note: "n"},
		RouteItem{APIKeyID: 100, GroupID: 7, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementTail, Position: 1, Note: "n"},
	)
	// 5 个用户项 + 3 个 admin 项：用户链上限只数 user 行，仍然通过
	require.NoError(t, hidden.svc.ReplaceUserChain(context.Background(), user, routeTestKey(100, 9, 1), []int64{2, 3, 4, 5, 6}))
	hiddenGot, err := hidden.svc.GetUserChain(context.Background(), 100)
	require.NoError(t, err)

	require.Equal(t, routeGroupIDs(plainGot), routeGroupIDs(hiddenGot))
	require.Len(t, hidden.repo.bySource(100, RouteSourceAdmin), 3, "admin rows must be untouched")
	for _, it := range hiddenGot {
		require.Equal(t, RouteSourceUser, it.Source, "user view must never contain admin rows")
	}
}

func TestReplaceUserChain_InvalidatesCache(t *testing.T) {
	env := newRouteTestEnv(true)
	user := &User{ID: 9}
	key := routeTestKey(100, 9, 1)
	got, err := env.svc.GetUserChain(context.Background(), 100)
	require.NoError(t, err)
	require.Empty(t, got)
	require.NoError(t, env.svc.ReplaceUserChain(context.Background(), user, key, []int64{2}))
	got, err = env.svc.GetUserChain(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, []int64{2}, routeGroupIDs(got))
}

// --- ReplaceHiddenChain ---

func TestReplaceHiddenChain_SavesHeadAndTail(t *testing.T) {
	env := newRouteTestEnv(true)
	key := routeTestKey(100, 9, 1)
	require.NoError(t, env.svc.ReplaceHiddenChain(context.Background(), 77, key, []int64{2}, []int64{3, 4}, "  513 pin  "))

	head, tail, err := env.svc.GetHiddenChain(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, []int64{2}, routeGroupIDs(head))
	require.Equal(t, []int64{3, 4}, routeGroupIDs(tail))
	for _, it := range append(head, tail...) {
		require.Equal(t, RouteSourceAdmin, it.Source)
		require.Equal(t, "513 pin", it.Note)
		require.NotNil(t, it.CreatedBy)
		require.Equal(t, int64(77), *it.CreatedBy)
	}
	require.Equal(t, RoutePlacementHead, head[0].Placement)
	require.Equal(t, []int{0, 1}, []int{tail[0].Position, tail[1].Position})
}

func TestReplaceHiddenChain_AdminExemptFromCanBindGroupOnly(t *testing.T) {
	env := newRouteTestEnv(true)
	key := routeTestKey(100, 9, 1)
	// 专属分组 20：admin 项豁免授权检查
	require.NoError(t, env.svc.ReplaceHiddenChain(context.Background(), 77, key, nil, []int64{20}, ""))

	// 其余校验照做
	require.Equal(t, "FALLBACK_GROUP_UNAVAILABLE", routeCode(t, env.svc.ReplaceHiddenChain(context.Background(), 77, key, nil, []int64{21}, "")))
	require.Equal(t, "FALLBACK_GROUP_PLATFORM_MISMATCH", routeCode(t, env.svc.ReplaceHiddenChain(context.Background(), 77, key, nil, []int64{22}, "")))
	require.Equal(t, "FALLBACK_GROUP_IS_PRIMARY", routeCode(t, env.svc.ReplaceHiddenChain(context.Background(), 77, key, nil, []int64{1}, "")))
}

func TestReplaceHiddenChain_Limits(t *testing.T) {
	env := newRouteTestEnv(true)
	key := routeTestKey(100, 9, 1)
	ctx := context.Background()

	require.Equal(t, "FALLBACK_CHAIN_TOO_LONG", routeCode(t, env.svc.ReplaceHiddenChain(ctx, 77, key, []int64{2, 3, 4, 5, 6, 7}, nil, "n")))
	require.Equal(t, "FALLBACK_CHAIN_TOO_LONG", routeCode(t, env.svc.ReplaceHiddenChain(ctx, 77, key, nil, []int64{2, 3, 4, 5, 6, 7}, "")))
	// head 与 tail 各 5 项允许（总数 10，有效链运行时再截断）
	require.NoError(t, env.svc.ReplaceHiddenChain(ctx, 77, key, []int64{2, 3, 4, 5, 6}, []int64{7, 8, 9, 10, 20}, "n"))
}

func TestReplaceHiddenChain_NoteAndDuplicates(t *testing.T) {
	env := newRouteTestEnv(true)
	key := routeTestKey(100, 9, 1)
	ctx := context.Background()

	require.Equal(t, "FALLBACK_NOTE_REQUIRED", routeCode(t, env.svc.ReplaceHiddenChain(ctx, 77, key, []int64{2}, nil, "  ")))
	long := make([]rune, MaxRouteNoteLen+1)
	for i := range long {
		long[i] = 'x'
	}
	require.Equal(t, "FALLBACK_NOTE_TOO_LONG", routeCode(t, env.svc.ReplaceHiddenChain(ctx, 77, key, nil, []int64{2}, string(long))))
	// head 与 tail 里同一分组也是重复（唯一约束 api_key_id+source+group_id）
	require.Equal(t, "FALLBACK_GROUP_DUPLICATE", routeCode(t, env.svc.ReplaceHiddenChain(ctx, 77, key, []int64{2}, []int64{2}, "n")))
	// 只有 tail 时不要求 note
	require.NoError(t, env.svc.ReplaceHiddenChain(ctx, 77, key, nil, []int64{2}, ""))
}

func TestReplaceHiddenChain_DoesNotTouchUserRows(t *testing.T) {
	env := newRouteTestEnv(true)
	key := routeTestKey(100, 9, 1)
	require.NoError(t, env.svc.ReplaceUserChain(context.Background(), &User{ID: 9}, key, []int64{2, 3}))
	require.NoError(t, env.svc.ReplaceHiddenChain(context.Background(), 77, key, nil, []int64{4}, ""))
	require.Len(t, env.repo.bySource(100, RouteSourceUser), 2)
	require.NoError(t, env.svc.ReplaceHiddenChain(context.Background(), 77, key, nil, nil, ""))
	require.Len(t, env.repo.bySource(100, RouteSourceUser), 2)
	require.Empty(t, env.repo.bySource(100, RouteSourceAdmin))
}

func TestReplaceHiddenChain_KeyNotGrouped(t *testing.T) {
	env := newRouteTestEnv(true)
	require.Equal(t, "FALLBACK_KEY_NOT_GROUPED", routeCode(t, env.svc.ReplaceHiddenChain(context.Background(), 1, &APIKey{ID: 1}, nil, []int64{2}, "")))
	require.ErrorIs(t, env.svc.ReplaceHiddenChain(context.Background(), 1, nil, nil, nil, ""), ErrAPIKeyNotFound)
}

// --- OnPrimaryGroupChanged ---

func TestOnPrimaryGroupChanged_DelegatesAndInvalidates(t *testing.T) {
	env := newRouteTestEnv(true)
	key := routeTestKey(100, 9, 1)
	env.repo.seed(RouteItem{APIKeyID: 100, GroupID: 2, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail})
	_, err := env.svc.GetUserChain(context.Background(), 100) // 填缓存
	require.NoError(t, err)

	require.NoError(t, env.svc.OnPrimaryGroupChanged(context.Background(), key, &Group{ID: 2, Platform: PlatformOpenAI}))
	require.Equal(t, []applyCall{{100, 2, PlatformOpenAI}}, env.repo.applied)

	before := env.repo.listCalls
	_, err = env.svc.GetUserChain(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, before+1, env.repo.listCalls, "cache must be invalidated")

	require.NoError(t, env.svc.OnPrimaryGroupChanged(context.Background(), nil, nil))
	require.Len(t, env.repo.applied, 1)
}

// --- ResolveEffectiveChain ---

func TestResolveEffectiveChain_SwitchOffReturnsEmpty(t *testing.T) {
	env := newRouteTestEnv(false)
	env.repo.seed(RouteItem{APIKeyID: 100, GroupID: 2, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail})
	chain, err := env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9}, ResolveOptions{})
	require.NoError(t, err)
	require.Empty(t, chain.Hops)
	require.Zero(t, env.repo.listCalls, "switch off must not even read the table")

	// 管理端 dry-run 可以忽略开关
	chain, err = env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9}, ResolveOptions{IgnoreSwitch: true})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, hopIDs(chain))
}

func TestResolveEffectiveChain_NilSettingsIsDisabled(t *testing.T) {
	env := newRouteTestEnv(true)
	svc := newGroupRouteService(env.repo, env.groups, nil, nil)
	chain, err := svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9}, ResolveOptions{})
	require.NoError(t, err)
	require.Empty(t, chain.Hops)
}

func TestResolveEffectiveChain_UngroupedKeyIsEmpty(t *testing.T) {
	env := newRouteTestEnv(true)
	chain, err := env.svc.ResolveEffectiveChain(context.Background(), &APIKey{ID: 100, UserID: 9}, &User{ID: 9}, ResolveOptions{})
	require.NoError(t, err)
	require.Empty(t, chain.Hops)
	chain, err = env.svc.ResolveEffectiveChain(context.Background(), nil, nil, ResolveOptions{})
	require.NoError(t, err)
	require.Empty(t, chain.Hops)
}

func TestResolveEffectiveChain_NoRoutesIsPrimaryOnly(t *testing.T) {
	env := newRouteTestEnv(true)
	chain, err := env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9}, ResolveOptions{})
	require.NoError(t, err)
	require.Equal(t, []int64{1}, hopIDs(chain))
	require.Equal(t, []string{RouteSourcePrimary}, hopSources(chain))
	require.Nil(t, chain.Hops[0].ServedRouteSourceValue())
}

func TestResolveEffectiveChain_MergeOrder(t *testing.T) {
	env := newRouteTestEnv(true)
	env.repo.seed(
		RouteItem{APIKeyID: 100, GroupID: 8, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementTail, Position: 1},
		RouteItem{APIKeyID: 100, GroupID: 7, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementTail, Position: 0},
		RouteItem{APIKeyID: 100, GroupID: 3, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail, Position: 1},
		RouteItem{APIKeyID: 100, GroupID: 2, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail, Position: 0},
		RouteItem{APIKeyID: 100, GroupID: 5, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementHead, Position: 1},
		RouteItem{APIKeyID: 100, GroupID: 4, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementHead, Position: 0},
	)
	chain, err := env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9}, ResolveOptions{})
	require.NoError(t, err)
	// 候选 8 项：admin.head(4,5) + 主分组(1) + user.tail(2,3) + admin.tail(7,8)，截断到前 6 项
	require.Equal(t, []int64{4, 5, 1, 2, 3, 7}, hopIDs(chain))
	require.True(t, chain.Truncated, "8 candidates must be truncated to 6")
	require.Equal(t, []string{RouteSourceAdmin, RouteSourceAdmin, RouteSourcePrimary, RouteSourceUser, RouteSourceUser, RouteSourceAdmin}, hopSources(chain))
	require.Equal(t, RoutePlacementHead, chain.Hops[0].Placement)
	require.NotNil(t, chain.Hops[3].Group)
	require.Equal(t, int64(2), chain.Hops[3].Group.ID)

	require.Equal(t, int16(ServedRouteSourceAdminChain), *chain.Hops[0].ServedRouteSourceValue())
	require.Equal(t, int16(ServedRouteSourceUserChain), *chain.Hops[3].ServedRouteSourceValue())
}

func TestResolveEffectiveChain_NotTruncatedWithinLimit(t *testing.T) {
	env := newRouteTestEnv(true)
	env.repo.seed(
		RouteItem{APIKeyID: 100, GroupID: 2, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail, Position: 0},
		RouteItem{APIKeyID: 100, GroupID: 3, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail, Position: 1},
		RouteItem{APIKeyID: 100, GroupID: 4, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail, Position: 2},
		RouteItem{APIKeyID: 100, GroupID: 5, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail, Position: 3},
		RouteItem{APIKeyID: 100, GroupID: 6, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail, Position: 4},
	)
	chain, err := env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9}, ResolveOptions{})
	require.NoError(t, err)
	require.Len(t, chain.Hops, 6)
	require.False(t, chain.Truncated)
}

// 先资格过滤后去重：失去授权的 user 项排在前面，不能把同分组、本来免授权的 admin 项去重掉。
func TestResolveEffectiveChain_FilterBeforeDedupe(t *testing.T) {
	env := newRouteTestEnv(true)
	env.repo.seed(
		RouteItem{APIKeyID: 100, GroupID: 20, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail, Position: 0},
		RouteItem{APIKeyID: 100, GroupID: 20, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementTail, Position: 0},
	)
	// 用户没有专属分组 20 的授权
	chain, err := env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9}, ResolveOptions{})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 20}, hopIDs(chain))
	require.Equal(t, []string{RouteSourcePrimary, RouteSourceAdmin}, hopSources(chain))
	require.Equal(t, []SkippedRoute{{GroupID: 20, Source: RouteSourceUser, Reason: RouteSkipNotAllowed}}, chain.Skipped)

	// 有授权时：user 项先出现，admin 项被去重，来源记 user
	chain, err = env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9, AllowedGroups: []int64{20}}, ResolveOptions{})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 20}, hopIDs(chain))
	require.Equal(t, []string{RouteSourcePrimary, RouteSourceUser}, hopSources(chain))
	require.Empty(t, chain.Skipped)
}

func TestResolveEffectiveChain_AdminExemptOnlyFromCanBindGroup(t *testing.T) {
	env := newRouteTestEnv(true)
	env.repo.seed(
		RouteItem{APIKeyID: 100, GroupID: 20, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementTail, Position: 0}, // 专属，免授权
		RouteItem{APIKeyID: 100, GroupID: 21, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementTail, Position: 1}, // 已停用
		RouteItem{APIKeyID: 100, GroupID: 999, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementTail, Position: 2},
	)
	chain, err := env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9}, ResolveOptions{})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 20}, hopIDs(chain))
	require.ElementsMatch(t, []SkippedRoute{
		{GroupID: 21, Source: RouteSourceAdmin, Reason: RouteSkipGroupInactive},
		{GroupID: 999, Source: RouteSourceAdmin, Reason: RouteSkipGroupMissing},
	}, chain.Skipped)
}

// 管理员事后改了分组平台、或主分组换了平台：行 platform 与主分组不一致 => invalid_platform。
func TestResolveEffectiveChain_InvalidPlatform(t *testing.T) {
	env := newRouteTestEnv(true)
	env.repo.seed(
		RouteItem{APIKeyID: 100, GroupID: 22, Platform: PlatformAnthropic, Source: RouteSourceAdmin, Placement: RoutePlacementTail, Position: 0}, // 分组平台 != 主分组
		RouteItem{APIKeyID: 100, GroupID: 2, Platform: PlatformAnthropic, Source: RouteSourceAdmin, Placement: RoutePlacementTail, Position: 1},  // 行平台 != 主分组
	)
	chain, err := env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9}, ResolveOptions{})
	require.NoError(t, err)
	require.Equal(t, []int64{1}, hopIDs(chain))
	require.ElementsMatch(t, []SkippedRoute{
		{GroupID: 22, Source: RouteSourceAdmin, Reason: RouteSkipInvalidPlatform},
		{GroupID: 2, Source: RouteSourceAdmin, Reason: RouteSkipInvalidPlatform},
	}, chain.Skipped)
}

func TestResolveEffectiveChain_UsesKeyUserWhenUserNil(t *testing.T) {
	env := newRouteTestEnv(true)
	env.repo.seed(RouteItem{APIKeyID: 100, GroupID: 20, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail})
	key := routeTestKey(100, 9, 1)
	key.User = &User{ID: 9, AllowedGroups: []int64{20}}
	chain, err := env.svc.ResolveEffectiveChain(context.Background(), key, nil, ResolveOptions{})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 20}, hopIDs(chain))

	// user 缺失：user 项一律视为无权限
	chain, err = env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), nil, ResolveOptions{})
	require.NoError(t, err)
	require.Equal(t, []int64{1}, hopIDs(chain))
}

func TestResolveEffectiveChain_PrimaryDedupeAndErrors(t *testing.T) {
	env := newRouteTestEnv(true)
	// 脏数据：admin head 里残留了主分组本身，首次出现的存活项保留，主分组不会出现两次
	env.repo.seed(RouteItem{APIKeyID: 100, GroupID: 1, Platform: PlatformOpenAI, Source: RouteSourceAdmin, Placement: RoutePlacementHead})
	chain, err := env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9}, ResolveOptions{})
	require.NoError(t, err)
	require.Equal(t, []int64{1}, hopIDs(chain))

	// 主分组读取失败 / 链读取失败：返回错误，由调用方走原逻辑
	_, err = env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 404), &User{ID: 9}, ResolveOptions{})
	require.ErrorIs(t, err, ErrGroupNotFound)
	env.repo.listErr = errors.New("db down")
	env.svc.invalidate(100)
	_, err = env.svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9}, ResolveOptions{})
	require.Error(t, err)
}

func TestResolveEffectiveChain_CacheTTL(t *testing.T) {
	env := newRouteTestEnv(true)
	key := routeTestKey(100, 9, 1)
	user := &User{ID: 9}
	_, err := env.svc.ResolveEffectiveChain(context.Background(), key, user, ResolveOptions{})
	require.NoError(t, err)
	_, err = env.svc.ResolveEffectiveChain(context.Background(), key, user, ResolveOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, env.repo.listCalls, "second call within TTL must hit the cache")

	*env.now = env.now.Add(groupRouteCacheTTL + time.Second)
	_, err = env.svc.ResolveEffectiveChain(context.Background(), key, user, ResolveOptions{})
	require.NoError(t, err)
	require.Equal(t, 2, env.repo.listCalls, "expired entry must be reloaded")
}

func TestGroupRouteCache_BoundedAndCopySafe(t *testing.T) {
	env := newRouteTestEnv(true)
	env.repo.seed(RouteItem{APIKeyID: 100, GroupID: 2, Platform: PlatformOpenAI, Source: RouteSourceUser, Placement: RoutePlacementTail})
	got, err := env.svc.GetUserChain(context.Background(), 100)
	require.NoError(t, err)
	got[0].GroupID = 999 // 修改返回值不得污染缓存
	again, err := env.svc.GetUserChain(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, int64(2), again[0].GroupID)

	for i := int64(0); i < groupRouteCacheMaxEntries+5; i++ {
		_, err := env.svc.GetUserChain(context.Background(), 1000+i)
		require.NoError(t, err)
	}
	require.LessOrEqual(t, len(env.svc.cache), groupRouteCacheMaxEntries)
}

func TestNewGroupRouteService_NilSettingServiceDisabled(t *testing.T) {
	env := newRouteTestEnv(true)
	svc := NewGroupRouteService(env.repo, env.groups, nil)
	chain, err := svc.ResolveEffectiveChain(context.Background(), routeTestKey(100, 9, 1), &User{ID: 9}, ResolveOptions{})
	require.NoError(t, err)
	require.Empty(t, chain.Hops)
}
