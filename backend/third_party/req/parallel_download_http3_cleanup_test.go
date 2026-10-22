// Copyright (c) 2016 the quic-go authors & Google, Inc.
// The owned TLS certificate fixture is h3WireTLS, attributed to quic-go v0.57.1
// in http3_wire_peer_test.go and internal/http3/CONTRACT-TESTS-LICENSE.
// This regression uses public req APIs and a real peer's public QUIC connection
// and UDP address. package req only permits reuse of that owned TLS fixture;
// no private production transport, socket or dialing hook supplies the oracle.
package req

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	peerhttp3 "github.com/quic-go/quic-go/http3"
)

type parallelH3CleanupConnectionKey struct{}

type parallelH3CleanupObservation struct {
	conn       *quic.Conn
	method     string
	rangeValue string
	ifRange    string
	protocol   int
	alpn       string
	status     int
	length     int64
	cause      error
	written    int
	writeErr   error
	flushErr   error
}

type parallelH3CleanupWriter struct {
	mu   sync.Mutex
	body bytes.Buffer
}

func (w *parallelH3CleanupWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(b)
}

func (w *parallelH3CleanupWriter) snapshot() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.body.Bytes()...)
}

func parallelH3CleanupWait[T any](t *testing.T, ctx context.Context, events <-chan T, label string) T {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-ctx.Done():
		t.Fatalf("G1748_H3_FIXTURE_TIMEOUT: no completed %s: %v", label, ctx.Err())
		var zero T
		return zero
	}
}

func parallelH3CleanupEndpoint(t *testing.T, conn *quic.Conn) *net.UDPAddr {
	t.Helper()
	if conn == nil {
		t.Fatal("G1748_H3_FIXTURE: peer has no actual QUIC connection")
	}
	addr, ok := conn.RemoteAddr().(*net.UDPAddr)
	if !ok || addr.Port == 0 || !addr.IP.IsLoopback() || addr.IP.To4() == nil {
		t.Fatalf("G1748_H3_FIXTURE: client endpoint is not nonzero IPv4 loopback: %v", conn.RemoteAddr())
	}
	return &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port, Zone: addr.Zone}
}

func parallelH3CleanupOccupied(t *testing.T, endpoint *net.UDPAddr) {
	t.Helper()
	listener, err := net.ListenUDP("udp4", endpoint)
	if err == nil {
		_ = listener.Close()
		t.Fatalf("G1748_H3_FIXTURE: active QUIC endpoint was bindable: %s", endpoint)
	}
	// Go's Windows EADDRINUSE is a synthetic errno, not Winsock10048.
	// Accept only the actual address-in-use cause, never EACCES/invalid address.
	inUse := errors.Is(err, syscall.EADDRINUSE)
	if runtime.GOOS == "windows" {
		inUse = errors.Is(err, syscall.Errno(10048)) // WSAEADDRINUSE.
	}
	if !inUse {
		t.Fatalf("G1748_H3_FIXTURE: binding active endpoint failed without address-in-use proof: %s: %v", endpoint, err)
	}
	t.Logf("G1748_H3_ACTIVE_ENDPOINT endpoint=%s cause=%v", endpoint, err)
}

