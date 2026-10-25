// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE-GO-BSD file at the req module root.
//
// Receive-credit behavior adapted from x/net v0.48.0 transport_test.go.
// See RECEIVE-FLOW-CONTRACTS.md for the exact sources and intentional changes.
package http2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/imroc/req/v3/internal/transport"
	xhttp2 "golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// This peer decodes actual bytes on the connection passed to the copied
// transport's public NewClientConn. It does not inspect or set client windows.
// x/net is only the independent peer's frame/HPACK codec, never the client.
type reqH2ReceiveEvent struct {
	kind xhttp2.FrameType
	id uint32
	ack bool
	code xhttp2.ErrCode
	ping [8]byte
	fields []hpack.HeaderField
	err error
}

type reqH2ReceivePeer struct {
	t *testing.T
	client, server net.Conn
	cc *ClientConn
	writer *xhttp2.Framer
	events chan reqH2ReceiveEvent
	done chan struct{}
	stop chan struct{}
	pending []reqH2ReceiveEvent
	mu sync.Mutex
	credits map[uint32]uint64
	barrierSequence byte
}

type reqH2ReceiveResponse struct {
	response *http.Response
	err error
}

func newReqH2ReceivePeer(t *testing.T) *reqH2ReceivePeer {
	t.Helper()
	client, server := net.Pipe()
	p := &reqH2ReceivePeer{t: t, client: client, server: server, writer: xhttp2.NewFramer(server, nil), events: make(chan reqH2ReceiveEvent, 128), done: make(chan struct{}), stop: make(chan struct{}), credits: make(map[uint32]uint64)}
	deadline := time.Now().Add(20 * time.Second)
	if err := client.SetDeadline(deadline); err != nil { t.Fatal(err) }
	if err := server.SetDeadline(deadline); err != nil { t.Fatal(err) }
	t.Cleanup(func() {
		close(p.stop)
		if p.cc != nil { p.cc.Close() }
		client.Close()
		server.Close()
		select {
		case <-p.done:
		case <-time.After(3 * time.Second): t.Error("RECEIVE_FLOW_FIXTURE: actual peer reader did not join")
		}
	})
	go p.read()
	tr := &Transport{Options: &transport.Options{}}
	cc, err := tr.NewClientConn(client)
	if err != nil { t.Fatalf("RECEIVE_FLOW_FIXTURE: NewClientConn: %v", err) }
	p.cc = cc
	p.wait(func(e reqH2ReceiveEvent) bool { return e.kind == xhttp2.FrameSettings && !e.ack })
	p.wait(func(e reqH2ReceiveEvent) bool { return e.kind == xhttp2.FrameWindowUpdate && e.id == 0 })
	p.must(p.writer.WriteSettings())
	p.wait(func(e reqH2ReceiveEvent) bool { return e.kind == xhttp2.FrameSettings && e.ack })
	return p
}

func (p *reqH2ReceivePeer) read() {
	defer close(p.done)
	emit := func(e reqH2ReceiveEvent) bool {
		select {
		case p.events <- e: return true
		case <-p.stop: return false
		}
	}
	preface := make([]byte, len(xhttp2.ClientPreface))
	if _, err := io.ReadFull(p.server, preface); err != nil { emit(reqH2ReceiveEvent{err: err}); return }
	if string(preface) != xhttp2.ClientPreface { emit(reqH2ReceiveEvent{err: fmt.Errorf("invalid actual client preface %q", preface)}); return }
	reader := xhttp2.NewFramer(io.Discard, p.server)
	reader.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	for {
		frame, err := reader.ReadFrame()
		if err != nil { emit(reqH2ReceiveEvent{err: err}); return }
		e := reqH2ReceiveEvent{kind: frame.Header().Type, id: frame.Header().StreamID}
		switch f := frame.(type) {
		case *xhttp2.SettingsFrame: e.ack = f.IsAck()
		case *xhttp2.WindowUpdateFrame:
			p.mu.Lock()
			p.credits[f.StreamID] += uint64(f.Increment)
			p.mu.Unlock()
		case *xhttp2.RSTStreamFrame: e.code = f.ErrCode
		case *xhttp2.PingFrame: e.ack, e.ping = f.IsAck(), f.Data
		case *xhttp2.MetaHeadersFrame: e.fields = append([]hpack.HeaderField(nil), f.Fields...)
		}
		if !emit(e) { return }
	}
}

