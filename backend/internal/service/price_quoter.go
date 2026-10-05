package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

// PriceQuoter 是「模型 × 分组（× 用户）」的统一取价入口。
//
// 设计目标：计费与展示同源。PriceQuoter 自己不保存任何价格，也不重写任何取价规则，
// 全部委托给计费路径上已有的函数：
//
//   - 渠道定价 / 基础价 / 部分字段覆盖：ModelPricingResolver.Resolve（内部调 BillingService.GetModelPricing）；
//   - 区间 + 官方价卡策略 + DeepSeek 峰谷：BillingService.effectiveTokenPricing
//     （原 calculateTokenCost 的取价部分，计费与报价共用同一个函数）；
//   - 费用算术：BillingService.computeTokenBreakdown / CalculateCostUnified / CalculateImageCost；
//   - 倍率：userGroupRateResolver.Resolve（用户专属倍率替换分组倍率）+ resolveImageRateMultiplierFromFields。
//
// 计费有两条路径，Quoter 按分组平台与是否命中渠道定价选路（QuotePricingPath），分别调用对应网关实际使用的函数：
//
//   - unified：OpenAI 网关（openai / grok 分组）的全部 token 请求，任何网关命中渠道定价时，
//     以及非 OpenAI 网关上无渠道定价的 DeepSeek → CalculateCostUnified
//     （DeepSeek 带 PricingAt，叠加峰时倍率；Gemini 分组的 DeepSeek 再按原生入口走 CalculateCostWithLongContextUnified）；
//   - legacy：非 OpenAI 网关（Anthropic / Gemini / Antigravity 等）、没有渠道定价、且不是 DeepSeek 模型时 →
//     CalculateCost（Gemini 分组再按 Gemini 原生入口的实现走 CalculateCostWithLongContext，200K 阈值、超出部分 2 倍）。
//
// service tier 只有 OpenAI 网关会传给计费；非 OpenAI 网关在两条路径上都忽略它。
//
// 因此 Quote.Cost 给出的费用与对应网关用同样入参调用计费函数得到的费用逐位相同。
//
// 额外倍率（W6，设计 3.2）：网关在成本函数里按「实际出价的那个模型」取分组的额外倍率并乘进倍率。
// Quote 只有一个模型（调用方给的计费模型），所以只取一次，位置与网关一致：OpenAI 文本是候选循环里出价的那个候选，
// OpenAI 图片是 calculateOpenAIImageCost 收到的模型，Anthropic 是 billableModelWithFallback 选定之后的模型。
// 取到的额外倍率乘进 EffectiveMultiplier 与 ImageMultiplier（顺序与网关相同：先按图片倍率策略算好，再乘额外倍率），
// 所以 FinalPrices 与 Cost 都含它。legacy 分组与没有单元格的 v2 分组恒为 1，结果与改动前逐位相同。
//
// Quote.Access（W6，设计 3.2「准入」）：分组准入 + 目录状态，见 quoteAccess。
//
// 注意：QuoteSourceFallback 的含义是「用了 BillingService.fallbackPrices 里的兜底价」，
// 与 ModelPricingResolver 的 PricingSourceFallback（同为字符串 "fallback"，表示「没有任何价格」）意思相反，别混用。
type PriceQuoter struct {
	resolver *ModelPricingResolver
	billing  *BillingService
	groups   priceQuoteGroupReader
	rates    *userGroupRateResolver
	// catalog 是可选的模型目录读取方，由 SetModelCatalog 在装配阶段接上。为 nil 时 Quote.Access 只反映分组准入。
	catalog quoteCatalogReader
}

// priceQuoteGroupReader 是 PriceQuoter 对分组仓储的最小依赖。
type priceQuoteGroupReader interface {
	GetByIDLite(ctx context.Context, id int64) (*Group, error)
}

// quoteCatalogReader 是 PriceQuoter 对模型目录的最小依赖：按平台与模型名（或别名）查目录条目，未登记返回 nil。
// 名字的规范化（先去首尾空白，再套 normalizeChannelPricingModelName）由目录一侧负责，调用方不必先归一。
type quoteCatalogReader interface {
	Resolve(ctx context.Context, platform, model string) (*ModelCatalogEntry, error)
}

// SetModelCatalog 给报价器接上模型目录，之后 Quote.Access 会叠加目录状态（draft、retired 不放行）。
// 只在装配阶段调用，不加锁；不调用或传 nil 时，Quote.Access 只反映分组准入。
// 目录状态只对 shadow、v2 阶段的分组生效：legacy 分组的运行时准入不读目录，报价不能比网关多拦一道。
func (q *PriceQuoter) SetModelCatalog(catalog quoteCatalogReader) {
	q.catalog = catalog
}

// normalizePricingEntryModel 是 PriceQuoter 与模型目录入口共用的名字规范化（设计 3.2、R2-D）：
// 先 strings.TrimSpace，再套既有的 normalizeChannelPricingModelName（小写，claude- 名的点号写成连字符）。
// 调用方不必先去空白或归一。注意它只用于「查键」（目录、准入）；送进价格链的名字（Quote.Model）只去首尾空白、
// 不改大小写与点号，价格链里各个取价函数各自有规范化，报价喂给它们的名字必须与网关喂给它们的一致。
func normalizePricingEntryModel(model string) string {
	return normalizeChannelPricingModelName(strings.TrimSpace(model))
}

var (
	// ErrPriceQuoterUnavailable 表示 PriceQuoter 缺少必要依赖（只会出现在装配错误或测试里）。
	ErrPriceQuoterUnavailable = infraerrors.ServiceUnavailable("PRICE_QUOTER_UNAVAILABLE", "price quoter is not available")
	// ErrPriceQuoteModelRequired 表示请求没有给出模型。
	ErrPriceQuoteModelRequired = infraerrors.BadRequest("PRICE_QUOTE_MODEL_REQUIRED", "model is required")
	// ErrPriceQuoteGroupRequired 表示请求没有给出有效的分组 ID。
	ErrPriceQuoteGroupRequired = infraerrors.BadRequest("PRICE_QUOTE_GROUP_REQUIRED", "group_id must be a positive integer")
	// ErrPriceQuoteServiceTierInvalid 表示 service tier 不是网关认可的取值。
	ErrPriceQuoteServiceTierInvalid = infraerrors.BadRequest("PRICE_QUOTE_SERVICE_TIER_INVALID", "service_tier must be one of priority, fast, flex, auto, default, scale")
)

// quoteReferenceContextTokens 是报价展示单价所用的参考上下文长度。
// 取 1 token：落在第一个区间（区间是 (min, max]，0 不会命中任何区间）、且不触发长上下文。
const quoteReferenceContextTokens = 1

