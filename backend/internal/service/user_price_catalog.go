package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 用户价格页的数据来源（W6 PR8a，设计 3.4）。
//
// 页面上每个「模型 × 分组」的价格都由 PriceQuoter.BatchQuote 给出，倍率（分组倍率或用户专属倍率，
// 以及 v2 分组的额外倍率）在这里乘好，前端只做币种换算与排版，不再自己乘倍率。
// 取价随各分组的阶段走：legacy、shadow 分组的价格就是计费现在用的价，v2 分组读矩阵，二者都在 Quoter 里，本文件不关心。
//
// 模型清单随阶段走：
//   - legacy、shadow：渠道里列出的模型（和改造前的页面完全相同的清单）；
//   - v2：模型目录里状态为 active 的模型，再按 Quote.Access 去掉该分组里关闭的。
//
// 返回内容不含渠道名、账号、上游信息。

const (
	userPriceCatalogCacheTTL = 30 * time.Second
	// 缓存条目数上限：超过就整体清空，页面是低频入口，不值得做 LRU。
	userPriceCatalogCacheMax = 256
)

// UserPriceGroup 是价格页上一个分组的展示信息。
type UserPriceGroup struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Platform         string `json:"platform"`
	SubscriptionType string `json:"subscription_type"`
	IsExclusive      bool   `json:"is_exclusive"`
	Description      string `json:"description"`
}

// UserPriceSet 是一组单价，单位 USD：token 计费是每 token，按次与按图是每次。null 表示这一项没有价格。
type UserPriceSet struct {
	Input       *float64 `json:"input"`
	Output      *float64 `json:"output"`
	CacheRead   *float64 `json:"cache_read"`
	CacheWrite  *float64 `json:"cache_write"`
	ImageOutput *float64 `json:"image_output"`
	Unit        *float64 `json:"unit"`
}

// UserPriceTier 是一档价格：token 计费按上下文长度分档，按次与按图按档位或分辨率分档。
type UserPriceTier struct {
	MinTokens int          `json:"min_tokens"`
	MaxTokens *int         `json:"max_tokens"`
	Label     string       `json:"label,omitempty"`
	Official  UserPriceSet `json:"official"`
	Prices    UserPriceSet `json:"prices"`
}

// UserPriceEntry 是一个模型在一个分组里的价格。
type UserPriceEntry struct {
	GroupID int64 `json:"group_id"`
	// Rate 是实际乘上的倍率：用户专属倍率优先于分组倍率，v2 分组再含额外倍率。
	Rate float64 `json:"rate"`
	// BaseRate 是分组默认倍率；HasCustomRate 表示 Rate 来自用户专属倍率。
	BaseRate      float64 `json:"base_rate"`
	HasCustomRate bool    `json:"has_custom_rate"`
	// BillingMode：token、per_request、image。Kind：token 按每百万 token 展示，request 按每次展示。
	BillingMode string `json:"billing_mode"`
	Kind        string `json:"kind"`
	// Official 是乘倍率之前的单价（对照用），Prices 是乘完倍率后的单价（用户实际按此扣额度）。
	Official UserPriceSet    `json:"official"`
	Prices   UserPriceSet    `json:"prices"`
	Tiers    []UserPriceTier `json:"tiers"`
}

// UserPriceModel 是价格页上的一个模型。Entries 只含有价格的分组；没有任何分组有价格时为空，页面显示「暂无价格」。
type UserPriceModel struct {
	Name     string           `json:"name"`
	Platform string           `json:"platform"`
	Entries  []UserPriceEntry `json:"entries"`
}

// UserPriceCatalog 是用户价格页的完整数据。
type UserPriceCatalog struct {
	Groups []UserPriceGroup `json:"groups"`
	Models []UserPriceModel `json:"models"`
}

// UserPriceCatalogQuery 是一次构建的输入。
type UserPriceCatalogQuery struct {
	UserID int64
	// Groups 是用户可访问的分组（与 API 密钥页同一个来源）。
	Groups []Group
	// DisplayGroupIDs、DisplayModels 是管理员在「对外展示设置」里勾选的范围，nil 表示不限。
	DisplayGroupIDs map[int64]struct{}
	DisplayModels   map[string]struct{}
}

type userPriceCatalogLister interface {
	List(ctx context.Context, filter ModelCatalogFilter) ([]ModelCatalogEntry, error)
}

// UserPriceCatalogService 构建用户价格页数据。
type UserPriceCatalogService struct {
	channels *ChannelService
	quoter   *PriceQuoter
	catalog  userPriceCatalogLister
	now      func() time.Time

	mu    sync.Mutex
	cache map[string]userPriceCatalogCacheEntry
}

