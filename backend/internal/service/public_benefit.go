package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// PublicBenefitConfig 公益 key（hvoy/hovy 等）单 IP 每自然日消费上限配置。
type PublicBenefitConfig struct {
	Enabled     bool     `json:"enabled"`
	DailyCapUSD float64  `json:"daily_cap_usd"` // 单 IP 每自然日消费上限（USD），<=0 视为不限制
	KeyNames    []string `json:"key_names"`     // 公益 key 名单（小写、去重）
	Message     string   `json:"message"`       // 超额时返回的文案（HTTP 200）
}

// matchesKeyName 判断给定 key 名是否属于公益 key 名单（大小写不敏感）。
func (c PublicBenefitConfig) matchesKeyName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, n := range c.KeyNames {
		if n == name {
			return true
		}
	}
	return false
}

// parsePublicBenefitKeyNames 解析逗号分隔名单为小写去重切片。
func parsePublicBenefitKeyNames(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// GetPublicBenefitConfig 读取公益 key 上限配置（缺失键回退默认）。
func (s *SettingService) GetPublicBenefitConfig(ctx context.Context) PublicBenefitConfig {
	vals, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyPublicBenefitIPCapEnabled,
		SettingKeyPublicBenefitIPDailyCapUSD,
		SettingKeyPublicBenefitKeyNames,
		SettingKeyPublicBenefitIPCapMessage,
	})
	if err != nil || vals == nil {
		vals = map[string]string{}
	}
	enabled := PublicBenefitIPCapEnabledDefault
	if v, ok := vals[SettingKeyPublicBenefitIPCapEnabled]; ok && strings.TrimSpace(v) != "" {
		enabled = v == "true"
	}
	cap := parseFloatDefault(vals[SettingKeyPublicBenefitIPDailyCapUSD], PublicBenefitIPDailyCapUSDDefault)
	if cap < 0 {
		cap = 0
	}
	namesRaw := vals[SettingKeyPublicBenefitKeyNames]
	if strings.TrimSpace(namesRaw) == "" {
		namesRaw = PublicBenefitKeyNamesDefault
	}
	msg := vals[SettingKeyPublicBenefitIPCapMessage]
	if strings.TrimSpace(msg) == "" {
		msg = PublicBenefitIPCapMessageDefault
	}
	return PublicBenefitConfig{
		Enabled:     enabled,
		DailyCapUSD: cap,
		KeyNames:    parsePublicBenefitKeyNames(namesRaw),
		Message:     msg,
	}
}

// UpdatePublicBenefitConfig 持久化公益 key 上限配置并触发缓存失效回调。
func (s *SettingService) UpdatePublicBenefitConfig(ctx context.Context, cfg PublicBenefitConfig) error {
	if cfg.DailyCapUSD < 0 {
		cfg.DailyCapUSD = 0
	}
	names := strings.Join(parsePublicBenefitKeyNames(strings.Join(cfg.KeyNames, ",")), ",")
	msg := strings.TrimSpace(cfg.Message)
	if msg == "" {
		msg = PublicBenefitIPCapMessageDefault
	}
	updates := map[string]string{
		SettingKeyPublicBenefitIPCapEnabled:  strconv.FormatBool(cfg.Enabled),
		SettingKeyPublicBenefitIPDailyCapUSD: strconv.FormatFloat(cfg.DailyCapUSD, 'f', 4, 64),
		SettingKeyPublicBenefitKeyNames:      names,
		SettingKeyPublicBenefitIPCapMessage:  msg,
	}
	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return err
	}
	if s.onUpdate != nil {
		s.onUpdate()
	}
	return nil
}

// ===== BillingCacheService：公益 key 单 IP 每日上限的检查与累加 =====

const publicBenefitConfigCacheTTL = 30 * time.Second

// publicBenefitConfig 读取公益 key 配置（进程内 30s 短缓存，避免热路径每请求读 DB）。
func (s *BillingCacheService) publicBenefitConfig(ctx context.Context) PublicBenefitConfig {
	now := time.Now().UnixNano()
	s.pbMu.RLock()
	if s.pbLoadedAt != 0 && now-s.pbLoadedAt < int64(publicBenefitConfigCacheTTL) {
		cfg := s.pbCfg
		s.pbMu.RUnlock()
		return cfg
	}
	s.pbMu.RUnlock()

	cfg := s.settingService.GetPublicBenefitConfig(ctx)
	s.pbMu.Lock()
	s.pbCfg = cfg
	s.pbLoadedAt = now
	s.pbMu.Unlock()
	return cfg
}

// publicBenefitDateKey 当前 Asia/Shanghai 自然日（YYYYMMDD），用作每日计数器的日期维度。
func publicBenefitDateKey() string {
	return timezone.StartOfDay(timezone.Now()).Format("20060102")
}

// publicBenefitTTLSeconds 计数器存活到次日自然日 + 1 小时缓冲（日期维度滚动天然清零）。
func publicBenefitTTLSeconds() int {
	next := timezone.StartOfDay(timezone.Now()).Add(24 * time.Hour)
	secs := int(time.Until(next).Seconds()) + 3600
	if secs < 3600 {
		secs = 3600
	}
	return secs
}

// PublicBenefitIPCapExceeded 当 apiKey 为公益 key 且该 clientIP 今日累计消费已达上限时，
// 返回 (true, 文案)。非公益 key / 未开启 / 上限<=0 / Redis 故障 均返回 (false, "")（fail-open）。
func (s *BillingCacheService) PublicBenefitIPCapExceeded(ctx context.Context, apiKey *APIKey, clientIP string) (bool, string) {
	if s == nil || s.cache == nil || apiKey == nil || strings.TrimSpace(clientIP) == "" {
		return false, ""
	}
	cfg := s.publicBenefitConfig(ctx)
	if !cfg.Enabled || cfg.DailyCapUSD <= 0 || !cfg.matchesKeyName(apiKey.Name) {
		return false, ""
	}
	spent, err := s.cache.GetPublicBenefitIPSpend(ctx, publicBenefitDateKey(), clientIP)
	if err != nil {
		return false, "" // fail-open：Redis 故障不拦正常请求
	}
	if spent >= cfg.DailyCapUSD {
		return true, cfg.Message
	}
	return false, ""
}

// AddPublicBenefitIPSpend 公益 key 请求计费后，累加该 IP 当日消费（best-effort，非公益 key 直接跳过）。
func (s *BillingCacheService) AddPublicBenefitIPSpend(ctx context.Context, apiKey *APIKey, clientIP string, amount float64) {
	if s == nil || s.cache == nil || apiKey == nil || strings.TrimSpace(clientIP) == "" || amount <= 0 {
		return
	}
	cfg := s.publicBenefitConfig(ctx)
	if !cfg.Enabled || !cfg.matchesKeyName(apiKey.Name) {
		return
	}
	cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = s.cache.IncrPublicBenefitIPSpend(cctx, publicBenefitDateKey(), clientIP, amount, publicBenefitTTLSeconds())
}
