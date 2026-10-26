//go:build unit

package service

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 渠道缓存里的模型映射由 expandMappingToCache 展开。Channel.ModelMapping 是 map，
// 遍历顺序每次都不同，这里证明展开结果与遍历顺序无关：
//   - 互为前缀的通配符并存时，固定命中前缀最长的；
//   - 只差大小写的 src，固定由原始 src 字节序靠后的那个生效；
//   - 能通过保存校验的配置（互不重叠），结果与改动前的实现一致；
//   - 同一份配置反复重建缓存，结果完全一致。
//
// 保存渠道时 validateNoConflictingMappings 会拒绝互为前缀、只差大小写的 src，
// 所以带冲突的配置只可能来自校验之前写入的数据；这里直接构造缓存来覆盖这类数据。

const (
	mappingOrderRounds   = 100
	mappingOrderGroupID  = int64(10)
	mappingOrderGroupID2 = int64(20)
	mappingOrderPlatform = PlatformAnthropic
)

type mappingOrderProbe struct {
	model string
	want  string // 期望的映射目标，空串表示没有命中映射
}

func mappingOrderSavedConfig(mapping map[string]string) map[string]map[string]string {
	return map[string]map[string]string{mappingOrderPlatform: mapping}
}

// buildMappingOrderCache 走线上缓存重建用的同一个入口 populateChannelCache 展开一份映射配置。
func buildMappingOrderCache(mapping map[string]string) *channelCache {
	ch := Channel{
		ID:           1,
		Status:       StatusActive,
		GroupIDs:     []int64{mappingOrderGroupID},
		ModelMapping: mappingOrderSavedConfig(mapping),
	}
	return populateChannelCache([]Channel{ch}, map[int64]string{mappingOrderGroupID: mappingOrderPlatform})
}

func mappingOrderWildcards(cache *channelCache) []*wildcardMappingEntry {
	return cache.wildcardMappingByGP[channelGroupPlatformKey{groupID: mappingOrderGroupID, platform: mappingOrderPlatform}]
}

// mappingOrderWildcardPrefixes 返回缓存里通配符映射的匹配顺序（只看前缀）。
func mappingOrderWildcardPrefixes(cache *channelCache) []string {
	entries := mappingOrderWildcards(cache)
	prefixes := make([]string, 0, len(entries))
	for _, entry := range entries {
		prefixes = append(prefixes, entry.prefix)
	}
	return prefixes
}

// mappingOrderWildcardPairs 返回通配符条目的「前缀=目标」，不保证顺序，用来比较条目集合。
func mappingOrderWildcardPairs(cache *channelCache) []string {
	entries := mappingOrderWildcards(cache)
	pairs := make([]string, 0, len(entries))
	for _, entry := range entries {
		pairs = append(pairs, entry.prefix+"="+entry.target)
	}
	return pairs
}

// mappingOrderLookup 与 resolveMapping 一样先转小写再查找；返回空串表示没有命中映射。
func mappingOrderLookup(cache *channelCache, model string) string {
	return lookupMappingAcrossPlatforms(cache, mappingOrderGroupID, mappingOrderPlatform, strings.ToLower(model))
}

// mappingOrderFingerprint 把缓存里和映射有关的内容压成一个字符串：精确名、通配符（含顺序）、探测结果。
func mappingOrderFingerprint(cache *channelCache, probes []mappingOrderProbe) string {
	lines := make([]string, 0, len(cache.mappingByGroupModel)+len(probes))
	for key, target := range cache.mappingByGroupModel {
		lines = append(lines, "exact "+key.model+"="+target)
	}
	// 精确名存在 map 里，排序后才可比较；通配符的顺序是被测对象，保持缓存里的原样
	sort.Strings(lines)
	for _, entry := range mappingOrderWildcards(cache) {
		lines = append(lines, "wildcard "+entry.prefix+"="+entry.target)
	}
	for _, probe := range probes {
		lines = append(lines, "probe "+probe.model+"="+mappingOrderLookup(cache, probe.model))
	}
	return strings.Join(lines, "\n")
}

