package req

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type reqProxyLifecycleCapture struct {
	method, uri, host, auth, body string
	err                           error
}

func reqProxyLifecycleReceive[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("proxy fixture did not reach the expected wire event")
		var zero T
		return zero
	}
}

func TestReqProxyLifecycle_HTTPAbsoluteFormAndAuthIsolation(t *testing.T) {
	originRequests := make(chan reqProxyLifecycleCapture, 2)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		select {
		case originRequests <- reqProxyLifecycleCapture{method: r.Method, uri: r.RequestURI, auth: r.Header.Get("Proxy-Authorization"), body: string(body), err: err}:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(origin.Close)
	forward := &http.Transport{Proxy: nil}
	t.Cleanup(forward.CloseIdleConnections)
	proxyRequests := make(chan reqProxyLifecycleCapture, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		select {
		case proxyRequests <- reqProxyLifecycleCapture{method: r.Method, uri: r.RequestURI, host: r.Host, auth: r.Header.Get("Proxy-Authorization"), body: string(body), err: err}:
		case <-r.Context().Done():
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		next := r.Clone(r.Context())
		next.RequestURI = ""
		next.Body = io.NopCloser(bytes.NewReader(body))
		// A forwarding proxy consumes its own credentials. The additional
		// direct request below checks that the req client did not retain them.
		next.Header.Del("Proxy-Authorization")
		response, err := forward.RoundTrip(next)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(proxy.Close)
	client := C().EnableForceHTTP1().SetTimeout(5 * time.Second).SetCommonRetryCount(0)
	client.SetProxyURL(strings.Replace(proxy.URL, "http://", "http://fixture:password@", 1))
	t.Cleanup(client.GetTransport().CloseIdleConnections)
	request := client.R().SetBodyString("proxy-payload")
	response, err := request.Post(origin.URL + "/echo?via=proxy")
	if err != nil || response == nil || response.GetStatusCode() != http.StatusOK || response.String() != "proxy-payload" {
		t.Fatalf("proxied response: response=%v err=%v", response, err)
	}
	proxied := reqProxyLifecycleReceive(t, proxyRequests)
	original := reqProxyLifecycleReceive(t, originRequests)
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("fixture:password"))
	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	if proxied.err != nil || proxied.method != "POST" || proxied.uri != origin.URL+"/echo?via=proxy" || proxied.host != originURL.Host || proxied.auth != wantAuth || proxied.body != "proxy-payload" {
		t.Fatalf("actual proxy request: %+v", proxied)
	}
	if original.err != nil || original.method != "POST" || original.uri != "/echo?via=proxy" || original.auth != "" || original.body != "proxy-payload" {
		t.Fatalf("actual forwarded origin request: %+v", original)
	}
	if request.RawRequest == nil {
		t.Fatal("real request was not retained")
	}
	if request.Headers.Get("Proxy-Authorization") != "" || request.RawRequest.Header.Get("Proxy-Authorization") != "" {
		t.Fatal("proxy authentication mutated the caller's request headers")
	}
	client.GetTransport().CloseIdleConnections()
	client.GetTransport().SetProxy(nil)
	response, err = client.R().SetBodyString("direct-payload").Post(origin.URL + "/direct")
	if err != nil || response == nil || response.GetStatusCode() != http.StatusOK || response.String() != "direct-payload" {
		t.Fatalf("subsequent direct response: response=%v err=%v", response, err)
	}
	direct := reqProxyLifecycleReceive(t, originRequests)
	if direct.err != nil || direct.uri != "/direct" || direct.auth != "" || direct.body != "direct-payload" {
		t.Fatalf("proxy credentials leaked into subsequent direct request: %+v", direct)
	}
}

type reqProxyLifecycleConnectPeer struct {
	listener  net.Listener
	mu        sync.Mutex
	conn      net.Conn
	stopped   bool
	requests  chan reqProxyLifecycleCapture
	terminal  chan reqProxyLifecycleTerminal
	done      chan struct{}
	release   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

type reqProxyLifecycleTerminal struct {
	bytes int64
	err   error
}

func newReqProxyLifecycleConnectPeer(t *testing.T, reply string, closeImmediately, holdReply bool) (*reqProxyLifecycleConnectPeer, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &reqProxyLifecycleConnectPeer{listener: listener, requests: make(chan reqProxyLifecycleCapture, 1), terminal: make(chan reqProxyLifecycleTerminal, 1), done: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	go func() {
		defer close(p.done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		p.mu.Lock()
		if p.stopped {
			p.mu.Unlock()
			return
		}
		p.conn = conn
		p.mu.Unlock()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(conn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			p.requests <- reqProxyLifecycleCapture{err: err}
			return
		}
		p.requests <- reqProxyLifecycleCapture{method: request.Method, uri: request.RequestURI, host: request.Host, auth: request.Header.Get("Proxy-Authorization")}
		if holdReply {
			select {
			case <-p.release:
			case <-p.closed:
				return
			}
		}
		if closeImmediately {
			// Send response EOF while retaining the read side. Only an EOF
			// observed there proves that the client closed its failed tunnel.
			if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
				p.terminal <- reqProxyLifecycleTerminal{err: err}
				return
			}
		} else if reply != "" {
			if _, err := io.WriteString(conn, reply); err != nil {
				p.terminal <- reqProxyLifecycleTerminal{err: err}
				return
			}
		}
		// EOF here observes the client actually retiring the failed/canceled
		// tunnel; a deadline is a failure, never evidence of successful close.
		n, err := io.Copy(io.Discard, reader)
		p.terminal <- reqProxyLifecycleTerminal{bytes: n, err: err}
	}()
	t.Cleanup(func() {
		p.stop()
		reqProxyLifecycleReceive(t, p.done)
	})
	return p, "http://fixture:password@" + listener.Addr().String()
}

func (p *reqProxyLifecycleConnectPeer) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	p.closeOnce.Do(func() { close(p.closed) })
	_ = p.listener.Close()
	if p.conn != nil {
		_ = p.conn.Close()
	}
}

func reqProxyLifecycleAssertConnect(t *testing.T, capture reqProxyLifecycleCapture) {
	t.Helper()
	if capture.err != nil || capture.method != "CONNECT" || capture.uri != "origin.fixture:443" || capture.host != "origin.fixture:443" || capture.auth != "Basic "+base64.StdEncoding.EncodeToString([]byte("fixture:password")) {
		t.Fatalf("actual CONNECT request: %+v", capture)
	}
}

func reqProxyLifecycleAssertRetired(t *testing.T, peer *reqProxyLifecycleConnectPeer) {
	t.Helper()
	terminal := reqProxyLifecycleReceive(t, peer.terminal)
	if terminal.err != nil || terminal.bytes != 0 {
		t.Fatalf("tunnel retirement: post-CONNECT bytes=%d error=%v", terminal.bytes, terminal.err)
	}
}

func TestReqProxyLifecycle_CONNECTFailureClosesSocket(t *testing.T) {
	for _, scenario := range []struct {
		name, reply      string
		closeImmediately bool
	}{
		{name: "authentication_rejected", reply: "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n"},
		{name: "malformed_response", reply: "HTTP/1.1 invalid status\r\n\r\n"},
		{name: "early_close", closeImmediately: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			peer, proxyURL := newReqProxyLifecycleConnectPeer(t, scenario.reply, scenario.closeImmediately, false)
			client := C().SetProxyURL(proxyURL).SetTimeout(5 * time.Second).SetCommonRetryCount(0)
			t.Cleanup(client.GetTransport().CloseIdleConnections)
			_, err := client.R().Get("https://origin.fixture/fail")
			if err == nil {
				t.Fatal("failed CONNECT unexpectedly returned a response")
			}
			if scenario.name == "authentication_rejected" && !strings.Contains(err.Error(), "Proxy Authentication Required") {
				t.Fatalf("CONNECT status error: %v", err)
			}
			if scenario.name == "malformed_response" && !strings.Contains(err.Error(), "malformed HTTP status code") {
				t.Fatalf("CONNECT malformed response error: %v", err)
			}
			if scenario.closeImmediately && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("CONNECT early-close error: %v", err)
			}
			reqProxyLifecycleAssertConnect(t, reqProxyLifecycleReceive(t, peer.requests))
			reqProxyLifecycleAssertRetired(t, peer)
		})
	}
}

func TestReqProxyLifecycle_CONNECTCallbackRejectsBeforeTLS(t *testing.T) {
	peer, proxyURL := newReqProxyLifecycleConnectPeer(t, "HTTP/1.1 200 Connection Established\r\n\r\n", false, false)
	client := C().SetProxyURL(proxyURL).SetTimeout(5 * time.Second).SetCommonRetryCount(0)
	t.Cleanup(client.GetTransport().CloseIdleConnections)
	rejected := errors.New("fixture rejected CONNECT response")
	callbacks := make(chan int, 1)
	var callbackCount atomic.Int32
	client.GetTransport().OnProxyConnectResponse = func(_ context.Context, _ *url.URL, request *http.Request, response *http.Response) error {
		callbackCount.Add(1)
		status := response.StatusCode
		if request.Method != "CONNECT" {
			status = -1
		}
		select {
		case callbacks <- status:
		default:
		}
		return rejected
	}
	_, err := client.R().Get("https://origin.fixture/callback")
	if !errors.Is(err, rejected) {
		t.Fatalf("CONNECT callback error: %v", err)
	}
	reqProxyLifecycleAssertConnect(t, reqProxyLifecycleReceive(t, peer.requests))
	if status := reqProxyLifecycleReceive(t, callbacks); status != http.StatusOK {
		t.Fatalf("callback response status: %d", status)
	}
	if count := callbackCount.Load(); count != 1 {
		t.Fatalf("CONNECT callback count: %d", count)
	}
	reqProxyLifecycleAssertRetired(t, peer)
}

func TestReqProxyLifecycle_CONNECTCancellationReturnsBeforeProxyReply(t *testing.T) {
	peer, proxyURL := newReqProxyLifecycleConnectPeer(t, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n", false, true)
	client := C().SetProxyURL(proxyURL).SetTimeout(5 * time.Second).SetCommonRetryCount(0)
	t.Cleanup(client.GetTransport().CloseIdleConnections)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var requestErr error
	go func() {
		defer close(done)
		_, requestErr = client.R().SetContext(ctx).Get("https://origin.fixture/cancel")
	}()
	t.Cleanup(func() { cancel(); peer.stop(); reqProxyLifecycleReceive(t, done) })
	// Only cancel after the proxy has parsed the actual complete CONNECT.
	reqProxyLifecycleAssertConnect(t, reqProxyLifecycleReceive(t, peer.requests))
	cancel()
	reqProxyLifecycleReceive(t, done)
	if !errors.Is(requestErr, context.Canceled) {
		t.Fatalf("CONNECT cancellation error: %v", requestErr)
	}
	// getConn deliberately detaches the dial from request cancellation for
	// possible reuse. A canceled request must return before this proxy reply;
	// the later failed handshake must retire that detached connection.
	close(peer.release)
	reqProxyLifecycleAssertRetired(t, peer)
}
