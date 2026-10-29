// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE-GO-BSD file at the req module root.
//
// Protocol/ownership contracts adapted from golang/net transport_test.go at
// 18ece0ce30bc35fa81fe72028bf309bb3ff3f4a5. Fork-specific header ordering and
// decompression expectations are documented in COVERAGE-CONTRACTS.md.
package http2

import (
	"bytes"
	"compress/gzip"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imroc/req/v3/internal/dump"
	"github.com/imroc/req/v3/internal/header"
	xhttp2 "golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestReqHTTP2CoverageContract_RequestAdmission(t *testing.T) {
	cases := []struct {
		name string
		mutate func(*http.Request)
		category string
	}{
		{"invalid_header_name", func(r *http.Request) { r.Header["Bad Name"] = []string{"value"} }, "invalid HTTP header name"},
		{"invalid_header_value", func(r *http.Request) { r.Header["X-Bad"] = []string{"a\r\nb"} }, "invalid HTTP header value"},
		{"invalid_trailer_name", func(r *http.Request) { r.Trailer = http.Header{"Bad Name": {"value"}} }, "invalid HTTP trailer name"},
		{"invalid_trailer_value", func(r *http.Request) { r.Trailer = http.Header{"X-Bad": {"a\r\nb"}} }, "invalid HTTP trailer value"},
		{"invalid_host", func(r *http.Request) { r.Host = "bad host" }, "invalid Host header"},
		{"invalid_opaque_path", func(r *http.Request) { r.URL.Opaque = "not-a-path" }, "invalid request :path"},
		{"peer_header_list_limit", func(r *http.Request) { r.Header.Set("X-Oversized", strings.Repeat("x", 1024)) }, "request header list larger than peer"},
		{"forbidden_connection_header", func(r *http.Request) { r.Header.Set("Connection", "upgrade") }, "invalid Connection request header"},
	}
	for _, tc := range cases { t.Run(tc.name, func(t *testing.T) {
		f := newReqH2ContractFixture(t)
		p := reqH2CoverageWarm(t, f)
		if tc.name == "peer_header_list_limit" {
			p.must(p.writer.WriteSettings(xhttp2.Setting{ID: xhttp2.SettingMaxHeaderListSize, Val: 512}))
			p.wait(func(e reqH2ContractFrame) bool { return e.kind == xhttp2.FrameSettings && e.ack })
		}
		body := reqH2CoverageNewBody(strings.NewReader("must-not-be-read"))
		req := reqH2ContractRequest(t, "POST", body)
		tc.mutate(req)
		c := f.start(req, true)
		r := c.get(t)
		if r.err == nil || !strings.Contains(r.err.Error(), tc.category) { t.Fatalf("admission error=%v, want %q", r.err, tc.category) }
		body.awaitClosed(t)
		if body.readCount() != 0 { t.Fatalf("invalid request body read %d times", body.readCount()) }
		// Consume the next HEADERS without filtering its ID or contents. If the
		// malformed request escaped admission, this control fails on that frame.
		reqH2CoverageReuse(t, f, p)
	}) }
}

