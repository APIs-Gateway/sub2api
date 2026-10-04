package service

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Key 级分组回退链的设置项。全部经 SettingService 读取，不加 config.yaml 项。
// PR1 只定义键、默认值与读取函数：开关默认关闭，且没有任何运行时代码读取它来改变行为。
const (
	SettingKeyGroupFallbackEnabled              = "group_fallback_enabled"
	SettingKeyGroupFallbackBusyWaitMS           = "group_fallback_busy_wait_ms"
	SettingKeyGroupFallbackStickyWaitMS         = "group_fallback_sticky_wait_ms"
	SettingKeyGroupFallbackStickyQueueShare     = "group_fallback_sticky_queue_share"
	SettingKeyGroupFallbackBreakerWindowMS      = "group_fallback_breaker_window_ms"
	SettingKeyGroupFallbackBreakerThreshold     = "group_fallback_breaker_threshold"
	SettingKeyGroupFallbackBreakerOpenTTLMS     = "group_fallback_breaker_open_ttl_ms"
	SettingKeyGroupFallbackBreakerMinUsers      = "group_fallback_breaker_min_distinct_users"
	SettingKeyGroupFallbackTotalBudgetNonStream = "group_fallback_total_budget_ms_nonstream"
	SettingKeyGroupFallbackTotalBudgetStream    = "group_fallback_total_budget_ms_stream"
	SettingKeyGroupFallbackMaxTotalAttempts     = "group_fallback_max_total_attempts"
	SettingKeyGroupFallbackMaxPerHopSwitches    = "group_fallback_max_per_hop_switches"
)

// GroupFallbackSettings 是回退链的运行参数。
type GroupFallbackSettings struct {
	// Enabled 全局开关，默认 false。
	Enabled bool
	// BusyWaitMS 主分组繁忙时短等多久再回退，默认 2000。
	BusyWaitMS int
	// StickyWaitMS 放弃粘性账号前最多等多久，默认 8000。
	StickyWaitMS int
	// StickyQueueShare 回退流量在非主分组排队时可占用的队列份额（0,1]，默认 0.3。
	StickyQueueShare float64
	// 熔断参数：按「分组 × 模型族」，窗口内失败达到阈值且有足够多不同用户贡献才打开。
	BreakerWindowMS        int
	BreakerThreshold       int
	BreakerOpenTTLMS       int
	BreakerMinDistinctUser int
	// TotalBudgetNonStreamMS / TotalBudgetStreamMS 回退阶段的总时间预算。
	TotalBudgetNonStreamMS int
	TotalBudgetStreamMS    int
	// MaxTotalAttempts 每请求上游尝试总次数上限。
	MaxTotalAttempts int
	// MaxPerHopSwitches 每一跳内的换号次数上限。
	MaxPerHopSwitches int
}

// DefaultGroupFallbackSettings 返回默认值（开关关闭）。
func DefaultGroupFallbackSettings() GroupFallbackSettings {
	return GroupFallbackSettings{
		Enabled:                false,
		BusyWaitMS:             2000,
		StickyWaitMS:           8000,
		StickyQueueShare:       0.3,
		BreakerWindowMS:        30000,
		BreakerThreshold:       5,
		BreakerOpenTTLMS:       30000,
		BreakerMinDistinctUser: 3,
		TotalBudgetNonStreamMS: 25000,
		TotalBudgetStreamMS:    60000,
		MaxTotalAttempts:       12,
		MaxPerHopSwitches:      3,
	}
}

var groupFallbackSettingKeys = []string{
	SettingKeyGroupFallbackEnabled,
	SettingKeyGroupFallbackBusyWaitMS,
	SettingKeyGroupFallbackStickyWaitMS,
	SettingKeyGroupFallbackStickyQueueShare,
	SettingKeyGroupFallbackBreakerWindowMS,
	SettingKeyGroupFallbackBreakerThreshold,
	SettingKeyGroupFallbackBreakerOpenTTLMS,
	SettingKeyGroupFallbackBreakerMinUsers,
	SettingKeyGroupFallbackTotalBudgetNonStream,
	SettingKeyGroupFallbackTotalBudgetStream,
	SettingKeyGroupFallbackMaxTotalAttempts,
	SettingKeyGroupFallbackMaxPerHopSwitches,
}

