package service

import (
	"encoding/json"
	"fmt"
	"time"
)

// W6 价格与模型配置重构（PR2「建表与派生」）的数据类型。
//
// 这些类型对应三组新表：model_catalog、group_model_config / model_group_prices、
// cost_accounting_rules。本 PR 没有任何计费、调度、准入路径读取它们；
// 唯一的写入方是渠道保存之后的派生钩子（pricing_matrix_service.go）。

// PricingStage 分组的价格体系阶段（设计文档 4.3）。
type PricingStage string

const (
	PricingStageLegacy PricingStage = "legacy"
	PricingStageShadow PricingStage = "shadow"
	PricingStageV2     PricingStage = "v2"
)

// MatrixAccessMode 分组的准入模式。
type MatrixAccessMode string

const (
	MatrixAccessOpen      MatrixAccessMode = "open"
	MatrixAccessAllowlist MatrixAccessMode = "allowlist"
)

// MatrixCostMode 分组的成本核算模式（设计文档 2.3）。
type MatrixCostMode string

const (
	MatrixCostAccountRate     MatrixCostMode = "account_rate"
	MatrixCostCatalogUpstream MatrixCostMode = "catalog_upstream"
	MatrixCostFollowBilling   MatrixCostMode = "follow_billing"
)

// MatrixPriceMode 单元格的价格模式，三者互斥。
type MatrixPriceMode string

const (
	MatrixPriceInherit MatrixPriceMode = "inherit"
	MatrixPriceExtra   MatrixPriceMode = "extra"
	MatrixPriceCustom  MatrixPriceMode = "custom"
)

// MatrixSource 单元格与成本核算规则的来源。
type MatrixSource string

const (
	MatrixSourceManual        MatrixSource = "manual"
	MatrixSourceCopied        MatrixSource = "copied"
	MatrixSourceLegacyDerived MatrixSource = "legacy_derived"
	MatrixSourceLegacyFrozen  MatrixSource = "legacy_frozen"
)

// ModelCatalogStatus 模型目录状态（设计文档 2.2）。
type ModelCatalogStatus string

const (
	ModelCatalogDraft   ModelCatalogStatus = "draft"
	ModelCatalogActive  ModelCatalogStatus = "active"
	ModelCatalogRetired ModelCatalogStatus = "retired"
)

// 派生备注的级别。
const (
	DerivationNoteInfo = "info"
	DerivationNoteWarn = "warn"
)

// MatrixMappingEntry 有序映射数组里的一项。Src 保留原始写法（含通配符的尾部 *），
// 查找端按小写比较；Dst 原样保留（包括空串）。
type MatrixMappingEntry struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

// MatrixGroupConfig 一个分组的矩阵侧配置（不含阶段与版本号）。
type MatrixGroupConfig struct {
	AccessMode MatrixAccessMode `json:"access_mode"`
	// BillingModelSource 为 nil 表示分组没有（启用的）渠道，legacy 此时返回空串（S-1）。
	BillingModelSource *string              `json:"billing_model_source"`
	ModelMapping       []MatrixMappingEntry `json:"model_mapping"`
	// Features 的形状按开关各自实际生效的形状保留（S-2）：值只会是 bool 或 map[string]any（值为 bool）。
	Features map[string]any `json:"features"`
	CostMode MatrixCostMode `json:"cost_mode"`
}

