package repository

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func http2KeepAliveTestPoolSettings() poolSettings {
	return poolSettings{
		maxIdleConns:          10,
		maxIdleConnsPerHost:   5,
		maxConnsPerHost:       10,
		idleConnTimeout:       90 * time.Second,
		responseHeaderTimeout: time.Minute,
	}
}

func TestEnableOpenAIHTTP2KeepAliveEnablesPingHealthCheck(t *testing.T) {
	tr := &http.Transport{}

	h2, err := enableOpenAIHTTP2KeepAlive(tr)
	require.NoError(t, err)
	require.NotNil(t, h2)
	require.Equal(t, openAIHTTP2ReadIdleTimeout, h2.SendPingTimeout)
	require.Equal(t, openAIHTTP2PingTimeout, h2.PingTimeout)
	require.NotNil(t, tr.HTTP2)
	require.True(t, tr.Protocols.HTTP1())
	require.True(t, tr.Protocols.HTTP2())
	t.Run("real ping ACK and reuse", testNativeOpenAIHTTP2PingACK)
	t.Run("real missing ping ACK retirement", testNativeOpenAIHTTP2PingRetirement)
}

func TestEnableOpenAIHTTP2KeepAlive_PropagatesConfigureError(t *testing.T) {
	tr := &http.Transport{}
	_, err := enableOpenAIHTTP2KeepAlive(tr)
	require.NoError(t, err)

	_, err = enableOpenAIHTTP2KeepAlive(tr)
	require.Error(t, err)
}

func TestBuildUpstreamTransportOpenAIH2EnablesPingHealthCheck(t *testing.T) {
	tr, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, upstreamProtocolModeOpenAIH2)
	require.NoError(t, err)
	require.True(t, tr.ForceAttemptHTTP2)
	require.NotNil(t, tr.HTTP2)
	require.True(t, tr.Protocols.HTTP1())
	require.True(t, tr.Protocols.HTTP2())
	t.Run("real HTTP1 fallback", testNativeOpenAIHTTP1Fallback)
	t.Run("custom TLS and close idle", testNativeOpenAIHTTP2CustomTLSAndCloseIdle)
}

func TestBuildUpstreamTransportNonOpenAIH2IsNotEagerlyConfigured(t *testing.T) {
	tr, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, upstreamProtocolModeDefault)
	require.NoError(t, err)
	require.Nil(t, tr.HTTP2)
	require.Nil(t, tr.Protocols)
	require.Nil(t, tr.TLSNextProto["h2"])
}

func TestBuildUpstreamTransportOpenAIH2WithHTTPProxyEnablesKeepAlive(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:8080")
	require.NoError(t, err)

	tr, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), proxyURL, upstreamProtocolModeOpenAIH2)
	require.NoError(t, err)
	require.True(t, tr.ForceAttemptHTTP2)
	require.NotNil(t, tr.HTTP2)
	require.True(t, tr.Protocols.HTTP1())
	require.True(t, tr.Protocols.HTTP2())
	require.NotNil(t, tr.Proxy)
	for _, scheme := range []string{"http", "https", "socks5h"} {
		t.Run(scheme+" real h2 tunnel", func(t *testing.T) { testNativeOpenAIHTTP2Proxy(t, scheme) })
	}
}