func TestReqHTTP2CoverageContract_HeaderWire(t *testing.T) {
	for _, name := range []string{"pseudo_order", "ordinary_header_order", "cookie_split", "connect_authority_only"} { t.Run(name, func(t *testing.T) {
		f := newReqH2ContractFixture(t)
		p := reqH2CoverageWarm(t, f)
		method := "GET"
		if name == "connect_authority_only" { method = "CONNECT" }
		req := reqH2ContractRequest(t, method, nil)
		switch name {
		case "pseudo_order": req.Header[header.PseudoHeaderOderKey] = []string{":method", ":scheme", ":authority", ":path"}
		case "ordinary_header_order":
			req.Header[header.HeaderOderKey] = []string{"x-z", "x-a", "accept-encoding", "user-agent"}
			req.Header.Set("X-A", "first-alphabetically")
			req.Header.Set("X-Z", "first-on-wire")
		case "cookie_split": req.Header["Cookie"] = []string{"a=1;  b=2; c=3"}
		}
		c := f.start(req, true)
		e := reqH2CoverageNext(t, p, method)
		var names, cookies []string
		for _, field := range e.fields {
			names = append(names, field.Name)
			if field.Name == "cookie" { cookies = append(cookies, field.Value) }
			if field.Name == header.HeaderOderKey || field.Name == header.PseudoHeaderOderKey { t.Fatalf("internal ordering key escaped: %v", field) }
		}
		switch name {
		case "pseudo_order":
			want := []string{":method", ":scheme", ":authority", ":path"}
			if len(names) < 4 || !reflect.DeepEqual(names[:4], want) { t.Fatalf("pseudo order=%v", names) }
		case "ordinary_header_order":
			var regular []string
			for _, n := range names { if !strings.HasPrefix(n, ":") { regular = append(regular, n) } }
			if !reflect.DeepEqual(regular, []string{"x-z", "x-a", "accept-encoding", "user-agent"}) { t.Fatalf("ordinary order=%v", regular) }
		case "cookie_split":
			if !reflect.DeepEqual(cookies, []string{"a=1", "b=2", "c=3"}) { t.Fatalf("cookie pairs=%q", cookies) }
		case "connect_authority_only":
			var pseudo []string
			for _, n := range names { if strings.HasPrefix(n, ":") { pseudo = append(pseudo, n) } }
			if !reflect.DeepEqual(pseudo, []string{":authority", ":method"}) { t.Fatalf("CONNECT pseudo fields=%v", pseudo) }
		}
		p.response(e.id, "ordered")
		reqH2ContractOK(t, c.get(t), "ordered")
		reqH2CoverageReuse(t, f, p)
	}) }
}

func TestReqHTTP2CoverageContract_ExpectContinue(t *testing.T) {
	for _, name := range []string{"continue_releases_body", "final_rejection_never_reads_body", "cancel_before_continue", "deadline_before_continue", "continue_timeout_releases_body"} { t.Run(name, func(t *testing.T) {
		f := newReqH2ContractFixture(t)
		f.tr.ExpectContinueTimeout = 30*time.Second
		if name == "continue_timeout_releases_body" { f.tr.ExpectContinueTimeout = 150*time.Millisecond }
		body := reqH2CoverageNewBody(strings.NewReader("upload-after-continue"))
		req := reqH2ContractRequest(t, "POST", body)
		req.Header.Set("Expect", "100-continue")
		waiting, got100 := make(chan struct{}), make(chan struct{})
		var waitOnce, continueOnce sync.Once
		trace := &httptrace.ClientTrace{
			Wait100Continue: func() { waitOnce.Do(func() { close(waiting) }) },
			Got100Continue: func() { continueOnce.Do(func() { close(got100) }) },
		}
		ctx, cancel := context.WithCancel(httptrace.WithClientTrace(req.Context(), trace))
		if name == "deadline_before_continue" {
			cancel()
			ctx, cancel = context.WithTimeout(httptrace.WithClientTrace(req.Context(), trace), 3*time.Second)
		}
		defer cancel()
		c := f.start(req.WithContext(ctx), false)
		p := f.peer()
		e := reqH2CoverageNext(t, p, "POST")
		if e.end { t.Fatal("Expect request ended in HEADERS before body") }
		reqH2CoverageAwait(t, waiting, "Wait100Continue trace")
		if ctx.Err() != nil { t.Fatalf("context expired before wait prerequisite: %v", ctx.Err()) }
		if name != "continue_timeout_releases_body" && body.readCount() != 0 { t.Fatal("body was read before 100/final/cancel/deadline") }
		switch name {
		case "continue_releases_body":
			p.headers(e.id, false, hpack.HeaderField{Name: ":status", Value: "100"})
			reqH2CoverageAwait(t, got100, "Got100Continue trace")
			if got := string(p.upload(e.id)); got != "upload-after-continue" { t.Fatalf("upload=%q", got) }
			p.response(e.id, "accepted")
			reqH2ContractOK(t, c.get(t), "accepted")
		case "continue_timeout_releases_body":
			// No informational response is sent. Observing the complete upload is
			// positive evidence of the timer release, not a sleep-based absence.
			if got := string(p.upload(e.id)); got != "upload-after-continue" { t.Fatalf("timeout upload=%q", got) }
			select { case <-got100: t.Fatal("unexpected Got100Continue without peer 100"); default: }
			p.response(e.id, "accepted")
			reqH2ContractOK(t, c.get(t), "accepted")
		case "final_rejection_never_reads_body":
			p.headers(e.id, true, hpack.HeaderField{Name: ":status", Value: "417"})
			r := c.get(t)
			if r.err != nil || r.response == nil || r.response.StatusCode != 417 || len(r.body) != 0 { t.Fatalf("rejection=%+v", r) }
			// The final response and body-close acknowledgement are the oracle.
			// Explicit cancellation also joins the still-waiting upload path.
			c.cancel()
		case "cancel_before_continue", "deadline_before_continue":
			want := context.Canceled
			if name == "cancel_before_continue" { cancel() } else { want = context.DeadlineExceeded; reqH2CoverageAwait(t, ctx.Done(), "real request deadline") }
			if r := c.get(t); !errors.Is(r.err, want) { t.Fatalf("context result=%v, want %v", r.err, want) }
		}
		body.awaitClosed(t)
		if name == "final_rejection_never_reads_body" || name == "cancel_before_continue" || name == "deadline_before_continue" {
			if body.readCount() != 0 { t.Fatalf("body read after terminal pre-continue result: %d", body.readCount()) }
			// Inspect queued frames only after request body closure/terminal result,
			// then reuse the connection to flush and catch any stray DATA.
			p.wait(func(frame reqH2ContractFrame) bool { return frame.kind == xhttp2.FrameRSTStream && frame.id == e.id })
		}
		reqH2CoverageReuse(t, f, p)
		for _, frame := range p.pending { if frame.kind == xhttp2.FrameData && frame.id == e.id && len(frame.data) != 0 { t.Fatalf("unexpected request DATA after rejection/cancel: %q", frame.data) } }
	}) }
}

