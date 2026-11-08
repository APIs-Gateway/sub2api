// Copyright (c) 2016 the quic-go authors & Google, Inc.
// Public TLS configuration contracts use real loopback peers with quic-go
// v0.57.1, revision 4b0ab7e48554e4440448c9252a92d7c065d88331.
// MIT license: see internal/http3/CONTRACT-TESTS-LICENSE.
package req

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	peerhttp3 "github.com/quic-go/quic-go/http3"
)

func TestReqHTTP3PublicTLS_Configuration(t *testing.T) {
	for _, mode := range []string{
		"client_setter_before_enable",
		"client_setter_after_enable",
		"transport_setter_after_enable",
		"get_tls_pointer_after_enable",
		"insecure_before_enable",
		"insecure_after_enable",
	} {
		t.Run(mode, func(t *testing.T) {
			observed := make(chan h3WireObservedRequest, 1)
			peer := h3WireNewPeer(t, func(w http.ResponseWriter, r *http.Request, _ <-chan struct{}) {
				observation := h3WireObservedRequest{protocol: r.ProtoMajor, method: r.Method}
				if r.TLS != nil {
					observation.alpn = r.TLS.NegotiatedProtocol
					observation.tlsVersion = r.TLS.Version
				}
				observed <- observation
				_, _ = io.WriteString(w, "public-tls-config")
			})
			cfg := peer.clientTLS.Clone()
			// dial must clone this public config before replacing ALPN or SNI.
			cfg.NextProtos = []string{"owned-config-must-remain-unchanged"}
			var client *Client
			switch mode {
			case "client_setter_before_enable":
				client = h3WireNewClient(t, cfg)
			case "client_setter_after_enable":
				client = h3WireNewClient(t, &tls.Config{RootCAs: x509.NewCertPool()})
				client.SetTLSClientConfig(cfg)
			case "transport_setter_after_enable":
				client = h3WireNewClient(t, &tls.Config{RootCAs: x509.NewCertPool()})
				client.GetTransport().SetTLSClientConfig(cfg)
			case "get_tls_pointer_after_enable":
				client = h3WireNewClient(t, &tls.Config{RootCAs: x509.NewCertPool()})
				current := client.GetTLSClientConfig()
				current.RootCAs = cfg.RootCAs
				current.NextProtos = append([]string(nil), cfg.NextProtos...)
				cfg = current
				if client.GetTLSClientConfig() != current {
					t.Fatal("public TLS getter did not retain its configuration pointer")
				}
			case "insecure_before_enable":
				cfg.RootCAs = x509.NewCertPool()
				client = h3WireOwnClient(t, C().SetTLSClientConfig(cfg).EnableInsecureSkipVerify().SetProxy(nil).SetTimeout(6*time.Second).EnableForceHTTP3())
			case "insecure_after_enable":
				cfg.RootCAs = x509.NewCertPool()
				client = h3WireNewClient(t, cfg)
				client.EnableInsecureSkipVerify()
			}
			insecure := mode == "insecure_before_enable" || mode == "insecure_after_enable"
			req, err := http.NewRequest(http.MethodGet, peer.url+"/public-tls", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("public TLS configuration mode %s failed its actual QUIC handshake: %v", mode, err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			data, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			if readErr != nil || closeErr != nil || string(data) != "public-tls-config" || resp.StatusCode != http.StatusOK || resp.ProtoMajor != 3 {
				t.Fatalf("actual HTTP3 payload: status=%d proto=%s body=%q read=%v close=%v", resp.StatusCode, resp.Proto, data, readErr, closeErr)
			}
			if resp.TLS == nil || !resp.TLS.HandshakeComplete || resp.TLS.Version != tls.VersionTLS13 || resp.TLS.NegotiatedProtocol != peerhttp3.NextProtoH3 {
				t.Fatalf("missing actual TLS1.3/h3 handshake: %+v", resp.TLS)
			}
			if (len(resp.TLS.VerifiedChains) == 0) != insecure {
				t.Fatalf("unexpected TLS verification for public mode %s: chains=%d", mode, len(resp.TLS.VerifiedChains))
			}
			observation := h3WireWait(t, observed, "public TLS peer observation")
			if observation.protocol != 3 || observation.alpn != peerhttp3.NextProtoH3 || observation.tlsVersion != tls.VersionTLS13 || observation.method != http.MethodGet {
				t.Fatalf("peer did not observe actual h3/TLS1.3 request: %+v", observation)
			}
			if cfg.ServerName != "" || !reflect.DeepEqual(cfg.NextProtos, []string{"owned-config-must-remain-unchanged"}) {
				t.Fatalf("HTTP3 dial mutated caller-owned public TLS configuration: ServerName=%q NextProtos=%v", cfg.ServerName, cfg.NextProtos)
			}
		})
	}
}
