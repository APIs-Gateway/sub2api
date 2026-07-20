package middleware

import (
	"math"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// IngressRejectReason identifies expected gateway admission failures that must
// not be treated as operational request errors.
type IngressRejectReason string

const (
	IngressRejectQueryAPIKeyDeprecated  IngressRejectReason = "query_api_key_deprecated"
	IngressRejectAPIKeyRequired         IngressRejectReason = "api_key_required"
	IngressRejectInvalidAPIKey          IngressRejectReason = "invalid_api_key"
	IngressRejectAPIKeyDisabled         IngressRejectReason = "api_key_disabled"
	IngressRejectIPRestricted           IngressRejectReason = "ip_restricted"
	IngressRejectUserInactive           IngressRejectReason = "user_inactive"
	IngressRejectGroupDeleted           IngressRejectReason = "group_deleted"
	IngressRejectGroupDisabled          IngressRejectReason = "group_disabled"
	IngressRejectGroupNotAllowed        IngressRejectReason = "group_not_allowed"
	IngressRejectGroupUnassigned        IngressRejectReason = "group_unassigned"
	IngressRejectInvalidAuthRateLimited IngressRejectReason = "invalid_auth_rate_limited"
	IngressRejectAPIKeyAuthOverloaded   IngressRejectReason = "api_key_auth_overloaded"
)

const ingressRejectReasonContextKey = "ingress_reject_reason"

func invalidAuthClientKey(c *gin.Context) string {
	return normalizeIngressRejectIP(SecurityClientIP(c))
}

func rejectInvalidAuthAbuse(c *gin.Context, apiKeyService interface {
	CheckInvalidAuthAbuse(string) (time.Duration, bool)
}) bool {
	if c == nil || apiKeyService == nil {
		return false
	}
	retry, blocked := apiKeyService.CheckInvalidAuthAbuse(invalidAuthClientKey(c))
	if !blocked {
		return false
	}
	retrySeconds := int(math.Ceil(retry.Seconds()))
	if retrySeconds < 1 {
		retrySeconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(retrySeconds))
	MarkIngressRejected(c, IngressRejectInvalidAuthRateLimited)
	return true
}

func recordInvalidAuthFailure(c *gin.Context, apiKeyService interface {
	RecordInvalidAuthFailure(string)
}) {
	if c == nil || apiKeyService == nil {
		return
	}
	apiKeyService.RecordInvalidAuthFailure(invalidAuthClientKey(c))
}

// MarkIngressRejected marks a request as rejected before gateway admission.
func MarkIngressRejected(c *gin.Context, reason IngressRejectReason) {
	if c == nil || reason == "" {
		return
	}
	c.Set(ingressRejectReasonContextKey, reason)
}

// GetIngressRejectReason returns the admission rejection reason, if any.
func GetIngressRejectReason(c *gin.Context) (IngressRejectReason, bool) {
	if c == nil {
		return "", false
	}
	value, exists := c.Get(ingressRejectReasonContextKey)
	if !exists {
		return "", false
	}
	reason, ok := value.(IngressRejectReason)
	return reason, ok && reason != ""
}

func normalizeIngressRejectIP(raw string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return "0.0.0.0"
	}
	addr = addr.Unmap()
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().Addr().String()
	}
	return addr.String()
}
