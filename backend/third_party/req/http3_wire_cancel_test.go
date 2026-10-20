// Copyright (c) 2016 the quic-go authors & Google, Inc.
// Contracts informed by quic-go v0.57.1 http3/transport_test.go,
// revision 4b0ab7e48554e4440448c9252a92d7c065d88331.
// MIT license: see internal/http3/CONTRACT-TESTS-LICENSE.
package req

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type h3WirePacket struct {
	size       int
	longHeader bool
	err        error
}

type h3WireRequestResult struct {
	response *http.Response
	err      error
}

func TestReqHTTP3Wire_HandshakeCancellationNoFallback(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			// A real trusted HTTP1 TLS endpoint is available on the same port.
			// The UDP peer receives QUIC packets but intentionally does not
			// complete a handshake. Forced HTTP3 must return its context error,
			// without succeeding through that reachable TCP fallback.
			var tcpRequests atomic.Int64
			tcp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tcpRequests.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(tcp.Close)
			addr := tcp.Listener.Addr().(*net.TCPAddr)
			udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: addr.Port})
			if err != nil {
				t.Fatal(err)
			}
			packets := make(chan h3WirePacket, 1)
			readDone := make(chan struct{})
			t.Cleanup(func() { _ = udp.Close(); h3WireWait(t, readDone, "owned UDP reader") })
			go func() {
				defer close(readDone)
				var data [2048]byte
				n, _, err := udp.ReadFromUDP(data[:])
				packets <- h3WirePacket{size: n, longHeader: n > 0 && data[0]&0x80 != 0, err: err}
			}()
			cfg := tcp.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			client := h3WireNewClient(t, cfg)
			client.SetTimeout(0)
			var tcpDials atomic.Int64
			client.GetTransport().DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				tcpDials.Add(1)
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}
			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			t.Cleanup(cancel)
			done := make(chan h3WireRequestResult, 1)
			joined := make(chan struct{})
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, tcp.URL+"/no-fallback", nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cancel(); h3WireWait(t, joined, "canceled request goroutine") })
			go func() {
				defer close(joined)
				response, err := client.Do(req)
				done <- h3WireRequestResult{response: response, err: err}
			}()
			packet := h3WireWait(t, packets, "real QUIC Initial packet")
			if packet.err != nil || packet.size < 1200 || !packet.longHeader {
				t.Fatalf("did not observe a genuine QUIC Initial datagram: %+v", packet)
			}
			if mode == "cancel" {
				cancel()
			}
			result := h3WireWait(t, done, "forced HTTP3 cancellation result")
			if result.response != nil {
				_ = result.response.Body.Close()
			}
			want := context.Canceled
			if mode == "deadline" {
				want = context.DeadlineExceeded
			}
			if result.response != nil || !errors.Is(result.err, want) {
				t.Fatalf("forced HTTP3 returned response=%v error=%v, want %v", result.response, result.err, want)
			}
			if tcpDials.Load() != 0 || tcpRequests.Load() != 0 {
				t.Fatalf("forced HTTP3 fell back to TCP: dials=%d requests=%d", tcpDials.Load(), tcpRequests.Load())
			}
		})
	}
}
