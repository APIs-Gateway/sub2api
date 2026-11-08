package req

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"testing"
	"time"

	reqtls "github.com/imroc/req/v3/pkg/tls"
)

func TestReqCustomTLS_PublicSuccessAndReuse(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		t.Run(fmt.Sprintf("HTTP%d", protocol), func(t *testing.T) {
			peer := newCustomTLSPeer(t, protocol)
			client := newCustomTLSClient(t, protocol).SetTLSHandshakeTimeout(500 * time.Millisecond)
			record := new(customTLSRecord)
			ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{TLSHandshakeStart: record.start, TLSHandshakeDone: record.done})
			var remote string
			for invocation := 0; invocation < 2; invocation++ {
				response, err := client.R().SetContext(ctx).SetBodyString("actual-upload").Post(peer.server.URL + "/entity")
				if err != nil || response.StatusCode != http.StatusOK || response.ProtoMajor != protocol || response.Header.Get("X-Custom-TLS-Peer") != "actual" ||
					sha256.Sum256([]byte(response.String())) != sha256.Sum256([]byte("actual-custom-tls:actual-upload")) {
					t.Fatalf("actual HTTP%d exchange: response=%v error=%v", protocol, response, err)
				}
				select {
				case request := <-peer.requests:
					if request.method != http.MethodPost || request.body != "actual-upload" || request.protocol != protocol || (invocation != 0 && request.remote != remote) {
						t.Fatalf("wire/reuse changed: %+v prior=%q", request, remote)
					}
					remote = request.remote
				case <-time.After(6 * time.Second):
					t.Fatal("actual peer request missing")
				}
				if invocation == 0 {
					// The previous handshake deadline must not close an accepted,
					// reusable connection after successful real response EOF.
					time.Sleep(750 * time.Millisecond)
				}
			}
			starts, dones, state, err := record.snapshot()
			if starts != 1 || dones != 1 || err != nil || !state.HandshakeComplete {
				t.Fatalf("actual success trace: starts=%d dones=%d state=%+v error=%v", starts, dones, state, err)
			}
			client.GetTransport().CloseIdleConnections()
			peer.waitClosed(t, remote)
		})
	}
}

// OLD/NEW use this identical public fixture. A real Firefox handshake completes
// before a bounded independent delay holds only its return/publication. Never
// release that delay from Do/Close/timeout events: doing so would create the very
// happens-before edge missing from the old production read/write pair.
func TestReqCustomTLS_LatePublicationRace(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		t.Run(fmt.Sprintf("HTTP%d", protocol), func(t *testing.T) {
			peer := newCustomTLSPeer(t, protocol)
			client := newCustomTLSClient(t, protocol).SetTLSHandshakeTimeout(500 * time.Millisecond)
			callback := client.TLSHandshakeContext
			info := make(chan customTLSLateResult, 1)
			returned := make(chan struct{})
			client.GetTransport().SetTLSHandshake(func(ctx context.Context, addr string, raw net.Conn) (net.Conn, *tls.ConnectionState, error) {
				defer close(returned)
				conn, state, err := callback(ctx, addr, raw)
				if err != nil {
					info <- customTLSLateResult{err: err}
					return conn, state, err
				}
				observed := &customTLSObservedConn{Conn: conn.(reqtls.Conn), closed: make(chan struct{})}
				info <- customTLSLateResult{conn: observed, state: *state, rawAddr: raw.LocalAddr().String()}
				time.Sleep(1500 * time.Millisecond)
				return observed, state, nil
			})
			t.Cleanup(func() { customTLSWait(t, returned, "controlled late Firefox callback return") })
			record := new(customTLSRecord)
			ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{TLSHandshakeStart: record.start, TLSHandshakeDone: record.done})
			response, err := client.R().SetContext(ctx).Get(peer.server.URL + "/late")
			actual := customTLSReceiveLate(t, info)
			if actual.err != nil || !actual.state.HandshakeComplete || actual.conn == nil {
				t.Fatalf("CUSTOM_TLS_PREREQUISITE: no genuine Firefox TLS success before publication delay: %v", actual.err)
			}
			var timeout net.Error
			if err == nil || response == nil || !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("CUSTOM_TLS_TIMEOUT: timer-first request returned response=%v error=%v", response, err)
			}
			t.Logf("CUSTOM_TLS_REAL_HANDSHAKE protocol=%d version=%x alpn=%q raw=%s; actual timeout=%v", protocol, actual.state.Version, actual.state.NegotiatedProtocol, actual.rawAddr, err)
			customTLSWait(t, returned, "independently delayed successful callback")
			customTLSWait(t, actual.conn.closed, "late returned wrapper cleanup")
			peer.waitClosed(t, actual.rawAddr)
			starts, dones, state, traceErr := record.snapshot()
			if starts != 1 || dones != 1 || traceErr == nil || !errors.As(traceErr, &timeout) || !timeout.Timeout() || state.HandshakeComplete {
				t.Fatalf("late callback changed terminal trace: starts=%d dones=%d state=%+v error=%v", starts, dones, state, traceErr)
			}
			if len(peer.requests) != 0 {
				t.Fatal("timed-out handshake published an HTTP request")
			}
		})
	}
}

