package service

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeAnthropicCacheTTLOrder preserves a client's 1h cache boundary after
// mimicry adds default/5m boundaries earlier in Anthropic's processing order.
// Invalid types and TTL values remain untouched for upstream validation.
func normalizeAnthropicCacheTTLOrder(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	_, messages, tools, system := collectCacheControlPaths(body)
	paths := append(tools, system...)
	paths = append(paths, messages...)
	if gjson.GetBytes(body, "cache_control").Exists() {
		paths = append(paths, "cache_control")
	}
	lastLong := -1
	for i, path := range paths {
		marker := gjson.GetBytes(body, path)
		ttl := marker.Get("ttl")
		if marker.IsObject() && marker.Get("type").Type == gjson.String && marker.Get("type").String() == "ephemeral" && ttl.Type == gjson.String && ttl.String() == cacheTTLTarget1h {
			lastLong = i
		}
	}
	out := body
	for i := 0; i < lastLong; i++ {
		marker := gjson.GetBytes(body, paths[i])
		ttl := marker.Get("ttl")
		if !marker.IsObject() || marker.Get("type").Type != gjson.String || marker.Get("type").String() != "ephemeral" {
			continue
		}
		if ttl.Exists() && (ttl.Type != gjson.String || ttl.String() != cacheTTLTarget5m) {
			continue
		}
		next, err := sjson.SetBytes(out, paths[i]+".ttl", cacheTTLTarget1h)
		if err != nil {
			return body
		}
		out = next
	}
	return out
}
