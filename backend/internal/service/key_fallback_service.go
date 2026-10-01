package service

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// KeyFallbackService 是 Key 级分组回退链 REST 接口（用户端 + 管理端）的编排层：
// 校验 Key 归属、组装响应、接入参考价。链的校验与读写仍由 GroupRouteService（PR1）负责。
//
// 总开关 group_fallback.enabled 关闭时：接口照常可用、链照常保存（并在响应里带 enabled=false），
// 只是运行时不生效——开关只影响 ResolveEffectiveChain（运行时），不影响这里。

// KeyFallbackStatus* 是链项在编辑器里的状态。
const (
	KeyFallbackStatusActive      = "active"
	KeyFallbackStatusDisabled    = "disabled"
	KeyFallbackStatusUnavailable = "unavailable"
)

// 用户端链项角色。用户端没有「管理员」概念。
const (
	KeyFallbackRolePrimary  = "primary"
	KeyFallbackRoleFallback = "fallback"
)

// keyFallbackListPageSize 是批量读取时一次取回的 Key 数上限。
const keyFallbackListPageSize = 1000

// keyFallbackRoutesByGroupLimit 是反查接口最多返回的条数。
const keyFallbackRoutesByGroupLimit = 200

// --- 用户端响应 ---
// 这些类型里没有任何管理员隐藏链的字段：用户端响应由它们构造，从类型上杜绝泄露。

