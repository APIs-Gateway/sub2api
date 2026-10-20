package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// contentModerationProxyRepoStub is a minimal ProxyRepository test double for content
// moderation proxy resolution tests. Only GetByID is implemented; every other method is
// satisfied via the embedded (nil) interface and would panic if accidentally invoked,
// which is fine since none of the resolution/config paths under test call them.
type contentModerationProxyRepoStub struct {
	ProxyRepository
	getByIDFunc func(ctx context.Context, id int64) (*Proxy, error)
	calls       atomic.Int32
}

func (s *contentModerationProxyRepoStub) GetByID(ctx context.Context, id int64) (*Proxy, error) {
	s.calls.Add(1)
	if s.getByIDFunc != nil {
		return s.getByIDFunc(ctx, id)
	}
	return nil, errors.New("proxy not found")
}

func TestContentModerationService_ResolveModerationHTTPClient_NoProxyConfigured(t *testing.T) {
	svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil, nil)

	client := svc.resolveModerationHTTPClient(context.Background(), nil)
	require.Same(t, svc.httpClient, client, "nil proxy id must use the direct client")

	zero := int64(0)
	client = svc.resolveModerationHTTPClient(context.Background(), &zero)
	require.Same(t, svc.httpClient, client, "proxy id <= 0 must use the direct client")

	neg := int64(-5)
	client = svc.resolveModerationHTTPClient(context.Background(), &neg)
	require.Same(t, svc.httpClient, client, "negative proxy id must use the direct client")
}

func TestContentModerationService_ResolveModerationHTTPClient_NilProxyRepo(t *testing.T) {
	svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil, nil)
	id := int64(7)
	client := svc.resolveModerationHTTPClient(context.Background(), &id)
	require.Same(t, svc.httpClient, client, "missing proxyRepo must fall back to the direct client")
}

func TestContentModerationService_ResolveModerationHTTPClient_ResolvesAndCaches(t *testing.T) {
	proxyRepo := &contentModerationProxyRepoStub{
		getByIDFunc: func(ctx context.Context, id int64) (*Proxy, error) {
			return &Proxy{ID: id, Protocol: "http", Host: "127.0.0.1", Port: 38080}, nil
		},
	}
	svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil, proxyRepo)
	id := int64(9)

	client := svc.resolveModerationHTTPClient(context.Background(), &id)
	require.NotSame(t, svc.httpClient, client)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok, "proxy-backed client must use *http.Transport")
	require.NotNil(t, transport.Proxy)

	target, err := url.Parse("https://api.openai.com/v1/moderations")
	require.NoError(t, err)
	proxyURL, err := transport.Proxy(&http.Request{URL: target})
	require.NoError(t, err)
	require.Equal(t, "http", proxyURL.Scheme)
	require.Equal(t, "127.0.0.1:38080", proxyURL.Host)

	// A second resolution within the cache TTL must reuse the cached client instead of
	// hitting proxyRepo again.
	client2 := svc.resolveModerationHTTPClient(context.Background(), &id)
	require.Same(t, client, client2)
	require.EqualValues(t, 1, proxyRepo.calls.Load())
}

func TestContentModerationService_ResolveModerationHTTPClient_GetByIDErrorFallsBackToDirect(t *testing.T) {
	proxyRepo := &contentModerationProxyRepoStub{
		getByIDFunc: func(ctx context.Context, id int64) (*Proxy, error) {
			return nil, errors.New("boom")
		},
	}
	svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil, proxyRepo)
	id := int64(3)
	client := svc.resolveModerationHTTPClient(context.Background(), &id)
	require.Same(t, svc.httpClient, client, "resolution errors must fail soft to the direct client")
}

func TestContentModerationService_ResolveModerationHTTPClient_InvalidProxyURLFallsBackToDirect(t *testing.T) {
	proxyRepo := &contentModerationProxyRepoStub{
		getByIDFunc: func(ctx context.Context, id int64) (*Proxy, error) {
			// A control character in the host makes the rendered URL unparseable, exercising
			// the defensive url.Parse error branch.
			return &Proxy{ID: id, Protocol: "http", Host: "exa\nmple.com", Port: 1}, nil
		},
	}
	svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil, proxyRepo)
	id := int64(4)
	client := svc.resolveModerationHTTPClient(context.Background(), &id)
	require.Same(t, svc.httpClient, client, "an unparseable proxy url must fail soft to the direct client")
}

