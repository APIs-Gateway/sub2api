//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// adminTokenTestRepo is an in-memory AdminTokenRepository indexed by hash, like
// the real table's unique index.
type adminTokenTestRepo struct {
	mu        sync.Mutex
	nextID    int64
	byHash    map[string]*AdminToken
	touched   []adminTokenTouch
	touchErr  error
	lookupErr error
}

type adminTokenTouch struct {
	id int64
	at time.Time
	ip string
}

func newAdminTokenTestRepo() *adminTokenTestRepo {
	return &adminTokenTestRepo{byHash: map[string]*AdminToken{}}
}

func (r *adminTokenTestRepo) Create(_ context.Context, token *AdminToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	token.ID = r.nextID
	token.CreatedAt = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	token.UpdatedAt = token.CreatedAt
	stored := *token
	r.byHash[token.TokenHash] = &stored
	return nil
}

func (r *adminTokenTestRepo) GetByHash(_ context.Context, hash string) (*AdminToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lookupErr != nil {
		return nil, r.lookupErr
	}
	token, ok := r.byHash[hash]
	if !ok {
		return nil, ErrAdminTokenNotFound
	}
	clone := *token
	return &clone, nil
}

func (r *adminTokenTestRepo) List(_ context.Context) ([]*AdminToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*AdminToken, 0, len(r.byHash))
	for _, token := range r.byHash {
		clone := *token
		out = append(out, &clone)
	}
	return out, nil
}

func (r *adminTokenTestRepo) Revoke(_ context.Context, id int64, at time.Time) (*AdminToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, token := range r.byHash {
		if token.ID == id {
			if token.RevokedAt == nil {
				revokedAt := at
				token.RevokedAt = &revokedAt
			}
			clone := *token
			return &clone, nil
		}
	}
	return nil, ErrAdminTokenNotFound
}

func (r *adminTokenTestRepo) TouchLastUsed(_ context.Context, id int64, at time.Time, ip string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touched = append(r.touched, adminTokenTouch{id: id, at: at, ip: ip})
	return r.touchErr
}

func (r *adminTokenTestRepo) touches() []adminTokenTouch {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]adminTokenTouch(nil), r.touched...)
}

var adminTokenTestNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func newAdminTokenTestService(t *testing.T) (*AdminTokenService, *adminTokenTestRepo) {
	t.Helper()
	repo := newAdminTokenTestRepo()
	users := map[int64]*User{
		1: {ID: 1, Email: "admin@example.com", Role: RoleAdmin, Status: StatusActive},
		2: {ID: 2, Email: "user@example.com", Role: RoleUser, Status: StatusActive},
		3: {ID: 3, Email: "disabled@example.com", Role: RoleAdmin, Status: StatusDisabled},
	}
	svc := NewAdminTokenService(repo, func(_ context.Context, id int64) (*User, error) {
		user, ok := users[id]
		if !ok {
			return nil, ErrUserNotFound
		}
		return user, nil
	})
	svc.now = func() time.Time { return adminTokenTestNow }
	svc.runAsync = func(fn func()) { fn() }
	return svc, repo
}

func validCreateInput() CreateAdminTokenInput {
	return CreateAdminTokenInput{
		Name:            "ops-bot",
		Scope:           AdminTokenScopeWrite,
		ActingUserID:    1,
		CreatedByUserID: 1,
		ExpiresAt:       adminTokenTestNow.Add(30 * 24 * time.Hour),
	}
}

func TestGenerateAdminTokenPlaintextFormat(t *testing.T) {
	first, err := GenerateAdminTokenPlaintext()
	require.NoError(t, err)
	second, err := GenerateAdminTokenPlaintext()
	require.NoError(t, err)

	require.NotEqual(t, first, second)
	require.True(t, strings.HasPrefix(first, "s2a_"))
	require.Len(t, first, len("s2a_")+43, "32 bytes of base64url without padding")
	require.True(t, adminTokenWellFormed(first))
	require.True(t, IsAdminTokenCandidate(first))
	require.NotContains(t, first, "=", "no base64 padding")
	require.NotContains(t, first, "+")
	require.NotContains(t, first, "/")
}

