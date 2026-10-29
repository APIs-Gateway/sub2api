package req

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type h1BoundaryBody struct {
	reader io.Reader
	reads atomic.Int32
	closes atomic.Int32
	closeError error
}

func (b *h1BoundaryBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.reader.Read(p)
}

func (b *h1BoundaryBody) Close() error {
	b.closes.Add(1)
	return b.closeError
}

// Rejecting a request must release its entity without reading it, opening a
// connection, or replacing the primary error with a body Close error.
func TestReqH1Boundary_PreflightOwnership(t *testing.T) {
	primary := errors.New("owned H1 primary failure")
	closeFailure := errors.New("owned body close failure")
	type rejection struct {
		name string
		configure func(*Transport, *http.Request)
		text string
		cause error
		dials int32
		tlsDials int32
	}
	var tlsDials atomic.Int32
	cases := []rejection{
		{name: "nil_url", configure: func(_ *Transport, r *http.Request) { r.URL = nil }, text: "nil Request.URL"},
		{name: "header_name", configure: func(_ *Transport, r *http.Request) { r.Header["Bad Name"] = []string{"value"} }, text: "invalid header field name"},
		{name: "header_value_redacted", configure: func(_ *Transport, r *http.Request) { r.Header.Set("X-Secret", "sensitive-secret\r\ninjected") }, text: "invalid header field value"},
		{name: "trailer_name", configure: func(_ *Transport, r *http.Request) { r.Trailer = http.Header{"Bad Name": []string{"value"}} }, text: "invalid trailer field name"},
		{name: "trailer_value_redacted", configure: func(_ *Transport, r *http.Request) { r.Trailer = http.Header{"X-Secret": []string{"sensitive-secret\r\ninjected"}} }, text: "invalid trailer field value"},
		{name: "unsupported_scheme", configure: func(_ *Transport, r *http.Request) { r.URL.Scheme = "ftp" }, text: "unsupported protocol scheme"},
		{name: "invalid_method", configure: func(_ *Transport, r *http.Request) { r.Method = "BAD METHOD" }, text: "invalid method"},
		{name: "missing_host", configure: func(_ *Transport, r *http.Request) { r.URL.Host = "" }, text: "no Host"},
		{name: "already_canceled", configure: func(_ *Transport, r *http.Request) { ctx, cancel := context.WithCancel(context.Background()); cancel(); *r = *r.WithContext(ctx) }, cause: context.Canceled},
		{name: "proxy_error", configure: func(tr *Transport, _ *http.Request) { tr.SetProxy(func(*http.Request) (*url.URL, error) { return nil, primary }) }, cause: primary},
		{name: "dial_error", cause: primary, dials: 1},
		{name: "nil_custom_tls", configure: func(tr *Transport, r *http.Request) { r.URL.Scheme = "https"; tr.SetDialTLS(func(context.Context, string, string) (net.Conn, error) { tlsDials.Add(1); return nil, nil }) }, text: "returned (nil, nil)", tlsDials: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tlsDials.Store(0)
			tr := T().SetProxy(nil).EnableForceHTTP1()
			t.Cleanup(tr.CloseIdleConnections)
			var dials atomic.Int32
			tr.SetDial(func(context.Context, string, string) (net.Conn, error) { dials.Add(1); return nil, primary })
			body := &h1BoundaryBody{reader: strings.NewReader("must-not-read"), closeError: closeFailure}
			r, err := http.NewRequest("POST", "http://owned.invalid/preflight", body)
			if err != nil { t.Fatal(err) }
			if tc.configure != nil { tc.configure(tr, r) }
			response, err := tr.RoundTrip(r)
			if response != nil || err == nil { t.Fatalf("response=%v error=%v, want rejection", response, err) }
			if tc.cause != nil && !errors.Is(err, tc.cause) { t.Fatalf("primary error=%v, want %v", err, tc.cause) }
			if tc.text != "" && !strings.Contains(err.Error(), tc.text) { t.Fatalf("error=%v, want %q", err, tc.text) }
			if strings.Contains(err.Error(), "sensitive-secret") || errors.Is(err, closeFailure) { t.Fatalf("rejection disclosed secret or replaced primary error: %v", err) }
			if body.reads.Load() != 0 || body.closes.Load() != 1 || dials.Load() != tc.dials || tlsDials.Load() != tc.tlsDials { t.Fatalf("reads=%d closes=%d dials=%d TLSdials=%d", body.reads.Load(), body.closes.Load(), dials.Load(), tlsDials.Load()) }
		})
	}
}

