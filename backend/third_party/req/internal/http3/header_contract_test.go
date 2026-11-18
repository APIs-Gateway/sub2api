// Copyright (c) 2016 the quic-go authors & Google, Inc.
// Derived from quic-go v0.57.1, revision 4b0ab7e48554e4440448c9252a92d7c065d88331.
// MIT license: see CONTRACT-TESTS-LICENSE.
// Source: http3/headers_test.go; client response/trailer subset only.
package http3

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net/http"
	"testing"

	"github.com/quic-go/qpack"
	"github.com/stretchr/testify/require"
)

func h3ContractDecodeFromSlice(headers []qpack.HeaderField) qpack.DecodeFunc {
	var i int
	return func() (qpack.HeaderField, error) {
		if i >= len(headers) {
			return qpack.HeaderField{}, io.EOF
		}
		h := headers[i]
		i++
		return h, nil
	}
}

func TestReqHTTP3Contract_ResponseHeaderParsing(t *testing.T) {
	headers := []qpack.HeaderField{
		{Name: ":status", Value: "200"},
		{Name: "content-length", Value: "42"},
	}
	rsp := &http.Response{}
	require.NoError(t, updateResponseFromHeaders(rsp, h3ContractDecodeFromSlice(headers), math.MaxInt, nil, nil))
	require.Equal(t, "HTTP/3.0", rsp.Proto)
	require.Equal(t, 3, rsp.ProtoMajor)
	require.Zero(t, rsp.ProtoMinor)
	require.Equal(t, int64(42), rsp.ContentLength)
	require.Equal(t, 1, len(rsp.Header))
	require.Equal(t, "42", rsp.Header.Get("Content-Length"))
	require.Nil(t, rsp.Body)
	require.Equal(t, 200, rsp.StatusCode)
	require.Equal(t, "200 OK", rsp.Status)
}

func TestReqHTTP3Contract_ResponseHeaderParsingValidation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		headers     []qpack.HeaderField
		err         string
		errContains string
	}{
		{
			name: "missing_status",
			headers: []qpack.HeaderField{
				{Name: "content-length", Value: "42"},
			},
			err: "missing :status field",
		},
		{
			name: "invalid_status_code",
			headers: []qpack.HeaderField{
				{Name: ":status", Value: "foobar"},
			},
			errContains: "invalid status code",
		},
		{
			name: "request_method", // :method is a request pseudo header
			headers: []qpack.HeaderField{
				{Name: ":method", Value: http.MethodGet},
			},
			err: "invalid response pseudo header: :method",
		},
		{
			name: "duplicate_status",
			headers: []qpack.HeaderField{
				{Name: ":status", Value: "200"},
				{Name: ":status", Value: "404"},
			},
			err: "duplicate pseudo header: :status",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := updateResponseFromHeaders(&http.Response{}, h3ContractDecodeFromSlice(tc.headers), math.MaxInt, nil, nil)
			if tc.errContains != "" {
				require.ErrorContains(t, err, tc.errContains)
			}
			if tc.err != "" {
				require.EqualError(t, err, tc.err)
			}
		})
	}

	for _, tc := range []struct {
		name         string
		invalidField string
	}{
		{name: "connection", invalidField: "connection"},
		{name: "keep-alive", invalidField: "keep-alive"},
		{name: "proxy-connection", invalidField: "proxy-connection"},
		{name: "transfer-encoding", invalidField: "transfer-encoding"},
		{name: "upgrade", invalidField: "upgrade"},
	} {
		t.Run("invalid_field_"+tc.name, func(t *testing.T) {
			headers := []qpack.HeaderField{
				{Name: ":status", Value: "404"},
				{Name: tc.invalidField, Value: "some-value"},
			}
			err := updateResponseFromHeaders(&http.Response{}, h3ContractDecodeFromSlice(headers), math.MaxInt, nil, nil)
			require.EqualError(t, err, fmt.Sprintf("invalid header field name: %q", tc.invalidField))
		})
	}
}

func TestReqHTTP3Contract_ResponseTrailerFields(t *testing.T) {
	headers := []qpack.HeaderField{
		{Name: ":status", Value: "200"},
		{Name: "trailer", Value: "Trailer1, Trailer2"},
		{Name: "trailer", Value: "TRAILER3"},
	}
	var rsp http.Response
	require.NoError(t, updateResponseFromHeaders(&rsp, h3ContractDecodeFromSlice(headers), math.MaxInt, nil, nil))
	require.Equal(t, 0, len(rsp.Header))
	require.Equal(t, http.Header(map[string][]string{
		"Trailer1": nil,
		"Trailer2": nil,
		"Trailer3": nil,
	}), rsp.Trailer)
}

