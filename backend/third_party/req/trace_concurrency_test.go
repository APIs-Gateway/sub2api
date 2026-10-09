package req

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These fixtures use the original public API and original trace callbacks, so
// the same source can exercise the unpatched module under the secured main MVS.
// A guard expiry is a fixture failure, never a successful race or red witness.
func traceConcurrencyJoin(t *testing.T, done <-chan struct{}, label string) bool {
	t.Helper()
	select {
	case <-done:
		return true
	case <-time.After(5 * time.Second):
		t.Errorf("%s did not join", label)
		return false
	}
}

func traceConcurrencyNonnegative(t *testing.T, info TraceInfo) {
	t.Helper()
	for name, value := range map[string]time.Duration{
		"DNS":            info.DNSLookupTime,
		"connect":        info.ConnectTime,
		"TCP":            info.TCPConnectTime,
		"TLS":            info.TLSHandshakeTime,
		"first response": info.FirstResponseTime,
		"response":       info.ResponseTime,
		"total":          info.TotalTime,
		"idle":           info.ConnIdleTime,
	} {
		if value < 0 {
			t.Errorf("negative %s duration: %v", name, value)
		}
	}
	if info.TotalTime <= 0 {
		t.Errorf("total duration must be positive: %v", info.TotalTime)
	}
}

func TestReqTraceSecurity_LateDNSAfterCompletion(t *testing.T) {
	for _, scope := range []string{"Client", "Request"} {
		for _, completion := range []string{"Cancel", "Deadline"} {
			for _, stage := range []string{"LateStart", "LateDone"} {
				t.Run(scope+"/"+completion+"/"+stage, func(t *testing.T) {
					client := C().SetProxy(nil)
					client.Transport.DisableKeepAlives = true
					if scope == "Client" {
						client.EnableTraceAll()
					}
					entered := make(chan struct{})
					dialDone := make(chan struct{})
					release := make(chan struct{})
					var releaseOnce sync.Once
					var started atomic.Bool
					var missingHook atomic.Bool
					client.SetDial(func(ctx context.Context, _, _ string) (net.Conn, error) {
						started.Store(true)
						defer close(dialDone)
						hooks := httptrace.ContextClientTrace(ctx)
						if hooks == nil || hooks.DNSStart == nil || hooks.DNSDone == nil {
							missingHook.Store(true)
						} else if stage == "LateDone" {
							hooks.DNSStart(httptrace.DNSStartInfo{Host: "trace.invalid"})
						}
						close(entered)
						<-release
						if !missingHook.Load() {
							if stage == "LateStart" {
								hooks.DNSStart(httptrace.DNSStartInfo{Host: "trace.invalid"})
							}
							hooks.DNSDone(httptrace.DNSDoneInfo{Err: context.Canceled})
						}
						return nil, context.Canceled
					})
					// The deadline itself is the public contract being tested.
					// Channels, rather than a sleep, establish the late callback.
					var ctx context.Context
					var cancel context.CancelFunc
					if completion == "Deadline" {
						ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
					} else {
						ctx, cancel = context.WithCancel(context.Background())
					}
					request := client.R().SetContext(ctx)
					if scope == "Request" {
						request.EnableTrace()
					}
					type outcome struct {
						response *Response
						err      error
					}
					result := make(chan outcome, 1)
					requestDone := make(chan struct{})
					go func() {
						defer close(requestDone)
						response, err := request.Get("http://trace.invalid/")
						result <- outcome{response, err}
					}()
					t.Cleanup(func() {
						cancel()
						releaseOnce.Do(func() { close(release) })
						traceConcurrencyJoin(t, requestDone, "canceled request")
						if started.Load() {
							traceConcurrencyJoin(t, dialDone, "late DNS dial")
						}
						client.Transport.CloseIdleConnections()
					})
					select {
					case <-entered:
					case <-time.After(5 * time.Second):
						t.Fatal("dial never entered")
					}
					if missingHook.Load() {
						t.Fatal("public request did not install DNS trace callbacks")
					}
					if completion == "Cancel" {
						cancel()
					}
					var got outcome
					select {
					case got = <-result:
					case <-time.After(5 * time.Second):
						t.Fatal("request did not complete while dial callback was retained")
					}
					expected := context.Canceled
					if completion == "Deadline" {
						expected = context.DeadlineExceeded
					}
					if !errors.Is(got.err, expected) || got.response == nil {
						t.Fatalf("completion: response=%v error=%v, want %v", got.response, got.err, expected)
					}
					if !traceConcurrencyJoin(t, requestDone, "completed request") {
						t.Fatal("request still running")
					}
					before := got.response.TraceInfo()
					traceConcurrencyNonnegative(t, before)
					if before.TotalTime != got.response.TotalTime() {
						t.Fatal("completed TraceInfo and TotalTime disagree")
					}
					if before.ConnectTime != 0 || before.TCPConnectTime != 0 || before.TLSHandshakeTime != 0 || before.FirstResponseTime != 0 || before.ResponseTime != 0 {
						t.Fatalf("unreached phases must remain zero: %+v", before)
					}
					releaseOnce.Do(func() { close(release) })
					if !traceConcurrencyJoin(t, dialDone, "released DNS callbacks") {
						t.Fatal("DNS callbacks still running")
					}
					after := got.response.TraceInfo()
					traceConcurrencyNonnegative(t, after)
					if after != before || after.TotalTime != got.response.TotalTime() {
						t.Fatalf("late DNS changed completed trace: before=%+v after=%+v", before, after)
					}
				})
			}
		}
	}
}

