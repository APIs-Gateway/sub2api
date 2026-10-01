// Package middleware provides HTTP middleware for authentication, authorization, and request processing.
package middleware

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// NewAdminAuthMiddleware 创建管理员认证中间件（不支持 admin token，保持原有三参签名供测试和旧调用点使用）
func NewAdminAuthMiddleware(
	authService *service.AuthService,
	userService *service.UserService,
	settingService *service.SettingService,
) AdminAuthMiddleware {
	return AdminAuthMiddleware(adminAuth(authService, userService, settingService, nil))
}

// ProvideAdminAuthMiddleware 创建管理员认证中间件（wire 使用），额外支持 admin token，
// 并为所有写请求记录审计日志（见 admin_audit.go）。auditWriter 为 nil 时不记录审计。
func ProvideAdminAuthMiddleware(
	authService *service.AuthService,
	userService *service.UserService,
	settingService *service.SettingService,
	adminTokens *service.AdminTokenService,
	auditWriter *service.AdminAuditWriter,
) AdminAuthMiddleware {
	// 避免把 nil 指针装进接口后被误判为"已配置"。
	var sink AdminAuditSink
	if auditWriter != nil {
		sink = auditWriter
	}
	return AdminAuthMiddleware(withAdminAudit(adminAuth(authService, userService, settingService, adminTokens), sink))
}

// adminAuth 管理员认证中间件实现
// 支持三种认证方式：
//  1. Admin Token: `x-api-key: s2a_...` 或 `Authorization: Bearer s2a_...`（以 s2a_ 开头即走 admin token 逻辑）
//  2. 旧的全局 Admin API Key: `x-api-key: <admin-api-key>`（行为不变，视为 danger 作用域，记为 legacy_api_key）
//  3. JWT Token: `Authorization: Bearer <jwt-token>`（需要管理员角色）
//
// admin token 通过后会依次完成：吊销/过期/IP 白名单校验（在 service 中）、acting 管理员校验、
// 作用域校验（见 admin_token_scope.go）、写请求的 X-Reason 校验（见 admin_reason.go）。
func adminAuth(
	authService *service.AuthService,
	userService *service.UserService,
	settingService *service.SettingService,
	adminTokens *service.AdminTokenService,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		// WebSocket upgrade requests cannot set Authorization headers in browsers.
		// For admin WebSocket endpoints (e.g. Ops realtime), allow passing the JWT via
		// Sec-WebSocket-Protocol (subprotocol list) using a prefixed token item:
		//   Sec-WebSocket-Protocol: sub2api-admin, jwt.<token>
		if isWebSocketUpgradeRequest(c) {
			if token := extractJWTFromWebSocketSubprotocol(c); token != "" {
				if !validateJWTForAdmin(c, token, authService, userService) {
					return
				}
				c.Next()
				return
			}
		}

		// 检查 x-api-key header（Admin Token 或旧的 Admin API Key 认证）
		apiKey := c.GetHeader("x-api-key")
		if apiKey != "" {
			if service.IsAdminTokenCandidate(apiKey) {
				if !authenticateAdminToken(c, apiKey, userService, adminTokens) {
					return
				}
				c.Next()
				return
			}
			if !validateAdminAPIKey(c, apiKey, settingService, userService) {
				return
			}
			c.Next()
			return
		}

		// 检查 Authorization header（Admin Token 或 JWT 认证）
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				token := strings.TrimSpace(parts[1])
				if token == "" {
					AbortWithError(c, 401, "UNAUTHORIZED", "Authorization required")
					return
				}
				if service.IsAdminTokenCandidate(token) {
					if !authenticateAdminToken(c, token, userService, adminTokens) {
						return
					}
					c.Next()
					return
				}
				if !validateJWTForAdmin(c, token, authService, userService) {
					return
				}
				c.Next()
				return
			}
		}

		// 无有效认证信息
		AbortWithError(c, 401, "UNAUTHORIZED", "Authorization required")
	}
}

func isWebSocketUpgradeRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	// RFC6455 handshake uses:
	//   Connection: Upgrade
	//   Upgrade: websocket
	upgrade := strings.ToLower(strings.TrimSpace(c.GetHeader("Upgrade")))
	if upgrade != "websocket" {
		return false
	}
	connection := strings.ToLower(c.GetHeader("Connection"))
	return strings.Contains(connection, "upgrade")
}

func extractJWTFromWebSocketSubprotocol(c *gin.Context) string {
	if c == nil {
		return ""
	}
	raw := strings.TrimSpace(c.GetHeader("Sec-WebSocket-Protocol"))
	if raw == "" {
		return ""
	}

	// The header is a comma-separated list of tokens. We reserve the prefix "jwt."
	// for carrying the admin JWT.
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if strings.HasPrefix(p, "jwt.") {
			token := strings.TrimSpace(strings.TrimPrefix(p, "jwt."))
			if token != "" {
				return token
			}
		}
	}
	return ""
}

// validateAdminAPIKey 验证管理员 API Key
func validateAdminAPIKey(
	c *gin.Context,
	key string,
	settingService *service.SettingService,
	userService *service.UserService,
) bool {
	storedKey, err := settingService.GetAdminAPIKey(c.Request.Context())
	if err != nil {
		AbortWithError(c, 500, "INTERNAL_ERROR", "Internal server error")
		return false
	}

	// 未配置或不匹配，统一返回相同错误（避免信息泄露）
	if storedKey == "" || subtle.ConstantTimeCompare([]byte(key), []byte(storedKey)) != 1 {
		AbortWithError(c, 401, "INVALID_ADMIN_KEY", "Invalid admin API key")
		return false
	}

	// 获取真实的管理员用户
	admin, err := userService.GetFirstAdmin(c.Request.Context())
	if err != nil {
		AbortWithError(c, 500, "INTERNAL_ERROR", "No admin user found")
		return false
	}

	c.Set(string(ContextKeyUser), AuthSubject{
		UserID:      admin.ID,
		Concurrency: admin.Concurrency,
	})
	c.Set(string(ContextKeyUserRole), admin.Role)
	c.Set("auth_method", service.AuditAuthMethodAdminAPIKey)
	// 旧的全局 key 不受作用域限制，等价于 danger；审计里记为 legacy_api_key。
	setAdminIdentity(c, service.AuditAuthKindLegacyAPIKey, admin.ID, service.AuditAuthKindLegacyAPIKey, admin.Email, service.MaskAuditCredential(key))
	c.Set(string(ContextKeyAdminTokenScope), service.AdminTokenScopeDanger)
	return true
}

// validateJWTForAdmin 验证 JWT 并检查管理员权限
func validateJWTForAdmin(
	c *gin.Context,
	token string,
	authService *service.AuthService,
	userService *service.UserService,
) bool {
	// 验证 JWT token
	claims, err := authService.ValidateToken(token)
	if err != nil {
		if errors.Is(err, service.ErrTokenExpired) {
			AbortWithError(c, 401, "TOKEN_EXPIRED", "Token has expired")
			return false
		}
		AbortWithError(c, 401, "INVALID_TOKEN", "Invalid token")
		return false
	}

	// 从数据库获取用户
	user, err := userService.GetByID(c.Request.Context(), claims.UserID)
	if err != nil {
		if errors.Is(err, service.ErrUserNotFound) {
			AbortWithError(c, 401, "USER_NOT_FOUND", "User not found")
		} else {
			AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to load user")
		}
		return false
	}

	// 检查用户状态
	if !user.IsActive() {
		AbortWithError(c, 401, "USER_INACTIVE", "User account is not active")
		return false
	}

	// 校验 TokenVersion，确保管理员改密后旧 token 失效
	if claims.TokenVersion != user.TokenVersion {
		AbortWithError(c, 401, "TOKEN_REVOKED", "Token has been revoked (password changed)")
		return false
	}

	// 检查管理员权限
	if !user.IsAdmin() {
		AbortWithError(c, 403, "FORBIDDEN", "Admin access required")
		return false
	}

	c.Set(string(ContextKeyUser), AuthSubject{
		UserID:      user.ID,
		Concurrency: user.Concurrency,
	})
	c.Set(string(ContextKeyUserRole), user.Role)
	c.Set(string(ContextKeySessionID), claims.SessionID)
	c.Set("auth_method", service.AuditAuthMethodJWT)
	setAdminIdentity(c, service.AuditAuthKindJWT, user.ID, "jwt:"+user.Email, user.Email, "")

	return true
}

