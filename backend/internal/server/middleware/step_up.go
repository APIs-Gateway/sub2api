package middleware

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type stepUpGrantChecker interface {
	HasStepUpGrant(ctx context.Context, userID int64, sessionKey string) (bool, error)
}

type stepUpUserReader interface {
	GetByID(ctx context.Context, id int64) (*service.User, error)
}

// StepUpSessionKey binds a grant to the current JWT session. Legacy tokens
// without sid use a user-scoped fallback for compatibility.
func StepUpSessionKey(c *gin.Context, userID int64) string {
	if sid := c.GetString(string(ContextKeySessionID)); sid != "" {
		return sid
	}
	return fmt.Sprintf("u%d", userID)
}

// EnforceStepUp applies the same gate used by sensitive route middleware and
// can be called conditionally by handlers.
func EnforceStepUp(c *gin.Context, grantChecker stepUpGrantChecker, userReader stepUpUserReader) bool {
	if c.GetString("auth_method") == service.AuditAuthMethodAdminAPIKey {
		AbortWithError(c, 403, "STEP_UP_ADMIN_API_KEY_FORBIDDEN",
			"Admin API key cannot access this endpoint; a two-factor verified admin session is required")
		return false
	}

	subject, ok := GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		AbortWithError(c, 401, "UNAUTHORIZED", "Authorization required")
		return false
	}
	if grantChecker == nil || userReader == nil {
		AbortWithError(c, 503, "STEP_UP_UNAVAILABLE", "Step-up verification service unavailable")
		return false
	}

	user, err := userReader.GetByID(c.Request.Context(), subject.UserID)
	if err != nil {
		AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to load user")
		return false
	}
	if !user.TotpEnabled {
		AbortWithError(c, 403, "STEP_UP_TOTP_NOT_ENABLED",
			"This operation requires two-factor authentication; please enable TOTP first")
		return false
	}

	granted, err := grantChecker.HasStepUpGrant(
		c.Request.Context(), subject.UserID, StepUpSessionKey(c, subject.UserID),
	)
	if err != nil {
		AbortWithError(c, 503, "STEP_UP_UNAVAILABLE", "Step-up verification service unavailable")
		return false
	}
	if !granted {
		AbortWithError(c, 403, "STEP_UP_REQUIRED",
			"This operation requires recent two-factor verification")
		return false
	}

	return true
}