// NewPriceQuoter 创建统一取价入口。
//
// 用户专属倍率的缓存只留 1 秒：这是给管理员/AI 看「现在的价」的入口，
// 不应被网关热路径几十秒的倍率缓存拖后腿。
func NewPriceQuoter(resolver *ModelPricingResolver, billingService *BillingService, groupRepo GroupRepository, userGroupRateRepo UserGroupRateRepository) *PriceQuoter {
	return &PriceQuoter{
		resolver: resolver,
		billing:  billingService,
		groups:   groupRepo,
		rates:    newUserGroupRateResolver(userGroupRateRepo, nil, time.Second, nil, "service.price_quoter"),
	}
}

// QuoteSource 标明报价的价格来自哪一层。
type QuoteSource string

const (
	// QuoteSourceNone 没有任何可用价格：计费会得到 ErrModelPricingUnavailable。
	QuoteSourceNone QuoteSource = "none"
	// QuoteSourceChannel 渠道定价生效（可能只覆盖了部分字段，其余字段见 BaseSource）。
	QuoteSourceChannel QuoteSource = "channel"
	// QuoteSourceLiteLLM 动态价格目录（LiteLLM JSON）。
	QuoteSourceLiteLLM QuoteSource = "litellm"
	// QuoteSourceFallback BillingService.fallbackPrices 里硬编码的兜底价（有价，只是来自代码里写死的表）。
	// 注意：ModelPricingResolver 的 PricingSourceFallback 字符串也是 "fallback"，但它表示「没有任何价格」，意思相反。
	QuoteSourceFallback QuoteSource = "fallback"
	// QuoteSourceCardPolicy 代码里的官方价卡策略强制覆盖了目录价（目前是 DeepSeek）。
	QuoteSourceCardPolicy QuoteSource = "card_policy"
)

// QuotePricingPath 标明 Quote.Cost 复现的是哪条网关计费路径。
type QuotePricingPath string

const (
	// QuotePricingPathUnified：CalculateCostUnified。OpenAI 网关的 token 请求，任何网关命中渠道定价时，
	// 以及非 OpenAI 网关上无渠道定价的 DeepSeek（叠加 DeepSeek 峰时倍率）。
	QuotePricingPathUnified QuotePricingPath = "unified"
	// QuotePricingPathLegacy：CalculateCost / CalculateCostWithLongContext。非 OpenAI 网关、无渠道定价、
	// 且不是 DeepSeek 模型时；忽略 service tier。
	QuotePricingPathLegacy QuotePricingPath = "legacy"
)

// QuoteRequest 是 PriceQuoter.Quote 的入参。
type QuoteRequest struct {
	Model string
	// GroupID 是 API Key 所属（home）分组，必填。
	GroupID int64
	// ServedGroupID 是稳定优先兜底时实际服务的分组；<=0 或等于 GroupID 表示没有兜底。
	ServedGroupID int64
	// UserID 用于查用户专属倍率；<=0 表示不查。
	UserID int64
	// ServiceTier 请求的 service tier（priority / fast / flex / ...），空表示标准档。
	ServiceTier string
	// At 计费时点（DeepSeek 峰谷与 pro→Flash 切换的判定依据），零值取当前时刻。
	At time.Time
}

// QuoteAccess 是「该分组是否能用这个模型」的判定。Reason 取值：
// closed_in_group（单元格显式关闭）、not_in_allowlist（白名单分组里没有它）、
// catalog_draft、catalog_retired（模型目录里显式为草稿或已下线）。
type QuoteAccess struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// 模型目录状态造成的拒绝原因。未登记的模型视同 active，不产生拒绝原因（设计 S-3、Q2）。
const (
	QuoteAccessReasonCatalogDraft   = "catalog_draft"
	QuoteAccessReasonCatalogRetired = "catalog_retired"
)

// QuoteUnitPrices 是一组单价，单位 USD/token（QuotePriceSet.PerMTok 里是 USD/1M token）。
type QuoteUnitPrices struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheWrite float64 `json:"cache_write"`
	CacheRead  float64 `json:"cache_read"`
	// CacheWrite5m / CacheWrite1h 仅在 5m 与 1h 缓存写价不同时给出；CacheWrite 是 API 未返回明细时的计费口径。
	CacheWrite5m float64 `json:"cache_write_5m,omitempty"`
	CacheWrite1h float64 `json:"cache_write_1h,omitempty"`
	// ImageInput / ImageOutput 仅在价格里有图片 token 单价时给出。
	ImageInput  float64 `json:"image_input,omitempty"`
	ImageOutput float64 `json:"image_output,omitempty"`
}

// QuotePriceSet 同时给出 $/token（计费基准）与 $/MTok（展示用）。
type QuotePriceSet struct {
	PerToken QuoteUnitPrices `json:"per_token"`
	PerMTok  QuoteUnitPrices `json:"per_mtok"`
}

// QuoteTokenInterval 是渠道区间定价的一档。区间语义与计费一致：(MinTokens, MaxTokens]，
// MaxTokens 为 nil 表示无上限。
type QuoteTokenInterval struct {
	MinTokens int           `json:"min_tokens"`
	MaxTokens *int          `json:"max_tokens"`
	Prices    QuotePriceSet `json:"prices"`
}

// QuoteLongContext 是长上下文规则：整次请求的输入侧 token（输入 + 缓存读 + 缓存写）
// 超过 ThresholdTokens 后，整次会话的输入/输出按倍率提价，或改用目录里显式给出的 >272K 单价。
type QuoteLongContext struct {
	ThresholdTokens  int     `json:"threshold_tokens"`
	InputMultiplier  float64 `json:"input_multiplier"`
	OutputMultiplier float64 `json:"output_multiplier"`
	// ExplicitPrices 是目录显式给出的 >272K 单价（USD/token），nil 表示按倍率。
	ExplicitPrices *QuotePriceSet `json:"explicit_prices,omitempty"`
	// PriorityExcludesLongContext 为 true 时，priority 档有独立价且不与长上下文叠加。
	PriorityExcludesLongContext bool `json:"priority_excludes_long_context"`
}

// QuoteServiceTier 说明请求的 service tier 在这个价卡上怎么计价。
type QuoteServiceTier struct {
	// Requested 是归一化后的档位（fast 已归为 priority），空表示标准档。
	Requested string `json:"requested"`
	// Mode：standard 标准价；priority_card 价卡自带 priority 独立价；multiplier 在标准价上乘 Multiplier。
	Mode       string  `json:"mode"`
	Multiplier float64 `json:"multiplier"`
}

