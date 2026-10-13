package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// All fixtures exercise the actual ProvideHTTPServer, with the compatibility
// adapter and native unencrypted dispatch installed by production code.
func compatibilityWireConfig() *config.Config {
	return &config.Config{Server: config.ServerConfig{Host: "127.0.0.1", IdleTimeout: 4, ReadHeaderTimeout: 3,
		H2C: config.H2CConfig{Enabled: true, IdleTimeout: 1, MaxConcurrentStreams: 3,
			MaxReadFrameSize: 32768, MaxUploadBufferPerConnection: 1 << 20, MaxUploadBufferPerStream: 131072}}}
}

func compatibilityWireRouter() *gin.Engine {
	router := gin.New()
	router.Any("/wire", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.String(http.StatusOK, "h%d:%s", c.Request.ProtoMajor, body)
	})
	return router
}

type compatibilityWireServer struct {
	server  *http.Server
	address string
}

func startCompatibilityWireServer(t *testing.T, cfg *config.Config, router *gin.Engine, mode string) (compatibilityWireServer, *x509.CertPool) {
	t.Helper()
	server := ProvideHTTPServer(cfg, router)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	roots := x509.NewCertPool()
	if mode != "plain" {
		seed := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		roots.AddCert(seed.Certificate())
		// ConfigureServer eagerly sets NextProtos; keep them for the custom
		// TLS-listener path. ServeTLS applies Protocols separately.
		if server.TLSConfig == nil {
			server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		server.TLSConfig.Certificates = seed.TLS.Certificates
		seed.Close()
	}
	done := make(chan error, 1)
	go func() {
		if mode == "serve-tls" {
			done <- server.ServeTLS(listener, "", "")
			return
		}
		if mode == "custom-tls" {
			done <- server.Serve(tls.NewListener(listener, server.TLSConfig.Clone()))
			return
		}
		done <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				t.Errorf("owned server stopped: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("owned server Serve did not join")
		}
	})
	return compatibilityWireServer{server: server, address: listener.Addr().String()}, roots
}

func compatibilityH1Conn(t *testing.T, address string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn, bufio.NewReader(conn)
}

func compatibilityH1Request(t *testing.T, conn net.Conn, reader *bufio.Reader, extra string) {
	t.Helper()
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	_, err := fmt.Fprintf(conn, "GET /wire HTTP/1.1\r\nHost: local\r\n%s\r\n", extra)
	require.NoError(t, err)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode, "Upgrade must not receive 101")
	require.Equal(t, 1, response.ProtoMajor)
	require.Equal(t, "h1:", string(body))
	require.NoError(t, conn.SetDeadline(time.Time{}))
}

type compatibilityH2Wire struct {
	conn     net.Conn
	framer   *http2.Framer
	settings map[http2.SettingID]uint32
	window   uint32
}

func compatibilityH2Conn(t *testing.T, address string) *compatibilityH2Wire {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	_, err = io.WriteString(conn, http2.ClientPreface)
	require.NoError(t, err)
	wire := &compatibilityH2Wire{conn: conn, framer: http2.NewFramer(conn, conn), settings: make(map[http2.SettingID]uint32)}
	require.NoError(t, wire.framer.WriteSettings())
	// This configured connection receive window exceeds 65535, so its initial
	// WINDOW_UPDATE must accompany SETTINGS before any request is sent.
	for len(wire.settings) == 0 || wire.window == 0 {
		frame, err := wire.framer.ReadFrame()
		require.NoError(t, err)
		switch frame := frame.(type) {
		case *http2.SettingsFrame:
			if !frame.IsAck() {
				require.NoError(t, frame.ForeachSetting(func(setting http2.Setting) error { wire.settings[setting.ID] = setting.Val; return nil }))
				require.NoError(t, wire.framer.WriteSettingsAck())
			}
		case *http2.WindowUpdateFrame:
			if frame.StreamID == 0 {
				wire.window += frame.Increment
			}
		}
	}
	require.NoError(t, conn.SetDeadline(time.Time{}))
	return wire
}