// KeyFallbackItemView 是用户端链上的一项。
type KeyFallbackItemView struct {
	GroupID  int64  `json:"group_id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Position int    `json:"position"`
	// Status：active / disabled / unavailable（分组被禁用、平台不一致、用户已失去专属授权）。
	Status string `json:"status"`
	// Usable=false 的项仍然展示（让用户能删除），运行时会被跳过。
	Usable              bool     `json:"usable"`
	RateMultiplier      float64  `json:"rate_multiplier"`
	UserRateMultiplier  *float64 `json:"user_rate_multiplier"`
	EffectiveMultiplier float64  `json:"effective_multiplier"`
	// ReferencePrice 仅单 Key 接口给出；批量接口不带（量大）。
	ReferencePrice *KeyEditorReferencePrice `json:"reference_price,omitempty"`
}

// KeyFallbackAvailableView 是「可添加」的分组。
type KeyFallbackAvailableView struct {
	GroupID             int64                   `json:"group_id"`
	Name                string                  `json:"name"`
	Status              string                  `json:"status"`
	RateMultiplier      float64                 `json:"rate_multiplier"`
	UserRateMultiplier  *float64                `json:"user_rate_multiplier"`
	EffectiveMultiplier float64                 `json:"effective_multiplier"`
	ReferencePrice      KeyEditorReferencePrice `json:"reference_price"`
}

// KeyFallbackUserView 是 GET/PUT /keys/:id/fallback-chain 的响应。
type KeyFallbackUserView struct {
	KeyID          int64                      `json:"key_id"`
	Platform       string                     `json:"platform"`
	ReferenceModel string                     `json:"reference_model"`
	MaxFallbacks   int                        `json:"max_fallbacks"`
	Enabled        bool                       `json:"enabled"`
	Items          []KeyFallbackItemView      `json:"items"`
	Available      []KeyFallbackAvailableView `json:"available"`
}

// KeyFallbackKeySummary 是批量接口里一把 Key 的摘要。
type KeyFallbackKeySummary struct {
	KeyID        int64                 `json:"key_id"`
	Name         string                `json:"name"`
	Items        []KeyFallbackItemView `json:"items"`
	HasAvailable bool                  `json:"has_available"`
}

// KeyFallbackPlatformSection 是批量接口里的一个平台分区。
type KeyFallbackPlatformSection struct {
	Platform string                  `json:"platform"`
	Keys     []KeyFallbackKeySummary `json:"keys"`
}

// KeyFallbackOverview 是 GET /keys/fallback-chains 的响应。
type KeyFallbackOverview struct {
	Enabled   bool                         `json:"enabled"`
	Platforms []KeyFallbackPlatformSection `json:"platforms"`
}

// --- 管理端响应 ---

// KeyFallbackGroupRef 是分组的简要引用。
type KeyFallbackGroupRef struct {
	GroupID int64  `json:"group_id"`
	Name    string `json:"name"`
}

// KeyFallbackAdminItem 是管理端看到的一个链项。
type KeyFallbackAdminItem struct {
	GroupID   int64      `json:"group_id"`
	Name      string     `json:"name,omitempty"`
	Position  int        `json:"position"`
	Note      string     `json:"note,omitempty"`
	CreatedBy *int64     `json:"created_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// KeyFallbackEffectiveHop 是有效链（dry-run，忽略总开关）上的一跳。
type KeyFallbackEffectiveHop struct {
	Hop      int    `json:"hop"`
	GroupID  int64  `json:"group_id"`
	Name     string `json:"name"`
	Source   string `json:"source"` // admin_head / primary / user / admin_tail
	Eligible bool   `json:"eligible"`
}

// KeyFallbackSkippedHop 是资格过滤时被跳过的链项及原因。
type KeyFallbackSkippedHop struct {
	GroupID    int64  `json:"group_id"`
	Name       string `json:"name,omitempty"`
	Source     string `json:"source"` // user / admin
	SkipReason string `json:"skip_reason"`
}

// KeyFallbackAdminView 是管理端 GET/PUT/DELETE 的响应。
type KeyFallbackAdminView struct {
	KeyID  int64 `json:"key_id"`
	UserID int64 `json:"user_id"`
	// Platform 为主分组所属平台；Key 未绑分组时为空。
	Platform string `json:"platform"`
	// Enabled 是回退链总开关当前状态；effective 是忽略开关的 dry-run 结果。
	Enabled      bool                      `json:"enabled"`
	PrimaryGroup *KeyFallbackGroupRef      `json:"primary_group"`
	UserItems    []KeyFallbackAdminItem    `json:"user_items"`
	HiddenHead   []KeyFallbackAdminItem    `json:"hidden_head"`
	HiddenTail   []KeyFallbackAdminItem    `json:"hidden_tail"`
	Effective    []KeyFallbackEffectiveHop `json:"effective"`
	Skipped      []KeyFallbackSkippedHop   `json:"skipped"`
	Truncated    bool                      `json:"truncated"`
}

// KeyFallbackRouteRefView 是反查结果的一行。
type KeyFallbackRouteRefView struct {
	KeyID     int64  `json:"key_id"`
	UserID    int64  `json:"user_id"`
	Source    string `json:"source"`
	Placement string `json:"placement"`
}

// KeyFallbackRoutesByGroupView 是 GET /admin/groups/:id/fallback-routes 的响应。
type KeyFallbackRoutesByGroupView struct {
	GroupID   int64                     `json:"group_id"`
	Items     []KeyFallbackRouteRefView `json:"items"`
	Truncated bool                      `json:"truncated"`
}

// --- 依赖 ---

type keyFallbackKeys interface {
	GetByID(ctx context.Context, id int64) (*APIKey, error)
	List(ctx context.Context, userID int64, params pagination.PaginationParams, filters APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error)
	GetAvailableGroups(ctx context.Context, userID int64) ([]Group, error)
	GetUserGroupRates(ctx context.Context, userID int64) (map[int64]float64, error)
}

type keyFallbackUsers interface {
	GetByID(ctx context.Context, id int64) (*User, error)
}

type keyFallbackGroups interface {
	GetByIDLite(ctx context.Context, id int64) (*Group, error)
}

type keyFallbackPricer interface {
	ReferenceModel(ctx context.Context, platform, override string) string
	ReferenceModels(ctx context.Context) map[string]string
	SetReferenceModels(ctx context.Context, models map[string]string) (map[string]string, error)
	ReferencePrice(ctx context.Context, model string, primaryGroupID, groupID, userID int64) KeyEditorReferencePrice
}

// KeyFallbackService 见文件头注释。
type KeyFallbackService struct {
	keys     keyFallbackKeys
	users    keyFallbackUsers
	groups   keyFallbackGroups
	routes   GroupRouteService
	extras   APIKeyGroupRouteExtras
	pricer   keyFallbackPricer
	settings groupFallbackSettingsReader
}

// NewKeyFallbackService 创建编排层。可用分组与专属授权判断全部走 APIKeyService（与 GET /groups/available 同源）。
func NewKeyFallbackService(
	keys *APIKeyService,
	users UserRepository,
	groups GroupRepository,
	routes GroupRouteService,
	extras APIKeyGroupRouteExtras,
	pricer *KeyEditorPriceService,
	settings *SettingService,
) *KeyFallbackService {
	s := &KeyFallbackService{
		keys:   keys,
		users:  users,
		groups: groups,
		routes: routes,
		extras: extras,
		pricer: pricer,
	}
	if settings != nil {
		s.settings = settings
	}
	return s
}

func (s *KeyFallbackService) enabled(ctx context.Context) bool {
	return s.settings != nil && s.settings.GetGroupFallbackSettings(ctx).Enabled
}

// --- 用户端 ---

// ownedKey 取 Key 并校验归属。别人的 Key、不存在、已软删除统一返回 ErrAPIKeyNotFound（防 IDOR，不区分原因）。
func (s *KeyFallbackService) ownedKey(ctx context.Context, userID, keyID int64) (*APIKey, error) {
	key, err := s.keys.GetByID(ctx, keyID)
	if err != nil {
		if errors.Is(err, ErrAPIKeyNotFound) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, err
	}
	if key == nil || key.UserID != userID || key.UserID <= 0 {
		return nil, ErrAPIKeyNotFound
	}
	return key, nil
}

func (s *KeyFallbackService) primaryGroup(ctx context.Context, key *APIKey) (*Group, error) {
	if key.GroupID == nil || *key.GroupID <= 0 {
		return nil, ErrFallbackKeyNotGrouped
	}
	g, err := s.groups.GetByIDLite(ctx, *key.GroupID)
	if err != nil {
		return nil, err
	}
	return g, nil
}

// GetUserChain 返回某把 Key 的用户链、可添加分组与参考价。modelOverride 为空用管理员设置的参考模型。
func (s *KeyFallbackService) GetUserChain(ctx context.Context, userID, keyID int64, modelOverride string) (*KeyFallbackUserView, error) {
	override, err := NormalizeKeyEditorModel(modelOverride)
	if err != nil {
		return nil, err
	}
	key, err := s.ownedKey(ctx, userID, keyID)
	if err != nil {
		return nil, err
	}
	return s.buildUserView(ctx, userID, key, override)
}

// ReplaceUserChain 整体替换用户链（空数组清空），成功后返回与 GetUserChain 相同的响应。
func (s *KeyFallbackService) ReplaceUserChain(ctx context.Context, userID, keyID int64, groupIDs []int64, modelOverride string) (*KeyFallbackUserView, error) {
	override, err := NormalizeKeyEditorModel(modelOverride)
	if err != nil {
		return nil, err
	}
	key, err := s.ownedKey(ctx, userID, keyID)
	if err != nil {
		return nil, err
	}
	if key.GroupID == nil || *key.GroupID <= 0 {
		return nil, ErrFallbackKeyNotGrouped
	}
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := s.routes.ReplaceUserChain(ctx, user, key, groupIDs); err != nil {
		return nil, err
	}
	return s.buildUserView(ctx, userID, key, override)
}

func (s *KeyFallbackService) buildUserView(ctx context.Context, userID int64, key *APIKey, modelOverride string) (*KeyFallbackUserView, error) {
	primary, err := s.primaryGroup(ctx, key)
	if err != nil {
		return nil, err
	}
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	rates, err := s.keys.GetUserGroupRates(ctx, userID)
	if err != nil {
		return nil, err
	}
	chain, err := s.routes.GetUserChain(ctx, key.ID)
	if err != nil {
		return nil, err
	}
	availableGroups, err := s.keys.GetAvailableGroups(ctx, userID)
	if err != nil {
		return nil, err
	}

	model := s.pricerModel(ctx, primary.Platform, modelOverride)
	price := func(groupID int64) KeyEditorReferencePrice {
		if s.pricer == nil {
			return KeyEditorReferencePrice{}
		}
		return s.pricer.ReferencePrice(ctx, model, primary.ID, groupID, userID)
	}

	items, chainIDs, err := s.userItems(ctx, user, primary, chain, rates)
	if err != nil {
		return nil, err
	}
	for i := range items {
		p := price(items[i].GroupID)
		items[i].ReferencePrice = &p
	}

	available := make([]KeyFallbackAvailableView, 0)
	for i := range availableGroups {
		g := &availableGroups[i]
		if !isFallbackCandidate(g, primary, chainIDs) {
			continue
		}
		rate, userRate, eff := groupMultipliers(g, rates)
		available = append(available, KeyFallbackAvailableView{
			GroupID:             g.ID,
			Name:                g.Name,
			Status:              KeyFallbackStatusActive,
			RateMultiplier:      rate,
			UserRateMultiplier:  userRate,
			EffectiveMultiplier: eff,
			ReferencePrice:      price(g.ID),
		})
	}

	return &KeyFallbackUserView{
		KeyID:          key.ID,
		Platform:       primary.Platform,
		ReferenceModel: model,
		MaxFallbacks:   MaxUserFallbackItems,
		Enabled:        s.enabled(ctx),
		Items:          items,
		Available:      available,
	}, nil
}

func (s *KeyFallbackService) pricerModel(ctx context.Context, platform, override string) string {
	if override != "" {
		return override
	}
	if s.pricer == nil {
		return ""
	}
	return s.pricer.ReferenceModel(ctx, platform, "")
}

// userItems 构造「主分组 + 用户链」。返回值第二项是已在链上（含主分组）的分组 ID 集合。
// 只读 source='user' 的行（GetUserChain），不接触管理员隐藏链。
func (s *KeyFallbackService) userItems(ctx context.Context, user *User, primary *Group, chain []RouteItem, rates map[int64]float64) ([]KeyFallbackItemView, map[int64]struct{}, error) {
	inChain := map[int64]struct{}{primary.ID: {}}
	items := make([]KeyFallbackItemView, 0, len(chain)+1)

	rate, userRate, eff := groupMultipliers(primary, rates)
	items = append(items, KeyFallbackItemView{
		GroupID:             primary.ID,
		Name:                primary.Name,
		Role:                KeyFallbackRolePrimary,
		Position:            0,
		Status:              groupStatus(primary),
		Usable:              primary.IsActive(),
		RateMultiplier:      rate,
		UserRateMultiplier:  userRate,
		EffectiveMultiplier: eff,
	})

	for _, it := range chain {
		g, err := s.groups.GetByIDLite(ctx, it.GroupID)
		if err != nil {
			if errors.Is(err, ErrGroupNotFound) {
				// 分组刚被删除：读取已用 JOIN 过滤，这里只是并发窗口。
				continue
			}
			return nil, nil, err
		}
		inChain[g.ID] = struct{}{}
		status, usable := fallbackItemStatus(g, primary, user)
		rate, userRate, eff := groupMultipliers(g, rates)
		items = append(items, KeyFallbackItemView{
			GroupID:             g.ID,
			Name:                g.Name,
			Role:                KeyFallbackRoleFallback,
			Position:            len(items),
			Status:              status,
			Usable:              usable,
			RateMultiplier:      rate,
			UserRateMultiplier:  userRate,
			EffectiveMultiplier: eff,
		})
	}
	return items, inChain, nil
}

// ListUserChains 一次返回当前用户全部 Key 的链摘要，按平台分区。不带价格与可添加分组明细。
func (s *KeyFallbackService) ListUserChains(ctx context.Context, userID int64) (*KeyFallbackOverview, error) {
	keys, _, err := s.keys.List(ctx, userID, pagination.PaginationParams{Page: 1, PageSize: keyFallbackListPageSize}, APIKeyListFilters{})
	if err != nil {
		return nil, err
	}
	out := &KeyFallbackOverview{Enabled: s.enabled(ctx), Platforms: []KeyFallbackPlatformSection{}}
	if len(keys) == 0 {
		return out, nil
	}
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	rates, err := s.keys.GetUserGroupRates(ctx, userID)
	if err != nil {
		return nil, err
	}
	availableGroups, err := s.keys.GetAvailableGroups(ctx, userID)
	if err != nil {
		return nil, err
	}

	groupCache := make(map[int64]*Group)
	byPlatform := make(map[string][]KeyFallbackKeySummary)
	for i := range keys {
		key := &keys[i]
		if key.UserID != userID || key.GroupID == nil || *key.GroupID <= 0 {
			continue
		}
		primary, ok := groupCache[*key.GroupID]
		if !ok {
			g, gerr := s.groups.GetByIDLite(ctx, *key.GroupID)
			if gerr != nil {
				if errors.Is(gerr, ErrGroupNotFound) {
					groupCache[*key.GroupID] = nil
					continue
				}
				return nil, gerr
			}
			groupCache[*key.GroupID] = g
			primary = g
		}
		if primary == nil {
			continue
		}
		chain, cerr := s.routes.GetUserChain(ctx, key.ID)
		if cerr != nil {
			return nil, cerr
		}
		items, chainIDs, ierr := s.userItems(ctx, user, primary, chain, rates)
		if ierr != nil {
			return nil, ierr
		}
		hasAvailable := false
		for j := range availableGroups {
			if isFallbackCandidate(&availableGroups[j], primary, chainIDs) {
				hasAvailable = true
				break
			}
		}
		byPlatform[primary.Platform] = append(byPlatform[primary.Platform], KeyFallbackKeySummary{
			KeyID:        key.ID,
			Name:         key.Name,
			Items:        items,
			HasAvailable: hasAvailable,
		})
	}

	platforms := make([]string, 0, len(byPlatform))
	for p := range byPlatform {
		platforms = append(platforms, p)
	}
	sort.Slice(platforms, func(i, j int) bool {
		return platformSortRank(platforms[i]) < platformSortRank(platforms[j]) ||
			(platformSortRank(platforms[i]) == platformSortRank(platforms[j]) && platforms[i] < platforms[j])
	})
	for _, p := range platforms {
		out.Platforms = append(out.Platforms, KeyFallbackPlatformSection{Platform: p, Keys: byPlatform[p]})
	}
	return out, nil
}

func platformSortRank(platform string) int {
	switch platform {
	case PlatformOpenAI:
		return 0
	case PlatformAnthropic:
		return 1
	case PlatformGemini:
		return 2
	case PlatformAntigravity:
		return 3
	case PlatformGrok:
		return 4
	default:
		return 5
	}
}

// isFallbackCandidate 判断某个「用户有权使用的活跃分组」能否作为这把 Key 的兜底候选：
// 同平台、不是主分组、尚未在用户链里。只看用户链，绝不因分组在隐藏链里而剔除。
func isFallbackCandidate(g, primary *Group, inUserChain map[int64]struct{}) bool {
	if g == nil || primary == nil || g.ID == primary.ID || g.Platform != primary.Platform {
		return false
	}
	_, taken := inUserChain[g.ID]
	return !taken
}

func groupStatus(g *Group) string {
	if g == nil {
		return KeyFallbackStatusUnavailable
	}
	if !g.IsActive() {
		return KeyFallbackStatusDisabled
	}
	return KeyFallbackStatusActive
}

// fallbackItemStatus 判断链上兜底项当前能否使用：被禁用 → disabled；平台与主分组不一致、
// 用户已失去专属授权 → unavailable。授权判断与 GetAvailableGroups 同一个来源（User.CanBindGroup）。
func fallbackItemStatus(g, primary *Group, user *User) (status string, usable bool) {
	if g == nil {
		return KeyFallbackStatusUnavailable, false
	}
	if !g.IsActive() {
		return KeyFallbackStatusDisabled, false
	}
	if g.Platform != primary.Platform || user == nil || !user.CanBindGroup(g.ID, g.IsExclusive) {
		return KeyFallbackStatusUnavailable, false
	}
	return KeyFallbackStatusActive, true
}

// groupMultipliers 返回分组倍率、用户专属倍率（没有为 nil）与实际生效倍率。
func groupMultipliers(g *Group, rates map[int64]float64) (rate float64, userRate *float64, effective float64) {
	rate = g.RateMultiplier
	effective = rate
	if r, ok := rates[g.ID]; ok {
		v := r
		userRate = &v
		effective = r
	}
	return rate, userRate, effective
}

// --- 管理端 ---

// AdminGetChain 返回某把 Key 的完整链（用户链 + 隐藏链 + 有效链 dry-run）。
func (s *KeyFallbackService) AdminGetChain(ctx context.Context, keyID int64) (*KeyFallbackAdminView, error) {
	key, err := s.keys.GetByID(ctx, keyID)
	if err != nil {
		if errors.Is(err, ErrAPIKeyNotFound) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, err
	}
	if key == nil {
		return nil, ErrAPIKeyNotFound
	}
	user, err := s.users.GetByID(ctx, key.UserID)
	if err != nil {
		return nil, err
	}

	names := make(map[int64]string)
	nameOf := func(id int64) string {
		if n, ok := names[id]; ok {
			return n
		}
		g, gerr := s.groups.GetByIDLite(ctx, id)
		if gerr != nil || g == nil {
			names[id] = ""
			return ""
		}
		names[id] = g.Name
		return g.Name
	}
	adminItems := func(items []RouteItem) []KeyFallbackAdminItem {
		out := make([]KeyFallbackAdminItem, 0, len(items))
		for _, it := range items {
			item := KeyFallbackAdminItem{GroupID: it.GroupID, Name: nameOf(it.GroupID), Position: it.Position, Note: it.Note, CreatedBy: it.CreatedBy}
			if !it.UpdatedAt.IsZero() {
				updated := it.UpdatedAt
				item.UpdatedAt = &updated
			}
			out = append(out, item)
		}
		return out
	}

	userChain, err := s.routes.GetUserChain(ctx, key.ID)
	if err != nil {
		return nil, err
	}
	head, tail, err := s.routes.GetHiddenChain(ctx, key.ID)
	if err != nil {
		return nil, err
	}

	view := &KeyFallbackAdminView{
		KeyID:      key.ID,
		UserID:     key.UserID,
		Enabled:    s.enabled(ctx),
		UserItems:  adminItems(userChain),
		HiddenHead: adminItems(head),
		HiddenTail: adminItems(tail),
		Effective:  []KeyFallbackEffectiveHop{},
		Skipped:    []KeyFallbackSkippedHop{},
	}
	if key.GroupID == nil || *key.GroupID <= 0 {
		return view, nil
	}
	primary, err := s.groups.GetByIDLite(ctx, *key.GroupID)
	if err != nil {
		return nil, err
	}
	view.Platform = primary.Platform
	view.PrimaryGroup = &KeyFallbackGroupRef{GroupID: primary.ID, Name: primary.Name}

	resolved, err := s.routes.ResolveEffectiveChain(ctx, key, user, ResolveOptions{IgnoreSwitch: true})
	if err != nil {
		return nil, err
	}
	for i, hop := range resolved.Hops {
		view.Effective = append(view.Effective, KeyFallbackEffectiveHop{
			Hop:      i,
			GroupID:  hop.GroupID,
			Name:     nameOf(hop.GroupID),
			Source:   effectiveHopSource(hop),
			Eligible: true,
		})
	}
	for _, sk := range resolved.Skipped {
		view.Skipped = append(view.Skipped, KeyFallbackSkippedHop{
			GroupID:    sk.GroupID,
			Name:       nameOf(sk.GroupID),
			Source:     sk.Source,
			SkipReason: sk.Reason,
		})
	}
	view.Truncated = resolved.Truncated
	return view, nil
}

func effectiveHopSource(h ChainHop) string {
	switch h.RouteSource {
	case RouteSourcePrimary:
		return "primary"
	case RouteSourceAdmin:
		if h.Placement == RoutePlacementHead {
			return "admin_head"
		}
		return "admin_tail"
	default:
		return RouteSourceUser
	}
}

// AdminReplaceHiddenChain 整体替换隐藏链（head / tail），并记录变更前后的完整链。
func (s *KeyFallbackService) AdminReplaceHiddenChain(ctx context.Context, adminID, keyID int64, head, tail []int64, note string) (*KeyFallbackAdminView, error) {
	key, err := s.keys.GetByID(ctx, keyID)
	if err != nil {
		if errors.Is(err, ErrAPIKeyNotFound) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, err
	}
	if key == nil {
		return nil, ErrAPIKeyNotFound
	}
	beforeHead, beforeTail, _ := s.routes.GetHiddenChain(ctx, key.ID)
	if err := s.routes.ReplaceHiddenChain(ctx, adminID, key, head, tail, note); err != nil {
		return nil, err
	}
	// 审计 before/after 目前没有统一注入点，这里用结构化日志留痕（与请求体审计互补）。
	slog.Info("group fallback: hidden chain changed",
		"event", "hidden_chain_changed",
		"api_key_id", key.ID,
		"admin_user_id", adminID,
		"before_head", routeGroupIDsOf(beforeHead),
		"before_tail", routeGroupIDsOf(beforeTail),
		"after_head", head,
		"after_tail", tail,
	)
	return s.AdminGetChain(ctx, key.ID)
}

// AdminClearHiddenChain 清空隐藏链（不动用户链）。
func (s *KeyFallbackService) AdminClearHiddenChain(ctx context.Context, adminID, keyID int64) (*KeyFallbackAdminView, error) {
	return s.AdminReplaceHiddenChain(ctx, adminID, keyID, nil, nil, "")
}

func routeGroupIDsOf(items []RouteItem) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.GroupID)
	}
	return out
}

