//go:build integration

package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/authidentity"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

const atomicTotpSecret = "JBSWY3DPEHPK3PXP"

func atomicTotpRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TOTP_TEST_REDIS_ADDR")
	if addr == "" {
		container, err := tcredis.Run(context.Background(), "redis:8.4-alpine")
		require.NoError(t, err, "real Redis is required for integration tests")
		t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
		addr, err = container.ConnectionString(context.Background())
		require.NoError(t, err)
		options, err := redis.ParseURL(addr)
		require.NoError(t, err)
		addr = options.Addr
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(context.Background()).Err())
	return client
}

type atomicTotpRefreshSpy struct {
	service.RefreshTokenCache
	stored atomic.Int32
}

func (s *atomicTotpRefreshSpy) StoreRefreshToken(context.Context, string, *service.RefreshTokenData, time.Duration) error {
	s.stored.Add(1)
	return nil
}
func (*atomicTotpRefreshSpy) AddToUserTokenSet(context.Context, int64, string, time.Duration) error {
	return nil
}
func (*atomicTotpRefreshSpy) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return nil
}

type atomicTotpReadBarrier struct {
	service.TotpCache
	arrived atomic.Int32
	ready   chan struct{}
}

func (s *atomicTotpReadBarrier) GetLoginSession(ctx context.Context, token string) (*service.TotpLoginSession, error) {
	session, err := s.TotpCache.GetLoginSession(ctx, token)
	if s.arrived.Add(1) == 32 {
		close(s.ready)
	}
	select {
	case <-s.ready:
		return session, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func atomicTotpHandler(t *testing.T, cache service.TotpCache, settings map[string]string) (*AuthHandler, *dbent.Client, *dbent.User, *atomicTotpRefreshSpy) {
	t.Helper()
	spy := &atomicTotpRefreshSpy{}
	h, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{totpCache: cache, totpEncryptor: oauthPendingFlowTotpEncryptorStub{}, refreshTokenCache: spy, settingValues: settings})
	// SQLite has one writer. Serialize DB operations while keeping the Redis
	// session reads/claims concurrent, avoiding unrelated identity-backfill locks.
	client.Driver().(*entsql.Driver).DB().SetMaxOpenConns(1)
	user, err := client.User.Create().SetEmail("atomic-totp@example.com").SetPasswordHash("fixture-hash").SetRole(service.RoleUser).SetStatus(service.StatusActive).SetBalance(3.25).SetConcurrency(4).SetTotpEnabled(true).SetTotpSecretEncrypted(atomicTotpSecret).Save(context.Background())
	require.NoError(t, err)
	_, err = client.AuthIdentity.Create().SetUserID(user.ID).SetProviderType("email").SetProviderKey("email").SetProviderSubject(user.Email).SetVerifiedAt(time.Now()).SetMetadata(map[string]any{"source": "preexisting"}).Save(context.Background())
	require.NoError(t, err)
	return h, client, user, spy
}

func atomicTotpRequest(h *AuthHandler, token, code string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/2fa", bytes.NewBufferString(`{"temp_token":"`+token+`","totp_code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(req.Context(), 15*time.Second)
	defer cancel()
	c.Request = req.WithContext(ctx)
	h.Login2FA(c)
	return rec
}

func TestLoginTotpAtomicActualConcurrent(t *testing.T) {
	rdb := atomicTotpRedis(t)
	cache := repository.NewTotpCache(rdb)
	barrier := &atomicTotpReadBarrier{TotpCache: cache, ready: make(chan struct{})}
	h, client, user, spy := atomicTotpHandler(t, barrier, nil)
	token, err := h.totpService.CreateLoginSession(context.Background(), user.ID, user.Email)
	require.NoError(t, err)
	code, err := totp.GenerateCode(atomicTotpSecret, time.Now())
	require.NoError(t, err)
	var wg sync.WaitGroup
	replies := make(chan *httptest.ResponseRecorder, 32)
	for range 32 {
		wg.Add(1)
		go func() { defer wg.Done(); replies <- atomicTotpRequest(h, token, code) }()
	}
	wg.Wait()
	close(replies)
	winners := 0
	for rec := range replies {
		if rec.Code == http.StatusOK {
			winners++
			data := decodeJSONResponseData(t, rec)
			require.NotEmpty(t, data["refresh_token"])
			claims, err := h.authService.ValidateToken(data["access_token"].(string))
			require.NoError(t, err)
			require.Equal(t, user.ID, claims.UserID)
		} else {
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), "Invalid or expired 2FA session")
		}
	}
	require.Equal(t, 1, winners, "actual refresh token stores=%d", spy.stored.Load())
	require.Equal(t, int32(1), spy.stored.Load(), "only the claimed session may mint a refresh token")
	stored, err := client.User.Get(context.Background(), user.ID)
	require.NoError(t, err)
	require.Equal(t, 3.25, stored.Balance)
	require.Equal(t, 4, stored.Concurrency)
	require.NotNil(t, stored.LastLoginAt)
	session, err := cache.GetLoginSession(context.Background(), token)
	require.NoError(t, err)
	require.Nil(t, session)
}

