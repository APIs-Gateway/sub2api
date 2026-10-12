package repository

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

type nativeHTTP2WireEvent struct { kind string; connection int; err error }
type nativeHTTP2WirePeer struct {
	listener net.Listener
	roots *x509.CertPool
	certificate tls.Certificate
	events chan nativeHTTP2WireEvent
	stop chan struct{}
	mu sync.Mutex
	connections map[net.Conn]int
	workers sync.WaitGroup
	ack bool
	hold <-chan struct{}
}

func newNativeHTTP2WirePeer(t *testing.T, ack bool, hold <-chan struct{}) *nativeHTTP2WirePeer {
	t.Helper()
	seed := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	roots := x509.NewCertPool()
	roots.AddCert(seed.Certificate())
	certificate := seed.TLS.Certificates[0]
	seed.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS12})
	require.NoError(t, err)
	peer := &nativeHTTP2WirePeer{listener: listener, roots: roots, certificate: certificate, events: make(chan nativeHTTP2WireEvent, 128), stop: make(chan struct{}), connections: make(map[net.Conn]int), ack: ack, hold: hold}
	peer.workers.Add(1)
	go func() {
		defer peer.workers.Done()
		id := 0
		for {
			conn, err := listener.Accept()
			if err != nil { return }
			peer.mu.Lock()
			select {
			case <-peer.stop:
				peer.mu.Unlock()
				_ = conn.Close()
				return
			default:
			}
			id++
			peer.connections[conn] = id
			peer.workers.Add(1)
			peer.mu.Unlock()
			go peer.serve(conn, id)
		}
	}()
	t.Cleanup(func() {
		close(peer.stop)
		_ = listener.Close()
		peer.mu.Lock()
		for conn := range peer.connections { _ = conn.Close() }
		peer.mu.Unlock()
		joined := make(chan struct{})
		go func() { peer.workers.Wait(); close(joined) }()
		select { case <-joined: case <-time.After(7*time.Second): t.Error("owned native H2 peers did not join") }
	})
	return peer
}

func (p *nativeHTTP2WirePeer) serve(conn net.Conn, id int) {
	defer p.workers.Done()
	defer func() { _ = conn.Close(); p.mu.Lock(); delete(p.connections, conn); p.mu.Unlock() }()
	result := func(kind string, err error) { p.events <- nativeHTTP2WireEvent{kind: kind, connection: id, err: err} }
	if err := conn.SetDeadline(time.Now().Add(6*time.Second)); err != nil { result("closed", err); return }
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil { result("closed", err); return }
	if string(preface) != http2.ClientPreface || conn.(*tls.Conn).ConnectionState().NegotiatedProtocol != "h2" { result("closed", errors.New("missing actual h2 ALPN/preface")); return }
	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(); err != nil { result("closed", err); return }
	for {
		frame, err := framer.ReadFrame()
		if err != nil { result("closed", err); return }
		switch frame := frame.(type) {
		case *http2.SettingsFrame:
			if !frame.IsAck() { if err := framer.WriteSettingsAck(); err != nil { result("closed", err); return } }
		case *http2.PingFrame:
			if !frame.IsAck() {
				if p.ack { if err := framer.WritePing(true, frame.Data); err != nil { result("closed", err); return } }
				result("ping", nil)
			}
		case *http2.HeadersFrame:
			if !frame.HeadersEnded() { result("closed", errors.New("unexpected split headers in local fixture")); return }
			result("headers", nil)
			if p.hold != nil {
				select { case <-p.hold: case <-p.stop: return }
			}
			var block bytes.Buffer
			encoder := hpack.NewEncoder(&block)
			for _, field := range []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "content-length", Value: "2"}} {
				if err := encoder.WriteField(field); err != nil { result("closed", err); return }
			}
			if err := framer.WriteHeaders(http2.HeadersFrameParam{StreamID: frame.StreamID, EndHeaders: true, BlockFragment: block.Bytes()}); err != nil { result("closed", err); return }
			if err := framer.WriteData(frame.StreamID, true, []byte("ok")); err != nil { result("closed", err); return }
		}
	}
}