func TestReqParallelDownloadHTTP3Cleanup(t *testing.T) {
	t.Run("owned_fallback_socket_released", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		serverTLS, clientTLS := h3WireTLS(t)
		udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		entity := bytes.Repeat([]byte("h3-owned-fallback-entity/"), 4)
		output := &parallelH3CleanupWriter{}
		headSeen := make(chan parallelH3CleanupObservation, 1)
		fullStarted := make(chan parallelH3CleanupObservation, 1)
		fullCompleted := make(chan parallelH3CleanupObservation, 1)
		probeSeen := make(chan parallelH3CleanupObservation, 1)
		rangesSeen := make(chan parallelH3CleanupObservation, 8)
		fullResponse := make(chan parallelH3CleanupObservation, 1)
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseFull := func() { releaseOnce.Do(func() { close(release) }) }
		var handlers sync.WaitGroup
		server := &peerhttp3.Server{
			TLSConfig: serverTLS,
			ConnContext: func(ctx context.Context, conn *quic.Conn) context.Context {
				return context.WithValue(ctx, parallelH3CleanupConnectionKey{}, conn)
			},
			Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				handlers.Add(1)
				defer handlers.Done()
				conn, _ := request.Context().Value(parallelH3CleanupConnectionKey{}).(*quic.Conn)
				event := parallelH3CleanupObservation{conn: conn, method: request.Method,
					rangeValue: request.Header.Get("Range"), ifRange: request.Header.Get("If-Range"), protocol: request.ProtoMajor}
				if request.TLS != nil {
					event.alpn = request.TLS.NegotiatedProtocol
				}
				if request.URL.Path == "/caller-check" {
					body := []byte("caller-still-open")
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
					w.WriteHeader(http.StatusOK)
					event.written, event.writeErr = w.Write(body)
					event.flushErr = http.NewResponseController(w).Flush()
					probeSeen <- event
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(entity)))
				w.WriteHeader(http.StatusOK)
				if request.Method == http.MethodHead {
					event.flushErr = http.NewResponseController(w).Flush()
					headSeen <- event
					return
				}
				if request.Method != http.MethodGet {
					return
				}
				if event.rangeValue == "" {
					event.flushErr = http.NewResponseController(w).Flush()
					fullStarted <- event
					select {
					case <-release:
					case <-request.Context().Done():
						event.writeErr = request.Context().Err()
						fullCompleted <- event
						return
					}
					event.written, event.writeErr = w.Write(entity)
					event.flushErr = http.NewResponseController(w).Flush()
					fullCompleted <- event
					return
				}
				// This ordinary HTTP3 endpoint ignores Range and returns200.
				event.written, event.writeErr = w.Write(entity)
				event.flushErr = http.NewResponseController(w).Flush()
				rangesSeen <- event
			}),
		}
		served := make(chan error, 1)
		t.Cleanup(func() {
			releaseFull()
			_ = udp.Close()
			closed := make(chan error, 1)
			go func() { closed <- server.Close() }()
			if err := h3WireWait(t, closed, "cleanup regression peer Close"); err != nil {
				t.Errorf("G1748_H3_FIXTURE: peer Close: %v", err)
			}
			if err := h3WireWait(t, served, "cleanup regression peer Serve"); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				t.Errorf("G1748_H3_FIXTURE: peer Serve: %v", err)
			}
			joined := make(chan struct{})
			go func() { handlers.Wait(); close(joined) }()
			h3WireWait(t, joined, "cleanup regression peer handlers")
		})
		go func() { served <- server.Serve(udp) }()
		client := h3WireOwnClient(t, C().SetTLSClientConfig(clientTLS).SetProxy(nil).SetTimeout(3*time.Second).EnableForceHTTP3())
		client.SetCommonHeader("Range", "bytes=0-0").SetCommonHeader("If-Range", "\"caller-validator\"")
		client.OnAfterResponse(func(_ *Client, response *Response) error {
			if response.Response != nil && response.Request != nil && response.Request.RawRequest != nil && response.Request.RawRequest.Method == http.MethodGet && response.Request.RawRequest.Header.Get("Range") == "" && response.Request.RawRequest.URL.Path == "/entity" {
				event := parallelH3CleanupObservation{method: response.Request.RawRequest.Method,
					protocol: response.ProtoMajor, status: response.StatusCode, length: response.ContentLength, cause: response.Err}
				if response.TLS != nil {
					event.alpn = response.TLS.NegotiatedProtocol
				}
				fullResponse <- event
			}
			return nil
		})
		url := "https://" + udp.LocalAddr().String()
		temporary := t.TempDir()
		result := make(chan error, 1)
		go func() {
			result <- client.NewParallelDownload(url+"/entity").SetTempRootDir(temporary).
				SetConcurrency(3).SetSegmentSize(17).SetOutput(output).Do(ctx)
		}()
		head := parallelH3CleanupWait(t, ctx, headSeen, "actual HEAD")
		full := parallelH3CleanupWait(t, ctx, fullStarted, "held full GET headers")
		received := parallelH3CleanupWait(t, ctx, fullResponse, "public full response headers")
		ranged := parallelH3CleanupWait(t, ctx, rangesSeen, "actual ignored Range transfer")
		if head.protocol != 3 || full.protocol != 3 || received.protocol != 3 || head.alpn != peerhttp3.NextProtoH3 || full.alpn != peerhttp3.NextProtoH3 || received.alpn != peerhttp3.NextProtoH3 || head.flushErr != nil || full.flushErr != nil || full.rangeValue != "" || full.ifRange != "" || received.status != http.StatusOK || received.length != int64(len(entity)) || received.cause != nil {
			t.Fatalf("G1748_H3_FIXTURE: actual h3/head/full headers were not established: head=%+v full=%+v received=%+v", head, full, received)
		}
		if ranged.rangeValue == "" || ranged.protocol != 3 || ranged.alpn != peerhttp3.NextProtoH3 || ranged.conn != head.conn {
			t.Fatalf("G1748_H3_FIXTURE: ignored Range did not traverse caller's real h3 connection: %+v", ranged)
		}
		// The discarded200 range stream may be aborted during recovery. The
		// complete full stream below, not this discarded body, proves success.
		t.Logf("G1748_H3_DISCARDED_RANGE %+v", ranged)
		callerEndpoint := parallelH3CleanupEndpoint(t, head.conn)
		ownedEndpoint := parallelH3CleanupEndpoint(t, full.conn)
		if head.conn == full.conn || callerEndpoint.String() == ownedEndpoint.String() {
			t.Fatal("G1748_H3_FIXTURE: full recovery has no distinct peer-observed owned connection")
		}
		parallelH3CleanupOccupied(t, ownedEndpoint)
		parallelH3CleanupOccupied(t, callerEndpoint)
		if len(output.snapshot()) != 0 {
			t.Fatal("G1748_H3_DOWNLOAD_ASSERT: output was written before full transfer completed")
		}
		releaseFull()
		completed := parallelH3CleanupWait(t, ctx, fullCompleted, "actual full body write")
		if completed.written != len(entity) || completed.writeErr != nil || completed.flushErr != nil {
			t.Fatalf("G1748_H3_FIXTURE: full entity transfer incomplete: %+v", completed)
		}
		if err := parallelH3CleanupWait(t, ctx, result, "download return"); err != nil {
			t.Fatalf("G1748_H3_DOWNLOAD_ASSERT: full HTTP3 fallback failed: %v", err)
		}
		actual := output.snapshot()
		if !bytes.Equal(actual, entity) || sha256.Sum256(actual) != sha256.Sum256(entity) {
			t.Fatalf("G1748_H3_DOWNLOAD_ASSERT: full entity differs: bytes=%d want=%d", len(actual), len(entity))
		}
		select {
		case <-full.conn.Context().Done():
		case <-ctx.Done():
			t.Fatalf("G1748_H3_RESOURCE_ASSERT: successful Do left private QUIC connection alive: %v", ctx.Err())
		}
		var remoteClose *quic.ApplicationError
		if !errors.As(context.Cause(full.conn.Context()), &remoteClose) || !remoteClose.Remote || remoteClose.ErrorCode != 0 {
			t.Fatalf("G1748_H3_RESOURCE_ASSERT: private connection did not receive a real normal remote close: %v", context.Cause(full.conn.Context()))
		}
		bound, err := net.ListenUDP("udp4", ownedEndpoint)
		if err != nil {
			t.Fatalf("G1748_H3_RESOURCE_ASSERT: owned endpoint not released after successful Do and peer closure: %s: %v", ownedEndpoint, err)
		}
		if err := bound.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-head.conn.Context().Done():
			t.Fatal("G1748_H3_RESOURCE_ASSERT: downloader closed caller's original connection")
		default:
		}
		probe := client.Get(url+"/caller-check").DisableAutoReadResponse().Do(ctx)
		if probe.Response != nil && probe.Body != nil {
			t.Cleanup(func() { _ = probe.Body.Close() })
		}
		if probe.Err != nil || probe.Response == nil || probe.Body == nil || probe.StatusCode != http.StatusOK || probe.ProtoMajor != 3 {
			t.Fatalf("G1748_H3_RESOURCE_ASSERT: caller transport no longer usable: response=%+v", probe)
		}
		probeBody, readErr := io.ReadAll(probe.Body)
		closeErr := probe.Body.Close()
		probeEvent := parallelH3CleanupWait(t, ctx, probeSeen, "caller reuse GET")
		if readErr != nil || closeErr != nil || string(probeBody) != "caller-still-open" || probeEvent.conn != head.conn || probeEvent.protocol != 3 || probeEvent.alpn != peerhttp3.NextProtoH3 || probeEvent.writeErr != nil || probeEvent.flushErr != nil {
			t.Fatalf("G1748_H3_RESOURCE_ASSERT: caller connection was not genuinely reused: read=%v close=%v peer=%+v", readErr, closeErr, probeEvent)
		}
		parallelH3CleanupOccupied(t, callerEndpoint)
		if client.Headers.Get("Range") != "bytes=0-0" || client.Headers.Get("If-Range") != "\"caller-validator\"" {
			t.Fatal("G1748_H3_DOWNLOAD_ASSERT: caller headers changed during full recovery")
		}
		t.Logf("G1748_H3_RELEASED owned=%s caller=%s bytes=%d hash=%x callerStillReusable=true", ownedEndpoint, callerEndpoint, len(actual), sha256.Sum256(actual))
	})
}

var _ io.Writer = (*parallelH3CleanupWriter)(nil)
