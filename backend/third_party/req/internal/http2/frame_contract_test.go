// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE-GO-BSD file.
//
// Contracts adapted from golang/net http2/frame_test.go at
// 18ece0ce30bc35fa81fe72028bf309bb3ff3f4a5 (v0.60.0).
// The private req Framer is the implementation under test. The additional
// malformed-wire cases exercise its production ReadFrame entry point.

package http2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	reqhttp2 "github.com/imroc/req/v3/http2"
	"golang.org/x/net/http2/hpack"
)

// Build independent peer bytes, including the reserved stream bit when asked.
// Do not use the Framer writer to construct expected output or malformed input.
func reqH2ContractWire(typ FrameType, flags Flags, stream uint32, payload []byte) []byte {
	wire := make([]byte, 9+len(payload))
	wire[0], wire[1], wire[2] = byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload))
	wire[3], wire[4] = byte(typ), byte(flags)
	binary.BigEndian.PutUint32(wire[5:9], stream)
	copy(wire[9:], payload)
	return wire
}

func reqH2ContractRead(t *testing.T, wire []byte) Frame {
	t.Helper()
	r := bytes.NewReader(wire)
	fr := NewFramer(io.Discard, r)
	frame, err := fr.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if r.Len() != 0 {
		t.Fatalf("frame left %d peer bytes unread", r.Len())
	}
	return frame
}

