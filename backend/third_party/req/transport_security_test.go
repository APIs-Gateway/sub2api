package req

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	reqhttp2 "github.com/imroc/req/v3/http2"
	xhttp2 "golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

type reqSecurityFlight struct {
	alpn           string
	settings       []xhttp2.Setting
	window         uint32
	priorities     []xhttp2.PriorityFrame
	pseudo         []string
	headerNames    []string
	userAgent      string
	headerPriority xhttp2.PriorityParam
}
type reqSecurityRequest struct {
	id   uint32
	path string
	body string
}
type reqSecurityTLSPeer struct {
	url         string
	flight      chan reqSecurityFlight
	requests    chan reqSecurityRequest
	resets      chan uint32
	errors      chan error
	connections atomic.Int32
}

func newReqSecurityTLSPeer(t *testing.T) *reqSecurityTLSPeer {
	t.Helper()
	certificateSource := httptest.NewTLSServer(http.NotFoundHandler())
	certificates := certificateSource.TLS.Certificates
	certificateSource.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: certificates, NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	p := &reqSecurityTLSPeer{url: "https://" + listener.Addr().String(), flight: make(chan reqSecurityFlight, 4), requests: make(chan reqSecurityRequest, 8), resets: make(chan uint32, 8), errors: make(chan error, 8)}
	var mu sync.Mutex
	var conns []net.Conn
	var workers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			p.connections.Add(1)
			workers.Add(1)
			go func() { defer workers.Done(); defer conn.Close(); p.serve(conn) }()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-acceptDone
		mu.Lock()
		for _, conn := range conns {
			conn.Close()
		}
		mu.Unlock()
		joined := make(chan struct{})
		go func() { workers.Wait(); close(joined) }()
		select {
		case <-joined:
		case <-time.After(2 * time.Second):
			t.Error("TLS peer handlers did not join")
		}
	})
	return p
}
func (p *reqSecurityTLSPeer) serve(conn net.Conn) {
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	tlsConn := conn.(*tls.Conn)
	if err := tlsConn.Handshake(); err != nil {
		p.errors <- err
		return
	}
	preface := make([]byte, len(xhttp2.ClientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil {
		p.errors <- err
		return
	}
	if string(preface) != xhttp2.ClientPreface {
		p.errors <- fmt.Errorf("wrong H2 preface")
		return
	}
	reader := xhttp2.NewFramer(io.Discard, conn)
	reader.ReadMetaHeaders = hpack.NewDecoder(65536, nil)
	writer := xhttp2.NewFramer(conn, bytes.NewReader(nil))
	flight := reqSecurityFlight{alpn: tlsConn.ConnectionState().NegotiatedProtocol}
	sentFlight := false
	requests := map[uint32]reqSecurityRequest{}
	respond := func(id uint32) error {
		var block bytes.Buffer
		encoder := hpack.NewEncoder(&block)
		if err := encoder.WriteField(hpack.HeaderField{Name: ":status", Value: "200"}); err != nil {
			return err
		}
		if err := encoder.WriteField(hpack.HeaderField{Name: "content-length", Value: "2"}); err != nil {
			return err
		}
		if err := writer.WriteHeaders(xhttp2.HeadersFrameParam{StreamID: id, EndHeaders: true, BlockFragment: block.Bytes()}); err != nil {
			return err
		}
		return writer.WriteData(id, true, []byte("ok"))
	}
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			return
		}
		switch frame := frame.(type) {
		case *xhttp2.SettingsFrame:
			if !frame.IsAck() {
				frame.ForeachSetting(func(s xhttp2.Setting) error { flight.settings = append(flight.settings, s); return nil })
				if err := writer.WriteSettings(xhttp2.Setting{ID: xhttp2.SettingMaxConcurrentStreams, Val: 100}); err != nil {
					p.errors <- err
					return
				}
				if err := writer.WriteSettingsAck(); err != nil {
					p.errors <- err
					return
				}
			}
		case *xhttp2.WindowUpdateFrame:
			if !sentFlight && frame.StreamID == 0 {
				flight.window = frame.Increment
			}
		case *xhttp2.PriorityFrame:
			if !sentFlight {
				flight.priorities = append(flight.priorities, *frame)
			}
		case *xhttp2.MetaHeadersFrame:
			r := reqSecurityRequest{id: frame.StreamID, path: frame.PseudoValue("path")}
			requests[r.id] = r
			if !sentFlight {
				for _, f := range frame.Fields {
					if strings.HasPrefix(f.Name, ":") {
						flight.pseudo = append(flight.pseudo, f.Name)
					} else {
						flight.headerNames = append(flight.headerNames, f.Name)
					}
					if f.Name == "user-agent" {
						flight.userAgent = f.Value
					}
				}
				flight.headerPriority = frame.Priority
				p.flight <- flight
				sentFlight = true
			}
			if r.path == "/cancel" {
				p.requests <- r
				continue
			}
			if frame.StreamEnded() {
				p.requests <- r
				if err := respond(r.id); err != nil {
					p.errors <- err
					return
				}
				delete(requests, r.id)
			}
		case *xhttp2.DataFrame:
			r := requests[frame.StreamID]
			r.body += string(frame.Data())
			if len(r.body) > 64 {
				p.errors <- fmt.Errorf("fixture body over64bytes")
				return
			}
			requests[r.id] = r
			if frame.StreamEnded() {
				p.requests <- r
				if err := respond(r.id); err != nil {
					p.errors <- err
					return
				}
				delete(requests, r.id)
			}
		case *xhttp2.RSTStreamFrame:
			p.resets <- frame.StreamID
			delete(requests, frame.StreamID)
		case *xhttp2.PingFrame:
			if !frame.IsAck() {
				if err := writer.WritePing(true, frame.Data); err != nil {
					p.errors <- err
					return
				}
			}
		}
	}
}
func reqSecurityReceive[T any](t *testing.T, values <-chan T, errors <-chan error) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case err := <-errors:
		t.Fatalf("peer setup/protocol error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("fixture guard expired (not behavioral red evidence)")
	}
	var zero T
	return zero
}

