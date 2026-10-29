// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE-GO-BSD file at the req module root.
//
// Additional wire contracts adapted from golang/net at
// 18ece0ce30bc35fa81fe72028bf309bb3ff3f4a5. See COVERAGE-CONTRACTS.md.
package http2

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/imroc/req/v3/internal/dump"
	xhttp2 "golang.org/x/net/http2"
)

// All peer operations run on the test goroutine. The existing harness owns the
// real private pool, net.Pipe endpoints and independent HPACK decoder.
func reqH2CoverageNext(t *testing.T, p *reqH2ContractPeer, method string) reqH2ContractFrame {
	t.Helper()
	e := p.wait(func(e reqH2ContractFrame) bool { return e.kind == xhttp2.FrameHeaders })
	if e.id == 0 || e.id%2 != 1 {
		t.Fatalf("request stream ID=%d", e.id)
	}
	fields := map[string]string{}
	for _, field := range e.fields {
		fields[field.Name] = field.Value
	}
	if fields[":method"] != method || fields[":authority"] != "wire.invalid" {
		t.Fatalf("request pseudo fields=%v", e.fields)
	}
	if method == "CONNECT" {
		if _, ok := fields[":path"]; ok {
			t.Fatal("CONNECT sent :path")
		}
		if _, ok := fields[":scheme"]; ok {
			t.Fatal("CONNECT sent :scheme")
		}
	} else if fields[":path"] != "/contracts" || fields[":scheme"] != "https" {
		t.Fatalf("unexpected request path/scheme: %v", e.fields)
	}
	if p.cc == nil {
		p.cc = p.fixture.clientConn(p)
	}
	return e
}

func reqH2CoverageWarm(t *testing.T, f *reqH2ContractFixture) *reqH2ContractPeer {
	t.Helper()
	c := f.start(reqH2ContractRequest(t, "GET", nil), false)
	p := f.peer()
	e := reqH2CoverageNext(t, p, "GET")
	p.response(e.id, "warm")
	reqH2ContractOK(t, c.get(t), "warm")
	return p
}

func reqH2CoverageReuse(t *testing.T, f *reqH2ContractFixture, p *reqH2ContractPeer) {
	t.Helper()
	req := reqH2ContractRequest(t, "GET", nil)
	req.Header.Set("X-Entity", "after-error-hpack-control")
	c := f.start(req, true)
	e := reqH2CoverageNext(t, p, "GET")
	var control string
	for _, h := range e.fields {
		if h.Name == "x-entity" {
			control = h.Value
		}
	}
	if control != "after-error-hpack-control" {
		t.Fatalf("decoded control=%q", control)
	}
	p.response(e.id, "reused")
	reqH2ContractOK(t, c.get(t), "reused")
	if f.dialCount() != 1 {
		t.Fatalf("dial count=%d, want one reused connection", f.dialCount())
	}
}

type reqH2CoverageBody struct {
	mu     sync.Mutex
	r      io.Reader
	reads  int
	closed chan struct{}
	once   sync.Once
}

func reqH2CoverageNewBody(r io.Reader) *reqH2CoverageBody {
	return &reqH2CoverageBody{r: r, closed: make(chan struct{})}
}
func (b *reqH2CoverageBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reads++
	return b.r.Read(p)
}
func (b *reqH2CoverageBody) Close() error   { b.once.Do(func() { close(b.closed) }); return nil }
func (b *reqH2CoverageBody) readCount() int { b.mu.Lock(); defer b.mu.Unlock(); return b.reads }
func (b *reqH2CoverageBody) awaitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-b.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("caller request Body.Close did not complete")
	}
}

type reqH2CoverageErrorReader struct {
	prefix []byte
	err    error
}

