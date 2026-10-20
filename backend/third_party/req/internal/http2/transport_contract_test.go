// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE-GO-BSD file at the req module root.
//
// Behavioral subset adapted from golang/net/http2/transport_test.go at
// 18ece0ce30bc35fa81fe72028bf309bb3ff3f4a5. See transport_contract_peer_test.go.
package http2

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	xhttp2 "golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestReqHTTP2TransportContract_GOAWAYDebug(t *testing.T) {
	for _, midBody := range []bool{false, true} {
		name := "before_headers"
		if midBody {
			name = "during_body"
		}
		t.Run(name, func(t *testing.T) {
			f := newReqH2ContractFixture(t)
			call := f.start(reqH2ContractRequest(t, "GET", nil), false)
			p := f.peer()
			p.request(1)
			if midBody {
				p.headers(1, false, hpack.HeaderField{Name: ":status", Value: "200"}, hpack.HeaderField{Name: "content-length", Value: "123"})
				call.header(t)
			}
			p.must(p.writer.WriteGoAway(5, xhttp2.ErrCodeNo, []byte("server-debug")))
			p.must(p.writer.WriteGoAway(5, xhttp2.ErrCodeHTTP11Required, nil))
			p.server.Close()
			r := call.get(t)
			var goaway GoAwayError
			if !errors.As(r.err, &goaway) || goaway.LastStreamID != 5 || goaway.ErrCode != ErrCodeHTTP11Required || goaway.DebugData != "server-debug" {
				t.Fatalf("GOAWAY result=%#v, want last=5 HTTP11_REQUIRED and retained debug", r.err)
			}
			if (r.response != nil) != midBody || f.dialCount() != 1 {
				t.Fatalf("response=%v dials=%d, unexpected replay or response stage", r.response, f.dialCount())
			}
		})
	}
}

func TestReqHTTP2TransportContract_GOAWAYRetry(t *testing.T) {
	t.Run("graceful_first_stream", func(t *testing.T) {
		f := newReqH2ContractFixture(t)
		call := f.start(reqH2ContractRequest(t, "GET", nil), false)
		p := f.peer()
		if !p.request(1).end {
			t.Fatal("GET request did not end at HEADERS")
		}
		p.must(p.writer.WriteGoAway(0, xhttp2.ErrCodeNo, nil))
		second := f.peer()
		second.request(1)
		second.response(1, "recovered")
		reqH2ContractOK(t, call.get(t), "recovered")
		if f.dialCount() != 2 {
			t.Fatalf("dials=%d, want 2", f.dialCount())
		}
		p.closed()
	})
	t.Run("first_stream_error_not_retried", func(t *testing.T) {
		f := newReqH2ContractFixture(t)
		call := f.start(reqH2ContractRequest(t, "GET", nil), false)
		p := f.peer()
		p.request(1)
		p.must(p.writer.WriteGoAway(0, xhttp2.ErrCodeInternal, nil))
		r := call.get(t)
		if r.err == nil || !strings.Contains(r.err.Error(), "GOAWAY from server ErrCode:INTERNAL_ERROR") || f.dialCount() != 1 {
			t.Fatalf("result=%v dials=%d, want terminal initial GOAWAY error", r.err, f.dialCount())
		}
		p.closed()
	})
	t.Run("later_stream_retried_while_last_stream_finishes", func(t *testing.T) {
		f := newReqH2ContractFixture(t)
		first := f.start(reqH2ContractRequest(t, "GET", nil), false)
		p := f.peer()
		p.request(1)
		p.headers(1, false, hpack.HeaderField{Name: ":status", Value: "200"})
		first.header(t)
		later := f.start(reqH2ContractRequest(t, "GET", nil), false)
		p.request(3)
		p.must(p.writer.WriteGoAway(1, xhttp2.ErrCodeProtocol, []byte("drain")))
		second := f.peer()
		second.request(1)
		second.response(1, "retried")
		reqH2ContractOK(t, later.get(t), "retried")
		p.must(p.writer.WriteData(1, true, []byte("last-stream")))
		reqH2ContractOK(t, first.get(t), "last-stream")
		p.closed()
		if f.dialCount() != 2 {
			t.Fatalf("dials=%d, want 2", f.dialCount())
		}
	})
}