type traceConcurrencyAddr string

func (a traceConcurrencyAddr) Network() string { return "fixture" }
func (a traceConcurrencyAddr) String() string  { return string(a) }

type traceConcurrencyConn struct {
	net.Conn
	onAddress func()
	addresses atomic.Int32
}

func (c *traceConcurrencyConn) LocalAddr() net.Addr {
	c.addresses.Add(1)
	if c.onAddress != nil {
		c.onAddress()
	}
	return traceConcurrencyAddr("local")
}
func (c *traceConcurrencyConn) RemoteAddr() net.Addr {
	c.addresses.Add(1)
	if c.onAddress != nil {
		c.onAddress()
	}
	return traceConcurrencyAddr("remote")
}

func TestReqTraceSecurity_CallbacksConcurrentSnapshot(t *testing.T) {
	state := &clientTrace{}
	now := time.Now()
	request := &Request{trace: state, StartTime: now, responseReturnTime: now}
	response := &Response{Request: request}
	hooks := httptrace.ContextClientTrace(state.createContext(context.Background()))
	conn := &traceConcurrencyConn{}
	// Seed the connection before concurrent completion can freeze callbacks.
	hooks.GotConn(httptrace.GotConnInfo{Conn: conn})
	calls := []func(int){
		func(int) { hooks.DNSStart(httptrace.DNSStartInfo{Host: "trace.invalid"}) },
		func(int) { hooks.DNSDone(httptrace.DNSDoneInfo{}) },
		func(int) { hooks.ConnectStart("tcp", "127.0.0.1:443") },
		func(int) { hooks.ConnectDone("tcp", "127.0.0.1:443", nil) },
		func(int) { hooks.GetConn("trace.invalid:443") },
		func(i int) {
			reused := i%2 == 0
			idle := time.Duration(0)
			if reused {
				idle = 23 * time.Millisecond
			}
			hooks.GotConn(httptrace.GotConnInfo{Conn: conn, Reused: reused, WasIdle: reused, IdleTime: idle})
		},
		func(int) { hooks.GotFirstResponseByte() },
		func(int) { hooks.TLSHandshakeStart() },
		func(int) { hooks.TLSHandshakeDone(tls.ConnectionState{}, nil) },
	}
	start := make(chan struct{})
	producersDone := make(chan struct{})
	snapshotsDone := make(chan struct{})
	snapshotStop := make(chan struct{})
	var producers sync.WaitGroup
	var count atomic.Int32
	const rounds = 256
	for _, call := range calls {
		call := call
		producers.Add(1)
		go func() {
			defer producers.Done()
			<-start
			for i := 0; i < rounds; i++ {
				call(i)
				count.Add(1)
			}
		}()
	}
	// Only this producer writes Response.receivedAt. Snapshot readers access
	// the trace state, not that independent Response field or Request mutators.
	producers.Add(1)
	go func() {
		defer producers.Done()
		<-start
		for i := 0; i < rounds; i++ {
			response.setReceivedAt()
		}
	}()
	go func() { producers.Wait(); close(producersDone) }()
	incoherent := make(chan TraceInfo, 1)
	go func() {
		defer close(snapshotsDone)
		<-start
		for i := 0; ; i++ {
			select {
			case <-snapshotStop:
				return
			default:
			}
			info := response.TraceInfo()
			_ = request.TraceInfo()
			_ = response.TotalTime()
			if info.IsConnReused != info.IsConnWasIdle || (info.IsConnReused && info.ConnIdleTime != 23*time.Millisecond) || (!info.IsConnReused && info.ConnIdleTime != 0) {
				select {
				case incoherent <- info:
				default:
				}
			}
			if i >= rounds {
				select {
				case <-producersDone:
					return
				default:
				}
			}
		}
	}()
	t.Cleanup(func() {
		close(snapshotStop)
		traceConcurrencyJoin(t, producersDone, "cleanup trace producers")
		traceConcurrencyJoin(t, snapshotsDone, "cleanup trace snapshots")
	})
	close(start)
	if !traceConcurrencyJoin(t, producersDone, "all nine hooks and response completion") {
		t.Fatal("callbacks still running")
	}
	if !traceConcurrencyJoin(t, snapshotsDone, "concurrent trace snapshots") {
		t.Fatal("snapshots still running")
	}
	if count.Load() != 9*rounds {
		t.Fatalf("callback inventory=%d, want %d", count.Load(), 9*rounds)
	}
	select {
	case info := <-incoherent:
		t.Fatalf("torn connection snapshot: %+v", info)
	default:
	}
	info := request.TraceInfo()
	if info.RemoteAddr == nil || info.LocalAddr == nil || info.RemoteAddr.String() != "remote" || info.LocalAddr.String() != "local" {
		t.Fatalf("connection addresses lost: %+v", info)
	}
	if response.ReceivedAt().IsZero() {
		t.Fatal("response completion callback was not exercised")
	}
}