func TestReqHTTP2FrameContract_WriterWire(t *testing.T) {
	priority := reqhttp2.PriorityParam{StreamDep: 3, Exclusive: true, Weight: 77}
	cases := []struct {
		name string
		write func(*Framer) error
		typ FrameType
		flags Flags
		stream uint32
		payload []byte
		check func(*testing.T, Frame)
	}{
		{"rst", func(f *Framer) error { return f.WriteRSTStream(0x01020304, ErrCode(0x07060504)) }, FrameRSTStream, 0, 0x01020304, []byte{7, 6, 5, 4}, func(t *testing.T, f Frame) {
			if f.(*RSTStreamFrame).ErrCode != ErrCode(0x07060504) { t.Fatal(f) }
		}},
		{"data_end", func(f *Framer) error { return f.WriteData(1, true, []byte("ABC")) }, FrameData, FlagDataEndStream, 1, []byte("ABC"), func(t *testing.T, f Frame) {
			d := f.(*DataFrame)
			if !d.StreamEnded() || string(d.Data()) != "ABC" { t.Fatal(d) }
		}},
		{"data_empty_padding", func(f *Framer) error { return f.WriteDataPadded(1, false, []byte("ABC"), []byte{}) }, FrameData, FlagDataPadded, 1, []byte{0, 'A', 'B', 'C'}, func(t *testing.T, f Frame) {
			d := f.(*DataFrame)
			if d.StreamEnded() || string(d.Data()) != "ABC" { t.Fatal(d) }
		}},
		{"data_max_padding", func(f *Framer) error { return f.WriteDataPadded(1, true, []byte("ABC"), make([]byte, 255)) }, FrameData, FlagDataPadded | FlagDataEndStream, 1, append([]byte{255, 'A', 'B', 'C'}, make([]byte, 255)...), func(t *testing.T, f Frame) {
			if string(f.(*DataFrame).Data()) != "ABC" { t.Fatal(f) }
		}},
		{"headers", func(f *Framer) error { return f.WriteHeaders(HeadersFrameParam{StreamID: 42, BlockFragment: []byte("abc"), EndStream: true, EndHeaders: true}) }, FrameHeaders, FlagHeadersEndStream | FlagHeadersEndHeaders, 42, []byte("abc"), func(t *testing.T, f Frame) {
			h := f.(*HeadersFrame)
			if !h.StreamEnded() || !h.HeadersEnded() || h.HasPriority() || string(h.HeaderBlockFragment()) != "abc" { t.Fatal(h) }
		}},
		{"headers_basic_open", func(f *Framer) error { return f.WriteHeaders(HeadersFrameParam{StreamID: 42, BlockFragment: []byte("abc")}) }, FrameHeaders, 0, 42, []byte("abc"), func(t *testing.T, f Frame) {
			h := f.(*HeadersFrame)
			if h.StreamEnded() || h.HeadersEnded() || h.HasPriority() || string(h.HeaderBlockFragment()) != "abc" { t.Fatal(h) }
		}},
		{"headers_padding_only", func(f *Framer) error { return f.WriteHeaders(HeadersFrameParam{StreamID: 42, BlockFragment: []byte("abc"), EndStream: true, EndHeaders: true, PadLength: 5}) }, FrameHeaders, FlagHeadersEndStream | FlagHeadersEndHeaders | FlagHeadersPadded, 42, []byte{5, 'a', 'b', 'c', 0, 0, 0, 0, 0}, func(t *testing.T, f Frame) {
			h := f.(*HeadersFrame)
			if !h.StreamEnded() || !h.HeadersEnded() || h.HasPriority() || string(h.HeaderBlockFragment()) != "abc" { t.Fatal(h) }
		}},
		{"headers_zero_length", func(f *Framer) error { return f.WriteHeaders(HeadersFrameParam{StreamID: 42}) }, FrameHeaders, 0, 42, nil, func(t *testing.T, f Frame) {
			h := f.(*HeadersFrame)
			if h.StreamEnded() || h.HeadersEnded() || h.HasPriority() || len(h.HeaderBlockFragment()) != 0 { t.Fatal(h) }
		}},
		{"headers_priority_end", func(f *Framer) error { return f.WriteHeaders(HeadersFrameParam{StreamID: 42, BlockFragment: []byte("abc"), EndStream: true, EndHeaders: true, Priority: reqhttp2.PriorityParam{StreamDep: 15, Exclusive: true, Weight: 127}, PadLength: 2}) }, FrameHeaders, FlagHeadersEndStream | FlagHeadersEndHeaders | FlagHeadersPriority | FlagHeadersPadded, 42, []byte{2, 0x80, 0, 0, 15, 127, 'a', 'b', 'c', 0, 0}, func(t *testing.T, f Frame) {
			h := f.(*HeadersFrame)
			if !h.StreamEnded() || !h.HeadersEnded() || !h.HasPriority() || h.Priority != (reqhttp2.PriorityParam{StreamDep: 15, Exclusive: true, Weight: 127}) || string(h.HeaderBlockFragment()) != "abc" { t.Fatal(h) }
		}},
		{"headers_priority_dependency_zero", func(f *Framer) error { return f.WriteHeaders(HeadersFrameParam{StreamID: 42, BlockFragment: []byte("abc"), EndStream: true, EndHeaders: true, Priority: reqhttp2.PriorityParam{Exclusive: true, Weight: 127}, PadLength: 2}) }, FrameHeaders, FlagHeadersEndStream | FlagHeadersEndHeaders | FlagHeadersPriority | FlagHeadersPadded, 42, []byte{2, 0x80, 0, 0, 0, 127, 'a', 'b', 'c', 0, 0}, func(t *testing.T, f Frame) {
			h := f.(*HeadersFrame)
			if !h.StreamEnded() || !h.HeadersEnded() || !h.HasPriority() || h.Priority != (reqhttp2.PriorityParam{Exclusive: true, Weight: 127}) || string(h.HeaderBlockFragment()) != "abc" { t.Fatal(h) }
		}},
		{"headers_priority_padding", func(f *Framer) error { return f.WriteHeaders(HeadersFrameParam{StreamID: 42, BlockFragment: []byte("abc"), EndHeaders: true, Priority: priority, PadLength: 2}) }, FrameHeaders, FlagHeadersEndHeaders | FlagHeadersPriority | FlagHeadersPadded, 42, []byte{2, 0x80, 0, 0, 3, 77, 'a', 'b', 'c', 0, 0}, func(t *testing.T, f Frame) {
			h := f.(*HeadersFrame)
			if h.StreamEnded() || !h.HeadersEnded() || !h.HasPriority() || h.Priority != priority || string(h.HeaderBlockFragment()) != "abc" { t.Fatal(h) }
		}},
		{"priority", func(f *Framer) error { return f.WritePriority(42, priority) }, FramePriority, 0, 42, []byte{0x80, 0, 0, 3, 77}, func(t *testing.T, f Frame) {
			if f.(*PriorityFrame).PriorityParam != priority { t.Fatal(f) }
		}},
		{"priority_not_exclusive", func(f *Framer) error { return f.WritePriority(42, reqhttp2.PriorityParam{StreamDep: 2, Weight: 127}) }, FramePriority, 0, 42, []byte{0, 0, 0, 2, 127}, func(t *testing.T, f Frame) {
			if f.(*PriorityFrame).PriorityParam != (reqhttp2.PriorityParam{StreamDep: 2, Weight: 127}) { t.Fatal(f) }
		}},
		{"settings", func(f *Framer) error { return f.WriteSettings(reqhttp2.Setting{ID: 1, Val: 2}, reqhttp2.Setting{ID: 3, Val: 4}) }, FrameSettings, 0, 0, []byte{0, 1, 0, 0, 0, 2, 0, 3, 0, 0, 0, 4}, func(t *testing.T, f Frame) {
			s := f.(*SettingsFrame)
			var got []reqhttp2.Setting
			if err := s.ForeachSetting(func(v reqhttp2.Setting) error { got = append(got, v); return nil }); err != nil { t.Fatal(err) }
			want := []reqhttp2.Setting{{ID: 1, Val: 2}, {ID: 3, Val: 4}}
			v, ok := s.Value(3)
			if s.IsAck() || s.NumSettings() != 2 || !reflect.DeepEqual(got, want) || !ok || v != 4 { t.Fatalf("settings=%v value=%d found=%v", got, v, ok) }
			if v, ok := s.Value(0xff); ok || v != 0 { t.Fatalf("unknown setting=%d,%v", v, ok) }
		}},
		{"settings_ack", func(f *Framer) error { return f.WriteSettingsAck() }, FrameSettings, FlagSettingsAck, 0, nil, func(t *testing.T, f Frame) {
			if s := f.(*SettingsFrame); !s.IsAck() || s.NumSettings() != 0 { t.Fatal(s) }
		}},
		{"ping", func(f *Framer) error { return f.WritePing(false, [8]byte{1, 2, 3, 4, 5, 6, 7, 8}) }, FramePing, 0, 0, []byte{1, 2, 3, 4, 5, 6, 7, 8}, func(t *testing.T, f Frame) {
			p := f.(*PingFrame)
			if p.IsAck() || p.Data != [8]byte{1, 2, 3, 4, 5, 6, 7, 8} { t.Fatal(p) }
		}},
		{"ping_ack", func(f *Framer) error { return f.WritePing(true, [8]byte{1, 2, 3, 4, 5, 6, 7, 8}) }, FramePing, FlagPingAck, 0, []byte{1, 2, 3, 4, 5, 6, 7, 8}, func(t *testing.T, f Frame) {
			p := f.(*PingFrame)
			if !p.IsAck() || p.Data != [8]byte{1, 2, 3, 4, 5, 6, 7, 8} { t.Fatal(p) }
		}},
		{"window_connection", func(f *Framer) error { return f.WriteWindowUpdate(0, 0x7fffffff) }, FrameWindowUpdate, 0, 0, []byte{0x7f, 0xff, 0xff, 0xff}, func(t *testing.T, f Frame) {
			if f.(*WindowUpdateFrame).Increment != 0x7fffffff { t.Fatal(f) }
		}},
		{"window_stream", func(f *Framer) error { return f.WriteWindowUpdate(0x01020304, 0x07060504) }, FrameWindowUpdate, 0, 0x01020304, []byte{7, 6, 5, 4}, func(t *testing.T, f Frame) {
			if f.(*WindowUpdateFrame).Increment != 0x07060504 { t.Fatal(f) }
		}},
		{"goaway_reserved_stream_bit", func(f *Framer) error { return f.WriteGoAway(0x81020304, ErrCodeCancel, []byte("debug")) }, FrameGoAway, 0, 0, append([]byte{1, 2, 3, 4, 0, 0, 0, 8}, []byte("debug")...), func(t *testing.T, f Frame) {
			g := f.(*GoAwayFrame)
			if g.LastStreamID != 0x01020304 || g.ErrCode != ErrCodeCancel || string(g.DebugData()) != "debug" { t.Fatal(g) }
		}},
		{"push_promise_padding", func(f *Framer) error { return f.WritePushPromise(PushPromiseParam{StreamID: 1, PromiseID: 2, EndHeaders: true, PadLength: 2, BlockFragment: []byte("abc")}) }, FramePushPromise, FlagPushPromiseEndHeaders | FlagPushPromisePadded, 1, []byte{2, 0, 0, 0, 2, 'a', 'b', 'c', 0, 0}, func(t *testing.T, f Frame) {
			p := f.(*PushPromiseFrame)
			if p.PromiseID != 2 || !p.HeadersEnded() || string(p.HeaderBlockFragment()) != "abc" { t.Fatal(p) }
		}},
		{"push_promise_unpadded_open", func(f *Framer) error { return f.WritePushPromise(PushPromiseParam{StreamID: 42, PromiseID: 42, BlockFragment: []byte("abc")}) }, FramePushPromise, 0, 42, []byte{0, 0, 0, 42, 'a', 'b', 'c'}, func(t *testing.T, f Frame) {
			p := f.(*PushPromiseFrame)
			if p.PromiseID != 42 || p.HeadersEnded() || string(p.HeaderBlockFragment()) != "abc" { t.Fatal(p) }
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := tt.write(NewFramer(&out, bytes.NewReader(nil))); err != nil { t.Fatal(err) }
			want := reqH2ContractWire(tt.typ, tt.flags, tt.stream, tt.payload)
			if !bytes.Equal(out.Bytes(), want) { t.Fatalf("wire=%x want=%x", out.Bytes(), want) }
			frame := reqH2ContractRead(t, out.Bytes())
			if h := frame.Header(); h.Type != tt.typ || h.Flags != tt.flags || h.StreamID != tt.stream || h.Length != uint32(len(tt.payload)) { t.Fatalf("header=%+v", h) }
			tt.check(t, frame)
		})
	}
}

func TestReqHTTP2FrameContract_ContinuationWire(t *testing.T) {
	for _, end := range []bool{false, true} {
		name := "open"
		if end { name = "end" }
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			writer := NewFramer(&out, bytes.NewReader(nil))
			if err := writer.WriteHeaders(HeadersFrameParam{StreamID: 42, BlockFragment: []byte("ab")}); err != nil { t.Fatal(err) }
			if err := writer.WriteContinuation(42, end, []byte("cd")); err != nil { t.Fatal(err) }
			flags := Flags(0)
			if end { flags = FlagContinuationEndHeaders }
			want := append(reqH2ContractWire(FrameHeaders, 0, 42, []byte("ab")), reqH2ContractWire(FrameContinuation, flags, 42, []byte("cd"))...)
			if !bytes.Equal(out.Bytes(), want) { t.Fatalf("wire=%x want=%x", out.Bytes(), want) }
			reader := NewFramer(io.Discard, &out)
			if _, err := reader.ReadFrame(); err != nil { t.Fatal(err) }
			frame, err := reader.ReadFrame()
			if err != nil { t.Fatal(err) }
			c := frame.(*ContinuationFrame)
			if c.HeadersEnded() != end || string(c.HeaderBlockFragment()) != "cd" || out.Len() != 0 { t.Fatal(c) }
		})
	}
}

