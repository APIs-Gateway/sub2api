package middleware

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// X-Reason: why an administrative change is being made
// -----------------------------------------------------
//
// Every state-changing request made with an admin token (any method other than
// GET/HEAD/OPTIONS) must say why it is being made, in the X-Reason header. The
// text is stored in the audit trail next to the request (audit_logs.reason) so
// that a later reader can tell what an automation or an AI agent was trying to
// do. A missing or too short reason is rejected with 400 ADMIN_REASON_REQUIRED
// before the handler runs.
//
// Requests made by a signed-in administrator (JWT) or with the legacy global
// admin API key are not required to send it; if they do, it is recorded too.
//
// How to send it (this is what an AI agent calling the admin API should do):
//
//  1. Plain text. Printable ASCII is always fine, and so is raw UTF-8 (Go,
//     curl and most HTTP libraries send the bytes as given):
//
//     X-Reason: refund order 1234, duplicate charge confirmed with the user
//
//  2. Percent-encoded UTF-8, with the companion header X-Reason-Encoded: url.
//     Use this whenever the reason contains non-ASCII text and the HTTP client
//     or an intermediate proxy only allows ASCII header values (Python
//     requests/urllib3 and browsers' fetch both refuse some non-ASCII
//     values). Encode with encodeURIComponent (JavaScript),
//     urllib.parse.quote (Python) or an equivalent. A space may be %20 or "+";
//     a literal plus sign must be %2B.
//
//     X-Reason: %E8%A1%A5%E5%81%BF%E6%95%85%E9%9A%9C%E7%94%A8%E6%88%B7
//     X-Reason-Encoded: url
//
// Rules: at least 4 characters (runes, after trimming), at most 1000 (longer
// text is cut). Control characters are replaced by a space. Headers can not
// carry line breaks, so a multi-line reason has to be flattened by the caller.
const (
	// AdminReasonHeader carries the reason.
	AdminReasonHeader = "X-Reason"
	// AdminReasonEncodingHeader says how X-Reason is encoded: absent / "none"
	// (raw UTF-8) or "url" (percent-encoded UTF-8).
	AdminReasonEncodingHeader = "X-Reason-Encoded"
	// AdminReasonMinRunes is the shortest accepted reason.
	AdminReasonMinRunes = 4
	// AdminReasonMaxRunes is the longest stored reason.
	AdminReasonMaxRunes = 1000

	// ContextKeyAdminReason holds the parsed reason (string) when one was supplied.
	ContextKeyAdminReason ContextKey = "admin_reason"
)

var (
	errAdminReasonEncodingUnsupported = errors.New("unsupported X-Reason-Encoded value; use \"url\" or omit the header")
	errAdminReasonUndecodable         = errors.New("X-Reason is not valid percent-encoding; send it as plain UTF-8 or percent-encode the whole value")
)

// ParseAdminReason returns the reason sent in the request headers, decoded and
// normalised, or "" when there is none. The error is non-nil only when the
// header is present but its encoding is unsupported or invalid.
func ParseAdminReason(header http.Header) (string, error) {
	raw := header.Get(AdminReasonHeader)
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}

	switch encoding := strings.ToLower(strings.TrimSpace(header.Get(AdminReasonEncodingHeader))); encoding {
	case "", "none", "plain", "utf-8", "utf8":
	case "url":
		decoded, err := url.QueryUnescape(raw)
		if err != nil {
			return "", errAdminReasonUndecodable
		}
		raw = decoded
	default:
		return "", errAdminReasonEncodingUnsupported
	}
	return normalizeAdminReason(raw), nil
}

// normalizeAdminReason makes a reason safe to store and display: valid UTF-8,
// no control characters, trimmed, at most AdminReasonMaxRunes runes.
func normalizeAdminReason(raw string) string {
	if !utf8.ValidString(raw) {
		raw = strings.ToValidUTF8(raw, "�")
	}
	raw = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, raw)
	raw = strings.TrimSpace(raw)
	if utf8.RuneCountInString(raw) > AdminReasonMaxRunes {
		raw = string([]rune(raw)[:AdminReasonMaxRunes])
	}
	return raw
}

// enforceAdminReason applies the X-Reason rule for admin tokens: state-changing
// requests need a reason of at least AdminReasonMinRunes characters. It
// returns false (after answering 400) when the request must not proceed.
func enforceAdminReason(c *gin.Context) bool {
	if isAdminReadOnlyMethod(c.Request.Method) {
		return true
	}
	reason, err := ParseAdminReason(c.Request.Header)
	if err != nil {
		abortAdminAuth(c, http.StatusBadRequest, "ADMIN_REASON_INVALID", err.Error())
		return false
	}
	if utf8.RuneCountInString(reason) < AdminReasonMinRunes {
		abortAdminAuth(c, http.StatusBadRequest, "ADMIN_REASON_REQUIRED",
			"admin tokens must explain every change: send an X-Reason header of at least 4 characters "+
				"(raw UTF-8, or percent-encoded with X-Reason-Encoded: url)")
		return false
	}
	c.Set(string(ContextKeyAdminReason), reason)
	return true
}
