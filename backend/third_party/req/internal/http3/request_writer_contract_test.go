// Copyright (c) 2016 the quic-go authors & Google, Inc.
// Derived from quic-go v0.57.1, revision 4b0ab7e48554e4440448c9252a92d7c065d88331.
// MIT license: see CONTRACT-TESTS-LICENSE.
// Source: http3/request_writer_test.go; private writer and decoded QPACK wire.
package http3

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/quic-go/qpack"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
	"github.com/quic-go/quic-go/testutils/events"

	"github.com/stretchr/testify/require"
)

func h3ContractDecodeRequest(t *testing.T, str io.Reader, streamID quic.StreamID, eventRecorder *events.Recorder) map[string]string {
	t.Helper()

	r := io.LimitedReader{R: str, N: 1000}
	fp := frameParser{r: &r}
	frame, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.IsType(t, &headersFrame{}, frame)
	headersFrame := frame.(*headersFrame)
	data := make([]byte, headersFrame.Length)
	_, err = io.ReadFull(&r, data)
	require.NoError(t, err)
	require.Equal(t, int64(1000), r.N+int64(len(data))+int64(len(headersFrame.Append(nil))))
	var extra [1]byte
	n, endErr := str.Read(extra[:])
	require.Zero(t, n)
	require.ErrorIs(t, endErr, io.EOF)
	hfs := h3ContractDecodeQpackHeaderFields(t, data)
	values := make(map[string]string)
	for _, hf := range hfs {
		require.NotContains(t, values, hf.Name, "this single-valued request must not encode duplicate fields")
		values[hf.Name] = hf.Value
	}

	headerFields := make([]qlog.HeaderField, len(hfs))
	for i, hf := range hfs {
		headerFields[i] = qlog.HeaderField{Name: hf.Name, Value: hf.Value}
	}
	require.Equal(t,
		[]qlogwriter.Event{
			qlog.FrameCreated{
				StreamID: streamID,
				Raw: qlog.RawInfo{
					Length:        int(1000 - r.N),
					PayloadLength: int(headersFrame.Length),
				},
				Frame: qlog.Frame{Frame: qlog.HeadersFrame{HeaderFields: headerFields}},
			},
		},
		eventRecorder.Events(qlog.FrameCreated{}),
	)

	return values
}

func TestReqHTTP3Contract_RequestWriterGetRequestGzip(t *testing.T) {
	t.Run("gzip", func(t *testing.T) {
		h3ContractRequestWriterGzip(t, true)
	})
	t.Run("no_gzip", func(t *testing.T) {
		h3ContractRequestWriterGzip(t, false)
	})
}

func h3ContractRequestWriterGzip(t *testing.T, gzip bool) {
	req := httptest.NewRequest(http.MethodGet, "https://quic-go.net/index.html?foo=bar", nil)
	req.AddCookie(&http.Cookie{Name: "foo", Value: "bar"})
	req.AddCookie(&http.Cookie{Name: "baz", Value: "lorem ipsum"})

	rw := newRequestWriter()
	var eventRecorder events.Recorder
	buf := &bytes.Buffer{}
	require.NoError(t, rw.WriteRequestHeader(buf, req, gzip, 42, &eventRecorder, nil))
	headerFields := h3ContractDecodeRequest(t, buf, 42, &eventRecorder)
	require.Equal(t, "quic-go.net", headerFields[":authority"])
	require.Equal(t, http.MethodGet, headerFields[":method"])
	require.Equal(t, "/index.html?foo=bar", headerFields[":path"])
	require.Equal(t, "https", headerFields[":scheme"])
	require.Equal(t, `foo=bar; baz="lorem ipsum"`, headerFields["cookie"])
	switch gzip {
	case true:
		require.Equal(t, "gzip", headerFields["accept-encoding"])
		require.Len(t, headerFields, 7)
	case false:
		require.NotContains(t, headerFields, "accept-encoding")
		require.Len(t, headerFields, 6)
	}
	require.NotEmpty(t, headerFields["user-agent"])
}

func TestReqHTTP3Contract_RequestWriterInvalidHostHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://quic-go.net/index.html?foo=bar", nil)
	req.Host = "foo@bar" // @ is invalid
	rw := newRequestWriter()
	var buf bytes.Buffer
	require.EqualError(t,
		rw.WriteRequestHeader(&buf, req, false, 0, nil, nil),
		"http3: invalid Host header",
	)
	require.Zero(t, buf.Len(), "invalid headers must not produce any wire bytes")
	// Reusing the writer after validation failure must not retain the failed
	// request's QPACK state or emit a partial first HEADERS frame.
	req.Host = "quic-go.net"
	var eventRecorder events.Recorder
	require.NoError(t, rw.WriteRequestHeader(&buf, req, false, 42, &eventRecorder, nil))
	fields := h3ContractDecodeRequest(t, &buf, 42, &eventRecorder)
	require.Equal(t, "quic-go.net", fields[":authority"])
	require.Equal(t, "/index.html?foo=bar", fields[":path"])
}

func TestReqHTTP3Contract_RequestWriterConnect(t *testing.T) {
	// httptest.NewRequest does not properly support the CONNECT method
	req, err := http.NewRequest(http.MethodConnect, "https://quic-go.net/", nil)
	require.NoError(t, err)
	rw := newRequestWriter()
	buf := &bytes.Buffer{}
	var eventRecorder events.Recorder
	require.NoError(t, rw.WriteRequestHeader(buf, req, false, 1337, &eventRecorder, nil))
	headerFields := h3ContractDecodeRequest(t, buf, 1337, &eventRecorder)
	require.Equal(t, http.MethodConnect, headerFields[":method"])
	require.Equal(t, "quic-go.net", headerFields[":authority"])
	require.NotContains(t, headerFields, ":path")
	require.NotContains(t, headerFields, ":scheme")
	require.NotContains(t, headerFields, ":protocol")
}

func TestReqHTTP3Contract_RequestWriterExtendedConnect(t *testing.T) {
	// httptest.NewRequest does not properly support the CONNECT method
	req, err := http.NewRequest(http.MethodConnect, "https://quic-go.net/", nil)
	require.NoError(t, err)
	req.Proto = "webtransport"
	rw := newRequestWriter()
	buf := &bytes.Buffer{}
	var eventRecorder events.Recorder
	require.NoError(t, rw.WriteRequestHeader(buf, req, false, 1234, &eventRecorder, nil))
	headerFields := h3ContractDecodeRequest(t, buf, 1234, &eventRecorder)
	require.Equal(t, "quic-go.net", headerFields[":authority"])
	require.Equal(t, http.MethodConnect, headerFields[":method"])
	require.Equal(t, "/", headerFields[":path"])
	require.Equal(t, "https", headerFields[":scheme"])
	require.Equal(t, "webtransport", headerFields[":protocol"])
}

func h3ContractDecodeQpackHeaderFields(t *testing.T, data []byte) []qpack.HeaderField {
	t.Helper()

	decoder := qpack.NewDecoder()
	decodeFn := decoder.Decode(data)
	var hfs []qpack.HeaderField
	for {
		hf, err := decodeFn()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		hfs = append(hfs, hf)
	}
	return hfs
}