func TestHashAdminTokenIsStableSHA256Hex(t *testing.T) {
	hash := HashAdminToken("s2a_abc")
	require.Len(t, hash, 64)
	require.Equal(t, hash, HashAdminToken("s2a_abc"))
	require.NotEqual(t, hash, HashAdminToken("s2a_abd"))
	// Known SHA-256 of the empty string, proving this is plain sha256 -> hex.
	require.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", HashAdminToken(""))
}

func TestIsAdminTokenCandidateOnlyMatchesPrefix(t *testing.T) {
	require.True(t, IsAdminTokenCandidate("s2a_whatever"))
	require.True(t, IsAdminTokenCandidate("  s2a_whatever  "))
	require.False(t, IsAdminTokenCandidate("admin-0123456789abcdef"), "legacy admin API key")
	require.False(t, IsAdminTokenCandidate("eyJhbGciOiJIUzI1NiJ9.e30.sig"), "JWT")
	require.False(t, IsAdminTokenCandidate("sk-abc"), "user API key")
	require.False(t, IsAdminTokenCandidate(""))
}

func TestAdminTokenScopeRankIsALadder(t *testing.T) {
	require.Less(t, AdminTokenScopeRank(AdminTokenScopeRead), AdminTokenScopeRank(AdminTokenScopeWrite))
	require.Less(t, AdminTokenScopeRank(AdminTokenScopeWrite), AdminTokenScopeRank(AdminTokenScopeDanger))
	require.Equal(t, 0, AdminTokenScopeRank("root"))
	require.Equal(t, 0, AdminTokenScopeRank(""))
	require.True(t, IsValidAdminTokenScope("read"))
	require.False(t, IsValidAdminTokenScope("admin"))
}

func TestAdminTokenCreateStoresOnlyTheHash(t *testing.T) {
	svc, repo := newAdminTokenTestService(t)
	input := validCreateInput()
	input.IPAllowlist = []string{"10.0.0.5", "192.168.1.0/24", "10.0.0.5/32"}

	token, plaintext, err := svc.Create(context.Background(), input)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(plaintext, "s2a_"))
	require.Equal(t, HashAdminToken(plaintext), token.TokenHash)
	require.Equal(t, plaintext[:8], token.TokenPrefix)
	require.Equal(t, "ops-bot", token.Name)
	require.Equal(t, AdminTokenScopeWrite, token.Scope)
	require.EqualValues(t, 1, token.ActingUserID)
	require.NotNil(t, token.CreatedByUserID)
	require.EqualValues(t, 1, *token.CreatedByUserID)
	require.Equal(t, []string{"10.0.0.5/32", "192.168.1.0/24"}, token.IPAllowlist, "bare IP normalised, duplicate dropped")

	stored := repo.byHash[token.TokenHash]
	require.NotNil(t, stored)
	require.NotContains(t, stored.TokenHash, plaintext)
	require.NotEqual(t, plaintext, stored.TokenHash)
}

