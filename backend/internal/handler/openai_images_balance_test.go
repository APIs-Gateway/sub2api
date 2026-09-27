//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIImagesBalanceSwitchAccountRepo struct {
	openAIImagesFailoverAccountRepo
	cooled []int64
}

func (r *openAIImagesBalanceSwitchAccountRepo) SetModelRateLimit(_ context.Context, accountID int64, _ string, _ time.Time, _ ...string) error {
	r.cooled = append(r.cooled, accountID)
	return nil
}

type openAIImagesBalanceSwitchUpstream struct {
	service.HTTPUpstream
	mu    sync.Mutex
	calls []int64
}

func (u *openAIImagesBalanceSwitchUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.calls = append(u.calls, accountID)
	u.mu.Unlock()
	if accountID == 8801 {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"cause":{"errorKey":"insufficient_balance"}}}`)),
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"created":1,"data":[{"b64_json":"YQ=="}]}`)),
	}, nil
}

func TestOpenAIImagesBalanceSwitchesToSecondAccountAndSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(8800)
	repo := &openAIImagesBalanceSwitchAccountRepo{openAIImagesFailoverAccountRepo: openAIImagesFailoverAccountRepo{accounts: []service.Account{
		{ID: 8801, Name: "empty-image-account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 0, Credentials: map[string]any{"api_key": "test-key-1", "base_url": "https://images.example.test/v1"}},
		{ID: 8802, Name: "funded-image-account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{"api_key": "test-key-2", "base_url": "https://images.example.test/v1"}},
	}}}
	upstream := &openAIImagesBalanceSwitchUpstream{}
	cfg := &config.Config{RunMode: config.RunModeSimple, Gateway: config.GatewayConfig{DisableOpenAIImagesStreaming: true}}
	gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billing.Stop)
	handler := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	handler.maxAccountSwitches = 10

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{"model":"gpt-image-2","prompt":"draw a cat","stream":false}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 8899, GroupID: &groupID, Group: &service.Group{ID: groupID, AllowImageGeneration: true}, User: &service.User{ID: 8898}})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 8898})

	handler.Images(c)

	require.Equal(t, []int64{8801, 8802}, upstream.calls)
	require.Equal(t, []int64{8801}, repo.cooled)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "YQ==", gjson.GetBytes(rec.Body.Bytes(), "data.0.b64_json").String())
	rawEvents, ok := c.Get(service.OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*service.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "failover", events[0].Kind)
	require.Equal(t, int64(8801), events[0].AccountID)
}

func newOpenAIImagesBalanceHandlerContext(path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c, recorder
}

func openAIImagesBalanceFailover() *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:                      http.StatusBadGateway,
		ResponseBody:                    []byte(`{"error":{"cause":{"errorKey":"insufficient_balance"}}}`),
		ResponseHeaders:                 http.Header{"Retry-After": []string{"23"}},
		OpenAIImagesInsufficientBalance: true,
	}
}

func requireOpenAIImagesBalanceJSON(t *testing.T, body []byte) {
	t.Helper()
	var payload map[string]map[string]any
	require.NoError(t, json.Unmarshal(body, &payload), "one JSON document must follow any keepalive whitespace")
	require.Equal(t, service.OpenAIImagesInsufficientBalanceCode, payload["error"]["code"])
	require.Equal(t, service.OpenAIImagesInsufficientBalanceMessage, payload["error"]["message"])
}

func TestOpenAIImagesBalanceExhaustionBeforeHeadersReturns402(t *testing.T) {
	for _, path := range []string{"/v1/images/generations", "/v1/images/edits"} {
		t.Run(path, func(t *testing.T) {
			c, rec := newOpenAIImagesBalanceHandlerContext(path)

			(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, openAIImagesBalanceFailover(), false)

			require.Equal(t, http.StatusPaymentRequired, rec.Code)
			require.Equal(t, "23", rec.Result().Header.Get("Retry-After"))
			requireOpenAIImagesBalanceJSON(t, rec.Body.Bytes())
		})
	}
}

func TestOpenAIImagesBalanceExhaustionAfterJSONKeepaliveKeepsOneDocument(t *testing.T) {
	c, rec := newOpenAIImagesBalanceHandlerContext("/v1/images/generations")
	stop := service.StartOpenAIImagesJSONKeepalive(c, time.Millisecond)
	defer stop()
	require.Eventually(t, c.Writer.Written, time.Second, time.Millisecond)
	require.Equal(t, http.StatusOK, rec.Code)

	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, openAIImagesBalanceFailover(), false)
	stop()
	body := rec.Body.String()

	require.Equal(t, http.StatusOK, rec.Code, "an already-committed status cannot be replaced")
	require.True(t, strings.HasPrefix(body, " \n"))
	requireOpenAIImagesBalanceJSON(t, []byte(body))
	time.Sleep(5 * time.Millisecond)
	require.Equal(t, body, rec.Body.String(), "heartbeats must stop before the JSON error is written")
}

func TestOpenAIImagesBalanceExhaustionAfterStreamCommitWritesSSEError(t *testing.T) {
	c, rec := newOpenAIImagesBalanceHandlerContext("/v1/images/edits")
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.WriteHeaderNow()
	_, err := c.Writer.Write([]byte("data: {\"type\":\"image.partial\"}\n\n"))
	require.NoError(t, err)

	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, openAIImagesBalanceFailover(), true)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Result().Header.Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "event: error\n")
	require.Contains(t, rec.Body.String(), `"code":"insufficient_balance"`)
	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error\n"))
}

func TestOpenAIImagesBalanceMarkerDoesNotChangeOther5xx(t *testing.T) {
	c, rec := newOpenAIImagesBalanceHandlerContext("/v1/images/generations")
	failover := &service.UpstreamFailoverError{
		StatusCode:   http.StatusBadGateway,
		ResponseBody: []byte(`{"error":{"message":"insufficient_balance in an echoed prompt"}}`),
	}

	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, failover, false)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.NotContains(t, rec.Body.String(), `"code":"insufficient_balance"`)
}
