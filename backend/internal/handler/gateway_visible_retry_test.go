//go:build unit

package handler

import (
	"bytes"
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type visibleRetryDelayedBody struct {
	reader *strings.Reader
	delay  bool
	closed chan struct{}
	once   sync.Once
}

func (b *visibleRetryDelayedBody) Read(p []byte) (int, error) {
	if b.delay {
		b.delay = false
		timer := time.NewTimer(1100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-b.closed:
			return 0, io.ErrClosedPipe
		}
	}
	return b.reader.Read(p)
}
func (b *visibleRetryDelayedBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

type visibleRetryUpstream struct {
	calls    int
	accounts []int64
	mode     string
}

func (u *visibleRetryUpstream) Do(req *http.Request, proxy string, id int64, n int) (*http.Response, error) {
	return u.DoWithTLS(req, proxy, id, n, nil)
}
func (u *visibleRetryUpstream) DoWithTLS(_ *http.Request, _ string, id int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	u.accounts = append(u.accounts, id)
	payload := compatPartialMessageStartSSE + "event: message_delta\ndata: " + `{"type":"message_delta","usage":{"output_tokens":15}}` + "\n\n"
	header := "success-attempt"
	if u.mode == "comment_retry" && u.calls == 1 {
		payload = "event: message_start\ndata: " + `{"type":"message_start","message":{"usage":{"input_tokens":99,"output_tokens":88}}}` + "\n\nevent: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
		header = "failed-attempt"
	}
	if u.mode == "committed_error" {
		payload += "event: error\ndata: " + `{"type":"error","error":{"type":"overloaded_error","message":"fixture"}}` + "\n\n"
	} else {
		payload += "event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
	}
	var body io.ReadCloser = io.NopCloser(strings.NewReader(payload))
	if u.mode == "comment_retry" && u.calls == 1 {
		body = &visibleRetryDelayedBody{reader: strings.NewReader(payload), delay: true, closed: make(chan struct{})}
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{header}}, Body: body}, nil
}
func TestGatewayVisibleRetry_RealMessagesHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"comment_retry", "committed_error"} {
		t.Run(mode, func(t *testing.T) {
			gid := int64(9152)
			group := &service.Group{ID: gid, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
			accounts := []*service.Account{}
			for _, id := range []int64{9252, 9253} {
				accounts = append(accounts, &service.Account{ID: id, Name: "native-visible", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "local", "pool_mode": true, "pool_mode_retry_count": 0}, Concurrency: 1, Priority: 1, Status: service.StatusActive, Schedulable: true, AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: gid}}})
			}
			upstream := &visibleRetryUpstream{mode: mode}
			usage := &partialUsageBillingUsageLogRepo{created: make(chan *service.UsageLog, 4)}
			h, cleanup := newPartialUsageBillingGatewayHandler(t, group, accounts, upstream, &forceCacheBillingGatewayCache{accountID: 9252}, usage)
			defer cleanup()
			h.cfg.Gateway.StreamKeepaliveInterval = 1
			h.cfg.Gateway.StreamDataIntervalTimeout = 3
			pool := newUsageRecordTestPool(t)
			h.usageRecordWorkerPool = pool
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(`{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
			key := &service.APIKey{ID: 9352, UserID: 9452, GroupID: &gid, Group: group, Status: service.StatusActive, User: &service.User{ID: 9452, Concurrency: 10, Balance: 100}}
			c.Set(string(middleware.ContextKeyAPIKey), key)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 10})
			h.Messages(c)
			pool.Stop()
			require.Contains(t, rec.Body.String(), "partial")
			require.Equal(t, "success-attempt", rec.Header().Get("X-Request-Id"))
			if mode == "comment_retry" {
				require.Equal(t, 2, upstream.calls)
				require.NotEqual(t, upstream.accounts[0], upstream.accounts[1])
				require.Contains(t, rec.Body.String(), ": ping\n\n")
				require.NotContains(t, rec.Body.String(), "failed-attempt")
				require.NotContains(t, rec.Body.String(), `"input_tokens":99`)
			} else {
				require.Equal(t, 1, upstream.calls)
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error\n"))
				require.Contains(t, rec.Body.String(), "overloaded_error")
			}
			select {
			case log := <-usage.created:
				require.Equal(t, 10, log.InputTokens)
				require.Equal(t, 15, log.OutputTokens)
				require.Equal(t, upstream.accounts[len(upstream.accounts)-1], log.AccountID)
			default:
				t.Fatal("missing real handler partial/success usage")
			}
			select {
			case extra := <-usage.created:
				t.Fatalf("duplicate usage: %+v", extra)
			default:
			}
		})
	}
}