func TestContentModerationService_UpdateConfig_ProxyIDLifecycle(t *testing.T) {
	proxyRepo := &contentModerationProxyRepoStub{
		getByIDFunc: func(ctx context.Context, id int64) (*Proxy, error) {
			if id == 11 {
				return &Proxy{ID: 11, Protocol: "http", Host: "127.0.0.1", Port: 3128}, nil
			}
			return nil, errors.New("proxy not found")
		},
	}
	settingRepo := &contentModerationTestSettingRepo{}
	svc := NewContentModerationService(settingRepo, nil, nil, nil, nil, nil, nil, proxyRepo)

	// >0 sets the proxy (and validates it exists).
	id := int64(11)
	view, err := svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{ProxyID: &id})
	require.NoError(t, err)
	require.NotNil(t, view.ProxyID)
	require.Equal(t, int64(11), *view.ProxyID)

	// nil (field omitted) means "no change".
	view, err = svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{})
	require.NoError(t, err)
	require.NotNil(t, view.ProxyID)
	require.Equal(t, int64(11), *view.ProxyID)

	// <=0 clears the proxy (back to direct connection).
	zero := int64(0)
	view, err = svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{ProxyID: &zero})
	require.NoError(t, err)
	require.Nil(t, view.ProxyID)

	// >0 pointing at a nonexistent proxy is rejected and does not persist.
	missing := int64(999)
	_, err = svc.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{ProxyID: &missing})
	require.Error(t, err)

	cfg, err := svc.GetConfig(context.Background())
	require.NoError(t, err)
	require.Nil(t, cfg.ProxyID, "a rejected update must not leave the proxy id changed")
}

// TestContentModerationService_TestAPIKeys_ProxyIDOverride exercises all three override
// semantics of TestContentModerationAPIKeysInput.ProxyID end to end against a real HTTP
// server: nil reuses the saved (here deliberately unroutable) proxy, and <=0 forces a
// direct connection regardless of what is saved. The unroutable proxy points at a port
// with nothing listening, so requests that are actually dialed through it fail fast
// with a connection error while requests that bypass it reach the test server.
func TestContentModerationService_TestAPIKeys_ProxyIDOverride(t *testing.T) {
	var upstreamHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{CategoryScores: map[string]float64{"sexual": 0.05}}}})
	}))
	defer server.Close()

	bogusProxyID := int64(21)
	proxyRepo := &contentModerationProxyRepoStub{
		getByIDFunc: func(ctx context.Context, id int64) (*Proxy, error) {
			if id == bogusProxyID {
				return &Proxy{ID: id, Protocol: "http", Host: "127.0.0.1", Port: 1}, nil
			}
			return nil, errors.New("proxy not found")
		},
	}

	cfg := defaultContentModerationConfig()
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"sk-test"}
	cfg.TimeoutMS = 2000
	cfg.ProxyID = &bogusProxyID
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	settingRepo := &contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyContentModerationConfig: string(rawCfg),
	}}
	svc := NewContentModerationService(settingRepo, nil, nil, nil, nil, nil, nil, proxyRepo)

	// nil ProxyID => reuse the saved (bogus) proxy => the dial must fail and the upstream
	// server must never see the request.
	result, err := svc.TestAPIKeys(context.Background(), TestContentModerationAPIKeysInput{Prompt: "hello"})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	require.NotEmpty(t, result.Items[0].LastError)
	require.EqualValues(t, 0, upstreamHits.Load())

	// ProxyID <= 0 => force a direct connection, bypassing the saved bogus proxy.
	forceDirect := int64(0)
	result, err = svc.TestAPIKeys(context.Background(), TestContentModerationAPIKeysInput{Prompt: "hello", ProxyID: &forceDirect})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	require.Empty(t, result.Items[0].LastError)
	require.EqualValues(t, 1, upstreamHits.Load())
}
