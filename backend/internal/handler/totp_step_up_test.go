package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
)

type handlerTotpUserRepoStub struct {
	service.UserRepository
	user *service.User
	err  error
}

func (s *handlerTotpUserRepoStub) GetByID(context.Context, int64) (*service.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.user, nil
}

func (s *handlerTotpUserRepoStub) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

type handlerTotpCacheStub struct {
	service.TotpCache
	granted  bool
	setGrant bool
	attempts int
}

func (s *handlerTotpCacheStub) GetVerifyAttempts(context.Context, int64) (int, error) {
	return s.attempts, nil
}

func (s *handlerTotpCacheStub) IncrementVerifyAttempts(context.Context, int64) (int, error) {
	s.attempts++
	return s.attempts, nil
}

func (s *handlerTotpCacheStub) ClearVerifyAttempts(context.Context, int64) error {
	s.attempts = 0
	return nil
}

func (s *handlerTotpCacheStub) SetStepUpGrant(context.Context, int64, string, time.Duration) error {
	s.setGrant = true
	return nil
}

func (s *handlerTotpCacheStub) HasStepUpGrant(context.Context, int64, string) (bool, error) {
	return s.granted, nil
}

type handlerTotpEncryptorStub struct{}

func (handlerTotpEncryptorStub) Encrypt(value string) (string, error) { return value, nil }
func (handlerTotpEncryptorStub) Decrypt(value string) (string, error) { return value, nil }

func newHandlerTotpService(user *service.User, repoErr error, cache *handlerTotpCacheStub) *service.TotpService {
	return service.NewTotpService(
		&handlerTotpUserRepoStub{user: user, err: repoErr},
		handlerTotpEncryptorStub{},
		cache,
		nil,
		nil,
		nil,
	)
}

func TestTotpVerificationMethodRequiresAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/totp/verification-method", NewTotpHandler(&service.TotpService{}).GetVerificationMethod)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/totp/verification-method", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestTotpVerificationMethodReturnsAdminPasswordMethod(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewTotpHandler(newHandlerTotpService(&service.User{ID: 7, Role: service.RoleAdmin}, nil, &handlerTotpCacheStub{}))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 7})
		c.Next()
	})
	router.GET("/totp/verification-method", handler.GetVerificationMethod)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/totp/verification-method", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"method":"password"`)
}

func TestTotpVerificationMethodReturnsServiceError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewTotpHandler(newHandlerTotpService(nil, context.Canceled, &handlerTotpCacheStub{}))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 7})
		c.Next()
	})
	router.GET("/totp/verification-method", handler.GetVerificationMethod)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/totp/verification-method", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestTotpStepUpRejectsUnauthenticatedAndMalformedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewTotpHandler(&service.TotpService{})
	router := gin.New()
	router.POST("/totp/step-up", handler.StepUp)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/totp/step-up", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	router = gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 1})
		c.Next()
	})
	router.POST("/totp/step-up", handler.StepUp)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/totp/step-up", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTotpStepUpReturnsGrantExpiryForValidCode(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Sub2API", AccountName: "admin@example.com"})
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)

	cache := &handlerTotpCacheStub{}
	handler := NewTotpHandler(newHandlerTotpService(&service.User{
		ID:                  7,
		Role:                service.RoleAdmin,
		TotpEnabled:         true,
		TotpSecretEncrypted: stringPtr(key.Secret()),
	}, nil, cache))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 7})
		c.Set(string(servermiddleware.ContextKeySessionID), "sid-123")
		c.Next()
	})
	router.POST("/totp/step-up", handler.StepUp)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/totp/step-up", strings.NewReader(`{"code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"verified":true`)
	require.Contains(t, rec.Body.String(), `"expires_in":900`)
	require.True(t, cache.setGrant)
}

func TestTotpStepUpReturnsVerificationError(t *testing.T) {
	cache := &handlerTotpCacheStub{}
	handler := NewTotpHandler(newHandlerTotpService(&service.User{
		ID:                  7,
		Role:                service.RoleAdmin,
		TotpEnabled:         true,
		TotpSecretEncrypted: stringPtr("JBSWY3DPEHPK3PXP"),
	}, nil, cache))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 7})
		c.Next()
	})
	router.POST("/totp/step-up", handler.StepUp)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/totp/step-up", strings.NewReader(`{"code":"000000"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "TOTP_INVALID_CODE")
}

func stringPtr(value string) *string { return &value }