func TestReqHTTP2FrameContract_MalformedWire(t *testing.T) {
	cases := []struct {
		name string
		typ FrameType
		flags Flags
		stream uint32
		payload []byte
		want error
		token string
	}{
		{"data_stream_zero", FrameData, 0, 0, []byte("x"), ConnectionError(ErrCodeProtocol), "frame_data_stream_0"},
		{"data_padding_missing", FrameData, FlagDataPadded, 1, nil, io.ErrUnexpectedEOF, "frame_data_pad_byte_short"},
		{"data_padding_overrun", FrameData, FlagDataPadded, 1, []byte{2, 0}, ConnectionError(ErrCodeProtocol), "frame_data_pad_too_big"},
		{"settings_ack_payload", FrameSettings, FlagSettingsAck, 0, make([]byte, 6), ConnectionError(ErrCodeFrameSize), "frame_settings_ack_with_length"},
		{"settings_stream", FrameSettings, 0, 1, nil, ConnectionError(ErrCodeProtocol), "frame_settings_has_stream"},
		{"settings_length", FrameSettings, 0, 0, []byte{0}, ConnectionError(ErrCodeFrameSize), "frame_settings_mod_6"},
		{"settings_window_overflow", FrameSettings, 0, 0, []byte{0, 4, 0x80, 0, 0, 0}, ConnectionError(ErrCodeFlowControl), "frame_settings_window_size_too_big"},
		{"ping_length", FramePing, 0, 0, make([]byte, 7), ConnectionError(ErrCodeFrameSize), "frame_ping_length"},
		{"ping_stream", FramePing, FlagPingAck, 1, make([]byte, 8), ConnectionError(ErrCodeProtocol), "frame_ping_has_stream"},
		{"goaway_stream", FrameGoAway, 0, 1, make([]byte, 8), ConnectionError(ErrCodeProtocol), "frame_goaway_has_stream"},
		{"goaway_length", FrameGoAway, 0, 0, make([]byte, 7), ConnectionError(ErrCodeFrameSize), "frame_goaway_short"},
		{"window_length", FrameWindowUpdate, 0, 0, make([]byte, 3), ConnectionError(ErrCodeFrameSize), "frame_windowupdate_bad_len"},
		{"window_zero_connection", FrameWindowUpdate, 0, 0, make([]byte, 4), ConnectionError(ErrCodeProtocol), "frame_windowupdate_zero_inc_conn"},
		{"window_zero_stream", FrameWindowUpdate, 0, 3, []byte{0x80, 0, 0, 0}, StreamError{StreamID: 3, Code: ErrCodeProtocol}, "frame_windowupdate_zero_inc_stream"},
		{"headers_stream_zero", FrameHeaders, FlagHeadersEndHeaders, 0, nil, ConnectionError(ErrCodeProtocol), "frame_headers_zero_stream"},
		{"headers_padding_missing", FrameHeaders, FlagHeadersPadded, 1, nil, io.ErrUnexpectedEOF, "frame_headers_pad_short"},
		{"headers_dependency_short", FrameHeaders, FlagHeadersPriority, 1, make([]byte, 3), io.ErrUnexpectedEOF, "frame_headers_prio_short"},
		{"headers_weight_missing", FrameHeaders, FlagHeadersPriority, 1, make([]byte, 4), io.ErrUnexpectedEOF, "frame_headers_prio_weight_short"},
		{"headers_padding_overrun", FrameHeaders, FlagHeadersPadded, 3, []byte{2, 0}, StreamError{StreamID: 3, Code: ErrCodeProtocol}, "frame_headers_pad_too_big"},
		{"priority_stream_zero", FramePriority, 0, 0, make([]byte, 5), ConnectionError(ErrCodeProtocol), "frame_priority_zero_stream"},
		{"priority_length", FramePriority, 0, 1, make([]byte, 4), ConnectionError(ErrCodeFrameSize), "frame_priority_bad_length"},
		{"rst_length", FrameRSTStream, 0, 1, make([]byte, 3), ConnectionError(ErrCodeFrameSize), "frame_rststream_bad_len"},
		{"rst_stream_zero", FrameRSTStream, 0, 0, make([]byte, 4), ConnectionError(ErrCodeProtocol), "frame_rststream_zero_stream"},
		{"continuation_stream_zero", FrameContinuation, FlagContinuationEndHeaders, 0, nil, ConnectionError(ErrCodeProtocol), "frame_continuation_zero_stream"},
		{"push_stream_zero", FramePushPromise, 0, 0, make([]byte, 4), ConnectionError(ErrCodeProtocol), "frame_pushpromise_zero_stream"},
		{"push_padding_missing", FramePushPromise, FlagPushPromisePadded, 1, nil, io.ErrUnexpectedEOF, "frame_pushpromise_pad_short"},
		{"push_promise_id_short", FramePushPromise, 0, 1, make([]byte, 3), io.ErrUnexpectedEOF, "frame_pushpromise_promiseid_short"},
		{"push_padding_overrun", FramePushPromise, FlagPushPromisePadded, 1, []byte{2, 0, 0, 0, 2, 0}, ConnectionError(ErrCodeProtocol), "frame_pushpromise_pad_too_big"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			peer := bytes.NewReader(reqH2ContractWire(tt.typ, tt.flags, tt.stream, tt.payload))
			fr := NewFramer(io.Discard, peer)
			var tokens []string
			fr.countError = func(token string) { tokens = append(tokens, token) }
			frame, err := fr.ReadFrame()
			if frame != nil || !reflect.DeepEqual(err, tt.want) { t.Fatalf("frame=%v err=%#v want=%#v", frame, err, tt.want) }
			if peer.Len() != 0 || !reflect.DeepEqual(tokens, []string{tt.token}) { t.Fatalf("unread=%d tokens=%v", peer.Len(), tokens) }
		})
	}
}

