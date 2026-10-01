package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// groupRouteCacheTTL 是链明细的进程内缓存时间；写入后本实例主动失效，多实例最长陈旧一个 TTL。
const groupRouteCacheTTL = 10 * time.Second

// groupRouteCacheMaxEntries 限制缓存条目数，超出时整体清空（链读取很便宜，不值得做 LRU）。
const groupRouteCacheMaxEntries = 20000

// GroupRouteService 管理 Key 级分组回退链。
//
// PR1 阶段只被测试调用，不接入任何 handler、中间件、网关或计费路径。
type GroupRouteService interface {
	// GetUserChain 返回用户链（只含 source='user'）。调用方（handler）负责校验 Key 归属。
	GetUserChain(ctx context.Context, keyID int64) ([]RouteItem, error)
	// ReplaceUserChain 校验并整体替换用户链；空数组表示清空。不触碰 admin 行。
	// 用户提交的分组与隐藏链撞了照常保存，错误码与成功响应都不依赖 admin 行。
	ReplaceUserChain(ctx context.Context, user *User, key *APIKey, groupIDs []int64) error
	// GetHiddenChain 返回管理员隐藏链，仅供管理端使用。
	GetHiddenChain(ctx context.Context, keyID int64) (head, tail []RouteItem, err error)
	// ReplaceHiddenChain 校验并整体替换管理员隐藏链。管理员项只豁免 CanBindGroup，其余校验照做。
	ReplaceHiddenChain(ctx context.Context, adminID int64, key *APIKey, head, tail []int64, note string) error
	// OnPrimaryGroupChanged 在 Key 主分组变更之后调用：删除与新主分组重复的项；换平台则清空 user 链。
	OnPrimaryGroupChanged(ctx context.Context, key *APIKey, newGroup *Group) error
	// ResolveEffectiveChain 解析有效链，dry-run 与运行时同一函数。
	ResolveEffectiveChain(ctx context.Context, key *APIKey, user *User, opt ResolveOptions) (Chain, error)
}

type groupFallbackSettingsReader interface {
	GetGroupFallbackSettings(ctx context.Context) GroupFallbackSettings
}

type groupRouteCacheEntry struct {
	items   []RouteItem
	expires time.Time
}

type groupRouteService struct {
	repo     APIKeyGroupRouteRepository
	groups   GroupRepository
	settings groupFallbackSettingsReader
	now      func() time.Time

	mu    sync.Mutex
	cache map[int64]groupRouteCacheEntry
	// gen 是按 Key 的代际计数：invalidate 时加一，listCached 回填前比对，
	// 读库期间发生过失效就不写回，避免把读到的旧链回填成缓存。
	// 条目只随写入过链的 Key 增长，不随 cache 的整体清空而清空（否则会丢失失效信号）。
	gen map[int64]uint64
}

// NewGroupRouteService 创建回退链服务。settingService 为 nil 时视为开关关闭。
func NewGroupRouteService(repo APIKeyGroupRouteRepository, groupRepo GroupRepository, settingService *SettingService) GroupRouteService {
	var reader groupFallbackSettingsReader
	if settingService != nil {
		reader = settingService
	}
	return newGroupRouteService(repo, groupRepo, reader, time.Now)
}

func newGroupRouteService(repo APIKeyGroupRouteRepository, groupRepo GroupRepository, settings groupFallbackSettingsReader, now func() time.Time) *groupRouteService {
	if now == nil {
		now = time.Now
	}
	return &groupRouteService{
		repo:     repo,
		groups:   groupRepo,
		settings: settings,
		now:      now,
		cache:    make(map[int64]groupRouteCacheEntry),
		gen:      make(map[int64]uint64),
	}
}

// --- 读 ---

func (s *groupRouteService) GetUserChain(ctx context.Context, keyID int64) ([]RouteItem, error) {
	// 用户端读取不走缓存：SQL 层强制 source='user'，隐藏链不会经由缓存或过滤逻辑泄露。
	items, err := s.repo.ListByKey(ctx, keyID, RouteSourceUser)
	if err != nil {
		return nil, err
	}
	return filterRouteItems(items, RouteSourceUser, ""), nil
}

