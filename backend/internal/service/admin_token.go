package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
)

// Admin tokens are revocable, scoped, expiring machine credentials for the
// admin API. They exist so an automation (an AI operator running on the
// production host, for example) does not have to share the single global
// admin API key and so that everything it does is attributable.
//
// Wire format: "s2a_" + base64url(32 random bytes), 47 characters in total.
// Only sha256(plaintext) is persisted; lookups are by hash, so there is no
// plaintext comparison against stored secrets.
const (
	// AdminTokenPrefix marks a credential as an admin token. The admin auth
	// middleware dispatches on it, so it must never collide with the legacy
	// admin API key prefix ("admin-") or a JWT ("eyJ").
	AdminTokenPrefix = "s2a_"

	adminTokenRandomBytes = 32
	// adminTokenEncodedLen is the length of base64url(32 bytes) without padding.
	adminTokenEncodedLen = 43
	// AdminTokenDisplayPrefixLen is how many leading plaintext characters are
	// kept (and shown) to help humans recognise a token.
	AdminTokenDisplayPrefixLen = 8

	// AdminTokenMaxLifetime caps expires_at relative to the creation time.
	AdminTokenMaxLifetime = 90 * 24 * time.Hour
	// AdminTokenMaxNameLen is the maximum label length in characters.
	AdminTokenMaxNameLen = 100
	// AdminTokenMaxIPAllowlistEntries bounds the allowlist size.
	AdminTokenMaxIPAllowlistEntries = 32

	// AdminTokenScopeRead allows safe (GET) admin routes only.
	AdminTokenScopeRead = "read"
	// AdminTokenScopeWrite additionally allows ordinary mutations.
	AdminTokenScopeWrite = "write"
	// AdminTokenScopeDanger additionally allows the routes on the explicit
	// danger list (money, deletion, pricing, system settings, ...).
	AdminTokenScopeDanger = "danger"

	// adminTokenLastUsedMinInterval throttles last_used_* writes per token so
	// a busy automation does not turn every request into an UPDATE.
	adminTokenLastUsedMinInterval  = time.Minute
	adminTokenLastUsedWriteTimeout = 5 * time.Second
)

var (
	ErrAdminTokenNotFound     = infraerrors.NotFound("ADMIN_TOKEN_NOT_FOUND", "admin token not found")
	ErrAdminTokenInvalid      = infraerrors.Unauthorized("ADMIN_TOKEN_INVALID", "invalid admin token")
	ErrAdminTokenRevoked      = infraerrors.Unauthorized("ADMIN_TOKEN_REVOKED", "admin token has been revoked")
	ErrAdminTokenExpired      = infraerrors.Unauthorized("ADMIN_TOKEN_EXPIRED", "admin token has expired")
	ErrAdminTokenIPNotAllowed = infraerrors.Forbidden("ADMIN_TOKEN_IP_NOT_ALLOWED", "client IP is not allowed for this admin token")

	ErrAdminTokenNameInvalid = infraerrors.BadRequest("ADMIN_TOKEN_NAME_INVALID",
		"name is required and must be at most 100 characters")
	ErrAdminTokenScopeInvalid = infraerrors.BadRequest("ADMIN_TOKEN_SCOPE_INVALID",
		"scope must be one of: read, write, danger")
	ErrAdminTokenExpiryRequired = infraerrors.BadRequest("ADMIN_TOKEN_EXPIRY_REQUIRED",
		"expires_at is required")
	ErrAdminTokenExpiryInPast = infraerrors.BadRequest("ADMIN_TOKEN_EXPIRY_INVALID",
		"expires_at must be in the future")
	ErrAdminTokenExpiryTooLong = infraerrors.BadRequest("ADMIN_TOKEN_EXPIRY_TOO_LONG",
		"expires_at must be at most 90 days from now")
	ErrAdminTokenIPAllowlistInvalid = infraerrors.BadRequest("ADMIN_TOKEN_IP_ALLOWLIST_INVALID",
		"ip_allowlist must be a list of valid CIDR ranges (or single IP addresses)")
	ErrAdminTokenActingUserInvalid = infraerrors.BadRequest("ADMIN_TOKEN_ACTING_USER_INVALID",
		"acting_user_id must refer to an active administrator")
)