// mappingOrderMixedConfig 是一份把几类情况都带上的配置：互为前缀的通配符链、
// 只差大小写的通配符与精确名、与通配符同前缀的精确名。共 15 条，超过 Go map 单个桶的容量，
// 遍历顺序更接近完全随机。
func mappingOrderMixedConfig() map[string]string {
	return map[string]string{
		// 互为前缀的通配符链，"*" 是最短的全匹配
		"*":                 "t-all",
		"claude-*":          "t-claude",
		"claude-opus-*":     "t-opus",
		"claude-sonnet-*":   "t-sonnet",
		"claude-sonnet-4-*": "t-sonnet-4",
		"gpt-*":             "t-gpt",
		"gpt-5-*":           "t-gpt-5",
		"gpt-5-codex-*":     "t-gpt-5-codex",
		// 只差大小写：通配符一组、精确名一组
		"DeepSeek-*": "t-ds-upper",
		"deepseek-*": "t-ds-lower",
		"O3":         "t-o3-upper",
		"o3":         "t-o3-lower",
		// 精确名，其中两个与通配符同前缀
		"claude-sonnet-4": "t-exact-sonnet-4",
		"gpt-5":           "t-exact-gpt-5",
		"gemini-2.5-pro":  "t-exact-gemini",
	}
}

var (
	mappingOrderMixedWildcardOrder = []string{
		"claude-sonnet-4-", // 16
		"claude-sonnet-",   // 14
		"claude-opus-",     // 12，与下一条同长，按字典序在前
		"gpt-5-codex-",     // 12
		"deepseek-",        // 9，DeepSeek-* 与 deepseek-* 合并成一条
		"claude-",          // 7
		"gpt-5-",           // 6
		"gpt-",             // 4
		"",                 // 0，即 "*"
	}

	mappingOrderMixedProbes = []mappingOrderProbe{
		{"claude-sonnet-4-5", "t-sonnet-4"},
		{"claude-sonnet-4", "t-exact-sonnet-4"},
		{"claude-sonnet-3-7", "t-sonnet"},
		{"claude-opus-4-1", "t-opus"},
		{"claude-haiku-4-5", "t-claude"},
		{"Claude-Haiku-4-5", "t-claude"},
		{"gpt-5-codex-max", "t-gpt-5-codex"},
		{"gpt-5", "t-exact-gpt-5"},
		{"gpt-5-mini", "t-gpt-5"},
		{"gpt-4o", "t-gpt"},
		{"DeepSeek-V3", "t-ds-lower"},
		{"o3", "t-o3-lower"},
		{"O3", "t-o3-lower"},
		{"gemini-2.5-pro", "t-exact-gemini"},
		{"llama-3", "t-all"},
	}
)

func TestExpandMappingToCache_OverlappingWildcardsLongestPrefixWins(t *testing.T) {
	mapping := map[string]string{
		"*":                 "t-all",
		"claude-*":          "t-claude",
		"claude-sonnet-*":   "t-sonnet",
		"claude-sonnet-4-*": "t-sonnet-4",
		"gpt-*":             "t-gpt",
	}
	// 互为前缀的配置过不了保存校验，只可能是校验之前写入的数据；缓存层仍要给出确定的结果
	require.Error(t, validateNoConflictingMappings(mappingOrderSavedConfig(mapping)))

	wantOrder := []string{"claude-sonnet-4-", "claude-sonnet-", "claude-", "gpt-", ""}
	probes := []mappingOrderProbe{
		{"claude-sonnet-4-5", "t-sonnet-4"},
		{"claude-sonnet-3-7", "t-sonnet"},
		{"claude-opus-4", "t-claude"},
		{"Claude-Sonnet-4-5", "t-sonnet-4"},
		{"gpt-5", "t-gpt"},
		{"gemini-2.5-pro", "t-all"},
	}

	for round := 0; round < mappingOrderRounds; round++ {
		cache := buildMappingOrderCache(mapping)
		require.Equal(t, wantOrder, mappingOrderWildcardPrefixes(cache), "round %d", round)
		for _, probe := range probes {
			require.Equal(t, probe.want, mappingOrderLookup(cache, probe.model), "round %d model %s", round, probe.model)
		}
	}
}

