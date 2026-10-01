package middleware

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// Admin audit trail
// -----------------
//
// withAdminAudit wraps the admin authentication middleware. It writes one
// audit_logs row for every state-changing request (any method other than
// GET/HEAD/OPTIONS) that reached an identified administrator credential:
//
//   - requests that succeed, and requests that fail (4xx/5xx, including those
//     rejected by the auth middleware itself for an identified admin token:
//     wrong scope, missing X-Reason, IP not allowed, revoked, expired);
//   - requests whose handler panics (recorded with status 500 and
//     extra.panic = true, then the panic continues to the recovery middleware).
//
// Requests with no recognisable credential (missing, unknown or malformed) are
// not recorded: they identify nobody, and recording them would let anyone on
// the internet fill the table. They are counted instead, and logged (rate
// limited) with client IP, route and user agent, via AdminAuditUnidentifiedReporter.
//
// Request bodies are stored redacted (service.RedactAuditBody). Routes that
// submit credentials (Codex auth.json import, account credential edits, OAuth
// code exchange, proxy passwords, settings, ...) do not store a body at all,
// only "body_omitted" and its length: see admin_audit_body_policy.go.
//
// Auditing never changes the outcome of the request. The row is only handed to
// a bounded asynchronous queue (service.AdminAuditWriter), outside any database
// transaction; if the queue is full the row is dropped and counted, and any
// failure while building or queueing it is logged and otherwise ignored.

// AdminAuditSink receives audit rows. *service.AdminAuditWriter implements it.
type AdminAuditSink interface {
	// Enqueue must not block. It reports whether the row was accepted.
	Enqueue(entry *service.AuditLog) bool
}

// AdminAuditUnidentifiedReporter is optionally implemented by an
// AdminAuditSink. The middleware calls it for state-changing requests that no
// administrator credential could be attributed to, so the sink can count them
// and log them without writing a row. It must not block.
type AdminAuditUnidentifiedReporter interface {
	RecordUnidentified(clientIP, route, userAgent string)
}

// withAdminAudit wraps next (the admin auth middleware, which runs the rest of
// the handler chain from inside). A nil sink disables auditing.
func withAdminAudit(next gin.HandlerFunc, sink AdminAuditSink) gin.HandlerFunc {
	if sink == nil {
		return next
	}
	return func(c *gin.Context) {
		if c.Request == nil || isAdminReadOnlyMethod(c.Request.Method) {
			next(c)
			return
		}

		start := time.Now()
		// Credential-submitting routes never have their body captured, so
		// the secrets are not even held in the audit buffer.
		var capture *auditBodyCapture
		if !adminAuditBodyOmitted(c.Request.Method, c.FullPath()) {
			capture = newAuditBodyCapture(c.Request.Body)
			if capture != nil {
				c.Request.Body = capture
			}
		}

		finished := false
		defer func() {
			if finished {
				return
			}
			// The handler chain is panicking (or exiting the goroutine). Record
			// it and let the panic carry on to the recovery middleware. The
			// helper only recovers a panic raised while recording, never the
			// handler's own panic (recover() in a function that merely runs
			// during unwinding does not catch the panic being unwound).
			safeEnqueueAdminAudit(c, sink, start, capture, true)
		}()

		next(c)
		finished = true

		safeEnqueueAdminAudit(c, sink, start, capture, false)
	}
}

// safeEnqueueAdminAudit records the request and swallows (but logs) any panic
// raised by the recording itself, so a bug in auditing can neither fail the
// request nor replace the original panic of a crashing handler.
func safeEnqueueAdminAudit(c *gin.Context, sink AdminAuditSink, start time.Time, capture *auditBodyCapture, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("admin audit: failed to record request", "panic", r)
		}
	}()
	enqueueAdminAudit(c, sink, start, capture, panicked)
}

func enqueueAdminAudit(c *gin.Context, sink AdminAuditSink, start time.Time, capture *auditBodyCapture, panicked bool) {
	entry := buildAdminAuditEntry(c, start, capture, panicked)
	if entry == nil {
		// Nobody was identified: no row, but make the attempt visible.
		if reporter, ok := sink.(AdminAuditUnidentifiedReporter); ok {
			reporter.RecordUnidentified(SecurityClientIP(c), c.FullPath(), c.Request.UserAgent())
		}
		return
	}
	// A dropped row is counted (and logged, rate limited) by the sink.
	_ = sink.Enqueue(entry)
}