func TestReqTraceSecurity_ConnAddressesOutsideStateLock(t *testing.T) {
	state := &clientTrace{}
	now := time.Now()
	request := &Request{trace: state, StartTime: now, responseReturnTime: now}
	hooks := httptrace.ContextClientTrace(state.createContext(context.Background()))
	var addressWorkers sync.WaitGroup
	var blocked atomic.Bool
	conn := &traceConcurrencyConn{onAddress: func() {
		// The external method waits on a callback from another goroutine. Its
		// bounded escape lets an incorrect implementation unlock and all
		// workers join before this fixture reports the lock regression.
		called := make(chan struct{})
		addressWorkers.Add(1)
		go func() { defer addressWorkers.Done(); hooks.ConnectStart("tcp", "127.0.0.1:443"); close(called) }()
		select {
		case <-called:
		case <-time.After(time.Second):
			blocked.Store(true)
		}
	}}
	hooks.GotConn(httptrace.GotConnInfo{Conn: conn})
	done := make(chan struct{})
	result := make(chan TraceInfo, 1)
	t.Cleanup(func() {
		if !traceConcurrencyJoin(t, done, "cleanup external address snapshot") {
			return
		}
		addressesDone := make(chan struct{})
		go func() { addressWorkers.Wait(); close(addressesDone) }()
		traceConcurrencyJoin(t, addressesDone, "cleanup external address callbacks")
	})
	go func() { defer close(done); result <- request.TraceInfo() }()
	select {
	case info := <-result:
		if info.LocalAddr == nil || info.RemoteAddr == nil || conn.addresses.Load() != 2 {
			t.Fatalf("external address calls not retained: %+v count=%d", info, conn.addresses.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TraceInfo held its state lock across external Conn address callback")
	}
	if !traceConcurrencyJoin(t, done, "external address snapshot") {
		t.Fatal("address snapshot still running")
	}
	addressesDone := make(chan struct{})
	go func() { addressWorkers.Wait(); close(addressesDone) }()
	if !traceConcurrencyJoin(t, addressesDone, "external address callbacks") {
		t.Fatal("address callbacks still running")
	}
	if blocked.Load() {
		t.Fatal("TraceInfo held its state lock across an external Conn address method")
	}
}

func traceConcurrencyTLSClient(t *testing.T, protocol string, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := C().SetProxy(nil).SetTimeout(5 * time.Second).EnableInsecureSkipVerify().EnableTraceAll()
	if protocol == "HTTP1" {
		client.EnableForceHTTP1()
	} else {
		client.EnableForceHTTP2()
	}
	t.Cleanup(client.Transport.CloseIdleConnections)
	return client, server
}

func TestReqTraceSecurity_NormalTLSAndReuse(t *testing.T) {
	for _, protocol := range []string{"HTTP1", "HTTP2"} {
		t.Run(protocol, func(t *testing.T) {
			var handled atomic.Int32
			client, server := traceConcurrencyTLSClient(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handled.Add(1)
				fmt.Fprint(w, "trace-body")
			}))
			for i := 0; i < 2; i++ {
				response, err := client.R().Get(server.URL)
				if err != nil || response == nil {
					t.Fatalf("TLS request %d: %v", i, err)
				}
				if response.String() != "trace-body" {
					t.Fatalf("body=%q", response.String())
				}
				major := 2
				if protocol == "HTTP1" {
					major = 1
				}
				if response.ProtoMajor != major {
					t.Fatalf("protocol=%s, want HTTP%d", response.Proto, major)
				}
				info := response.TraceInfo()
				traceConcurrencyNonnegative(t, info)
				if info.TotalTime != response.TotalTime() || info.RemoteAddr == nil || info.LocalAddr == nil || response.ReceivedAt().IsZero() {
					t.Fatalf("completed TLS trace lost fields: %+v", info)
				}
				if info.IsConnReused != (i == 1) {
					t.Fatalf("request %d reuse=%v", i, info.IsConnReused)
				}
				if i == 0 && info.TLSHandshakeTime <= 0 {
					t.Fatalf("fresh TLS handshake missing: %+v", info)
				}
				if i == 1 && (info.TLSHandshakeTime != 0 || info.TCPConnectTime != 0 || info.DNSLookupTime != 0) {
					t.Fatalf("reused connection gained absent phases: %+v", info)
				}
			}
			if handled.Load() != 2 {
				t.Fatalf("actual server requests=%d", handled.Load())
			}
		})
	}
}