func TestExpandMappingToCache_SameLengthWildcardsOrderedLexicographically(t *testing.T) {
	// 互不为前缀的通配符，长度相同时按前缀字典序，长度不同时长者在前
	mapping := map[string]string{
		"zz-*": "t-zz",
		"a-*":  "t-a",
		"bb-*": "t-bb",
		"m-*":  "t-m",
		"ab-*": "t-ab",
	}
	require.NoError(t, validateNoConflictingMappings(mappingOrderSavedConfig(mapping)))

	wantOrder := []string{"ab-", "bb-", "zz-", "a-", "m-"}
	probes := []mappingOrderProbe{
		{"ab-1", "t-ab"},
		{"bb-1", "t-bb"},
		{"zz-1", "t-zz"},
		{"a-1", "t-a"},
		{"m-1", "t-m"},
		{"c-1", ""},
	}

	for round := 0; round < mappingOrderRounds; round++ {
		cache := buildMappingOrderCache(mapping)
		require.Equal(t, wantOrder, mappingOrderWildcardPrefixes(cache), "round %d", round)
		for _, probe := range probes {
			require.Equal(t, probe.want, mappingOrderLookup(cache, probe.model), "round %d model %s", round, probe.model)
		}
	}
}

func TestExpandMappingToCache_ExactCaseCollisionLaterSrcWins(t *testing.T) {
	// 只差大小写的精确 src 小写后同键，按原始 src 的字节序处理，字节序靠后的覆盖靠前的。
	// 大写字母（0x41-0x5A）的字节小于小写字母（0x61-0x7A），所以首个不同的位置上小写的那个靠后。
	tests := []struct {
		name    string
		mapping map[string]string
		probe   string
		want    string
	}{
		{
			name:    "大写与小写",
			mapping: map[string]string{"GPT-5": "t-upper", "gpt-5": "t-lower"},
			probe:   "gpt-5",
			want:    "t-lower",
		},
		{
			name:    "三种写法",
			mapping: map[string]string{"GPT-5": "t-upper", "Gpt-5": "t-mixed", "gpt-5": "t-lower"},
			probe:   "GPT-5",
			want:    "t-lower",
		},
		{
			name:    "胜出的是混合大小写",
			mapping: map[string]string{"Ab-c": "t-first", "aB-c": "t-second"},
			probe:   "ab-c",
			want:    "t-second",
		},
		{
			name:    "夹在其他精确名中间",
			mapping: map[string]string{"Model-X": "t-upper", "model-x": "t-lower", "model-y": "t-y", "Model-Z": "t-z"},
			probe:   "model-x",
			want:    "t-lower",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, validateNoConflictingMappings(mappingOrderSavedConfig(tt.mapping)))
			for round := 0; round < mappingOrderRounds; round++ {
				cache := buildMappingOrderCache(tt.mapping)
				require.Equal(t, tt.want, mappingOrderLookup(cache, tt.probe), "round %d", round)
			}
		})
	}
}

func TestExpandMappingToCache_WildcardCaseCollisionLaterSrcWins(t *testing.T) {
	// 通配符小写后前缀相同时规则与精确名一致：字节序靠后的 src 覆盖靠前的，缓存里只留一条。
	// "GQ-*" 夹在 "GPT-*" 与 "Gpt-*" 之间，说明撞键的几个 src 不必相邻。
	mapping := map[string]string{
		"GPT-*": "t-upper",
		"Gpt-*": "t-mixed",
		"gpt-*": "t-lower",
		"GQ-*":  "t-gq",
	}
	require.Error(t, validateNoConflictingMappings(mappingOrderSavedConfig(mapping)))

	for round := 0; round < mappingOrderRounds; round++ {
		cache := buildMappingOrderCache(mapping)
		require.Equal(t, []string{"gpt-", "gq-"}, mappingOrderWildcardPrefixes(cache), "round %d", round)
		require.Equal(t, "t-lower", mappingOrderLookup(cache, "gpt-5"), "round %d", round)
		require.Equal(t, "t-lower", mappingOrderLookup(cache, "GPT-5"), "round %d", round)
		require.Equal(t, "t-gq", mappingOrderLookup(cache, "gq-1"), "round %d", round)
	}
}