func TestReqHTTP2CoverageContract_Informational(t *testing.T) {
	for _, name := range []string{"early_hints_then_final", "trace_error_is_stream_cause", "end_stream_1xx_rejected", "missing_status_rejected", "nonnumeric_status_rejected"} { t.Run(name, func(t *testing.T) {
		f := newReqH2ContractFixture(t)
		p := reqH2CoverageWarm(t, f)
		var mu sync.Mutex
		var events []string
		first := 0
		sentinel := errors.New("caller rejected informational headers")
		trace := &httptrace.ClientTrace{
			GotFirstResponseByte: func() { mu.Lock(); first++; events = append(events, "first-byte"); mu.Unlock() },
			Got1xxResponse: func(code int, h textproto.MIMEHeader) error {
				mu.Lock(); events = append(events, fmt.Sprintf("%d:%s", code, h.Get("Link"))); mu.Unlock()
				if name == "trace_error_is_stream_cause" { return sentinel }; return nil
			},
		}
		req := reqH2ContractRequest(t, "GET", nil)
		c := f.start(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)), true)
		e := reqH2CoverageNext(t, p, "GET")
		switch name {
		case "early_hints_then_final", "trace_error_is_stream_cause":
			p.headers(e.id, false, hpack.HeaderField{Name: ":status", Value: "103"}, hpack.HeaderField{Name: "link", Value: "</style.css>; rel=preload"})
			if name == "early_hints_then_final" { p.response(e.id, "final") }
		case "end_stream_1xx_rejected": p.headers(e.id, true, hpack.HeaderField{Name: ":status", Value: "103"})
		case "missing_status_rejected": p.headers(e.id, true, hpack.HeaderField{Name: "x-marker", Value: "missing-status"})
		case "nonnumeric_status_rejected": p.headers(e.id, true, hpack.HeaderField{Name: ":status", Value: "xyz"})
		}
		r := c.get(t)
		if name == "early_hints_then_final" { reqH2ContractOK(t, r, "final") } else {
			var se StreamError
			if !errors.As(r.err, &se) || se.Code != ErrCodeProtocol || se.StreamID != e.id || se.Cause == nil { t.Fatalf("informational stream error=%#v", r.err) }
			if name == "trace_error_is_stream_cause" && se.Cause != sentinel { t.Fatalf("trace cause=%v", se.Cause) }
			p.wait(func(frame reqH2ContractFrame) bool { return frame.kind == xhttp2.FrameRSTStream && frame.id == e.id && frame.code == xhttp2.ErrCodeProtocol })
		}
		mu.Lock(); actual := append([]string(nil), events...); actualFirst := first; mu.Unlock()
		if actualFirst != 1 { t.Fatalf("first-byte callbacks=%d", actualFirst) }
		if name == "early_hints_then_final" || name == "trace_error_is_stream_cause" {
			if !reflect.DeepEqual(actual, []string{"first-byte", "103:</style.css>; rel=preload"}) { t.Fatalf("trace order/headers=%v", actual) }
		} else if !reflect.DeepEqual(actual, []string{"first-byte"}) { t.Fatalf("invalid response trace=%v", actual) }
		reqH2CoverageReuse(t, f, p)
	}) }
}