type atomicTotpClaimError struct{ service.TotpCache }

func (*atomicTotpClaimError) ConsumeLoginSession(context.Context, string) (*service.TotpLoginSession, error) {
	return nil, errors.New("redis unavailable")
}

type atomicTotpChangedClaim struct{ service.TotpCache }

func (s *atomicTotpChangedClaim) ConsumeLoginSession(ctx context.Context, token string) (*service.TotpLoginSession, error) {
	session, err := s.TotpCache.ConsumeLoginSession(ctx, token)
	if session != nil {
		session.UserID++
	}
	return session, err
}

func TestLoginTotpAtomicActualLifecycle(t *testing.T) {
	rdb := atomicTotpRedis(t)
	for _, kind := range []string{"wrong_code_retry", "disabled_retry", "backend_mode_retry", "claim_error", "claim_payload_changed", "expired", "pending_failure_after_claim"} {
		t.Run(kind, func(t *testing.T) {
			cache := repository.NewTotpCache(rdb)
			var used service.TotpCache = cache
			settings := map[string]string{}
			if kind == "backend_mode_retry" {
				settings[service.SettingKeyBackendModeEnabled] = "true"
			}
			if kind == "claim_error" {
				used = &atomicTotpClaimError{TotpCache: cache}
			}
			if kind == "claim_payload_changed" {
				used = &atomicTotpChangedClaim{TotpCache: cache}
			}
			h, client, user, spy := atomicTotpHandler(t, used, settings)
			token, err := h.totpService.CreateLoginSession(context.Background(), user.ID, user.Email)
			require.NoError(t, err)
			code, err := totp.GenerateCode(atomicTotpSecret, time.Now())
			require.NoError(t, err)
			validCode := code
			if kind == "wrong_code_retry" {
				if code[0] == '0' {
					code = "1" + code[1:]
				} else {
					code = "0" + code[1:]
				}
			}
			if kind == "disabled_retry" {
				require.NoError(t, client.User.UpdateOneID(user.ID).SetStatus(service.StatusDisabled).Exec(context.Background()))
			}
			if kind == "expired" {
				session, err := cache.GetLoginSession(context.Background(), token)
				require.NoError(t, err)
				require.NoError(t, cache.SetLoginSession(context.Background(), token, session, time.Millisecond))
				require.Eventually(t, func() bool {
					session, err := cache.GetLoginSession(context.Background(), token)
					return err == nil && session == nil
				}, time.Second, time.Millisecond)
			}
			if kind == "pending_failure_after_claim" {
				session, err := cache.GetLoginSession(context.Background(), token)
				require.NoError(t, err)
				session.PendingOAuthBind = &service.PendingOAuthBindLoginSession{PendingSessionToken: "missing-pending", BrowserSessionKey: "fixture-browser"}
				require.NoError(t, cache.SetLoginSession(context.Background(), token, session, time.Minute))
			}
			rec := atomicTotpRequest(h, token, code)
			require.NotEqual(t, http.StatusOK, rec.Code)
			require.Zero(t, spy.stored.Load())
			stored, err := client.User.Get(context.Background(), user.ID)
			require.NoError(t, err)
			require.Nil(t, stored.LastLoginAt)
			require.Equal(t, 3.25, stored.Balance)
			session, err := cache.GetLoginSession(context.Background(), token)
			require.NoError(t, err)
			if kind == "expired" || kind == "pending_failure_after_claim" || kind == "claim_payload_changed" {
				require.Nil(t, session)
				require.Equal(t, http.StatusBadRequest, atomicTotpRequest(h, token, validCode).Code)
			} else {
				require.NotNil(t, session, "pre-claim failure must retain retry semantics")
				if kind == "wrong_code_retry" || kind == "disabled_retry" || kind == "backend_mode_retry" {
					if kind == "disabled_retry" {
						require.NoError(t, client.User.UpdateOneID(user.ID).SetStatus(service.StatusActive).Exec(context.Background()))
					}
					if kind == "backend_mode_retry" {
						require.NoError(t, client.User.UpdateOneID(user.ID).SetRole(service.RoleAdmin).Exec(context.Background()))
					}
					require.Equal(t, http.StatusOK, atomicTotpRequest(h, token, validCode).Code)
					require.Equal(t, int32(1), spy.stored.Load())
				} else {
					require.NoError(t, cache.DeleteLoginSession(context.Background(), token))
				}
			}
		})
	}
}

