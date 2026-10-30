// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE-GO-BSD file at the req module root.
//
// Additional frame contracts adapted from golang/net http2/frame_test.go at
// 18ece0ce30bc35fa81fe72028bf309bb3ff3f4a5. See COVERAGE-CONTRACTS.md.
package http2

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	reqhttp2 "github.com/imroc/req/v3/http2"
	xhttp2 "golang.org/x/net/http2"
)

func TestReqHTTP2CoverageFrame_DiagnosticWire(t *testing.T) {
	longData := bytes.Repeat([]byte("q"), 300)
	cases := []struct {
		name            string
		write           func(*Framer) error
		typ             FrameType
		flags           Flags
		stream          uint32
		payload         []byte
		header, summary string
	}{
		{"settings", func(f *Framer) error {
			return f.WriteSettings(reqhttp2.Setting{ID: reqhttp2.SettingHeaderTableSize, Val: 64}, reqhttp2.Setting{ID: reqhttp2.SettingInitialWindowSize, Val: 100})
		}, FrameSettings, 0, 0, []byte{0, 1, 0, 0, 0, 64, 0, 4, 0, 0, 0, 100}, "SETTINGS len=12", "SETTINGS len=12, settings: HEADER_TABLE_SIZE=64, INITIAL_WINDOW_SIZE=100"},
		{"data_short", func(f *Framer) error { return f.WriteData(3, true, []byte("abc")) }, FrameData, FlagDataEndStream, 3, []byte("abc"), "DATA flags=END_STREAM stream=3 len=3", "DATA flags=END_STREAM stream=3 len=3 data=\"abc\""},
		{"data_truncated", func(f *Framer) error { return f.WriteData(3, true, longData) }, FrameData, FlagDataEndStream, 3, longData, "DATA flags=END_STREAM stream=3 len=300", "DATA flags=END_STREAM stream=3 len=300 data=" + fmt.Sprintf("%q", longData[:256]) + " (44 bytes omitted)"},
		{"window_connection", func(f *Framer) error { return f.WriteWindowUpdate(0, 4096) }, FrameWindowUpdate, 0, 0, []byte{0, 0, 16, 0}, "WINDOW_UPDATE len=4", "WINDOW_UPDATE len=4 (conn) incr=4096"},
		{"window_stream", func(f *Framer) error { return f.WriteWindowUpdate(3, 17) }, FrameWindowUpdate, 0, 3, []byte{0, 0, 0, 17}, "WINDOW_UPDATE stream=3 len=4", "WINDOW_UPDATE stream=3 len=4 incr=17"},
		{"ping", func(f *Framer) error { return f.WritePing(true, [8]byte{'1', '2', '3', '4', '5', '6', '7', '8'}) }, FramePing, FlagPingAck, 0, []byte("12345678"), "PING flags=ACK len=8", "PING flags=ACK len=8 ping=\"12345678\""},
		{"goaway", func(f *Framer) error { return f.WriteGoAway(3, ErrCodeEnhanceYourCalm, []byte("bounded")) }, FrameGoAway, 0, 0, append([]byte{0, 0, 0, 3, 0, 0, 0, 11}, []byte("bounded")...), "GOAWAY len=15", "GOAWAY len=15 LastStreamID=3 ErrCode=ENHANCE_YOUR_CALM Debug=\"bounded\""},
		{"rst_stream", func(f *Framer) error { return f.WriteRSTStream(3, ErrCodeCancel) }, FrameRSTStream, 0, 3, []byte{0, 0, 0, 8}, "RST_STREAM stream=3 len=4", "RST_STREAM stream=3 len=4 ErrCode=CANCEL"},
		{"unknown_type_and_flags", func(f *Framer) error { return f.WriteRawFrame(FrameType(255), Flags(0x81), 7, []byte("ext")) }, FrameType(255), Flags(0x81), 7, []byte("ext"), "UNKNOWN_FRAME_TYPE_255 flags=0x1|0x80 stream=7 len=3", "UNKNOWN_FRAME_TYPE_255 flags=0x1|0x80 stream=7 len=3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var wire bytes.Buffer
			var writeLogs, readLogs []string
			fr := NewFramer(&wire, nil)
			fr.logWrites = true
			fr.debugWriteLoggerf = func(format string, args ...any) { writeLogs = append(writeLogs, fmt.Sprintf(format, args...)) }
			if err := tc.write(fr); err != nil {
				t.Fatal(err)
			}
			wantWire := reqH2ContractWire(tc.typ, tc.flags, tc.stream, tc.payload)
			if !bytes.Equal(wire.Bytes(), wantWire) {
				t.Fatalf("logged write changed wire: %x, want %x", wire.Bytes(), wantWire)
			}
			// The independent codec must accept the complete bytes, including the
			// bytes intentionally omitted from the diagnostic DATA preview.
			peerReader := bytes.NewReader(wire.Bytes())
			peer := xhttp2.NewFramer(io.Discard, peerReader)
			actualPeer, err := peer.ReadFrame()
			if err != nil {
				t.Fatalf("independent decoder: %v", err)
			}
			h := actualPeer.Header()
			if h.Type != xhttp2.FrameType(tc.typ) || h.Flags != xhttp2.Flags(tc.flags) || h.StreamID != tc.stream || h.Length != uint32(len(tc.payload)) || peerReader.Len() != 0 {
				t.Fatalf("independent header=%+v leftover=%d", h, peerReader.Len())
			}
			if data, ok := actualPeer.(*xhttp2.DataFrame); ok && !bytes.Equal(data.Data(), tc.payload) {
				t.Fatalf("independent DATA=%q", data.Data())
			}
			if len(writeLogs) != 1 || writeLogs[0] != fmt.Sprintf("http2: Framer %p: wrote %s", fr, tc.summary) {
				t.Fatalf("write logs=%q", writeLogs)
			}
			reader := NewFramer(io.Discard, bytes.NewReader(wire.Bytes()))
			reader.logReads = true
			reader.debugReadLoggerf = func(format string, args ...any) { readLogs = append(readLogs, fmt.Sprintf(format, args...)) }
			actual, err := reader.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			if got := actual.Header().String(); got != "[FrameHeader "+tc.header+"]" {
				t.Fatalf("FrameHeader.String=%q", got)
			}
			if len(readLogs) != 1 || readLogs[0] != fmt.Sprintf("http2: Framer %p: read %s", reader, tc.summary) {
				t.Fatalf("read logs=%q", readLogs)
			}
		})
	}
}

