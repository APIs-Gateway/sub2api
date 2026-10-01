package admin

import (
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// AdminTokenHandler manages admin tokens (machine credentials for the admin
// API). The routes are registered behind middleware.RequireAdminJWT, so only a
// signed-in human administrator can reach it: a machine credential, whatever
// its scope, can neither mint nor revoke tokens.
type AdminTokenHandler struct {
	tokenService *service.AdminTokenService
}

// NewAdminTokenHandler creates the handler.
func NewAdminTokenHandler(tokenService *service.AdminTokenService) *AdminTokenHandler {
	return &AdminTokenHandler{tokenService: tokenService}
}

// CreateAdminTokenRequest is the body of POST /api/v1/admin/admin-tokens.
type CreateAdminTokenRequest struct {
	// Name is a free-form label (at most 100 characters).
	Name string `json:"name" binding:"required"`
	// Scope is "read", "write" or "danger".
	Scope string `json:"scope" binding:"required"`
	// ActingUserID is the administrator the token acts as. Defaults to the
	// administrator creating the token.
	ActingUserID *int64 `json:"acting_user_id"`
	// IPAllowlist restricts the client IPs that may use the token. Entries are
	// CIDR ranges (a bare IP is accepted). Empty means "any IP".
	IPAllowlist []string `json:"ip_allowlist"`
	// ExpiresAt is mandatory (RFC 3339) and at most 90 days in the future.
	ExpiresAt *time.Time `json:"expires_at" binding:"required"`
}

// AdminTokenDTO is the client representation of a token. It never contains the
// plaintext token or its hash.
type AdminTokenDTO struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	TokenPrefix     string     `json:"token_prefix"`
	Scope           string     `json:"scope"`
	ActingUserID    int64      `json:"acting_user_id"`
	CreatedByUserID *int64     `json:"created_by_user_id"`
	IPAllowlist     []string   `json:"ip_allowlist"`
	ExpiresAt       time.Time  `json:"expires_at"`
	RevokedAt       *time.Time `json:"revoked_at"`
	LastUsedAt      *time.Time `json:"last_used_at"`
	LastUsedIP      string     `json:"last_used_ip"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	// Status is "active", "expired" or "revoked", evaluated when the response
	// is produced.
	Status string `json:"status"`
}

// CreatedAdminTokenDTO is returned once, when a token is created. Token is the
// only time the plaintext is ever shown.
type CreatedAdminTokenDTO struct {
	AdminTokenDTO
	Token string `json:"token"`
}

func adminTokenToDTO(token *service.AdminToken, now time.Time) AdminTokenDTO {
	allowlist := token.IPAllowlist
	if allowlist == nil {
		allowlist = []string{}
	}
	return AdminTokenDTO{
		ID:              token.ID,
		Name:            token.Name,
		TokenPrefix:     token.TokenPrefix,
		Scope:           token.Scope,
		ActingUserID:    token.ActingUserID,
		CreatedByUserID: token.CreatedByUserID,
		IPAllowlist:     allowlist,
		ExpiresAt:       token.ExpiresAt,
		RevokedAt:       token.RevokedAt,
		LastUsedAt:      token.LastUsedAt,
		LastUsedIP:      token.LastUsedIP,
		CreatedAt:       token.CreatedAt,
		UpdatedAt:       token.UpdatedAt,
		Status:          token.Status(now),
	}
}

// Create mints a token and returns its plaintext exactly once.
// POST /api/v1/admin/admin-tokens
func (h *AdminTokenHandler) Create(c *gin.Context) {
	var req CreateAdminTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	actingUserID := subject.UserID
	if req.ActingUserID != nil {
		actingUserID = *req.ActingUserID
	}
	input := service.CreateAdminTokenInput{
		Name:            req.Name,
		Scope:           req.Scope,
		ActingUserID:    actingUserID,
		CreatedByUserID: subject.UserID,
		IPAllowlist:     req.IPAllowlist,
	}
	if req.ExpiresAt != nil {
		input.ExpiresAt = *req.ExpiresAt
	}

	token, plaintext, err := h.tokenService.Create(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// The plaintext must not be cached by anything between us and the caller.
	c.Header("Cache-Control", "no-store")
	response.Created(c, CreatedAdminTokenDTO{
		AdminTokenDTO: adminTokenToDTO(token, time.Now()),
		Token:         plaintext,
	})
}

// List returns all tokens without plaintext or hash.
// GET /api/v1/admin/admin-tokens
func (h *AdminTokenHandler) List(c *gin.Context) {
	tokens, err := h.tokenService.List(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	now := time.Now()
	out := make([]AdminTokenDTO, 0, len(tokens))
	for _, token := range tokens {
		out = append(out, adminTokenToDTO(token, now))
	}
	response.Success(c, out)
}

// Revoke marks a token revoked (soft: the row is kept). Revoking twice is
// harmless.
// DELETE /api/v1/admin/admin-tokens/:id
func (h *AdminTokenHandler) Revoke(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid token ID")
		return
	}
	token, err := h.tokenService.Revoke(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, adminTokenToDTO(token, time.Now()))
}
