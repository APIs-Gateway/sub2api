package repository

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNewProxyExitInfoProber_ConfiguredTargets(t *testing.T) {
	prober, ok := NewProxyExitInfoProber(nil).(*proxyProbeService)
	require.True(t, ok)
	require.Empty(t, prober.configuredProbeURLs, "nil config keeps the built-in probe list")

	cfg := &config.Config{}
	prober, ok = NewProxyExitInfoProber(cfg).(*proxyProbeService)
	require.True(t, ok)
	require.Empty(t, prober.configuredProbeURLs, "empty urls keep the built-in probe list")

	cfg.Security.ProxyProbe.URLs = []config.ProbeURLConfig{
		{URL: "https://chatgpt.com/cdn-cgi/trace", Parser: "chatgpt-trace"},
		{URL: "https://api64.ipify.org?format=json", Parser: "ipify"},
	}
	prober, ok = NewProxyExitInfoProber(cfg).(*proxyProbeService)
	require.True(t, ok)
	require.Equal(t, []configuredProbeTarget{
		{url: "https://chatgpt.com/cdn-cgi/trace", parser: "chatgpt-trace"},
		{url: "https://api64.ipify.org?format=json", parser: "ipify"},
	}, prober.configuredProbeURLs)
}

// recordingProbeProxy 作为 HTTP 代理记录被请求的目标 URL，并按 host 返回预设响应。
func recordingProbeProxy(t *testing.T, responses map[string]string) (string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.RequestURI)
		mu.Unlock()
		for host, body := range responses {
			if strings.Contains(r.RequestURI, host) {
				_, _ = io.WriteString(w, body)
				return
			}
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func TestProbeProxy_ConfiguredTargetsOverrideBuiltinsInOrder(t *testing.T) {
	proxyURL, seen := recordingProbeProxy(t, map[string]string{
		"trace.example.test": "fl=1\nip=198.51.100.7\nloc=JP\n",
	})
	prober := &proxyProbeService{
		allowPrivateHosts: true,
		configuredProbeURLs: []configuredProbeTarget{
			{url: "http://down.example.test/json", parser: "ipify"},
			{url: "http://trace.example.test/cdn-cgi/trace", parser: "chatgpt-trace"},
		},
	}

	info, _, err := prober.ProbeProxy(context.Background(), proxyURL)
	require.NoError(t, err)
	require.Equal(t, "198.51.100.7", info.IP)
	require.Equal(t, "JP", info.CountryCode)

	requests := seen()
	require.Len(t, requests, 2, "configured targets are tried in order and stop at the first success")
	require.Contains(t, requests[0], "down.example.test")
	require.Contains(t, requests[1], "trace.example.test")
	for _, uri := range requests {
		require.NotContains(t, uri, "ip-api.com", "configured targets replace the built-in list")
		require.NotContains(t, uri, "api64.ipify.org", "configured targets replace the built-in list")
	}
}

func TestProbeProxy_ConfiguredTargetsAllFailDoNotFallBackToBuiltins(t *testing.T) {
	proxyURL, seen := recordingProbeProxy(t, map[string]string{
		// 内置目标可用，但配置了自定义目标时不应被使用。
		"ip-api.com": `{"status":"success","query":"1.2.3.4"}`,
	})
	prober := &proxyProbeService{
		allowPrivateHosts: true,
		configuredProbeURLs: []configuredProbeTarget{
			{url: "http://down.example.test/cdn-cgi/trace", parser: "chatgpt-trace"},
		},
	}

	_, _, err := prober.ProbeProxy(context.Background(), proxyURL)
	require.ErrorContains(t, err, "all probe URLs failed")
	require.Len(t, seen(), 1)
}

func TestProbeWithURL_UnknownParser(t *testing.T) {
	proxyURL, _ := recordingProbeProxy(t, map[string]string{"any.example.test": "ok"})
	prober := &proxyProbeService{
		allowPrivateHosts:   true,
		configuredProbeURLs: []configuredProbeTarget{{url: "http://any.example.test/", parser: "bogus"}},
	}

	_, _, err := prober.ProbeProxy(context.Background(), proxyURL)
	require.ErrorContains(t, err, "unknown parser: bogus")
}