// StoredGroupConfig group_model_config 的一行。
type StoredGroupConfig struct {
	GroupID int64 `json:"group_id"`
	MatrixGroupConfig
	PricingStage   PricingStage `json:"pricing_stage"`
	StageChangedAt *time.Time   `json:"stage_changed_at,omitempty"`
	StageChangedBy *int64       `json:"stage_changed_by,omitempty"`
	Revision       int64        `json:"revision"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

// MatrixPriceInterval 区间定价（与 PricingInterval 同形，去掉数据库 id）。
type MatrixPriceInterval struct {
	MinTokens       int      `json:"min_tokens"`
	MaxTokens       *int     `json:"max_tokens,omitempty"`
	TierLabel       string   `json:"tier_label,omitempty"`
	InputPrice      *float64 `json:"input_price,omitempty"`
	OutputPrice     *float64 `json:"output_price,omitempty"`
	CacheWritePrice *float64 `json:"cache_write_price,omitempty"`
	CacheReadPrice  *float64 `json:"cache_read_price,omitempty"`
	PerRequestPrice *float64 `json:"per_request_price,omitempty"`
	SortOrder       int      `json:"sort_order"`
}

// MatrixCustomPrice 单元格 custom_price 与成本核算规则 price 的 JSON 形状，
// 与 ChannelModelPricing 同形（设计文档 2.4）：读取时用 ToChannelModelPricing 还原后交给现有的
// 价格解析函数，所以 custom 仍是逐字段覆盖，字段为空即回落官方价。
type MatrixCustomPrice struct {
	BillingMode      BillingMode           `json:"billing_mode"`
	InputPrice       *float64              `json:"input_price,omitempty"`
	OutputPrice      *float64              `json:"output_price,omitempty"`
	CacheWritePrice  *float64              `json:"cache_write_price,omitempty"`
	CacheReadPrice   *float64              `json:"cache_read_price,omitempty"`
	ImageOutputPrice *float64              `json:"image_output_price,omitempty"`
	PerRequestPrice  *float64              `json:"per_request_price,omitempty"`
	Intervals        []MatrixPriceInterval `json:"intervals,omitempty"`
}

// MatrixCustomPriceFromPricing 由渠道定价条目构造 custom_price。
// billing_mode 为空时规范成 token（与 repository 写入渠道定价时的默认值一致）。
func MatrixCustomPriceFromPricing(p ChannelModelPricing) MatrixCustomPrice {
	out := MatrixCustomPrice{
		BillingMode:      matrixBillingMode(p),
		InputPrice:       p.InputPrice,
		OutputPrice:      p.OutputPrice,
		CacheWritePrice:  p.CacheWritePrice,
		CacheReadPrice:   p.CacheReadPrice,
		ImageOutputPrice: p.ImageOutputPrice,
		PerRequestPrice:  p.PerRequestPrice,
	}
	if len(p.Intervals) > 0 {
		out.Intervals = make([]MatrixPriceInterval, len(p.Intervals))
		for i, iv := range p.Intervals {
			out.Intervals[i] = MatrixPriceInterval{
				MinTokens:       iv.MinTokens,
				MaxTokens:       iv.MaxTokens,
				TierLabel:       iv.TierLabel,
				InputPrice:      iv.InputPrice,
				OutputPrice:     iv.OutputPrice,
				CacheWritePrice: iv.CacheWritePrice,
				CacheReadPrice:  iv.CacheReadPrice,
				PerRequestPrice: iv.PerRequestPrice,
				SortOrder:       iv.SortOrder,
			}
		}
	}
	return out
}

// ToChannelModelPricing 还原成渠道定价条目，供现有的价格解析函数使用。
func (c MatrixCustomPrice) ToChannelModelPricing(platform string, models []string) ChannelModelPricing {
	out := ChannelModelPricing{
		Platform:         platform,
		Models:           append([]string(nil), models...),
		BillingMode:      c.BillingMode,
		InputPrice:       c.InputPrice,
		OutputPrice:      c.OutputPrice,
		CacheWritePrice:  c.CacheWritePrice,
		CacheReadPrice:   c.CacheReadPrice,
		ImageOutputPrice: c.ImageOutputPrice,
		PerRequestPrice:  c.PerRequestPrice,
	}
	if len(c.Intervals) > 0 {
		out.Intervals = make([]PricingInterval, len(c.Intervals))
		for i, iv := range c.Intervals {
			out.Intervals[i] = PricingInterval{
				MinTokens:       iv.MinTokens,
				MaxTokens:       iv.MaxTokens,
				TierLabel:       iv.TierLabel,
				InputPrice:      iv.InputPrice,
				OutputPrice:     iv.OutputPrice,
				CacheWritePrice: iv.CacheWritePrice,
				CacheReadPrice:  iv.CacheReadPrice,
				PerRequestPrice: iv.PerRequestPrice,
				SortOrder:       iv.SortOrder,
			}
		}
	}
	return out
}

// MatrixCell 矩阵单元格（model_group_prices 的一行，不含数据库字段）。
type MatrixCell struct {
	ModelKey        string             `json:"model_key"`
	IsPattern       bool               `json:"is_pattern"`
	PatternOrder    int                `json:"pattern_order"`
	Open            bool               `json:"open"`
	PriceMode       MatrixPriceMode    `json:"price_mode"`
	ExtraMultiplier *float64           `json:"extra_multiplier,omitempty"`
	CustomPrice     *MatrixCustomPrice `json:"custom_price,omitempty"`
	Source          MatrixSource       `json:"source"`
}

// StoredMatrixCell model_group_prices 的一行。
type StoredMatrixCell struct {
	ID            int64      `json:"id"`
	GroupID       int64      `json:"group_id"`
	EffectiveFrom *time.Time `json:"effective_from,omitempty"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	Revision      int64      `json:"revision"`
	UpdatedAt     time.Time  `json:"updated_at"`
	MatrixCell
}