func TestReqHTTP3Contract_ResponseTrailerParsingTE(t *testing.T) {
	headers := []qpack.HeaderField{
		{Name: ":status", Value: "404"},
		{Name: "te", Value: "trailers"},
	}
	require.NoError(t, updateResponseFromHeaders(&http.Response{}, h3ContractDecodeFromSlice(headers), math.MaxInt, nil, nil))
	headers = []qpack.HeaderField{
		{Name: ":status", Value: "404"},
		{Name: "te", Value: "not-trailers"},
	}
	require.EqualError(t,
		updateResponseFromHeaders(&http.Response{}, h3ContractDecodeFromSlice(headers), math.MaxInt, nil, nil),
		`invalid TE header field value: "not-trailers"`)
}

func TestReqHTTP3Contract_ResponseTrailerParsing(t *testing.T) {
	trailerHdr, err := parseTrailers(h3ContractDecodeFromSlice([]qpack.HeaderField{
		{Name: "content-length", Value: "42"},
	}), nil)
	require.NoError(t, err)
	require.Equal(t, "42", trailerHdr.Get("Content-Length"))
}

func TestReqHTTP3Contract_ResponseTrailerParsingValidation(t *testing.T) {
	headers := []qpack.HeaderField{
		{Name: ":status", Value: "200"},
	}
	_, err := parseTrailers(h3ContractDecodeFromSlice(headers), nil)
	require.EqualError(t, err, "http3: received pseudo header in trailer: :status")
}

func TestReqHTTP3Contract_QpackError(t *testing.T) {
	buf := &bytes.Buffer{}
	enc := qpack.NewEncoder(buf)
	enc.WriteField(qpack.HeaderField{Name: ":status", Value: "200"})
	enc.Close()

	t.Run("shared_header_parsing", func(t *testing.T) {
		dec := qpack.NewDecoder()
		decodeFn := dec.Decode(buf.Bytes()[:len(buf.Bytes())/2])
		_, err := parseHeaders(decodeFn, true, math.MaxInt, nil, nil)
		require.ErrorAs(t, err, new(*qpackError))
	})

	t.Run("response_header_parsing", func(t *testing.T) {
		dec := qpack.NewDecoder()
		decodeFn := dec.Decode(buf.Bytes()[:len(buf.Bytes())/2])
		err := updateResponseFromHeaders(&http.Response{}, decodeFn, math.MaxInt, nil, nil)
		require.ErrorAs(t, err, new(*qpackError))
	})
}

// These field-level Content-Length and size contracts adapt the official
// headers_test.go validation tables to the private client response adapter.
// No requestFromHeaders server API is introduced.
func TestReqHTTP3Contract_ResponseContentLength(t *testing.T) {
	for _, tc := range []struct {
		name      string
		values    []string
		want      int64
		wantError string
	}{
		{name: "absent", want: -1},
		{name: "duplicate_equal", values: []string{"42", "42"}, want: 42},
		{name: "duplicate_conflict", values: []string{"42", "43"}, wantError: "contradicting content lengths (42 and 43)"},
		{name: "negative", values: []string{"-1"}, wantError: "invalid content length:"},
		{name: "overflow", values: []string{"9223372036854775808"}, wantError: "invalid content length:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := []qpack.HeaderField{{Name: ":status", Value: "200"}}
			for _, value := range tc.values {
				fields = append(fields, qpack.HeaderField{Name: "content-length", Value: value})
			}
			var rsp http.Response
			err := updateResponseFromHeaders(&rsp, h3ContractDecodeFromSlice(fields), math.MaxInt, nil, nil)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, rsp.ContentLength)
			if len(tc.values) > 0 {
				require.Equal(t, []string{tc.values[0]}, rsp.Header.Values("Content-Length"))
			} else {
				require.NotContains(t, rsp.Header, "Content-Length")
			}
		})
	}
}

func TestReqHTTP3Contract_ResponseFieldSectionBudget(t *testing.T) {
	// RFC 9114 field size is name + value + 32, including pseudo headers.
	fields := []qpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "x-test", Value: "valid"}}
	budget := len(":status") + len("200") + 32 + len("x-test") + len("valid") + 32
	for _, tc := range []struct {
		name     string
		limit    int
		tooLarge bool
	}{
		{name: "exact_boundary", limit: budget},
		{name: "one_byte_short", limit: budget - 1, tooLarge: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rsp http.Response
			err := updateResponseFromHeaders(&rsp, h3ContractDecodeFromSlice(fields), tc.limit, nil, nil)
			if tc.tooLarge {
				require.ErrorIs(t, err, errHeaderTooLarge)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 200, rsp.StatusCode)
			require.Equal(t, "valid", rsp.Header.Get("X-Test"))
		})
	}
}