func TestReqHTTP2TransportContract_RefusedStreamReplay(t *testing.T) {
	for _, mode := range []string{"replayable", "unreplayable", "getbody_error", "no_body"} {
		t.Run(mode, func(t *testing.T) {
			f := newReqH2ContractFixture(t)
			req := reqH2ContractRequest(t, "POST", strings.NewReader("replay-payload"))
			var replays atomic.Int32
			replayErr := errors.New("owned replay failure")
			if mode == "unreplayable" {
				req.GetBody = nil
			} else {
				req.GetBody = func() (io.ReadCloser, error) {
					replays.Add(1)
					if mode == "getbody_error" {
						return nil, replayErr
					}
					return io.NopCloser(strings.NewReader("replay-payload")), nil
				}
			}
			if mode == "no_body" {
				req = reqH2ContractRequest(t, "GET", nil)
			}
			call := f.start(req, false)
			p := f.peer()
			header := p.request(1, req.Method)
			if mode == "no_body" {
				if !header.end {
					t.Fatal("bodyless request missing END_STREAM")
				}
			} else if got := string(p.upload(1)); got != "replay-payload" {
				t.Fatalf("first upload=%q", got)
			}
			p.must(p.writer.WriteRSTStream(1, xhttp2.ErrCodeRefusedStream))
			if mode == "unreplayable" || mode == "getbody_error" {
				r := call.get(t)
				if mode == "getbody_error" && (!errors.Is(r.err, replayErr) || replays.Load() != 1) {
					t.Fatalf("replay error=%v count=%d", r.err, replays.Load())
				}
				if mode == "unreplayable" && (r.err == nil || !strings.Contains(r.err.Error(), "cannot retry") || replays.Load() != 0) {
					t.Fatalf("unreplayable result=%v count=%d", r.err, replays.Load())
				}
			} else {
				p.request(3, req.Method)
				if mode == "replayable" && string(p.upload(3)) != "replay-payload" {
					t.Fatal("replayed upload differs")
				}
				p.response(3, "retry-success")
				reqH2ContractOK(t, call.get(t), "retry-success")
				if mode == "replayable" && replays.Load() != 1 {
					t.Fatalf("GetBody calls=%d, want 1", replays.Load())
				}
			}
			// A following successful request proves the refusal/error did not kill
			// the pool or leave a reserved stream. No speculative dial is needed.
			nextID := uint32(3)
			if mode == "replayable" || mode == "no_body" {
				nextID = 5
			}
			next := f.start(reqH2ContractRequest(t, "GET", nil), true)
			p.request(nextID)
			p.response(nextID, "usable")
			reqH2ContractOK(t, next.get(t), "usable")
			if f.dialCount() != 1 {
				t.Fatalf("dials=%d, want same connection", f.dialCount())
			}
		})
	}
}

func TestReqHTTP2TransportContract_ProtocolErrorDoesNotRetry(t *testing.T) {
	f := newReqH2ContractFixture(t)
	call := f.start(reqH2ContractRequest(t, "GET", nil), false)
	p := f.peer()
	p.request(1)
	p.must(p.writer.WriteData(1, true, []byte("DATA before response headers")))
	r := call.get(t)
	var streamErr StreamError
	if !errors.As(r.err, &streamErr) || streamErr.Code != ErrCodeProtocol || f.dialCount() != 1 {
		t.Fatalf("result=%v dials=%d, want client-detected stream protocol error", r.err, f.dialCount())
	}
	reset := p.wait(func(e reqH2ContractFrame) bool { return e.kind == xhttp2.FrameRSTStream && e.id == 1 })
	if reset.code != xhttp2.ErrCodeProtocol {
		t.Fatalf("RST=%v, want PROTOCOL_ERROR", reset.code)
	}
	next := f.start(reqH2ContractRequest(t, "GET", nil), false)
	p.request(3)
	p.response(3, "unrelated-stream")
	reqH2ContractOK(t, next.get(t), "unrelated-stream")
}

