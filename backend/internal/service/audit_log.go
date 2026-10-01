package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const (
	// AuditAuthMethodJWT, AuditAuthMethodAdminAPIKey and AuditAuthMethodAdminToken
	// are the values the admin authentication middleware stores in the gin
	// context under "auth_method" (the legacy global admin API key keeps its
	// historical "admin_api_key" value so existing checks behave as before).
	AuditAuthMethodJWT         = "jwt"
	AuditAuthMethodAdminAPIKey = "admin_api_key"
	AuditAuthMethodAdminToken  = "admin_token"

	// AuditAuthKind* classify how an admin request authenticated. They are the
	// values persisted in audit_logs.auth_kind and accepted by the audit-log
	// filter of the same name.
	AuditAuthKindJWT          = "jwt"
	AuditAuthKindAdminToken   = "admin_token"
	AuditAuthKindLegacyAPIKey = "legacy_api_key"

	// auditRequestBodyMaxBytes caps the redacted request body stored per row.
	auditRequestBodyMaxBytes     = 8 * 1024
	auditRedactMaxDepth          = 24
	auditCredentialPrefixBytes   = 6
	auditCredentialSuffixBytes   = 4
	auditNonJSONContentTypeLimit = 128

	// AuditRequestBodyCaptureLimit is the maximum request prefix that may be
	// parsed for audit storage.
	AuditRequestBodyCaptureLimit = 256 * 1024

	// AuditLogDefaultPageSize is the default number of records returned by a list query.
	AuditLogDefaultPageSize = 50
	// AuditLogMaxPageSize bounds a single list query.
	AuditLogMaxPageSize = 200
	// AuditLogDefaultDeleteBatchSize bounds a normal retention batch.
	AuditLogDefaultDeleteBatchSize = 5000
	// AuditLogMaxDeleteBatchSize prevents an accidental unbounded cleanup query.
	AuditLogMaxDeleteBatchSize = 10000
)

// AuditLog is an append-only record of a sensitive management or security
// event. RequestBody and CredentialMasked must contain only sanitized data.
type AuditLog struct {
	ID               int64          `json:"id"`
	CreatedAt        time.Time      `json:"created_at"`
	ActorUserID      *int64         `json:"actor_user_id,omitempty"`
	ActorEmail       string         `json:"actor_email"`
	ActorRole        string         `json:"actor_role"`
	AuthMethod       string         `json:"auth_method"`
	CredentialMasked string         `json:"credential_masked"`
	Action           string         `json:"action"`
	Method           string         `json:"method"`
	Path             string         `json:"path"`
	RequestID        string         `json:"request_id"`
	ClientIP         string         `json:"client_ip"`
	UserAgent        string         `json:"user_agent"`
	RequestBody      string         `json:"request_body,omitempty"`
	StatusCode       int            `json:"status_code"`
	LatencyMs        int64          `json:"latency_ms"`
	Extra            map[string]any `json:"extra,omitempty"`

	// Admin audit fields (audit_logs migration 194). For admin requests
	// LatencyMs is the request duration.

	// ActorLabel says who acted: "jwt:<email>", "token:<name>#<id>" or
	// "legacy_api_key".
	ActorLabel string `json:"actor_label"`
	// AuthKind is AuditAuthKindJWT, AuditAuthKindAdminToken or
	// AuditAuthKindLegacyAPIKey.
	AuthKind string `json:"auth_kind"`
	// TokenID is the admin token that made the request, if any.
	TokenID *int64 `json:"token_id,omitempty"`
	// Route is the gin route template, e.g. /api/v1/admin/users/:id/balance.
	Route string `json:"route"`
	// TargetType and TargetID identify the object addressed by the route.
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	// Reason is the caller supplied X-Reason.
	Reason string `json:"reason"`
	// Before and After are reserved for state snapshots and are currently left empty.
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
}

// AuditLogFilter contains bounded, parameterized list filters.
type AuditLogFilter struct {
	Page     int
	PageSize int

	StartTime   *time.Time
	EndTime     *time.Time
	ActorUserID *int64
	ActorEmail  string
	AuthMethod  string
	Action      string
	Method      string
	ClientIP    string
	// Success nil means all; true means status < 400; false means status >= 400.
	Success *bool
	// Query matches path, action, or actor email.
	Query string

	// AuthKind is AuditAuthKindJWT, AuditAuthKindAdminToken or AuditAuthKindLegacyAPIKey.
	AuthKind string
	// TokenID matches rows written for one admin token.
	TokenID *int64
	// RoutePrefix matches the beginning of the route template.
	RoutePrefix string
	TargetType  string
	TargetID    string
	// StatusMin and StatusMax bound status_code (inclusive).
	StatusMin *int
	StatusMax *int
}