// buildAdminAuditEntry assembles the row, or returns nil when the caller was
// never identified. It must not panic when called while a panic is in flight,
// so it only does nil-safe work.
func buildAdminAuditEntry(c *gin.Context, start time.Time, capture *auditBodyCapture, panicked bool) *service.AuditLog {
	kind := AdminAuthKindFromContext(c)
	if kind == "" {
		return nil
	}

	status := c.Writer.Status()
	if panicked {
		status = http.StatusInternalServerError
	}

	route := c.FullPath()
	urlPath := ""
	rawQuery := ""
	if c.Request.URL != nil {
		urlPath = c.Request.URL.Path
		rawQuery = c.Request.URL.RawQuery
	}
	if route == "" {
		route = urlPath
	}
	path := urlPath
	if query := service.RedactAuditQuery(rawQuery); query != "" {
		path += "?" + query
	}
	targetType, targetID := adminAuditTarget(route, c.Params)
	if override, ok := c.Get(string(ContextKeyAdminAuditTarget)); ok {
		if target, ok := override.(AdminAuditTarget); ok {
			targetType, targetID = target.Type, target.ID
		}
	}

	authMethod := c.GetString("auth_method")
	if authMethod == "" {
		// An identified admin token that was rejected before it authenticated.
		authMethod = kind
	}

	entry := &service.AuditLog{
		CreatedAt:        start.UTC(),
		ActorEmail:       c.GetString(string(ContextKeyAdminActorEmail)),
		ActorRole:        c.GetString(string(ContextKeyUserRole)),
		AuthMethod:       authMethod,
		CredentialMasked: c.GetString(string(ContextKeyAdminCredentialMasked)),
		Action:           adminAuditAction(c.Request.Method, route),
		Method:           c.Request.Method,
		Path:             path,
		RequestID:        adminAuditRequestID(c),
		ClientIP:         SecurityClientIP(c),
		UserAgent:        c.Request.UserAgent(),
		StatusCode:       status,
		LatencyMs:        time.Since(start).Milliseconds(),
		ActorLabel:       c.GetString(string(ContextKeyAdminActorLabel)),
		AuthKind:         kind,
		Route:            route,
		TargetType:       targetType,
		TargetID:         targetID,
	}
	if userID, ok := c.Get(string(ContextKeyAdminActorUserID)); ok {
		if id, ok := userID.(int64); ok && id > 0 {
			entry.ActorUserID = &id
		}
	}
	if tokenID, ok := AdminTokenIDFromContext(c); ok && tokenID > 0 {
		entry.TokenID = &tokenID
	}

	extra := map[string]any{}
	if handlerExtra, ok := c.Get(string(ContextKeyAdminAuditExtra)); ok {
		if values, ok := handlerExtra.(map[string]any); ok {
			for key, value := range values {
				extra[key] = value
			}
		}
	}
	if code := c.GetString(string(ContextKeyAdminRejectCode)); code != "" {
		extra["reject_code"] = code
	}
	if panicked {
		extra["panic"] = true
	}

	entry.Reason = c.GetString(string(ContextKeyAdminReason))
	if entry.Reason == "" {
		reason, err := ParseAdminReason(c.Request.Header)
		if err != nil {
			extra["reason_error"] = err.Error()
		}
		entry.Reason = reason
	}

	if adminAuditBodyOmitted(c.Request.Method, c.FullPath()) {
		// The route submits credentials: the body is deliberately not stored.
		extra["body_omitted"] = true
		if c.Request.ContentLength > 0 {
			extra["body_bytes"] = c.Request.ContentLength
		}
	} else if capture != nil {
		body, total := capture.snapshot()
		entry.RequestBody = service.RedactAuditBody(body, c.GetHeader("Content-Type"))
		if total > 0 {
			extra["body_bytes"] = total
		}
	}
	if len(extra) > 0 {
		entry.Extra = extra
	}
	return entry
}

func adminAuditRequestID(c *gin.Context) string {
	if id, ok := c.Request.Context().Value(ctxkey.RequestID).(string); ok && id != "" {
		return id
	}
	return c.Writer.Header().Get(requestIDHeader)
}

