//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/config"
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
	reader  *strings.Reader
	delay   bool
	closed  chan struct{}
	once    sync.Once
	readErr error
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
	n, err := b.reader.Read(p)
	if err == io.EOF && b.readErr != nil {
		return n, b.readErr
	}
	return n, err
}
func (b *visibleRetryDelayedBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

type visibleRetryAccountRepo struct {
	compatPartialAccountRepo
	overloaded []int64
}

func (r *visibleRetryAccountRepo) SetOverloaded(_ context.Context, id int64, _ time.Time) error {
	r.overloaded = append(r.overloaded, id)
	return nil
}

type visibleRetryUpstream struct {
	calls       int
	accounts    []int64
	mode        string
	delayedBody *visibleRetryDelayedBody
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
		payload = "event: message_start\ndata: " + `{"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\nevent: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
		header = "failed-attempt"
	}
	if strings.HasPrefix(u.mode, "metered_") {
		payload = "event: message_start\ndata: " + `{"type":"message_start","message":{"usage":{"input_tokens":99}}}` + "\n\nevent: message_delta\ndata: " + `{"type":"message_delta","usage":{"output_tokens":88}}` + "\n\n"
		header = "metered-attempt"
	}
	if u.mode == "committed_error" || u.mode == "metered_sse_error" {
		payload += "event: error\ndata: " + `{"type":"error","error":{"type":"overloaded_error","message":"fixture"}}` + "\n\n"
	} else {
		payload += "event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
	}
	if strings.HasPrefix(u.mode, "comment_write_failed_") {
		payload = "event: message_start\ndata: " + `{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\nevent: message_delta\ndata: " + `{"type":"message_delta","usage":{"output_tokens":15}}` + "\n\n"
	}
	var body io.ReadCloser = io.NopCloser(strings.NewReader(payload))
	if (u.mode == "comment_retry" && u.calls == 1) || u.mode == "metered_empty" || strings.HasPrefix(u.mode, "comment_write_failed_") {
		u.delayedBody = &visibleRetryDelayedBody{reader: strings.NewReader(payload), delay: true, closed: make(chan struct{})}
		if u.mode == "comment_write_failed_read_error" {
			u.delayedBody.readErr = io.ErrUnexpectedEOF
		}
		body = u.delayedBody
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{header}}, Body: body}, nil
}
func TestGatewayVisibleRetry_RealMessagesHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"comment_retry", "committed_error", "metered_empty", "metered_sse_error", "comment_write_failed_eof", "comment_write_failed_read_error"} {
		t.Run(mode, func(t *testing.T) {
			gid := int64(9152)
			group := &service.Group{ID: gid, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
			accounts := []*service.Account{}
			for _, id := range []int64{9252, 9253} {
				accounts = append(accounts, &service.Account{ID: id, Name: "native-visible", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "local", "pool_mode": true, "pool_mode_retry_count": 0}, Concurrency: 1, Priority: 1, Status: service.StatusActive, Schedulable: true, AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: gid}}})
			}
			upstream := &visibleRetryUpstream{mode: mode}
			usage := &partialUsageBillingUsageLogRepo{created: make(chan *service.UsageLog, 4)}
			cfg := &config.Config{}
			cfg.Default.RateMultiplier = 1
			userRepo := &openAIRecordUsageUserRepoStub795{user: service.User{ID: 9452, Balance: 100, Status: service.StatusActive}}
			billingRepo := &compatPartialBillingRepo{}
			billingCache := service.NewBillingCacheService(nil, userRepo, nil, nil, nil, nil, cfg, nil, nil)
			t.Cleanup(billingCache.Stop)
			accountRepo := &visibleRetryAccountRepo{}
			snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: accounts}, nil, nil, nil, nil)
			gateway := service.NewGatewayService(
				accountRepo, &fakeGroupRepo{group: group}, usage, billingRepo, userRepo, nil, nil, &forceCacheBillingGatewayCache{accountID: 9252}, cfg,
				snapshot, nil, service.NewBillingService(cfg, nil), service.NewRateLimitService(accountRepo, nil, cfg, nil, nil), billingCache, nil, upstream,
				&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			)
			h := &GatewayHandler{maxAccountSwitches: 1, gatewayService: gateway, billingCacheService: billingCache, cfg: cfg,
				concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0)}
			h.cfg.Gateway.StreamKeepaliveInterval = 1
			h.cfg.Gateway.StreamDataIntervalTimeout = 3
			pool := newUsageRecordTestPool(t)
			h.usageRecordWorkerPool = pool
			rec := httptest.NewRecorder()
			var failedWriter *compatLiveFailedWriter
			var downstream http.ResponseWriter = rec
			if strings.HasPrefix(mode, "comment_write_failed_") {
				failedWriter = &compatLiveFailedWriter{ResponseRecorder: rec}
				downstream = failedWriter
			}
			c, _ := gin.CreateTestContext(downstream)
			c.Request = httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(`{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
			key := &service.APIKey{ID: 9352, UserID: 9452, GroupID: &gid, Group: group, Status: service.StatusActive, User: &service.User{ID: 9452, Concurrency: 10, Balance: 100}}
			c.Set(string(middleware.ContextKeyAPIKey), key)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 10})
			h.Messages(c)
			pool.Stop()
			require.Empty(t, accountRepo.overloaded, "preserve fork pool-mode policy: provider 529 must not cool down this account")
			if mode == "metered_sse_error" {
				events, exists := c.Get(service.OpsUpstreamErrorsKey)
				require.True(t, exists)
				ops, ok := events.([]*service.OpsUpstreamErrorEvent)
				require.True(t, ok)
				require.Len(t, ops, 1)
				require.Equal(t, 529, ops[0].UpstreamStatusCode, "retain provider overload attribution even when pool policy skips cooldown")
			}
			if failedWriter != nil {
				require.NoError(t, c.Request.Context().Err(), "write failure must not cancel request context to hide generic fallback")
				require.True(t, failedWriter.failed, "delayed upstream must force pre-visible comment write failure")
				require.Equal(t, 1, upstream.calls, "failed comment must not replay or switch accounts")
				require.Zero(t, failedWriter.afterWrites)
				require.Zero(t, failedWriter.afterFlushes)
				require.Empty(t, rec.Body.String())
				require.Empty(t, rec.Header().Get("X-Request-Id"))
				select {
				case <-upstream.delayedBody.closed:
				default:
					t.Fatal("failed-write upstream body not closed")
				}
			} else if strings.HasPrefix(mode, "metered_") {
				require.Equal(t, 1, upstream.calls, "upstream-metered empty response must not replay or lose its cost")
				require.NotContains(t, rec.Body.String(), "partial")
				require.Contains(t, rec.Body.String(), "error")
				require.Empty(t, rec.Header().Get("X-Request-Id"))
				if mode == "metered_sse_error" {
					require.Contains(t, rec.Body.String(), "error")
				}
			} else {
				require.Contains(t, rec.Body.String(), "partial")
				require.Equal(t, "success-attempt", rec.Header().Get("X-Request-Id"))
			}
			if mode == "comment_retry" {
				require.Equal(t, 2, upstream.calls)
				require.NotEqual(t, upstream.accounts[0], upstream.accounts[1])
				require.Contains(t, rec.Body.String(), ": ping\n\n")
				require.NotContains(t, rec.Body.String(), "failed-attempt")
				require.NotContains(t, rec.Body.String(), `"input_tokens":99`)
			} else if mode == "committed_error" {
				require.Equal(t, 1, upstream.calls)
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error\n"))
				require.Contains(t, rec.Body.String(), "overloaded_error")
				errorFrames := 0
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if !strings.HasPrefix(line, "data:") {
						continue
					}
					var frame struct {
						Type  string          `json:"type"`
						Error json.RawMessage `json:"error"`
					}
					require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &frame))
					if frame.Type == "error" || len(frame.Error) > 0 {
						errorFrames++
					}
				}
				require.Equal(t, 1, errorFrames, "count tagged and untagged SSE error payloads; generic handler fallback must not append a second error")
				require.NotContains(t, rec.Body.String(), "Upstream request failed")
			}
			select {
			case log := <-usage.created:
				if mode == "comment_retry" {
					require.Zero(t, log.InputTokens)
					require.Equal(t, 10, log.CacheReadTokens, "preserve fork sticky-failover force-cache billing")
				} else if strings.HasPrefix(mode, "metered_") {
					require.Equal(t, 99, log.InputTokens)
					require.Equal(t, 88, log.OutputTokens)
					require.Zero(t, log.CacheReadTokens)
				} else {
					require.Equal(t, 10, log.InputTokens)
					require.Zero(t, log.CacheReadTokens)
				}
				if !strings.HasPrefix(mode, "metered_") {
					require.Equal(t, 15, log.OutputTokens)
				}
				require.Equal(t, upstream.accounts[len(upstream.accounts)-1], log.AccountID)
			default:
				t.Fatal("missing real handler partial/success usage")
			}
			require.Len(t, billingRepo.commands, 1, "normal mode must atomically bill exactly the successful/committed attempt once")
			cmd := billingRepo.commands[0]
			require.Equal(t, key.ID, cmd.APIKeyID)
			require.Equal(t, upstream.accounts[len(upstream.accounts)-1], cmd.AccountID)
			if strings.HasPrefix(mode, "metered_") {
				require.Equal(t, 99, cmd.InputTokens)
				require.Equal(t, 88, cmd.OutputTokens)
				require.Zero(t, cmd.CacheReadTokens)
			} else {
				require.Equal(t, 15, cmd.OutputTokens)
			}
			if mode == "comment_retry" {
				require.Zero(t, cmd.InputTokens)
				require.Equal(t, 10, cmd.CacheReadTokens)
			} else if !strings.HasPrefix(mode, "metered_") {
				require.Equal(t, 10, cmd.InputTokens)
				require.Zero(t, cmd.CacheReadTokens)
			}
			require.Positive(t, cmd.OfficialCost)
			require.Positive(t, cmd.BalanceCost)
			select {
			case extra := <-usage.created:
				t.Fatalf("duplicate usage: %+v", extra)
			default:
			}
		})
	}
}