type AuditLogList struct {
	Logs     []*AuditLog `json:"logs"`
	Total    int         `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

// AuditLogRepository deliberately has no single-record delete operation.
type AuditLogRepository interface {
	Insert(ctx context.Context, entry *AuditLog) error
	BatchInsert(ctx context.Context, entries []*AuditLog) (int64, error)
	List(ctx context.Context, filter *AuditLogFilter) (*AuditLogList, error)
	DeleteBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error)
}

func normalizeAuditBodyKey(key string) string {
	var builder strings.Builder
	builder.Grow(len(key))
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		switch r {
		case '_', '-', '.', ' ':
			continue
		default:
			_, _ = builder.WriteRune(r)
		}
	}
	return builder.String()
}

// auditSensitiveBodyExactKeys are matched against the whole normalized key.
// They are short words that would over-match as substrings ("code" is in
// "barcode", "pin" in "spinner") but are credentials when they stand alone.
var auditSensitiveBodyExactKeys = func() map[string]struct{} {
	builtin := []string{
		"code",
		"codes",
		"cvv",
		"pin",
	}
	sensitive := make(map[string]struct{}, len(builtin)+len(SensitiveCredentialKeys)+16)
	for _, key := range builtin {
		sensitive[normalizeAuditBodyKey(key)] = struct{}{}
	}
	for _, key := range SensitiveCredentialKeys {
		sensitive[normalizeAuditBodyKey(key)] = struct{}{}
	}
	for _, fields := range providerSensitiveConfigFields {
		for key := range fields {
			sensitive[normalizeAuditBodyKey(key)] = struct{}{}
		}
	}
	return sensitive
}()

// auditSensitiveKeySubstrings are matched against the normalized key (lower
// case, with "_", "-", "." and spaces removed), so "API-Key", "apiKey" and
// "api_key" are all caught by "key" / "apikey". The list is deliberately
// broad: over-redacting a harmless field costs a little detail in the audit
// trail, under-redacting leaks a credential into it.
var auditSensitiveKeySubstrings = []string{
	"password",
	"passwd",
	"secret",
	"token",
	"apikey",
	"key",
	"credential",
	"cookie",
	"authorization",
	"private",
	"serviceaccount",
	"totp",
	"otp",
}

// auditRedactedPlaceholder replaces the value of a sensitive key.
const auditRedactedPlaceholder = "[REDACTED]"

func isAuditSensitiveKey(key string) bool {
	normalized := normalizeAuditBodyKey(key)
	if _, ok := auditSensitiveBodyExactKeys[normalized]; ok {
		return true
	}
	for _, substring := range auditSensitiveKeySubstrings {
		if strings.Contains(normalized, substring) {
			return true
		}
	}
	return false
}

// RedactAuditBody removes secrets before a request body is eligible for audit
// storage and caps the result at 8KB.
//
//   - JSON bodies (by content type, or sniffed when the content type is
//     missing or generic, as with "curl -d") are parsed and every value whose
//     key looks sensitive (password, secret, token, key, credential, cookie,
//     authorization, private, ...) is replaced by "[REDACTED]", recursively
//     through objects and arrays. A sensitive key hides its whole value, so
//     account "credentials" objects never appear in the audit trail.
//   - Anything else (multipart, form, binary) is not parsed: only its size
//     and content type are recorded.
//
// Numbers are preserved verbatim, not round-tripped through float64.
func RedactAuditBody(raw []byte, contentType string) string {
	if len(raw) == 0 {
		return ""
	}
	if len(raw) > AuditRequestBodyCaptureLimit {
		return "<body omitted: exceeds " + strconv.Itoa(AuditRequestBodyCaptureLimit) + " bytes>"
	}

	if !auditBodyIsJSON(raw, contentType) {
		return auditNonJSONBodyMarker(len(raw), contentType)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "<unparsable body omitted>"
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(redactAuditValue(value, 0)); err != nil {
		return "<redacted body omitted>"
	}
	return truncateAuditString(strings.TrimRight(out.String(), "\n"), auditRequestBodyMaxBytes)
}

// auditBodyIsJSON reports whether raw should be treated as JSON: the content
// type says so, or it is generic/missing and the body is a valid JSON object
// or array.
func auditBodyIsJSON(raw []byte, contentType string) bool {
	if !json.Valid(raw) {
		return false
	}
	if strings.Contains(strings.ToLower(contentType), "json") {
		return true
	}
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

func auditNonJSONBodyMarker(size int, contentType string) string {
	contentType = truncateAuditString(strings.TrimSpace(contentType), auditNonJSONContentTypeLimit)
	return "<non-json body omitted: " + strconv.Itoa(size) + " bytes, content-type=" + contentType + ">"
}

func redactAuditValue(value any, depth int) any {
	if depth > auditRedactMaxDepth {
		return "<depth limit exceeded>"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if isAuditSensitiveKey(key) {
				out[key] = auditRedactedPlaceholder
				continue
			}
			out[key] = redactAuditValue(item, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = redactAuditValue(item, depth+1)
		}
		return out
	default:
		return value
	}
}

// MaskAuditCredential preserves only a small prefix/suffix for correlation.
func MaskAuditCredential(credential string) string {
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return ""
	}
	if len(credential) <= auditCredentialPrefixBytes+auditCredentialSuffixBytes+4 {
		return "****"
	}
	return credential[:auditCredentialPrefixBytes] + "****" + credential[len(credential)-auditCredentialSuffixBytes:]
}

// RedactAuditQuery parses query parameters so sensitive values are replaced
// structurally; malformed queries fall back to the existing text redactor.
func RedactAuditQuery(rawQuery string) string {
	rawQuery = strings.TrimSpace(rawQuery)
	if rawQuery == "" {
		return ""
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return logredact.RedactText(rawQuery,
			"api_key", "apikey", "api-v3-key", "token", "secret", "key",
			"cookie", "authorization", "private_key", "privatekey",
			"proxy_key", "custom_key",
		)
	}
	for key, items := range values {
		if !isAuditSensitiveKey(key) {
			continue
		}
		for index := range items {
			items[index] = auditRedactedPlaceholder
		}
		values[key] = items
	}
	return values.Encode()
}

func truncateAuditString(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	const marker = "...<truncated>"
	if maxBytes <= len(marker) {
		return marker[:maxBytes]
	}
	prefix := strings.ToValidUTF8(value[:maxBytes-len(marker)], "")
	return prefix + marker
}