// adminAuditAction is a compact "METHOD /route" without the API prefix, e.g.
// "POST /users/:id/balance".
func adminAuditAction(method, route string) string {
	return method + " " + strings.TrimPrefix(route, adminAPIPrefix)
}

// adminAuditTarget derives the object a request addressed from its route
// template: the collection in front of the first path parameter and that
// parameter's value (/users/:id/balance -> users, 42). Routes without a
// parameter report their first collection (/settings/smtp/test -> settings).
func adminAuditTarget(route string, params gin.Params) (targetType, targetID string) {
	relative := strings.Trim(strings.TrimPrefix(route, adminAPIPrefix), "/")
	if relative == "" {
		return "", ""
	}
	segments := strings.Split(relative, "/")
	// "payment" is a namespace, not an object type.
	if len(segments) > 1 && segments[0] == "payment" {
		segments = segments[1:]
	}
	for index, segment := range segments {
		if !strings.HasPrefix(segment, ":") && !strings.HasPrefix(segment, "*") {
			continue
		}
		targetID = strings.Trim(params.ByName(segment[1:]), "/")
		if index > 0 {
			targetType = segments[index-1]
		}
		return targetType, targetID
	}
	return segments[0], ""
}

// auditBodyCapture records the request body as the handler reads it, up to
// AuditRequestBodyCaptureLimit+1 bytes (one more than may be stored, so
// "too large" can be told apart from "exactly at the limit"). It adds no
// buffering of its own: bytes are copied only as they flow to the handler.
type auditBodyCapture struct {
	rc io.ReadCloser

	mu    sync.Mutex
	buf   bytes.Buffer
	total int64
}

func newAuditBodyCapture(body io.ReadCloser) *auditBodyCapture {
	if body == nil || body == http.NoBody {
		return nil
	}
	return &auditBodyCapture{rc: body}
}

func (a *auditBodyCapture) Read(p []byte) (int, error) {
	n, err := a.rc.Read(p)
	if n > 0 {
		a.mu.Lock()
		a.total += int64(n)
		if room := service.AuditRequestBodyCaptureLimit + 1 - a.buf.Len(); room > 0 {
			if n < room {
				room = n
			}
			_, _ = a.buf.Write(p[:room])
		}
		a.mu.Unlock()
	}
	return n, err
}

func (a *auditBodyCapture) Close() error { return a.rc.Close() }

// snapshot returns a copy of the captured prefix and the number of body bytes
// the handler consumed.
func (a *auditBodyCapture) snapshot() ([]byte, int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]byte(nil), a.buf.Bytes()...), a.total
}

// ContextKeyAdminAuditExtra holds extra key/values a handler wants recorded in
// the audit row's "extra" column (see SetAdminAuditExtra).
const ContextKeyAdminAuditExtra ContextKey = "admin_audit_extra"

// ContextKeyAdminAuditTarget holds a target override set by a handler (see
// SetAdminAuditTarget).
const ContextKeyAdminAuditTarget ContextKey = "admin_audit_target"

// AdminAuditTarget is the object a request acted on, as recorded in
// audit_logs.target_type / target_id.
type AdminAuditTarget struct {
	Type string
	ID   string
}

// SetAdminAuditExtra lets a handler attach a value to the audit row of the
// current request, for facts that are not visible in the route or the
// (redacted) body: for example the id of an object the request created. Never
// pass secrets. It is a no-op outside the admin audit middleware.
func SetAdminAuditExtra(c *gin.Context, key string, value any) {
	if c == nil || key == "" {
		return
	}
	values, _ := c.Get(string(ContextKeyAdminAuditExtra))
	extra, ok := values.(map[string]any)
	if !ok {
		extra = map[string]any{}
		c.Set(string(ContextKeyAdminAuditExtra), extra)
	}
	extra[key] = value
}

// SetAdminAuditTarget overrides the target_type/target_id derived from the
// route, for requests that create the object they address (POST /admin-tokens
// has no :id in the route).
func SetAdminAuditTarget(c *gin.Context, targetType, targetID string) {
	if c == nil {
		return
	}
	c.Set(string(ContextKeyAdminAuditTarget), AdminAuditTarget{Type: targetType, ID: targetID})
}