func TestReqHTTP2FrameContract_InvalidWrites(t *testing.T) {
	cases := []struct {
		name string
		write func(*Framer) error
		want error
	}{
		{"data_zero_stream", func(f *Framer) error { return f.WriteData(0, false, nil) }, errStreamID},
		{"data_reserved_stream", func(f *Framer) error { return f.WriteData(1<<31, false, nil) }, errStreamID},
		{"data_padding_too_large", func(f *Framer) error { return f.WriteDataPadded(1, false, nil, make([]byte, 256)) }, errPadLength},
		{"data_nonzero_padding", func(f *Framer) error { return f.WriteDataPadded(1, false, nil, []byte{1}) }, errPadBytes},
		{"headers_zero_stream", func(f *Framer) error { return f.WriteHeaders(HeadersFrameParam{}) }, errStreamID},
		{"headers_reserved_stream", func(f *Framer) error { return f.WriteHeaders(HeadersFrameParam{StreamID: 1<<31}) }, errStreamID},
		{"headers_reserved_dependency", func(f *Framer) error { return f.WriteHeaders(HeadersFrameParam{StreamID: 1, Priority: reqhttp2.PriorityParam{StreamDep: 1<<31}}) }, errDepStreamID},
		{"priority_zero_stream", func(f *Framer) error { return f.WritePriority(0, reqhttp2.PriorityParam{}) }, errStreamID},
		{"priority_reserved_stream", func(f *Framer) error { return f.WritePriority(1<<31, reqhttp2.PriorityParam{}) }, errStreamID},
		{"priority_reserved_dependency", func(f *Framer) error { return f.WritePriority(1, reqhttp2.PriorityParam{StreamDep: 1<<31}) }, errDepStreamID},
		{"rst_zero_stream", func(f *Framer) error { return f.WriteRSTStream(0, ErrCodeCancel) }, errStreamID},
		{"rst_reserved_stream", func(f *Framer) error { return f.WriteRSTStream(1<<31, ErrCodeCancel) }, errStreamID},
		{"continuation_zero_stream", func(f *Framer) error { return f.WriteContinuation(0, true, nil) }, errStreamID},
		{"continuation_reserved_stream", func(f *Framer) error { return f.WriteContinuation(1<<31, true, nil) }, errStreamID},
		{"push_zero_stream", func(f *Framer) error { return f.WritePushPromise(PushPromiseParam{PromiseID: 2}) }, errStreamID},
		{"push_reserved_stream", func(f *Framer) error { return f.WritePushPromise(PushPromiseParam{StreamID: 1<<31, PromiseID: 2}) }, errStreamID},
		{"push_zero_promise", func(f *Framer) error { return f.WritePushPromise(PushPromiseParam{StreamID: 1}) }, errStreamID},
		{"push_reserved_promise", func(f *Framer) error { return f.WritePushPromise(PushPromiseParam{StreamID: 1, PromiseID: 1<<31}) }, errStreamID},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := tt.write(NewFramer(&out, bytes.NewReader(nil))); !errors.Is(err, tt.want) { t.Fatalf("err=%v want=%v", err, tt.want) }
			if out.Len() != 0 { t.Fatalf("invalid write emitted %x", out.Bytes()) }
		})
	}
	t.Run("window_zero_increment", func(t *testing.T) {
		var out bytes.Buffer
		if err := NewFramer(&out, bytes.NewReader(nil)).WriteWindowUpdate(0, 0); err == nil || err.Error() != "illegal window increment value" || out.Len() != 0 { t.Fatalf("err=%v wire=%x", err, out.Bytes()) }
	})
	t.Run("window_reserved_increment", func(t *testing.T) {
		var out bytes.Buffer
		if err := NewFramer(&out, bytes.NewReader(nil)).WriteWindowUpdate(1, 1<<31); err == nil || err.Error() != "illegal window increment value" || out.Len() != 0 { t.Fatalf("err=%v wire=%x", err, out.Bytes()) }
	})
}

func TestReqHTTP2FrameContract_FrameOrder(t *testing.T) {
	cases := []struct {
		name string
		frames [][]byte
		good int
		detail string
	}{
		{"complete_then_data", [][]byte{reqH2ContractWire(FrameHeaders, FlagHeadersEndHeaders, 1, nil), reqH2ContractWire(FrameData, FlagDataEndStream, 1, nil)}, 2, ""},
		{"continuation_then_next_stream", [][]byte{reqH2ContractWire(FrameHeaders, 0, 1, nil), reqH2ContractWire(FrameContinuation, 0, 1, []byte("a")), reqH2ContractWire(FrameContinuation, FlagContinuationEndHeaders, 1, []byte("b")), reqH2ContractWire(FrameHeaders, FlagHeadersEndHeaders, 3, nil)}, 4, ""},
		{"interleaved_data", [][]byte{reqH2ContractWire(FrameHeaders, 0, 1, nil), reqH2ContractWire(FrameData, 0, 1, nil)}, 1, "got DATA for stream 1; expected CONTINUATION following HEADERS for stream 1"},
		{"interleaved_headers", [][]byte{reqH2ContractWire(FrameHeaders, 0, 1, nil), reqH2ContractWire(FrameHeaders, FlagHeadersEndHeaders, 3, nil)}, 1, "got HEADERS for stream 3; expected CONTINUATION following HEADERS for stream 1"},
		{"wrong_continuation_stream", [][]byte{reqH2ContractWire(FrameHeaders, 0, 1, nil), reqH2ContractWire(FrameContinuation, FlagContinuationEndHeaders, 3, nil)}, 1, "got CONTINUATION for stream 3; expected stream 1"},
		{"unexpected_continuation", [][]byte{reqH2ContractWire(FrameContinuation, FlagContinuationEndHeaders, 1, nil)}, 0, "unexpected CONTINUATION for stream 1"},
		{"continuation_after_end", [][]byte{reqH2ContractWire(FrameHeaders, 0, 1, nil), reqH2ContractWire(FrameContinuation, FlagContinuationEndHeaders, 1, nil), reqH2ContractWire(FrameContinuation, 0, 1, nil)}, 2, "unexpected CONTINUATION for stream 1"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			peer := bytes.NewReader(bytes.Join(tt.frames, nil))
			fr := NewFramer(io.Discard, peer)
			for i := 0; i < tt.good; i++ { if _, err := fr.ReadFrame(); err != nil { t.Fatalf("frame %d: %v", i, err) } }
			frame, err := fr.ReadFrame()
			if tt.detail == "" {
				if frame != nil || err != io.EOF { t.Fatalf("after complete sequence: frame=%v err=%v", frame, err) }
			} else if frame != nil || err != ConnectionError(ErrCodeProtocol) || fr.ErrorDetail() == nil || fr.ErrorDetail().Error() != tt.detail { t.Fatalf("frame=%v err=%v detail=%v", frame, err, fr.ErrorDetail()) }
			if peer.Len() != 0 { t.Fatalf("unread peer bytes=%d", peer.Len()) }
		})
	}
}

