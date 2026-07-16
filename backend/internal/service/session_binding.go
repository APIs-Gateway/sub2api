package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// ErrSessionBindingMismatch indicates that a session's trusted IP/User-Agent fingerprint changed.
var ErrSessionBindingMismatch = infraerrors.Unauthorized("SESSION_BINDING_MISMATCH", "session network fingerprint changed, please login again")

// SessionBinding is the trusted client fingerprint attached to an auth session.
type SessionBinding struct {
	IP        string
	UserAgent string
}

// Hash returns a bounded, stable fingerprint for the normalized IP and User-Agent.
// An empty fingerprint means that the request did not provide enough trusted data.
func (b *SessionBinding) Hash() string {
	if b == nil {
		return ""
	}
	ip := strings.TrimSpace(b.IP)
	userAgent := strings.TrimSpace(b.UserAgent)
	if ip == "" && userAgent == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ip + "\n" + userAgent))
	return hex.EncodeToString(sum[:16])
}

type sessionBindingContextKey struct{}

// WithSessionBinding stores a trusted request binding in context.
func WithSessionBinding(ctx context.Context, binding *SessionBinding) context.Context {
	if binding == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionBindingContextKey{}, binding)
}

// SessionBindingFromContext returns the trusted binding attached to ctx.
func SessionBindingFromContext(ctx context.Context) *SessionBinding {
	if ctx == nil {
		return nil
	}
	binding, _ := ctx.Value(sessionBindingContextKey{}).(*SessionBinding)
	return binding
}

func sessionBindingHashFromContext(ctx context.Context) string {
	return SessionBindingFromContext(ctx).Hash()
}