func TestReqHTTP2TransportContract_CancelRetryBackoff(t *testing.T) {
	f := newReqH2ContractFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := reqH2ContractRequest(t, "POST", strings.NewReader("retry-body")).WithContext(ctx)
	replayed := make(chan struct{}, 2)
	replayCount := 0
	req.GetBody = func() (io.ReadCloser, error) {
		replayCount++
		replayed <- struct{}{}
		if replayCount == 2 {
			<-ctx.Done()
		}
		return io.NopCloser(strings.NewReader("retry-body")), nil
	}
	call := f.start(req, false)
	p := f.peer()
	for _, id := range []uint32{1, 3} {
		p.request(id, "POST")
		if string(p.upload(id)) != "retry-body" {
			t.Fatal("retry upload differs")
		}
		p.must(p.writer.WriteRSTStream(id, xhttp2.ErrCodeRefusedStream))
		select {
		case <-replayed:
		case <-time.After(5 * time.Second):
			t.Fatal("refused stream did not reach actual replay decision")
		}
	}
	// GetBody has now been called for the second genuine REFUSED_STREAM.
	// RoundTrip must enter its production retry backoff, whose context arm
	// returns cancellation rather than issuing a third request.
	cancel()
	if r := call.get(t); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("retry cancellation=%v", r.err)
	}
	next := f.start(reqH2ContractRequest(t, "GET", nil), true)
	p.request(5)
	p.response(5, "cached-after-backoff-cancel")
	reqH2ContractOK(t, next.get(t), "cached-after-backoff-cancel")
	if f.dialCount() != 1 {
		t.Fatalf("dials=%d, want 1", f.dialCount())
	}
}

func TestReqHTTP2TransportContract_CachedPoolAndIdleClose(t *testing.T) {
	f := newReqH2ContractFixture(t)
	miss := f.start(reqH2ContractRequest(t, "GET", nil), true).get(t)
	if !errors.Is(miss.err, ErrNoCachedConn) || f.dialCount() != 0 {
		t.Fatalf("cached miss=%v dials=%d", miss.err, f.dialCount())
	}
	first := f.start(reqH2ContractRequest(t, "GET", nil), false)
	p := f.peer()
	p.request(1)
	p.response(1, "first")
	reqH2ContractOK(t, first.get(t), "first")
	second := f.start(reqH2ContractRequest(t, "GET", nil), true)
	p.request(3)
	p.response(3, "cached")
	reqH2ContractOK(t, second.get(t), "cached")
	if f.dialCount() != 1 {
		t.Fatalf("cached reuse dials=%d", f.dialCount())
	}
	f.tr.CloseIdleConnections()
	p.closed()
	third := f.start(reqH2ContractRequest(t, "GET", nil), false)
	fresh := f.peer()
	fresh.request(1)
	fresh.response(1, "fresh")
	reqH2ContractOK(t, third.get(t), "fresh")
	if f.dialCount() != 2 {
		t.Fatalf("idle replacement dials=%d", f.dialCount())
	}
}

func TestReqHTTP2TransportContract_ConnectionCloseSingleUse(t *testing.T) {
	f := newReqH2ContractFixture(t)
	for i := 1; i <= 2; i++ {
		req := reqH2ContractRequest(t, "GET", nil)
		req.Close = true
		call := f.start(req, false)
		p := f.peer()
		p.request(1)
		p.response(1, "single-use")
		reqH2ContractOK(t, call.get(t), "single-use")
		p.closed()
		if f.dialCount() != i {
			t.Fatalf("dials=%d, want %d", f.dialCount(), i)
		}
	}
}

