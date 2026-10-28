package http2

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imroc/req/v3/internal/transport"
	reqtls "github.com/imroc/req/v3/pkg/tls"
)

type customTLSBoundaryResult struct {
	conn  net.Conn
	state *tls.ConnectionState
	err   error
}

func TestReqCustomTLS_Boundaries(t *testing.T) {
	cases := []string{"NoTimeoutSuccess", "CallbackError", "ParentCanceled", "ParentDeadline", "HandshakeTimeout", "LateError", "SlowStart", "ExpiredBeforeStart", "SlowDoneSuccess", "NilConnection", "TypedNilConnection", "NilState", "NonTLSConnection"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			peer := newCustomTLSPeer(t, 1)
			parent, cancelParent := context.WithCancel(context.Background())
			defer cancelParent()
			if name == "ParentDeadline" {
				var cancel context.CancelFunc
				parent, cancel = context.WithTimeoutCause(parent, 120*time.Millisecond, errors.New("caller deadline cause"))
				defer cancel()
			}
			timeout := time.Second
			switch name {
			case "NoTimeoutSuccess":
				timeout = 0
			case "HandshakeTimeout", "LateError", "SlowStart":
				timeout = 120 * time.Millisecond
			case "ExpiredBeforeStart":
				timeout = -time.Nanosecond
			case "SlowDoneSuccess":
				timeout = 250 * time.Millisecond
			}
			callbackDone := make(chan struct{})
			startEntered := make(chan struct{})
			doneEntered := make(chan struct{})
			traceReturned := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseHook := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(func() {
				releaseHook()
				customTLSWait(t, callbackDone, "boundary callback join")
				customTLSWait(t, traceReturned, "boundary trace join")
			})
			info := make(chan customTLSLateResult, 1)
			rawAddr := make(chan string, 1)
			callbackError := errors.New("controlled custom TLS callback failure")
			callback := func(ctx context.Context, addr string, raw net.Conn) (net.Conn, *tls.ConnectionState, error) {
				defer close(callbackDone)
				rawAddr <- raw.LocalAddr().String()
				switch name {
				case "ParentDeadline", "HandshakeTimeout", "SlowStart", "ExpiredBeforeStart":
					<-ctx.Done()
					return raw, nil, ctx.Err()
				}
				conn, state, err := customTLSRealHandshake(ctx, addr, raw)
				if err != nil {
					info <- customTLSLateResult{err: err}
					return conn, state, err
				}
				observed := &customTLSObservedConn{Conn: conn.(reqtls.Conn), closed: make(chan struct{})}
				info <- customTLSLateResult{conn: observed, state: *state, rawAddr: raw.LocalAddr().String()}
				switch name {
				case "CallbackError":
					return observed, state, callbackError
				case "ParentCanceled":
					cancelParent()
				case "LateError":
					// No timeout/Close event releases this callback delay.
					time.Sleep(450 * time.Millisecond)
					return observed, state, callbackError
				case "NilConnection":
					return nil, state, nil
				case "TypedNilConnection":
					var absent *tls.Conn
					return absent, state, nil
				case "NilState":
					return observed, nil, nil
				case "NonTLSConnection":
					return raw, state, nil
				}
				return observed, state, nil
			}
			record := new(customTLSRecord)
			trace := &httptrace.ClientTrace{
				TLSHandshakeStart: func() {
					record.start()
					close(startEntered)
					if name == "SlowStart" {
						<-release
					}
				},
				TLSHandshakeDone: func(state tls.ConnectionState, err error) {
					record.done(state, err)
					close(doneEntered)
					if name == "SlowDoneSuccess" {
						<-release
					}
					close(traceReturned)
				},
			}
			ctx := httptrace.WithClientTrace(parent, trace)
			results := make(chan customTLSBoundaryResult, 1)
			go func() { results <- customTLSBoundaryCall(ctx, peer, callback, timeout) }()
			var result customTLSBoundaryResult
			if name == "SlowStart" {
				customTLSWait(t, startEntered, "slow Start entered")
				result = customTLSBoundaryReceive(t, results)
				_, dones, _, _ := record.snapshot()
				if dones != 0 {
					t.Fatal("Done overtook the still-blocked Start hook")
				}
				releaseHook()
			} else if name == "SlowDoneSuccess" {
				customTLSWait(t, doneEntered, "selected success entered slow Done")
				// An already committed genuine result must survive its old
				// deadline, independently of when the owner can return.
				time.Sleep(500 * time.Millisecond)
				select {
				case early := <-results:
					t.Fatalf("owner bypassed slow Done or changed selected success: %v", early.err)
				default:
				}
				releaseHook()
				result = customTLSBoundaryReceive(t, results)
			} else {
				result = customTLSBoundaryReceive(t, results)
			}
			if result.conn != nil {
				t.Cleanup(func() { result.conn.Close() })
			}
			customTLSWait(t, callbackDone, "controlled callback returned")
			customTLSWait(t, traceReturned, "terminal trace returned")
			record.assertOrdered(t)
			starts, dones, state, traceErr := record.snapshot()
			if starts != 1 || dones != 1 || !errors.Is(traceErr, result.err) {
				t.Fatalf("terminal trace mismatch: starts=%d dones=%d trace=%v result=%v", starts, dones, traceErr, result.err)
			}
			success := name == "NoTimeoutSuccess" || name == "SlowDoneSuccess"
			if success {
				if result.err != nil || result.conn == nil || result.state == nil || !result.state.HandshakeComplete || !state.HandshakeComplete {
					t.Fatalf("genuine success changed: result=%+v trace=%+v", result, state)
				}
				customTLSBoundaryExchange(t, result.conn)
			} else {
				if result.err == nil || state.HandshakeComplete {
					t.Fatalf("failure published success: result=%+v trace=%+v", result, state)
				}
				switch name {
				case "CallbackError":
					if result.err != callbackError {
						t.Fatalf("callback primary error replaced: %v", result.err)
					}
				case "ParentCanceled":
					if result.err != context.Canceled {
						t.Fatalf("caller cancellation identity changed: %v", result.err)
					}
				case "ParentDeadline":
					if result.err != context.DeadlineExceeded {
						t.Fatalf("caller deadline identity changed: %T %v", result.err, result.err)
					}
				case "NilConnection", "TypedNilConnection", "NilState":
					if !strings.Contains(result.err.Error(), "nil connection or state") {
						t.Fatalf("invalid callback shape did not receive its explicit error: %v", result.err)
					}
				case "NonTLSConnection":
					if !strings.Contains(result.err.Error(), "non-TLS connection") {
						t.Fatalf("non-TLS callback shape did not receive its explicit error: %v", result.err)
					}
				case "HandshakeTimeout", "LateError", "SlowStart", "ExpiredBeforeStart":
					var ownTimeout tlsHandshakeTimeoutError
					if !errors.As(result.err, &ownTimeout) {
						t.Fatalf("own handshake deadline lost typed timeout: %T %v", result.err, result.err)
					}
				}
			}
			select {
			case actual := <-info:
				if actual.err != nil || !actual.state.HandshakeComplete {
					t.Fatalf("CUSTOM_TLS_PREREQUISITE: actual TLS handshake failed: %v", actual.err)
				}
				t.Cleanup(func() { actual.conn.Close() })
				if !success && name != "NilConnection" && name != "TypedNilConnection" && name != "NonTLSConnection" {
					customTLSWait(t, actual.conn.closed, "returned TLS connection cleanup")
				}
			default:
				if name != "ParentDeadline" && name != "HandshakeTimeout" && name != "SlowStart" && name != "ExpiredBeforeStart" {
					t.Fatal("CUSTOM_TLS_PREREQUISITE: real handshake witness missing")
				}
			}
			select {
			case addr := <-rawAddr:
				peer.waitClosed(t, addr)
			case <-time.After(6 * time.Second):
				t.Fatal("actual raw connection witness missing")
			}
		})
	}
}