type userPriceCatalogCacheEntry struct {
	catalog *UserPriceCatalog
	expires time.Time
}

// NewUserPriceCatalogService 创建服务。catalog 为 nil 时 v2 分组没有模型清单（装配阶段没有接模型目录的测试场景）。
func NewUserPriceCatalogService(channels *ChannelService, quoter *PriceQuoter, catalog *ModelCatalogService) *UserPriceCatalogService {
	s := &UserPriceCatalogService{
		channels: channels,
		quoter:   quoter,
		now:      time.Now,
		cache:    make(map[string]userPriceCatalogCacheEntry),
	}
	if catalog != nil {
		s.catalog = catalog
	}
	return s
}

type userPriceBucket struct {
	name     string
	platform string
	groupIDs []int64
	seen     map[int64]struct{}
}

// Build 构建价格页数据。同一个用户、同一组分组与筛选在 30 秒内复用结果。
func (s *UserPriceCatalogService) Build(ctx context.Context, q UserPriceCatalogQuery) (*UserPriceCatalog, error) {
	if s == nil || s.quoter == nil {
		return nil, ErrPriceQuoterUnavailable
	}

	groups := visiblePriceGroups(q)
	if len(groups) == 0 {
		return &UserPriceCatalog{Groups: []UserPriceGroup{}, Models: []UserPriceModel{}}, nil
	}

	key := userPriceCatalogCacheKey(q.UserID, groups, q.DisplayModels)
	if cached := s.cacheGet(key); cached != nil {
		return cached, nil
	}

	buckets, err := s.collectBuckets(ctx, groups, q.DisplayModels)
	if err != nil {
		return nil, err
	}
	result := s.quoteBuckets(ctx, q.UserID, groups, buckets)
	s.cachePut(key, result)
	return result, nil
}

// visiblePriceGroups 按展示设置过滤用户可访问的分组，去重并按 ID 排序。
func visiblePriceGroups(q UserPriceCatalogQuery) []Group {
	byID := make(map[int64]Group, len(q.Groups))
	for i := range q.Groups {
		g := q.Groups[i]
		if g.ID <= 0 {
			continue
		}
		if q.DisplayGroupIDs != nil {
			if _, ok := q.DisplayGroupIDs[g.ID]; !ok {
				continue
			}
		}
		byID[g.ID] = g
	}
	out := make([]Group, 0, len(byID))
	for _, g := range byID {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func userPriceCatalogCacheKey(userID int64, groups []Group, models map[string]struct{}) string {
	var sb strings.Builder
	sb.WriteString(strconv.FormatInt(userID, 10))
	sb.WriteString("|")
	for _, g := range groups {
		sb.WriteString(strconv.FormatInt(g.ID, 10))
		sb.WriteString(",")
	}
	sb.WriteString("|")
	if models == nil {
		sb.WriteString("*")
		return sb.String()
	}
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sb.WriteString(strconv.Quote(name))
		sb.WriteString(",")
	}
	return sb.String()
}

func (s *UserPriceCatalogService) cacheGet(key string) *UserPriceCatalog {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cache[key]
	if !ok || !s.now().Before(entry.expires) {
		return nil
	}
	return entry.catalog
}

func (s *UserPriceCatalogService) cachePut(key string, catalog *UserPriceCatalog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cache) >= userPriceCatalogCacheMax {
		s.cache = make(map[string]userPriceCatalogCacheEntry)
	}
	s.cache[key] = userPriceCatalogCacheEntry{catalog: catalog, expires: s.now().Add(userPriceCatalogCacheTTL)}
}

