//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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

// 本文件覆盖 issue #764：GatewayHandler.Messages() 在 Forward 中途出错但已经
// 探测到部分 usage 时，必须照常提交计费记录；同时 failover 重试成功后不得对
// 已失败的前序尝试重复计费。

// partialUsageBillingUsageLogRepo 记录每次 usageLogRepo.Create 调用，用于统计
// 计费记录被提交的次数（验证不重复计费）以及校验提交内容。
type partialUsageBillingUsageLogRepo struct {
	service.UsageLogRepository
	created chan *service.UsageLog
}

func (s *partialUsageBillingUsageLogRepo) Create(ctx context.Context, log *service.UsageLog) (bool, error) {
	if s.created != nil {
		s.created <- log
	}
	return true, nil
}

// partialUsageStreamBody 模拟一个流式响应体：先吐出一段已携带 usage 的 SSE
// 事件，随后在下一次 Read 时返回错误（模拟连接中断/newapi 类上游缺失 terminal
// 事件）。
type partialUsageStreamBody struct {
	payload []byte
	sent    bool
	err     error
}

func (r *partialUsageStreamBody) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		n := copy(p, r.payload)
		return n, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	return 0, io.EOF
}

func (r *partialUsageStreamBody) Close() error { return nil }

type partialUsageBillingUpstream struct {
	resp *http.Response
}

func (u *partialUsageBillingUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.resp, nil
}

func (u *partialUsageBillingUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, "", 0, 0)
}

func newPartialUsageBillingGatewayHandler(t *testing.T, group *service.Group, accounts []*service.Account, upstream service.HTTPUpstream, cache service.GatewayCache, usageLogRepo service.UsageLogRepository) (*GatewayHandler, func()) {
	t.Helper()

	schedulerCache := &fakeSchedulerCache{accounts: accounts}
	schedulerSnapshot := service.NewSchedulerSnapshotService(schedulerCache, nil, nil, nil, nil)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	accountRepo := &forceCacheBillingAccountRepo{}

	gwSvc := service.NewGatewayService(
		accountRepo,
		&fakeGroupRepo{group: group},
		usageLogRepo,
		nil,
		nil,
		nil,
		nil,
		cache,
		cfg,
		schedulerSnapshot,
		nil,
		nil,
		service.NewRateLimitService(accountRepo, nil, cfg, nil, nil),
		nil,
		nil,
		upstream,
		&service.DeferredService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	h := &GatewayHandler{
		gatewayService:           gwSvc,
		billingCacheService:      billingCacheSvc,
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
		maxAccountSwitches:       1,
		maxAccountSwitchesGemini: 1,
		cfg:                      cfg,
	}

	return h, func() { billingCacheSvc.Stop() }
}

// TestGatewayHandlerMessages_StreamReadErrorRecordsPartialUsage 验证：流式转发
// 中途出错（已写出 message_start 后连接中断，非 failover 类错误）时，handler
// 必须照常提交已探测到的 usage，而不是随错误一起整体丢弃（issue #764）。
func TestGatewayHandlerMessages_StreamReadErrorRecordsPartialUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(9151)
	accountID := int64(9251)
	group := &service.Group{
		ID:       groupID,
		Hydrated: true,
		Platform: service.PlatformAnthropic,
		Status:   service.StatusActive,
	}
	account := &service.Account{
		ID:       accountID,
		Name:     "anthropic-partial-usage",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-key",
		},
		Extra:       map[string]any{"anthropic_passthrough": true},
		Concurrency: 1,
		Priority:    1,
		Status:      service.StatusActive,
		Schedulable: true,
		AccountGroups: []service.AccountGroup{{
			AccountID: accountID,
			GroupID:   groupID,
		}},
	}

	upstream := &partialUsageBillingUpstream{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-handler-partial"}},
			Body: &partialUsageStreamBody{
				payload: []byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":13,\"output_tokens\":2}}}\n\n"),
				err:     io.ErrUnexpectedEOF,
			},
		},
	}
	usageRepo := &partialUsageBillingUsageLogRepo{created: make(chan *service.UsageLog, 1)}
	cache := &forceCacheBillingGatewayCache{accountID: accountID}
	h, cleanup := newPartialUsageBillingGatewayHandler(t, group, []*service.Account{account}, upstream, cache, usageRepo)
	defer cleanup()

	body := []byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
	c.Request = req

	apiKey := &service.APIKey{
		ID:      9351,
		UserID:  9451,
		GroupID: &groupID,
		Status:  service.StatusActive,
		User: &service.User{
			ID:          9451,
			Concurrency: 10,
			Balance:     100,
		},
		Group: group,
	}
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

	h.Messages(c)

	select {
	case usageLog := <-usageRepo.created:
		require.NotNil(t, usageLog)
		require.Equal(t, 13, usageLog.InputTokens)
		require.Equal(t, 2, usageLog.OutputTokens)
	case <-time.After(3 * time.Second):
		t.Fatal("等待 partial usage 写入超时——流式错误路径未提交已探测到的 usage")
	}
}