func customTLSBoundaryReceive(t *testing.T, results <-chan customTLSBoundaryResult) customTLSBoundaryResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(6 * time.Second):
		t.Fatal("custom TLS owner did not return")
		return customTLSBoundaryResult{}
	}
}

func customTLSBoundaryExchange(t *testing.T, conn net.Conn) {
	t.Helper()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	request, err := http.NewRequest(http.MethodPost, "https://actual-peer/boundary", strings.NewReader("boundary-payload"))
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Write(conn); err != nil {
		t.Fatalf("real accepted TLS request failed: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		t.Fatalf("real accepted TLS response failed: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "actual-custom-tls:boundary-payload" {
		t.Fatalf("real accepted TLS body changed: status=%d body=%q err=%v", response.StatusCode, body, err)
	}
	conn.Close()
}

func customTLSBoundaryCall(ctx context.Context, peer *customTLSPeer, callback func(context.Context, string, net.Conn) (net.Conn, *tls.ConnectionState, error), timeout time.Duration) customTLSBoundaryResult {
	t := &Transport{Options: &transport.Options{TLSHandshakeContext: callback, TLSHandshakeTimeout: timeout}}
	conn, err := t.dialTLSWithContext(ctx, "tcp", peer.server.Listener.Addr().String(), nil)
	result := customTLSBoundaryResult{conn: conn, err: err}
	if err == nil {
		state := conn.ConnectionState()
		result.state = &state
	}
	return result
}