func TestExpandMappingToCache_ExactBeatsWildcard(t *testing.T) {
	// 精确名存在独立的表里，先于所有通配符查找；即使有前缀更长的通配符也一样
	mapping := map[string]string{
		"claude-sonnet-4":  "t-exact",
		"claude-sonnet-4*": "t-long-wildcard",
		"claude-*":         "t-wildcard",
	}
	require.Error(t, validateNoConflictingMappings(mappingOrderSavedConfig(mapping)))

	probes := []mappingOrderProbe{
		{"claude-sonnet-4", "t-exact"},
		{"Claude-Sonnet-4", "t-exact"},
		{"claude-sonnet-4-5", "t-long-wildcard"},
		{"claude-opus-4", "t-wildcard"},
	}

	for round := 0; round < mappingOrderRounds; round++ {
		cache := buildMappingOrderCache(mapping)
		require.Equal(t, []string{"claude-sonnet-4", "claude-"}, mappingOrderWildcardPrefixes(cache), "round %d", round)
		for _, probe := range probes {
			require.Equal(t, probe.want, mappingOrderLookup(cache, probe.model), "round %d model %s", round, probe.model)
		}
	}
}

// expandMappingToCacheLegacy 是本次改动之前的实现（直接 range map 追加，不排序），
// 只用来证明：能通过保存校验的配置，新旧实现的查找结果相同。
func expandMappingToCacheLegacy(cache *channelCache, ch *Channel, gid int64, platform string) {
	for _, mappingPlatform := range matchingPlatforms(platform) {
		platformMapping, ok := ch.ModelMapping[mappingPlatform]
		if !ok {
			continue
		}
		gpKey := channelGroupPlatformKey{groupID: gid, platform: mappingPlatform}
		for src, dst := range platformMapping {
			if strings.HasSuffix(src, "*") {
				prefix := strings.ToLower(strings.TrimSuffix(src, "*"))
				cache.wildcardMappingByGP[gpKey] = append(cache.wildcardMappingByGP[gpKey], &wildcardMappingEntry{
					prefix: prefix,
					target: dst,
				})
			} else {
				key := channelModelKey{groupID: gid, platform: mappingPlatform, model: strings.ToLower(src)}
				cache.mappingByGroupModel[key] = dst
			}
		}
	}
}

