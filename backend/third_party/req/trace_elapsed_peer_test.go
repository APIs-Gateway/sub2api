// Original test fixture code under this module's existing MIT license.
// Real clocks, TLS/HTTP peers and public httptrace callbacks supply the oracle.
// No production timestamp or transport callback is replaced by a fake clock.
package req

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

// A timer is only a sampling opportunity. Success requires an observed later
// real monotonic-clock reading; guard expiry is always a fixture failure.
func tracePortableAdvance(ctx context.Context, start time.Time) (time.Time, error) {
	guard := time.NewTimer(5 * time.Second)
	defer guard.Stop()
	ticks := time.NewTicker(time.Millisecond)
	defer ticks.Stop()
	for {
		if err := ctx.Err(); err != nil { return time.Time{}, fmt.Errorf("TRACE_FIXTURE: clock prerequisite canceled: %w", err) }
		now := time.Now()
		if now.After(start) {
			return now, nil
		}
		select {
		case <-ticks.C:
		case <-ctx.Done():
			return time.Time{}, fmt.Errorf("TRACE_FIXTURE: clock prerequisite canceled: %w", ctx.Err())
		case <-guard.C:
			return time.Time{}, fmt.Errorf("TRACE_FIXTURE: real clock did not advance within guard")
		}
	}
}

func tracePortableAwait(ctx context.Context, done <-chan struct{}, label string) error {
	guard := time.NewTimer(5 * time.Second)
	defer guard.Stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("TRACE_FIXTURE: %s canceled: %w", label, ctx.Err())
	case <-guard.C:
		return fmt.Errorf("TRACE_FIXTURE: missing actual %s", label)
	}
}

func tracePortableTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	return &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		start := time.Now()
		end, err := tracePortableAdvance(hello.Context(), start)
		if err != nil {
			return nil, err
		}
		t.Logf("TRACE_REAL_TLS_ELAPSED start=%s end=%s elapsed=%s", start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), end.Sub(start))
		return nil, nil
	}}
}

type tracePortablePeerConnectionKey struct{}

type tracePortableEvents struct {
	connectStart int
	connectDone int
	connectError error
	tlsStart int
	tlsDone int
	tlsError error
	tlsComplete bool
	gotConn int
	firstByte int
	connection net.Conn
	peerConnection net.Conn
	peerALPN string
	reused bool
	remote net.Addr
	local net.Addr
	protocol int
	written int
	fixtureError error
	firstElapsed time.Duration
	bodyElapsed time.Duration
}

type tracePortableRequest struct {
	mu sync.Mutex
	events tracePortableEvents
	firstSeen chan struct{}
	firstOnce sync.Once
	peerDone chan struct{}
}

type tracePortablePeer struct {
	mu sync.Mutex
	sequence int
	requests map[string]*tracePortableRequest
	byRequest map[*Request]*tracePortableRequest
	expectedProtocol int
	firstClientConnection net.Conn
	firstPeerConnection net.Conn
}

const tracePortableBody = "portable-before-portable-after"