func TestReqHTTP2FrameContract_SizeAndTruncation(t *testing.T) {
	t.Run("exact_read_limit", func(t *testing.T) {
		peer := bytes.NewReader(reqH2ContractWire(FrameData, FlagDataEndStream, 1, []byte("ABC")))
		fr := NewFramer(io.Discard, peer)
		fr.SetMaxReadFrameSize(3)
		frame, err := fr.ReadFrame()
		if err != nil || frame == nil { t.Fatalf("frame=%v err=%v", frame, err) }
		if string(frame.(*DataFrame).Data()) != "ABC" || peer.Len() != 0 { t.Fatal(frame) }
	})
	t.Run("over_read_limit_preserves_payload", func(t *testing.T) {
		peer := bytes.NewReader(reqH2ContractWire(FrameData, 0, 1, []byte("ABC")))
		fr := NewFramer(io.Discard, peer)
		fr.SetMaxReadFrameSize(2)
		frame, err := fr.ReadFrame()
		if frame != nil || !errors.Is(err, errFrameTooLarge) || peer.Len() != 3 { t.Fatalf("frame=%v err=%v unread=%d", frame, err, peer.Len()) }
	})
	t.Run("short_header", func(t *testing.T) {
		wire := reqH2ContractWire(FrameData, 0, 1, nil)
		peer := bytes.NewReader(wire[:8])
		frame, err := NewFramer(io.Discard, peer).ReadFrame()
		if frame != nil || err != io.ErrUnexpectedEOF || peer.Len() != 0 { t.Fatalf("frame=%v err=%v unread=%d", frame, err, peer.Len()) }
	})
	t.Run("short_payload", func(t *testing.T) {
		wire := reqH2ContractWire(FrameData, 0, 1, []byte("ABC"))
		peer := bytes.NewReader(wire[:len(wire)-1])
		frame, err := NewFramer(io.Discard, peer).ReadFrame()
		if frame != nil || err != io.ErrUnexpectedEOF || peer.Len() != 0 { t.Fatalf("frame=%v err=%v unread=%d", frame, err, peer.Len()) }
	})
	t.Run("empty_peer", func(t *testing.T) {
		frame, err := NewFramer(io.Discard, bytes.NewReader(nil)).ReadFrame()
		if frame != nil || err != io.EOF { t.Fatalf("frame=%v err=%v", frame, err) }
	})
	t.Run("maximum_wire_length", func(t *testing.T) {
		payload := make([]byte, (1<<24)-1)
		payload[0], payload[len(payload)-1] = 'A', 'Z'
		var wire bytes.Buffer
		if err := NewFramer(&wire, bytes.NewReader(nil)).WriteData(1, true, payload); err != nil { t.Fatal(err) }
		if wire.Len() != len(payload)+9 || !bytes.Equal(wire.Bytes()[:9], []byte{0xff, 0xff, 0xff, 0, 1, 0, 0, 0, 1}) { t.Fatal("maximum DATA has wrong wire header/length") }
		frame, err := NewFramer(io.Discard, &wire).ReadFrame()
		if err != nil || frame == nil { t.Fatalf("frame=%v err=%v", frame, err) }
		if !bytes.Equal(frame.(*DataFrame).Data(), payload) || wire.Len() != 0 { t.Fatal("maximum DATA changed payload") }
	})
	t.Run("length_over_24_bits", func(t *testing.T) {
		var wire bytes.Buffer
		err := NewFramer(&wire, bytes.NewReader(nil)).WriteData(1, true, make([]byte, 1<<24))
		if !errors.Is(err, errFrameTooLarge) || wire.Len() != 0 { t.Fatalf("err=%v wire length=%d", err, wire.Len()) }
	})
}

func TestReqHTTP2FrameContract_UnknownAndReservedBits(t *testing.T) {
	for _, typ := range []FrameType{0x0a, 0x0f, 0x20, 0xff} {
		name := typ.String()
		t.Run(name, func(t *testing.T) {
			wire := reqH2ContractWire(typ, 0xff, 0x80000003, []byte("extension"))
			frame := reqH2ContractRead(t, wire)
			unknown, ok := frame.(*UnknownFrame)
			if !ok { t.Fatalf("got %T", frame) }
			if unknown.StreamID != 3 || unknown.Flags != 0xff || string(unknown.Payload()) != "extension" { t.Fatal(unknown) }
		})
	}
	t.Run("reserved_window_increment", func(t *testing.T) {
		frame := reqH2ContractRead(t, reqH2ContractWire(FrameWindowUpdate, 0, 1, []byte{0x80, 0, 0, 1}))
		if frame.(*WindowUpdateFrame).Increment != 1 { t.Fatal(frame) }
	})
	t.Run("reserved_promise_id", func(t *testing.T) {
		frame := reqH2ContractRead(t, reqH2ContractWire(FramePushPromise, FlagPushPromiseEndHeaders, 1, []byte{0x80, 0, 0, 2, 'x'}))
		promise := frame.(*PushPromiseFrame)
		if promise.PromiseID != 2 || string(promise.HeaderBlockFragment()) != "x" { t.Fatal(promise) }
	})
}

func TestReqHTTP2FrameContract_SettingsDuplicates(t *testing.T) {
	cases := []struct { name string; ids []reqhttp2.SettingID; duplicates bool }{
		{"empty", nil, false},
		{"small_unique", []reqhttp2.SettingID{1, 2, 3}, false},
		{"small_duplicate", []reqhttp2.SettingID{1, 2, 3, 2}, true},
		{"large_unique", []reqhttp2.SettingID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, false},
		{"large_duplicate", []reqhttp2.SettingID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 11}, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var payload bytes.Buffer
			var want []reqhttp2.Setting
			for i, id := range tt.ids {
				setting := reqhttp2.Setting{ID: id, Val: uint32(i)}
				want = append(want, setting)
				var encoded [6]byte
				binary.BigEndian.PutUint16(encoded[:2], uint16(id))
				binary.BigEndian.PutUint32(encoded[2:], setting.Val)
				payload.Write(encoded[:])
			}
			s := reqH2ContractRead(t, reqH2ContractWire(FrameSettings, 0, 0, payload.Bytes())).(*SettingsFrame)
			var got []reqhttp2.Setting
			if err := s.ForeachSetting(func(v reqhttp2.Setting) error { got = append(got, v); return nil }); err != nil { t.Fatal(err) }
			if !reflect.DeepEqual(got, want) || s.HasDuplicates() != tt.duplicates { t.Fatalf("settings=%v want=%v duplicates=%v", got, want, s.HasDuplicates()) }
			if len(want) != 0 {
				stop := errors.New("owned settings consumer failure")
				calls := 0
				err := s.ForeachSetting(func(reqhttp2.Setting) error { calls++; return stop })
				if err != stop || calls != 1 { t.Fatalf("callback error=%v calls=%d", err, calls) }
			}
		})
	}
}

func TestReqHTTP2FrameContract_FrameLifetime(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		name := "fresh_frames"
		if reuse { name = "reuse_idempotent" }
		t.Run(name, func(t *testing.T) {
			wire := append(reqH2ContractWire(FrameData, 0, 1, []byte("ABC")), reqH2ContractWire(FrameData, FlagDataEndStream, 3, []byte("XYZ"))...)
			fr := NewFramer(io.Discard, bytes.NewReader(wire))
			if reuse { fr.SetReuseFrames() }
			first, err := fr.ReadFrame()
			if err != nil { t.Fatal(err) }
			if string(first.(*DataFrame).Data()) != "ABC" { t.Fatal(first) }
			if reuse { fr.SetReuseFrames() }
			second, err := fr.ReadFrame()
			if err != nil { t.Fatal(err) }
			d := second.(*DataFrame)
			if (first == second) != reuse || string(d.Data()) != "XYZ" || d.StreamID != 3 || !d.StreamEnded() { t.Fatalf("first=%p second=%p second=%v", first, second, d) }
			// Without reuse, an old Frame stays a distinct invalid object. Its
			// payload accessor must reject use after the next ReadFrame.
			if !reuse {
				func() {
					defer func() { if recover() == nil { t.Error("stale DATA accessor did not panic") } }()
					first.(*DataFrame).Data()
				}()
			}
		})
	}
}

func reqH2ContractBlock(t *testing.T, fields []hpack.HeaderField) []byte {
	t.Helper()
	var out bytes.Buffer
	encoder := hpack.NewEncoder(&out)
	for _, field := range fields { if err := encoder.WriteField(field); err != nil { t.Fatal(err) } }
	return out.Bytes()
}