// parseGroupFallbackSettings 把存储的键值解析成参数；缺失、空串、非法值一律回落默认值。
func parseGroupFallbackSettings(vals map[string]string) GroupFallbackSettings {
	out := DefaultGroupFallbackSettings()
	out.Enabled = strings.EqualFold(strings.TrimSpace(vals[SettingKeyGroupFallbackEnabled]), "true")
	out.BusyWaitMS = positiveIntSetting(vals[SettingKeyGroupFallbackBusyWaitMS], out.BusyWaitMS)
	out.StickyWaitMS = positiveIntSetting(vals[SettingKeyGroupFallbackStickyWaitMS], out.StickyWaitMS)
	if v, err := strconv.ParseFloat(strings.TrimSpace(vals[SettingKeyGroupFallbackStickyQueueShare]), 64); err == nil && v > 0 && v <= 1 {
		out.StickyQueueShare = v
	}
	out.BreakerWindowMS = positiveIntSetting(vals[SettingKeyGroupFallbackBreakerWindowMS], out.BreakerWindowMS)
	out.BreakerThreshold = positiveIntSetting(vals[SettingKeyGroupFallbackBreakerThreshold], out.BreakerThreshold)
	out.BreakerOpenTTLMS = positiveIntSetting(vals[SettingKeyGroupFallbackBreakerOpenTTLMS], out.BreakerOpenTTLMS)
	out.BreakerMinDistinctUser = positiveIntSetting(vals[SettingKeyGroupFallbackBreakerMinUsers], out.BreakerMinDistinctUser)
	out.TotalBudgetNonStreamMS = positiveIntSetting(vals[SettingKeyGroupFallbackTotalBudgetNonStream], out.TotalBudgetNonStreamMS)
	out.TotalBudgetStreamMS = positiveIntSetting(vals[SettingKeyGroupFallbackTotalBudgetStream], out.TotalBudgetStreamMS)
	out.MaxTotalAttempts = positiveIntSetting(vals[SettingKeyGroupFallbackMaxTotalAttempts], out.MaxTotalAttempts)
	out.MaxPerHopSwitches = positiveIntSetting(vals[SettingKeyGroupFallbackMaxPerHopSwitches], out.MaxPerHopSwitches)
	return out
}

func positiveIntSetting(raw string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

// 回退链设置的进程内缓存时间（热路径每个带链请求都会读）。管理员改设置后，各实例最长一个 TTL 生效。
const (
	groupFallbackSettingsCacheTTL  = 60 * time.Second
	groupFallbackSettingsErrorTTL  = 5 * time.Second
	groupFallbackSettingsDBTimeout = 5 * time.Second
)

type cachedGroupFallbackSettings struct {
	settings  GroupFallbackSettings
	expiresAt int64 // unix nano
}

// GetGroupFallbackSettings 读取回退链设置，进程内缓存 60 秒（读取失败缓存 5 秒的默认值）。
// 读取失败时返回默认值（开关关闭，fail-closed）。热路径可以放心调用，不会每次访问数据库。
func (s *SettingService) GetGroupFallbackSettings(ctx context.Context) GroupFallbackSettings {
	if s == nil || s.settingRepo == nil {
		return DefaultGroupFallbackSettings()
	}
	if cached, ok := s.groupFallbackSettingsCache.Load().(*cachedGroupFallbackSettings); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.settings
		}
	}
	result, _, _ := s.groupFallbackSettingsSF.Do("group_fallback_settings", func() (any, error) {
		if cached, ok := s.groupFallbackSettingsCache.Load().(*cachedGroupFallbackSettings); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), groupFallbackSettingsDBTimeout)
		defer cancel()
		vals, err := s.settingRepo.GetMultiple(dbCtx, groupFallbackSettingKeys)
		var entry *cachedGroupFallbackSettings
		if err != nil {
			slog.Warn("failed to get group fallback settings, using defaults (disabled)", "error", err)
			entry = &cachedGroupFallbackSettings{
				settings:  DefaultGroupFallbackSettings(),
				expiresAt: time.Now().Add(groupFallbackSettingsErrorTTL).UnixNano(),
			}
		} else {
			entry = &cachedGroupFallbackSettings{
				settings:  parseGroupFallbackSettings(vals),
				expiresAt: time.Now().Add(groupFallbackSettingsCacheTTL).UnixNano(),
			}
		}
		s.groupFallbackSettingsCache.Store(entry)
		return entry, nil
	})
	if entry, ok := result.(*cachedGroupFallbackSettings); ok && entry != nil {
		return entry.settings
	}
	return DefaultGroupFallbackSettings()
}

// IsGroupFallbackEnabled 返回回退链全局开关，fail-closed。
func (s *SettingService) IsGroupFallbackEnabled(ctx context.Context) bool {
	return s.GetGroupFallbackSettings(ctx).Enabled
}
