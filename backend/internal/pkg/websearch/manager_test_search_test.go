package websearch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testSearchRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testSearchRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testSearchHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestManagerTestSearchSafeStatusSummary(t *testing.T) {
	const secret = "api-key-secret"
	const upstreamBody = "provider-body-secret"
	m := NewManager([]ProviderConfig{
		{Type: ProviderTypeBrave, APIKey: secret},
		{Type: ProviderTypeTavily, APIKey: secret},
	}, nil)
	var calls []string
	m.clientCache[""] = &http.Client{Transport: testSearchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") == "Bearer "+secret {
			calls = append(calls, ProviderTypeTavily)
			return testSearchHTTPResponse(432, upstreamBody+" "+secret), nil
		}
		calls = append(calls, ProviderTypeBrave)
		return testSearchHTTPResponse(http.StatusUnauthorized, upstreamBody+" "+secret), nil
	})}

	resp, provider, err := m.TestSearch(context.Background(), SearchRequest{Query: "test"})
	require.Nil(t, resp)
	require.Empty(t, provider)
	var failures *TestSearchFailuresError
	require.ErrorAs(t, err, &failures)
	require.Equal(t, []string{ProviderTypeBrave, ProviderTypeTavily}, calls)
	require.Equal(t, "Brave: auth (HTTP 401); Tavily: limit (HTTP 432)", failures.Summary())
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), upstreamBody) {
		t.Fatal("test diagnostic contains provider secret or response body")
	}
}

func TestManagerTestSearchRateLimitAndInvalidResponse(t *testing.T) {
	m := NewManager([]ProviderConfig{{Type: ProviderTypeBrave, APIKey: "key"}}, nil)
	m.clientCache[""] = &http.Client{Transport: testSearchRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return testSearchHTTPResponse(http.StatusTooManyRequests, "raw-rate-limit-body"), nil
	})}
	_, _, err := m.TestSearch(context.Background(), SearchRequest{Query: "test"})
	var failures *TestSearchFailuresError
	require.ErrorAs(t, err, &failures)
	require.Equal(t, "Brave: limit (HTTP 429)", failures.Summary())
	if strings.Contains(err.Error(), "raw-rate-limit-body") {
		t.Fatal("test diagnostic contains provider response body")
	}

	m.clientCache[""] = &http.Client{Transport: testSearchRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return testSearchHTTPResponse(http.StatusOK, "invalid-response-secret"), nil
	})}
	_, _, err = m.TestSearch(context.Background(), SearchRequest{Query: "test"})
	require.ErrorAs(t, err, &failures)
	require.Equal(t, "Brave: invalid response", failures.Summary())
	if strings.Contains(err.Error(), "invalid-response-secret") {
		t.Fatal("test diagnostic contains invalid provider response")
	}
}

func TestManagerTestSearchFirstFailureThenSuccess(t *testing.T) {
	m := NewManager([]ProviderConfig{
		{Type: ProviderTypeBrave, APIKey: "brave-key"},
		{Type: ProviderTypeTavily, APIKey: "tavily-key"},
	}, nil)
	var calls int
	m.clientCache[""] = &http.Client{Transport: testSearchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Authorization") == "Bearer tavily-key" {
			return testSearchHTTPResponse(http.StatusOK, `{"results":[{"url":"https://example.com","title":"result","content":"found"}]}`), nil
		}
		return testSearchHTTPResponse(http.StatusUnauthorized, "first provider failed"), nil
	})}

	resp, provider, err := m.TestSearch(context.Background(), SearchRequest{Query: "test"})
	require.NoError(t, err)
	require.Equal(t, ProviderTypeTavily, provider)
	require.Equal(t, 2, calls)
	require.Len(t, resp.Results, 1)
	require.Equal(t, "found", resp.Results[0].Snippet)
}

func TestManagerTestSearchNoCandidate(t *testing.T) {
	past := time.Now().Add(-time.Hour).Unix()
	m := NewManager([]ProviderConfig{
		{Type: ProviderTypeBrave},
		{Type: ProviderTypeTavily, APIKey: "expired", ExpiresAt: &past},
	}, nil)
	resp, provider, err := m.TestSearch(context.Background(), SearchRequest{Query: "test"})
	require.Nil(t, resp)
	require.Empty(t, provider)
	require.ErrorIs(t, err, ErrTestNoAvailableProvider)
}

func TestManagerTestSearchSafeProxyAndNetworkSummary(t *testing.T) {
	const credentials = "user:password-secret"
	proxyURL := "://" + credentials + "@proxy.invalid"
	m := NewManager([]ProviderConfig{{Type: ProviderTypeBrave, APIKey: "api-key-secret", ProxyURL: proxyURL}}, nil)
	_, _, err := m.TestSearch(context.Background(), SearchRequest{Query: "test"})
	var failures *TestSearchFailuresError
	require.ErrorAs(t, err, &failures)
	require.Equal(t, "Brave: proxy", failures.Summary())
	if strings.Contains(err.Error(), credentials) || strings.Contains(err.Error(), "api-key-secret") {
		t.Fatal("test diagnostic contains proxy credentials or API key")
	}

	m = NewManager([]ProviderConfig{{Type: "unknown-secret-provider", APIKey: "api-key-secret"}}, nil)
	m.clientCache[""] = &http.Client{Transport: testSearchRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: "https://" + credentials + "@proxy.invalid", Err: errors.New("network-secret")}
	})}
	_, _, err = m.TestSearch(context.Background(), SearchRequest{Query: "test"})
	require.ErrorAs(t, err, &failures)
	require.Equal(t, "Provider: network", failures.Summary())
	if strings.Contains(err.Error(), credentials) || strings.Contains(err.Error(), "network-secret") || strings.Contains(err.Error(), "unknown-secret-provider") {
		t.Fatal("test diagnostic contains transport detail or untrusted provider type")
	}
}

func TestManagerTestSearchTimeoutSummary(t *testing.T) {
	m := NewManager([]ProviderConfig{{Type: ProviderTypeBrave, APIKey: "key"}}, nil)
	m.clientCache[""] = &http.Client{Transport: testSearchRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	_, _, err := m.TestSearch(context.Background(), SearchRequest{Query: "test"})
	var failures *TestSearchFailuresError
	require.ErrorAs(t, err, &failures)
	require.Equal(t, "Brave: timeout", failures.Summary())
}