// QuoteRequestTier 是按次/图片模式的一档价格。
type QuoteRequestTier struct {
	TierLabel string  `json:"tier_label,omitempty"`
	MinTokens int     `json:"min_tokens"`
	MaxTokens *int    `json:"max_tokens"`
	Price     float64 `json:"price"`
}

// QuotePerRequest 是渠道按次/图片模式的价格（USD/次）。
type QuotePerRequest struct {
	// DefaultPrice 是未命中任何层级时的默认价。
	DefaultPrice float64            `json:"default_price"`
	Tiers        []QuoteRequestTier `json:"tiers,omitempty"`
}

// QuoteImageTier 是无渠道定价时一档图片尺寸的单价（USD/张）。
type QuoteImageTier struct {
	Tier  string  `json:"tier"`
	Price float64 `json:"price"`
	// Source：group_config 分组上配置的图片价；litellm 目录里的 output_cost_per_image；default 代码里的 $0.134 兜底。
	Source string `json:"source"`
}

// QuoteImageRequest 是无渠道定价时图片生成请求的单价（CalculateImageCost 的口径，倍率用 ImageMultiplier）。
type QuoteImageRequest struct {
	Tiers []QuoteImageTier `json:"tiers"`
}

// QuoteGatewayLongContext 是网关自己的长上下文加价：仅 Gemini 原生 /v1beta 入口
// （handler/gemini_v1beta_handler.go 调 RecordUsageWithLongContext，阈值 200K、倍率 2.0）会加价；
// Gemini 分组走 /v1/messages 等其它入口时不加。Quoter 不知道入口，对 Gemini 平台分组按原生入口给出。
// 输入 + 缓存读取合计超过阈值后，超出部分的输入与缓存读取按 ExtraMultiplier 倍计费，
// 输出和缓存写入不加价。与 QuoteLongContext（价卡自带规则）是两回事。
type QuoteGatewayLongContext struct {
	ThresholdTokens int     `json:"threshold_tokens"`
	ExtraMultiplier float64 `json:"extra_multiplier"`
}

// QuotePolicyFlags 标记取价过程中套用了哪些价卡策略。
type QuotePolicyFlags struct {
	// DeepSeekOfficialCard：DeepSeek 模型的单价被强制成官方价卡，不看目录里写的价。
	DeepSeekOfficialCard bool `json:"deepseek_official_card"`
	// DeepSeekProBilledAsFlash：At 时点 deepseek-v4-pro 已按 Flash 价计费。
	DeepSeekProBilledAsFlash bool `json:"deepseek_pro_billed_as_flash"`
	// DeepSeekPeak：At 时点处于 DeepSeek 高峰，且已在默认价卡上叠加峰时倍率。
	DeepSeekPeak           bool    `json:"deepseek_peak"`
	DeepSeekPeakMultiplier float64 `json:"deepseek_peak_multiplier,omitempty"`
	// LongContextPolicy：GPT-5.x 的长上下文/缓存写价由代码策略补齐（gpt-5.6 或 gpt-5.4-5.5 族）。
	LongContextPolicy string `json:"long_context_policy,omitempty"`
}

// Quote 是一次「模型 × 分组（× 用户）」的报价。单价是 USD/token 基准，倍率另列；
// 需要精确费用时用 Cost，它与网关计费同一套算术。
type Quote struct {
	Model string `json:"model"`
	// GroupID 是请求的 home 分组；ServedGroupID 是实际用于计价的分组（稳定优先兜底时不同）。
	GroupID       int64     `json:"group_id"`
	ServedGroupID int64     `json:"served_group_id"`
	UserID        int64     `json:"user_id,omitempty"`
	At            time.Time `json:"at"`

	Access QuoteAccess `json:"access"`

	// Priced 为 false 表示没有任何价格可用（Source=none）。
	Priced bool `json:"priced"`
	// Source 是价格来自哪一层；渠道定价只覆盖部分字段时 Source=channel，其余字段来自 BaseSource。
	Source           QuoteSource `json:"source"`
	BaseSource       QuoteSource `json:"base_source,omitempty"`
	BillingMode      string      `json:"billing_mode"`
	ChannelOverrides []string    `json:"channel_overrides,omitempty"`

	// Prices 是参考上下文（1 token）下、叠加 service tier 后的单价，未乘倍率。
	// FinalPrices 是 Prices 再乘 EffectiveMultiplier。
	Prices      *QuotePriceSet       `json:"prices,omitempty"`
	FinalPrices *QuotePriceSet       `json:"final_prices,omitempty"`
	Intervals   []QuoteTokenInterval `json:"intervals,omitempty"`
	LongContext *QuoteLongContext    `json:"long_context,omitempty"`
	ServiceTier *QuoteServiceTier    `json:"service_tier,omitempty"`
	// PricingPath 是 Cost 复现的网关计费路径；GatewayLongContext 仅「非 OpenAI 网关且无渠道定价」下的 Gemini 分组有值。
	PricingPath        QuotePricingPath         `json:"pricing_path"`
	GatewayLongContext *QuoteGatewayLongContext `json:"gateway_long_context,omitempty"`

	PerRequest   *QuotePerRequest   `json:"per_request,omitempty"`
	ImageRequest *QuoteImageRequest `json:"image_request,omitempty"`

	// GroupMultiplier 是计价分组的默认倍率；UserMultiplier 仅在用户专属倍率与分组倍率不同时给出；
	// EffectiveMultiplier 是 token / 按次计费实际使用的倍率；ImageMultiplier 是图片请求使用的倍率。
	// 两者都已乘上 ExtraMultiplier（即 usage_logs.rate_multiplier 里记的值）。
	GroupMultiplier     float64  `json:"group_multiplier"`
	UserMultiplier      *float64 `json:"user_multiplier,omitempty"`
	EffectiveMultiplier float64  `json:"effective_multiplier"`
	ImageMultiplier     float64  `json:"image_multiplier"`
	// ExtraMultiplier 是分组对这个模型设的额外倍率（设计 3.2），仅在不等于 1 时给出。
	ExtraMultiplier *float64 `json:"extra_multiplier,omitempty"`

	Policy QuotePolicyFlags `json:"policy"`

	// 以下是 Cost 用的内部状态，不进 JSON。
	quoter *PriceQuoter
	// extra 是已经乘进两个倍率的额外倍率，1 表示没有（手工构造的 Quote 里零值同样按 1）。
	extra          float64
	resolved       *ResolvedPricing
	serviceTier    string
	pricingGroupID int64
	imageConfig    *ImagePriceConfig
	platform       string
}

