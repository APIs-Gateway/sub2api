//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// compatUsageLogRepository lets the capture embed the full repository
// interface while only implementing Create.
type compatUsageLogRepository = service.UsageLogRepository

type compatPartialUsageRepo struct {
	compatUsageLogRepository
	mu   sync.Mutex
	logs []service.UsageLog
}

func (r *compatPartialUsageRepo) Create(ctx context.Context, usage *service.UsageLog) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, *usage)
	return true, nil
}

type compatPartialBillingRepo struct {
	service.UsageBillingRepository
	commands []service.UsageBillingCommand
}

type compatPartialAccountRepo struct {
	service.AccountRepository
}

func (*compatPartialAccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}

func (r *compatPartialBillingRepo) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.commands = append(r.commands, *cmd)
	return &service.UsageBillingApplyResult{Applied: true, WalletDebit: &cmd.BalanceCost}, nil
}

type compatPartialUsageUpstream struct {
	payload   string
	cancel    context.CancelFunc
	calls     int
	failFirst bool
	accounts  []int64
}

func (u *compatPartialUsageUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (u *compatPartialUsageUpstream) DoWithTLS(_ *http.Request, _ string, accountID int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	u.accounts = append(u.accounts, accountID)
	if u.cancel != nil {
		u.cancel()
	}
	payload := u.payload
	if u.failFirst && u.calls == 1 {
		payload = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Unavailable\"}}\n\n"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {fmt.Sprintf("compat_usage_%d", u.calls)}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
}

const compatPartialMessageStartSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_partial\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-sonnet-4-5\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"

// Streams through the Chat Completions and Responses compatibility entries on
// an Anthropic account must persist the upstream-metered usage exactly once
// whether the stream completes, is truncated, or the client left first. The
// buffered/ variants serve a non-streaming client from the same upstream
// stream, which must never receive a truncated answer as a success.
func TestGatewayCompatibleHandlersPreservePartialUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"chat/completions", "responses"} {
		for _, ending := range []string{"complete", "truncated", "disconnected_truncated", "before_start", "failover",
			"write_failed_late_error", "write_failed_pre_stop_error",
			"buffered/complete", "buffered/truncated", "buffered/disconnected_truncated", "buffered/before_start", "buffered/failover"} {
			t.Run(endpoint+"/"+ending, func(t *testing.T) {
				clientStream := !strings.HasPrefix(ending, "buffered/")
				ending := strings.TrimPrefix(ending, "buffered/")
				groupID := int64(9200)
				group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
				account := &service.Account{
					ID: 9201, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true, Concurrency: 1,
					Credentials:   map[string]any{"api_key": "local-test", "pool_mode": true, "pool_mode_retry_count": 0},
					AccountGroups: []service.AccountGroup{{AccountID: 9201, GroupID: groupID}},
				}
				payload := compatPartialMessageStartSSE + "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":15}}\n\n"
				switch ending {
				case "complete", "failover":
					payload += "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				case "before_start":
					payload = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Unavailable\"}}\n\n"
				}
				if ending == "write_failed_late_error" {
					payload += "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				}
				if strings.HasPrefix(ending, "write_failed_") {
					payload += "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"fixture\"}}\n\n"
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				upstream := &compatPartialUsageUpstream{payload: payload}
				accounts := []*service.Account{account}
				if ending == "failover" {
					upstream.failFirst = true
					account.Credentials["pool_mode"] = true
					account.Credentials["pool_mode_retry_count"] = 0
					second := *account
					second.ID = 9204
					second.AccountGroups = []service.AccountGroup{{AccountID: second.ID, GroupID: groupID}}
					accounts = append(accounts, &second)
				}
				if ending == "disconnected_truncated" {
					upstream.cancel = cancel
				}
				usageRepo := &compatPartialUsageRepo{}
				cfg := &config.Config{}
				cfg.Default.RateMultiplier = 1
				userRepo := &openAIRecordUsageUserRepoStub795{user: service.User{ID: 9203, Balance: 100, Status: service.StatusActive}}
				billingRepo := &compatPartialBillingRepo{}
				billingCache := service.NewBillingCacheService(nil, userRepo, nil, nil, nil, nil, cfg, nil, nil)
				t.Cleanup(billingCache.Stop)
				accountRepo := &compatPartialAccountRepo{}
				snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: accounts}, nil, nil, nil, nil)
				gateway := service.NewGatewayService(
					accountRepo, &fakeGroupRepo{group: group}, usageRepo, billingRepo, userRepo, nil, nil, nil, cfg,
					snapshot, nil, service.NewBillingService(cfg, nil), service.NewRateLimitService(accountRepo, nil, cfg, nil, nil), billingCache, nil, upstream,
					&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				)
				pool := newUsageRecordTestPool(t)
				h := &GatewayHandler{
					maxAccountSwitches: 2,
					gatewayService:     gateway, billingCacheService: billingCache, cfg: cfg,
					concurrencyHelper:     NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
					usageRecordWorkerPool: pool,
				}
				apiKey := &service.APIKey{ID: 9202, UserID: 9203, GroupID: &groupID, Group: group, Status: service.StatusActive,
					User: &service.User{ID: 9203, Concurrency: 10, Balance: 100}}
				body := fmt.Sprintf(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}],"stream":%t}`, clientStream)
				if endpoint == "responses" {
					body = fmt.Sprintf(`{"model":"claude-sonnet-4-5","input":"hello","stream":%t}`, clientStream)
				}
				recorder := httptest.NewRecorder()
				var writeFailed *compatLiveFailedWriter
				var downstream http.ResponseWriter = recorder
				if strings.HasPrefix(ending, "write_failed_") {
					writeFailed = &compatLiveFailedWriter{ResponseRecorder: recorder}
					downstream = writeFailed
				}
				c, _ := gin.CreateTestContext(downstream)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, strings.NewReader(body)).WithContext(context.WithValue(ctx, ctxkey.Group, group))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Set(string(middleware.ContextKeyAPIKey), apiKey)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})
				if endpoint == "responses" {
					h.Responses(c)
				} else {
					h.ChatCompletions(c)
				}
				pool.Stop()
				if writeFailed != nil {
					require.NoError(t, ctx.Err(), "write failure fixture must leave request context live")
					require.True(t, writeFailed.failed)
					require.Zero(t, writeFailed.afterWrites)
					require.Zero(t, writeFailed.afterFlushes)
					require.NotContains(t, recorder.Body.String(), "[DONE]")
				}
				if ending == "failover" {
					require.Equal(t, 2, upstream.calls, "unmetered pre-output failure must switch to a successful account")
					require.NotEqual(t, upstream.accounts[0], upstream.accounts[1])
				} else {
					require.Equal(t, 1, upstream.calls, "no replay after a started or cancelled request")
				}
				if !clientStream {
					if ending == "complete" || ending == "failover" {
						require.Equal(t, http.StatusOK, recorder.Code)
						require.Contains(t, recorder.Body.String(), "partial")
					} else if ending == "before_start" {
						require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "provider overload retains native 529-to-503 policy")
						require.Contains(t, recorder.Body.String(), "overloaded_error")
					} else {
						require.Equal(t, http.StatusBadGateway, recorder.Code, "a truncated upstream must not answer a buffered client with success; response=%s", recorder.Body.String())
						require.NotContains(t, recorder.Body.String(), "partial")
					}
				}
				usageRepo.mu.Lock()
				defer usageRepo.mu.Unlock()
				if ending == "before_start" {
					require.Empty(t, usageRepo.logs, "an upstream failure before any usage must not be billed")
					require.Empty(t, billingRepo.commands)
					return
				}
				require.Len(t, usageRepo.logs, 1, "upstream-metered usage must be persisted exactly once; response=%s", recorder.Body.String())
				require.Equal(t, 10, usageRepo.logs[0].InputTokens)
				require.Equal(t, 15, usageRepo.logs[0].OutputTokens)
				require.Equal(t, apiKey.ID, usageRepo.logs[0].APIKeyID)
				require.Equal(t, upstream.accounts[len(upstream.accounts)-1], usageRepo.logs[0].AccountID)
				require.Len(t, billingRepo.commands, 1, "metered usage must reach atomic billing exactly once")
				require.Equal(t, 10, billingRepo.commands[0].InputTokens)
				require.Equal(t, 15, billingRepo.commands[0].OutputTokens)
				require.Equal(t, upstream.accounts[len(upstream.accounts)-1], billingRepo.commands[0].AccountID)
				require.Positive(t, billingRepo.commands[0].OfficialCost)
				require.Positive(t, billingRepo.commands[0].BalanceCost)
			})
		}
	}
}

type compatLiveFailedWriter struct {
	*httptest.ResponseRecorder
	failed       bool
	afterWrites  int
	afterFlushes int
}

func (w *compatLiveFailedWriter) Write(p []byte) (int, error) {
	if w.failed {
		w.afterWrites++
		return 0, io.ErrClosedPipe
	}
	w.failed = true
	return 0, io.ErrClosedPipe
}
func (w *compatLiveFailedWriter) Flush() {
	if w.failed {
		w.afterFlushes++
	}
	w.ResponseRecorder.Flush()
}