func TestAdminTokenCreateValidation(t *testing.T) {
	svc, _ := newAdminTokenTestService(t)
	ctx := context.Background()

	tests := []struct {
		name   string
		mutate func(in *CreateAdminTokenInput)
		want   error
	}{
		{"empty name", func(in *CreateAdminTokenInput) { in.Name = "   " }, ErrAdminTokenNameInvalid},
		{"name too long", func(in *CreateAdminTokenInput) { in.Name = strings.Repeat("x", 101) }, ErrAdminTokenNameInvalid},
		{"unknown scope", func(in *CreateAdminTokenInput) { in.Scope = "root" }, ErrAdminTokenScopeInvalid},
		{"empty scope", func(in *CreateAdminTokenInput) { in.Scope = "" }, ErrAdminTokenScopeInvalid},
		{"missing expiry", func(in *CreateAdminTokenInput) { in.ExpiresAt = time.Time{} }, ErrAdminTokenExpiryRequired},
		{"expiry in the past", func(in *CreateAdminTokenInput) { in.ExpiresAt = adminTokenTestNow.Add(-time.Hour) }, ErrAdminTokenExpiryInPast},
		{"expiry now", func(in *CreateAdminTokenInput) { in.ExpiresAt = adminTokenTestNow }, ErrAdminTokenExpiryInPast},
		{"expiry beyond 90 days", func(in *CreateAdminTokenInput) {
			in.ExpiresAt = adminTokenTestNow.Add(AdminTokenMaxLifetime + time.Second)
		}, ErrAdminTokenExpiryTooLong},
		{"bad CIDR", func(in *CreateAdminTokenInput) { in.IPAllowlist = []string{"10.0.0.0/33"} }, ErrAdminTokenIPAllowlistInvalid},
		{"not an IP", func(in *CreateAdminTokenInput) { in.IPAllowlist = []string{"localhost"} }, ErrAdminTokenIPAllowlistInvalid},
		{"blank entry", func(in *CreateAdminTokenInput) { in.IPAllowlist = []string{" "} }, ErrAdminTokenIPAllowlistInvalid},
		{"acting user missing", func(in *CreateAdminTokenInput) { in.ActingUserID = 99 }, ErrAdminTokenActingUserInvalid},
		{"acting user zero", func(in *CreateAdminTokenInput) { in.ActingUserID = 0 }, ErrAdminTokenActingUserInvalid},
		{"acting user not admin", func(in *CreateAdminTokenInput) { in.ActingUserID = 2 }, ErrAdminTokenActingUserInvalid},
		{"acting user disabled", func(in *CreateAdminTokenInput) { in.ActingUserID = 3 }, ErrAdminTokenActingUserInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validCreateInput()
			test.mutate(&input)
			token, plaintext, err := svc.Create(ctx, input)
			require.Error(t, err)
			require.True(t, errors.Is(err, test.want), "got %v want %v", err, test.want)
			require.Nil(t, token)
			require.Empty(t, plaintext)
		})
	}

	t.Run("exactly 90 days is accepted", func(t *testing.T) {
		input := validCreateInput()
		input.ExpiresAt = adminTokenTestNow.Add(AdminTokenMaxLifetime)
		_, _, err := svc.Create(ctx, input)
		require.NoError(t, err)
	})
	t.Run("empty allowlist means unrestricted", func(t *testing.T) {
		input := validCreateInput()
		input.IPAllowlist = nil
		token, _, err := svc.Create(ctx, input)
		require.NoError(t, err)
		require.Empty(t, token.IPAllowlist)
	})
}

func TestNormalizeAdminTokenIPAllowlist(t *testing.T) {
	got, err := NormalizeAdminTokenIPAllowlist([]string{"10.1.2.3/8", "::1", "2001:db8::/32", " 127.0.0.1 "})
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.0/8", "::1/128", "2001:db8::/32", "127.0.0.1/32"}, got)

	tooMany := make([]string, AdminTokenMaxIPAllowlistEntries+1)
	for i := range tooMany {
		tooMany[i] = "10.0.0.1"
	}
	_, err = NormalizeAdminTokenIPAllowlist(tooMany)
	require.Error(t, err)
}

func createAdminTokenForTest(t *testing.T, svc *AdminTokenService, mutate func(in *CreateAdminTokenInput)) (*AdminToken, string) {
	t.Helper()
	input := validCreateInput()
	if mutate != nil {
		mutate(&input)
	}
	token, plaintext, err := svc.Create(context.Background(), input)
	require.NoError(t, err)
	return token, plaintext
}

func TestAdminTokenAuthenticateByHash(t *testing.T) {
	svc, repo := newAdminTokenTestService(t)
	created, plaintext := createAdminTokenForTest(t, svc, nil)

	token, err := svc.Authenticate(context.Background(), plaintext, "203.0.113.9")
	require.NoError(t, err)
	require.Equal(t, created.ID, token.ID)
	require.Equal(t, AdminTokenScopeWrite, token.Scope)

	touches := repo.touches()
	require.Len(t, touches, 1)
	require.Equal(t, created.ID, touches[0].id)
	require.Equal(t, "203.0.113.9", touches[0].ip)
	require.Equal(t, adminTokenTestNow, touches[0].at)
}

