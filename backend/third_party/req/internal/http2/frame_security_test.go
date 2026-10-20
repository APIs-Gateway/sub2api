package http2

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	xhttp2 "golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func reqSecurityHeaderBlock(t *testing.T, fields []hpack.HeaderField) []byte {
	t.Helper()
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	for _, f := range fields {
		if err := encoder.WriteField(f); err != nil {
			t.Fatal(err)
		}
	}
	return block.Bytes()
}
func reqSecurityReadHeaders(t *testing.T, max uint32, fields []hpack.HeaderField) (*MetaHeadersFrame, error) {
	t.Helper()
	var wire bytes.Buffer
	writer := xhttp2.NewFramer(&wire, bytes.NewReader(nil))
	if err := writer.WriteHeaders(xhttp2.HeadersFrameParam{StreamID: 1, EndHeaders: true, BlockFragment: reqSecurityHeaderBlock(t, fields)}); err != nil {
		t.Fatal(err)
	}
	reader := NewFramer(io.Discard, &wire)
	reader.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	reader.MaxHeaderListSize = max
	frame, err := reader.ReadFrame()
	if err != nil {
		return nil, err
	}
	meta, ok := frame.(*MetaHeadersFrame)
	if !ok {
		t.Fatalf("got %T", frame)
	}
	return meta, nil
}
func TestReqHTTP2Security_TrailerExpansionBudget(t *testing.T) {
	fields := []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "trailer", Value: strings.TrimSuffix(strings.Repeat("x,", 20), ",")}}
	t.Run("expanded_names_exceed_512", func(t *testing.T) {
		f, err := reqSecurityReadHeaders(t, 512, fields)
		if err != nil {
			t.Fatal(err)
		}
		if !f.Truncated {
			t.Fatal("SECURITY_WIRE_ASSERT: twenty declared trailer fields exceeded budget but were accepted")
		}
	})
	t.Run("expanded_names_fit_1024", func(t *testing.T) {
		f, err := reqSecurityReadHeaders(t, 1024, fields)
		if err != nil {
			t.Fatal(err)
		}
		if f.Truncated || len(f.Fields) != 2 {
			t.Fatalf("valid trailers rejected: %+v", f)
		}
	})
	for _, limit := range []uint32{1 << 31, math.MaxUint32} {
		t.Run(fmtReqSecurityLimit(limit), func(t *testing.T) {
			// These limits exercise arithmetic only; the wire block stays under 100 bytes.
			f, err := reqSecurityReadHeaders(t, limit, []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "x-small", Value: "yes"}})
			if err != nil || f.Truncated || len(f.Fields) != 2 {
				t.Fatalf("large valid budget, tiny headers: frame=%v err=%v", f, err)
			}
		})
	}
}
func fmtReqSecurityLimit(n uint32) string {
	if n == 1<<31 {
		return "limit_2_to_31"
	}
	return "limit_MaxUint32"
}
func TestReqHTTP2Security_TruncatedContinuationIsBounded(t *testing.T) {
	var wire bytes.Buffer
	writer := xhttp2.NewFramer(&wire, bytes.NewReader(nil))
	block := reqSecurityHeaderBlock(t, []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "trailer", Value: strings.TrimSuffix(strings.Repeat("x,", 20), ",")}})
	if err := writer.WriteHeaders(xhttp2.HeadersFrameParam{StreamID: 1, EndHeaders: false, BlockFragment: block}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteContinuation(1, true, []byte{0}); err != nil {
		t.Fatal(err)
	}
	reader := NewFramer(io.Discard, &wire)
	reader.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	reader.MaxHeaderListSize = 512
	_, err := reader.ReadFrame()
	var connectionError ConnectionError
	if !errors.As(err, &connectionError) || connectionError != ConnectionError(ErrCodeProtocol) {
		t.Fatalf("truncated continuation got %v", err)
	}
}
func TestReqHTTP2Security_HeaderValidationAndDecoderState(t *testing.T) {
	t.Run("invalid_pseudo_header", func(t *testing.T) {
		_, err := reqSecurityReadHeaders(t, 1024, []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: ":unknown", Value: "bad"}})
		var streamError StreamError
		if !errors.As(err, &streamError) || streamError.Code != ErrCodeProtocol {
			t.Fatalf("invalid pseudo header: %v", err)
		}
	})
	t.Run("dynamic_state_after_truncation", func(t *testing.T) {
		var blocks bytes.Buffer
		encoder := hpack.NewEncoder(&blocks)
		for _, f := range []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "x-long", Value: strings.Repeat("a", 32)}, {Name: "x-reused", Value: "yes"}} {
			if err := encoder.WriteField(f); err != nil {
				t.Fatal(err)
			}
		}
		first := append([]byte(nil), blocks.Bytes()...)
		blocks.Reset()
		for _, f := range []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "x-reused", Value: "yes"}} {
			if err := encoder.WriteField(f); err != nil {
				t.Fatal(err)
			}
		}
		var wire bytes.Buffer
		writer := xhttp2.NewFramer(&wire, bytes.NewReader(nil))
		if err := writer.WriteHeaders(xhttp2.HeadersFrameParam{StreamID: 1, EndHeaders: true, BlockFragment: first}); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteHeaders(xhttp2.HeadersFrameParam{StreamID: 3, EndHeaders: true, BlockFragment: blocks.Bytes()}); err != nil {
			t.Fatal(err)
		}
		reader := NewFramer(io.Discard, &wire)
		reader.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
		reader.MaxHeaderListSize = 64
		f, err := reader.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if !f.(*MetaHeadersFrame).Truncated {
			t.Fatal("oversized block not truncated")
		}
		reader.MaxHeaderListSize = 256
		f, err = reader.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		meta := f.(*MetaHeadersFrame)
		if meta.Truncated || len(meta.Fields) != 2 || meta.Fields[1].Name != "x-reused" || meta.Fields[1].Value != "yes" {
			t.Fatalf("HPACK state lost after truncation: %+v", meta)
		}
	})
}