// QuoteUsage 是 Quote.Cost 要估算的一次请求用量。
type QuoteUsage struct {
	Tokens UsageTokens
	// ImageCount > 0 表示图片生成请求，网关按这个条件走图片计费分支。
	ImageCount int
	// ImageSize 是图片尺寸（"1K"/"2K"/"4K" 或 "1024x1024" 这类），空按默认档。
	ImageSize string
}

// Quote 计算「模型 × 分组（× 用户）」的报价。
func (q *PriceQuoter) Quote(ctx context.Context, req QuoteRequest) (*Quote, error) {
	if q == nil || q.resolver == nil || q.billing == nil || q.groups == nil {
		return nil, ErrPriceQuoterUnavailable
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return nil, ErrPriceQuoteModelRequired
	}
	if req.GroupID <= 0 {
		return nil, ErrPriceQuoteGroupRequired
	}
	tier, ok := normalizeQuoteServiceTier(req.ServiceTier)
	if !ok {
		return nil, ErrPriceQuoteServiceTierInvalid
	}

	pg, err := q.pricingGroup(ctx, req)
	if err != nil {
		return nil, err
	}

	// 倍率：用户专属倍率替换分组倍率（与两个网关一致，同一个 resolver 逻辑）；
	// 图片倍率按计价分组的 image 费率策略（与 resolveImageRateMultiplier 同一个函数）。
	rateMultiplier := q.rates.Resolve(ctx, req.UserID, pg.ID, pg.RateMultiplier)
	imageMultiplier := resolveImageRateMultiplierFromFields(pg.ImageRateIndependent, pg.ImageRateMultiplier, rateMultiplier)

	at := deepseekPricingAt(req.At)
	gid := pg.ID

	// 额外倍率：与网关一致，按实际出价的模型（这里就是 model）、计价分组、计费时点取，乘进两个倍率。
	// 乘法的位置与网关相同（先按图片倍率策略算好 imageMultiplier，再各自乘 extra），extra 为 1 时 x*1 与 x 逐位相同。
	policy := q.resolver.groupPolicy()
	extra := extraMultiplierFor(ctx, policy, pg.ID, model, at)
	effectiveMultiplier := rateMultiplier * extra
	imageMultiplier *= extra

	resolved := q.resolver.Resolve(ctx, PricingInput{Model: model, GroupID: &gid})

	quote := &Quote{
		Model:               model,
		GroupID:             req.GroupID,
		ServedGroupID:       pg.ID,
		UserID:              req.UserID,
		At:                  at,
		Access:              q.quoteAccess(ctx, policy, pg, model),
		BillingMode:         string(resolved.Mode),
		GroupMultiplier:     pg.RateMultiplier,
		EffectiveMultiplier: effectiveMultiplier,
		ImageMultiplier:     imageMultiplier,
		quoter:              q,
		extra:               extra,
		resolved:            resolved,
		serviceTier:         tier,
		pricingGroupID:      pg.ID,
		platform:            pg.Platform,
		imageConfig: &ImagePriceConfig{
			Price1K: pg.ImagePrice1K,
			Price2K: pg.ImagePrice2K,
			Price4K: pg.ImagePrice4K,
		},
	}
	if req.UserID > 0 && rateMultiplier != pg.RateMultiplier {
		userMultiplier := rateMultiplier
		quote.UserMultiplier = &userMultiplier
	}
	if extra != 1 {
		extraMultiplier := extra
		quote.ExtraMultiplier = &extraMultiplier
	}

	// 选路：与两个网关一致。OpenAI 网关 token 请求一律走 CalculateCostUnified；
	// 其它平台命中渠道定价、或是无渠道定价的 DeepSeek 才走 Unified，其余走 CalculateCost（legacy）。
	quote.PricingPath = QuotePricingPathUnified
	if quote.usesGatewayNoChannelPricePath() {
		if quote.usesLegacyTokenPath() {
			quote.PricingPath = QuotePricingPathLegacy
		}
		if pg.Platform == PlatformGemini {
			quote.GatewayLongContext = &QuoteGatewayLongContext{
				ThresholdTokens: geminiGatewayLongContextThreshold,
				ExtraMultiplier: geminiGatewayLongContextMultiplier,
			}
		}
	}

	if resolved.Source == PricingSourceChannel {
		quote.Source = QuoteSourceChannel
		quote.ChannelOverrides = quoteChannelOverrides(resolved)
	} else {
		quote.Source = q.billing.quoteCatalogLayer(model)
	}

	switch resolved.Mode {
	case BillingModePerRequest, BillingModeImage:
		q.fillRequestQuote(quote)
	default:
		if quote.Source == QuoteSourceChannel {
			quote.BaseSource = q.billing.quoteCatalogLayer(model)
		}
		q.fillTokenQuote(quote)
	}
	q.fillImageRequestQuote(quote)
	return quote, nil
}

// Gemini 原生 /v1beta 入口（handler/gemini_v1beta_handler.go）调用 RecordUsageWithLongContext 时写死的参数。
// Quote 只对 Gemini 平台分组应用它；Antigravity 分组经 Gemini 原生接口访问时网关也会加价，
// 但 Quoter 不知道请求走的是哪个入口，不在这里猜。
const (
	geminiGatewayLongContextThreshold  = 200000
	geminiGatewayLongContextMultiplier = 2.0
)

// usesGatewayNoChannelPricePath 判断 token 计费是否落在 GatewayService.calculateTokenCost 的「无渠道价」分支：
// 非 OpenAI 网关且没有渠道定价（resolveChannelPricing 返回 nil 的情形）。Gemini 原生入口的长上下文加价只在这个分支生效。
func (qt *Quote) usesGatewayNoChannelPricePath() bool {
	return !quotePlatformUsesOpenAIGateway(qt.platform) && qt.resolved.Source != PricingSourceChannel
}

// usesLegacyTokenPath 判断 token 计费是否走 CalculateCost 路径：无渠道价分支里的非 DeepSeek 模型。
// 无渠道价的 DeepSeek 在网关里走 CalculateCostUnified（传 PricingAt，叠加峰时倍率），不属于 legacy。
func (qt *Quote) usesLegacyTokenPath() bool {
	return qt.usesGatewayNoChannelPricePath() && !isDeepSeekModel(qt.Model)
}

// quotePlatformUsesOpenAIGateway 判断分组平台的请求是否由 OpenAIGatewayService 计费。
// 与 server/routes/gateway.go 的 isOpenAICompatibleGatewayPlatform 保持一致：openai 与 grok 都进 OpenAI 网关。
func quotePlatformUsesOpenAIGateway(platform string) bool {
	return platform == PlatformOpenAI || platform == PlatformGrok
}