func TestAdminTokenAuthenticateRejectsUnknownAndMalformed(t *testing.T) {
	svc, repo := newAdminTokenTestService(t)
	_, plaintext := createAdminTokenForTest(t, svc, nil)

	other, err := GenerateAdminTokenPlaintext()
	require.NoError(t, err)

	for name, credential := range map[string]string{
		"unknown token":          other,
		"truncated token":        plaintext[:len(plaintext)-1],
		"extended token":         plaintext + "A",
		"wrong prefix":           "s2b_" + plaintext[4:],
		"bad character":          plaintext[:10] + "!" + plaintext[11:],
		"empty":                  "",
		"prefix only":            "s2a_",
		"legacy key shaped":      "admin-0123456789abcdef0123456789abcdef",
		"hash instead of secret": HashAdminToken(plaintext),
	} {
		t.Run(name, func(t *testing.T) {
			token, err := svc.Authenticate(context.Background(), credential, "127.0.0.1")
			require.ErrorIs(t, err, ErrAdminTokenInvalid)
			require.Nil(t, token, "an unrecognised credential must not identify any token")
		})
	}
	require.Empty(t, repo.touches())
}

func TestAdminTokenAuthenticateTrimsWhitespace(t *testing.T) {
	svc, _ := newAdminTokenTestService(t)
	_, plaintext := createAdminTokenForTest(t, svc, nil)

	_, err := svc.Authenticate(context.Background(), "  "+plaintext+"\n", "127.0.0.1")
	require.NoError(t, err)
}

func TestAdminTokenAuthenticateRevoked(t *testing.T) {
	svc, repo := newAdminTokenTestService(t)
	created, plaintext := createAdminTokenForTest(t, svc, nil)

	revoked, err := svc.Revoke(context.Background(), created.ID)
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt)
	require.Equal(t, "revoked", revoked.Status(adminTokenTestNow))

	token, err := svc.Authenticate(context.Background(), plaintext, "127.0.0.1")
	require.ErrorIs(t, err, ErrAdminTokenRevoked)
	require.NotNil(t, token, "a revoked token is still identified, so the attempt can be attributed")
	require.Equal(t, created.ID, token.ID)
	require.Empty(t, repo.touches(), "a rejected attempt is not a use")
}

func TestAdminTokenRevokeIsIdempotentAndKeepsFirstTimestamp(t *testing.T) {
	svc, _ := newAdminTokenTestService(t)
	created, _ := createAdminTokenForTest(t, svc, nil)

	first, err := svc.Revoke(context.Background(), created.ID)
	require.NoError(t, err)

	svc.now = func() time.Time { return adminTokenTestNow.Add(time.Hour) }
	second, err := svc.Revoke(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, *first.RevokedAt, *second.RevokedAt)

	_, err = svc.Revoke(context.Background(), 12345)
	require.ErrorIs(t, err, ErrAdminTokenNotFound)
	_, err = svc.Revoke(context.Background(), 0)
	require.ErrorIs(t, err, ErrAdminTokenNotFound)
}

func TestAdminTokenAuthenticateExpired(t *testing.T) {
	svc, repo := newAdminTokenTestService(t)
	created, plaintext := createAdminTokenForTest(t, svc, nil)

	svc.now = func() time.Time { return created.ExpiresAt.Add(-time.Second) }
	_, err := svc.Authenticate(context.Background(), plaintext, "127.0.0.1")
	require.NoError(t, err, "still valid one second before expiry")

	svc.now = func() time.Time { return created.ExpiresAt }
	token, err := svc.Authenticate(context.Background(), plaintext, "127.0.0.1")
	require.ErrorIs(t, err, ErrAdminTokenExpired, "expires_at itself is already expired")
	require.NotNil(t, token)
	require.Equal(t, "expired", token.Status(created.ExpiresAt))
	require.Len(t, repo.touches(), 1, "only the successful call counted as a use")
}

func TestAdminTokenAuthenticateIPAllowlist(t *testing.T) {
	svc, repo := newAdminTokenTestService(t)
	_, plaintext := createAdminTokenForTest(t, svc, func(in *CreateAdminTokenInput) {
		in.IPAllowlist = []string{"127.0.0.1", "10.20.0.0/16"}
	})

	for ip, allowed := range map[string]bool{
		"127.0.0.1":   true,
		"10.20.99.7":  true,
		"10.21.0.1":   false,
		"203.0.113.9": false,
		"":            false,
		"not-an-ip":   false,
		"::1":         false,
	} {
		t.Run("client "+ip, func(t *testing.T) {
			token, err := svc.Authenticate(context.Background(), plaintext, ip)
			if allowed {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrAdminTokenIPNotAllowed)
			require.NotNil(t, token)
		})
	}
	_ = repo
}

