package req

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Every peer owns its listener and accepted sockets. Closing them in cleanup
// releases both Accept and blocked reads before the peer goroutine is joined.
type reqH1LifecyclePeer struct {
	listener    net.Listener
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	stopping    bool
	finished    chan struct{}
	result      chan error
}

func newReqH1LifecyclePeer(t *testing.T, run func(*reqH1LifecyclePeer) error) *reqH1LifecyclePeer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &reqH1LifecyclePeer{listener: listener, connections: make(map[net.Conn]struct{}), finished: make(chan struct{}), result: make(chan error, 1)}
	t.Cleanup(func() {
		p.mu.Lock()
		p.stopping = true
		_ = p.listener.Close()
		for conn := range p.connections {
			_ = conn.Close()
		}
		p.mu.Unlock()
		select {
		case <-p.finished:
		case <-time.After(3 * time.Second):
			t.Error("HTTP/1 peer did not join after closing its sockets")
		}
	})
	go func() { defer close(p.finished); p.result <- run(p) }()
	return p
}

func (p *reqH1LifecyclePeer) accept() (net.Conn, error) {
	conn, err := p.listener.Accept()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	p.connections[conn] = struct{}{}
	p.mu.Unlock()
	if err := conn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (p *reqH1LifecyclePeer) join(t *testing.T) {
	t.Helper()
	select {
	case err := <-p.result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP/1 peer did not finish its wire assertions")
	}
	<-p.finished
}

func reqH1LifecycleClient(t *testing.T) *Client {
	t.Helper()
	client := C().EnableForceHTTP1().SetProxy(nil).SetTimeout(5 * time.Second)
	t.Cleanup(client.GetTransport().CloseIdleConnections)
	return client
}

func reqH1LifecycleReadRequest(reader *bufio.Reader, path string) (*http.Request, error) {
	r, err := http.ReadRequest(reader)
	if err != nil {
		return nil, err
	}
	if r.ProtoMajor != 1 || r.ProtoMinor != 1 || r.URL.Path != path {
		_ = r.Body.Close()
		return nil, fmt.Errorf("unexpected wire request: %s %s %s", r.Method, r.URL, r.Proto)
	}
	return r, nil
}

func reqH1LifecycleGet(t *testing.T, client *Client, ctx context.Context, address, path string) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestReqHTTP1Lifecycle_ResponseBody(t *testing.T) {
	for _, scenario := range []string{"chunked_trailers_reuse", "early_close", "cancel_read", "truncated_length", "truncated_chunk"} {
		t.Run(scenario, func(t *testing.T) {
			var firstAddress, secondAddress string
			peer := newReqH1LifecyclePeer(t, func(p *reqH1LifecyclePeer) error {
				conn, err := p.accept()
				if err != nil {
					return err
				}
				firstAddress = conn.RemoteAddr().String()
				reader := bufio.NewReader(conn)
				r, err := reqH1LifecycleReadRequest(reader, "/first")
				if err != nil {
					return err
				}
				_ = r.Body.Close()
				var wire string
				switch scenario {
				case "chunked_trailers_reuse":
					wire = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: X-Wire-End\r\n\r\n3\r\none\r\n3\r\ntwo\r\n0\r\nX-Wire-End: complete\r\n\r\n"
				case "truncated_length":
					wire = "HTTP/1.1 200 OK\r\nContent-Length: 9\r\n\r\nshort"
				case "truncated_chunk":
					wire = "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n9\r\nshort"
				default:
					wire = "HTTP/1.1 200 OK\r\nContent-Length: 20\r\n\r\npart"
				}
				if _, err := io.WriteString(conn, wire); err != nil {
					return err
				}
				if scenario == "truncated_length" || scenario == "truncated_chunk" {
					_ = conn.Close()
				} else if scenario != "chunked_trailers_reuse" {
					// Body.Close/cancel must close this incomplete response's socket.
					b, err := reader.ReadByte()
					if err != io.EOF {
						return fmt.Errorf("incomplete response socket remained usable: byte=%x error=%v", b, err)
					}
				}
				if scenario != "chunked_trailers_reuse" {
					conn, err = p.accept()
					if err != nil {
						return err
					}
					reader = bufio.NewReader(conn)
				}
				secondAddress = conn.RemoteAddr().String()
				r, err = reqH1LifecycleReadRequest(reader, "/second")
				if err != nil {
					return err
				}
				_ = r.Body.Close()
				_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 9\r\nConnection: close\r\n\r\nrecovered")
				return err
			})
			client := reqH1LifecycleClient(t)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			response := reqH1LifecycleGet(t, client, ctx, peer.listener.Addr().String(), "/first")
			if response.StatusCode != http.StatusOK {
				t.Fatalf("first status=%d", response.StatusCode)
			}
			switch scenario {
			case "chunked_trailers_reuse":
				if response.Trailer.Get("X-Wire-End") != "" {
					t.Fatal("trailer became visible before body EOF")
				}
				body, err := io.ReadAll(response.Body)
				if err != nil || string(body) != "onetwo" || response.Trailer.Get("X-Wire-End") != "complete" {
					t.Fatalf("chunked body=%q trailer=%v error=%v", body, response.Trailer, err)
				}
			case "early_close", "cancel_read":
				prefix := make([]byte, 4)
				if _, err := io.ReadFull(response.Body, prefix); err != nil || string(prefix) != "part" {
					t.Fatalf("prefix=%q error=%v", prefix, err)
				}
				if scenario == "cancel_read" {
					cancel()
					_, err := io.ReadAll(response.Body)
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("canceled body read=%v", err)
					}
				}
			default:
				body, err := io.ReadAll(response.Body)
				if !errors.Is(err, io.ErrUnexpectedEOF) || string(body) != "short" {
					t.Fatalf("truncated body=%q error=%v", body, err)
				}
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			second := reqH1LifecycleGet(t, client, context.Background(), peer.listener.Addr().String(), "/second")
			body, err := io.ReadAll(second.Body)
			if err != nil || second.StatusCode != http.StatusOK || string(body) != "recovered" {
				t.Fatalf("recovery status=%d body=%q error=%v", second.StatusCode, body, err)
			}
			peer.join(t)
			if reused := firstAddress == secondAddress; reused != (scenario == "chunked_trailers_reuse") {
				t.Fatalf("server observed reuse=%v for %s: %s -> %s", reused, scenario, firstAddress, secondAddress)
			}
		})
	}
}

type reqH1LifecycleBody struct {
	reader io.Reader
	reads  atomic.Int32
	closed chan struct{}
	once   sync.Once
}

func (b *reqH1LifecycleBody) Read(p []byte) (int, error) { b.reads.Add(1); return b.reader.Read(p) }
func (b *reqH1LifecycleBody) Close() error               { b.once.Do(func() { close(b.closed) }); return nil }

func TestReqHTTP1Lifecycle_ExpectContinue(t *testing.T) {
	for _, scenario := range []string{"informational_then_continue", "final_close_without_body", "final_keepalive_sends_body"} {
		t.Run(scenario, func(t *testing.T) {
			const payload = "upload-only-after-peer-response"
			var received string
			var continuations atomic.Int32
			var informational atomic.Int32
			body := &reqH1LifecycleBody{reader: strings.NewReader(payload), closed: make(chan struct{})}
			peer := newReqH1LifecyclePeer(t, func(p *reqH1LifecyclePeer) error {
				conn, err := p.accept()
				if err != nil {
					return err
				}
				reader := bufio.NewReader(conn)
				r, err := reqH1LifecycleReadRequest(reader, "/upload")
				if err != nil {
					return err
				}
				if r.Method != http.MethodPost || r.Header.Get("Expect") != "100-continue" || r.ContentLength != int64(len(payload)) {
					return fmt.Errorf("unexpected Expect request: %s headers=%v length=%d", r.Method, r.Header, r.ContentLength)
				}
				if body.reads.Load() != 0 {
					return fmt.Errorf("request body consumed before the peer sent any response: reads=%d", body.reads.Load())
				}
				if scenario == "final_close_without_body" {
					if _, err := io.WriteString(conn, "HTTP/1.1 417 Expectation Failed\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"); err != nil {
						return err
					}
					bytes, err := io.ReadAll(reader)
					if err != nil || len(bytes) != 0 {
						return fmt.Errorf("closed final response received body %q error=%v", bytes, err)
					}
					return nil
				}
				if scenario == "informational_then_continue" {
					if _, err := io.WriteString(conn, "HTTP/1.1 103 Early Hints\r\nLink: </asset>\r\n\r\nHTTP/1.1 100 Continue\r\n\r\n"); err != nil {
						return err
					}
				} else {
					if _, err := io.WriteString(conn, "HTTP/1.1 417 Expectation Failed\r\nContent-Length: 0\r\n\r\n"); err != nil {
						return err
					}
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					return err
				}
				received = string(body)
				_ = r.Body.Close()
				if received != payload {
					return fmt.Errorf("peer upload=%q", received)
				}
				if scenario == "informational_then_continue" {
					_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
					return err
				}
				// The early keepalive final response must still finish the upload,
				// leaving the same socket aligned for the next independent request.
				r, err = reqH1LifecycleReadRequest(reader, "/after-expect")
				if err != nil {
					return err
				}
				_ = r.Body.Close()
				_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
				return err
			})
			client := reqH1LifecycleClient(t)
			client.GetTransport().ExpectContinueTimeout = 30 * time.Second
			request, err := http.NewRequest(http.MethodPost, "http://"+peer.listener.Addr().String()+"/upload", body)
			if err != nil {
				t.Fatal(err)
			}
			request.ContentLength = int64(len(payload))
			request.Header.Set("Expect", "100-continue")
			trace := &httptrace.ClientTrace{Got100Continue: func() { continuations.Add(1) }, Got1xxResponse: func(code int, _ textproto.MIMEHeader) error { informational.Add(1); return nil }}
			request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = response.Body.Close() })
			responseBody, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-body.closed:
			case <-time.After(2 * time.Second):
				t.Fatal("request body was not closed")
			}
			if scenario == "informational_then_continue" {
				if response.StatusCode != http.StatusOK || string(responseBody) != "ok" || continuations.Load() != 1 || informational.Load() != 2 {
					t.Fatalf("continue status=%d body=%q got100=%d got1xx=%d", response.StatusCode, responseBody, continuations.Load(), informational.Load())
				}
			} else {
				if response.StatusCode != http.StatusExpectationFailed || len(responseBody) != 0 || continuations.Load() != 0 || informational.Load() != 0 {
					t.Fatalf("final response status=%d body=%q got100=%d got1xx=%d", response.StatusCode, responseBody, continuations.Load(), informational.Load())
				}
				if scenario == "final_keepalive_sends_body" {
					next := reqH1LifecycleGet(t, client, context.Background(), peer.listener.Addr().String(), "/after-expect")
					bytes, err := io.ReadAll(next.Body)
					if err != nil || string(bytes) != "ok" {
						t.Fatalf("after Expect body=%q error=%v", bytes, err)
					}
				}
			}
			peer.join(t)
			if scenario == "final_close_without_body" {
				if body.reads.Load() != 0 {
					t.Fatalf("closed final response consumed request body %d times", body.reads.Load())
				}
			} else if body.reads.Load() == 0 || received != payload {
				t.Fatalf("body reads=%d peer payload=%q", body.reads.Load(), received)
			}
		})
	}
}
