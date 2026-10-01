package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Key 编辑器（回退链）的参考价。
//
// 参考模型由管理员设置项 key_editor_reference_model 决定（按平台一个默认值，值是 JSON 对象
// {"openai":"gpt-5.6-sol",...}），接口可用 ?model= 覆盖。报价复用 PriceQuoter（与网关计费同源）。
//
// 人民币只给一个口径 cny：余额价 = 官方价 × 有效倍率 ÷ 充值倍率，与价格页的「余额价」一致，
// 也就是用户用钱包余额实际要付的人民币。官方汇率（OFFICIAL_PRICE_CNY_RATE）只能乘在未乘倍率的
// 官方价上，不能乘在这里的额度价上（#1478），所以不在本接口里换算；前端要显示官方价的人民币对照，
// 用 official_*_usd_per_mtok 乘公开设置里的汇率。
//
// 前端在 m==1（free 站）或用户切到美元模式时不应使用 cny，而应显示 USD 价（与价格页的 isFiat 一致）。
const SettingKeyEditorReferenceModel = "key_editor_reference_model"

// keyEditorPriceCacheTTL 是单项报价的缓存时间（设计 4.3：编辑器一次要 N 项，不能每次都查库）。
const keyEditorPriceCacheTTL = 30 * time.Second

// keyEditorPriceCacheMaxEntries 限制缓存条目数（?model= 可由用户指定，必须有上限）。
const keyEditorPriceCacheMaxEntries = 4096

// keyEditorModelMaxLen 参考模型名的最大长度。
const keyEditorModelMaxLen = 100

var keyEditorModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\-]*$`)

// ErrKeyEditorInvalidModel 参考模型名不合法。
var ErrKeyEditorInvalidModel = infraerrors.BadRequest("FALLBACK_INVALID_MODEL", "invalid reference model")

// defaultKeyEditorReferenceModels 是每个平台的默认参考模型；管理员设置项优先。
// 模型没有定价时接口降级为 priced=false，不会报错。
var defaultKeyEditorReferenceModels = map[string]string{
	PlatformOpenAI:      "gpt-5.6-sol",
	PlatformAnthropic:   "claude-sonnet-5",
	PlatformGemini:      "gemini-3.1-pro-preview",
	PlatformAntigravity: "gemini-3.1-pro-high",
	PlatformGrok:        "grok-4.3",
}

// KeyEditorCNYPrice 是每百万 token 的人民币价。
type KeyEditorCNYPrice struct {
	InputPerMTok  float64 `json:"input_per_mtok"`
	OutputPerMTok float64 `json:"output_per_mtok"`
}

// KeyEditorReferencePrice 是某分组下参考模型的参考价。Priced=false 时其余字段都不出现。
type KeyEditorReferencePrice struct {
	Priced bool `json:"priced"`
	// InputUSDPerMTok / OutputUSDPerMTok：官方价 × 有效倍率（含用户专属倍率），USD / 百万 token。
	InputUSDPerMTok  *float64 `json:"input_usd_per_mtok,omitempty"`
	OutputUSDPerMTok *float64 `json:"output_usd_per_mtok,omitempty"`
	// OfficialInputUSDPerMTok / OfficialOutputUSDPerMTok：未乘倍率的官方价。
	OfficialInputUSDPerMTok  *float64 `json:"official_input_usd_per_mtok,omitempty"`
	OfficialOutputUSDPerMTok *float64 `json:"official_output_usd_per_mtok,omitempty"`
	// CNY 余额价口径：额度价 ÷ 充值倍率，与价格页的「余额价」一致。
	CNY *KeyEditorCNYPrice `json:"cny,omitempty"`
}

type keyEditorQuoter interface {
	Quote(ctx context.Context, req QuoteRequest) (*Quote, error)
}

type keyEditorPriceCacheEntry struct {
	price   KeyEditorReferencePrice
	expires time.Time
}

type keyEditorRates struct {
	rechargeMult float64
	expires      time.Time
}

// KeyEditorPriceService 给 Key 编辑器出参考价，并管理参考模型设置。
type KeyEditorPriceService struct {
	quoter   keyEditorQuoter
	settings SettingRepository
	now      func() time.Time

	mu    sync.Mutex
	cache map[string]keyEditorPriceCacheEntry
	rates keyEditorRates
}

// NewKeyEditorPriceService 创建编辑器报价服务。
func NewKeyEditorPriceService(quoter *PriceQuoter, settings SettingRepository) *KeyEditorPriceService {
	return newKeyEditorPriceService(quoter, settings, time.Now)
}

func newKeyEditorPriceService(quoter keyEditorQuoter, settings SettingRepository, now func() time.Time) *KeyEditorPriceService {
	if now == nil {
		now = time.Now
	}
	return &KeyEditorPriceService{
		quoter:   quoter,
		settings: settings,
		now:      now,
		cache:    make(map[string]keyEditorPriceCacheEntry),
	}
}

// NormalizeKeyEditorModel 清洗并校验参考模型名。空串表示不覆盖。
func NormalizeKeyEditorModel(raw string) (string, error) {
	model := strings.TrimSpace(raw)
	if model == "" {
		return "", nil
	}
	if len(model) > keyEditorModelMaxLen || !keyEditorModelPattern.MatchString(model) {
		return "", ErrKeyEditorInvalidModel
	}
	return model, nil
}

// ReferenceModels 返回每个平台当前生效的参考模型（管理员设置覆盖默认值）。
func (s *KeyEditorPriceService) ReferenceModels(ctx context.Context) map[string]string {
	out := make(map[string]string, len(defaultKeyEditorReferenceModels))
	for k, v := range defaultKeyEditorReferenceModels {
		out[k] = v
	}
	if s == nil || s.settings == nil {
		return out
	}
	raw, err := s.settings.GetValue(ctx, SettingKeyEditorReferenceModel)
	if err != nil || strings.TrimSpace(raw) == "" {
		return out
	}
	var stored map[string]string
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return out
	}
	for platform, model := range stored {
		model = strings.TrimSpace(model)
		if _, err := NormalizeKeyEditorModel(model); err != nil || model == "" {
			continue
		}
		out[platform] = model
	}
	return out
}

// ReferenceModel 返回某平台的参考模型：override 优先，其次管理员设置，再次内置默认。
func (s *KeyEditorPriceService) ReferenceModel(ctx context.Context, platform, override string) string {
	if override = strings.TrimSpace(override); override != "" {
		return override
	}
	return s.ReferenceModels(ctx)[platform]
}

// SetReferenceModels 保存管理员设置的参考模型（按平台）。
// 语义是整体替换：传入的映射就是保存后的全部覆盖，没传的平台会被清掉、回到内置默认；
// 值为空串的平台同样不保存覆盖，所以调用方要保留某个平台的覆盖，就必须带上完整映射。
func (s *KeyEditorPriceService) SetReferenceModels(ctx context.Context, models map[string]string) (map[string]string, error) {
	if s == nil || s.settings == nil {
		return nil, infraerrors.ServiceUnavailable("SETTINGS_UNAVAILABLE", "settings are not available")
	}
	clean := make(map[string]string, len(models))
	for platform, model := range models {
		platform = strings.TrimSpace(platform)
		if _, ok := defaultKeyEditorReferenceModels[platform]; !ok {
			return nil, infraerrors.BadRequest("FALLBACK_INVALID_PLATFORM", fmt.Sprintf("unsupported platform %q", platform))
		}
		normalized, err := NormalizeKeyEditorModel(model)
		if err != nil {
			return nil, err
		}
		if normalized == "" {
			continue
		}
		clean[platform] = normalized
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		return nil, err
	}
	if err := s.settings.Set(ctx, SettingKeyEditorReferenceModel, string(encoded)); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache = make(map[string]keyEditorPriceCacheEntry)
	s.mu.Unlock()
	return s.ReferenceModels(ctx), nil
}

// loadRechargeMult 读取充值倍率（30 秒缓存）。读取失败回落默认值 1。
func (s *KeyEditorPriceService) loadRechargeMult(ctx context.Context) float64 {
	now := s.now()
	s.mu.Lock()
	if now.Before(s.rates.expires) {
		r := s.rates.rechargeMult
		s.mu.Unlock()
		return r
	}
	s.mu.Unlock()

	rechargeMult := defaultBalanceRechargeMultiplier
	if s.settings != nil {
		vals, err := s.settings.GetMultiple(ctx, []string{SettingBalanceRechargeMult})
		if err == nil {
			rechargeMult = normalizeBalanceRechargeMultiplier(pcParseFloat(vals[SettingBalanceRechargeMult], defaultBalanceRechargeMultiplier))
		}
	}
	s.mu.Lock()
	s.rates = keyEditorRates{rechargeMult: rechargeMult, expires: now.Add(keyEditorPriceCacheTTL)}
	s.mu.Unlock()
	return rechargeMult
}

// ReferencePrice 返回「主分组 primaryGroupID 的 Key 经 groupID 服务」时参考模型的参考价。
// 任何失败（没有模型、分组不存在、未定价、按次计费没有 token 单价）都降级为 priced=false。
func (s *KeyEditorPriceService) ReferencePrice(ctx context.Context, model string, primaryGroupID, groupID, userID int64) KeyEditorReferencePrice {
	model = strings.TrimSpace(model)
	if s == nil || s.quoter == nil || model == "" || primaryGroupID <= 0 || groupID <= 0 {
		return KeyEditorReferencePrice{}
	}
	cacheKey := fmt.Sprintf("%s|%d|%d|%d", model, primaryGroupID, groupID, userID)
	now := s.now()
	s.mu.Lock()
	if e, ok := s.cache[cacheKey]; ok && now.Before(e.expires) {
		s.mu.Unlock()
		return e.price
	}
	s.mu.Unlock()

	price := s.quoteReferencePrice(ctx, model, primaryGroupID, groupID, userID)

	s.mu.Lock()
	if len(s.cache) >= keyEditorPriceCacheMaxEntries {
		s.cache = make(map[string]keyEditorPriceCacheEntry)
	}
	s.cache[cacheKey] = keyEditorPriceCacheEntry{price: price, expires: now.Add(keyEditorPriceCacheTTL)}
	s.mu.Unlock()
	return price
}

func (s *KeyEditorPriceService) quoteReferencePrice(ctx context.Context, model string, primaryGroupID, groupID, userID int64) KeyEditorReferencePrice {
	quote, err := s.quoter.Quote(ctx, QuoteRequest{Model: model, GroupID: primaryGroupID, ServedGroupID: groupID, UserID: userID})
	if err == nil && quote != nil && groupID != primaryGroupID && quote.ServedGroupID != groupID {
		// 服务分组倍率为 0 时 Quoter 会回落到主分组计价；编辑器展示以该分组自身倍率为准，
		// 所以改用它自己作为计价分组再报一次。
		quote, err = s.quoter.Quote(ctx, QuoteRequest{Model: model, GroupID: groupID, UserID: userID})
	}
	if err != nil || quote == nil || !quote.Priced || quote.FinalPrices == nil || quote.Prices == nil {
		return KeyEditorReferencePrice{}
	}

	finalIn, finalOut := quote.FinalPrices.PerMTok.Input, quote.FinalPrices.PerMTok.Output
	officialIn, officialOut := quote.Prices.PerMTok.Input, quote.Prices.PerMTok.Output
	if !finitePrice(finalIn) || !finitePrice(finalOut) || !finitePrice(officialIn) || !finitePrice(officialOut) {
		return KeyEditorReferencePrice{}
	}
	rechargeMult := s.loadRechargeMult(ctx)

	return KeyEditorReferencePrice{
		Priced:                   true,
		InputUSDPerMTok:          roundedPtr(finalIn, 6),
		OutputUSDPerMTok:         roundedPtr(finalOut, 6),
		OfficialInputUSDPerMTok:  roundedPtr(officialIn, 6),
		OfficialOutputUSDPerMTok: roundedPtr(officialOut, 6),
		CNY: &KeyEditorCNYPrice{
			InputPerMTok:  roundTo(finalIn/rechargeMult, 4),
			OutputPerMTok: roundTo(finalOut/rechargeMult, 4),
		},
	}
}

func finitePrice(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0
}

func roundedPtr(v float64, places int) *float64 {
	r := roundTo(v, places)
	return &r
}