func TestReqHTTP2CoverageContract_ResponseEntity(t *testing.T) {
	for _, name := range []string{"end_headers_missing_body", "body_shorter_than_declared", "body_longer_than_declared", "head_rejects_data"} { t.Run(name, func(t *testing.T) {
		f := newReqH2ContractFixture(t)
		p := reqH2CoverageWarm(t, f)
		method := "GET"
		if name == "head_rejects_data" { method = "HEAD" }
		c := reqH2CoverageOpen(t, f, reqH2ContractRequest(t, method, nil))
		e := reqH2CoverageNext(t, p, method)
		length := "5"
		if name == "body_longer_than_declared" { length = "3" }
		p.headers(e.id, name == "end_headers_missing_body", hpack.HeaderField{Name: ":status", Value: "200"}, hpack.HeaderField{Name: "content-length", Value: length})
		if name == "head_rejects_data" {
			// Keep the stream alive through illegal DATA. Returning/closing a HEAD
			// response first would silently cancel it and hide the protocol check.
			p.must(p.writer.WriteData(e.id, false, []byte("forbidden-HEAD-entity")))
			reset := p.wait(func(frame reqH2ContractFrame) bool { return frame.kind == xhttp2.FrameRSTStream && frame.id == e.id })
			if reset.code != xhttp2.ErrCodeProtocol { t.Fatalf("HEAD reset=%v", reset.code) }
			res := c.get(t)
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if len(body) != 0 || err != nil || res.ContentLength != 5 { t.Fatalf("HEAD entity=%q err=%v length=%d", body, err, res.ContentLength) }
		} else {
			res := c.get(t)
			if name == "body_shorter_than_declared" { p.must(p.writer.WriteData(e.id, true, []byte("ab"))) }
			if name == "body_longer_than_declared" { p.must(p.writer.WriteData(e.id, false, []byte("abcdef"))) }
			body, err := io.ReadAll(res.Body)
			if name == "body_longer_than_declared" {
				if string(body) != "abc" || err == nil || !strings.Contains(err.Error(), "more than declared Content-Length; truncated") { t.Fatalf("oversized response body=%q err=%v", body, err) }
				buffer := make([]byte, 8)
				n, again := res.Body.Read(buffer)
				if n != 0 || again != err { t.Fatalf("terminal overrun read=%d/%v, original=%v", n, again, err) }
			} else {
				want := ""
				if name == "body_shorter_than_declared" { want = "ab" }
				if string(body) != want || !errors.Is(err, io.ErrUnexpectedEOF) { t.Fatalf("short response body=%q err=%v", body, err) }
			}
			res.Body.Close()
		}
		reqH2CoverageReuse(t, f, p)
	}) }
}