// These literal text-protocol records exercise req's own parser. Expected
// normalized fields and rejected syntax are independent protocol observations.
func TestReqH1Boundary_HeaderParsing(t *testing.T) {
	cases := []struct {
		name string
		wire string
		memory int64
		maxHeaders int64
		want textproto.MIMEHeader
		tooLarge bool
		malformed bool
	}{
		{name: "fold_and_repeat", wire: "x-contract: one\r\n\ttwo\r\nX-Contract: three\r\nEmpty:\r\n\r\nNEXT", want: textproto.MIMEHeader{"X-Contract": []string{"one two", "three"}, "Empty": []string{""}}},
		{name: "trim_horizontal_space", wire: "X-Contract:\t one \t\r\n\r\nNEXT", want: textproto.MIMEHeader{"X-Contract": []string{"one"}}},
		{name: "missing_colon", wire: "missing-colon\r\n\r\n", malformed: true},
		{name: "leading_continuation", wire: " continuation\r\n\r\n", malformed: true},
		{name: "nul_field_name", wire: "X-\x00-Name: bad\r\n\r\n", malformed: true},
		{name: "nul_field_value", wire: "X-Name: bad\x00value\r\n\r\n", malformed: true},
		{name: "header_count_bound", wire: "X-One: 1\r\nX-Two: 2\r\n\r\n", maxHeaders: 1, tooLarge: true},
		{name: "memory_bound", wire: "X-Long: " + strings.Repeat("a", 128) + "\r\n\r\n", memory: 32, tooLarge: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			memory, maxHeaders := tc.memory, tc.maxHeaders
			if memory == 0 { memory = 1 << 20 }
			if maxHeaders == 0 { maxHeaders = 100 }
			buffer := bufio.NewReaderSize(strings.NewReader(tc.wire), 16)
			reader := newTextprotoReader(buffer, nil)
			fields, err := reader.readMIMEHeader(memory, maxHeaders)
			if tc.tooLarge {
				if !errors.Is(err, errMessageTooLarge) { t.Fatalf("limit error=%v, want message-too-large", err) }
				return
			}
			if tc.malformed {
				var protocol protocolError
				if !errors.As(err, &protocol) { t.Fatalf("malformed input error=%T/%v", err, err) }
				return
			}
			if err != nil || !reflect.DeepEqual(fields, tc.want) { t.Fatalf("fields=%v error=%v, want %v", fields, err, tc.want) }
			next, err := buffer.Peek(4)
			if err != nil || string(next) != "NEXT" { t.Fatalf("header parser consumed next message: %q/%v", next, err) }
		})
	}
}

func TestReqH1Boundary_HeadlessStatusFraming(t *testing.T) {
	cases := []struct { name, method, status, headers string; length int64; chunked bool }{
		{name: "head_length_metadata", method: "HEAD", status: "200 OK", headers: "Content-Length: 7\r\n", length: 7},
		{name: "head_chunked_metadata", method: "HEAD", status: "200 OK", headers: "Transfer-Encoding: chunked\r\n", length: -1, chunked: true},
		{name: "no_content", method: "GET", status: "204 No Content", length: 0},
		{name: "not_modified", method: "GET", status: "304 Not Modified", headers: "Content-Length: 7\r\n", length: 0},
		{name: "informational", method: "GET", status: "103 Early Hints", headers: "Link: </a>; rel=preload\r\n", length: 0},
		{name: "zero_length", method: "GET", status: "200 OK", headers: "Content-Length: 0\r\n", length: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest(tc.method, "http://owned.invalid/status", nil)
			if err != nil { t.Fatal(err) }
			buffer := bufio.NewReader(strings.NewReader("HTTP/1.1 " + tc.status + "\r\n" + tc.headers + "\r\nNEXT"))
			response, err := reqH1TransferReadResponse(t, buffer, r)
			if err != nil { t.Fatal(err) }
			got, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if len(got) != 0 || readErr != nil || closeErr != nil || response.ContentLength != tc.length { t.Fatalf("body=%q length=%d read=%v close=%v", got, response.ContentLength, readErr, closeErr) }
			if (tc.chunked && !reflect.DeepEqual(response.TransferEncoding, []string{"chunked"})) || (!tc.chunked && len(response.TransferEncoding) != 0) { t.Fatalf("encoding=%v, want chunked=%v", response.TransferEncoding, tc.chunked) }
			next, err := buffer.Peek(4)
			if err != nil || string(next) != "NEXT" { t.Fatalf("bodyless status consumed following message: %q/%v", next, err) }
		})
	}
}