func (s *groupRouteService) GetHiddenChain(ctx context.Context, keyID int64) (head, tail []RouteItem, err error) {
	items, err := s.listCached(ctx, keyID)
	if err != nil {
		return nil, nil, err
	}
	return filterRouteItems(items, RouteSourceAdmin, RoutePlacementHead),
		filterRouteItems(items, RouteSourceAdmin, RoutePlacementTail), nil
}

// listCached 读取某把 Key 的全部链项（含两种 source），走 10 秒进程内缓存。
// 返回的切片是副本，调用方可任意修改。
func (s *groupRouteService) listCached(ctx context.Context, keyID int64) ([]RouteItem, error) {
	now := s.now()
	s.mu.Lock()
	if e, ok := s.cache[keyID]; ok && now.Before(e.expires) {
		out := append([]RouteItem(nil), e.items...)
		s.mu.Unlock()
		return out, nil
	}
	gen := s.gen[keyID]
	s.mu.Unlock()

	items, err := s.repo.ListByKey(ctx, keyID, "")
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	// 读库期间该 Key 被失效过：本次读到的可能是旧链，只返回、不回填。
	if s.gen[keyID] == gen {
		if len(s.cache) >= groupRouteCacheMaxEntries {
			s.cache = make(map[int64]groupRouteCacheEntry)
		}
		s.cache[keyID] = groupRouteCacheEntry{items: append([]RouteItem(nil), items...), expires: now.Add(groupRouteCacheTTL)}
	}
	s.mu.Unlock()
	return items, nil
}

func (s *groupRouteService) invalidate(keyID int64) {
	s.mu.Lock()
	delete(s.cache, keyID)
	s.gen[keyID]++
	s.mu.Unlock()
}

