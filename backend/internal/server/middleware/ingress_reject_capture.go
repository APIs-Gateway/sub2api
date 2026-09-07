package middleware

import (
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// This file adds structured, sanitized telemetry capture for ingress
// rejections on top of the existing IngressRejectReason contract in
// ingress_reject.go. It intentionally only captures/normalizes data into a
// bounded in-memory buffer; it does not add any admin/query API or
// persistence (that is deferred to a later, separately reviewed Ops PR).
//
// Hard constraints honored here:
//   - never captures the raw API key, Authorization header, or request body;
//   - the client IP reuses the existing trusted-proxy resolution
//     (SecurityClientIP) and the existing IPv6 /64 masking
//     (normalizeIngressRejectIP) already used by the invalid-auth abuse key;
//   - the User-Agent is stripped of control characters and truncated to a
//     bounded length before capture;
//   - the route family is derived only from the request path, never the raw
//     path plus query string, so credentials passed as a query parameter
//     cannot leak into telemetry.

const (
	// ingressRejectCaptureCapacity bounds the process-wide ring buffer so a
	// sustained flood of rejected requests cannot grow memory unbounded.
	ingressRejectCaptureCapacity = 256

	// ingressRejectCaptureUserAgentMaxBytes bounds the sanitized User-Agent
	// captured per event.
	ingressRejectCaptureUserAgentMaxBytes = 256
)

// IngressRejectEvent is the structured, sanitized record captured for each
// ingress rejection. Every field is bounded and safe to log, persist, or
// expose in aggregate form in a future Ops PR.
type IngressRejectEvent struct {
	CapturedAt  time.Time
	Reason      IngressRejectReason
	RouteFamily string
	Protocol    string
	ClientIP    string
	UserAgent   string
}

// ingressRejectCaptureBuffer is a fixed-capacity, mutex-protected ring
// buffer. Once full, the oldest captured event is overwritten so memory
// usage stays constant regardless of request volume.
type ingressRejectCaptureBuffer struct {
	mu       sync.Mutex
	items    []IngressRejectEvent
	capacity int
	next     int
	count    int
	dropped  uint64
}

func newIngressRejectCaptureBuffer(capacity int) *ingressRejectCaptureBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &ingressRejectCaptureBuffer{
		items:    make([]IngressRejectEvent, capacity),
		capacity: capacity,
	}
}

// add inserts an event, evicting the oldest entry once the buffer is full.
func (b *ingressRejectCaptureBuffer) add(event IngressRejectEvent) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.count >= b.capacity {
		b.dropped++
	} else {
		b.count++
	}
	b.items[b.next] = event
	b.next = (b.next + 1) % b.capacity
}

// Snapshot returns a copy of the currently captured events, ordered oldest
// to newest. The returned slice is safe to retain and mutate.
func (b *ingressRejectCaptureBuffer) Snapshot() []IngressRejectEvent {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]IngressRejectEvent, b.count)
	start := (b.next - b.count + b.capacity) % b.capacity
	for i := 0; i < b.count; i++ {
		out[i] = b.items[(start+i)%b.capacity]
	}
	return out
}

// Dropped returns how many captured events have been evicted because the
// bounded buffer was full when they arrived.
func (b *ingressRejectCaptureBuffer) Dropped() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

// Len returns the number of events currently held in the buffer.
func (b *ingressRejectCaptureBuffer) Len() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

var globalIngressRejectCapture = newIngressRejectCaptureBuffer(ingressRejectCaptureCapacity)

// IngressRejectCaptureSnapshot exposes the bounded in-memory ingress-reject
// telemetry captured so far. It is intended as the data source for a later,
// separately reviewed Ops aggregation PR (Phase C of #548) and is
// deliberately not wired to any HTTP endpoint in this change.
func IngressRejectCaptureSnapshot() []IngressRejectEvent {
	return globalIngressRejectCapture.Snapshot()
}

// IngressRejectCaptureDropped returns how many captured events have been
// evicted from the bounded buffer because it was full.
func IngressRejectCaptureDropped() uint64 {
	return globalIngressRejectCapture.Dropped()
}