func TestReqHTTP2FrameContract_MetaHeaders(t *testing.T) {
	cases := []struct {
		name string
		fields []hpack.HeaderField
		detail string
	}{
		{"response", []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "content-type", Value: "text/plain"}}, ""},
		{"trailers", []hpack.HeaderField{{Name: "x-trailer", Value: "done"}}, ""},
		{"request_pseudos", []hpack.HeaderField{{Name: ":method", Value: "GET"}, {Name: ":path", Value: "/"}}, ""},
		{"empty_headers", nil, ""},
		{"pseudo_after_regular", []hpack.HeaderField{{Name: "x-first", Value: "ok"}, {Name: ":status", Value: "200"}}, "pseudo header field after regular"},
		{"mixed_pseudos", []hpack.HeaderField{{Name: ":method", Value: "GET"}, {Name: ":status", Value: "200"}}, "mix of request and response pseudo headers"},
		{"duplicate_pseudos", []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: ":status", Value: "201"}}, "duplicate pseudo-header \":status\""},
		{"uppercase_name", []hpack.HeaderField{{Name: "CapitalBad", Value: "x"}}, "invalid header field name \"CapitalBad\""},
		{"invalid_value", []hpack.HeaderField{{Name: "key", Value: "secret\x00"}}, "invalid header field value for \"key\""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			block := reqH2ContractBlock(t, tt.fields)
			peer := bytes.NewReader(reqH2ContractWire(FrameHeaders, FlagHeadersEndHeaders | FlagHeadersEndStream, 3, block))
			fr := NewFramer(io.Discard, peer)
			fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
			frame, err := fr.ReadFrame()
			if tt.detail != "" {
				var se StreamError
				if frame != nil || !errors.As(err, &se) || se.StreamID != 3 || se.Code != ErrCodeProtocol || se.Cause == nil || se.Cause.Error() != tt.detail || fr.ErrorDetail() == nil || fr.ErrorDetail().Error() != tt.detail { t.Fatalf("frame=%v err=%v detail=%v", frame, err, fr.ErrorDetail()) }
			} else {
				if err != nil || frame == nil { t.Fatalf("frame=%v err=%v", frame, err) }
				meta := frame.(*MetaHeadersFrame)
				if !reflect.DeepEqual(meta.Fields, tt.fields) || meta.Truncated || !meta.StreamEnded() { t.Fatal(meta) }
				if tt.name == "response" {
					if meta.PseudoValue("status") != "200" || meta.PseudoValue("method") != "" || !reflect.DeepEqual(meta.PseudoFields(), tt.fields[:1]) || !reflect.DeepEqual(meta.RegularFields(), tt.fields[1:]) { t.Fatal("response metadata accessors lost fields") }
				} else if tt.name == "request_pseudos" {
					if !reflect.DeepEqual(meta.PseudoFields(), tt.fields) || meta.RegularFields() != nil || meta.PseudoValue("method") != "GET" || meta.PseudoValue("path") != "/" || meta.PseudoValue("status") != "" { t.Fatal("request pseudo accessors lost or misclassified fields") }
				} else if len(meta.PseudoFields()) != 0 || !reflect.DeepEqual(meta.RegularFields(), tt.fields) { t.Fatal("regular fields were classified as pseudo headers") }
			}
			if peer.Len() != 0 { t.Fatalf("unread=%d", peer.Len()) }
		})
	}
	t.Run("fragmented_hpack_literal", func(t *testing.T) {
		fields := []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "x-fragment", Value: "a literal value"}}
		block := reqH2ContractBlock(t, fields)
		wire := reqH2ContractWire(FrameHeaders, FlagHeadersEndStream, 1, block[:2])
		wire = append(wire, reqH2ContractWire(FrameContinuation, 0, 1, block[2:4])...)
		wire = append(wire, reqH2ContractWire(FrameContinuation, FlagContinuationEndHeaders, 1, block[4:])...)
		peer := bytes.NewReader(wire)
		fr := NewFramer(io.Discard, peer)
		fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
		frame, err := fr.ReadFrame()
		if err != nil || frame == nil { t.Fatalf("frame=%v err=%v", frame, err) }
		meta := frame.(*MetaHeadersFrame)
		if !reflect.DeepEqual(meta.Fields, fields) || !meta.StreamEnded() || meta.Length != 2 || meta.Flags != FlagHeadersEndStream || peer.Len() != 0 { t.Fatal(meta) }
	})
	for _, tt := range []struct { name string; block []byte }{{"invalid_hpack_index", []byte{0x80}}, {"truncated_hpack_literal", []byte{0x40}}} {
		t.Run(tt.name, func(t *testing.T) {
			peer := bytes.NewReader(reqH2ContractWire(FrameHeaders, FlagHeadersEndHeaders, 1, tt.block))
			fr := NewFramer(io.Discard, peer)
			fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
			_, err := fr.ReadFrame()
			if err != ConnectionError(ErrCodeCompression) || peer.Len() != 0 { t.Fatalf("err=%v unread=%d", err, peer.Len()) }
		})
	}
}

func TestReqHTTP2FrameContract_DecoderAfterStreamError(t *testing.T) {
	// One encoder and one decoder deliberately span both streams. A stream
	// header error must not discard HPACK dynamic entries for the connection.
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	for _, field := range []hpack.HeaderField{{Name: "x-reused", Value: "value"}, {Name: ":status", Value: "200"}} { if err := encoder.WriteField(field); err != nil { t.Fatal(err) } }
	first := append([]byte(nil), block.Bytes()...)
	block.Reset()
	fields := []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "x-reused", Value: "value"}}
	for _, field := range fields { if err := encoder.WriteField(field); err != nil { t.Fatal(err) } }
	// Both fields are indexed. This makes a lost connection decoder state a
	// real COMPRESSION_ERROR on the second frame rather than a silent pass.
	if block.Len() != 2 || block.Bytes()[0]&0x80 == 0 || block.Bytes()[1]&0x80 == 0 { t.Fatalf("second block is not indexed: %x", block.Bytes()) }
	wire := append(reqH2ContractWire(FrameHeaders, FlagHeadersEndHeaders, 1, first), reqH2ContractWire(FrameHeaders, FlagHeadersEndHeaders | FlagHeadersEndStream, 3, block.Bytes())...)
	peer := bytes.NewReader(wire)
	fr := NewFramer(io.Discard, peer)
	fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	_, err := fr.ReadFrame()
	var se StreamError
	if !errors.As(err, &se) || se.StreamID != 1 || se.Code != ErrCodeProtocol || fr.ErrorDetail() == nil || fr.ErrorDetail().Error() != "pseudo header field after regular" { t.Fatalf("first err=%v detail=%v", err, fr.ErrorDetail()) }
	frame, err := fr.ReadFrame()
	if err != nil || frame == nil { t.Fatalf("second frame=%v err=%v", frame, err) }
	if !reflect.DeepEqual(frame.(*MetaHeadersFrame).Fields, fields) || fr.ErrorDetail() != nil || peer.Len() != 0 { t.Fatalf("second=%v detail=%v unread=%d", frame, fr.ErrorDetail(), peer.Len()) }
}

// The peer records the complete write offered to it before accepting either
// the whole frame or a prefix. This checks both wire bytes and I/O semantics.
type reqH2ContractWriter struct {
	limit int
	err error
	calls int
	offered []byte
	accepted []byte
}

func (w *reqH2ContractWriter) Write(p []byte) (int, error) {
	w.calls++
	w.offered = append(w.offered, p...)
	n := len(p)
	if w.limit >= 0 && n > w.limit { n = w.limit }
	w.accepted = append(w.accepted, p[:n]...)
	return n, w.err
}