func TestAdminTokenAuthenticateEmptyAllowlistAllowsAnyIP(t *testing.T) {
	svc, _ := newAdminTokenTestService(t)
	_, plaintext := createAdminTokenForTest(t, svc, nil)

	for _, ip := range []string{"127.0.0.1", "203.0.113.9", "2001:db8::1", ""} {
		_, err := svc.Authenticate(context.Background(), plaintext, ip)
		require.NoErrorf(t, err, "client %q", ip)
	}
}

func TestAdminTokenAuthenticateCheckOrderIsRevokedThenExpiredThenIP(t *testing.T) {
	svc, _ := newAdminTokenTestService(t)
	created, plaintext := createAdminTokenForTest(t, svc, func(in *CreateAdminTokenInput) {
		in.IPAllowlist = []string{"10.0.0.0/8"}
	})
	_, err := svc.Revoke(context.Background(), created.ID)
	require.NoError(t, err)
	svc.now = func() time.Time { return created.ExpiresAt.Add(time.Hour) }

	_, err = svc.Authenticate(context.Background(), plaintext, "203.0.113.9")
	require.ErrorIs(t, err, ErrAdminTokenRevoked)
}

func TestAdminTokenAuthenticateRepositoryFailureIsNotAnAuthFailure(t *testing.T) {
	svc, repo := newAdminTokenTestService(t)
	_, plaintext := createAdminTokenForTest(t, svc, nil)
	repo.lookupErr = errors.New("database is down")

	token, err := svc.Authenticate(context.Background(), plaintext, "127.0.0.1")
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrAdminTokenInvalid), "an outage must not look like a bad credential")
	require.Nil(t, token)
}

func TestAdminTokenLastUsedIsThrottled(t *testing.T) {
	svc, repo := newAdminTokenTestService(t)
	created, plaintext := createAdminTokenForTest(t, svc, nil)

	now := adminTokenTestNow
	svc.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		_, err := svc.Authenticate(context.Background(), plaintext, "127.0.0.1")
		require.NoError(t, err)
		now = now.Add(5 * time.Second)
	}
	require.Len(t, repo.touches(), 1, "five calls within a minute cause one write")

	now = adminTokenTestNow.Add(adminTokenLastUsedMinInterval)
	_, err := svc.Authenticate(context.Background(), plaintext, "198.51.100.1")
	require.NoError(t, err)
	touches := repo.touches()
	require.Len(t, touches, 2)
	require.Equal(t, created.ID, touches[1].id)
	require.Equal(t, "198.51.100.1", touches[1].ip)
}

func TestAdminTokenLastUsedFailureDoesNotFailTheRequest(t *testing.T) {
	svc, repo := newAdminTokenTestService(t)
	_, plaintext := createAdminTokenForTest(t, svc, nil)
	repo.touchErr = errors.New("write failed")

	_, err := svc.Authenticate(context.Background(), plaintext, "127.0.0.1")
	require.NoError(t, err)
}

func TestAdminTokenStatus(t *testing.T) {
	revokedAt := adminTokenTestNow
	token := &AdminToken{ExpiresAt: adminTokenTestNow.Add(time.Hour)}
	require.Equal(t, "active", token.Status(adminTokenTestNow))
	require.Equal(t, "expired", token.Status(adminTokenTestNow.Add(time.Hour)))
	token.RevokedAt = &revokedAt
	require.Equal(t, "revoked", token.Status(adminTokenTestNow), "revoked wins over expired")
	require.Equal(t, "revoked", token.Status(adminTokenTestNow.Add(2*time.Hour)))
}

func TestAdminTokenCreateRejectsUnconfiguredUserLookup(t *testing.T) {
	svc := NewAdminTokenService(newAdminTokenTestRepo(), nil)
	svc.now = func() time.Time { return adminTokenTestNow }
	_, _, err := svc.Create(context.Background(), validCreateInput())
	require.Error(t, err)
}