func (p *reqH2ReceivePeer) must(err error) {
	p.t.Helper()
	if err != nil { p.t.Fatalf("RECEIVE_FLOW_FIXTURE: real peer write failed: %v", err) }
}

func (p *reqH2ReceivePeer) wait(match func(reqH2ReceiveEvent) bool) reqH2ReceiveEvent {
	p.t.Helper()
	for i, e := range p.pending {
		if match(e) { p.pending = append(p.pending[:i], p.pending[i+1:]...); return e }
	}
	guard := time.NewTimer(5 * time.Second)
	defer guard.Stop()
	for {
		select {
		case e := <-p.events:
			if e.err != nil { p.t.Fatalf("RECEIVE_FLOW_FIXTURE: real peer read failed: %v", e.err) }
			if e.kind == xhttp2.FrameGoAway { p.t.Fatalf("RECEIVE_FLOW_CONTRACT: unexpected connection GOAWAY: %+v", e) }
			if match(e) { return e }
			p.pending = append(p.pending, e)
		case <-guard.C:
			p.t.Fatal("RECEIVE_FLOW_FIXTURE: actual frame completion guard expired")
			return reqH2ReceiveEvent{}
		}
	}
}

func (p *reqH2ReceivePeer) start(id uint32) <-chan reqH2ReceiveResponse {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12 * time.Second)
	req, err := http.NewRequestWithContext(ctx, "GET", "https://receive.invalid/entity", nil)
	if err != nil { p.t.Fatal(err) }
	result := make(chan reqH2ReceiveResponse, 1)
	done := make(chan struct{})
	go func() { defer close(done); response, err := p.cc.RoundTrip(req); result <- reqH2ReceiveResponse{response, err} }()
	p.t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second): p.t.Error("RECEIVE_FLOW_FIXTURE: actual RoundTrip did not join")
		}
	})
	e := p.wait(func(e reqH2ReceiveEvent) bool { return e.kind == xhttp2.FrameHeaders })
	want := map[string]string{":method": "GET", ":scheme": "https", ":authority": "receive.invalid", ":path": "/entity"}
	if e.id != id { p.t.Fatalf("RECEIVE_FLOW_FIXTURE: request stream=%d want=%d", e.id, id) }
	for _, field := range e.fields { if value, ok := want[field.Name]; ok && value == field.Value { delete(want, field.Name) } }
	if len(want) != 0 { p.t.Fatalf("RECEIVE_FLOW_FIXTURE: actual request fields missing/wrong: %v", want) }
	return result
}

func (p *reqH2ReceivePeer) response(result <-chan reqH2ReceiveResponse) *http.Response {
	p.t.Helper()
	select {
	case r := <-result:
		if r.response != nil && r.response.Body != nil { p.t.Cleanup(func() { r.response.Body.Close() }) }
		if r.err != nil || r.response == nil || r.response.Body == nil || r.response.StatusCode != 200 || r.response.ProtoMajor != 2 {
			p.t.Fatalf("RECEIVE_FLOW_FIXTURE: target HTTP2 response missing: response=%v error=%v", r.response, r.err)
		}
		return r.response
	case <-time.After(5 * time.Second):
		p.t.Fatal("RECEIVE_FLOW_FIXTURE: target response completion guard expired")
		return nil
	}
}

func (p *reqH2ReceivePeer) headers(id uint32, end bool, length int) {
	p.t.Helper()
	var buffer bytes.Buffer
	encoder := hpack.NewEncoder(&buffer)
	for _, field := range []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "content-length", Value: strconv.Itoa(length)}} { p.must(encoder.WriteField(field)) }
	p.must(p.writer.WriteHeaders(xhttp2.HeadersFrameParam{StreamID: id, EndHeaders: true, EndStream: end, BlockFragment: buffer.Bytes()}))
}