func (p *nativeHTTP2WirePeer) wait(t *testing.T, kind string) nativeHTTP2WireEvent {
	t.Helper()
	deadline := time.NewTimer(5*time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-p.events:
			if event.kind == kind { return event }
			if event.err != nil { t.Fatalf("wire peer failed while waiting for %s: %v", kind, event.err) }
		case <-deadline.C: t.Fatalf("no actual wire event %s", kind); return nativeHTTP2WireEvent{}
		}
	}
}

func nativeHTTP2TestTransport(t *testing.T, peer *nativeHTTP2WirePeer, proxy *url.URL) *http.Transport {
	t.Helper()
	transport, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), proxy, upstreamProtocolModeOpenAIH2)
	require.NoError(t, err)
	transport.TLSClientConfig = &tls.Config{RootCAs: peer.roots, MinVersion: tls.VersionTLS12}
	// Only the fixture duration is shortened; production constants stay 15s.
	transport.HTTP2.SendPingTimeout = 100*time.Millisecond
	transport.HTTP2.PingTimeout = time.Second
	t.Cleanup(transport.CloseIdleConnections)
	return transport
}

func nativeHTTP2TestGET(t *testing.T, transport *http.Transport, endpoint string) httptrace.GotConnInfo {
	t.Helper()
	var info httptrace.GotConnInfo
	trace := &httptrace.ClientTrace{GotConn: func(got httptrace.GotConnInfo) { info = got }}
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, endpoint, nil)
	require.NoError(t, err)
	response, err := (&http.Client{Transport: transport, Timeout: 4*time.Second}).Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, 2, response.ProtoMajor)
	require.Equal(t, "ok", string(body))
	return info
}

func testNativeOpenAIHTTP2PingACK(t *testing.T) {
	peer := newNativeHTTP2WirePeer(t, true, nil)
	transport := nativeHTTP2TestTransport(t, peer, nil)
	endpoint := "https://"+peer.listener.Addr().String()+"/models"
	first := nativeHTTP2TestGET(t, transport, endpoint)
	require.False(t, first.Reused)
	firstPing := peer.wait(t, "ping")
	require.NoError(t, firstPing.err)
	// An immediately reused socket would not prove ACK processing: a client
	// ignoring ACK could still retire it at PingTimeout. Observe another real
	// PING on this socket after a complete timeout interval has elapsed.
	surviveUntil := time.Now().Add(transport.HTTP2.PingTimeout+200*time.Millisecond)
	for {
		ping := peer.wait(t, "ping")
		require.Equal(t, firstPing.connection, ping.connection)
		if !time.Now().Before(surviveUntil) { break }
	}
	second := nativeHTTP2TestGET(t, transport, endpoint)
	require.True(t, second.Reused, "ACK must keep the actual H2 socket reusable")
	require.Same(t, first.Conn, second.Conn)
}

func testNativeOpenAIHTTP2PingRetirement(t *testing.T) {
	peer := newNativeHTTP2WirePeer(t, false, nil)
	transport := nativeHTTP2TestTransport(t, peer, nil)
	endpoint := "https://"+peer.listener.Addr().String()+"/models"
	first := nativeHTTP2TestGET(t, transport, endpoint)
	require.NoError(t, peer.wait(t, "ping").err)
	closed := peer.wait(t, "closed")
	require.True(t, errors.Is(closed.err, io.EOF) || errors.Is(closed.err, net.ErrClosed), "missing ACK must retire the client socket, not time out the fixture: %v", closed.err)
	second := nativeHTTP2TestGET(t, transport, endpoint)
	require.False(t, second.Reused)
	require.NotSame(t, first.Conn, second.Conn)
}

