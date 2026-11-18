// Copyright (c) 2016 the quic-go authors & Google, Inc.
// TLS dial controls use real quic-go v0.57.1 handshakes, revision
// 4b0ab7e48554e4440448c9252a92d7c065d88331, and its public DialAddr API.
// MIT license: see CONTRACT-TESTS-LICENSE.
package http3

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/imroc/req/v3/internal/transport"
	"github.com/quic-go/quic-go"
)

func TestReqHTTP3TLSControl_ConfigSelection(t *testing.T) {
	for _, mode := range []string{
		"options_nil_default",
		"explicit_tls_without_options",
		"shared_options_tls",
		"explicit_trusted_overrides_untrusted_options",
		"explicit_untrusted_overrides_trusted_options",
	} {
		t.Run(mode, func(t *testing.T) {
			// The stdlib owns a localhost certificate and its trusted client
			// root pool. Its TCP endpoint is only a certificate fixture; all
			// tested handshakes below use the owned loopback UDP listener.
			tcp := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			t.Cleanup(tcp.Close)
			serverTLS := tcp.TLS.Clone()
			serverTLS.MinVersion = tls.VersionTLS13
			serverTLS.NextProtos = []string{NextProtoH3}
			listener, err := quic.ListenAddr("127.0.0.1:0", serverTLS, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := listener.Close(); err != nil {
					t.Errorf("owned QUIC listener Close: %v", err)
				}
			})
			trusted := tcp.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			trusted.MinVersion = tls.VersionTLS13
			trusted.NextProtos = []string{"owned-dial-control"}
			untrusted := trusted.Clone()
			untrusted.RootCAs = x509.NewCertPool()
			tr := &Transport{}
			wantTrusted := mode != "options_nil_default" && mode != "explicit_untrusted_overrides_trusted_options"
			switch mode {
			case "options_nil_default":
				// Preserve the default certificate check with both sources nil.
			case "explicit_tls_without_options":
				tr.TLSClientConfig = trusted
			case "shared_options_tls":
				tr.Options = &transport.Options{TLSClientConfig: trusted}
			case "explicit_trusted_overrides_untrusted_options":
				tr.TLSClientConfig = trusted
				tr.Options = &transport.Options{TLSClientConfig: untrusted}
			case "explicit_untrusted_overrides_trusted_options":
				tr.TLSClientConfig = untrusted
				tr.Options = &transport.Options{TLSClientConfig: trusted}
			}
			verifiedHandshake := errors.New("owned verified QUIC handshake complete")
			var dialCalls int
			// Test the TLS configuration actually passed by private dial into
			// its documented Dial hook. This hook performs a real handshake
			// before returning an isolation sentinel, so nil Options need not
			// be passed into the unrelated HTTP request/connection machinery.
			tr.Dial = func(ctx context.Context, addr string, cfg *tls.Config, quicCfg *quic.Config) (*quic.Conn, error) {
				dialCalls++
				conn, err := quic.DialAddr(ctx, addr, cfg, quicCfg)
				if err != nil {
					return nil, err
				}
				defer conn.CloseWithError(0, "owned TLS control complete")
				state := conn.ConnectionState().TLS
				if !state.HandshakeComplete || state.Version != tls.VersionTLS13 || state.NegotiatedProtocol != NextProtoH3 || len(state.VerifiedChains) == 0 {
					return nil, errors.New("dial control did not complete a verified TLS1.3 h3 handshake")
				}
				serverConn, err := listener.Accept(ctx)
				if err != nil {
					return nil, err
				}
				defer serverConn.CloseWithError(0, "owned TLS control complete")
				peerState := serverConn.ConnectionState().TLS
				if !peerState.HandshakeComplete || peerState.Version != tls.VersionTLS13 || peerState.NegotiatedProtocol != NextProtoH3 {
					return nil, errors.New("peer did not observe the actual TLS1.3 h3 handshake")
				}
				return nil, verifiedHandshake
			}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			t.Cleanup(cancel)
			conn, clientConn, err := tr.dial(ctx, listener.Addr().String())
			if conn != nil || clientConn != nil || dialCalls != 1 {
				t.Fatalf("isolated dial returned unexpected connections or count: conn=%v client=%v calls=%d", conn, clientConn, dialCalls)
			}
			if wantTrusted {
				if !errors.Is(err, verifiedHandshake) {
					t.Fatalf("selected TLS config failed actual verified handshake: %v", err)
				}
			} else {
				var cryptoErr *quic.TransportError
				if !errors.As(err, &cryptoErr) || uint64(cryptoErr.ErrorCode) < 0x100 || uint64(cryptoErr.ErrorCode) > 0x1ff || !strings.Contains(err.Error(), "certificate") {
					t.Fatalf("default/explicit untrusted config must fail actual QUIC TLS verification: %v", err)
				}
			}
			for _, cfg := range []*tls.Config{trusted, untrusted} {
				if cfg.ServerName != "" || !reflect.DeepEqual(cfg.NextProtos, []string{"owned-dial-control"}) {
					t.Fatalf("private dial modified caller-owned TLS source: ServerName=%q ALPN=%v", cfg.ServerName, cfg.NextProtos)
				}
			}
		})
	}
}