func (r *reqH2CoverageErrorReader) Read(p []byte) (int, error) {
	if len(r.prefix) != 0 {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	return 0, r.err
}

// Preserve the response's live body for CL/repeated-read/HEAD assertions. The
// response is published under a lock so cancellation cleanup cannot miss it.
type reqH2CoverageOpenCall struct {
	result   chan reqH2ContractResult
	done     chan struct{}
	cancel   context.CancelFunc
	mu       sync.Mutex
	response *http.Response
}

func reqH2CoverageOpen(t *testing.T, f *reqH2ContractFixture, req *http.Request) *reqH2CoverageOpenCall {
	t.Helper()
	ctx, cancel := context.WithTimeout(req.Context(), 12*time.Second)
	c := &reqH2CoverageOpenCall{result: make(chan reqH2ContractResult, 1), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(c.done)
		res, err := f.tr.RoundTrip(req.WithContext(ctx))
		c.mu.Lock()
		c.response = res
		c.mu.Unlock()
		c.result <- reqH2ContractResult{response: res, err: err}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-c.done:
		case <-time.After(3 * time.Second):
			t.Error("open RoundTrip did not join")
			return
		}
		c.mu.Lock()
		res := c.response
		c.mu.Unlock()
		if res != nil {
			res.Body.Close()
		}
	})
	return c
}
func (c *reqH2CoverageOpenCall) get(t *testing.T) *http.Response {
	t.Helper()
	select {
	case r := <-c.result:
		if r.err != nil || r.response == nil {
			t.Fatalf("open response=%v error=%v", r.response, r.err)
		}
		return r.response
	case <-time.After(5 * time.Second):
		t.Fatal("open response guard expired")
		return nil
	}
}

func reqH2CoverageAwait(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not arrive", label)
	}
}

func reqH2CoverageState(t *testing.T, cc *ClientConn, want func(ClientConnState) bool) ClientConnState {
	t.Helper()
	guard := time.NewTimer(5 * time.Second)
	defer guard.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		st := cc.State()
		if want(st) {
			return st
		}
		select {
		case <-tick.C:
		case <-guard.C:
			t.Fatalf("connection state never reached prerequisite; last=%+v", st)
			return st
		}
	}
}

// Direct real ClientConn calls make connection-level error metrics unambiguous:
// a retry from Transport cannot introduce another connection or another EOF.
func reqH2CoverageDirect(t *testing.T, cc *ClientConn, req *http.Request) *reqH2ContractCall {
	t.Helper()
	ctx, cancel := context.WithTimeout(req.Context(), 12*time.Second)
	c := &reqH2ContractCall{result: make(chan reqH2ContractResult, 1), headers: make(chan *http.Response, 1), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(c.done)
		res, err := cc.RoundTrip(req.WithContext(ctx))
		var body []byte
		if err == nil {
			body, err = io.ReadAll(res.Body)
			res.Body.Close()
		}
		c.result <- reqH2ContractResult{response: res, body: body, err: err}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-c.done:
		case <-time.After(3 * time.Second):
			t.Error("direct ClientConn call did not join")
		}
	})
	return c
}

type reqH2CoverageDumpWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	closes int
}

func (w *reqH2CoverageDumpWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(p)
}
func (w *reqH2CoverageDumpWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closes++
	return nil
}
func (w *reqH2CoverageDumpWriter) snapshot() (string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String(), w.closes
}

type reqH2CoverageDumpOptions struct{ writer io.Writer }

func (o *reqH2CoverageDumpOptions) Output() io.Writer               { return nil }
func (o *reqH2CoverageDumpOptions) RequestHeaderOutput() io.Writer  { return nil }
func (o *reqH2CoverageDumpOptions) RequestBodyOutput() io.Writer    { return nil }
func (o *reqH2CoverageDumpOptions) ResponseHeaderOutput() io.Writer { return o.writer }
func (o *reqH2CoverageDumpOptions) ResponseBodyOutput() io.Writer   { return nil }
func (o *reqH2CoverageDumpOptions) RequestHeader() bool             { return false }
func (o *reqH2CoverageDumpOptions) RequestBody() bool               { return false }
func (o *reqH2CoverageDumpOptions) ResponseHeader() bool            { return true }
func (o *reqH2CoverageDumpOptions) ResponseBody() bool              { return false }
func (o *reqH2CoverageDumpOptions) Async() bool                     { return true }
func (o *reqH2CoverageDumpOptions) Clone() dump.Options {
	return &reqH2CoverageDumpOptions{writer: o.writer}
}
