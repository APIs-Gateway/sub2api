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

type cancelOnGatewayGroupLookup struct {
	*fakeGroupRepo
	cancel context.CancelFunc
}

func (r *cancelOnGatewayGroupLookup) GetByIDLite(context.Context, int64) (*service.Group, error) {
	r.cancel()
	return nil, context.Canceled
}

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
		name	string
		path	string
		body	string
		call	func(*gin.Context)
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

// 客户端在调度查询中断开时，各入口都应立即标记 499，不能再返回无账号错误。
func TestGatewayClientCancelDuringAccountSelectionMarks499(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name     string
		platform string
		path     string
		body     string
		route    string
	}{
		{"messages", service.PlatformAnthropic, "/v1/messages", `{"model":"claude-test","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "messages"},
		{"gemini messages", service.PlatformGemini, "/v1/messages", `{"model":"gemini-2.5-flash","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "messages"},
		{"chat completions", service.PlatformAnthropic, "/v1/chat/completions", `{"model":"claude-test","messages":[{"role":"user","content":"hi"}]}`, "chat"},
		{"responses", service.PlatformAnthropic, "/v1/responses", `{"model":"claude-test","input":"hi"}`, "responses"},
		{"gemini native", service.PlatformGemini, "/v1beta/models/gemini-2.5-flash:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, "native"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groupID := int64(9110)
			group := &service.Group{ID: groupID, Hydrated: true, Platform: tc.platform, Status: service.StatusActive}
			requestCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			repo := &cancelOnGatewayGroupLookup{fakeGroupRepo: &fakeGroupRepo{group: group}, cancel: cancel}
			h, cleanup := newTestGatewayHandler(t, group, nil, repo)
			t.Cleanup(cleanup)
			h.cfg = &config.Config{RunMode: config.RunModeSimple}
			apiKey := &service.APIKey{
				ID: 9112, UserID: 9113, GroupID: &groupID, Group: group, Status: service.StatusActive,
				User: &service.User{ID: 9113, Concurrency: 10, Balance: 100},
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body)).WithContext(requestCtx)
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware.ContextKeyAPIKey), apiKey)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})
			if tc.route == "native" {
				c.Params = gin.Params{{Key: "modelAction", Value: "/gemini-2.5-flash:generateContent"}}
			}

			switch tc.route {
			case "messages":
				h.Messages(c)
			case "chat":
				h.ChatCompletions(c)
			case "responses":
				h.Responses(c)
			case "native":
				h.GeminiV1BetaModels(c)
			}

			require.ErrorIs(t, requestCtx.Err(), context.Canceled)
			require.Equal(t, statusClientClosedRequest, c.Writer.Status())
			require.Zero(t, rec.Body.Len())
		})
	}
}