func TestExpandMappingToCache_NonOverlappingMatchesLegacy(t *testing.T) {
	tests := []struct {
		name    string
		mapping map[string]string
		probes  []mappingOrderProbe
	}{
		{
			name:    "只有精确名",
			mapping: map[string]string{"claude-sonnet-4": "mapped-a", "GPT-5": "mapped-b", "o3": "mapped-c"},
			probes: []mappingOrderProbe{
				{"claude-sonnet-4", "mapped-a"},
				{"gpt-5", "mapped-b"},
				{"GPT-5", "mapped-b"},
				{"o3", "mapped-c"},
				{"claude-sonnet-4-5", ""},
				{"llama-3", ""},
			},
		},
		{
			name:    "互不重叠的通配符",
			mapping: map[string]string{"claude-*": "wild-a", "gpt-*": "wild-b", "gemini-*": "wild-c"},
			probes: []mappingOrderProbe{
				{"claude-opus-4", "wild-a"},
				{"gpt-5", "wild-b"},
				{"Gemini-2.5-Pro", "wild-c"},
				{"llama-3", ""},
			},
		},
		{
			name:    "通配符与精确名混合",
			mapping: map[string]string{"claude-*": "wild-a", "gpt-*": "wild-b", "o3": "exact-o3", "deepseek-v3": "exact-ds"},
			probes: []mappingOrderProbe{
				{"claude-haiku-4-5", "wild-a"},
				{"gpt-4o", "wild-b"},
				{"o3", "exact-o3"},
				{"o3-mini", ""},
				{"deepseek-v3", "exact-ds"},
				{"deepseek-r1", ""},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 这些配置都能通过保存校验，也就是生产里真正能存进去的形态
			require.NoError(t, validateNoConflictingMappings(mappingOrderSavedConfig(tt.mapping)))
			ch := &Channel{ModelMapping: mappingOrderSavedConfig(tt.mapping)}

			for round := 0; round < mappingOrderRounds; round++ {
				legacy := newEmptyChannelCache()
				expandMappingToCacheLegacy(legacy, ch, mappingOrderGroupID, mappingOrderPlatform)
				current := newEmptyChannelCache()
				expandMappingToCache(current, ch, mappingOrderGroupID, mappingOrderPlatform)

				require.Equal(t, legacy.mappingByGroupModel, current.mappingByGroupModel, "round %d", round)
				require.ElementsMatch(t, mappingOrderWildcardPairs(legacy), mappingOrderWildcardPairs(current), "round %d", round)
				for _, probe := range tt.probes {
					require.Equal(t, probe.want, mappingOrderLookup(legacy, probe.model), "round %d model %s (legacy)", round, probe.model)
					require.Equal(t, probe.want, mappingOrderLookup(current, probe.model), "round %d model %s", round, probe.model)
				}
			}
		})
	}
}

func TestPopulateChannelCache_MappingStableAcrossRebuilds(t *testing.T) {
	mapping := mappingOrderMixedConfig()
	require.Error(t, validateNoConflictingMappings(mappingOrderSavedConfig(mapping)))

	var firstFingerprint string
	for round := 0; round < mappingOrderRounds; round++ {
		cache := buildMappingOrderCache(mapping)
		require.Equal(t, mappingOrderMixedWildcardOrder, mappingOrderWildcardPrefixes(cache), "round %d", round)
		for _, probe := range mappingOrderMixedProbes {
			require.Equal(t, probe.want, mappingOrderLookup(cache, probe.model), "round %d model %s", round, probe.model)
		}

		fingerprint := mappingOrderFingerprint(cache, mappingOrderMixedProbes)
		if round == 0 {
			firstFingerprint = fingerprint
			continue
		}
		require.Equal(t, firstFingerprint, fingerprint, "round %d", round)
	}
}

func TestChannelService_ResolveChannelMapping_StableAcrossCacheInvalidation(t *testing.T) {
	// 走服务层的完整路径：渠道保存、pubsub 通知、TTL 到期都会触发 InvalidateCache / 重建，
	// 同一份配置每次重建后，两个分组的解析结果都必须不变。
	ch := Channel{
		ID:           1,
		Status:       StatusActive,
		GroupIDs:     []int64{mappingOrderGroupID, mappingOrderGroupID2},
		ModelMapping: mappingOrderSavedConfig(mappingOrderMixedConfig()),
	}
	repo := makeStandardRepo(ch, map[int64]string{
		mappingOrderGroupID:  mappingOrderPlatform,
		mappingOrderGroupID2: mappingOrderPlatform,
	})
	svc := newTestChannelService(repo)
	ctx := context.Background()

	for round := 0; round < mappingOrderRounds; round++ {
		svc.InvalidateCache()
		for _, gid := range []int64{mappingOrderGroupID, mappingOrderGroupID2} {
			for _, probe := range mappingOrderMixedProbes {
				result := svc.ResolveChannelMapping(ctx, gid, probe.model)
				require.True(t, result.Mapped, "round %d group %d model %s", round, gid, probe.model)
				require.Equal(t, probe.want, result.MappedModel, "round %d group %d model %s", round, gid, probe.model)
			}
		}
	}
}