func TestReqHTTP2FrameContract_WriteFailures(t *testing.T) {
	peerError := errors.New("owned frame peer write failure")
	wantWire := reqH2ContractWire(FrameData, FlagDataEndStream, 1, []byte("payload"))
	for _, tt := range []struct { name string; limit int; peerErr error; wantErr error }{
		{"complete_single_write", -1, nil, nil},
		{"short_write", 10, nil, io.ErrShortWrite},
		{"peer_error", 10, peerError, peerError},
		{"full_write_with_error", -1, peerError, peerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			peer := &reqH2ContractWriter{limit: tt.limit, err: tt.peerErr}
			fr := NewFramer(peer, bytes.NewReader(nil))
			err := fr.WriteData(1, true, []byte("payload"))
			accepted := len(wantWire)
			if tt.limit >= 0 { accepted = tt.limit }
			if err != tt.wantErr || peer.calls != 1 || !bytes.Equal(peer.offered, wantWire) || !bytes.Equal(peer.accepted, wantWire[:accepted]) { t.Fatalf("err=%v calls=%d offered=%x accepted=%x", err, peer.calls, peer.offered, peer.accepted) }
		})
	}
}

func TestReqHTTP2FrameContract_IllegalModes(t *testing.T) {
	t.Run("nonzero_padding_allowed", func(t *testing.T) {
		var out bytes.Buffer
		fr := NewFramer(&out, bytes.NewReader(nil))
		fr.AllowIllegalWrites = true
		if err := fr.WriteDataPadded(1, true, []byte("ABC"), []byte{9}); err != nil { t.Fatal(err) }
		want := reqH2ContractWire(FrameData, FlagDataPadded | FlagDataEndStream, 1, []byte{1, 'A', 'B', 'C', 9})
		if !bytes.Equal(out.Bytes(), want) { t.Fatalf("wire=%x want=%x", out.Bytes(), want) }
		frame := reqH2ContractRead(t, out.Bytes()).(*DataFrame)
		if string(frame.Data()) != "ABC" || !frame.StreamEnded() { t.Fatal(frame) }
	})
	t.Run("invalid_stream_allowed_on_write_rejected_on_read", func(t *testing.T) {
		var out bytes.Buffer
		fr := NewFramer(&out, bytes.NewReader(nil))
		fr.AllowIllegalWrites = true
		if err := fr.WriteData(0, false, []byte("ABC")); err != nil { t.Fatal(err) }
		if !bytes.Equal(out.Bytes(), reqH2ContractWire(FrameData, 0, 0, []byte("ABC"))) { t.Fatalf("wire=%x", out.Bytes()) }
		reader := NewFramer(io.Discard, &out)
		frame, err := reader.ReadFrame()
		if frame != nil || err != ConnectionError(ErrCodeProtocol) || out.Len() != 0 { t.Fatalf("frame=%v err=%v unread=%d", frame, err, out.Len()) }
	})
	t.Run("padding_bound_still_enforced", func(t *testing.T) {
		var out bytes.Buffer
		fr := NewFramer(&out, bytes.NewReader(nil))
		fr.AllowIllegalWrites = true
		if err := fr.WriteDataPadded(1, false, nil, make([]byte, 256)); err != errPadLength || out.Len() != 0 { t.Fatalf("err=%v wire=%x", err, out.Bytes()) }
	})
	t.Run("out_of_order_continuation_allowed", func(t *testing.T) {
		wire := reqH2ContractWire(FrameContinuation, FlagContinuationEndHeaders, 3, []byte("part"))
		peer := bytes.NewReader(wire)
		fr := NewFramer(io.Discard, peer)
		fr.AllowIllegalReads = true
		frame, err := fr.ReadFrame()
		if err != nil || frame == nil { t.Fatalf("frame=%v err=%v", frame, err) }
		c := frame.(*ContinuationFrame)
		if c.StreamID != 3 || !c.HeadersEnded() || string(c.HeaderBlockFragment()) != "part" || peer.Len() != 0 { t.Fatal(c) }
	})
	t.Run("illegal_reads_with_metadata_rejected", func(t *testing.T) {
		peer := bytes.NewReader(reqH2ContractWire(FrameHeaders, FlagHeadersEndHeaders, 1, []byte{0x88}))
		fr := NewFramer(io.Discard, peer)
		fr.AllowIllegalReads = true
		fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
		frame, err := fr.ReadFrame()
		if frame != nil || err == nil || err.Error() != "illegal use of AllowIllegalReads with ReadMetaHeaders" || peer.Len() != 0 { t.Fatalf("frame=%v err=%v unread=%d", frame, err, peer.Len()) }
	})
}

func TestReqHTTP2FrameContract_RawExtensionWrite(t *testing.T) {
	var out bytes.Buffer
	if err := NewFramer(&out, bytes.NewReader(nil)).WriteRawFrame(0xff, 0xa5, 3, []byte("extension")); err != nil { t.Fatal(err) }
	want := reqH2ContractWire(0xff, 0xa5, 3, []byte("extension"))
	if !bytes.Equal(out.Bytes(), want) { t.Fatalf("wire=%x want=%x", out.Bytes(), want) }
	frame := reqH2ContractRead(t, out.Bytes()).(*UnknownFrame)
	if frame.Type != 0xff || frame.Flags != 0xa5 || frame.StreamID != 3 || string(frame.Payload()) != "extension" { t.Fatal(frame) }
}

func TestReqHTTP2FrameContract_MetadataReadErrors(t *testing.T) {
	for _, tt := range []struct { name string; suffix []byte; want error; unread int }{
		{"missing_continuation_header", nil, io.EOF, 0},
		{"short_continuation_header", []byte{0, 0, 1, byte(FrameContinuation)}, io.ErrUnexpectedEOF, 0},
		{"short_continuation_payload", reqH2ContractWire(FrameContinuation, FlagContinuationEndHeaders, 1, []byte{0x88})[:9], io.EOF, 0},
		{"wrong_frame_before_following_frame", append(reqH2ContractWire(FrameData, 0, 1, nil), reqH2ContractWire(FramePing, 0, 0, make([]byte, 8))...), ConnectionError(ErrCodeProtocol), 17},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wire := append(reqH2ContractWire(FrameHeaders, 0, 1, nil), tt.suffix...)
			peer := bytes.NewReader(wire)
			fr := NewFramer(io.Discard, peer)
			fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
			frame, err := fr.ReadFrame()
			if frame != nil || err != tt.want || peer.Len() != tt.unread { t.Fatalf("frame=%v err=%v want=%v unread=%d", frame, err, tt.want, peer.Len()) }
			if tt.unread != 0 {
				remaining, err := io.ReadAll(peer)
				want := reqH2ContractWire(FramePing, 0, 0, make([]byte, 8))
				if err != nil || !bytes.Equal(remaining, want) { t.Fatalf("retained peer bytes=%x want=%x err=%v", remaining, want, err) }
			}
		})
	}
	// Once a regular header has invalidated the block, a further continuation
	// closes the connection before decoding that fragment. The following PING
	// remains unread, proving that the rejection is bounded to the header block.
	t.Run("invalid_header_continuation_is_bounded", func(t *testing.T) {
		block := reqH2ContractBlock(t, []hpack.HeaderField{{Name: "CapitalBad", Value: "x"}})
		wire := reqH2ContractWire(FrameHeaders, 0, 1, block)
		wire = append(wire, reqH2ContractWire(FrameContinuation, FlagContinuationEndHeaders, 1, []byte{0x88})...)
		wire = append(wire, reqH2ContractWire(FramePing, 0, 0, make([]byte, 8))...)
		peer := bytes.NewReader(wire)
		fr := NewFramer(io.Discard, peer)
		fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
		frame, err := fr.ReadFrame()
		meta, ok := frame.(*MetaHeadersFrame)
		if !ok || meta.StreamID != 1 || len(meta.Fields) != 0 || err != ConnectionError(ErrCodeProtocol) || peer.Len() != 17 { t.Fatalf("frame=%v err=%v unread=%d", frame, err, peer.Len()) }
	})
}