func TestReqHTTP2CoverageContract_AutoDecompression(t *testing.T) {
	for _, name := range []string{"implicit_gzip", "disabled_gzip_passthrough", "opted_in_deflate"} { t.Run(name, func(t *testing.T) {
		const entity = "exact compressed entity: \x00\x01\xff repeat repeat repeat"
		var compressed bytes.Buffer
		coding := "gzip"
		if name == "opted_in_deflate" {
			coding = "deflate"
			w, err := flate.NewWriter(&compressed, flate.DefaultCompression)
			if err != nil { t.Fatal(err) }
			if _, err := io.WriteString(w, entity); err != nil { t.Fatal(err) }
			if err := w.Close(); err != nil { t.Fatal(err) }
		} else {
			w := gzip.NewWriter(&compressed)
			if _, err := io.WriteString(w, entity); err != nil { t.Fatal(err) }
			if err := w.Close(); err != nil { t.Fatal(err) }
		}
		f := newReqH2ContractFixture(t)
		if name == "disabled_gzip_passthrough" { f.tr.DisableCompression = true }
		if name == "opted_in_deflate" { f.tr.AutoDecompression = true }
		req := reqH2ContractRequest(t, "GET", nil)
		if name == "opted_in_deflate" { req.Header.Set("Accept-Encoding", "deflate") }
		c := f.start(req, false)
		p := f.peer()
		e := reqH2CoverageNext(t, p, "GET")
		var actualEncoding []string
		for _, field := range e.fields { if field.Name == "accept-encoding" { actualEncoding = append(actualEncoding, field.Value) } }
		wantEncoding := []string{coding}
		if name == "disabled_gzip_passthrough" { wantEncoding = nil }
		if !reflect.DeepEqual(actualEncoding, wantEncoding) { t.Fatalf("wire Accept-Encoding=%q, want %q", actualEncoding, wantEncoding) }
		p.headers(e.id, false, hpack.HeaderField{Name: ":status", Value: "200"}, hpack.HeaderField{Name: "content-encoding", Value: coding}, hpack.HeaderField{Name: "content-length", Value: strconv.Itoa(compressed.Len())})
		p.must(p.writer.WriteData(e.id, true, compressed.Bytes()))
		r := c.get(t)
		if r.err != nil || r.response == nil { t.Fatalf("decompression response=%v err=%v", r.response, r.err) }
		if name == "disabled_gzip_passthrough" {
			if !bytes.Equal(r.body, compressed.Bytes()) || r.response.Uncompressed || r.response.ContentLength != int64(compressed.Len()) || r.response.Header.Get("Content-Encoding") != coding || r.response.Header.Get("Content-Length") != strconv.Itoa(compressed.Len()) { t.Fatalf("passthrough entity/metadata=%q %#v", r.body, r.response) }
		} else if string(r.body) != entity || !r.response.Uncompressed || r.response.ContentLength != -1 || r.response.Header.Get("Content-Encoding") != "" || r.response.Header.Get("Content-Length") != "" { t.Fatalf("automatic entity/metadata=%q %#v", r.body, r.response) }
		reqH2CoverageReuse(t, f, p)
	}) }
}

func TestReqHTTP2CoverageContract_StrictConcurrency(t *testing.T) {
	for _, name := range []string{"queued_request_waits_for_slot", "queued_cancel_releases_reservation"} { t.Run(name, func(t *testing.T) {
		f := newReqH2ContractFixture(t)
		f.tr.StrictMaxConcurrentStreams = true
		p := reqH2CoverageWarm(t, f)
		p.must(p.writer.WriteSettings(xhttp2.Setting{ID: xhttp2.SettingMaxConcurrentStreams, Val: 1}))
		p.wait(func(e reqH2ContractFrame) bool { return e.kind == xhttp2.FrameSettings && e.ack })
		first := f.start(reqH2ContractRequest(t, "GET", nil), true)
		e1 := reqH2CoverageNext(t, p, "GET")
		second := f.start(reqH2ContractRequest(t, "GET", nil), true)
		st := reqH2CoverageState(t, p.cc, func(st ClientConnState) bool { return st.StreamsPending == 1 })
		if st.StreamsActive != 1 || st.MaxConcurrentStreams != 1 || f.dialCount() != 1 { t.Fatalf("queued state=%+v, dials=%d", st, f.dialCount()) }
		if name == "queued_cancel_releases_reservation" {
			second.cancel()
			if r := second.get(t); !errors.Is(r.err, context.Canceled) { t.Fatalf("queued cancel=%v", r.err) }
			reqH2CoverageState(t, p.cc, func(st ClientConnState) bool { return st.StreamsPending == 0 && st.StreamsReserved == 0 && st.StreamsActive == 1 })
		}
		p.response(e1.id, "first")
		reqH2ContractOK(t, first.get(t), "first")
		if name == "queued_request_waits_for_slot" {
			e2 := reqH2CoverageNext(t, p, "GET")
			if e2.id != e1.id+2 { t.Fatalf("queued stream=%d, first=%d", e2.id, e1.id) }
			p.response(e2.id, "second")
			reqH2ContractOK(t, second.get(t), "second")
		}
		reqH2CoverageState(t, p.cc, func(st ClientConnState) bool { return st.StreamsActive == 0 && st.StreamsReserved == 0 && st.StreamsPending == 0 })
		reqH2CoverageReuse(t, f, p)
	}) }
}