func TestReqHTTP2TransportContract_ResponseTrailers(t *testing.T) {
	for _, declared := range []bool{false, true} {
		name := "undeclared"
		if declared {
			name = "declared"
		}
		t.Run(name, func(t *testing.T) {
			f := newReqH2ContractFixture(t)
			call := f.start(reqH2ContractRequest(t, "GET", nil), false)
			p := f.peer()
			p.request(1)
			fields := []hpack.HeaderField{{Name: ":status", Value: "200"}}
			if declared {
				fields = append(fields, hpack.HeaderField{Name: "trailer", Value: "x-checksum, x-request-id"})
			}
			p.headers(1, false, fields...)
			res := call.header(t)
			if declared && len(res.Trailer) != 2 {
				t.Fatalf("declared trailer slots=%v", res.Trailer)
			}
			p.must(p.writer.WriteData(1, false, []byte("payload")))
			p.headers(1, true, hpack.HeaderField{Name: "x-checksum", Value: "valid"}, hpack.HeaderField{Name: "x-request-id", Value: "wire-id"})
			r := call.get(t)
			reqH2ContractOK(t, r, "payload")
			if r.response.Trailer.Get("X-Checksum") != "valid" || r.response.Trailer.Get("X-Request-Id") != "wire-id" {
				t.Fatalf("trailers=%v", r.response.Trailer)
			}
		})
	}
}

func TestReqHTTP2TransportContract_InvalidTrailers(t *testing.T) {
	for _, mode := range []string{"missing_end_stream", "pseudoheader"} {
		t.Run(mode, func(t *testing.T) {
			f := newReqH2ContractFixture(t)
			call := f.start(reqH2ContractRequest(t, "GET", nil), false)
			p := f.peer()
			p.request(1)
			p.headers(1, false, hpack.HeaderField{Name: ":status", Value: "200"})
			call.header(t)
			p.must(p.writer.WriteData(1, false, []byte("partial")))
			if mode == "missing_end_stream" {
				p.headers(1, false, hpack.HeaderField{Name: "x-trailer", Value: "invalid"})
			} else {
				p.headers(1, true, hpack.HeaderField{Name: ":status", Value: "200"})
			}
			r := call.get(t)
			var connErr ConnectionError
			if !errors.As(r.err, &connErr) || connErr != ConnectionError(ErrCodeProtocol) {
				t.Fatalf("invalid trailer body error=%v, want connection PROTOCOL_ERROR", r.err)
			}
			p.closed()
			if f.dialCount() != 1 {
				t.Fatalf("invalid response trailer replayed; dials=%d", f.dialCount())
			}
		})
	}
}

func TestReqHTTP2TransportContract_ResponseHeaderTimeout(t *testing.T) {
	for _, body := range []bool{false, true} {
		name := "no_body"
		if body {
			name = "with_body"
		}
		t.Run(name, func(t *testing.T) {
			f := newReqH2ContractFixture(t)
			f.tr.ResponseHeaderTimeout = 250 * time.Millisecond
			req := reqH2ContractRequest(t, "GET", nil)
			if body {
				req = reqH2ContractRequest(t, "POST", strings.NewReader("complete-upload"))
			}
			call := f.start(req, false)
			p := f.peer()
			p.request(1, req.Method)
			if body && string(p.upload(1)) != "complete-upload" {
				t.Fatal("upload was not complete")
			}
			r := call.get(t)
			var timeout net.Error
			if !errors.As(r.err, &timeout) || !timeout.Timeout() || !strings.Contains(r.err.Error(), "timeout awaiting response headers") {
				t.Fatalf("response header timeout=%v", r.err)
			}
			reset := p.wait(func(e reqH2ContractFrame) bool { return e.kind == xhttp2.FrameRSTStream && e.id == 1 })
			if reset.code != xhttp2.ErrCodeCancel {
				t.Fatalf("timeout RST=%v, want CANCEL", reset.code)
			}
		})
	}
}

