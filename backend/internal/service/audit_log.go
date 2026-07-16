package service

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const (
	// AuditAuthMethodJWT and AuditAuthMethodAdminAPIKey match the values written
	// by the existing authentication middleware.
	AuditAuthMethodJWT         = "jwt"
	AuditAuthMethodAdminAPIKey = "admin_api_key"

	auditRequestBodyMaxBytes     = 16 * 1024
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
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

var auditSensitiveBodyExactKeys = func() map[string]struct{} {
	builtin := []string{
		"authorization",
		"code",
		"codes",
		"cookie",
		"cvv",
		"key",
		"pin",
		"x-api-key",
		"proxy_key",
		"custom_key",
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

var auditSensitiveKeySubstrings = []string{
	"accesskey",
	"apikey",
	"credentialvalue",
	"password",
	"passwd",
	"privatekey",
	"secret",
	"serviceaccount",
	"sessionkey",
	"token",
	"totp",
	"otp",
}

const auditRedactedPlaceholder = "***"

func isAuditSensitiveKey(key string) bool {
	normalized := normalizeAuditBodyKey(key)
	if _, ok := auditSensitiveBodyExactKeys[normalized]; ok {
		return true
	}
	for _, substring := range auditSensitiveKeySubstrings {
		if strings.Contains(normalized, normalizeAuditBodyKey(substring)) {
			return true
		}
	}
	return false
}

// RedactAuditBody removes secrets before a request body is eligible for audit
// storage. Non-JSON data is omitted because format-specific parsing is unsafe.
func RedactAuditBody(raw []byte, contentType string) string {
	if len(raw) == 0 {
		return ""
	}
	if len(raw) > AuditRequestBodyCaptureLimit {
		return "<body omitted: exceeds " + strconv.Itoa(AuditRequestBodyCaptureLimit) + " bytes>"
	}

	if !strings.Contains(strings.ToLower(contentType), "json") || !json.Valid(raw) {
		return auditNonJSONBodyMarker(len(raw), contentType)
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "<unparsable body omitted>"
	}
	encoded, err := json.Marshal(redactAuditValue(value, 0))
	if err != nil {
		return "<redacted body omitted>"
	}
	return truncateAuditString(string(encoded), auditRequestBodyMaxBytes)
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