func TestReqHTTP2CoverageContract_ReadErrorMetrics(t *testing.T) {
	cases := []struct { name string; wire []byte; tokens []string; closePeer bool }{
		{"peer_eof", nil, []string{"read_frame_eof"}, true},
		{"partial_frame_eof", []byte{0, 0, 8, byte(FramePing)}, []string{"read_frame_unexpected_eof"}, true},
		{"connection_protocol_error", reqH2ContractWire(FrameSettings, 0, 1, nil), []string{"frame_settings_has_stream", "read_frame_conn_error_PROTOCOL_ERROR"}, false},
	}
	for _, tc := range cases { t.Run(tc.name, func(t *testing.T) {
		f := newReqH2ContractFixture(t)
		var mu sync.Mutex
		var metrics []string
		f.tr.CountError = func(token string) { mu.Lock(); metrics = append(metrics, token); mu.Unlock() }
		p := reqH2CoverageWarm(t, f)
		c := reqH2CoverageDirect(t, p.cc, reqH2ContractRequest(t, "GET", nil))
		e := reqH2CoverageNext(t, p, "GET")
		if e.id == 1 { t.Fatal("warmup stream was not consumed") }
		if len(tc.wire) != 0 { _, err := p.server.Write(tc.wire); p.must(err) }
		if tc.closePeer { p.must(p.server.Close()) }
		r := c.get(t)
		if r.err == nil || r.response != nil { t.Fatalf("broken connection returned response=%v err=%v", r.response, r.err) }
		reqH2CoverageAwait(t, p.cc.readerDone, "terminal private reader")
		reqH2CoverageAwait(t, p.done, "terminal independent peer reader")
		if !p.cc.State().Closed { t.Fatalf("connection not closed: %+v", p.cc.State()) }
		mu.Lock(); actual := append([]string(nil), metrics...); mu.Unlock()
		if !reflect.DeepEqual(actual, tc.tokens) { t.Fatalf("CountError events=%q, want exact %q", actual, tc.tokens) }
		if f.dialCount() != 1 { t.Fatalf("metric test unexpectedly retried: %d dials", f.dialCount()) }
	}) }
}

