package repository

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUpstreamIPModeDialFamilies(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	for _, mode := range []string{"", config.UpstreamIPModeAuto, config.UpstreamIPModeIPv4} {
		t.Run(mode, func(t *testing.T) {
			conn, err := newUpstreamDialContext(mode)(context.Background(), "tcp", listener.Addr().String())
			require.NoError(t, err)
			require.NoError(t, conn.Close())
		})
	}

	conn, err := newUpstreamDialContext(config.UpstreamIPModeIPv6)(context.Background(), "tcp", listener.Addr().String())
	require.Error(t, err, "IPv6 mode must not use an IPv4 address")
	require.Nil(t, conn)

	conn, err = newUpstreamDialContext(config.UpstreamIPModeIPv4)(context.Background(), "tcp", "[::1]:443")
	require.Error(t, err, "IPv4 mode must not use an IPv6 address")
	require.Nil(t, conn)
}

func TestUpstreamIPModeDirectTransportAndHTTP2(t *testing.T) {
	settings := defaultPoolSettings(&config.Config{Gateway: config.GatewayConfig{UpstreamIPMode: config.UpstreamIPModeIPv6}})
	for _, protocolMode := range []string{upstreamProtocolModeDefault, upstreamProtocolModeOpenAIH2} {
		t.Run(protocolMode, func(t *testing.T) {
			transport, err := buildUpstreamTransport(settings, nil, protocolMode)
			require.NoError(t, err)
			defer transport.CloseIdleConnections()
			conn, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:443")
			require.Error(t, err)
			require.Nil(t, conn)
			require.Equal(t, defaultUpstreamTLSHandshakeTimeout, transport.TLSHandshakeTimeout)
		})
	}
}

func TestUpstreamIPModeDirectTLSFingerprint(t *testing.T) {
	settings := defaultPoolSettings(&config.Config{Gateway: config.GatewayConfig{UpstreamIPMode: config.UpstreamIPModeIPv6}})
	transport, err := buildUpstreamTransportWithTLSFingerprint(settings, nil, nil)
	require.NoError(t, err)
	defer transport.CloseIdleConnections()
	conn, err := transport.DialTLSContext(context.Background(), "tcp", "127.0.0.1:443")
	require.Error(t, err, "direct TLS fingerprint dialing must reject the wrong address family before TLS")
	require.Nil(t, conn)
	require.Equal(t, defaultUpstreamTLSHandshakeTimeout, transport.TLSHandshakeTimeout)
}

func TestUpstreamIPModeDoesNotChangeHTTPProxyDial(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)

	settings := defaultPoolSettings(&config.Config{Gateway: config.GatewayConfig{UpstreamIPMode: config.UpstreamIPModeIPv6}})
	transport, err := buildUpstreamTransport(settings, proxyURL, upstreamProtocolModeDefault)
	require.NoError(t, err)
	defer transport.CloseIdleConnections()

	resp, err := (&http.Client{Transport: transport}).Get("http://upstream.invalid/path")
	require.NoError(t, err, "the IPv4 proxy must remain reachable in IPv6 upstream mode")
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestUpstreamIPModeDoesNotChangeSOCKSProxyDial(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	proxyDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			proxyDone <- err
			return
		}
		defer conn.Close()
		greeting := make([]byte, 3)
		if _, err = io.ReadFull(conn, greeting); err != nil {
			proxyDone <- err
			return
		}
		if _, err = conn.Write([]byte{5, 0}); err != nil {
			proxyDone <- err
			return
		}
		request := make([]byte, 4)
		if _, err = io.ReadFull(conn, request); err != nil {
			proxyDone <- err
			return
		}
		if request[3] != 3 {
			proxyDone <- errors.New("expected proxy-side DNS resolution")
			return
		}
		length := make([]byte, 1)
		if _, err = io.ReadFull(conn, length); err != nil {
			proxyDone <- err
			return
		}
		if _, err = io.CopyN(io.Discard, conn, int64(length[0])+2); err != nil {
			proxyDone <- err
			return
		}
		_, err = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
		proxyDone <- err
	}()

	proxyURL, err := url.Parse("socks5h://" + listener.Addr().String())
	require.NoError(t, err)
	settings := defaultPoolSettings(&config.Config{Gateway: config.GatewayConfig{UpstreamIPMode: config.UpstreamIPModeIPv6}})
	transport, err := buildUpstreamTransport(settings, proxyURL, upstreamProtocolModeDefault)
	require.NoError(t, err)
	defer transport.CloseIdleConnections()
	conn, err := transport.DialContext(context.Background(), "tcp", "upstream.invalid:80")
	require.NoError(t, err, "the IPv4 SOCKS proxy must remain reachable in IPv6 upstream mode")
	require.NoError(t, conn.Close())
	require.NoError(t, <-proxyDone)
}

func TestUpstreamIPModeSeparatesConnectionPools(t *testing.T) {
	settings := defaultPoolSettings(nil)
	autoKey := buildPoolKey(settings, upstreamProtocolModeDefault)
	settings.ipMode = config.UpstreamIPModeIPv4
	ipv4Key := buildPoolKey(settings, upstreamProtocolModeDefault)
	settings.ipMode = config.UpstreamIPModeIPv6
	ipv6Key := buildPoolKey(settings, upstreamProtocolModeDefault)
	require.NotEqual(t, autoKey, ipv4Key)
	require.NotEqual(t, autoKey, ipv6Key)
	require.NotEqual(t, ipv4Key, ipv6Key)
}