func testNativeOpenAIHTTP1Fallback(t *testing.T) {
	for _, supportsH2 := range []bool{false, true} {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, strconv.Itoa(r.ProtoMajor)) }))
	server.EnableHTTP2 = supportsH2
	server.StartTLS()
	t.Cleanup(server.Close)
	for _, mode := range []string{upstreamProtocolModeOpenAIH2, upstreamProtocolModeOpenAIH1, upstreamProtocolModeOpenAIH1Fallback, upstreamProtocolModeDefault} {
		t.Run(fmt.Sprintf("%s/serverH2=%t", mode, supportsH2), func(t *testing.T) {
			transport, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, mode)
			require.NoError(t, err)
			transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			defer transport.CloseIdleConnections()
			response, err := (&http.Client{Transport: transport, Timeout: 4*time.Second}).Get(server.URL)
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			major := 1
			if supportsH2 && mode == upstreamProtocolModeOpenAIH2 { major = 2 }
			require.Equal(t, major, response.ProtoMajor)
			require.Equal(t, strconv.Itoa(major), string(body))
			if supportsH2 {
				alpn := "http/1.1"; if major == 2 { alpn = "h2" }
				require.Equal(t, alpn, response.TLS.NegotiatedProtocol)
			}
		})
	}
	}
}

func testNativeOpenAIHTTP2CustomTLSAndCloseIdle(t *testing.T) {
	release := make(chan struct{})
	peer := newNativeHTTP2WirePeer(t, true, release)
	transport := nativeHTTP2TestTransport(t, peer, nil)
	var dials atomic.Int32
	transport.DialTLSContext = withUpstreamTLSHandshakeTimeout(func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		dialer := tls.Dialer{NetDialer: newUpstreamDialer(), Config: &tls.Config{RootCAs: peer.roots, NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12}}
		return dialer.DialContext(ctx, network, address)
	})
	result := make(chan error, 1)
	requestJoined := false
	go func() {
		response, err := (&http.Client{Transport: transport, Timeout: 4*time.Second}).Get("https://"+peer.listener.Addr().String()+"/models")
		if err != nil { result <- err; return }
		body, err := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if err == nil { err = closeErr }
		if err == nil && (response.ProtoMajor != 2 || string(body) != "ok") { err = errors.New("active H2 response was lost") }
		result <- err
	}()
	t.Cleanup(func() {
		if requestJoined { return }
		select { case <-result: case <-time.After(5*time.Second): t.Error("owned custom TLS request did not join during cleanup") }
	})
	peer.wait(t, "headers")
	transport.CloseIdleConnections()
	close(release)
	select { case err := <-result: requestJoined = true; require.NoError(t, err); case <-time.After(5*time.Second): t.Fatal("owned active request did not finish") }
	require.GreaterOrEqual(t, dials.Load(), int32(1))
	// Stream cleanup can finish after Body.Close. Retry the idle-only operation
	// until the peer sees closure, with a bound shorter than its own deadline.
	deadline := time.NewTimer(2*time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10*time.Millisecond)
	defer tick.Stop()
	var closed nativeHTTP2WireEvent
idleClose:
	for {
		transport.CloseIdleConnections()
		select {
		case event := <-peer.events:
			if event.kind == "closed" { closed = event; break idleClose }
		case <-tick.C:
		case <-deadline.C: t.Fatal("idle CloseIdleConnections did not close the wire before the peer deadline")
		}
	}
	require.True(t, errors.Is(closed.err, io.EOF) || errors.Is(closed.err, net.ErrClosed))
	nativeHTTP2TestGET(t, transport, "https://"+peer.listener.Addr().String()+"/models")
	require.GreaterOrEqual(t, dials.Load(), int32(2))
}

func nativeHTTP2Tunnel(client net.Conn, clientInput io.Reader, address string) {
	upstream, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil { _ = client.Close(); return }
	_ = client.SetDeadline(time.Now().Add(6*time.Second))
	_ = upstream.SetDeadline(time.Now().Add(6*time.Second))
	completed := make(chan struct{}, 1)
	go func() { _, _ = io.Copy(upstream, clientInput); _ = upstream.Close(); completed <- struct{}{} }()
	_, _ = io.Copy(client, upstream)
	_ = client.Close()
	_ = upstream.Close()
	<-completed
}

