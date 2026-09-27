//go:build unit

package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayPreCanceledCompatibleRequestsMark499(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(9100)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
	h, cleanup := newTestGatewayHandler(t, group, nil)
	t.Cleanup(cleanup)
	h.cfg = &config.Config{RunMode: config.RunModeSimple}
	apiKey := &service.APIKey{
		ID: 9102, UserID: 9103, GroupID: &groupID, Group: group, Status: service.StatusActive,
		User: &service.User{ID: 9103, Concurrency: 10, Balance: 100},
	}
	cases := []struct {
		name, path, body string
		call             func(*gin.Context)
	}{
		{"responses", "/v1/responses", `{"model":"claude-test","input":"hello","stream":false}`, h.Responses},
		{"chat completions", "/v1/chat/completions", `{"model":"claude-test","messages":[{"role":"user","content":"hello"}],"stream":false}`, h.ChatCompletions},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			ctx = context.WithValue(ctx, ctxkey.Group, group)
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			c.Request = req
			c.Set(string(middleware.ContextKeyAPIKey), apiKey)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

			tc.call(c)

			_, selected := c.Get(opsAccountIDKey)
			require.False(t, selected)
			require.Equal(t, statusClientClosedRequest, c.Writer.Status())
			require.Zero(t, recorder.Body.Len())
		})
	}
}