func newReqSecurityConnectProxy(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	count := &atomic.Int32{}
	var workers sync.WaitGroup
	var mu sync.Mutex
	var tunnels []net.Conn
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("fixture:password")) {
			http.Error(w, "proxy authentication rejected", http.StatusProxyAuthRequired)
			return
		}
		upstream, err := net.DialTimeout("tcp", r.Host, 2*time.Second)
		if err != nil {
			http.Error(w, "dial", http.StatusBadGateway)
			return
		}
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		upstream.SetDeadline(time.Now().Add(10 * time.Second))
		mu.Lock()
		tunnels = append(tunnels, conn, upstream)
		mu.Unlock()
		count.Add(1)
		buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		if err := buffer.Flush(); err != nil {
			conn.Close()
			upstream.Close()
			return
		}
		workers.Add(1)
		defer workers.Done()
		defer conn.Close()
		defer upstream.Close()
		backDone := make(chan struct{})
		go func() { defer close(backDone); io.Copy(conn, upstream); conn.Close() }()
		io.Copy(upstream, buffer)
		upstream.Close()
		<-backDone
	}))
	t.Cleanup(func() {
		proxy.Close()
		mu.Lock()
		for _, conn := range tunnels {
			conn.Close()
		}
		mu.Unlock()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("CONNECT tunnel workers did not join")
		}
	})
	return strings.Replace(proxy.URL, "http://", "http://fixture:password@", 1), count
}