// ignoresServiceTier：只有 OpenAI 网关把 service tier 传给计费。GatewayService（Anthropic / Gemini / Antigravity）
// 在任何路径上（含命中渠道价的 CalculateCostUnified，见 gateway_service.go calculateTokenCost）都不传档位。
func (qt *Quote) ignoresServiceTier() bool {
	return !quotePlatformUsesOpenAIGateway(qt.platform)
}

// pricingGroup 决定用哪个分组计价：稳定优先兜底到服务组时按服务组计价
// （与 OpenAIGatewayService.RecordUsage 的 servedFromFallback 判定一致：服务组倍率必须 > 0）。
func (q *PriceQuoter) pricingGroup(ctx context.Context, req QuoteRequest) (*Group, error) {
	home, err := q.groups.GetByIDLite(ctx, req.GroupID)
	if err != nil {
		return nil, err
	}
	if home == nil {
		return nil, ErrGroupNotFound
	}
	if req.ServedGroupID <= 0 || req.ServedGroupID == req.GroupID {
		return home, nil
	}
	served, err := q.groups.GetByIDLite(ctx, req.ServedGroupID)
	if err != nil {
		return nil, err
	}
	if served == nil {
		return nil, ErrGroupNotFound
	}
	if served.RateMultiplier > 0 {
		return served, nil
	}
	return home, nil
}

// quoteAccess 给出 Quote.Access：分组准入，再叠加目录状态（设计 3.2「准入」）。
//
// 分组准入镜像网关的两个检查点：
//   - 计费来源是 upstream 的限制型分组（policy.UpstreamCheck 为真）：Quote 的输入是计费模型，这时它就是账号映射之后的
//     上游模型，网关对它做的是 UpstreamAccess（调度循环里逐账号），不是请求模型的 ModelAccess。目录状态不拦上游模型：
//     上游模型名不对用户开放，新模型上线前账号可以先映射过去。
//   - 其余分组：网关调度前对计费模型做 ModelAccess（checkChannelPricingRestriction），这里同样。
//
// UpstreamCheck 返回错误（缓存加载失败）时按「不需要检查」处理，与网关的 needsUpstreamChannelRestrictionCheck 一致。
// 没有策略（没有渠道服务）时放行。ModelAccess 与 UpstreamAccess 的查找规则（含对空白与大小写的处理）由策略负责，
// 与 checkRestricted 一致，这里不另加一层。
func (q *PriceQuoter) quoteAccess(ctx context.Context, policy GroupPolicy, pg *Group, model string) QuoteAccess {
	if policy == nil {
		return QuoteAccess{OK: true}
	}
	if required, err := policy.UpstreamCheck(ctx, pg.ID); err == nil && required {
		return policy.UpstreamAccess(ctx, pg.ID, model)
	}
	if access := policy.ModelAccess(ctx, pg.ID, model); !access.OK {
		return access
	}
	return q.catalogAccess(ctx, policy, pg, model)
}

// catalogAccess 叠加模型目录状态：目录里显式为 draft 或 retired 才拦，未登记视同 active（Q2）。
// 只对 shadow、v2 阶段的分组生效，因为 legacy 分组的运行时准入不读目录；没有接目录（SetModelCatalog）时放行。
// 目录读取失败时放行并记 Warn：Access 是展示与校验用的，读不到目录不能把一个可用的模型报成不可用。
func (q *PriceQuoter) catalogAccess(ctx context.Context, policy GroupPolicy, pg *Group, model string) QuoteAccess {
	if q.catalog == nil || policy.Stage(ctx, pg.ID) == PricingStageLegacy {
		return QuoteAccess{OK: true}
	}
	entry, err := q.catalog.Resolve(ctx, pg.Platform, normalizePricingEntryModel(model))
	if err != nil {
		slog.Warn("price quote: catalog lookup failed, treating the model as active",
			"group_id", pg.ID, "platform", pg.Platform, "model", model, "error", err)
		return QuoteAccess{OK: true}
	}
	return catalogEntryAccess(entry)
}

// fillTokenQuote 填 token 计费模式的报价。
func (q *PriceQuoter) fillTokenQuote(quote *Quote) {
	resolved := quote.resolved
	legacy := quote.PricingPath == QuotePricingPathLegacy
	// 非 OpenAI 网关（legacy、渠道价、无渠道价的 DeepSeek 三种路径都一样）不按 service tier 计费，展示与计费同样按标准档。
	tier := quote.serviceTier
	if quote.ignoresServiceTier() {
		tier = ""
	}
	var pricing *ModelPricing
	var trace tokenPricingTrace
	var err error
	if legacy {
		// CalculateCost → GetModelPricing：按当前时刻（不是 req.At）判定 DeepSeek pro→Flash，不叠加峰时倍率。
		pricing, err = q.billing.GetModelPricing(quote.Model)
	} else {
		pricing, trace, err = q.billing.effectiveTokenPricing(q.resolver, resolved, quote.Model, quoteReferenceContextTokens, quote.At)
	}
	if err != nil {
		// 与计费一致：取不到价格时计费返回 ErrModelPricingUnavailable。
		quote.Source = QuoteSourceNone
		return
	}
	quote.Priced = true

	// 长上下文定价仅在无区间定价时应用（与 calculateTokenCost 一致）。
	applyLongCtx := len(resolved.Intervals) == 0

	prices := q.tokenPriceSet(pricing, tier, applyLongCtx, 1)
	finalPrices := q.tokenPriceSet(pricing, tier, applyLongCtx, quote.EffectiveMultiplier)
	quote.Prices = &prices
	quote.FinalPrices = &finalPrices

	for i := range resolved.Intervals {
		iv := resolved.Intervals[i]
		// 边角情况：用 MinTokens+1 探价。渠道区间若有重叠或没按下限排序，这里显示的可能是命中
		// 另一个区间的价；计费时同样取第一个命中的区间，所以这只是展示上的歧义，不影响扣费。
		ivPricing, _, ivErr := q.billing.effectiveTokenPricing(q.resolver, resolved, quote.Model, iv.MinTokens+1, quote.At)
		if ivErr != nil {
			continue
		}
		var maxTokens *int
		if iv.MaxTokens != nil {
			maxValue := *iv.MaxTokens
			maxTokens = &maxValue
		}
		quote.Intervals = append(quote.Intervals, QuoteTokenInterval{
			MinTokens: iv.MinTokens,
			MaxTokens: maxTokens,
			Prices:    q.tokenPriceSet(ivPricing, tier, false, 1),
		})
	}

	if applyLongCtx && q.billing.shouldApplySessionLongContextPricing(UsageTokens{InputTokens: pricing.LongContextInputThreshold + 1}, pricing) {
		quote.LongContext = quoteLongContext(pricing)
	}

	serviceTier := &QuoteServiceTier{Requested: quote.serviceTier, Mode: "standard", Multiplier: 1}
	if quote.ignoresServiceTier() {
		// 请求了档位但本网关不按档位收费：如实标出，免得调用方以为会乘倍率。
		if quote.serviceTier != "" {
			serviceTier.Mode = "ignored"
		}
	} else if usePriorityServiceTierPricing(quote.serviceTier, pricing) {
		serviceTier.Mode = "priority_card"
	} else if multiplier := serviceTierCostMultiplier(quote.serviceTier); multiplier != 1 {
		serviceTier.Mode = "multiplier"
		serviceTier.Multiplier = multiplier
	}
	quote.ServiceTier = serviceTier

	// 官方价卡只在渠道没有自定义价时才强制（渠道定价保留运营者配置）。
	isDeepSeek := isDeepSeekModel(quote.Model)
	quote.Policy.DeepSeekOfficialCard = isDeepSeek && resolved.Source != PricingSourceChannel
	proAt := quote.At
	if legacy {
		proAt = deepseekPricingAt(time.Time{}) // 与上面 GetModelPricing 同口径：当前时刻
	}
	quote.Policy.DeepSeekProBilledAsFlash = isDeepSeek && isDeepSeekProModel(quote.Model) && deepseekProBilledAsFlash(proAt)
	if trace.DeepSeekPeakMultiplier > 1 {
		quote.Policy.DeepSeekPeak = true
		quote.Policy.DeepSeekPeakMultiplier = trace.DeepSeekPeakMultiplier
	}
	switch {
	case isOpenAIGPT56Model(quote.Model):
		quote.Policy.LongContextPolicy = "gpt-5.6"
	case isOpenAIGPT54Model(quote.Model):
		quote.Policy.LongContextPolicy = "gpt-5.4-5.5"
	}
}