func (wire *compatibilityH2Wire) request(t *testing.T, stream uint32) {
	t.Helper()
	require.NoError(t, wire.conn.SetDeadline(time.Now().Add(3*time.Second)))
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	for _, field := range []hpack.HeaderField{{Name: ":method", Value: "GET"}, {Name: ":scheme", Value: "http"}, {Name: ":authority", Value: "local"}, {Name: ":path", Value: "/wire"}} {
		require.NoError(t, encoder.WriteField(field))
	}
	require.NoError(t, wire.framer.WriteHeaders(http2.HeadersFrameParam{StreamID: stream, EndHeaders: true, EndStream: true, BlockFragment: block.Bytes()}))
	var body bytes.Buffer
	for {
		frame, err := wire.framer.ReadFrame()
		require.NoError(t, err)
		switch frame := frame.(type) {
		case *http2.DataFrame:
			require.Equal(t, stream, frame.StreamID)
			body.Write(frame.Data())
			if frame.StreamEnded() {
				require.Equal(t, "h2:", body.String())
				require.NoError(t, wire.conn.SetDeadline(time.Time{}))
				return
			}
		case *http2.GoAwayFrame:
			t.Fatalf("unexpected GOAWAY during active request: %v", frame.ErrCode)
		}
	}
}

func (wire *compatibilityH2Wire) goAway(t *testing.T, timeout time.Duration) *http2.GoAwayFrame {
	t.Helper()
	require.NoError(t, wire.conn.SetReadDeadline(time.Now().Add(timeout)))
	for {
		frame, err := wire.framer.ReadFrame()
		require.NoError(t, err)
		if goAway, ok := frame.(*http2.GoAwayFrame); ok {
			require.Equal(t, http2.ErrCodeNo, goAway.ErrCode)
			return goAway
		}
	}
}