// A real peer PING/ACK is a causal read-loop barrier, not a timer or a claim
// about how many WINDOW_UPDATE frames the implementation happens to coalesce.
func (p *reqH2ReceivePeer) barrier() {
	p.t.Helper()
	p.barrierSequence++
	data := [8]byte{'r', 'e', 'c', 'e', 'i', 'v', 'e', p.barrierSequence}
	p.must(p.writer.WritePing(false, data))
	p.wait(func(e reqH2ReceiveEvent) bool { return e.kind == xhttp2.FramePing && e.ack && e.ping == data })
}

func (p *reqH2ReceivePeer) connectionCredit() uint64 {
	p.mu.Lock(); defer p.mu.Unlock()
	return p.credits[0]
}

func (p *reqH2ReceivePeer) nextEntity(id uint32) {
	p.t.Helper()
	call := p.start(id)
	want := []byte("independent-followup-entity")
	p.headers(id, false, len(want))
	p.must(p.writer.WriteData(id, true, want))
	response := p.response(call)
	actual, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil || !bytes.Equal(actual, want) { p.t.Fatalf("RECEIVE_FLOW_CONTRACT: same-connection followup body=%q read=%v close=%v", actual, err, closeErr) }
}

func TestReqHTTP2ReceiveFlowContract_UnreadRefund(t *testing.T) {
	for _, split := range []bool{false, true} {
		name := "single_frame"
		if split { name = "late_frame_after_cancel" }
		t.Run(name, func(t *testing.T) {
			p := newReqH2ReceivePeer(t)
			before := p.connectionCredit()
			call := p.start(1)
			entity := bytes.Repeat([]byte("credit"), 834)[:5000]
			p.headers(1, false, len(entity))
			first := entity
			if split { first = entity[:1] }
			p.must(p.writer.WriteData(1, false, first))
			response := p.response(call)
			one := make([]byte, 1)
			if _, err := io.ReadFull(response.Body, one); err != nil || one[0] != entity[0] { t.Fatalf("RECEIVE_FLOW_FIXTURE: actual first byte=%q err=%v", one, err) }
			if err := response.Body.Close(); err != nil { t.Fatalf("RECEIVE_FLOW_CONTRACT: close unread response: %v", err) }
			reset := p.wait(func(e reqH2ReceiveEvent) bool { return e.kind == xhttp2.FrameRSTStream && e.id == 1 })
			if reset.code != xhttp2.ErrCodeCancel { t.Fatalf("RECEIVE_FLOW_CONTRACT: unread-body reset=%v, want CANCEL", reset.code) }
			if split { p.must(p.writer.WriteData(1, false, entity[1:])) }
			p.barrier()
			refunded := p.connectionCredit() - before
			if refunded != uint64(len(entity)) { t.Fatalf("RECEIVE_FLOW_CONTRACT: actual connection credit=%d, want retired DATA octets=%d", refunded, len(entity)) }
			p.nextEntity(3)
			t.Logf("RECEIVE_FLOW_ACTUAL_COMPLETE case=%s data=%d refunded=%d reset=CANCEL followupStream=3", name, len(entity), refunded)
		})
	}
}

