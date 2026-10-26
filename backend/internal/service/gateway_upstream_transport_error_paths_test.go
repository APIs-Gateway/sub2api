//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// transportTempUnschedFailingRepoStub simulates a DB write failure when
// temporarily unscheduling an account after a durable transport fault.
type transportTempUnschedFailingRepoStub struct {
	AccountRepository
	calls int
}

func (r *transportTempUnschedFailingRepoStub) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.calls++
	return errors.New("db unavailable")
}

const (
	transportTestPersistentErr = "dial tcp 10.0.0.1:443: connect: connection refused"
	transportTestTransientErr  = `Post "https://upstream.test/v1/messages": EOF`
)

func requireGatewayTransportFailover(t *testing.T, err error, c *gin.Context, rec *httptest.ResponseRecorder) {
	t.Helper()
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, string(gatewayTransportFailoverBody), string(failoverErr.ResponseBody))
	require.False(t, failoverErr.RetryableOnSameAccount)
	// 传输层错误交给 handler 换号 / 耗尽渲染，service 不得写响应。
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestGatewayForward_TransportErrorFailsOverAndUnschedulesPersistentFault(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       string
		wantCalls int
	}{
		{name: "persistent", err: transportTestPersistentErr, wantCalls: 1},
		{name: "transient", err: transportTestTransientErr, wantCalls: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newPartialUsageTestContext(t)
			body := []byte(`{"model":"claude-3-5-sonnet-latest","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
			parsed := &ParsedRequest{
				Body:  NewRequestBodyRef(body),
				Model: "claude-3-5-sonnet-latest",
			}
			upstream := &anthropicHTTPUpstreamRecorder{err: errors.New(tc.err)}
			svc := newForwardPartialUsageServiceForTest(upstream)
			repo := &transportTempUnschedRepoStub{}
			svc.accountRepo = repo
			account := newAnthropicAPIKeyAccountForPartialUsageTest()

			result, err := svc.Forward(context.Background(), c, account, parsed)
			require.Nil(t, result)
			requireGatewayTransportFailover(t, err, c, rec)
			require.Equal(t, tc.wantCalls, repo.calls)
			if tc.wantCalls > 0 {
				require.Equal(t, account.ID, repo.lastID)
			}
		})
	}
}

func TestGatewayForwardAsCompat_TransportErrorFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)

	paths := []struct {
		name string
		path string
		body []byte
		call func(*GatewayService, context.Context, *gin.Context, *Account, []byte) (*ForwardResult, error)
	}{
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			body: []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`),
			call: func(svc *GatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
				return svc.ForwardAsChatCompletions(ctx, c, account, body, nil)
			},
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: []byte(`{"model":"claude-sonnet-4-5","input":"hello"}`),
			call: func(svc *GatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
				return svc.ForwardAsResponses(ctx, c, account, body, nil)
			},
		},
	}
	faults := []struct {
		name      string
		err       string
		wantCalls int
	}{
		{name: "persistent", err: transportTestPersistentErr, wantCalls: 1},
		{name: "transient", err: transportTestTransientErr, wantCalls: 0},
	}

	for _, p := range paths {
		for _, f := range faults {
			t.Run(p.name+"/"+f.name, func(t *testing.T) {
				upstream := &queuedHTTPUpstreamStub{errors: []error{errors.New(f.err)}}
				repo := &transportTempUnschedRepoStub{}
				svc := &GatewayService{
					cfg:                 &config.Config{},
					httpUpstream:        upstream,
					accountRepo:         repo,
					tlsFPProfileService: &TLSFingerprintProfileService{},
				}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, p.path, nil)
				account := &Account{
					ID:       321,
					Name:     "compat-transport",
					Platform: PlatformAnthropic,
					Type:     AccountTypeAPIKey,
					Credentials: map[string]any{
						"api_key": "test-key",
					},
				}

				result, err := p.call(svc, context.Background(), c, account, p.body)
				require.Nil(t, result)
				requireGatewayTransportFailover(t, err, c, rec)
				require.Equal(t, 1, upstream.callCount, "transport failure must not be retried on the same account")
				require.Equal(t, f.wantCalls, repo.calls)
			})
		}
	}
}

func TestGatewayBedrockUpstream_TransportErrorFailsOverAndUnschedules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	upstream := &queuedHTTPUpstreamStub{errors: []error{errors.New("lookup bedrock-runtime.us-east-1.amazonaws.com: no such host")}}
	repo := &transportTempUnschedRepoStub{}
	svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: repo}
	account := &Account{
		ID:       654,
		Name:     "bedrock-apikey",
		Platform: PlatformAnthropic,
		Type:     AccountTypeBedrock,
		Credentials: map[string]any{
			"auth_mode": "apikey",
			"api_key":   "bedrock-key",
		},
	}

	resp, err := svc.executeBedrockUpstream(context.Background(), c, account, []byte(`{"messages":[]}`),
		"anthropic.claude-3-5-sonnet-20241022-v2:0", "us-east-1", false, nil, "bedrock-key", "")
	require.Nil(t, resp)
	requireGatewayTransportFailover(t, err, c, rec)
	require.Equal(t, 1, upstream.callCount)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, account.ID, repo.lastID)
}

// TestHandleUpstreamTransportError_UnscheduleWriteFailureStillFailsOver pins
// that a DB failure while unscheduling does not change the client-facing
// contract: the request still fails over.
func TestHandleUpstreamTransportError_UnscheduleWriteFailureStillFailsOver(t *testing.T) {
	repo := &transportTempUnschedFailingRepoStub{}
	s := &GatewayService{accountRepo: repo}
	c := newTransportErrorTestGin(t)
	account := &Account{ID: 150, Name: "acc", Platform: PlatformAnthropic}

	err := s.handleUpstreamTransportError(context.Background(), c, account,
		errors.New(transportTestPersistentErr), OpsUpstreamErrorEvent{})

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, 1, repo.calls)
	require.False(t, c.Writer.Written())
}

// TestHandleUpstreamTransportError_NoAccountRepoStillFailsOver covers the
// defensive nil-repo guard in tempUnscheduleTransportError.
func TestHandleUpstreamTransportError_NoAccountRepoStillFailsOver(t *testing.T) {
	s := &GatewayService{}
	c := newTransportErrorTestGin(t)
	account := &Account{ID: 151, Name: "acc", Platform: PlatformAnthropic}

	err := s.handleUpstreamTransportError(context.Background(), c, account,
		errors.New(transportTestPersistentErr), OpsUpstreamErrorEvent{})

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
}

// TestHandleUpstreamTransportError_RequestDeadlineNoFailover pins that when the
// request context itself has expired, the deadline error is returned as-is
// (no account switch, no eviction) — the client budget is exhausted.
func TestHandleUpstreamTransportError_RequestDeadlineNoFailover(t *testing.T) {
	repo := &transportTempUnschedRepoStub{}
	s := &GatewayService{accountRepo: repo}
	c := newTransportErrorTestGin(t)
	account := &Account{ID: 152, Name: "acc", Platform: PlatformAnthropic}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	err := s.handleUpstreamTransportError(ctx, c, account, context.DeadlineExceeded, OpsUpstreamErrorEvent{})

	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "expired request context must not fail over")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 0, repo.calls)
}