func TestReqHTTP2FrameContract_HeaderOnly(t *testing.T) {
	// The package-level ReadFrameHeader reads exactly nine bytes. Deliberately
	// retain only a small sentinel after each header even when its advertised
	// payload length is large; this API must not allocate or consume the body.
	for _, tt := range []struct { name string; wire [9]byte; want FrameHeader }{
		{"zero", [9]byte{}, FrameHeader{}},
		{"mixed_bytes", [9]byte{1, 2, 3, 4, 5, 6, 7, 8, 9}, FrameHeader{Length: 66051, Type: 4, Flags: 5, StreamID: 0x06070809}},
		{"max_reserved_stream", [9]byte{255, 255, 255, 255, 255, 255, 255, 255, 255}, FrameHeader{Length: 16777215, Type: 255, Flags: 255, StreamID: 2147483647}},
		{"max_clear_stream", [9]byte{255, 255, 255, 255, 255, 127, 255, 255, 255}, FrameHeader{Length: 16777215, Type: 255, Flags: 255, StreamID: 2147483647}},
		{"stream_0", [9]byte{0, 0, 0, 255, 1, 0, 0, 0, 0}, FrameHeader{Type: 255, Flags: 1}},
		{"stream_1", [9]byte{0, 0, 0, 255, 1, 0, 0, 0, 1}, FrameHeader{Type: 255, Flags: 1, StreamID: 1}},
		{"stream_255", [9]byte{0, 0, 0, 255, 1, 0, 0, 0, 255}, FrameHeader{Type: 255, Flags: 1, StreamID: 255}},
		{"stream_256", [9]byte{0, 0, 0, 255, 1, 0, 0, 1, 0}, FrameHeader{Type: 255, Flags: 1, StreamID: 256}},
		{"stream_65535", [9]byte{0, 0, 0, 255, 1, 0, 0, 255, 255}, FrameHeader{Type: 255, Flags: 1, StreamID: 65535}},
		{"stream_65536", [9]byte{0, 0, 0, 255, 1, 0, 1, 0, 0}, FrameHeader{Type: 255, Flags: 1, StreamID: 65536}},
		{"length_0", [9]byte{0, 0, 0, 1, 255, 0, 0, 0, 1}, FrameHeader{Type: 1, Flags: 255, StreamID: 1}},
		{"length_255", [9]byte{0, 0, 255, 1, 255, 0, 0, 0, 1}, FrameHeader{Length: 255, Type: 1, Flags: 255, StreamID: 1}},
		{"length_256", [9]byte{0, 1, 0, 1, 255, 0, 0, 0, 1}, FrameHeader{Length: 256, Type: 1, Flags: 255, StreamID: 1}},
		{"length_65535", [9]byte{0, 255, 255, 1, 255, 0, 0, 0, 1}, FrameHeader{Length: 65535, Type: 1, Flags: 255, StreamID: 1}},
		{"length_65536", [9]byte{1, 0, 0, 1, 255, 0, 0, 0, 1}, FrameHeader{Length: 65536, Type: 1, Flags: 255, StreamID: 1}},
		{"length_maximum", [9]byte{255, 255, 255, 1, 255, 0, 0, 0, 1}, FrameHeader{Length: 16777215, Type: 1, Flags: 255, StreamID: 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			peer := bytes.NewReader(append(tt.wire[:], []byte("body")...))
			got, err := ReadFrameHeader(peer)
			want := tt.want
			want.valid = true
			if err != nil || !reflect.DeepEqual(got, want) || peer.Len() != 4 { t.Fatalf("header=%+v want=%+v err=%v unread=%d", got, want, err, peer.Len()) }
			retained, err := io.ReadAll(peer)
			if err != nil || string(retained) != "body" { t.Fatalf("retained=%q err=%v", retained, err) }
		})
	}
	for _, tt := range []struct { name string; wire []byte; want error }{
		{"empty_peer", nil, io.EOF},
		{"short_header", []byte{0, 0, 0, 1, 0, 0, 0, 0}, io.ErrUnexpectedEOF},
	} {
		t.Run(tt.name, func(t *testing.T) {
			peer := bytes.NewReader(tt.wire)
			got, err := ReadFrameHeader(peer)
			if got != (FrameHeader{}) || err != tt.want || peer.Len() != 0 { t.Fatalf("header=%+v err=%v unread=%d", got, err, peer.Len()) }
		})
	}
}

func TestReqHTTP2FrameContract_RepeatedFrameLifetime(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		name := "fresh_frames"
		if reuse { name = "reuse_and_zero_payload" }
		t.Run(name, func(t *testing.T) {
			var wire bytes.Buffer
			for _, data := range []string{"ABC", "XYZ", "", "HHH"} {
				for i := 0; i < 10; i++ { wire.Write(reqH2ContractWire(FrameData, FlagDataEndStream, 0x01020304, []byte(data))) }
			}
			fr := NewFramer(io.Discard, &wire)
			if reuse { fr.SetReuseFrames() }
			var prior []*DataFrame
			for _, data := range []string{"ABC", "XYZ", "", "HHH"} {
				for i := 0; i < 10; i++ {
					if reuse { fr.SetReuseFrames() }
					frame, err := fr.ReadFrame()
					if err != nil || frame == nil { t.Fatalf("data=%q iteration=%d frame=%v err=%v", data, i, frame, err) }
					d := frame.(*DataFrame)
					if string(d.Data()) != data || d.Length != uint32(len(data)) || d.StreamID != 0x01020304 || d.Flags != FlagDataEndStream || !d.StreamEnded() { t.Fatalf("data=%q iteration=%d got=%v", data, i, d) }
					for _, old := range prior {
						if (old == d) != reuse { t.Fatalf("reuse=%v old=%p current=%p", reuse, old, d) }
					}
					prior = append(prior, d)
				}
			}
			if wire.Len() != 0 { t.Fatalf("unread=%d", wire.Len()) }
		})
	}
}

func TestReqHTTP2FrameContract_MetaStringLength(t *testing.T) {
	fields := []hpack.HeaderField{{Name: ":method", Value: "GET"}, {Name: ":path", Value: "/"}, {Name: "foo", Value: strings.Repeat("a", 1024)}}
	block := reqH2ContractBlock(t, fields)
	for _, tt := range []struct { name string; limit uint32; want error }{
		{"big_string_okay", 0, nil},
		{"big_string_error", 512, ConnectionError(ErrCodeCompression)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wire := reqH2ContractWire(FrameHeaders, FlagHeadersEndStream, 1, block[:2])
			wire = append(wire, reqH2ContractWire(FrameContinuation, FlagContinuationEndHeaders, 1, block[2:])...)
			peer := bytes.NewReader(wire)
			fr := NewFramer(io.Discard, peer)
			fr.MaxHeaderListSize = tt.limit
			fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
			frame, err := fr.ReadFrame()
			meta, ok := frame.(*MetaHeadersFrame)
			wantFields := fields
			if tt.want != nil { wantFields = fields[:2] }
			if !ok || err != tt.want || !reflect.DeepEqual(meta.Fields, wantFields) || meta.Truncated || !meta.StreamEnded() || peer.Len() != 0 { t.Fatalf("frame=%v err=%v want=%v unread=%d", frame, err, tt.want, peer.Len()) }
		})
	}
}