func TestProvideHTTPServerCompatibilityWire(t *testing.T) {
	t.Run("prior knowledge, HTTP1, Upgrade and advertised limits", func(t *testing.T) {
		cfg := compatibilityWireConfig()
		fixture, _ := startCompatibilityWireServer(t, cfg, compatibilityWireRouter(), "plain")
		require.True(t, fixture.server.Protocols.HTTP1())
		require.True(t, fixture.server.Protocols.UnencryptedHTTP2())
		require.False(t, fixture.server.Protocols.HTTP2())
		h1, reader := compatibilityH1Conn(t, fixture.address)
		compatibilityH1Request(t, h1, reader, "")
		compatibilityH1Request(t, h1, reader, "Connection: Upgrade, HTTP2-Settings\r\nUpgrade: h2c\r\nHTTP2-Settings: AAMAAABk\r\n")
		h2 := compatibilityH2Conn(t, fixture.address)
		require.Equal(t, cfg.Server.H2C.MaxConcurrentStreams, h2.settings[http2.SettingMaxConcurrentStreams])
		require.Equal(t, uint32(cfg.Server.H2C.MaxReadFrameSize), h2.settings[http2.SettingMaxFrameSize])
		require.Equal(t, uint32(cfg.Server.H2C.MaxUploadBufferPerStream), h2.settings[http2.SettingInitialWindowSize])
		require.Equal(t, uint32(cfg.Server.H2C.MaxUploadBufferPerConnection)-65535, h2.window)
		h2.request(t, 1)
	})
	t.Run("disabled retains HTTP1 and rejects prior knowledge", func(t *testing.T) {
		cfg := compatibilityWireConfig()
		cfg.Server.H2C.Enabled = false
		fixture, _ := startCompatibilityWireServer(t, cfg, compatibilityWireRouter(), "plain")
		require.Nil(t, fixture.server.Protocols)
		h1, reader := compatibilityH1Conn(t, fixture.address)
		compatibilityH1Request(t, h1, reader, "")
		protocols := new(http.Protocols)
		protocols.SetUnencryptedHTTP2(true)
		transport := &http.Transport{Protocols: protocols}
		defer transport.CloseIdleConnections()
		response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Get("http://" + fixture.address + "/wire")
		if response != nil {
			_ = response.Body.Close()
		}
		require.Error(t, err, "disabled server must not serve a successful H2 request")
	})
	t.Run("independent HTTP1 and HTTP2 idle expiry", func(t *testing.T) {
		fixture, _ := startCompatibilityWireServer(t, compatibilityWireConfig(), compatibilityWireRouter(), "plain")
		require.Equal(t, 4*time.Second, fixture.server.IdleTimeout)
		h1, reader := compatibilityH1Conn(t, fixture.address)
		compatibilityH1Request(t, h1, reader, "")
		h2 := compatibilityH2Conn(t, fixture.address)
		h2.request(t, 1)
		goAway := h2.goAway(t, 3*time.Second)
		require.Equal(t, uint32(1), goAway.LastStreamID)
		// H2 has expired, yet the older H1 socket still serves successfully.
		compatibilityH1Request(t, h1, reader, "")
		require.NoError(t, h1.SetReadDeadline(time.Now().Add(6*time.Second)))
		_, err := reader.ReadByte()
		require.ErrorIs(t, err, io.EOF, "H1 must expire on its own idle timer, not the client deadline")
	})
	t.Run("zero H2 idle inherits HTTP1", func(t *testing.T) {
		cfg := compatibilityWireConfig()
		cfg.Server.IdleTimeout = 2
		cfg.Server.H2C.IdleTimeout = 0
		fixture, _ := startCompatibilityWireServer(t, cfg, compatibilityWireRouter(), "plain")
		h2 := compatibilityH2Conn(t, fixture.address)
		h2.request(t, 1)
		started := time.Now()
		h2.goAway(t, 4*time.Second)
		require.GreaterOrEqual(t, time.Since(started), time.Second, "zero must inherit rather than expire immediately")
	})
	t.Run("negative H2 idle disables its timer", func(t *testing.T) {
		cfg := compatibilityWireConfig()
		cfg.Server.IdleTimeout = 1
		cfg.Server.H2C.IdleTimeout = -1
		fixture, _ := startCompatibilityWireServer(t, cfg, compatibilityWireRouter(), "plain")
		h2 := compatibilityH2Conn(t, fixture.address)
		h2.request(t, 1)
		require.NoError(t, h2.conn.SetReadDeadline(time.Now().Add(2*time.Second)))
		// Drain any final SETTINGS ACK, then require a client read timeout.
		for {
			frame, err := h2.framer.ReadFrame()
			if err != nil {
				var timeout net.Error
				require.ErrorAs(t, err, &timeout)
				require.True(t, timeout.Timeout())
				break
			}
			_, goAway := frame.(*http2.GoAwayFrame)
			require.False(t, goAway)
		}
		require.NoError(t, h2.conn.SetReadDeadline(time.Time{}))
		h2.request(t, 3)
	})
	t.Run("body limits on HTTP1 and prior knowledge HTTP2", func(t *testing.T) {
		for _, fallback := range []bool{false, true} {
			cfg := compatibilityWireConfig()
			cfg.Server.MaxRequestBodySize = 8
			if fallback {
				cfg.Server.MaxRequestBodySize = 0
				cfg.Gateway.MaxBodySize = 8
			}
			fixture, _ := startCompatibilityWireServer(t, cfg, compatibilityWireRouter(), "plain")
			for _, h2 := range []bool{false, true} {
				protocols := new(http.Protocols)
				protocols.SetHTTP1(!h2)
				protocols.SetUnencryptedHTTP2(h2)
				transport := &http.Transport{Protocols: protocols}
				t.Cleanup(transport.CloseIdleConnections)
				client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
				for _, size := range []int{4, 16} {
					response, err := client.Post("http://"+fixture.address+"/wire", "text/plain", strings.NewReader(strings.Repeat("a", size)))
					require.NoError(t, err)
					_, err = io.Copy(io.Discard, response.Body)
					require.NoError(t, err)
					require.NoError(t, response.Body.Close())
					expected := http.StatusOK
					if size > 8 {
						expected = http.StatusRequestEntityTooLarge
					}
					require.Equal(t, expected, response.StatusCode)
					major := 1
					if h2 {
						major = 2
					}
					require.Equal(t, major, response.ProtoMajor)
				}
			}
		}
	})
	t.Run("graceful shutdown preserves active H2 response", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		router := gin.New()
		router.GET("/wire", func(c *gin.Context) { close(started); <-release; c.String(http.StatusOK, "h2:") })
		cfg := compatibilityWireConfig()
		cfg.Server.H2C.IdleTimeout = -1
		fixture, _ := startCompatibilityWireServer(t, cfg, router, "plain")
		h2 := compatibilityH2Conn(t, fixture.address)
		// Send headers separately so the main test can observe GOAWAY while
		// the handler remains active; do not depend on Shutdown waiting for H2.
		var block bytes.Buffer
		encoder := hpack.NewEncoder(&block)
		for _, field := range []hpack.HeaderField{{Name: ":method", Value: "GET"}, {Name: ":scheme", Value: "http"}, {Name: ":authority", Value: "local"}, {Name: ":path", Value: "/wire"}} {
			require.NoError(t, encoder.WriteField(field))
		}
		require.NoError(t, h2.framer.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, EndHeaders: true, EndStream: true, BlockFragment: block.Bytes()}))
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("H2 handler did not start")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		shutdown := make(chan error, 1)
		go func() { shutdown <- fixture.server.Shutdown(ctx) }()
		shutdownJoined := false
		t.Cleanup(func() {
			if shutdownJoined {
				return
			}
			select {
			case <-shutdown:
			case <-time.After(5 * time.Second):
				t.Error("owned Shutdown did not join during cleanup")
			}
		})
		goAway := h2.goAway(t, 3*time.Second)
		require.Equal(t, uint32(1), goAway.LastStreamID)
		close(release)
		released = true
		require.NoError(t, h2.conn.SetReadDeadline(time.Now().Add(3*time.Second)))
		var body bytes.Buffer
		for {
			frame, err := h2.framer.ReadFrame()
			require.NoError(t, err)
			if data, ok := frame.(*http2.DataFrame); ok {
				body.Write(data.Data())
				if data.StreamEnded() {
					break
				}
			}
		}
		require.Equal(t, "h2:", body.String())
		select {
		case err := <-shutdown:
			shutdownJoined = true
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("owned Shutdown did not join")
		}
	})
	t.Run("ServeTLS and custom TLS listener preserve distinct ALPN paths", func(t *testing.T) {
		for _, mode := range []string{"serve-tls", "custom-tls"} {
			t.Run(mode, func(t *testing.T) {
				fixture, roots := startCompatibilityWireServer(t, compatibilityWireConfig(), compatibilityWireRouter(), mode)
				protocols := new(http.Protocols)
				protocols.SetHTTP1(true)
				protocols.SetHTTP2(true)
				transport := &http.Transport{Protocols: protocols, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
				t.Cleanup(transport.CloseIdleConnections)
				response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Get("https://" + fixture.address + "/wire")
				require.NoError(t, err)
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				major := 1
				if mode == "custom-tls" {
					major = 2
				}
				require.Equal(t, major, response.ProtoMajor)
				require.Equal(t, fmt.Sprintf("h%d:", major), string(body))
				alpn := "http/1.1"
				if major == 2 {
					alpn = "h2"
				}
				require.Equal(t, alpn, response.TLS.NegotiatedProtocol)
			})
		}
	})
}