func TestReqCustomTLS_LateDistinctConnection(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		t.Run(fmt.Sprintf("HTTP%d", protocol), func(t *testing.T) {
			peer := newCustomTLSPeer(t, protocol)
			client := newCustomTLSClient(t, protocol).SetTLSHandshakeTimeout(500 * time.Millisecond)
			callback := client.TLSHandshakeContext
			info := make(chan customTLSLateResult, 1)
			returned := make(chan struct{})
			client.GetTransport().SetTLSHandshake(func(ctx context.Context, addr string, raw net.Conn) (net.Conn, *tls.ConnectionState, error) {
				defer close(returned)
				first, firstState, err := callback(ctx, addr, raw)
				if err != nil || firstState == nil || !firstState.HandshakeComplete {
					info <- customTLSLateResult{err: fmt.Errorf("first real handshake: %w", err)}
					return first, firstState, err
				}
				conn, err := (&tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}}).DialContext(ctx, "tcp", peer.server.Listener.Addr().String())
				if err != nil {
					info <- customTLSLateResult{err: err}
					return first, firstState, err
				}
				actual := conn.(*tls.Conn)
				state := actual.ConnectionState()
				observed := &customTLSObservedConn{Conn: actual, closed: make(chan struct{})}
				info <- customTLSLateResult{conn: observed, state: state, rawAddr: raw.LocalAddr().String()}
				time.Sleep(1500 * time.Millisecond)
				return observed, &state, nil
			})
			t.Cleanup(func() { customTLSWait(t, returned, "controlled distinct-connection callback") })
			response := client.Get(peer.server.URL).Do()
			err := response.Err
			actual := customTLSReceiveLate(t, info)
			if actual.err != nil || actual.conn == nil || !actual.state.HandshakeComplete {
				t.Fatalf("distinct real TLS prerequisite: %v", actual.err)
			}
			var timeout net.Error
			if err == nil || !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("distinct late result changed primary timeout: %v", err)
			}
			customTLSWait(t, returned, "distinct callback return")
			customTLSWait(t, actual.conn.closed, "distinct late TLS connection cleanup")
			peer.waitClosed(t, actual.rawAddr)
			peer.waitClosed(t, actual.conn.LocalAddr().String())
		})
	}
}

func customTLSReceiveLate(t *testing.T, info <-chan customTLSLateResult) customTLSLateResult {
	t.Helper()
	select {
	case actual := <-info:
		if actual.conn != nil {
			t.Cleanup(func() { actual.conn.Close() })
		}
		return actual
	case <-time.After(6 * time.Second):
		t.Fatal("CUSTOM_TLS_PREREQUISITE: real handshake information missing")
		return customTLSLateResult{}
	}
}
