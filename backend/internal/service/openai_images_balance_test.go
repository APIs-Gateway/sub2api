//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIImagesInsufficientBalanceRequiresStructuredError(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"errorKey":"insufficient_balance"}`, true},
		{`{"error":{"cause":{"errorKey":"insufficient_balance"}}}`, true},
		{`{"error":{"details":[{"code":"insufficient_balance"}]}}`, true},
		{`{"error":{"inner_error":{"error_key":" INSUFFICIENT_BALANCE. "}}}`, true},
		{`{"error":{"message":"Insufficient credit balance"}}`, false},
		{`{"error":{"message":"{\"errorKey\":\"insufficient_balance\"}"}}`, false},
		{`{"request":{"prompt":"draw the words insufficient_balance"}}`, false},
		{`{"request":{"errorKey":"insufficient_balance"}}`, false},
		{`{"message":"insufficient_balance"}`, false},
		{`upstream says insufficient_balance`, false},
		{`{"error":{"code":"server_error"}}`, false},
	} {
		require.Equal(t, tc.want, isOpenAIImagesInsufficientBalance([]byte(tc.body)), tc.body)
	}
}

func newOpenAIImagesBalanceTestContext(path string, body []byte) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func newOpenAIImagesBalanceTestService(responseBody string) (*OpenAIGatewayService, *httpUpstreamRecorder, *openAIImagesCooldownAccountRepoStub) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadGateway,
		Header: http.Header{
			"X-Request-Id": []string{"req-image-balance"},
			"Retry-After":  []string{"23"},
		},
		Body: io.NopCloser(bytes.NewBufferString(responseBody)),
	}}
	repo := &openAIImagesCooldownAccountRepoStub{}
	return &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: repo}, upstream, repo
}

func openAIImagesBalanceTestAccount() *Account {
	return &Account{
		ID: 7, Name: "image-account", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://images.example.test/v1"},
	}
}

func TestForwardOpenAIImagesAPIKey_BalanceFailsOverAndCoolsOnlyImages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name     string
		endpoint string
		stream   bool
	}{
		{name: "nonstream_generation", endpoint: openAIImagesGenerationsEndpoint},
		{name: "stream_edit", endpoint: openAIImagesEditsEndpoint, stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"cat"}`)
			svc, upstream, repo := newOpenAIImagesBalanceTestService(`{"error":{"message":"gateway failed","cause":{"errorKey":"insufficient_balance"}}}`)
			c := newOpenAIImagesBalanceTestContext("/v1/images/"+tc.endpoint, body)
			account := openAIImagesBalanceTestAccount()
			parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Endpoint: tc.endpoint, ContentType: "application/json", Stream: tc.stream, N: 1}
			before := time.Now()

			result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")

			require.Nil(t, result)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.True(t, failover.OpenAIImagesInsufficientBalance)
			require.Equal(t, http.StatusBadGateway, failover.StatusCode)
			require.False(t, failover.RetryableOnSameAccount)
			require.Equal(t, "23", failover.ResponseHeaders.Get("Retry-After"))
			require.Len(t, upstream.requests, 1)
			require.Len(t, repo.calls, 1)
			require.Equal(t, account.ID, repo.calls[0].accountID)
			require.Equal(t, openAIImageGenerationRateLimitKey, repo.calls[0].scope)
			require.Equal(t, openAIImagesBalanceRateLimitReason, repo.calls[0].reason)
			require.WithinDuration(t, before.Add(openAIImagesBalanceCooldown), repo.calls[0].resetAt, 5*time.Second)
			require.False(t, c.Writer.Written(), "the handler owns the exhausted response")
			events := upstreamErrorEventsFromContext(t, c)
			require.Len(t, events, 1)
			require.Equal(t, "failover", events[0].Kind)
			require.Equal(t, account.ID, events[0].AccountID)
			require.Equal(t, OpenAIImagesInsufficientBalanceMessage, events[0].Message)

			// The persisted scope is consumed only for image intent, leaving text
			// traffic on this same account schedulable.
			account.Extra = map[string]any{modelRateLimitsKey: map[string]any{
				repo.calls[0].scope: map[string]any{"rate_limit_reset_at": repo.calls[0].resetAt.Format(time.RFC3339)},
			}}
			require.True(t, account.isModelRateLimitedWithContext(WithOpenAIImageGenerationIntent(context.Background()), "gpt-5.5"))
			require.False(t, account.isModelRateLimitedWithContext(context.Background(), "gpt-5.5"))
		})
	}
}

func TestForwardOpenAIImagesAPIKey_BalanceCooldownWriteFailureStillFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"cat"}`)
	svc, _, repo := newOpenAIImagesBalanceTestService(`{"errorKey":"insufficient_balance"}`)
	repo.err = errors.New("database unavailable")
	c := newOpenAIImagesBalanceTestContext("/v1/images/generations", body)

	result, err := svc.ForwardImages(context.Background(), c, openAIImagesBalanceTestAccount(), body,
		&OpenAIImagesRequest{Model: "gpt-image-2", Endpoint: openAIImagesGenerationsEndpoint, ContentType: "application/json", N: 1}, "")

	require.Nil(t, result)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.True(t, failover.OpenAIImagesInsufficientBalance)
	require.Len(t, repo.calls, 1)
}

func TestForwardOpenAIImagesAPIKey_CanceledClientDoesNotLogOrCoolBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"cat"}`)
	svc, upstream, repo := newOpenAIImagesBalanceTestService(`{"errorKey":"insufficient_balance"}`)
	c := newOpenAIImagesBalanceTestContext("/v1/images/generations", body)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := svc.ForwardImages(ctx, c, openAIImagesBalanceTestAccount(), body,
		&OpenAIImagesRequest{Model: "gpt-image-2", Endpoint: openAIImagesGenerationsEndpoint, ContentType: "application/json", N: 1}, "")

	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, upstream.requests, 1)
	require.Empty(t, repo.calls)
	require.Empty(t, upstreamErrorEventsFromContext(t, c))
}

func TestForwardOpenAIImagesAPIKey_Unstructured502KeepsExistingFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"cat"}`)
	svc, _, repo := newOpenAIImagesBalanceTestService(`{"error":{"message":"upstream request failed; prompt said insufficient_balance"}}`)
	c := newOpenAIImagesBalanceTestContext("/v1/images/generations", body)

	result, err := svc.ForwardImages(context.Background(), c, openAIImagesBalanceTestAccount(), body,
		&OpenAIImagesRequest{Model: "gpt-image-2", Endpoint: openAIImagesGenerationsEndpoint, ContentType: "application/json", N: 1}, "")

	require.Nil(t, result)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.False(t, failover.OpenAIImagesInsufficientBalance)
	require.Empty(t, repo.calls)
}