func TestLoginTotpAtomicActualPendingOAuthBind(t *testing.T) {
	rdb := atomicTotpRedis(t)
	cache := repository.NewTotpCache(rdb)
	h, client, user, spy := atomicTotpHandler(t, cache, map[string]string{
		service.SettingKeyAuthSourceDefaultOIDCBalance:          "8",
		service.SettingKeyAuthSourceDefaultOIDCConcurrency:      "2",
		service.SettingKeyAuthSourceDefaultOIDCGrantOnFirstBind: "true",
	})
	ctx := context.Background()
	pending, err := client.PendingAuthSession.Create().
		SetSessionToken("totp-atomic-pending").SetIntent("adopt_existing_user_by_email").
		SetProviderType("oidc").SetProviderKey("https://fixture.example").SetProviderSubject("atomic-pending-subject").
		SetTargetUserID(user.ID).SetResolvedEmail(user.Email).SetBrowserSessionKey("atomic-browser").
		SetExpiresAt(time.Now().Add(time.Minute)).Save(ctx)
	require.NoError(t, err)
	token, err := h.totpService.CreatePendingOAuthBindLoginSession(ctx, user.ID, user.Email, pending.SessionToken, pending.BrowserSessionKey)
	require.NoError(t, err)
	code, err := totp.GenerateCode(atomicTotpSecret, time.Now())
	require.NoError(t, err)
	rec := atomicTotpRequest(h, token, code)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, int32(1), spy.stored.Load())
	identity, err := client.AuthIdentity.Query().Where(authidentity.ProviderSubjectEQ("atomic-pending-subject")).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, user.ID, identity.UserID)
	storedPending, err := client.PendingAuthSession.Get(ctx, pending.ID)
	require.NoError(t, err)
	require.NotNil(t, storedPending.ConsumedAt)
	storedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 11.25, storedUser.Balance)
	require.Equal(t, 6, storedUser.Concurrency)
	require.Equal(t, 1, countProviderGrantRecords(t, client, user.ID, "oidc", "first_bind"))
	require.Equal(t, http.StatusBadRequest, atomicTotpRequest(h, token, code).Code)
	require.Equal(t, int32(1), spy.stored.Load())
	storedUser, err = client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 11.25, storedUser.Balance)
}