// TestGatewayHandlerMessages_FailoverRetrySuccessDoesNotDoubleRecordUsage 验证：
// 第一个账号在未写出任何字节前失败（走 UpstreamFailoverError 换号重试），
// 第二个账号成功后，usage 只能被提交一次——第一次失败的尝试不得重复计费。
func TestGatewayHandlerMessages_FailoverRetrySuccessDoesNotDoubleRecordUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(9152)
	firstAccountID := int64(9252)
	secondAccountID := int64(9253)
	group := &service.Group{
		ID:       groupID,
		Hydrated: true,
		Platform: service.PlatformAnthropic,
		Status:   service.StatusActive,
	}
	account := func(id int64) *service.Account {
		return &service.Account{
			ID:       id,
			Name:     "anthropic-failover",
			Platform: service.PlatformAnthropic,
			Type:     service.AccountTypeAPIKey,
			Credentials: map[string]any{
				"api_key":   "test-key",
				"pool_mode": true,
			},
			Extra:       map[string]any{"anthropic_passthrough": true},
			Concurrency: 1,
			Priority:    1,
			Status:      service.StatusActive,
			Schedulable: true,
			AccountGroups: []service.AccountGroup{{
				AccountID: id,
				GroupID:   groupID,
			}},
		}
	}
	upstream := &forceCacheBillingUpstream{
		firstStatus: http.StatusInternalServerError,
		successBody: `{"id":"msg_ok","type":"message","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":3}}`,
	}
	usageRepo := &partialUsageBillingUsageLogRepo{created: make(chan *service.UsageLog, 4)}
	cache := &forceCacheBillingGatewayCache{accountID: firstAccountID}
	h, cleanup := newPartialUsageBillingGatewayHandler(t, group, []*service.Account{account(firstAccountID), account(secondAccountID)}, upstream, cache, usageRepo)
	defer cleanup()

	body := []byte(`{"model":"claude-sonnet-4-5","stream":false,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
	c.Request = req

	apiKey := &service.APIKey{
		ID:      9352,
		UserID:  9452,
		GroupID: &groupID,
		Status:  service.StatusActive,
		User: &service.User{
			ID:          9452,
			Concurrency: 10,
			Balance:     100,
		},
		Group: group,
	}
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

	h.Messages(c)

	require.Equal(t, http.StatusOK, rec.Code)

	select {
	case usageLog := <-usageRepo.created:
		require.NotNil(t, usageLog)
	case <-time.After(3 * time.Second):
		t.Fatal("等待成功请求的 usage 写入超时")
	}

	upstream.mu.Lock()
	callNum := upstream.callNum
	upstream.mu.Unlock()
	require.Equal(t, 2, callNum, "应先失败一次再在第二个账号上成功")

	select {
	case extra := <-usageRepo.created:
		t.Fatalf("失败的第一次尝试不应再提交一次 usage 记录，但收到了额外记录: %+v", extra)
	case <-time.After(200 * time.Millisecond):
		// 期望：没有额外记录。
	}
}
