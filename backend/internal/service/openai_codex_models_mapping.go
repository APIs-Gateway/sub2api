package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tidwall/sjson"
)

// Only mapped, non-passthrough accounts need a different representation from
// their upstream catalog. Fetch it unconditionally before applying local ETags:
// an upstream 304 cannot prove a changed account mapping is still represented.
func codexModelsNeedAccountProjection(account *Account) bool {
	return !account.IsOpenAIPassthroughEnabled() && len(account.GetModelMapping()) > 0
}

func projectCodexModelsForAccount(body []byte, account *Account) ([]byte, error) {
	if !codexModelsNeedAccountProjection(account) {
		return body, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(envelope["models"], &entries); err != nil {
		return nil, err
	}
	bySlug := make(map[string]json.RawMessage, len(entries))
	candidates := make([]string, 0, len(entries))
	for _, raw := range entries {
		var entry map[string]json.RawMessage
		var slug string
		if json.Unmarshal(raw, &entry) != nil || json.Unmarshal(entry["slug"], &slug) != nil {
			continue
		}
		slug = strings.TrimSpace(slug)
		if slug == "" || strings.Contains(slug, "*") {
			continue
		}
		if _, exists := bySlug[slug]; !exists {
			bySlug[slug] = raw
			candidates = append(candidates, slug)
		}
	}
	aliases := make([]string, 0, len(account.GetModelMapping()))
	for alias := range account.GetModelMapping() {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	candidates = append(candidates, aliases...)
	projected := make([]json.RawMessage, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || strings.Contains(candidate, "*") || !account.IsModelSupported(candidate) {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		target, _ := account.ResolveMappedModel(candidate)
		target = strings.TrimSpace(target)
		raw, available := bySlug[target]
		if target == "" || strings.Contains(target, "*") || !available {
			continue
		}
		seen[candidate] = struct{}{}
		if candidate == target {
			projected = append(projected, raw)
			continue
		}
		encoded, err := sjson.SetBytes(raw, "slug", candidate)
		if err != nil {
			return nil, err
		}
		encoded, err = sjson.SetBytes(encoded, "display_name", candidate)
		if err != nil {
			return nil, err
		}
		projected = append(projected, encoded)
	}
	unchanged := len(projected) == len(entries)
	for i := range projected {
		if !unchanged || !bytes.Equal(projected[i], entries[i]) {
			unchanged = false
			break
		}
	}
	if unchanged {
		return body, nil
	}
	// Splice only the known model names/array. Opaque client metadata stays
	// byte-for-byte intact, including large numbers and unknown string escapes.
	var encoded bytes.Buffer
	encoded.WriteByte('[')
	for i, raw := range projected {
		if i > 0 {
			encoded.WriteByte(',')
		}
		encoded.Write(raw)
	}
	encoded.WriteByte(']')
	return sjson.SetRawBytes(body, "models", encoded.Bytes())
}

func codexModelsRepresentationETag(body []byte) string {
	return fmt.Sprintf(`W/"sub2api-codex-%x"`, sha256.Sum256(body))
}

// If-None-Match uses weak comparison for GET, including lists and '*'.
func codexModelsETagMatches(header, etag string) bool {
	if strings.TrimSpace(header) == "*" {
		return true
	}
	if header == "" || etag == "" {
		return false
	}
	value := strings.TrimPrefix(etag, "W/")
	for header = strings.TrimSpace(header); header != ""; header = strings.TrimSpace(header) {
		if header == "*" {
			return true
		}
		candidate := strings.TrimPrefix(header, "W/")
		if !strings.HasPrefix(candidate, `"`) {
			return false
		}
		end := strings.IndexByte(candidate[1:], '"')
		if end < 0 {
			return false
		}
		end += 2
		rest := strings.TrimSpace(candidate[end:])
		if rest != "" && rest[0] != ',' {
			return false
		}
		if candidate[:end] == value {
			return true
		}
		if rest == "" {
			return false
		}
		header = rest[1:]
	}
	return false
}