func TestReqTraceSecurity_RequestReuse(t *testing.T) {
	for _, protocol := range []string{"HTTP1", "HTTP2"} {
		t.Run(protocol, func(t *testing.T) {
			var handled atomic.Int32
			client, server := traceConcurrencyTLSClient(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handled.Add(1)
				fmt.Fprint(w, "repeated-request")
			}))
			request := client.R().EnableTrace()
			for i := 0; i < 2; i++ {
				response, err := request.Get(server.URL)
				if err != nil || response == nil {
					t.Fatalf("repeated request %d: %v", i, err)
				}
				if response.String() != "repeated-request" {
					t.Fatalf("body=%q", response.String())
				}
				major := 2
				if protocol == "HTTP1" {
					major = 1
				}
				if response.ProtoMajor != major {
					t.Fatalf("protocol=%s, want HTTP%d", response.Proto, major)
				}
				info := response.TraceInfo()
				traceConcurrencyNonnegative(t, info)
				if info.TotalTime <= 0 || info.TotalTime != response.TotalTime() || info.RemoteAddr == nil || info.LocalAddr == nil || response.ReceivedAt().IsZero() {
					t.Fatalf("request %d lost completion or trace fields: %+v", i, info)
				}
				if info.IsConnReused != (i == 1) {
					t.Fatalf("request %d reuse=%v", i, info.IsConnReused)
				}
				if i == 0 && info.TLSHandshakeTime <= 0 {
					t.Fatalf("fresh TLS handshake missing: %+v", info)
				}
				if i == 1 && (info.TLSHandshakeTime != 0 || info.TCPConnectTime != 0 || info.DNSLookupTime != 0) {
					t.Fatalf("repeated request retained prior connection phases: %+v", info)
				}
			}
			if handled.Load() != 2 {
				t.Fatalf("actual server requests=%d", handled.Load())
			}
		})
	}
}

