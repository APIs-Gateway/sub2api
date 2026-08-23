//go:build unit

package handler

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// openAIStreamFailedRateLimitUpstream 对每次上游调用都返回 HTTP 200 SSE，流内携带
// event: response.failed + rate_limit_exceeded（复现 review finding f1 的场景：
// 上游用 HTTP 200 承载限流语义，而非真实 HTTP 429）。
type openAIStreamFailedRateLimitUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
}

func (u *openAIStreamFailedRateLimitUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.mu.Unlock()
	body := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		"",
		"event: response.failed",
		`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"rate_limit_exceeded","message":"Concurrency limit exceeded for account, please retry later"}}}`,
		"",
	}, "\n")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (u *openAIStreamFailedRateLimitUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

// TestOpenAIGatewayHandlerResponses_StreamFailedRateLimitSwitchesAccount 覆盖
// review finding f1：newOpenAIStreamFailoverError 把流内 response.failed 限流
// 事件提升为 StatusCode=429 后，handler 的 ShouldSwitchAccountOn429 闸门
// （fork 独有，upstream 没有）此前只认真实 HTTP 429，导致该 429 被当成已耗尽、
// 第一个账号就直接把 429 返回给客户端。修复后应像其它可切号错误一样继续
// 尝试下一个账号。
func TestOpenAIGatewayHandlerResponses_StreamFailedRateLimitSwitchesAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	accounts := []service.Account{
		{
			ID:          1,
			Name:        "oauth-account-1",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Status:      service.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"access_token": "token-1"},
		},
		{
			ID:          2,
			Name:        "oauth-account-2",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Status:      service.StatusActive,
			Schedulable: true,
			Priority:    1,
			Credentials: map[string]any{"access_token": "token-2"},
		},
	}
	accountRepo := openAIImagesFailoverAccountRepo{accounts: accounts}
	upstream := &openAIStreamFailedRateLimitUpstream{}
	handler := newOpenAIResponsesFailoverTestHandlerWithRepo(t, upstream, accountRepo)
	c, rec := newOpenAIFailoverTestContext(t, nil, "/v1/responses", `{"model":"gpt-5.1","stream":true,"input":"hello"}`, false)

	handler.Responses(c)

	require.Equal(t, []int64{1, 2}, upstream.calls(), "流内限流事件应像真实 429 一样触发切号，而不是在账号 1 上直接判定耗尽")
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "两个账号都被流内限流后应按 429 耗尽返回")
}
