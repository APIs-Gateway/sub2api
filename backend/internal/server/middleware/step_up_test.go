package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stepUpGrantCheckerStub struct {
	granted bool
	err     error
}

func (s stepUpGrantCheckerStub) HasStepUpGrant(context.Context, int64, string) (bool, error) {
	return s.granted, s.err
}

type stepUpUserReaderStub struct {
	user *service.User
	err  error
}

func (s stepUpUserReaderStub) GetByID(context.Context, int64) (*service.User, error) {
	return s.user, s.err
}

func newStepUpContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/sensitive", nil)
	return c, rec
}

func TestStepUpSessionKeyPrefersJWTSession(t *testing.T) {
	c, _ := newStepUpContext(t)
	c.Set(string(ContextKeySessionID), "sid-123")
	require.Equal(t, "sid-123", StepUpSessionKey(c, 7))
}

func TestStepUpSessionKeyFallsBackForLegacyToken(t *testing.T) {
	c, _ := newStepUpContext(t)
	require.Equal(t, "u7", StepUpSessionKey(c, 7))
}

func TestEnforceStepUpRejectsAdminAPIKey(t *testing.T) {
	c, rec := newStepUpContext(t)
	c.Set("auth_method", service.AuditAuthMethodAdminAPIKey)

	ok := EnforceStepUp(c, stepUpGrantCheckerStub{granted: true}, stepUpUserReaderStub{user: &service.User{TotpEnabled: true}})

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_ADMIN_API_KEY_FORBIDDEN")
}

func TestEnforceStepUpRejectsMissingAuth(t *testing.T) {
	c, rec := newStepUpContext(t)

	ok := EnforceStepUp(c, stepUpGrantCheckerStub{granted: true}, stepUpUserReaderStub{user: &service.User{TotpEnabled: true}})

	require.False(t, ok)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestEnforceStepUpRejectsDisabledTotp(t *testing.T) {
	c, rec := newStepUpContext(t)
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stepUpGrantCheckerStub{granted: true}, stepUpUserReaderStub{user: &service.User{TotpEnabled: false}})

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_TOTP_NOT_ENABLED")
}

func TestEnforceStepUpFailsClosedOnGrantError(t *testing.T) {
	c, rec := newStepUpContext(t)
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stepUpGrantCheckerStub{err: errors.New("redis down")}, stepUpUserReaderStub{user: &service.User{TotpEnabled: true}})

	require.False(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_UNAVAILABLE")
}

func TestEnforceStepUpFailsClosedWhenDependenciesAreMissing(t *testing.T) {
	c, rec := newStepUpContext(t)
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, nil, stepUpUserReaderStub{user: &service.User{TotpEnabled: true}})

	require.False(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_UNAVAILABLE")
}

func TestEnforceStepUpRequiresGrant(t *testing.T) {
	c, rec := newStepUpContext(t)
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stepUpGrantCheckerStub{}, stepUpUserReaderStub{user: &service.User{TotpEnabled: true}})

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_REQUIRED")
}

func TestEnforceStepUpPassesWithGrant(t *testing.T) {
	c, _ := newStepUpContext(t)
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})

	require.True(t, EnforceStepUp(c, stepUpGrantCheckerStub{granted: true}, stepUpUserReaderStub{user: &service.User{TotpEnabled: true}}))
}
