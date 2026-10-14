// Copyright (c) 2016 the quic-go authors & Google, Inc.
// TLS fixture adapted from quic-go v0.57.1 http3/http3_helper_test.go,
// revision 4b0ab7e48554e4440448c9252a92d7c065d88331.
// MIT license: see internal/http3/CONTRACT-TESTS-LICENSE.
package req

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	peerhttp3 "github.com/quic-go/quic-go/http3"
)

type h3WireConnIDKey struct{}

type h3WirePeer struct {
	url       string
	clientTLS *tls.Config
	stop      chan struct{}
}

func h3WireWait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(8 * time.Second):
		t.Fatalf("HTTP3 wire fixture did not finish %s", what)
		var zero T
		return zero
	}
}

func h3WireTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "req owned HTTP3 test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafPublic, leafPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "req owned HTTP3 loopback peer"},
		NotBefore:    caTemplate.NotBefore,
		NotAfter:     caTemplate.NotAfter,
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, leafPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("owned TLS certificate fixture is not trusted by its configured root pool: %v", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{peerhttp3.NextProtoH3},
		Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafPrivate}},
	}, &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}
}

func h3WireNewPeer(t *testing.T, handler func(http.ResponseWriter, *http.Request, <-chan struct{})) *h3WirePeer {
	t.Helper()
	serverTLS, clientTLS := h3WireTLS(t)
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	p := &h3WirePeer{url: "https://" + udp.LocalAddr().String(), clientTLS: clientTLS, stop: make(chan struct{})}
	var connID atomic.Int64
	var handlers sync.WaitGroup
	server := &peerhttp3.Server{
		TLSConfig: serverTLS,
		ConnContext: func(ctx context.Context, conn *quic.Conn) context.Context {
			return context.WithValue(ctx, h3WireConnIDKey{}, connID.Add(1))
		},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlers.Add(1)
			defer handlers.Done()
			handler(w, r, p.stop)
		}),
	}
	served := make(chan error, 1)
	t.Cleanup(func() {
		close(p.stop)
		// Only this fixture's socket and server are stopped. Closing before the
		// bounded join also releases a request that failed before the handshake.
		_ = udp.Close()
		closed := make(chan error, 1)
		go func() { closed <- server.Close() }()
		if err := h3WireWait(t, closed, "peer Close"); err != nil {
			t.Errorf("HTTP3 peer Close: %v", err)
		}
		if err := h3WireWait(t, served, "peer Serve"); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			t.Errorf("HTTP3 peer Serve: %v", err)
		}
		joined := make(chan struct{})
		go func() { handlers.Wait(); close(joined) }()
		h3WireWait(t, joined, "peer handlers")
	})
	go func() { served <- server.Serve(udp) }()
	return p
}

func h3WireNewClient(t *testing.T, cfg *tls.Config) *Client {
	t.Helper()
	// Exercise the public library configuration and its production routing.
	// In particular, never assign Transport.t3.TLSClientConfig to hide a
	// failure to propagate the public TLS configuration into forced HTTP3.
	client := C().SetTLSClientConfig(cfg).SetProxy(nil).SetTimeout(6 * time.Second).EnableForceHTTP3()
	if client.Transport.t3 == nil {
		t.Fatal("public ForceHTTP3 did not create the private HTTP3 transport")
	}
	t.Cleanup(func() {
		client.GetTransport().CloseIdleConnections()
		// t3 is accessed only to dispose of this test's owned UDP socket.
		// No private connection/TLS/dial field is used to satisfy a contract.
		closed := make(chan error, 1)
		go func() { closed <- client.Transport.t3.Close() }()
		if err := h3WireWait(t, closed, "owned client Close"); err != nil {
			t.Errorf("HTTP3 client Close: %v", err)
		}
	})
	return client
}

func h3WireDo(t *testing.T, client *Client, req *http.Request, cancel context.CancelFunc) h3WireRequestResult {
	t.Helper()
	done := make(chan h3WireRequestResult, 1)
	joined := make(chan struct{})
	var response *http.Response
	t.Cleanup(func() {
		cancel()
		h3WireWait(t, joined, "request goroutine")
		if response != nil {
			_ = response.Body.Close()
		}
	})
	go func() {
		defer close(joined)
		var err error
		response, err = client.Do(req)
		done <- h3WireRequestResult{response: response, err: err}
	}()
	return h3WireWait(t, done, "actual request result")
}

func h3WireReadFull(t *testing.T, body io.ReadCloser, dst []byte, cancel context.CancelFunc) error {
	t.Helper()
	done := make(chan error, 1)
	joined := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		_ = body.Close()
		h3WireWait(t, joined, "response reader")
	})
	go func() { defer close(joined); _, err := io.ReadFull(body, dst); done <- err }()
	return h3WireWait(t, done, "actual response read")
}