// AdminRoutesByGroup 反查：哪些 Key 的链里放了这个分组（只返回 ID，不含 Key 明文）。
func (s *KeyFallbackService) AdminRoutesByGroup(ctx context.Context, groupID int64) (*KeyFallbackRoutesByGroupView, error) {
	if s.extras == nil {
		return nil, infraerrors.ServiceUnavailable("FALLBACK_UNAVAILABLE", "fallback routes are not available")
	}
	if groupID <= 0 {
		return nil, ErrFallbackGroupNotFound
	}
	if _, err := s.groups.GetByIDLite(ctx, groupID); err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			return nil, ErrFallbackGroupNotFound
		}
		return nil, err
	}
	refs, err := s.extras.ListByGroup(ctx, groupID, keyFallbackRoutesByGroupLimit+1)
	if err != nil {
		return nil, err
	}
	view := &KeyFallbackRoutesByGroupView{GroupID: groupID, Items: make([]KeyFallbackRouteRefView, 0, len(refs))}
	if len(refs) > keyFallbackRoutesByGroupLimit {
		refs = refs[:keyFallbackRoutesByGroupLimit]
		view.Truncated = true
	}
	for _, r := range refs {
		view.Items = append(view.Items, KeyFallbackRouteRefView(r))
	}
	return view, nil
}

// AdminReferenceModels 返回每个平台当前生效的参考模型。
func (s *KeyFallbackService) AdminReferenceModels(ctx context.Context) map[string]string {
	if s.pricer == nil {
		return map[string]string{}
	}
	return s.pricer.ReferenceModels(ctx)
}

// AdminSetReferenceModels 保存管理员设置的参考模型（按平台），返回保存后生效的完整映射。
func (s *KeyFallbackService) AdminSetReferenceModels(ctx context.Context, models map[string]string) (map[string]string, error) {
	if s.pricer == nil {
		return nil, infraerrors.ServiceUnavailable("FALLBACK_UNAVAILABLE", "reference model settings are not available")
	}
	return s.pricer.SetReferenceModels(ctx, models)
}