// MatrixCostRulePrice 成本核算规则里的一条模型定价。Platform 为空表示匹配任意平台（BK-2）。
type MatrixCostRulePrice struct {
	Platform string            `json:"platform"`
	Models   []string          `json:"models"`
	Price    MatrixCustomPrice `json:"price"`
}

// MatrixCostRule 成本核算规则（cost_accounting_rules 的一行，不含数据库字段）。
// SourceChannelID、SourceOrdinal 为 0 表示没有来源（manual），派生行的 SourceOrdinal 从 1 起。
type MatrixCostRule struct {
	Name            string                `json:"name"`
	SourceChannelID int64                 `json:"source_channel_id"`
	SourceOrdinal   int                   `json:"source_ordinal"`
	GroupIDs        []int64               `json:"group_ids"`
	AccountIDs      []int64               `json:"account_ids"`
	SortOrder       int                   `json:"sort_order"`
	Enabled         bool                  `json:"enabled"`
	Prices          []MatrixCostRulePrice `json:"prices"`
}

// StoredMatrixCostRule cost_accounting_rules 的一行（价格行已并入 Prices）。
type StoredMatrixCostRule struct {
	ID           int64        `json:"id"`
	ScopeGroupID int64        `json:"scope_group_id"`
	Source       MatrixSource `json:"source"`
	MatrixCostRule
}

// DerivationNote 派生过程里的一条备注：没有运行时效果、但管理员或审查者需要看到的决定。
type DerivationNote struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Model   string `json:"model,omitempty"`
	Message string `json:"message"`
}

// DeriveGroup 派生的分组输入。
type DeriveGroup struct {
	ID       int64
	Platform string
	// Deleted 为 true 表示分组已软删除；派生按「无渠道」处理。
	Deleted bool
}

// OfficialPriceFact 官方价数据里关于某个模型的两条事实。
type OfficialPriceFact struct {
	// HasPrice 官方价（动态目录或内置兜底）里有这个模型的价格。
	HasPrice bool `json:"has_price"`
	// ImageCapable 动态目录里这个模型是图片生成模型（有按张价或图片 token 价）。
	ImageCapable bool `json:"image_capable"`
}

// OfficialPriceFacts 由调用方传入的价格快照，键是 officialPriceFactKey(模型名)。
// 派生函数保持纯函数：它不自己去读会变动的价格数据，只读这份显式输入，
// 并把用到的那部分事实的摘要折进派生 revision（CHECK_OPUS_3 3.4）。
type OfficialPriceFacts map[string]OfficialPriceFact

// OfficialPriceFactSource 查询官方价事实的来源，由 BillingService 实现。
type OfficialPriceFactSource interface {
	LookupOfficialPriceFact(model string) OfficialPriceFact
}

// DerivedGroupState 一个分组的派生结果。
type DerivedGroupState struct {
	GroupID int64 `json:"group_id"`
	// ChannelID 为 0 表示按「无渠道」处理（没有渠道、渠道停用、分组已删除、分组不在渠道里）。
	ChannelID int64             `json:"channel_id"`
	Config    MatrixGroupConfig `json:"config"`
	Cells     []MatrixCell      `json:"cells"`
	CostRules []MatrixCostRule  `json:"cost_rules"`
	Notes     []DerivationNote  `json:"notes"`
	// Revision 是配置、单元格、成本核算规则与所用官方价事实的摘要。
	Revision string `json:"revision"`
}

// matrixCanonicalJSON 返回值的规范 JSON（map 键按字典序），用于相等比较与摘要。
func matrixCanonicalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// 不可序列化（理论上只有 NaN / Inf）时返回带错误信息的串，保证两侧不会因为都失败而被当成相等。
		return fmt.Sprintf("!marshal-error:%v:%T", err, v)
	}
	return string(b)
}