func newTracePortablePeer(t *testing.T, client *Client, expectedProtocol int) *tracePortablePeer {
	t.Helper()
	peer := &tracePortablePeer{requests: make(map[string]*tracePortableRequest), byRequest: make(map[*Request]*tracePortableRequest), expectedProtocol: expectedProtocol}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		peer.mu.Lock()
		observation := peer.requests[request.Header.Get("X-Trace-Portable-Token")]
		peer.mu.Unlock()
		if observation == nil {
			t.Error("TRACE_FIXTURE: real peer did not receive its actual request token")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer close(observation.peerDone)
		fail := func(err error) {
			observation.mu.Lock()
			observation.events.fixtureError = err
			observation.mu.Unlock()
		}
		start := time.Now()
		end, err := tracePortableAdvance(request.Context(), start)
		if err != nil {
			fail(err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		written, writeErr := fmt.Fprint(w, "portable-before-")
		if writeErr != nil {
			fail(fmt.Errorf("TRACE_FIXTURE: actual first fragment write: %w", writeErr))
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			fail(fmt.Errorf("TRACE_FIXTURE: actual first fragment Flush: %w", err))
			return
		}
		// Flush alone is insufficient: the client must actually report its
		// public first-byte callback before the body elapsed gate begins.
		if err := tracePortableAwait(request.Context(), observation.firstSeen, "public GotFirstResponseByte after first fragment Flush"); err != nil {
			fail(err)
			return
		}
		bodyStart := time.Now()
		bodyEnd, err := tracePortableAdvance(request.Context(), bodyStart)
		if err != nil {
			fail(err)
			return
		}
		n, writeErr := fmt.Fprint(w, "portable-after")
		if writeErr != nil {
			fail(fmt.Errorf("TRACE_FIXTURE: actual suffix write: %w", writeErr))
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			fail(fmt.Errorf("TRACE_FIXTURE: actual suffix Flush: %w", err))
			return
		}
		observation.mu.Lock()
		observation.events.protocol = request.ProtoMajor
		observation.events.peerConnection, _ = request.Context().Value(tracePortablePeerConnectionKey{}).(net.Conn)
		if request.TLS != nil { observation.events.peerALPN = request.TLS.NegotiatedProtocol }
		observation.events.written = written + n
		observation.events.firstElapsed = end.Sub(start)
		observation.events.bodyElapsed = bodyEnd.Sub(bodyStart)
		observation.mu.Unlock()
	}))
	server.Config.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		return context.WithValue(ctx, tracePortablePeerConnectionKey{}, conn)
	}
	server.EnableHTTP2 = true
	server.TLS = tracePortableTLSConfig(t)
	server.StartTLS()
	t.Cleanup(func() { client.Transport.CloseIdleConnections(); server.Close() })
	client.SetBaseURL(server.URL).SetProxy(nil)
	client.OnBeforeRequest(func(_ *Client, request *Request) error {
		observation := &tracePortableRequest{firstSeen: make(chan struct{}), peerDone: make(chan struct{})}
		peer.mu.Lock()
		peer.sequence++
		token := strconv.Itoa(peer.sequence)
		peer.requests[token] = observation
		peer.byRequest[request] = observation
		peer.mu.Unlock()
		request.SetHeader("X-Trace-Portable-Token", token)
		trace := &httptrace.ClientTrace{
			ConnectStart: func(_, _ string) { observation.mu.Lock(); observation.events.connectStart++; observation.mu.Unlock() },
			ConnectDone: func(_, _ string, err error) { observation.mu.Lock(); observation.events.connectDone++; observation.events.connectError = err; observation.mu.Unlock() },
			TLSHandshakeStart: func() { observation.mu.Lock(); observation.events.tlsStart++; observation.mu.Unlock() },
			TLSHandshakeDone: func(state tls.ConnectionState, err error) { observation.mu.Lock(); observation.events.tlsDone++; observation.events.tlsComplete = state.HandshakeComplete; observation.events.tlsError = err; observation.mu.Unlock() },
			GotConn: func(info httptrace.GotConnInfo) {
				var local, remote net.Addr
				if info.Conn != nil { local, remote = info.Conn.LocalAddr(), info.Conn.RemoteAddr() }
				observation.mu.Lock()
				observation.events.gotConn++
				observation.events.connection = info.Conn
				observation.events.reused = info.Reused
				observation.events.local, observation.events.remote = local, remote
				observation.mu.Unlock()
			},
			GotFirstResponseByte: func() {
				observation.mu.Lock(); observation.events.firstByte++; observation.mu.Unlock()
				observation.firstOnce.Do(func() { close(observation.firstSeen) })
			},
		}
		request.SetContext(httptrace.WithClientTrace(request.Context(), trace))
		return nil
	})
	return peer
}