// AdminToken is a stored admin token. The plaintext is never part of this
// type; it only exists in the return value of AdminTokenService.Create.
type AdminToken struct {
	ID          int64
	Name        string
	TokenHash   string // hex sha256 of the plaintext; never serialised to clients
	TokenPrefix string // first AdminTokenDisplayPrefixLen plaintext characters
	Scope       string

	// ActingUserID is the administrator all requests made with the token are
	// attributed to (and authorised as).
	ActingUserID    int64
	CreatedByUserID *int64

	// IPAllowlist holds CIDR ranges; empty means "any client IP".
	IPAllowlist []string

	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	LastUsedIP string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Status reports "revoked", "expired" or "active" as of now.
func (t *AdminToken) Status(now time.Time) string {
	switch {
	case t.RevokedAt != nil:
		return "revoked"
	case !now.Before(t.ExpiresAt):
		return "expired"
	default:
		return "active"
	}
}

// AdminTokenRepository persists admin tokens. There is intentionally no
// physical delete.
type AdminTokenRepository interface {
	// Create inserts the token and fills ID, CreatedAt and UpdatedAt.
	Create(ctx context.Context, token *AdminToken) error
	// GetByHash returns ErrAdminTokenNotFound when no token has that hash.
	GetByHash(ctx context.Context, tokenHash string) (*AdminToken, error)
	// List returns all tokens, newest first.
	List(ctx context.Context) ([]*AdminToken, error)
	// Revoke sets revoked_at (first revocation wins) and returns the row, or
	// ErrAdminTokenNotFound.
	Revoke(ctx context.Context, id int64, at time.Time) (*AdminToken, error)
	// TouchLastUsed records the most recent use of a token.
	TouchLastUsed(ctx context.Context, id int64, at time.Time, clientIP string) error
}

// AdminTokenUserGetter loads the administrator a token acts as. It is
// satisfied by (*UserService).GetByID.
type AdminTokenUserGetter func(ctx context.Context, id int64) (*User, error)

// CreateAdminTokenInput is the validated-at-the-service request to mint a token.
type CreateAdminTokenInput struct {
	Name            string
	Scope           string
	ActingUserID    int64
	CreatedByUserID int64
	IPAllowlist     []string
	ExpiresAt       time.Time
}

// AdminTokenService creates, lists, revokes and authenticates admin tokens.
type AdminTokenService struct {
	repo    AdminTokenRepository
	getUser AdminTokenUserGetter
	now     func() time.Time

	// runAsync runs best-effort background work; tests replace it to run
	// inline.
	runAsync func(func())

	touchMu   sync.Mutex
	lastTouch map[int64]time.Time
}

// NewAdminTokenService creates the service. getUser is used to validate the
// acting administrator when a token is created.
func NewAdminTokenService(repo AdminTokenRepository, getUser AdminTokenUserGetter) *AdminTokenService {
	return &AdminTokenService{
		repo:      repo,
		getUser:   getUser,
		now:       time.Now,
		runAsync:  func(fn func()) { go fn() },
		lastTouch: make(map[int64]time.Time),
	}
}

// ProvideAdminTokenService wires the service to the user service.
func ProvideAdminTokenService(repo AdminTokenRepository, userService *UserService) *AdminTokenService {
	var getUser AdminTokenUserGetter
	if userService != nil {
		getUser = userService.GetByID
	}
	return NewAdminTokenService(repo, getUser)
}

// IsAdminTokenCandidate reports whether a presented credential should be
// handled by the admin token logic (it starts with the s2a_ prefix). A
// candidate is not necessarily well formed.
func IsAdminTokenCandidate(credential string) bool {
	return strings.HasPrefix(strings.TrimSpace(credential), AdminTokenPrefix)
}

// GenerateAdminTokenPlaintext returns a new random token in wire format.
func GenerateAdminTokenPlaintext() (string, error) {
	buf := make([]byte, adminTokenRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate admin token: %w", err)
	}
	return AdminTokenPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashAdminToken returns the hex sha256 of a plaintext token.
func HashAdminToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// adminTokenWellFormed rejects obviously malformed credentials before they
// reach the database.
func adminTokenWellFormed(plaintext string) bool {
	if !strings.HasPrefix(plaintext, AdminTokenPrefix) {
		return false
	}
	body := plaintext[len(AdminTokenPrefix):]
	if len(body) != adminTokenEncodedLen {
		return false
	}
	for i := 0; i < len(body); i++ {
		ch := body[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-', ch == '_':
		default:
			return false
		}
	}
	return true
}

// AdminTokenScopeRank orders scopes: read < write < danger. Unknown scopes
// rank 0 and therefore satisfy nothing.
func AdminTokenScopeRank(scope string) int {
	switch scope {
	case AdminTokenScopeRead:
		return 1
	case AdminTokenScopeWrite:
		return 2
	case AdminTokenScopeDanger:
		return 3
	default:
		return 0
	}
}

// IsValidAdminTokenScope reports whether scope is read, write or danger.
func IsValidAdminTokenScope(scope string) bool {
	return AdminTokenScopeRank(scope) > 0
}

// NormalizeAdminTokenIPAllowlist validates CIDR entries and returns them in
// canonical form. A bare IP is accepted and becomes a /32 (or /128) range.
// Duplicates are dropped. An empty list means "no restriction".
func NormalizeAdminTokenIPAllowlist(entries []string) ([]string, error) {
	if len(entries) > AdminTokenMaxIPAllowlistEntries {
		return nil, ErrAdminTokenIPAllowlistInvalid.WithMetadata(map[string]string{
			"max_entries": fmt.Sprintf("%d", AdminTokenMaxIPAllowlistEntries),
		})
	}
	out := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, raw := range entries {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, ErrAdminTokenIPAllowlistInvalid.WithMetadata(map[string]string{"entry": raw})
		}
		var canonical string
		if strings.Contains(value, "/") {
			_, network, err := net.ParseCIDR(value)
			if err != nil {
				return nil, ErrAdminTokenIPAllowlistInvalid.WithMetadata(map[string]string{"entry": raw})
			}
			canonical = network.String()
		} else {
			parsed := net.ParseIP(value)
			if parsed == nil {
				return nil, ErrAdminTokenIPAllowlistInvalid.WithMetadata(map[string]string{"entry": raw})
			}
			if v4 := parsed.To4(); v4 != nil {
				canonical = v4.String() + "/32"
			} else {
				canonical = parsed.String() + "/128"
			}
		}
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	return out, nil
}

// Create validates the request, mints a token and returns it together with
// its plaintext. The plaintext cannot be recovered afterwards.
func (s *AdminTokenService) Create(ctx context.Context, in CreateAdminTokenInput) (*AdminToken, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > AdminTokenMaxNameLen {
		return nil, "", ErrAdminTokenNameInvalid
	}
	if !IsValidAdminTokenScope(in.Scope) {
		return nil, "", ErrAdminTokenScopeInvalid
	}
	if in.ExpiresAt.IsZero() {
		return nil, "", ErrAdminTokenExpiryRequired
	}
	now := s.now()
	if !in.ExpiresAt.After(now) {
		return nil, "", ErrAdminTokenExpiryInPast
	}
	if in.ExpiresAt.After(now.Add(AdminTokenMaxLifetime)) {
		return nil, "", ErrAdminTokenExpiryTooLong
	}
	allowlist, err := NormalizeAdminTokenIPAllowlist(in.IPAllowlist)
	if err != nil {
		return nil, "", err
	}
	if err := s.validateActingUser(ctx, in.ActingUserID); err != nil {
		return nil, "", err
	}

	plaintext, err := GenerateAdminTokenPlaintext()
	if err != nil {
		return nil, "", err
	}
	token := &AdminToken{
		Name:         name,
		TokenHash:    HashAdminToken(plaintext),
		TokenPrefix:  plaintext[:AdminTokenDisplayPrefixLen],
		Scope:        in.Scope,
		ActingUserID: in.ActingUserID,
		IPAllowlist:  allowlist,
		ExpiresAt:    in.ExpiresAt.UTC(),
	}
	if in.CreatedByUserID > 0 {
		createdBy := in.CreatedByUserID
		token.CreatedByUserID = &createdBy
	}
	if err := s.repo.Create(ctx, token); err != nil {
		return nil, "", fmt.Errorf("create admin token: %w", err)
	}
	return token, plaintext, nil
}

func (s *AdminTokenService) validateActingUser(ctx context.Context, userID int64) error {
	if userID <= 0 {
		return ErrAdminTokenActingUserInvalid
	}
	if s.getUser == nil {
		return fmt.Errorf("admin token service: user lookup is not configured")
	}
	user, err := s.getUser(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return ErrAdminTokenActingUserInvalid
		}
		return fmt.Errorf("load acting user: %w", err)
	}
	if user == nil || !user.IsActive() || !user.IsAdmin() {
		return ErrAdminTokenActingUserInvalid
	}
	return nil
}

// List returns every token (including revoked and expired ones), newest first.
func (s *AdminTokenService) List(ctx context.Context) ([]*AdminToken, error) {
	return s.repo.List(ctx)
}

// Revoke marks a token as revoked. Revoking an already revoked token is a
// no-op that returns the existing row.
func (s *AdminTokenService) Revoke(ctx context.Context, id int64) (*AdminToken, error) {
	if id <= 0 {
		return nil, ErrAdminTokenNotFound
	}
	return s.repo.Revoke(ctx, id, s.now())
}

// Authenticate resolves a presented credential to a stored token and checks
// that it is usable from clientIP: not revoked, not expired, IP allowed (in
// that order).
//
// The returned token is non-nil whenever the credential matched a stored
// row, even if an error is returned. That lets callers attribute a rejected
// attempt (for audit) to the token that was presented. The token is nil when
// the credential is malformed or unknown.
//
// On success last_used_at/last_used_ip are refreshed in the background, at
// most once per minute per token.
func (s *AdminTokenService) Authenticate(ctx context.Context, plaintext, clientIP string) (*AdminToken, error) {
	plaintext = strings.TrimSpace(plaintext)
	if !adminTokenWellFormed(plaintext) {
		return nil, ErrAdminTokenInvalid
	}
	hash := HashAdminToken(plaintext)
	token, err := s.repo.GetByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, ErrAdminTokenNotFound) {
			return nil, ErrAdminTokenInvalid
		}
		return nil, fmt.Errorf("lookup admin token: %w", err)
	}
	// The row was found by hash already; this guards against a repository
	// returning an unrelated row.
	if subtle.ConstantTimeCompare([]byte(token.TokenHash), []byte(hash)) != 1 {
		return nil, ErrAdminTokenInvalid
	}

	now := s.now()
	if token.RevokedAt != nil {
		return token, ErrAdminTokenRevoked
	}
	if !now.Before(token.ExpiresAt) {
		return token, ErrAdminTokenExpired
	}
	if len(token.IPAllowlist) > 0 {
		if allowed, _ := ip.CheckIPRestriction(clientIP, token.IPAllowlist, nil); !allowed {
			return token, ErrAdminTokenIPNotAllowed
		}
	}

	s.touchLastUsed(token.ID, clientIP, now)
	return token, nil
}

func (s *AdminTokenService) touchLastUsed(id int64, clientIP string, now time.Time) {
	s.touchMu.Lock()
	last, seen := s.lastTouch[id]
	if seen && now.Sub(last) < adminTokenLastUsedMinInterval {
		s.touchMu.Unlock()
		return
	}
	s.lastTouch[id] = now
	s.touchMu.Unlock()

	s.runAsync(func() {
		ctx, cancel := context.WithTimeout(context.Background(), adminTokenLastUsedWriteTimeout)
		defer cancel()
		if err := s.repo.TouchLastUsed(ctx, id, now, clientIP); err != nil {
			slog.Warn("admin token: failed to record last use", "token_id", id, "error", err)
		}
	})
}
