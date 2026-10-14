// Copyright (c) 2016 the quic-go authors & Google, Inc.
// Contracts informed by quic-go v0.57.1 http3/client_test.go,
// transport_test.go and body_test.go, revision
// 4b0ab7e48554e4440448c9252a92d7c065d88331; these tests use real peers.
// MIT license: see internal/http3/CONTRACT-TESTS-LICENSE.
package req

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	privatehttp3 "github.com/imroc/req/v3/internal/http3"
	"github.com/quic-go/quic-go"
	peerhttp3 "github.com/quic-go/quic-go/http3"
)

type h3WireObservedRequest struct {
	connID int64
	protocol int
	alpn string
	tlsVersion uint16
	method string
	body string
	err error
}

func TestReqHTTP3Wire_PublicRootCAAndReuse(t *testing.T) {
	observed := make(chan h3WireObservedRequest, 2)
	peer := h3WireNewPeer(t, func(w http.ResponseWriter, r *http.Request, _ <-chan struct{}) {
		body, err := io.ReadAll(r.Body)
		observation := h3WireObservedRequest{
			connID: r.Context().Value(h3WireConnIDKey{}).(int64),
			protocol: r.ProtoMajor,
			method: r.Method,
			body: string(body),
			err: err,
		}
		if r.TLS != nil { observation.alpn = r.TLS.NegotiatedProtocol; observation.tlsVersion = r.TLS.Version }
		observed <- observation
		if err != nil { http.Error(w, err.Error(), http.StatusBadRequest); return }
		w.Header().Set("X-Owned-QUIC-Connection", fmt.Sprint(observation.connID))
		_, _ = io.WriteString(w, "echo:"+string(body))
	})
	client := h3WireNewClient(t, peer.clientTLS)
	var firstConn int64
	for _, body := range []string{"first-payload", "second-payload"} {
		req, err := http.NewRequest(http.MethodPost, peer.url+"/echo", strings.NewReader(body))
		if err != nil { t.Fatal(err) }
		resp, err := client.Do(req)
		if err != nil { t.Fatalf("public TLS RootCAs must reach the forced HTTP3 handshake: %v", err) }
		data, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil || closeErr != nil { t.Fatalf("HTTP3 response Read/Close: %v/%v", readErr, closeErr) }
		if resp.StatusCode != http.StatusOK || resp.ProtoMajor != 3 || string(data) != "echo:"+body {
			t.Fatalf("unexpected actual HTTP3 response: status=%d proto=%s body=%q", resp.StatusCode, resp.Proto, data)
		}
		if resp.TLS == nil || !resp.TLS.HandshakeComplete || resp.TLS.NegotiatedProtocol != peerhttp3.NextProtoH3 || len(resp.TLS.VerifiedChains) == 0 {
			t.Fatalf("response did not preserve a verified h3 TLS handshake: %+v", resp.TLS)
		}
		observation := h3WireWait(t, observed, "peer request observation")
		if observation.err != nil || observation.protocol != 3 || observation.alpn != peerhttp3.NextProtoH3 || observation.tlsVersion != tls.VersionTLS13 || observation.method != http.MethodPost || observation.body != body {
			t.Fatalf("peer did not observe exact HTTP3 TLS/payload contract: %+v", observation)
		}
		if firstConn == 0 { firstConn = observation.connID } else if observation.connID != firstConn {
			t.Fatalf("drained response did not reuse QUIC connection: first=%d next=%d", firstConn, observation.connID)
		}
		if resp.Header.Get("X-Owned-QUIC-Connection") != fmt.Sprint(observation.connID) {
			t.Fatal("response connection identity disagrees with peer ConnContext")
		}
	}
}

func TestReqHTTP3Wire_UntrustedCertificate(t *testing.T) {
	var requests atomic.Int64
	peer := h3WireNewPeer(t, func(w http.ResponseWriter, r *http.Request, _ <-chan struct{}) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	client := h3WireNewClient(t, &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: x509.NewCertPool()})
	req, err := http.NewRequest(http.MethodGet, peer.url+"/untrusted", nil)
	if err != nil { t.Fatal(err) }
	resp, err := client.Do(req)
	if resp != nil { _ = resp.Body.Close() }
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("untrusted peer must be rejected by actual certificate verification: response=%v error=%v", resp, err)
	}
	var cryptoErr *quic.TransportError
	if !errors.As(err, &cryptoErr) || uint64(cryptoErr.ErrorCode) < 0x100 || uint64(cryptoErr.ErrorCode) > 0x1ff {
		t.Fatalf("certificate rejection must carry a real QUIC TLS crypto error, got %T: %v", err, err)
	}
	if requests.Load() != 0 { t.Fatal("untrusted TLS peer received an HTTP request") }
}

func TestReqHTTP3Wire_ResponseStreamRetirement(t *testing.T) {
	for _, mode := range []string{"body_close", "context_cancel"} {
		t.Run(mode, func(t *testing.T) {
			peerError := make(chan error, 1)
			peer := h3WireNewPeer(t, func(w http.ResponseWriter, r *http.Request, stop <-chan struct{}) {
				if _, err := io.WriteString(w, "prefix"); err != nil { peerError <- err; return }
				if err := http.NewResponseController(w).Flush(); err != nil { peerError <- err; return }
				select {
				case <-r.Context().Done():
					// The fixed official server cancels this context only after the
					// underlying QUIC stream context ends. Inspect the actual next
					// stream write, not a synthetic callback or cleanup-close event.
					_, err := io.WriteString(w, "after-cancel")
					if err == nil { err = http.NewResponseController(w).Flush() }
					peerError <- err
				case <-stop:
					peerError <- errors.New("fixture cleanup occurred before peer stream cancellation")
				}
			})
			client := h3WireNewClient(t, peer.clientTLS)
			// No automatic client timeout may substitute for the explicit
			// body-close or caller cancellation action being tested.
			client.SetTimeout(0)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, peer.url+"/blocked-response", nil)
			if err != nil { t.Fatal(err) }
			result := h3WireDo(t, client, req, cancel)
			resp := result.response
			if result.err != nil { t.Fatalf("verified forced HTTP3 request before stream retirement: %v", result.err) }
			if resp.ProtoMajor != 3 || resp.TLS == nil || resp.TLS.NegotiatedProtocol != peerhttp3.NextProtoH3 || len(resp.TLS.VerifiedChains) == 0 {
				t.Fatal("stream-retirement response did not come from actual verified HTTP3")
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			var prefix [6]byte
			if err := h3WireReadFull(t, resp.Body, prefix[:], cancel); err != nil || string(prefix[:]) != "prefix" { t.Fatalf("genuine streamed prefix: %q / %v", prefix, err) }
			if mode == "body_close" {
				if err := resp.Body.Close(); err != nil { t.Fatal(err) }
			} else {
				cancel()
				var next [1]byte
				err := h3WireReadFull(t, resp.Body, next[:], cancel)
				var local *privatehttp3.Error
				if !errors.As(err, &local) || local.Remote || local.ErrorCode != privatehttp3.ErrCodeRequestCanceled {
					t.Fatalf("canceled response reader must receive local H3_REQUEST_CANCELLED: %v", err)
				}
			}
			err = h3WireWait(t, peerError, "remote stream cancellation")
			var remote *peerhttp3.Error
			if !errors.As(err, &remote) || !remote.Remote || remote.ErrorCode != peerhttp3.ErrCodeRequestCanceled {
				t.Fatalf("actual peer write must receive remote H3_REQUEST_CANCELLED: %v", err)
			}
		})
	}
}