func TestReqHTTP2Security_PublicFirefoxTLSProxyAndReuse(t *testing.T) {
	for _, proxyMode := range []bool{false, true} {
		for _, custom := range []bool{false, true} {
			name := "direct"
			if proxyMode {
				name = "connect_auth"
			}
			if custom {
				name += "_custom"
			} else {
				name += "_firefox"
			}
			t.Run(name, func(t *testing.T) {
				peer := newReqSecurityTLSPeer(t)
				client := C().ImpersonateFirefox().EnableInsecureSkipVerify().SetTimeout(5 * time.Second).SetCommonRetryCount(0)
				client.GetTransport().SetProxy(nil)
				var connects *atomic.Int32
				if proxyMode {
					url, count := newReqSecurityConnectProxy(t)
					connects = count
					client.SetProxyURL(url)
				}
				if custom {
					client.SetHTTP2SettingsFrame(reqhttp2.Setting{ID: reqhttp2.SettingHeaderTableSize, Val: 65536}, reqhttp2.Setting{ID: reqhttp2.SettingInitialWindowSize, Val: 262144}, reqhttp2.Setting{ID: reqhttp2.SettingMaxFrameSize, Val: 16384}).SetHTTP2ConnectionFlow(900000)
				}
				t.Cleanup(client.GetTransport().CloseIdleConnections)
				for _, method := range []string{"GET", "POST"} {
					request := client.R()
					var response *Response
					var err error
					if method == "POST" {
						response, err = request.SetBodyBytes([]byte("hello")).Post(peer.url + "/echo")
					} else {
						response, err = request.Get(peer.url + "/echo")
					}
					if err != nil {
						t.Fatal(err)
					}
					if response.Response.ProtoMajor != 2 || response.String() != "ok" {
						t.Fatalf("public req response: %s %q", response.Response.Proto, response.String())
					}
					r := reqSecurityReceive(t, peer.requests, peer.errors)
					if r.path != "/echo" || (method == "POST" && r.body != "hello") {
						t.Fatalf("actual request payload: %+v", r)
					}
				}
				flight := reqSecurityReceive(t, peer.flight, peer.errors)
				wantWindow := uint32(12517377)
				wantInitial := uint32(131072)
				if custom {
					wantWindow = 900000
					wantInitial = 262144
				}
				wantSettings := []xhttp2.Setting{{ID: xhttp2.SettingHeaderTableSize, Val: 65536}, {ID: xhttp2.SettingInitialWindowSize, Val: wantInitial}, {ID: xhttp2.SettingMaxFrameSize, Val: 16384}}
				if flight.alpn != "h2" || flight.window != wantWindow || fmt.Sprint(flight.settings) != fmt.Sprint(wantSettings) {
					t.Fatalf("ALPN/settings/window changed: %+v", flight)
				}
				if strings.Join(flight.pseudo, ",") != ":method,:path,:authority,:scheme" {
					t.Fatalf("pseudo order changed: %v", flight.pseudo)
				}
				if flight.userAgent != firefoxHeaders["user-agent"] {
					t.Fatalf("Firefox user-agent changed: %q", flight.userAgent)
				}
				if len(flight.priorities) != 6 {
					t.Fatalf("priority frame count: %d", len(flight.priorities))
				}
				for i, frame := range flight.priorities {
					expected := firefoxPriorityFrames[i]
					if frame.StreamID != expected.StreamID || frame.StreamDep != expected.PriorityParam.StreamDep || frame.Exclusive != expected.PriorityParam.Exclusive || frame.Weight != expected.PriorityParam.Weight {
						t.Fatalf("priority %d changed: %+v", i, frame)
					}
				}
				if flight.headerPriority.StreamDep != 13 || flight.headerPriority.Weight != 41 || flight.headerPriority.Exclusive {
					t.Fatalf("header priority changed: %+v", flight.headerPriority)
				}
				// Verify declared common-header relative order on the actual HPACK wire.
				position := map[string]int{}
				for i, name := range firefoxHeaderOrder {
					position[name] = i
				}
				last := -1
				for _, name := range flight.headerNames {
					if pos, ok := position[name]; ok {
						if pos <= last {
							t.Fatalf("Firefox common-header order: %v", flight.headerNames)
						}
						last = pos
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { _, err := client.R().SetContext(ctx).Get(peer.url + "/cancel"); done <- err }()
				t.Cleanup(func() {
					cancel()
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Error("public cancellation request did not join")
					}
				})
				canceled := reqSecurityReceive(t, peer.requests, peer.errors)
				if canceled.path != "/cancel" {
					t.Fatalf("cancel path: %+v", canceled)
				}
				cancel()
				if reset := reqSecurityReceive(t, peer.resets, peer.errors); reset != canceled.id {
					t.Fatalf("cancel reset stream: %d want%d", reset, canceled.id)
				}
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("cancellation succeeded")
					}
					done <- err
				case <-time.After(2 * time.Second):
					t.Fatal("cancellation guard expired")
				}
				if peer.connections.Load() != 1 || (proxyMode && connects.Load() != 1) {
					t.Fatalf("connection reuse lost: TLS%d proxy%v", peer.connections.Load(), connects)
				}
			})
		}
	}
}