// tokenPriceSet 用 1 token 的探针经 computeTokenBreakdown 反推各项单价。
// 探针走的就是计费的分档、service tier 与缓存算术，所以展示的单价与计费逐位一致。
// rateMultiplier=1 得到未乘倍率的单价，传有效倍率得到最终单价。
func (q *PriceQuoter) tokenPriceSet(pricing *ModelPricing, serviceTier string, applyLongCtx bool, rateMultiplier float64) QuotePriceSet {
	probe := func(tokens UsageTokens) float64 {
		return q.billing.computeTokenBreakdown(pricing, tokens, rateMultiplier, serviceTier, applyLongCtx).ActualCost
	}
	unit := QuoteUnitPrices{
		Input:      probe(UsageTokens{InputTokens: 1}),
		Output:     probe(UsageTokens{OutputTokens: 1}),
		CacheWrite: probe(UsageTokens{CacheCreationTokens: 1}),
		CacheRead:  probe(UsageTokens{CacheReadTokens: 1}),
	}
	cacheWrite5m := probe(UsageTokens{CacheCreationTokens: 1, CacheCreation5mTokens: 1})
	cacheWrite1h := probe(UsageTokens{CacheCreationTokens: 1, CacheCreation1hTokens: 1})
	if cacheWrite5m != unit.CacheWrite || cacheWrite1h != unit.CacheWrite {
		unit.CacheWrite5m = cacheWrite5m
		unit.CacheWrite1h = cacheWrite1h
	}
	if pricing.ImageInputPricePerToken != 0 || pricing.ImageOutputPricePerToken != 0 || pricing.ImageOutputPriceExplicit {
		unit.ImageInput = probe(UsageTokens{InputTokens: 1, ImageInputTokens: 1})
		unit.ImageOutput = probe(UsageTokens{OutputTokens: 1, ImageOutputTokens: 1})
	}
	return newQuotePriceSet(unit)
}

// fillRequestQuote 填渠道按次/图片模式的报价。
func (q *PriceQuoter) fillRequestQuote(quote *Quote) {
	resolved := quote.resolved
	perRequest := &QuotePerRequest{DefaultPrice: resolved.DefaultPerRequestPrice}
	for i := range resolved.RequestTiers {
		tier := resolved.RequestTiers[i]
		if tier.PerRequestPrice == nil {
			continue
		}
		var maxTokens *int
		if tier.MaxTokens != nil {
			maxValue := *tier.MaxTokens
			maxTokens = &maxValue
		}
		perRequest.Tiers = append(perRequest.Tiers, QuoteRequestTier{
			TierLabel: tier.TierLabel,
			MinTokens: tier.MinTokens,
			MaxTokens: maxTokens,
			Price:     *tier.PerRequestPrice,
		})
	}
	quote.PerRequest = perRequest
	quote.Priced = len(perRequest.Tiers) > 0 ||
		(resolved.channelPricing != nil && resolved.channelPricing.PerRequestPrice != nil)
}

// fillImageRequestQuote 填无渠道定价时图片生成请求的单价。
// 渠道定价生效时图片请求走渠道的按次/图片价或 token 价（见 Quote.Cost），不走 CalculateImageCost。
func (q *PriceQuoter) fillImageRequestQuote(quote *Quote) {
	if quote.resolved.Source == PricingSourceChannel || !q.billing.quoteModelImageCapable(quote.Model) {
		return
	}
	imageRequest := &QuoteImageRequest{}
	for _, tier := range []string{ImageBillingSize1K, ImageBillingSize2K, ImageBillingSize4K} {
		imageRequest.Tiers = append(imageRequest.Tiers, QuoteImageTier{
			Tier:   tier,
			Price:  q.billing.getImageUnitPrice(quote.Model, tier, quote.imageConfig),
			Source: q.billing.quoteImageTierSource(quote.Model, tier, quote.imageConfig),
		})
	}
	quote.ImageRequest = imageRequest
}