// collectBuckets 按阶段收集每个分组要报价的模型，合并成「平台 + 模型名」为键的清单。
func (s *UserPriceCatalogService) collectBuckets(ctx context.Context, groups []Group, display map[string]struct{}) ([]*userPriceBucket, error) {
	legacyGroups := make(map[int64]Group)
	var v2Groups []Group
	for _, g := range groups {
		if s.quoter.GroupStage(ctx, g.ID) == PricingStageV2 {
			v2Groups = append(v2Groups, g)
		} else {
			legacyGroups[g.ID] = g
		}
	}

	buckets := make(map[string]*userPriceBucket)
	add := func(platform, name string, groupID int64) {
		if display != nil {
			if _, ok := display[name]; !ok {
				return
			}
		}
		key := platform + "::" + name
		b, ok := buckets[key]
		if !ok {
			b = &userPriceBucket{name: name, platform: platform, seen: make(map[int64]struct{})}
			buckets[key] = b
		}
		if _, dup := b.seen[groupID]; dup {
			return
		}
		b.seen[groupID] = struct{}{}
		b.groupIDs = append(b.groupIDs, groupID)
	}

	if len(legacyGroups) > 0 && s.channels != nil {
		channels, err := s.channels.ListAvailable(ctx)
		if err != nil {
			return nil, err
		}
		for _, ch := range channels {
			if ch.Status != StatusActive {
				continue
			}
			for _, ref := range ch.Groups {
				g, ok := legacyGroups[ref.ID]
				if !ok {
					continue
				}
				for _, m := range ch.SupportedModels {
					if m.Platform != g.Platform {
						continue
					}
					add(m.Platform, m.Name, g.ID)
				}
			}
		}
	}

	if len(v2Groups) > 0 && s.catalog != nil {
		entries, err := s.catalog.List(ctx, ModelCatalogFilter{Status: ModelCatalogActive})
		if err != nil {
			return nil, fmt.Errorf("list model catalog: %w", err)
		}
		for _, g := range v2Groups {
			for i := range entries {
				if entries[i].Platform != g.Platform {
					continue
				}
				add(entries[i].Platform, entries[i].ModelKey, g.ID)
			}
		}
	}

	out := make([]*userPriceBucket, 0, len(buckets))
	for _, b := range buckets {
		sort.Slice(b.groupIDs, func(i, j int) bool { return b.groupIDs[i] < b.groupIDs[j] })
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].platform != out[j].platform {
			return out[i].platform < out[j].platform
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

// quoteBuckets 对清单里的每个「模型 × 分组」报价，并整理成页面数据。
// 模型只要在任一分组里放行就保留（没有任何价格时页面显示「暂无价格」，与改造前一致）；分组里关闭的不出现。
func (s *UserPriceCatalogService) quoteBuckets(ctx context.Context, userID int64, groups []Group, buckets []*userPriceBucket) *UserPriceCatalog {
	type slot struct {
		bucket int
		group  int64
	}
	var reqs []QuoteRequest
	var slots []slot
	offPeak := quoteOffPeakReference(deepseekNowFunc())
	for bi, b := range buckets {
		for _, gid := range b.groupIDs {
			req := QuoteRequest{Model: b.name, GroupID: gid, UserID: userID}
			if isDeepSeekModel(b.name) {
				// 价格页只给标准（低谷）价，不随一天里的时段跳动。
				req.At = offPeak
			}
			reqs = append(reqs, req)
			slots = append(slots, slot{bucket: bi, group: gid})
		}
	}
	quotes := s.quoter.BatchQuote(ctx, reqs)

	models := make([]UserPriceModel, len(buckets))
	open := make([]bool, len(buckets))
	for i, b := range buckets {
		models[i] = UserPriceModel{Name: b.name, Platform: b.platform, Entries: []UserPriceEntry{}}
	}
	usedGroups := make(map[int64]struct{})
	for i, qt := range quotes {
		if qt == nil || !qt.Access.OK {
			continue
		}
		sl := slots[i]
		open[sl.bucket] = true
		entry := userPriceEntryFromQuote(qt)
		if entry == nil {
			continue
		}
		models[sl.bucket].Entries = append(models[sl.bucket].Entries, *entry)
		usedGroups[sl.group] = struct{}{}
	}

	result := &UserPriceCatalog{Groups: []UserPriceGroup{}, Models: []UserPriceModel{}}
	for i := range models {
		if open[i] {
			result.Models = append(result.Models, models[i])
		}
	}
	for _, g := range groups {
		if _, ok := usedGroups[g.ID]; !ok {
			continue
		}
		result.Groups = append(result.Groups, UserPriceGroup{
			ID:               g.ID,
			Name:             g.Name,
			Platform:         g.Platform,
			SubscriptionType: g.SubscriptionType,
			IsExclusive:      g.IsExclusive,
			Description:      g.Description,
		})
	}
	return result
}

// quoteOffPeakReference 返回从 now 起第一个 DeepSeek 低谷整点（now 本身是低谷就是 now）。
// DeepSeek 默认价卡工作日高峰按 2 倍计费，价格页展示标准价；用时点前移而不是自己除 2，保证与计费同一条取价链。
func quoteOffPeakReference(now time.Time) time.Time {
	t := now
	for i := 0; i < 24*8; i++ {
		if deepseekPeakMultiplierAt(t) <= 1 {
			return t
		}
		t = t.Add(time.Hour)
	}
	return now
}

const (
	userPriceKindToken   = "token"
	userPriceKindRequest = "request"
)

// userPriceEntryFromQuote 把一次报价整理成页面条目；没有价格时返回 nil。
func userPriceEntryFromQuote(qt *Quote) *UserPriceEntry {
	if qt == nil || !qt.Priced || qt.resolved == nil {
		return nil
	}
	entry := &UserPriceEntry{
		GroupID:       qt.GroupID,
		Rate:          qt.EffectiveMultiplier,
		BaseRate:      qt.GroupMultiplier,
		HasCustomRate: qt.UserMultiplier != nil,
		BillingMode:   string(qt.resolved.Mode),
		Tiers:         []UserPriceTier{},
	}
	switch qt.resolved.Mode {
	case BillingModePerRequest, BillingModeImage:
		return fillRequestPriceEntry(qt, entry)
	default:
		if filled := fillImageGenerationPriceEntry(qt, entry); filled != nil {
			return filled
		}
		return fillTokenPriceEntry(qt, entry)
	}
}

// fillRequestPriceEntry 渠道按次、按图条目：首档价加分档价，倍率与计费一致（图片模式用图片倍率）。
func fillRequestPriceEntry(qt *Quote, entry *UserPriceEntry) *UserPriceEntry {
	if qt.PerRequest == nil {
		return nil
	}
	mult := qt.EffectiveMultiplier
	if qt.resolved.Mode == BillingModeImage {
		mult = qt.ImageMultiplier
	}
	entry.Rate = mult
	entry.Kind = userPriceKindRequest
	first := qt.PerRequest.DefaultPrice
	if len(qt.PerRequest.Tiers) > 0 {
		first = qt.PerRequest.Tiers[0].Price
	}
	entry.Official = UserPriceSet{Unit: float64Ptr(first)}
	entry.Prices = UserPriceSet{Unit: float64Ptr(first * mult)}
	if len(qt.PerRequest.Tiers) > 1 {
		for _, tier := range qt.PerRequest.Tiers {
			entry.Tiers = append(entry.Tiers, UserPriceTier{
				MinTokens: tier.MinTokens,
				MaxTokens: tier.MaxTokens,
				Label:     tier.TierLabel,
				Official:  UserPriceSet{Unit: float64Ptr(tier.Price)},
				Prices:    UserPriceSet{Unit: float64Ptr(tier.Price * mult)},
			})
		}
	}
	return entry
}

// fillImageGenerationPriceEntry 目录里的图片生成模型（没有渠道价）：按张计价，分 1K、2K、4K 三档，
// 倍率用图片倍率，与 CalculateImageCost 的口径一致。不是图片生成模型，或按张价只是代码里的兜底值时返回 nil。
func fillImageGenerationPriceEntry(qt *Quote, entry *UserPriceEntry) *UserPriceEntry {
	if qt.ImageRequest == nil || len(qt.ImageRequest.Tiers) == 0 || qt.quoter == nil || qt.quoter.billing == nil ||
		qt.quoter.billing.pricingService == nil {
		return nil
	}
	lp := qt.quoter.billing.pricingService.GetModelPricing(qt.Model)
	if lp == nil || lp.Mode != "image_generation" {
		return nil
	}
	first := qt.ImageRequest.Tiers[0]
	if first.Source == "default" || first.Price <= 0 {
		return nil
	}
	mult := qt.ImageMultiplier
	entry.BillingMode = string(BillingModeImage)
	entry.Rate = mult
	entry.Kind = userPriceKindRequest
	entry.Official = UserPriceSet{Unit: float64Ptr(first.Price)}
	entry.Prices = UserPriceSet{Unit: float64Ptr(first.Price * mult)}
	for _, tier := range qt.ImageRequest.Tiers {
		entry.Tiers = append(entry.Tiers, UserPriceTier{
			Label:    tier.Tier,
			Official: UserPriceSet{Unit: float64Ptr(tier.Price)},
			Prices:   UserPriceSet{Unit: float64Ptr(tier.Price * mult)},
		})
	}
	return entry
}

// fillTokenPriceEntry token 计费：首档价，加上渠道区间或长上下文形成的分档价。
func fillTokenPriceEntry(qt *Quote, entry *UserPriceEntry) *UserPriceEntry {
	if qt.Prices == nil || qt.FinalPrices == nil {
		return nil
	}
	entry.Kind = userPriceKindToken
	mult := qt.EffectiveMultiplier
	present := tokenPricePresence(qt.Prices.PerToken, qt.resolved.channelPricing)
	entry.Official = present.set(qt.Prices.PerToken, 1)
	entry.Prices = present.set(qt.FinalPrices.PerToken, 1)

	switch {
	case len(qt.Intervals) > 1:
		for _, iv := range qt.Intervals {
			ivPresent := tokenPricePresence(iv.Prices.PerToken, nil)
			entry.Tiers = append(entry.Tiers, UserPriceTier{
				MinTokens: iv.MinTokens,
				MaxTokens: iv.MaxTokens,
				Official:  ivPresent.set(iv.Prices.PerToken, 1),
				Prices:    ivPresent.set(iv.Prices.PerToken, mult),
			})
		}
	case qt.LongContext != nil && qt.LongContext.ThresholdTokens > 0 && len(qt.Intervals) == 0:
		threshold := qt.LongContext.ThresholdTokens
		long := longContextUnitPrices(qt.Prices.PerToken, qt.LongContext)
		entry.Tiers = append(entry.Tiers,
			UserPriceTier{
				MinTokens: 0,
				MaxTokens: &threshold,
				Official:  present.set(qt.Prices.PerToken, 1),
				Prices:    present.set(qt.Prices.PerToken, mult),
			},
			UserPriceTier{
				MinTokens: threshold,
				Official:  present.set(long, 1),
				Prices:    present.set(long, mult),
			},
		)
	}
	return entry
}

// longContextUnitPrices 算超过长上下文阈值后的单价（USD/token，未乘倍率），规则与 computeTokenBreakdown 一致：
// 价卡显式给了超阈值单价就用它，否则输入与缓存（读、写）乘输入倍率，输出乘输出倍率；倍率缺省按 1。
func longContextUnitPrices(base QuoteUnitPrices, lc *QuoteLongContext) QuoteUnitPrices {
	inMult, outMult := lc.InputMultiplier, lc.OutputMultiplier
	if inMult <= 0 {
		inMult = 1
	}
	if outMult <= 0 {
		outMult = 1
	}
	var explicit QuoteUnitPrices
	if lc.ExplicitPrices != nil {
		explicit = lc.ExplicitPrices.PerToken
	}
	pick := func(explicitPrice, basePrice, mult float64) float64 {
		if explicitPrice > 0 {
			return explicitPrice
		}
		return basePrice * mult
	}
	return QuoteUnitPrices{
		Input:       pick(explicit.Input, base.Input, inMult),
		Output:      pick(explicit.Output, base.Output, outMult),
		CacheRead:   pick(explicit.CacheRead, base.CacheRead, inMult),
		CacheWrite:  pick(explicit.CacheWrite, base.CacheWrite, inMult),
		ImageOutput: base.ImageOutput,
	}
}

// userPricePresence 记录 token 价格里哪些项目要展示。
type userPricePresence struct {
	input, output, cacheRead, cacheWrite, imageOutput bool
}

// tokenPricePresence 决定哪些价格项目展示：价格大于 0，或渠道显式填了这一项（显式的 0 表示免费，要展示）。
// 输入与输出都没有价格（整个模型免费）时，两项都按 0 展示，避免页面把一个免费模型当成没有价格。
func tokenPricePresence(official QuoteUnitPrices, channel *ChannelModelPricing) userPricePresence {
	p := userPricePresence{
		input:       official.Input > 0,
		output:      official.Output > 0,
		cacheRead:   official.CacheRead > 0,
		cacheWrite:  official.CacheWrite > 0,
		imageOutput: official.ImageOutput > 0,
	}
	if channel != nil {
		p.input = p.input || channel.InputPrice != nil
		p.output = p.output || channel.OutputPrice != nil
		p.cacheRead = p.cacheRead || channel.CacheReadPrice != nil
		p.cacheWrite = p.cacheWrite || channel.CacheWritePrice != nil
		p.imageOutput = p.imageOutput || channel.ImageOutputPrice != nil
	}
	if !p.input && !p.output {
		p.input, p.output = true, true
	}
	return p
}

// set 取出要展示的项目，并乘上 mult（mult 为 1 时原值返回，不引入任何运算）。
func (p userPricePresence) set(prices QuoteUnitPrices, mult float64) UserPriceSet {
	scale := func(v float64) *float64 {
		if mult == 1 {
			return float64Ptr(v)
		}
		return float64Ptr(v * mult)
	}
	var out UserPriceSet
	if p.input {
		out.Input = scale(prices.Input)
	}
	if p.output {
		out.Output = scale(prices.Output)
	}
	if p.cacheRead {
		out.CacheRead = scale(prices.CacheRead)
	}
	if p.cacheWrite {
		out.CacheWrite = scale(prices.CacheWrite)
	}
	if p.imageOutput {
		out.ImageOutput = scale(prices.ImageOutput)
	}
	return out
}
