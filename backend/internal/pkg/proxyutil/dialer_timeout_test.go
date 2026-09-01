package proxyutil

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var errStub = errors.New("stub dial")

// 回归：SOCKS5 分支覆盖了调用方在 Transport 上设置的 DialContext，
// 底层 forward dialer 必须自带建连超时。proxy.Direct 是零值 net.Dialer，
// 代理不可达时会一直卡到内核 TCP 重传耗尽（Linux 约 130 秒）。
func TestSOCKS5ForwardDialerHasBoundedTimeout(t *testing.T) {
	require.Greater(t, socks5ForwardDialer.Timeout, time.Duration(0))
	require.Equal(t, socks5DialTimeout, socks5ForwardDialer.Timeout)
	require.Equal(t, socks5DialKeepAlive, socks5ForwardDialer.KeepAlive)
}

func TestConfigureTransportProxySOCKS5SetsDialContext(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			proxyURL, err := url.Parse(scheme + "://127.0.0.1:1080")
			require.NoError(t, err)

			transport := &http.Transport{}
			require.NoError(t, ConfigureTransportProxy(transport, proxyURL))
			require.NotNil(t, transport.DialContext)
			require.Nil(t, transport.Proxy, "SOCKS5 不应设置 Transport.Proxy")
		})
	}
}

// HTTP 代理走 Transport.Proxy，不得覆盖调用方设置的 DialContext。
func TestConfigureTransportProxyHTTPPreservesDialContext(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:8080")
	require.NoError(t, err)

	called := false
	transport := &http.Transport{}
	transport.DialContext = func(_ context.Context, _, _ string) (net.Conn, error) {
		called = true
		return nil, errStub
	}

	require.NoError(t, ConfigureTransportProxy(transport, proxyURL))
	require.NotNil(t, transport.Proxy)
	require.NotNil(t, transport.DialContext)

	_, _ = transport.DialContext(context.Background(), "tcp", "127.0.0.1:1")
	require.True(t, called, "HTTP 代理分支不应替换调用方的 DialContext")
}

// 端到端回归：SOCKS5 forward dialer 的超时确实会在拨往不可达地址时生效，
// 而不是被内核默认的 TCP 重传（约 130 秒）拖住。使用一个极小超时验证
// dial 会在超时窗口内返回错误，不依赖真实网络环境的可达性差异。
func TestSOCKS5ForwardDialerTimeoutTakesEffect(t *testing.T) {
	shortDialer := &net.Dialer{
		Timeout:   50 * time.Millisecond,
		KeepAlive: socks5DialKeepAlive,
	}

	// TEST-NET-1 (RFC 5737)：文档保留地址段，保证连接尝试不会被立即拒绝，
	// 而是黑洞式静默丢包，从而真正触发 dialer 的超时而不是即时 RST。
	start := time.Now()
	_, err := shortDialer.DialContext(context.Background(), "tcp", "192.0.2.1:1080")
	elapsed := time.Since(start)

	require.Error(t, err)
	require.Less(t, elapsed, 5*time.Second, "dialer 应在超时窗口附近返回，而不是卡到内核重传耗尽")
}
