// Derived from imroc/req PR535, commit099387e5ee9ac4631611281e6413ebcc7d0871ea,
// decode_test.go/TestAutoDecodeReadCloserSplitMultibyte and issue525's HTTP
// reproduction. The upstream module's MIT terms are preserved in LICENSE.
// Fork child1736; shared integration1712; permanent tracking291.
package req

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// Coalesce only the sniffing read of the actual socket-backed response body.
// TCP packet boundaries must not choose whether the GBK character is split.
// No response bytes, charset decisions or errors are replaced by a test double.
type metaCharsetFirstRead struct {
	io.ReadCloser
	first []byte
	size  int
	limit int
	calls int
	close int
}

func (r *metaCharsetFirstRead) Read(p []byte) (int, error) {
	r.calls++
	if r.calls == 1 {
		r.size = len(p)
		first := p
		if r.limit > 0 && len(first) > r.limit {
			first = first[:r.limit]
		}
		n, err := io.ReadFull(r.ReadCloser, first)
		r.first = append([]byte(nil), p[:n]...)
		return n, err
	}
	return r.ReadCloser.Read(p)
}

func (r *metaCharsetFirstRead) Close() error {
	r.close++
	return r.ReadCloser.Close()
}

func TestReqMetaCharset_PublicHTTPBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		charset   string
		firstRead int
		shortRead int
		aligned   bool
		header    bool
		disabled  bool
		noMeta    bool
	}{
		{name: "meta_split_512_gbk", charset: "gbk", firstRead: 512},
		{name: "meta_aligned_512_gbk", charset: "gbk", firstRead: 512, aligned: true},
		{name: "header_split_512_gbk", charset: "gbk", firstRead: 512, header: true},
		{name: "meta_split_128_gb2312", charset: "gb2312", firstRead: 128},
		{name: "meta_aligned_128_gb2312", charset: "gb2312", firstRead: 128, aligned: true},
		{name: "disabled_split_512_gbk", charset: "gbk", firstRead: 512, disabled: true},
		{name: "no_meta_split_512", firstRead: 512, noMeta: true},
		{name: "no_meta_short_128_stale_buffer", firstRead: 512, shortRead: 128, noMeta: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gbkChina, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("中国"))
			if err != nil {
				t.Fatal(err)
			}
			base := `<html><head><meta charset="` + tc.charset + `">`
			if tc.noMeta {
				base = "<html><head><!-- no charset -->"
			}
			const open = "<title>"
			const tail = "</title></head><body>owned-loopback-response</body></html>"
			wireFirstRead := tc.firstRead
			if tc.shortRead > 0 {
				wireFirstRead = tc.shortRead
			}
			titleStart := wireFirstRead - 3
			if tc.aligned {
				titleStart--
			}
			padding := titleStart - len(base) - len(open)
			if padding < 0 || len(gbkChina) != 4 {
				t.Fatal("invalid owned meta-charset boundary fixture")
			}
			prefix := base + strings.Repeat("x", padding) + open
			wire := append([]byte(prefix), gbkChina...)
			wire = append(wire, []byte(tail)...)
			if !tc.aligned && (wire[wireFirstRead-1] != gbkChina[2] || wire[wireFirstRead] != gbkChina[3]) {
				t.Fatal("国 is not split across the configured first-read boundary")
			}
			want := []byte(prefix + "中国" + tail)
			if tc.disabled || tc.noMeta {
				want = append([]byte(nil), wire...)
			}
			type observation struct {
				method   string
				uri      string
				protocol int
				written  int
				err      error
			}
			observed := make(chan observation, 1)
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contentType := "text/html"
				if tc.header {
					contentType += "; charset=" + tc.charset
				}
				w.Header().Set("Content-Type", contentType)
				written, err := w.Write(wire)
				observed <- observation{method: r.Method, uri: r.URL.RequestURI(), protocol: r.ProtoMajor, written: written, err: err}
			}))
			t.Cleanup(peer.Close)
			client := C().SetProxy(nil).SetTimeout(0).EnableForceHTTP1()
			if tc.disabled {
				client.DisableAutoDecode()
			}
			t.Cleanup(func() { client.GetTransport().CloseIdleConnections() })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			resp, requestErr := client.R().SetContext(ctx).DisableAutoReadResponse().Get(peer.URL + "/meta-boundary")
			if resp != nil && resp.Response != nil && resp.Body != nil {
				t.Cleanup(func() { _ = resp.Body.Close() })
			}
			if requestErr != nil || resp == nil || resp.Response == nil || resp.Body == nil || resp.StatusCode != http.StatusOK || resp.ProtoMajor != 1 {
				t.Fatalf("real public HTTP response before charset read: response=%v error=%v", resp, requestErr)
			}
			var first *metaCharsetFirstRead
			if !tc.header && !tc.disabled {
				decoder, ok := resp.Body.(*autoDecodeReadCloser)
				if !ok {
					t.Fatalf("public HTTP response did not select the production meta reader: %T", resp.Body)
				}
				first = &metaCharsetFirstRead{ReadCloser: decoder.ReadCloser, limit: tc.shortRead}
				decoder.ReadCloser = first
			}
			var got []byte
			buffer := make([]byte, tc.firstRead)
			if tc.shortRead > 0 {
				// A reused caller buffer must not supply metadata beyond the
				// actual socket bytes returned by the first short Read.
				copy(buffer[tc.shortRead:], `</title><meta charset="gbk">`)
			}
			for reads := 0; ; reads++ {
				if reads == 64 {
					t.Fatal("real charset body did not reach terminal EOF")
				}
				n, readErr := resp.Body.Read(buffer)
				if n < 0 || n > len(buffer) {
					t.Fatalf("invalid public Reader byte count %d", n)
				}
				got = append(got, buffer[:n]...)
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					t.Fatalf("real charset body read: %v", readErr)
				}
			}
			if closeErr := resp.Body.Close(); closeErr != nil {
				t.Fatalf("actual response Close: %v", closeErr)
			}
			if first != nil && (first.size != tc.firstRead || len(first.first) != wireFirstRead || !bytes.Equal(first.first, wire[:wireFirstRead]) || first.close != 1) {
				t.Fatalf("actual first wire read/Close did not establish the fixture: size=%d bytes=%d close=%d", first.size, len(first.first), first.close)
			}
			select {
			case o := <-observed:
				if o.method != http.MethodGet || o.uri != "/meta-boundary" || o.protocol != 1 || o.written != len(wire) || o.err != nil {
					t.Fatalf("owned peer did not send the exact real HTTP fixture: %+v", o)
				}
			case <-ctx.Done():
				t.Fatalf("owned HTTP peer did not complete: %v", ctx.Err())
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("META_CHARSET_ASSERT: mode=%s real HTTP decoded bytes differ: gotLen=%d wantLen=%d got=%q want=%q", tc.name, len(got), len(want), got, want)
			}
			t.Logf("actual HTTP charset: mode=%s first=%d titleStart=%d wireBytes=%d decodedBytes=%d", tc.name, tc.firstRead, titleStart, len(wire), len(got))
		})
	}
}
