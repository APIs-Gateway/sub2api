//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type sessionBindingUserRepo struct {
	service.UserRepository
	user *service.User
}

func (r *sessionBindingUserRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	if r.user == nil || r.user.ID != id {
		return nil, service.ErrUserNotFound
	}
	user := *r.user
	return &user, nil
}

func (r *sessionBindingUserRepo) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

func (r *sessionBindingUserRepo) UpdateUserLastActiveAt(context.Context, int64, time.Time) error {
	return nil
}

type sessionBindingSettingRepo struct{}

func (sessionBindingSettingRepo) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}

func (sessionBindingSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeySessionBindingEnabled {
		return "true", nil
	}
	return "", service.ErrSettingNotFound
}

func (sessionBindingSettingRepo) Set(context.Context, string, string) error { return nil }
func (sessionBindingSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (sessionBindingSettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }
func (sessionBindingSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (sessionBindingSettingRepo) Delete(context.Context, string) error { return nil }

func newSessionBindingAuthMiddlewareEnv(admin bool) (*gin.Engine, *service.AuthService, *service.User) {
	role := service.RoleUser
	if admin {
		role = service.RoleAdmin
	}
	user := &service.User{
		ID:           1,
		Email:        "session-binding@example.com",
		Role:         role,
		Status:       service.StatusActive,
		TokenVersion: 1,
		Concurrency:  1,
	}
	userRepo := &sessionBindingUserRepo{user: user}
	settingService := service.NewSettingService(&sessionBindingSettingRepo{}, &config.Config{})
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-session-binding-secret", AccessTokenExpireMinutes: 60}}
	authService := service.NewAuthService(nil, userRepo, nil, nil, cfg, settingService, nil, nil, nil, nil, nil, nil, nil)
	userService := service.NewUserService(userRepo, nil, nil, nil)

	router := gin.New()
	router.Use(SessionBindingContext())
	if admin {
		router.Use(adminAuth(authService, userService, settingService))
	} else {
		router.Use(jwtAuth(authService, userService, nil))
	}
	router.GET("/protected", func(c *gin.Context) { c.Status(http.StatusOK) })
	return router, authService, user
}

func sessionBindingToken(t *testing.T, authService *service.AuthService, user *service.User) string {
	t.Helper()
	ctx := service.WithSessionBinding(context.Background(), &service.SessionBinding{
		IP:        "192.0.2.1",
		UserAgent: "bound-agent/1.0",
	})
	token, err := authService.GenerateTokenWithContext(ctx, user)
	require.NoError(t, err)
	return token
}

func sessionBindingRequest(token, remoteAddr, userAgent string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.RemoteAddr = remoteAddr
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", userAgent)
	return req
}

func TestJWTAuthRejectsSessionBindingMismatch(t *testing.T) {
	for _, admin := range []bool{false, true} {
		t.Run(map[bool]string{false: "user", true: "admin"}[admin], func(t *testing.T) {
			router, authService, user := newSessionBindingAuthMiddlewareEnv(admin)
			token := sessionBindingToken(t, authService, user)

			matching := httptest.NewRecorder()
			router.ServeHTTP(matching, sessionBindingRequest(token, "192.0.2.1:1234", "bound-agent/1.0"))
			require.Equal(t, http.StatusOK, matching.Code)

			for _, mismatchRequest := range []struct {
				name       string
				remoteAddr string
				userAgent  string
			}{
				{name: "ip", remoteAddr: "192.0.2.2:1234", userAgent: "bound-agent/1.0"},
				{name: "user_agent", remoteAddr: "192.0.2.1:1234", userAgent: "other-agent/1.0"},
			} {
				t.Run(mismatchRequest.name, func(t *testing.T) {
					mismatch := httptest.NewRecorder()
					router.ServeHTTP(mismatch, sessionBindingRequest(token, mismatchRequest.remoteAddr, mismatchRequest.userAgent))
					require.Equal(t, http.StatusUnauthorized, mismatch.Code)
					require.Contains(t, mismatch.Body.String(), "SESSION_BINDING_MISMATCH")
				})
			}
		})
	}
}