// setIngressRejectCaptureBufferForTest swaps the process-wide capture buffer
// for the duration of a test and returns a function that restores it. Tests
// must not run in parallel with each other while the buffer is swapped.
func setIngressRejectCaptureBufferForTest(buf *ingressRejectCaptureBuffer) func() {
	previous := globalIngressRejectCapture
	globalIngressRejectCapture = buf
	return func() { globalIngressRejectCapture = previous }
}

// captureIngressReject builds a sanitized telemetry event for a marked
// rejection and appends it to the bounded capture buffer.
func captureIngressReject(c *gin.Context, reason IngressRejectReason) {
	event, ok := buildIngressRejectEvent(c, reason)
	if !ok {
		return
	}
	globalIngressRejectCapture.add(event)
}

// buildIngressRejectEvent derives the structured, sanitized event fields
// from the request. It never reads the request body and never touches the
// Authorization header.
func buildIngressRejectEvent(c *gin.Context, reason IngressRejectReason) (IngressRejectEvent, bool) {
	if c == nil || c.Request == nil || c.Request.URL == nil || reason == "" {
		return IngressRejectEvent{}, false
	}
	protocol := "http"
	if c.IsWebsocket() {
		protocol = "ws"
	}
	return IngressRejectEvent{
		CapturedAt:  time.Now().UTC(),
		Reason:      reason,
		RouteFamily: ingressRejectRouteFamily(c.Request.URL.Path),
		Protocol:    protocol,
		ClientIP:    normalizeIngressRejectIP(SecurityClientIP(c)),
		UserAgent:   sanitizeIngressRejectUserAgent(c.Request.UserAgent()),
	}, true
}

// ingressRejectRouteFamily classifies a request path into a coarse family
// using only the path component (never the raw query string), so credentials
// or other sensitive values passed as query parameters can never leak into
// telemetry through this field.
func ingressRejectRouteFamily(path string) string {
	path = strings.ToLower(strings.TrimSpace(path))
	switch {
	case path == "":
		return "other"
	case strings.HasPrefix(path, "/antigravity/v1beta"):
		return "antigravity_gemini"
	case strings.HasPrefix(path, "/antigravity/models"):
		return "antigravity_models"
	case strings.HasPrefix(path, "/antigravity"):
		return "antigravity"
	case strings.HasPrefix(path, "/v1beta"):
		return "gemini"
	case strings.HasPrefix(path, "/backend-api/codex/wham/usage"):
		return "usage"
	case strings.HasPrefix(path, "/backend-api/codex"):
		return "codex"
	case strings.HasPrefix(path, "/backend-api/wham/usage"):
		return "usage"
	case strings.Contains(path, "/sub2api/billing"):
		return "billing"
	case strings.Contains(path, "/messages"):
		return "messages"
	case strings.Contains(path, "/responses"):
		return "responses"
	case strings.Contains(path, "/chat/completions"):
		return "chat_completions"
	case strings.Contains(path, "/alpha/search"):
		return "alpha_search"
	case strings.Contains(path, "/images"):
		return "images"
	case strings.Contains(path, "/embeddings"):
		return "embeddings"
	case strings.Contains(path, "/usage"):
		return "usage"
	case strings.Contains(path, "/models"):
		return "models"
	default:
		return "other"
	}
}

// sanitizeIngressRejectUserAgent strips control characters (which could
// otherwise be used for log/line injection) and invalid UTF-8, then
// truncates to a bounded byte length while preserving UTF-8 validity.
func sanitizeIngressRejectUserAgent(raw string) string {
	raw = strings.ToValidUTF8(strings.TrimSpace(raw), "")
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if r == utf8.RuneError || unicode.IsControl(r) {
			continue
		}
		_, _ = b.WriteRune(r)
	}
	value := strings.TrimSpace(b.String())
	if len(value) <= ingressRejectCaptureUserAgentMaxBytes {
		return value
	}
	value = value[:ingressRejectCaptureUserAgentMaxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