// filterRouteItems 按 source（和可选的 placement）过滤，并按 position 升序返回。
func filterRouteItems(items []RouteItem, source, placement string) []RouteItem {
	out := make([]RouteItem, 0, len(items))
	for _, it := range items {
		if it.Source != source {
			continue
		}
		if placement != "" && it.Placement != placement {
			continue
		}
		out = append(out, it)
	}
	// 项数 <= 5，插入排序足够
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Position > out[j].Position; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// --- 写 ---

func (s *groupRouteService) ReplaceUserChain(ctx context.Context, user *User, key *APIKey, groupIDs []int64) error {
	if user == nil || key == nil || key.UserID != user.ID {
		return ErrAPIKeyNotFound
	}
	if key.GroupID == nil || *key.GroupID <= 0 {
		return ErrFallbackKeyNotGrouped
	}
	// 长度与重复只统计用户提交的这一组（source='user'），与 admin 行无关。
	if len(groupIDs) > MaxUserFallbackItems {
		return ErrFallbackChainTooLong
	}
	primary, err := s.groups.GetByIDLite(ctx, *key.GroupID)
	if err != nil {
		return err
	}
	if err := s.validateGroupIDs(ctx, primary, groupIDs, user); err != nil {
		return err
	}

	items := make([]RouteItem, 0, len(groupIDs))
	for i, gid := range groupIDs {
		items = append(items, RouteItem{
			GroupID:   gid,
			Platform:  primary.Platform,
			Source:    RouteSourceUser,
			Placement: RoutePlacementTail,
			Position:  i,
		})
	}
	defer s.invalidate(key.ID)
	return s.repo.ReplaceChain(ctx, ReplaceRoutesParams{
		APIKeyID:               key.ID,
		Source:                 RouteSourceUser,
		ExpectedPrimaryGroupID: primary.ID,
		Items:                  items,
	})
}

func (s *groupRouteService) ReplaceHiddenChain(ctx context.Context, adminID int64, key *APIKey, head, tail []int64, note string) error {
	if key == nil {
		return ErrAPIKeyNotFound
	}
	if key.GroupID == nil || *key.GroupID <= 0 {
		return ErrFallbackKeyNotGrouped
	}
	if len(head) > MaxAdminFallbackItemsPerPlacement || len(tail) > MaxAdminFallbackItemsPerPlacement {
		return ErrFallbackChainTooLong
	}
	note = strings.TrimSpace(note)
	if len([]rune(note)) > MaxRouteNoteLen {
		return ErrFallbackNoteTooLong
	}
	// head 会让用户编辑器上看到的主分组与实际计费分组不一致，必须留下说明。
	if len(head) > 0 && note == "" {
		return ErrFallbackNoteRequired
	}
	primary, err := s.groups.GetByIDLite(ctx, *key.GroupID)
	if err != nil {
		return err
	}
	// head 与 tail 合起来不得重复（uq: api_key_id, source, group_id）。admin 项豁免 CanBindGroup（user 传 nil）。
	all := make([]int64, 0, len(head)+len(tail))
	all = append(all, head...)
	all = append(all, tail...)
	if err := s.validateGroupIDs(ctx, primary, all, nil); err != nil {
		return err
	}

	var createdBy *int64
	if adminID > 0 {
		id := adminID
		createdBy = &id
	}
	items := make([]RouteItem, 0, len(all))
	build := func(ids []int64, placement string) {
		for i, gid := range ids {
			items = append(items, RouteItem{
				GroupID:   gid,
				Platform:  primary.Platform,
				Source:    RouteSourceAdmin,
				Placement: placement,
				Position:  i,
				Note:      note,
				CreatedBy: createdBy,
			})
		}
	}
	build(head, RoutePlacementHead)
	build(tail, RoutePlacementTail)

	defer s.invalidate(key.ID)
	return s.repo.ReplaceChain(ctx, ReplaceRoutesParams{
		APIKeyID:               key.ID,
		Source:                 RouteSourceAdmin,
		ExpectedPrimaryGroupID: primary.ID,
		Items:                  items,
	})
}

// validateGroupIDs 校验一组兜底分组。user 非 nil 时额外要求 CanBindGroup（admin 项传 nil 豁免）。
// 错误只取决于本次提交的分组本身，不读取、不依赖另一个 source 的行。
func (s *groupRouteService) validateGroupIDs(ctx context.Context, primary *Group, ids []int64, user *User) error {
	seen := make(map[int64]struct{}, len(ids))
	for _, gid := range ids {
		if gid <= 0 {
			return ErrFallbackGroupNotFound
		}
		if gid == primary.ID {
			return ErrFallbackGroupIsPrimary
		}
		if _, dup := seen[gid]; dup {
			return ErrFallbackGroupDup
		}
		seen[gid] = struct{}{}

		g, err := s.groups.GetByIDLite(ctx, gid)
		if err != nil {
			if errors.Is(err, ErrGroupNotFound) {
				return ErrFallbackGroupNotFound
			}
			return err
		}
		if !g.IsActive() {
			return ErrFallbackGroupUnavailable
		}
		if g.Platform != primary.Platform {
			return ErrFallbackPlatformMismatch
		}
		if user != nil && !user.CanBindGroup(g.ID, g.IsExclusive) {
			return ErrFallbackGroupNotAllowed
		}
	}
	return nil
}

func (s *groupRouteService) OnPrimaryGroupChanged(ctx context.Context, key *APIKey, newGroup *Group) error {
	if key == nil || newGroup == nil {
		return nil
	}
	defer s.invalidate(key.ID)
	return s.repo.ApplyPrimaryGroupChange(ctx, key.ID, newGroup.ID, newGroup.Platform)
}

// --- 有效链 ---

type chainCandidate struct {
	item        RouteItem
	routeSource string
	placement   string
	isPrimary   bool
}

func (s *groupRouteService) ResolveEffectiveChain(ctx context.Context, key *APIKey, user *User, opt ResolveOptions) (Chain, error) {
	var chain Chain
	if key == nil || key.GroupID == nil || *key.GroupID <= 0 {
		return chain, nil
	}
	if !opt.IgnoreSwitch {
		// 开关关闭（或未注入设置）返回空链：调用方走原逻辑。
		if s.settings == nil || !s.settings.GetGroupFallbackSettings(ctx).Enabled {
			return chain, nil
		}
	}
	if user == nil {
		user = key.User
	}

	primary, err := s.groups.GetByIDLite(ctx, *key.GroupID)
	if err != nil {
		return chain, err
	}
	items, err := s.listCached(ctx, key.ID)
	if err != nil {
		return chain, err
	}

	// 候选顺序：admin.head + 主分组 + user.tail + admin.tail，各自按 position。
	var cands []chainCandidate
	for _, it := range filterRouteItems(items, RouteSourceAdmin, RoutePlacementHead) {
		cands = append(cands, chainCandidate{item: it, routeSource: RouteSourceAdmin, placement: RoutePlacementHead})
	}
	cands = append(cands, chainCandidate{
		item:        RouteItem{GroupID: primary.ID, Platform: primary.Platform},
		routeSource: RouteSourcePrimary,
		isPrimary:   true,
	})
	for _, it := range filterRouteItems(items, RouteSourceUser, RoutePlacementTail) {
		cands = append(cands, chainCandidate{item: it, routeSource: RouteSourceUser, placement: RoutePlacementTail})
	}
	for _, it := range filterRouteItems(items, RouteSourceAdmin, RoutePlacementTail) {
		cands = append(cands, chainCandidate{item: it, routeSource: RouteSourceAdmin, placement: RoutePlacementTail})
	}

	// 先逐项做资格过滤，再按 group_id 去重（保留首次出现的存活项）。
	// 顺序不能反：否则失去授权的 user 项会把同分组、本来免授权的 admin 项去重掉。
	seen := make(map[int64]struct{}, len(cands))
	var hops []ChainHop
	for _, c := range cands {
		var group *Group
		// 脏数据：链里残留了主分组本身（改主分组与 ApplyPrimaryGroupChange 之间的窗口）。
		// 直接丢弃，保证主分组那一跳永远是 primary 来源。
		if !c.isPrimary && c.item.GroupID == primary.ID {
			continue
		}
		if c.isPrimary {
			group = primary
		} else {
			g, reason, gerr := s.eligibleGroup(ctx, primary, c, user)
			if gerr != nil {
				return Chain{}, gerr
			}
			if reason != "" {
				chain.Skipped = append(chain.Skipped, SkippedRoute{GroupID: c.item.GroupID, Source: c.routeSource, Reason: reason})
				continue
			}
			group = g
		}
		if _, dup := seen[group.ID]; dup {
			continue
		}
		seen[group.ID] = struct{}{}
		hops = append(hops, ChainHop{GroupID: group.ID, Group: group, RouteSource: c.routeSource, Placement: c.placement})
	}

	if len(hops) > MaxEffectiveChainLen {
		hops = truncateHops(hops, MaxEffectiveChainLen)
		chain.Truncated = true
	}
	chain.Hops = hops
	return chain, nil
}

// eligibleGroup 对一个兜底项做静态资格检查：分组存在且 active、平台与主分组一致、
// user 项通过 CanBindGroup（admin 项只豁免这一条）。返回 reason 非空表示应跳过。
func (s *groupRouteService) eligibleGroup(ctx context.Context, primary *Group, c chainCandidate, user *User) (*Group, string, error) {
	g, err := s.groups.GetByIDLite(ctx, c.item.GroupID)
	if err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			return nil, RouteSkipGroupMissing, nil
		}
		return nil, "", err
	}
	if !g.IsActive() {
		return nil, RouteSkipGroupInactive, nil
	}
	if g.Platform != primary.Platform || c.item.Platform != primary.Platform {
		return nil, RouteSkipInvalidPlatform, nil
	}
	if c.routeSource == RouteSourceUser && (user == nil || !user.CanBindGroup(g.ID, g.IsExclusive)) {
		return nil, RouteSkipNotAllowed, nil
	}
	return g, "", nil
}

// truncateHops 把链截到 limit 项（设计 2.2）：优先保留 admin 项，先从 user 链尾部开始丢；
// user 项丢完仍超长，再从整条链末尾截。
func truncateHops(hops []ChainHop, limit int) []ChainHop {
	excess := len(hops) - limit
	if excess <= 0 {
		return hops
	}
	drop := make(map[int]struct{}, excess)
	for i := len(hops) - 1; i >= 0 && len(drop) < excess; i-- {
		if hops[i].RouteSource == RouteSourceUser {
			drop[i] = struct{}{}
		}
	}
	out := make([]ChainHop, 0, limit)
	for i, h := range hops {
		if _, ok := drop[i]; !ok {
			out = append(out, h)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