func TestReqHTTP2CoverageFrame_DebugDecodeFailure(t *testing.T) {
	var wire bytes.Buffer
	var logs []string
	fr := NewFramer(&wire, nil)
	fr.logWrites = true
	fr.debugWriteLoggerf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	if err := fr.WriteRawFrame(FramePing, 0, 0, []byte("x")); err != nil {
		t.Fatal(err)
	}
	want := reqH2ContractWire(FramePing, 0, 0, []byte("x"))
	if !bytes.Equal(wire.Bytes(), want) {
		t.Fatalf("raw malformed wire=%x, want %x", wire.Bytes(), want)
	}
	peer := xhttp2.NewFramer(io.Discard, bytes.NewReader(wire.Bytes()))
	if _, err := peer.ReadFrame(); err == nil {
		t.Fatal("independent peer accepted malformed PING length")
	}
	if len(logs) != 1 || logs[0] != fmt.Sprintf("http2: Framer %p: failed to decode just-written frame", fr) || strings.Contains(logs[0], ": wrote ") {
		t.Fatalf("decode failure logs=%q", logs)
	}
}

func TestReqHTTP2CoverageFrame_MaxReadSizeClamp(t *testing.T) {
	payload := bytes.Repeat([]byte{0x42}, 32<<10)
	wire := reqH2ContractWire(FrameType(255), 0, 7, payload)
	reader := bytes.NewReader(wire)
	fr := NewFramer(io.Discard, reader)
	fr.SetMaxReadFrameSize(^uint32(0))
	if fr.maxReadSize != (1<<24)-1 {
		t.Fatalf("max read size=%d", fr.maxReadSize)
	}
	frame, err := fr.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	unknown, ok := frame.(*UnknownFrame)
	if !ok || unknown.StreamID != 7 || !bytes.Equal(unknown.Payload(), payload) || reader.Len() != 0 {
		t.Fatalf("clamped reader frame=%T leftover=%d", frame, reader.Len())
	}
	peer := xhttp2.NewFramer(io.Discard, bytes.NewReader(wire))
	peer.SetMaxReadFrameSize(32 << 10)
	other, err := peer.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(other.(*xhttp2.UnknownFrame).Payload(), payload) {
		t.Fatal("independent extension payload mismatch")
	}
}

func TestReqHTTP2CoverageFrame_DiagnosticWriterErrorPreserved(t *testing.T) {
	sentinel := errors.New("owned diagnostic writer failed")
	writer := &reqH2ContractWriter{limit: 4, err: sentinel}
	var logs []string
	fr := NewFramer(writer, nil)
	fr.logWrites = true
	fr.debugWriteLoggerf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	err := fr.WriteData(1, true, []byte("abc"))
	want := reqH2ContractWire(FrameData, FlagDataEndStream, 1, []byte("abc"))
	if err != sentinel || writer.calls != 1 || !bytes.Equal(writer.offered, want) || !bytes.Equal(writer.accepted, want[:4]) {
		t.Fatalf("writer outcome err=%v calls=%d offered=%x accepted=%x", err, writer.calls, writer.offered, writer.accepted)
	}
	if len(logs) != 1 || logs[0] != fmt.Sprintf("http2: Framer %p: wrote DATA flags=END_STREAM stream=1 len=3 data=\"abc\"", fr) {
		t.Fatalf("writer failure diagnostic=%q", logs)
	}
	peer := xhttp2.NewFramer(io.Discard, bytes.NewReader(writer.offered))
	frame, err := peer.ReadFrame()
	if err != nil || !bytes.Equal(frame.(*xhttp2.DataFrame).Data(), []byte("abc")) {
		t.Fatalf("full offered frame=%v err=%v", frame, err)
	}
	partial := xhttp2.NewFramer(io.Discard, bytes.NewReader(writer.accepted))
	if _, err := partial.ReadFrame(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("actual accepted wire error=%v", err)
	}
}