// adminTokenLabel is the actor label recorded for an admin token: "token:<name>#<id>".
func adminTokenLabel(token *service.AdminToken) string {
	return fmt.Sprintf("token:%s#%d", token.Name, token.ID)
}

// authenticateAdminToken 校验 admin token（吊销/过期/IP 白名单在 service 中完成），
// 然后确认 acting 管理员仍然有效，最后做作用域校验。失败时响应已写好，返回 false。
//
// 与旧 key / JWT 一致，成功后 ContextKeyUser 是 acting 管理员；另外在 context 里记下
// token_id、scope 和 auth kind，供后续作用域/审计使用。
func authenticateAdminToken(
	c *gin.Context,
	credential string,
	userService *service.UserService,
	adminTokens *service.AdminTokenService,
) bool {
	if adminTokens == nil || userService == nil {
		abortAdminAuth(c, 401, "ADMIN_TOKEN_INVALID", "Invalid admin token")
		return false
	}

	token, err := adminTokens.Authenticate(c.Request.Context(), credential, SecurityClientIP(c))
	if token != nil {
		// 即使 token 随后被拒绝，也记下是哪把（供审计归因）；此时不设置 ContextKeyUser。
		setAdminIdentity(c, service.AuditAuthKindAdminToken, token.ActingUserID, adminTokenLabel(token), "", token.TokenPrefix)
		c.Set(string(ContextKeyAdminTokenID), token.ID)
		c.Set(string(ContextKeyAdminTokenScope), token.Scope)
	}
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAdminTokenInvalid):
			abortAdminAuth(c, 401, "ADMIN_TOKEN_INVALID", "Invalid admin token")
		case errors.Is(err, service.ErrAdminTokenRevoked):
			abortAdminAuth(c, 401, "ADMIN_TOKEN_REVOKED", "Admin token has been revoked")
		case errors.Is(err, service.ErrAdminTokenExpired):
			abortAdminAuth(c, 401, "ADMIN_TOKEN_EXPIRED", "Admin token has expired")
		case errors.Is(err, service.ErrAdminTokenIPNotAllowed):
			abortAdminAuth(c, 403, "ADMIN_TOKEN_IP_NOT_ALLOWED", "Client IP is not allowed for this admin token")
		default:
			abortAdminAuth(c, 500, "INTERNAL_ERROR", "Internal server error")
		}
		return false
	}

	actor, err := userService.GetByID(c.Request.Context(), token.ActingUserID)
	if err != nil {
		if errors.Is(err, service.ErrUserNotFound) {
			abortAdminAuth(c, 403, "ADMIN_TOKEN_ACTING_USER_INVALID", "The administrator this token acts as no longer exists")
		} else {
			abortAdminAuth(c, 500, "INTERNAL_ERROR", "Failed to load user")
		}
		return false
	}
	if !actor.IsActive() || !actor.IsAdmin() {
		abortAdminAuth(c, 403, "ADMIN_TOKEN_ACTING_USER_INVALID", "The administrator this token acts as is disabled or is no longer an administrator")
		return false
	}

	c.Set(string(ContextKeyUser), AuthSubject{
		UserID:      actor.ID,
		Concurrency: actor.Concurrency,
	})
	c.Set(string(ContextKeyUserRole), actor.Role)
	c.Set("auth_method", service.AuditAuthMethodAdminToken)
	setAdminIdentity(c, service.AuditAuthKindAdminToken, actor.ID, adminTokenLabel(token), actor.Email, token.TokenPrefix)

	if !enforceAdminTokenScope(c, token.Scope) {
		return false
	}
	// 只有 admin token 发起的写请求强制要求 X-Reason；JWT 与旧 key 不强制（有则只记录）。
	return enforceAdminReason(c)
}