func TestReqTraceSecurity_ManualBodyEOF(t *testing.T) {
	for _, protocol := range []string{"HTTP1", "HTTP2"} {
		t.Run(protocol, func(t *testing.T) {
			release := make(chan struct{})
			var releaseOnce sync.Once
			client, server := traceConcurrencyTLSClient(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "before-")
				w.(http.Flusher).Flush()
				select {
				case <-release:
					fmt.Fprint(w, "after")
				case <-r.Context().Done():
				}
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(func() { cancel(); releaseOnce.Do(func() { close(release) }) })
			response, err := client.R().SetContext(ctx).DisableAutoReadResponse().Get(server.URL)
			if err != nil || response == nil {
				t.Fatalf("manual response: %v", err)
			}
			major := 2
			if protocol == "HTTP1" {
				major = 1
			}
			if response.ProtoMajor != major {
				t.Fatalf("manual protocol=%s, want HTTP%d", response.Proto, major)
			}
			t.Cleanup(func() { response.Body.Close() })
			if !response.ReceivedAt().IsZero() {
				t.Fatal("manual response completed before body EOF")
			}
			before := response.TraceInfo()
			traceConcurrencyNonnegative(t, before)
			type bodyResult struct {
				body []byte
				err  error
			}
			body := make(chan bodyResult, 1)
			readDone := make(chan struct{})
			snapshotEntered := make(chan struct{})
			snapshotDone := make(chan struct{})
			go func() { defer close(readDone); value, err := response.ToBytes(); body <- bodyResult{value, err} }()
			go func() {
				defer close(snapshotDone)
				_ = response.TraceInfo()
				close(snapshotEntered)
				for {
					_ = response.TraceInfo()
					_ = response.TotalTime()
					select {
					case <-readDone:
						return
					default:
					}
				}
			}()
			t.Cleanup(func() {
				cancel()
				releaseOnce.Do(func() { close(release) })
				response.Body.Close()
				traceConcurrencyJoin(t, readDone, "manual body reader")
				traceConcurrencyJoin(t, snapshotDone, "manual completion snapshots")
			})
			select {
			case <-snapshotEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("manual snapshot did not start")
			}
			releaseOnce.Do(func() { close(release) })
			var got bodyResult
			select {
			case got = <-body:
			case <-time.After(5 * time.Second):
				t.Fatal("manual body did not finish")
			}
			if !traceConcurrencyJoin(t, readDone, "body EOF") {
				t.Fatal("body reader still running")
			}
			if !traceConcurrencyJoin(t, snapshotDone, "EOF snapshots") {
				t.Fatal("EOF snapshots still running")
			}
			if got.err != nil || string(got.body) != "before-after" {
				t.Fatalf("manual body=%q error=%v", got.body, got.err)
			}
			after := response.TraceInfo()
			traceConcurrencyNonnegative(t, after)
			if response.ReceivedAt().IsZero() || after.TotalTime <= before.TotalTime || after.ResponseTime <= before.ResponseTime || after.TotalTime != response.TotalTime() {
				t.Fatalf("manual EOF did not finalize trace: before=%+v after=%+v", before, after)
			}
		})
	}
}