func TestReqH1Boundary_ResponseBodySignals(t *testing.T) {
	t.Run("sticky_error_and_one_notification", func(t *testing.T) {
		primary := errors.New("owned read failure")
		var notifications atomic.Int32
		underlying := &h1BoundaryBody{reader: errorReader{primary}}
		body := &bodyEOFSignal{body: underlying, fn: func(err error) error { notifications.Add(1); return fmt.Errorf("observed: %w", err) }}
		for i := 0; i < 2; i++ { if n, err := body.Read(make([]byte, 1)); n != 0 || !errors.Is(err, primary) { t.Fatalf("read=%d/%v, want original cause", n, err) } }
		if underlying.reads.Load() != 1 || notifications.Load() != 1 { t.Fatal("sticky error performed more I/O or notifications") }
		if err := body.Close(); err != nil { t.Fatal(err) }
		if err := body.Close(); err != nil { t.Fatal(err) }
		if underlying.closes.Load() != 1 || notifications.Load() != 1 { t.Fatal("Close duplicated ownership or notification") }
		if _, err := body.Read(make([]byte, 1)); !errors.Is(err, errReadOnClosedResBody) { t.Fatalf("read after Close=%v", err) }
	})
	t.Run("early_close_owns_cleanup_once", func(t *testing.T) {
		closeFailure := errors.New("owned early close failure")
		underlying := &h1BoundaryBody{reader: strings.NewReader("not-consumed"), closeError: closeFailure}
		var early, completed atomic.Int32
		body := &bodyEOFSignal{body: underlying, earlyCloseFn: func() error { early.Add(1); return underlying.Close() }, fn: func(err error) error { completed.Add(1); return err }}
		if err := body.Close(); !errors.Is(err, closeFailure) { t.Fatalf("early close=%v", err) }
		if err := body.Close(); err != nil { t.Fatal(err) }
		if early.Load() != 1 || completed.Load() != 0 || underlying.closes.Load() != 1 || underlying.reads.Load() != 0 { t.Fatal("early Close read body, duplicated cleanup, or signaled completion") }
	})
	t.Run("eof_completes_instead_of_early_close", func(t *testing.T) {
		underlying := &h1BoundaryBody{reader: strings.NewReader("body")}
		var completed, early atomic.Int32
		body := &bodyEOFSignal{body: underlying, earlyCloseFn: func() error { early.Add(1); return underlying.Close() }, fn: func(err error) error { completed.Add(1); return err }}
		got, err := io.ReadAll(body)
		if err != nil || string(got) != "body" { t.Fatalf("body=%q error=%v", got, err) }
		if err := body.Close(); err != nil { t.Fatal(err) }
		if completed.Load() != 1 || early.Load() != 0 || underlying.closes.Load() != 1 { t.Fatal("EOF did not transfer cleanup from early-close to completion") }
	})
}

func TestReqH1Boundary_GzipReader(t *testing.T) {
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	if _, err := io.WriteString(writer, "owned compressed payload"); err != nil { t.Fatal(err) }
	if err := writer.Close(); err != nil { t.Fatal(err) }
	valid := append([]byte(nil), encoded.Bytes()...)
	corrupt := append([]byte(nil), valid...)
	corrupt[len(corrupt)-1] ^= 1
	cases := []struct { name string; wire []byte; want string; cause error }{
		{name: "valid_and_closed", wire: valid, want: "owned compressed payload"},
		{name: "invalid_header_sticky", wire: []byte("not a compressed entity"), cause: gzip.ErrHeader},
		{name: "truncated_header", wire: []byte{0x1f, 0x8b}, cause: io.ErrUnexpectedEOF},
		{name: "corrupt_checksum", wire: corrupt, want: "owned compressed payload", cause: gzip.ErrChecksum},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			underlying := &h1BoundaryBody{reader: bytes.NewReader(tc.wire)}
			body := &gzipReader{body: &bodyEOFSignal{body: underlying}}
			got, err := io.ReadAll(body)
			if string(got) != tc.want || (tc.cause == nil && err != nil) || (tc.cause != nil && !errors.Is(err, tc.cause)) { t.Fatalf("gzip body=%q error=%v, want %q/%v", got, err, tc.want, tc.cause) }
			if tc.name == "invalid_header_sticky" || tc.name == "truncated_header" {
				reads := underlying.reads.Load()
				if _, again := body.Read(make([]byte, 1)); !errors.Is(again, tc.cause) || underlying.reads.Load() != reads { t.Fatalf("gzip header failure was not sticky: %v", again) }
			}
			if err := body.Close(); err != nil { t.Fatal(err) }
			if err := body.Close(); err != nil { t.Fatal(err) }
			if underlying.closes.Load() != 1 { t.Fatalf("gzip closed original body %d times", underlying.closes.Load()) }
			if tc.name == "valid_and_closed" { if _, err := body.Read(make([]byte, 1)); !errors.Is(err, errReadOnClosedResBody) { t.Fatalf("gzip read after Close=%v", err) } }
		})
	}
}