// Cost 估算一次请求的费用。它把 Quote 里已解析好的价格和倍率交给对应网关实际使用的计费函数，
// 分支与网关一致：
//
// 图片请求（ImageCount > 0）：
//   - 无渠道定价 → CalculateImageCost，乘 ImageMultiplier
//     （openai_gateway_service.go calculateOpenAIRecordUsageCost / gateway_service.go calculateRecordUsageCost）；
//   - 渠道是按次/图片模式 → CalculateCostUnified，RequestCount=张数、SizeTier、ImageMultiplier。
//     OpenAI 网关（calculateOpenAIImageCost）不传 token；其它网关（calculateImageCost）只传 input/output/image output token；
//   - 渠道是 token 模式 → 两个网关都当作普通 token 请求（OpenAI：calculateOpenAIRecordUsageCost 里 resolved != nil
//     时不 return，落到 token 循环；其它网关：calculateRecordUsageCost 里 Mode==Token 走 calculateTokenCost），
//     即下面的 token 路径：EffectiveMultiplier、完整 token、RequestCount=1。
//
// token 请求：
//   - legacy 路径（非 OpenAI 网关、无渠道定价、非 DeepSeek 模型）：CalculateCost；Gemini 分组走 CalculateCostWithLongContext。
//     定价时点是调用时刻而不是 Quote.At；
//   - 非 OpenAI 网关上无渠道定价的 DeepSeek：CalculateCostUnified 并传 PricingAt = Quote.At（叠加峰时倍率），
//     Gemini 分组走 CalculateCostWithLongContextUnified（gateway_service.go calculateTokenCost）；
//   - 其余：CalculateCostUnified。非 OpenAI 网关不传 ServiceTier（渠道价分支连 PricingAt 也不传，但渠道价不受计费时点影响）。
//
// 额外倍率已经乘进 EffectiveMultiplier 与 ImageMultiplier；返回的费用明细与网关一样带上这个标记
// （withExtraMultiplier，extra 为 1 时原样返回），所以 Cost 与网关成本函数的返回值可以逐字段比较。
func (qt *Quote) Cost(ctx context.Context, usage QuoteUsage) (*CostBreakdown, error) {
	if qt == nil || qt.quoter == nil || qt.resolved == nil {
		return nil, ErrPriceQuoterUnavailable
	}
	extra := qt.extra
	if extra <= 0 {
		extra = 1
	}
	cost, err := qt.cost(ctx, usage)
	return withExtraMultiplier(cost, extra), err
}

// cost 是 Cost 去掉额外倍率标记的部分：按网关的分支选计费函数。
func (qt *Quote) cost(ctx context.Context, usage QuoteUsage) (*CostBreakdown, error) {
	billing := qt.quoter.billing
	channelPriced := qt.resolved.Source == PricingSourceChannel
	gid := qt.pricingGroupID
	openAI := quotePlatformUsesOpenAIGateway(qt.platform)

	if usage.ImageCount > 0 {
		perRequestMode := qt.resolved.Mode == BillingModePerRequest || qt.resolved.Mode == BillingModeImage
		if !channelPriced {
			return billing.CalculateImageCost(qt.Model, usage.ImageSize, usage.ImageCount, qt.imageConfig, qt.ImageMultiplier), nil
		}
		if perRequestMode {
			tokens := UsageTokens{}
			if !openAI {
				tokens = UsageTokens{
					InputTokens:       usage.Tokens.InputTokens,
					OutputTokens:      usage.Tokens.OutputTokens,
					ImageOutputTokens: usage.Tokens.ImageOutputTokens,
				}
			}
			return billing.CalculateCostUnified(CostInput{
				Ctx:            ctx,
				Model:          qt.Model,
				GroupID:        &gid,
				Tokens:         tokens,
				RequestCount:   usage.ImageCount,
				SizeTier:       NormalizeImageBillingTierOrDefault(usage.ImageSize),
				RateMultiplier: qt.ImageMultiplier,
				Resolver:       qt.quoter.resolver,
				Resolved:       qt.resolved,
			})
		}
		// 渠道 token 模式：落到下面的 token 路径。
	}

	if qt.PricingPath == QuotePricingPathLegacy {
		if qt.GatewayLongContext != nil {
			return billing.CalculateCostWithLongContext(qt.Model, usage.Tokens, qt.EffectiveMultiplier,
				qt.GatewayLongContext.ThresholdTokens, qt.GatewayLongContext.ExtraMultiplier)
		}
		return billing.CalculateCost(qt.Model, usage.Tokens, qt.EffectiveMultiplier)
	}

	input := CostInput{
		Ctx:            ctx,
		Model:          qt.Model,
		GroupID:        &gid,
		Tokens:         usage.Tokens,
		RequestCount:   1,
		RateMultiplier: qt.EffectiveMultiplier,
		Resolver:       qt.quoter.resolver,
		Resolved:       qt.resolved,
	}
	switch {
	case openAI:
		input.ServiceTier = qt.serviceTier
		input.PricingAt = qt.At
	case qt.usesGatewayNoChannelPricePath():
		// 到这里的只会是无渠道价的 DeepSeek（非 DeepSeek 已在上面走 legacy）：网关传 PricingAt、不传 ServiceTier。
		input.PricingAt = qt.At
		if qt.GatewayLongContext != nil {
			return billing.CalculateCostWithLongContextUnified(input, qt.GatewayLongContext.ThresholdTokens, qt.GatewayLongContext.ExtraMultiplier)
		}
	}
	return billing.CalculateCostUnified(input)
}

// tokenPricingTrace 记录 effectiveTokenPricing 取价时套用的价卡策略，只供报价展示，不参与计费。
type tokenPricingTrace struct {
	// DeepSeekPeakMultiplier > 1 表示已在默认价卡上叠加了 DeepSeek 峰时倍率。
	DeepSeekPeakMultiplier float64
}

// effectiveTokenPricing 返回 token 计费实际使用的价卡：先按上下文长度选区间，
// 再套官方价卡策略（DeepSeek 强制官方价、GPT-5.x 长上下文/缓存写价），最后在默认价卡上叠加 DeepSeek 峰时倍率。
//
// 这是原 calculateTokenCost 的取价部分，原样搬出：calculateTokenCost 与 PriceQuoter 共用，
// 计费与报价由此同源。
func (s *BillingService) effectiveTokenPricing(resolver *ModelPricingResolver, resolved *ResolvedPricing, model string, totalContext int, pricingAt time.Time) (*ModelPricing, tokenPricingTrace, error) {
	var trace tokenPricingTrace

	pricing := resolver.GetIntervalPricing(resolved, totalContext)
	if pricing == nil {
		return nil, trace, fmt.Errorf("no pricing available for model: %s: %w", model, ErrModelPricingUnavailable)
	}

	// 默认价卡（Source=LiteLLM）应用 DeepSeek 官方价强制覆盖（幂等，GetModelPricing
	// 内部已强制过）；分组/渠道自定义定价保留运营者配置，不强制覆盖官方价。
	// 计费时点：优先请求级 PricingAt（用户计费与账号统计成本同源，DeepSeek 峰谷与
	// pro→Flash 切换判定共用），零值回退 deepseekNowFunc()。
	pricingAt = deepseekPricingAt(pricingAt)

	pricing = s.applyModelSpecificPricingPolicyEx(model, pricing, resolved.Source == PricingSourceLiteLLM, pricingAt)

	// DeepSeek 模型默认价卡按官方峰谷口径调整：高峰时段（01:00–04:00 与
	// 06:00–10:00 UTC，仅工作日；北京时间周末全天低谷）按 2× 低谷价计费。
	// 仅作用于默认价卡（Source=LiteLLM，无分组/渠道自定义定价）——分组/渠道
	// 自定义定价保持运营者语义，不叠加。先克隆再乘，避免污染共享 fallbackPrices 指针。
	if resolved.Source == PricingSourceLiteLLM && isDeepSeekModel(model) {
		if mult := deepseekPeakMultiplierAt(pricingAt); mult > 1 {
			cloned := *pricing
			cloned.InputPricePerToken *= mult
			cloned.OutputPricePerToken *= mult
			cloned.CacheReadPricePerToken *= mult
			pricing = &cloned
			trace.DeepSeekPeakMultiplier = mult
		}
	}
	return pricing, trace, nil
}