func (peer *tracePortablePeer) assertCompleted(t *testing.T, response *Response, enabled, expectedReuse bool) {
	t.Helper()
	if response == nil || response.Request == nil { t.Fatal("TRACE_FIXTURE: actual response/request missing") }
	peer.mu.Lock()
	observation := peer.byRequest[response.Request]
	peer.mu.Unlock()
	if observation == nil { t.Fatal("TRACE_FIXTURE: response lacks actual peer/public trace observation") }
	if err := tracePortableAwait(response.Request.Context(), observation.peerDone, "actual complete peer body"); err != nil { t.Fatal(err) }
	observation.mu.Lock()
	events := observation.events
	observation.mu.Unlock()
	if events.fixtureError != nil { t.Fatal(events.fixtureError) }
	if events.firstElapsed <= 0 || events.bodyElapsed <= 0 || events.written != len(tracePortableBody) || response.String() != tracePortableBody || response.ReceivedAt().IsZero() {
		t.Fatalf("TRACE_FIXTURE: actual elapsed/body prerequisites incomplete: events=%+v received=%s body=%q", events, response.ReceivedAt(), response.String())
	}
	if response.ProtoMajor != peer.expectedProtocol || events.protocol != peer.expectedProtocol {
		t.Fatalf("TRACE_CONTRACT: expected HTTP%d on both response and real peer: response=%s peerHTTP%d", peer.expectedProtocol, response.Proto, events.protocol)
	}
	if events.gotConn != 1 || events.firstByte != 1 || events.connection == nil || events.peerConnection == nil || events.local == nil || events.remote == nil || response.TLS == nil {
		t.Fatalf("TRACE_FIXTURE: actual TLS/public/peer connection callbacks incomplete: %+v", events)
	}
	alpn := response.TLS.NegotiatedProtocol
	if alpn != events.peerALPN || (peer.expectedProtocol == 2 && alpn != "h2") || (peer.expectedProtocol == 1 && alpn != "" && alpn != "http/1.1") {
		t.Fatalf("TRACE_CONTRACT: actual TLS ALPN differs from expected HTTP%d: client=%q peer=%q", peer.expectedProtocol, alpn, events.peerALPN)
	}
	if !reflect.ValueOf(events.connection).Comparable() || !reflect.ValueOf(events.peerConnection).Comparable() {
		t.Fatalf("TRACE_FIXTURE: actual loopback connection identities are not comparable: client=%T peer=%T", events.connection, events.peerConnection)
	}
	if events.peerConnection.LocalAddr().String() != events.remote.String() || events.peerConnection.RemoteAddr().String() != events.local.String() {
		t.Fatalf("TRACE_FIXTURE: public client/peer addresses do not identify the same real connection: %+v", events)
	}
	peer.mu.Lock()
	firstClient, firstPeer := peer.firstClientConnection, peer.firstPeerConnection
	if !expectedReuse && firstClient == nil && firstPeer == nil {
		peer.firstClientConnection, peer.firstPeerConnection = events.connection, events.peerConnection
	}
	peer.mu.Unlock()
	if events.reused != expectedReuse || (expectedReuse && (firstClient == nil || firstPeer == nil || events.connection != firstClient || events.peerConnection != firstPeer)) || (!expectedReuse && (firstClient != nil || firstPeer != nil)) {
		t.Fatalf("TRACE_CONTRACT: expectedReuse=%t actual=%t sameClient=%t samePeer=%t events=%+v", expectedReuse, events.reused, events.connection == firstClient, events.peerConnection == firstPeer, events)
	}
	if expectedReuse {
		if events.connectStart != 0 || events.connectDone != 0 || events.tlsStart != 0 || events.tlsDone != 0 { t.Fatalf("TRACE_FIXTURE: reused connection had fresh connect/TLS events: %+v", events) }
	} else if events.connectStart != 1 || events.connectDone != 1 || events.connectError != nil || events.tlsStart != 1 || events.tlsDone != 1 || events.tlsError != nil || !events.tlsComplete {
		t.Fatalf("TRACE_FIXTURE: actual fresh TCP/TLS completion missing: %+v", events)
	}
	info := response.TraceInfo()
	if enabled && (info.IsConnReused != events.reused || info.LocalAddr == nil || info.RemoteAddr == nil || info.LocalAddr.String() != events.local.String() || info.RemoteAddr.String() != events.remote.String()) {
		t.Fatalf("TRACE_CONTRACT: trace connection/reuse differs from actual public callbacks: trace=%+v events=%+v", info, events)
	}
	t.Logf("TRACE_ACTUAL_COMPLETION enabled=%t expectedHTTP=%d expectedReuse=%t actualALPN=%q clientConn=%p peerConn=%p events=%+v received=%s trace=%+v", enabled, peer.expectedProtocol, expectedReuse, alpn, events.connection, events.peerConnection, events, response.ReceivedAt().Format(time.RFC3339Nano), info)
}
