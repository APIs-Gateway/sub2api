package http2

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	reqtls "github.com/imroc/req/v3/pkg/tls"
)

type customTLSWire struct {
	method   string
	body     string
	protocol int
	remote   string
}

type customTLSPeer struct {
	server   *httptest.Server
	requests chan customTLSWire
	mu       sync.Mutex
	closed   map[string]bool
	changed  chan struct{}
}

func newCustomTLSPeer(t *testing.T, protocol int) *customTLSPeer {
	t.Helper()
	p := &customTLSPeer{requests: make(chan customTLSWire, 16), closed: make(map[string]bool), changed: make(chan struct{}, 32)}
	p.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "incomplete request body", http.StatusBadRequest)
			return
		}
		p.requests <- customTLSWire{method: r.Method, body: string(body), protocol: r.ProtoMajor, remote: r.RemoteAddr}
		w.Header().Set("X-Custom-TLS-Peer", "actual")
		io.WriteString(w, "actual-custom-tls:"+string(body))
	}))
	p.server.EnableHTTP2 = protocol == 2
	p.server.Config.ErrorLog = log.New(io.Discard, "", 0)
	p.server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			p.mu.Lock()
			p.closed[conn.RemoteAddr().String()] = true
			p.mu.Unlock()
			select {
			case p.changed <- struct{}{}:
			default:
			}
		}
	}
	p.server.StartTLS()
	t.Cleanup(p.server.Close)
	return p
}

func customTLSWait(t *testing.T, done <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatalf("CUSTOM_TLS_PREREQUISITE: timed out waiting for %s", label)
	}
}

func (p *customTLSPeer) waitClosed(t *testing.T, addr string) {
	t.Helper()
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	for {
		p.mu.Lock()
		closed := p.closed[addr]
		p.mu.Unlock()
		if closed {
			return
		}
		select {
		case <-p.changed:
		case <-timer.C:
			t.Fatalf("CUSTOM_TLS_RESOURCE: real peer connection %s did not close", addr)
		}
	}
}

type customTLSObservedConn struct {
	reqtls.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *customTLSObservedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

type customTLSRecord struct {
	mu     sync.Mutex
	starts int
	dones  int
	state  tls.ConnectionState
	err    error
	events []string
}

func (r *customTLSRecord) start() {
	r.mu.Lock()
	r.starts++
	r.events = append(r.events, "start")
	r.mu.Unlock()
}

func (r *customTLSRecord) done(state tls.ConnectionState, err error) {
	r.mu.Lock()
	r.dones++
	r.events = append(r.events, "done")
	r.state, r.err = state, err
	r.mu.Unlock()
}

func (r *customTLSRecord) snapshot() (int, int, tls.ConnectionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts, r.dones, r.state, r.err
}

func customTLSRealHandshake(ctx context.Context, _ string, raw net.Conn) (net.Conn, *tls.ConnectionState, error) {
	conn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}})
	if err := conn.HandshakeContext(ctx); err != nil {
		return conn, nil, err
	}
	state := conn.ConnectionState()
	if !state.HandshakeComplete {
		return conn, nil, fmt.Errorf("real TLS handshake did not complete")
	}
	return conn, &state, nil
}

func (r *customTLSRecord) assertOrdered(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) != 2 || r.events[0] != "start" || r.events[1] != "done" {
		t.Fatalf("trace order changed: %v", r.events)
	}
}

type customTLSLateResult struct {
	conn    *customTLSObservedConn
	state   tls.ConnectionState
	rawAddr string
	err     error
}