func testNativeOpenAIHTTP2Proxy(t *testing.T, scheme string) {
	peer := newNativeHTTP2WirePeer(t, true, nil)
	target := peer.listener.Addr().String()
	var proxyURL *url.URL
	var workers sync.WaitGroup
	var relayMu sync.Mutex
	relayClients := make(map[net.Conn]struct{})
	register := func(client net.Conn) {
		relayMu.Lock(); relayClients[client] = struct{}{}; relayMu.Unlock()
		_ = client.SetDeadline(time.Now().Add(6*time.Second))
	}
	unregister := func(client net.Conn) { relayMu.Lock(); delete(relayClients, client); relayMu.Unlock(); _ = client.Close() }
	join := func() {
		relayMu.Lock(); for client := range relayClients { _ = client.Close() }; relayMu.Unlock()
		joined := make(chan struct{}); go func() { workers.Wait(); close(joined) }()
		select { case <-joined: case <-time.After(7*time.Second): t.Error("owned proxy relay did not join") }
	}
	if scheme == "socks5h" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		proxyURL, err = url.Parse("socks5h://"+listener.Addr().String())
		require.NoError(t, err)
		workers.Add(1)
		go func() {
			defer workers.Done()
			client, err := listener.Accept()
			if err != nil { return }
			register(client)
			defer unregister(client)
			greeting := make([]byte, 2)
			if _, err := io.ReadFull(client, greeting); err != nil { _ = client.Close(); return }
			if _, err := io.CopyN(io.Discard, client, int64(greeting[1])); err != nil { _ = client.Close(); return }
			if _, err := client.Write([]byte{5,0}); err != nil { _ = client.Close(); return }
			request := make([]byte, 4)
			if _, err := io.ReadFull(client, request); err != nil { _ = client.Close(); return }
			var host string
			switch request[3] {
			case 1:
				ip := make([]byte, 4); if _, err := io.ReadFull(client, ip); err != nil { _ = client.Close(); return }; host = net.IP(ip).String()
			case 3:
				length := make([]byte, 1); if _, err := io.ReadFull(client, length); err != nil { _ = client.Close(); return }
				domain := make([]byte, int(length[0])); if _, err := io.ReadFull(client, domain); err != nil { _ = client.Close(); return }; host = string(domain)
			default: _ = client.Close(); return
			}
			port := make([]byte, 2); if _, err := io.ReadFull(client, port); err != nil { _ = client.Close(); return }
			if net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))) != target { _ = client.Close(); return }
			if _, err := client.Write([]byte{5,0,0,1,127,0,0,1,0,0}); err != nil { _ = client.Close(); return }
			nativeHTTP2Tunnel(client, client, target)
		}()
		t.Cleanup(func() { _ = listener.Close(); join() })
	} else {
		proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodConnect || r.Host != target { w.WriteHeader(http.StatusBadRequest); return }
			workers.Add(1)
			defer workers.Done()
			client, buffered, err := w.(http.Hijacker).Hijack()
			if err != nil { return }
			register(client)
			defer unregister(client)
			if _, err := fmt.Fprint(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil { return }
			nativeHTTP2Tunnel(client, buffered.Reader, target)
		}))
		if scheme == "https" {
			proxy.StartTLS()
			peer.roots.AddCert(proxy.Certificate())
		} else { proxy.Start() }
		var err error
		proxyURL, err = url.Parse(proxy.URL)
		require.NoError(t, err)
		t.Cleanup(func() { proxy.Close(); join() })
	}
	transport := nativeHTTP2TestTransport(t, peer, proxyURL)
	first := nativeHTTP2TestGET(t, transport, "https://"+target+"/models")
	peer.wait(t, "ping")
	second := nativeHTTP2TestGET(t, transport, "https://"+target+"/models")
	require.True(t, second.Reused)
	require.Same(t, first.Conn, second.Conn)
}