func TestReqHTTP2CoverageContract_RequestBodyFailure(t *testing.T) {
	for _, name := range []string{"reader_error_immediate", "reader_error_after_prefix", "body_larger_than_declared"} { t.Run(name, func(t *testing.T) {
		f := newReqH2ContractFixture(t)
		p := reqH2CoverageWarm(t, f)
		sentinel := errors.New("owned request source failure")
		prefix := ""
		if name == "reader_error_after_prefix" { prefix = "known-prefix" }
		var source io.Reader = &reqH2CoverageErrorReader{prefix: []byte(prefix), err: sentinel}
		if name == "body_larger_than_declared" { source = strings.NewReader("abcdef") }
		body := reqH2CoverageNewBody(source)
		req := reqH2ContractRequest(t, "POST", body)
		if name == "body_larger_than_declared" { req.ContentLength = 3 }
		c := f.start(req, true)
		e := reqH2CoverageNext(t, p, "POST")
		r := c.get(t)
		if name == "body_larger_than_declared" {
			if !errors.Is(r.err, errReqBodyTooLong) { t.Fatalf("body overrun=%v", r.err) }
		} else if !errors.Is(r.err, sentinel) { t.Fatalf("request body cause=%v, want identity %v", r.err, sentinel) }
		body.awaitClosed(t)
		reset := p.wait(func(frame reqH2ContractFrame) bool { return frame.kind == xhttp2.FrameRSTStream && frame.id == e.id })
		if reset.code != xhttp2.ErrCodeCancel { t.Fatalf("request writer failure reset=%v", reset.code) }
		var actual []byte
		for _, frame := range p.pending { if frame.kind == xhttp2.FrameData && frame.id == e.id { actual = append(actual, frame.data...) } }
		if string(actual) != prefix { t.Fatalf("failed request wire prefix=%q, want %q", actual, prefix) }
		if body.readCount() == 0 { t.Fatal("request source never read") }
		reqH2CoverageReuse(t, f, p)
	}) }
}

func TestReqHTTP2CoverageContract_HeaderDump(t *testing.T) {
	for _, name := range []string{"response_continuation_fields_once", "invalid_value_error_redacted_raw_dump_retained"} { t.Run(name, func(t *testing.T) {
		f := newReqH2ContractFixture(t)
		writer := &reqH2CoverageDumpWriter{}
		dumper := dump.NewDumper(&reqH2CoverageDumpOptions{writer: writer})
		f.tr.Dump = dumper
		t.Cleanup(dumper.Stop)
		c := f.start(reqH2ContractRequest(t, "GET", nil), false)
		p := f.peer()
		e := reqH2CoverageNext(t, p, "GET")
		const secret = "sensitive-secret\r\nnot-a-header"
		if name == "response_continuation_fields_once" {
			var block bytes.Buffer
			enc := hpack.NewEncoder(&block)
			for _, field := range []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "x-first", Value: "one"}, {Name: "x-second", Value: "two"}} { if err := enc.WriteField(field); err != nil { t.Fatal(err) } }
			encoded := block.Bytes()
			cut := len(encoded)/2
			p.must(p.writer.WriteHeaders(xhttp2.HeadersFrameParam{StreamID: e.id, EndStream: true, EndHeaders: false, BlockFragment: encoded[:cut]}))
			p.must(p.writer.WriteContinuation(e.id, true, encoded[cut:]))
			reqH2ContractOK(t, c.get(t), "")
		} else {
			p.headers(e.id, true, hpack.HeaderField{Name: ":status", Value: "200"}, hpack.HeaderField{Name: "x-sensitive", Value: secret})
			r := c.get(t)
			var se StreamError
			if !errors.As(r.err, &se) || se.Code != ErrCodeProtocol || se.StreamID != e.id { t.Fatalf("illegal response value error=%#v", r.err) }
			if strings.Contains(r.err.Error(), secret) || strings.Contains(r.err.Error(), "sensitive-secret") { t.Fatalf("sensitive header leaked into error: %q", r.err) }
			p.wait(func(frame reqH2ContractFrame) bool { return frame.kind == xhttp2.FrameRSTStream && frame.id == e.id })
			// A bad decoded value must not poison the same HPACK connection.
			reqH2CoverageReuse(t, f, p)
		}
		dumper.Stop()
		actual, closes := writer.snapshot()
		if closes != 0 { t.Fatalf("dumper adopted caller writer: Close calls=%d", closes) }
		if name == "response_continuation_fields_once" {
			want := ":status: 200\r\nx-first: one\r\nx-second: two\r\n\r\n"
			if actual != want { t.Fatalf("continuation header dump=%q, want ordered fields exactly once %q", actual, want) }
		} else {
			// Dump is explicitly opted-in raw diagnostics; only the returned
			// error has a redaction contract. Preserve the actual raw bytes.
			want := ":status: 200\r\nx-sensitive: "+secret+"\r\n:status: 200\r\n\r\n"
			if actual != want { t.Fatalf("raw diagnostic bytes=%q, want %q", actual, want) }
		}
	}) }
}
