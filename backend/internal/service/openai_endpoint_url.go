package service

import (
	"net/url"
	"strings"
)

func buildOpenAIEndpointURL(base string, endpoint string) string {
	normalized := strings.TrimSpace(base)
	endpoint = "/" + strings.TrimLeft(strings.TrimSpace(endpoint), "/")
	relative := strings.TrimPrefix(endpoint, "/v1")

	// Base URLs that carry a query string or fragment (e.g. a probed
	// Sub2API upstream exposing "?redirect=/" or "#anchor") must only be
	// rewritten on the path component; naive suffix/concat used to splice
	// the endpoint after the query string and corrupt the URL. This is done
	// with raw substring slicing rather than url.Parse+String() round-trip
	// because callers (e.g. async image-poll task ids) may pass endpoint
	// segments that are already percent-encoded; re-serializing through
	// net/url would double-escape them.
	path, query := splitOpenAIBaseURLPathAndQuery(normalized)
	path = strings.TrimRight(path, "/")
	if !strings.HasSuffix(path, endpoint) && !strings.HasSuffix(path, relative) {
		if openAIBaseURLHasVersionSuffix(path) {
			path += relative
		} else {
			path += endpoint
		}
	}
	return path + query
}

// splitOpenAIBaseURLPathAndQuery splits a base URL into its path portion and
// its "?query" suffix, dropping any "#fragment" entirely. It operates on raw
// bytes only (no decode/re-encode) so percent-encoded path segments are left
// untouched.
func splitOpenAIBaseURLPathAndQuery(base string) (path string, query string) {
	if frag := strings.IndexByte(base, '#'); frag >= 0 {
		base = base[:frag]
	}
	if q := strings.IndexByte(base, '?'); q >= 0 {
		return base[:q], base[q:]
	}
	return base, ""
}

func openAIBaseURLHasVersionSuffix(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}

	pathValue := ""
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		pathValue = parsed.Path
	} else if slash := strings.Index(trimmed, "/"); slash >= 0 {
		pathValue = trimmed[slash:]
	}

	pathValue = strings.TrimRight(pathValue, "/")
	if pathValue == "" {
		return false
	}
	lastSlash := strings.LastIndex(pathValue, "/")
	segment := pathValue
	if lastSlash >= 0 {
		segment = pathValue[lastSlash+1:]
	}
	return isOpenAIAPIVersionSegment(segment)
}

func isOpenAIAPIVersionSegment(segment string) bool {
	s := strings.ToLower(strings.TrimSpace(segment))
	if len(s) < 2 || s[0] != 'v' || !isASCIIDigit(s[1]) {
		return false
	}

	i := 1
	for i < len(s) && isASCIIDigit(s[i]) {
		i++
	}
	if i == len(s) {
		return true
	}
	if s[i] == '.' {
		i++
		if i == len(s) || !isASCIIDigit(s[i]) {
			return false
		}
		for i < len(s) && isASCIIDigit(s[i]) {
			i++
		}
		return i == len(s)
	}

	suffix := s[i:]
	return strings.HasPrefix(suffix, "alpha") ||
		strings.HasPrefix(suffix, "beta") ||
		strings.HasPrefix(suffix, "preview")
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