func TestReqHTTP2TransportContract_CancelKeepsOtherStream(t *testing.T) {
	for _, stage := range []string{"before_headers", "during_body"} {
		t.Run(stage, func(t *testing.T) {
			f := newReqH2ContractFixture(t)
			cancelled := f.start(reqH2ContractRequest(t, "GET", nil), false)
			p := f.peer()
			p.request(1)
			if stage == "during_body" {
				p.headers(1, false, hpack.HeaderField{Name: ":status", Value: "200"})
				cancelled.header(t)
				p.must(p.writer.WriteData(1, false, []byte("partial")))
			}
			other := f.start(reqH2ContractRequest(t, "GET", nil), false)
			p.request(3)
			cancelled.cancel()
			if r := cancelled.get(t); !errors.Is(r.err, context.Canceled) {
				t.Fatalf("canceled stream=%v", r.err)
			}
			reset := p.wait(func(e reqH2ContractFrame) bool { return e.kind == xhttp2.FrameRSTStream && e.id == 1 })
			if reset.code != xhttp2.ErrCodeCancel {
				t.Fatalf("cancel RST=%v", reset.code)
			}
			p.response(3, "other-stream")
			reqH2ContractOK(t, other.get(t), "other-stream")
			if f.dialCount() != 1 {
				t.Fatalf("cancel killed shared connection; dials=%d", f.dialCount())
			}
		})
	}
}

func TestReqHTTP2TransportContract_Shutdown(t *testing.T) {
	for _, cancelShutdown := range []bool{false, true} {
		name := "drain"
		if cancelShutdown {
			name = "cancel_shutdown_keeps_active_stream"
		}
		t.Run(name, func(t *testing.T) {
			f := newReqH2ContractFixture(t)
			call := f.start(reqH2ContractRequest(t, "GET", nil), false)
			p := f.peer()
			p.request(1)
			cc := f.clientConn(p)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- cc.Shutdown(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("Shutdown did not join")
				}
			})
			frame := p.wait(func(e reqH2ContractFrame) bool { return e.kind == xhttp2.FrameGoAway })
			if frame.code != xhttp2.ErrCodeNo {
				t.Fatalf("shutdown GOAWAY=%v", frame.code)
			}
			if cancelShutdown {
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("Shutdown error=%v", err)
					}
					done <- err
				case <-time.After(5 * time.Second):
					t.Fatal("canceled Shutdown blocked")
				}
			}
			p.response(1, "drained")
			reqH2ContractOK(t, call.get(t), "drained")
			if !cancelShutdown {
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("Shutdown error=%v", err)
					}
					done <- err
				case <-time.After(5 * time.Second):
					t.Fatal("Shutdown did not finish after last stream")
				}
				p.closed()
			}
		})
	}
}

func TestReqHTTP2TransportContract_Ping(t *testing.T) {
	for _, ack := range []bool{false, true} {
		name := "cancel"
		if ack {
			name = "ack"
		}
		t.Run(name, func(t *testing.T) {
			f := newReqH2ContractFixture(t)
			call := f.start(reqH2ContractRequest(t, "GET", nil), false)
			p := f.peer()
			p.request(1)
			cc := f.clientConn(p)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- cc.Ping(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("Ping did not join")
				}
			})
			ping := p.wait(func(e reqH2ContractFrame) bool { return e.kind == xhttp2.FramePing && !e.ack })
			if len(ping.data) != 8 {
				t.Fatalf("PING payload length=%d", len(ping.data))
			}
			if ack {
				var payload [8]byte
				copy(payload[:], ping.data)
				p.must(p.writer.WritePing(true, payload))
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if (ack && err != nil) || (!ack && !errors.Is(err, context.Canceled)) {
					t.Fatalf("Ping ack=%v error=%v", ack, err)
				}
				done <- err
			case <-time.After(5 * time.Second):
				t.Fatal("Ping did not return")
			}
			p.response(1, "ping-preserved-request")
			reqH2ContractOK(t, call.get(t), "ping-preserved-request")
		})
	}
}