// quoteCatalogLayer 返回模型基础价来自哪一层（不含渠道覆盖）。
// 判定顺序与 getModelPricingAt 一致：DeepSeek 一律被强制成官方价卡；其余先查动态目录，再查硬编码兜底。
func (s *BillingService) quoteCatalogLayer(model string) QuoteSource {
	lower := strings.ToLower(model)
	if isDeepSeekModel(lower) {
		return QuoteSourceCardPolicy
	}
	if s.pricingService != nil && s.pricingService.GetModelPricing(lower) != nil {
		return QuoteSourceLiteLLM
	}
	if s.getFallbackPricing(lower) != nil {
		return QuoteSourceFallback
	}
	return QuoteSourceNone
}

// quoteModelImageCapable 判断动态目录里这个模型是否是图片生成模型（有按张价或图片 token 价）。
func (s *BillingService) quoteModelImageCapable(model string) bool {
	if s.pricingService == nil {
		return false
	}
	pricing := s.pricingService.GetModelPricing(model)
	return pricing != nil &&
		(pricing.Mode == "image_generation" || pricing.OutputCostPerImage > 0 || pricing.OutputCostPerImageToken > 0)
}

// quoteImageTierSource 说明 getImageUnitPrice 对某一档尺寸取的是哪一层的价。
func (s *BillingService) quoteImageTierSource(model, tier string, groupConfig *ImagePriceConfig) string {
	if groupConfig != nil {
		switch tier {
		case ImageBillingSize1K:
			if groupConfig.Price1K != nil {
				return "group_config"
			}
		case ImageBillingSize2K:
			if groupConfig.Price2K != nil {
				return "group_config"
			}
		case ImageBillingSize4K:
			if groupConfig.Price4K != nil {
				return "group_config"
			}
		}
	}
	if s.pricingService != nil {
		if pricing := s.pricingService.GetModelPricing(model); pricing != nil && pricing.OutputCostPerImage > 0 {
			return "litellm"
		}
	}
	return "default"
}

// quoteChannelOverrides 列出渠道定价在计费里实际覆盖了哪些字段。
func quoteChannelOverrides(resolved *ResolvedPricing) []string {
	channel := resolved.channelPricing
	if channel == nil {
		return nil
	}
	var fields []string
	switch resolved.Mode {
	case BillingModePerRequest, BillingModeImage:
		if channel.PerRequestPrice != nil {
			fields = append(fields, "per_request")
		}
		if len(resolved.RequestTiers) > 0 {
			fields = append(fields, "tiers")
		}
	default:
		// 有有效区间时 flat 价不生效（applyTokenOverrides 在区间分支提前返回）。
		if len(resolved.Intervals) > 0 {
			fields = append(fields, "intervals")
		} else {
			if channel.InputPrice != nil {
				fields = append(fields, "input")
			}
			if channel.OutputPrice != nil {
				fields = append(fields, "output")
			}
			if channel.CacheWritePrice != nil {
				fields = append(fields, "cache_write")
			}
			if channel.CacheReadPrice != nil {
				fields = append(fields, "cache_read")
			}
		}
		if channel.ImageOutputPrice != nil {
			fields = append(fields, "image_output")
		}
	}
	return fields
}

// quoteLongContext 把价卡上的长上下文规则整理成展示结构。
func quoteLongContext(pricing *ModelPricing) *QuoteLongContext {
	longContext := &QuoteLongContext{
		ThresholdTokens:             pricing.LongContextInputThreshold,
		InputMultiplier:             pricing.LongContextInputMultiplier,
		OutputMultiplier:            pricing.LongContextOutputMultiplier,
		PriorityExcludesLongContext: pricing.PriorityExcludesLongContext,
	}
	if pricing.InputPricePerTokenAbove272K > 0 || pricing.OutputPricePerTokenAbove272K > 0 ||
		pricing.CacheCreationPriceAbove272K > 0 || pricing.CacheReadPricePerTokenAbove272K > 0 {
		explicit := newQuotePriceSet(QuoteUnitPrices{
			Input:      pricing.InputPricePerTokenAbove272K,
			Output:     pricing.OutputPricePerTokenAbove272K,
			CacheWrite: pricing.CacheCreationPriceAbove272K,
			CacheRead:  pricing.CacheReadPricePerTokenAbove272K,
		})
		longContext.ExplicitPrices = &explicit
	}
	return longContext
}

// normalizeQuoteServiceTier 与 OpenAI 网关入口使用同一个归一化（fast→priority，未知值拒绝）。
func normalizeQuoteServiceTier(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", true
	}
	if tier := normalizeOpenAIServiceTier(raw); tier != nil {
		return *tier, true
	}
	return "", false
}

func newQuotePriceSet(perToken QuoteUnitPrices) QuotePriceSet {
	return QuotePriceSet{
		PerToken: perToken,
		PerMTok: QuoteUnitPrices{
			Input:        quoteUSDPerMTok(perToken.Input),
			Output:       quoteUSDPerMTok(perToken.Output),
			CacheWrite:   quoteUSDPerMTok(perToken.CacheWrite),
			CacheRead:    quoteUSDPerMTok(perToken.CacheRead),
			CacheWrite5m: quoteUSDPerMTok(perToken.CacheWrite5m),
			CacheWrite1h: quoteUSDPerMTok(perToken.CacheWrite1h),
			ImageInput:   quoteUSDPerMTok(perToken.ImageInput),
			ImageOutput:  quoteUSDPerMTok(perToken.ImageOutput),
		},
	}
}

// quoteUSDPerMTok 把 USD/token 换成 USD/1M token，用十进制运算并保留 9 位小数，避免浮点噪声。
func quoteUSDPerMTok(perToken float64) float64 {
	if perToken == 0 || math.IsNaN(perToken) || math.IsInf(perToken, 0) {
		return 0
	}
	return decimal.NewFromFloat(perToken).Mul(decimal.NewFromInt(1_000_000)).Round(9).InexactFloat64()
}