func TestReqHTTP2ReceiveFlowContract_PaddedData(t *testing.T) {
	for _, name := range []string{"read_to_eof", "read_one_then_close", "close_unread"} {
		t.Run(name, func(t *testing.T) {
			p := newReqH2ReceivePeer(t)
			before := p.connectionCredit()
			call := p.start(1)
			fragment := bytes.Repeat([]byte("p"), 313)
			padding := make([]byte, 255)
			const frames = 16
			entity := bytes.Repeat(fragment, frames)
			p.headers(1, false, len(entity))
			for i := 0; i < frames; i++ { p.must(p.writer.WriteDataPadded(1, name == "read_to_eof" && i == frames-1, fragment, padding)) }
			response := p.response(call)
			p.barrier() // All actual DATA arrived before the caller's read/close.
			if name == "read_one_then_close" {
				one := make([]byte, 1)
				if _, err := io.ReadFull(response.Body, one); err != nil || one[0] != fragment[0] { t.Fatalf("RECEIVE_FLOW_FIXTURE: actual padded first byte=%q err=%v", one, err) }
			} else if name == "read_to_eof" {
				actual, err := io.ReadAll(response.Body)
				if err != nil || !bytes.Equal(actual, entity) { t.Fatalf("RECEIVE_FLOW_CONTRACT: padding entered/truncated entity: bytes=%d want=%d error=%v", len(actual), len(entity), err) }
			}
			if err := response.Body.Close(); err != nil { t.Fatalf("RECEIVE_FLOW_CONTRACT: padded response close: %v", err) }
			if name != "read_to_eof" {
				reset := p.wait(func(e reqH2ReceiveEvent) bool { return e.kind == xhttp2.FrameRSTStream && e.id == 1 })
				if reset.code != xhttp2.ErrCodeCancel { t.Fatalf("RECEIVE_FLOW_CONTRACT: padded unread reset=%v, want CANCEL", reset.code) }
			}
			p.barrier()
			// RFC9113: DATA flow-control octets include the Pad Length field and
			// padding, while Response.Body exposes only the actual entity.
			wireOctets := len(entity) + frames * (1 + len(padding))
			refunded := p.connectionCredit() - before
			if refunded > uint64(wireOctets) { t.Fatalf("RECEIVE_FLOW_CONTRACT: actual credit=%d exceeds received DATA=%d", refunded, wireOctets) }
			if name != "read_to_eof" && refunded != uint64(wireOctets) { t.Fatalf("RECEIVE_FLOW_CONTRACT: padded retired connection credit=%d want=%d wire octets", refunded, wireOctets) }
			// The full-read positive control does not demand a particular last
			// WINDOW_UPDATE batch: a legal implementation may defer a small
			// residual. Unread-close cases exercise the upstream refund contract.
			p.nextEntity(3)
			t.Logf("RECEIVE_FLOW_ACTUAL_COMPLETE case=%s entity=%d paddingAndLength=%d refunded=%d followupStream=3", name, len(entity), frames*(1+len(padding)), refunded)
		})
	}
}

func TestReqHTTP2ReceiveFlowContract_DataBeforeHeaders(t *testing.T) {
	f := newReqH2ContractFixture(t)
	first := f.start(reqH2ContractRequest(t, "GET", nil), false)
	p := f.peer()
	p.request(1)
	p.response(1, "normal-before-invalid-stream")
	reqH2ContractOK(t, first.get(t), "normal-before-invalid-stream")
	bad := f.start(reqH2ContractRequest(t, "GET", nil), false)
	p.request(3)
	p.must(p.writer.WriteData(3, true, []byte("data-before-headers")))
	r := bad.get(t)
	var streamError StreamError
	if !errors.As(r.err, &streamError) || streamError.StreamID != 3 || streamError.Code != ErrCodeProtocol || r.response != nil { t.Fatalf("RECEIVE_FLOW_CONTRACT: DATA before HEADERS response=%v error=%#v", r.response, r.err) }
	reset := p.wait(func(e reqH2ContractFrame) bool { return e.kind == xhttp2.FrameRSTStream && e.id == 3 })
	if reset.code != xhttp2.ErrCodeProtocol { t.Fatalf("RECEIVE_FLOW_CONTRACT: malformed stream reset=%v, want PROTOCOL_ERROR", reset.code) }
	last := f.start(reqH2ContractRequest(t, "GET", nil), false)
	p.request(5)
	p.response(5, "normal-after-invalid-stream")
	reqH2ContractOK(t, last.get(t), "normal-after-invalid-stream")
	t.Log("RECEIVE_FLOW_ACTUAL_COMPLETE case=data_before_headers streamError=PROTOCOL_ERROR isolatedStream=3 completedStreams=1,5")
}
