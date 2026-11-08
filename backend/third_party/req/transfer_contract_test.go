// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in LICENSE-GO-BSD at this module root.
//
// Adapted subset of Go go1.26.9 transfer_test.go, revision
// 2ae494ef9fb90e0da0073c31800ba38870249994. The final-chunk contract drives
// req's actual private response parser, rather than net/http.ReadResponse.
package req

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func reqH1TransferReadResponse(t *testing.T, r *bufio.Reader, request *http.Request) (*http.Response, error) {
	t.Helper()
	if request == nil {
		var err error
		request, err = http.NewRequest("GET", "http://owned.invalid/transfer", nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	pc := &persistConn{t: C().Transport, br: r}
	return pc._readResponse(request)
}

func TestReqHTTP1Transfer_BadTrailer(t *testing.T) {
	b := &body{
		src: strings.NewReader("foobar"),
		hdr: true, // force reading the trailer
		r:   bufio.NewReader(strings.NewReader("")),
	}
	buf := make([]byte, 7)
	n, err := b.Read(buf[:3])
	got := string(buf[:n])
	if got != "foo" || err != nil {
		t.Fatalf(`first Read = %d (%q), %v; want 3 ("foo")`, n, got, err)
	}

	n, err = b.Read(buf[:])
	got = string(buf[:n])
	if got != "bar" || err != nil {
		t.Fatalf(`second Read = %d (%q), %v; want 3 ("bar")`, n, got, err)
	}

	n, err = b.Read(buf[:])
	got = string(buf[:n])
	if n != 0 || !errors.Is(err, errTrailerEOF) {
		t.Errorf("final Read = %d/%q/%v, want no bytes and exact trailer EOF", n, got, err)
	}
	if n, err := b.Read(buf); n != 0 || !errors.Is(err, http.ErrBodyReadAfterClose) {
		t.Fatalf("Read after malformed trailer = %d/%v, want closed body", n, err)
	}
}

func TestReqHTTP1Transfer_FinalChunkEOF(t *testing.T) {
	res, err := reqH1TransferReadResponse(t, bufio.NewReader(strings.NewReader(
		"HTTP/1.1 200 OK\r\n"+
			"Transfer-Encoding: chunked\r\n"+
			"\r\n"+
			"0a\r\n"+
			"Body here\n\r\n"+
			"09\r\n"+
			"continued\r\n"+
			"0\r\n"+
			"\r\n")), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	want := "Body here\ncontinued"
	buf := make([]byte, len(want))
	n, err := res.Body.Read(buf)
	if n != len(want) || err != io.EOF {
		t.Logf("body = %#v", res.Body)
		t.Errorf("Read = %v, %v; want %d, EOF", n, err, len(want))
	}
	if string(buf) != want {
		t.Errorf("buf = %q; want %q", buf, want)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReqHTTP1Transfer_ParseTransferEncoding(t *testing.T) {
	tests := []struct {
		hdr     http.Header
		wantErr error
	}{
		{
			hdr:     http.Header{"Transfer-Encoding": {"fugazi"}},
			wantErr: &unsupportedTEError{`unsupported transfer encoding: "fugazi"`},
		},
		{
			hdr:     http.Header{"Transfer-Encoding": {"chunked, chunked", "identity", "chunked"}},
			wantErr: &unsupportedTEError{`too many transfer encodings: ["chunked, chunked" "identity" "chunked"]`},
		},
		{
			hdr:     http.Header{"Transfer-Encoding": {""}},
			wantErr: &unsupportedTEError{`unsupported transfer encoding: ""`},
		},
		{
			hdr:     http.Header{"Transfer-Encoding": {"chunked, identity"}},
			wantErr: &unsupportedTEError{`unsupported transfer encoding: "chunked, identity"`},
		},
		{
			hdr:     http.Header{"Transfer-Encoding": {"chunked", "identity"}},
			wantErr: &unsupportedTEError{`too many transfer encodings: ["chunked" "identity"]`},
		},
		{
			hdr:     http.Header{"Transfer-Encoding": {"\x0bchunked"}},
			wantErr: &unsupportedTEError{`unsupported transfer encoding: "\vchunked"`},
		},
		{
			hdr:     http.Header{"Transfer-Encoding": {"chunked"}},
			wantErr: nil,
		},
	}

	for i, tt := range tests {
		tr := &transferReader{
			Header:     tt.hdr,
			ProtoMajor: 1,
			ProtoMinor: 1,
		}
		gotErr := tr.parseTransferEncoding()
		if !reflect.DeepEqual(gotErr, tt.wantErr) {
			t.Errorf("%d.\ngot error:\n%v\nwant error:\n%v\n\n", i, gotErr, tt.wantErr)
		}
	}
}

// issue 39017 - disallow Content-Length values such as "+3"
func TestReqHTTP1Transfer_ParseContentLength(t *testing.T) {
	t.Setenv("GODEBUG", "httplaxcontentlength=0")
	tests := []struct {
		cl         string
		wantErr    error
		wantLength int64
	}{
		{
			cl:      "",
			wantErr: badStringError("invalid empty Content-Length", ""),
		},
		{
			cl:         "3",
			wantErr:    nil,
			wantLength: 3,
		},
		{
			cl:      "+3",
			wantErr: badStringError("bad Content-Length", "+3"),
		},
		{
			cl:      "-3",
			wantErr: badStringError("bad Content-Length", "-3"),
		},
		{
			// max int64, for safe conversion before returning
			cl:         "9223372036854775807",
			wantErr:    nil,
			wantLength: 9223372036854775807,
		},
		{
			cl:      "9223372036854775808",
			wantErr: badStringError("bad Content-Length", "9223372036854775808"),
		},
	}

	for _, tt := range tests {
		got, gotErr := parseContentLength([]string{tt.cl})
		if !reflect.DeepEqual(gotErr, tt.wantErr) || (tt.wantErr == nil && got != tt.wantLength) {
			t.Errorf("%q: got=%d/%v want=%d/%v", tt.cl, got, gotErr, tt.wantLength, tt.wantErr)
		}
	}
}

// Writer contracts compare complete output and ownership, rather than the
// implementation's choice of io.Copy optimization or internal reader type.
type reqH1TransferOwnedBody struct {
	io.Reader
	closer   io.Closer
	closes   int
	closeErr error
}

func (b *reqH1TransferOwnedBody) Close() error {
	b.closes++
	if b.closer != nil {
		if err := b.closer.Close(); err != nil {
			return err
		}
	}
	return b.closeErr
}

func TestReqHTTP1Transfer_WriterWireAndOwnership(t *testing.T) {
	const payload = "owned-payload\n"
	for _, reader := range []string{"buffer", "string", "file"} {
		for _, framing := range []string{"known_length", "unknown_length", "chunked_trailer"} {
			t.Run(reader+"/"+framing, func(t *testing.T) {
				owned := &reqH1TransferOwnedBody{}
				switch reader {
				case "buffer":
					owned.Reader = bytes.NewBufferString(payload)
				case "string":
					owned.Reader = strings.NewReader(payload)
				case "file":
					path := filepath.Join(t.TempDir(), "owned-body")
					if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
						t.Fatal(err)
					}
					file, err := os.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = file.Close() })
					owned.Reader, owned.closer = file, file
				}
				writer := &transferWriter{Method: "PUT", Body: owned, BodyCloser: owned, ContentLength: int64(len(payload))}
				want := payload
				if framing != "known_length" {
					writer.ContentLength = -1
				}
				if framing == "chunked_trailer" {
					writer.TransferEncoding = []string{"chunked"}
					writer.Trailer = http.Header{"X-Owned-Checksum": {"valid"}}
					want = fmt.Sprintf("%x\r\n%s\r\n0\r\nX-Owned-Checksum: valid\r\n\r\n", len(payload), payload)
				}
				var output bytes.Buffer
				if err := writer.writeBody(&output, nil); err != nil {
					t.Fatal(err)
				}
				if output.String() != want || owned.closes != 1 {
					t.Fatalf("wire=%q closes=%d, want %q and exactly one Close", output.String(), owned.closes, want)
				}
			})
		}
	}
}

type reqH1TransferErrorWriter struct{ err error }

func (w reqH1TransferErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestReqHTTP1Transfer_WriterFailuresCloseBody(t *testing.T) {
	for _, mode := range []string{"short_body", "long_body", "read_error", "write_error", "close_error"} {
		t.Run(mode, func(t *testing.T) {
			sentinel := errors.New("owned transfer " + mode)
			owned := &reqH1TransferOwnedBody{Reader: strings.NewReader("abc")}
			writer := &transferWriter{Method: "PUT", Body: owned, BodyCloser: owned, ContentLength: 3}
			var output bytes.Buffer
			var dst io.Writer = &output
			wantWire := "abc"
			wantText := ""
			switch mode {
			case "short_body":
				writer.ContentLength = 4
				wantText = "ContentLength=4 with Body length 3"
			case "long_body":
				writer.ContentLength = 2
				wantWire = "ab"
				wantText = "ContentLength=2 with Body length 3"
			case "read_error":
				owned.Reader = errorReader{err: sentinel}
				wantWire = ""
			case "write_error":
				dst = reqH1TransferErrorWriter{err: sentinel}
				wantWire = ""
			case "close_error":
				owned.closeErr = sentinel
			}
			err := writer.writeBody(dst, nil)
			if err == nil || (wantText != "" && !strings.Contains(err.Error(), wantText)) || (wantText == "" && !errors.Is(err, sentinel)) {
				t.Fatalf("mode=%s error=%v want text=%q or exact sentinel", mode, err, wantText)
			}
			if owned.closes != 1 || output.String() != wantWire {
				t.Fatalf("mode=%s closes=%d wire=%q want one Close and %q", mode, owned.closes, output.String(), wantWire)
			}
			if mode == "read_error" && !errors.Is(writer.bodyReadError, sentinel) {
				t.Fatalf("bodyReadError=%v, want actual source error", writer.bodyReadError)
			}
		})
	}
}

func TestReqHTTP1Transfer_ResponseFraming(t *testing.T) {
	for _, tc := range []struct {
		name       string
		headers    string
		wireBody   string
		wantErr    string
		wantLength int64
	}{
		{name: "length", headers: "Content-Length: 3\r\n", wireBody: "abc", wantLength: 3},
		{name: "same_duplicate_length", headers: "Content-Length: 3\r\nContent-Length: 3\r\n", wireBody: "abc", wantLength: 3},
		{name: "conflicting_length", headers: "Content-Length: 3\r\nContent-Length: 4\r\n", wantErr: "multiple Content-Length"},
		{name: "signed_length", headers: "Content-Length: +3\r\n", wantErr: "bad Content-Length"},
		{name: "negative_length", headers: "Content-Length: -3\r\n", wantErr: "bad Content-Length"},
		{name: "overflow_length", headers: "Content-Length: 9223372036854775808\r\n", wantErr: "bad Content-Length"},
		{name: "comma_length", headers: "Content-Length: 3, 3\r\n", wantErr: "bad Content-Length"},
		{name: "chunked_overrides_length", headers: "Transfer-Encoding: chunked\r\nContent-Length: 42\r\n", wireBody: "3\r\nabc\r\n0\r\n\r\n", wantLength: -1},
		{name: "mixed_case_chunked", headers: "Transfer-Encoding: ChUnKeD\r\n", wireBody: "3\r\nabc\r\n0\r\n\r\n", wantLength: -1},
		{name: "duplicate_encoding", headers: "Transfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n", wantErr: "too many transfer encodings"},
		{name: "encoding_list", headers: "Transfer-Encoding: gzip, chunked\r\n", wantErr: "unsupported transfer encoding"},
		{name: "identity_encoding", headers: "Transfer-Encoding: identity\r\n", wantErr: "unsupported transfer encoding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const next = "NEXT"
			reader := bufio.NewReader(strings.NewReader("HTTP/1.1 200 OK\r\n" + tc.headers + "\r\n" + tc.wireBody + next))
			response, err := reqH1TransferReadResponse(t, reader, nil)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || response != nil {
					t.Fatalf("response=%v err=%v, want rejected framing %q", response, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantLength == -1 {
				if _, present := response.Header["Content-Length"]; present || !reflect.DeepEqual(response.TransferEncoding, []string{"chunked"}) {
					t.Fatalf("chunked response retained ambiguous framing: Content-Length=%v TransferEncoding=%v", response.Header["Content-Length"], response.TransferEncoding)
				}
			} else if !reflect.DeepEqual(response.Header["Content-Length"], []string{"3"}) || len(response.TransferEncoding) != 0 {
				t.Fatalf("length response not normalized: Content-Length=%v TransferEncoding=%v", response.Header["Content-Length"], response.TransferEncoding)
			}
			got, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if readErr != nil || closeErr != nil || string(got) != "abc" || response.ContentLength != tc.wantLength {
				t.Fatalf("body=%q length=%d read=%v close=%v", got, response.ContentLength, readErr, closeErr)
			}
			remaining, err := reader.Peek(len(next))
			if err != nil || string(remaining) != next {
				t.Fatalf("parser consumed following response prefix: %q/%v", remaining, err)
			}
		})
	}
}
