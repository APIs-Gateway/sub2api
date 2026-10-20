// Copyright (c) 2016 the quic-go authors & Google, Inc.
// Derived from quic-go v0.57.1, revision 4b0ab7e48554e4440448c9252a92d7c065d88331.
// MIT license: see CONTRACT-TESTS-LICENSE.
// Source: http3/frames_test.go; private parser contracts, no QUIC peer execution.
package http3

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
	"github.com/quic-go/quic-go/quicvarint"
	"github.com/quic-go/quic-go/testutils/events"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func h3ContractFrameParserEOF(t *testing.T, data []byte) {
	t.Helper()
	for i := range data {
		b := make([]byte, i)
		copy(b, data[:i])
		fp := frameParser{r: bytes.NewReader(b)}
		_, err := fp.ParseNext(nil)
		require.Error(t, err)
		require.ErrorIs(t, err, io.EOF)
	}
}

func TestReqHTTP3Contract_ParserReservedFrameType(t *testing.T) {
	for _, ft := range []uint64{0x2, 0x6, 0x8, 0x9} {
		t.Run(fmt.Sprintf("reserved_0x%x", ft), func(t *testing.T) {
			var eventRecorder events.Recorder
			data := quicvarint.Append(nil, ft)
			data = quicvarint.Append(data, 6)
			data = append(data, []byte("foobar")...)
			r := bytes.NewReader(data)
			var closed []quic.ApplicationErrorCode
			fp := frameParser{
				streamID: 42,
				r:        r,
				closeConn: func(code quic.ApplicationErrorCode, reason string) error {
					closed = append(closed, code)
					require.Empty(t, reason)
					return nil
				},
			}
			frame, err := fp.ParseNext(&eventRecorder)
			require.Nil(t, frame)
			require.EqualError(t, err, fmt.Sprintf("http3: reserved frame type: %d", ft))
			require.Equal(t, []quic.ApplicationErrorCode{quic.ApplicationErrorCode(ErrCodeFrameUnexpected)}, closed)
			// The parser rejects at the frame header: the payload belongs to the
			// rejected stream and must not be consumed as another frame.
			require.Equal(t, 6, r.Len())
			require.Equal(t, []qlogwriter.Event{
				qlog.FrameParsed{
					StreamID: 42,
					Raw:      qlog.RawInfo{Length: len(data), PayloadLength: 6},
					Frame:    qlog.Frame{Frame: qlog.ReservedFrame{Type: ft}},
				},
			}, eventRecorder.Events(qlog.FrameParsed{}))
		})
	}
}

func TestReqHTTP3Contract_ParserUnknownFrameType(t *testing.T) {
	data := quicvarint.Append(nil, 0xdead)
	data = quicvarint.Append(data, 6)
	data = append(data, []byte("foobar")...)
	data = quicvarint.Append(data, 0xbeef)
	data = quicvarint.Append(data, 3)
	data = append(data, []byte("baz")...)
	hf := &headersFrame{Length: 3}
	data = hf.Append(data)
	data = append(data, []byte("foo")...)

	r := bytes.NewReader(data)
	fp := frameParser{r: r}
	f, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.IsType(t, &headersFrame{}, f)
	hf = f.(*headersFrame)
	require.Equal(t, uint64(3), hf.Length)
	payload := make([]byte, 3)
	_, err = io.ReadFull(r, payload)
	require.NoError(t, err)
	require.Equal(t, []byte("foo"), payload)
}

func TestReqHTTP3Contract_ParserUnsupportedFrameTypes(t *testing.T) {
	for _, tc := range []struct {
		name string
		ft   uint64
		qf   any
	}{
		{name: "CANCEL_PUSH", ft: 0x3, qf: qlog.CancelPushFrame{}},
		{name: "PUSH_PROMISE", ft: 0x5, qf: qlog.PushPromiseFrame{}},
		{name: "MAX_PUSH_ID", ft: 0xd, qf: qlog.MaxPushIDFrame{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var eventRecorder events.Recorder

			data := quicvarint.Append(nil, tc.ft)
			data = quicvarint.Append(data, 6)
			data = append(data, []byte("foobar")...)
			df := &dataFrame{Length: 3}
			data = df.Append(data)
			data = append(data, []byte("foo")...)

			r := bytes.NewReader(data)
			fp := frameParser{streamID: 42, r: r}

			f, err := fp.ParseNext(&eventRecorder)
			require.NoError(t, err)
			require.IsType(t, &dataFrame{}, f)
			df = f.(*dataFrame)
			require.Equal(t, uint64(3), df.Length)
			payload := make([]byte, 3)
			_, err = io.ReadFull(r, payload)
			require.NoError(t, err)
			require.Equal(t, []byte("foo"), payload)

			headerLen := quicvarint.Len(tc.ft) + quicvarint.Len(6)
			dfLen := len(df.Append(nil)) + int(df.Length)
			require.Equal(t,
				[]qlogwriter.Event{
					qlog.FrameParsed{
						StreamID: 42,
						Raw:      qlog.RawInfo{Length: headerLen, PayloadLength: 6},
						Frame:    qlog.Frame{Frame: tc.qf},
					},
					qlog.FrameParsed{
						StreamID: 42,
						Raw:      qlog.RawInfo{Length: dfLen, PayloadLength: 3},
						Frame:    qlog.Frame{Frame: qlog.DataFrame{}},
					},
				},
				eventRecorder.Events(qlog.FrameParsed{}),
			)
		})
	}
}

func TestReqHTTP3Contract_ParserHeadersFrame(t *testing.T) {
	data := quicvarint.Append(nil, 1) // type byte
	data = quicvarint.Append(data, 0x1337)
	fp := frameParser{r: bytes.NewReader(data)}

	// incomplete data results in an io.EOF
	h3ContractFrameParserEOF(t, data)

	// parse
	f1, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.IsType(t, &headersFrame{}, f1)
	require.Equal(t, uint64(0x1337), f1.(*headersFrame).Length)

	// write and parse
	fp = frameParser{r: bytes.NewReader(f1.(*headersFrame).Append(nil))}
	f2, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.Equal(t, f1, f2)
}

func TestReqHTTP3Contract_DataFrame(t *testing.T) {
	data := quicvarint.Append(nil, 0) // type byte
	data = quicvarint.Append(data, 0x1337)
	fp := frameParser{r: bytes.NewReader(data)}

	// incomplete data results in an io.EOF
	h3ContractFrameParserEOF(t, data)

	// parse
	f1, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.IsType(t, &dataFrame{}, f1)
	require.Equal(t, uint64(0x1337), f1.(*dataFrame).Length)

	// write and parse
	fp = frameParser{r: bytes.NewReader(f1.(*dataFrame).Append(nil))}
	f2, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.Equal(t, f1, f2)
}

func h3ContractAppendSetting(b []byte, key, value uint64) []byte {
	b = quicvarint.Append(b, key)
	b = quicvarint.Append(b, value)
	return b
}

func TestReqHTTP3Contract_ParserSettingsFrame(t *testing.T) {
	settings := h3ContractAppendSetting(nil, 13, 37)
	settings = h3ContractAppendSetting(settings, 0xdead, 0xbeef)
	data := quicvarint.Append(nil, 4) // type byte
	data = quicvarint.Append(data, uint64(len(settings)))
	data = append(data, settings...)

	// incomplete data results in an io.EOF
	h3ContractFrameParserEOF(t, data)

	fp := frameParser{r: bytes.NewReader(data)}
	frame, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.IsType(t, &settingsFrame{}, frame)
	sf := frame.(*settingsFrame)
	require.Len(t, sf.Other, 2)
	require.Equal(t, uint64(37), sf.Other[uint64(13)])
	require.Equal(t, uint64(0xbeef), sf.Other[uint64(0xdead)])

	// write and parse
	fp = frameParser{r: bytes.NewReader(sf.Append(nil))}
	f2, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.IsType(t, &settingsFrame{}, f2)
	sf2 := f2.(*settingsFrame)
	require.Len(t, sf2.Other, len(sf.Other))
	require.Equal(t, sf.Other, sf2.Other)
}

func TestReqHTTP3Contract_ParserSettingsFrameDuplicateSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		num  uint64
		val  uint64
	}{
		{
			name: "other_setting",
			num:  13,
			val:  37,
		},
		{
			name: "extended_connect",
			num:  settingExtendedConnect,
			val:  1,
		},
		{
			name: "max_field_section_size",
			num:  settingMaxFieldSectionSize,
			val:  1337,
		},
		{
			name: "datagram",
			num:  settingDatagram,
			val:  1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := h3ContractAppendSetting(nil, tc.num, tc.val)
			settings = h3ContractAppendSetting(settings, tc.num, tc.val)
			data := quicvarint.Append(nil, 4) // type byte
			data = quicvarint.Append(data, uint64(len(settings)))
			data = append(data, settings...)
			fp := frameParser{r: bytes.NewReader(data)}
			_, err := fp.ParseNext(nil)
			require.Error(t, err)
			require.EqualError(t, err, fmt.Sprintf("duplicate setting: %d", tc.num))
		})
	}
}

func TestReqHTTP3Contract_ParserSettingsFrameMaxFieldSectionSize(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		h3ContractSettingsMaxFieldSectionSize(t, false)
	})

	t.Run("with_value", func(t *testing.T) {
		h3ContractSettingsMaxFieldSectionSize(t, true)
	})
}

func h3ContractSettingsMaxFieldSectionSize(t *testing.T, present bool) {
	var settings []byte
	if present {
		settings = h3ContractAppendSetting(nil, settingMaxFieldSectionSize, 1337)
	}
	data := quicvarint.Append(nil, 4) // type byte
	data = quicvarint.Append(data, uint64(len(settings)))
	data = append(data, settings...)

	fp := frameParser{r: bytes.NewReader(data)}
	f, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.IsType(t, &settingsFrame{}, f)
	sf := f.(*settingsFrame)
	if present {
		require.EqualValues(t, 1337, sf.MaxFieldSectionSize)
	} else {
		require.EqualValues(t, -1, sf.MaxFieldSectionSize)
	}

	fp = frameParser{r: bytes.NewReader(sf.Append(nil))}
	f2, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.Equal(t, sf, f2)
}

func TestReqHTTP3Contract_ParserSettingsFrameDatagram(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		h3ContractSettingsDatagram(t, true)
	})
	t.Run("disabled", func(t *testing.T) {
		h3ContractSettingsDatagram(t, false)
	})
}

func h3ContractSettingsDatagram(t *testing.T, enabled bool) {
	var settings []byte
	switch enabled {
	case true:
		settings = h3ContractAppendSetting(nil, settingDatagram, 1)
	case false:
		settings = h3ContractAppendSetting(nil, settingDatagram, 0)
	}
	data := quicvarint.Append(nil, 4) // type byte
	data = quicvarint.Append(data, uint64(len(settings)))
	data = append(data, settings...)

	fp := frameParser{r: bytes.NewReader(data)}
	f, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.IsType(t, &settingsFrame{}, f)
	sf := f.(*settingsFrame)
	require.Equal(t, enabled, sf.Datagram)

	fp = frameParser{r: bytes.NewReader(sf.Append(nil))}
	f2, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.Equal(t, sf, f2)
}

func TestReqHTTP3Contract_ParserSettingsFrameDatagramInvalidValue(t *testing.T) {
	settings := quicvarint.Append(nil, settingDatagram)
	settings = quicvarint.Append(settings, 1337)
	data := quicvarint.Append(nil, 4) // type byte
	data = quicvarint.Append(data, uint64(len(settings)))
	data = append(data, settings...)
	fp := frameParser{r: bytes.NewReader(data)}
	_, err := fp.ParseNext(nil)
	require.EqualError(t, err, "invalid value for SETTINGS_H3_DATAGRAM: 1337")
}

func TestReqHTTP3Contract_ParserSettingsFrameExtendedConnect(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		h3ContractSettingsExtendedConnect(t, true)
	})
	t.Run("disabled", func(t *testing.T) {
		h3ContractSettingsExtendedConnect(t, false)
	})
}

func h3ContractSettingsExtendedConnect(t *testing.T, enabled bool) {
	var settings []byte
	switch enabled {
	case true:
		settings = h3ContractAppendSetting(nil, settingExtendedConnect, 1)
	case false:
		settings = h3ContractAppendSetting(nil, settingExtendedConnect, 0)
	}
	data := quicvarint.Append(nil, 4) // type byte
	data = quicvarint.Append(data, uint64(len(settings)))
	data = append(data, settings...)

	fp := frameParser{r: bytes.NewReader(data)}
	f, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.IsType(t, &settingsFrame{}, f)
	sf := f.(*settingsFrame)
	require.Equal(t, enabled, sf.ExtendedConnect)

	fp = frameParser{r: bytes.NewReader(sf.Append(nil))}
	f2, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.Equal(t, sf, f2)
}

func TestReqHTTP3Contract_ParserSettingsFrameExtendedConnectInvalidValue(t *testing.T) {
	settings := quicvarint.Append(nil, settingExtendedConnect)
	settings = quicvarint.Append(settings, 1337)
	data := quicvarint.Append(nil, 4) // type byte
	data = quicvarint.Append(data, uint64(len(settings)))
	data = append(data, settings...)
	fp := frameParser{r: bytes.NewReader(data)}
	_, err := fp.ParseNext(nil)
	require.EqualError(t, err, "invalid value for SETTINGS_ENABLE_CONNECT_PROTOCOL: 1337")
}

func TestReqHTTP3Contract_ParserGoAwayFrame(t *testing.T) {
	data := quicvarint.Append(nil, 7) // type byte
	data = quicvarint.Append(data, uint64(quicvarint.Len(100)))
	data = quicvarint.Append(data, 100)

	// incomplete data results in an io.EOF
	h3ContractFrameParserEOF(t, data)

	fp := frameParser{r: bytes.NewReader(data)}
	f, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.IsType(t, &goAwayFrame{}, f)
	require.Equal(t, quic.StreamID(100), f.(*goAwayFrame).StreamID)

	// write and parse
	fp = frameParser{r: bytes.NewReader(f.(*goAwayFrame).Append(nil))}
	f2, err := fp.ParseNext(nil)
	require.NoError(t, err)
	require.Equal(t, f, f2)
}

func TestReqHTTP3Contract_ParserHijacking(t *testing.T) {
	t.Run("hijacking", func(t *testing.T) {
		h3ContractParserHijacking(t, true)
	})
	t.Run("not_hijacking", func(t *testing.T) {
		h3ContractParserHijacking(t, false)
	})
}

func h3ContractParserHijacking(t *testing.T, hijack bool) {
	b := quicvarint.Append(nil, 1337)
	if hijack {
		b = append(b, "foobar"...)
	} else {
		// if the stream is not hijacked, this will be treated as an unknown frame
		b = quicvarint.Append(b, 11)
		b = append(b, []byte("lorem ipsum")...)
		b = (&dataFrame{Length: 6}).Append(b)
		b = append(b, []byte("foobar")...)
	}

	var called bool
	r := bytes.NewReader(b)
	fp := frameParser{
		r: r,
		unknownFrameHandler: func(ft FrameType, e error) (hijacked bool, err error) {
			called = true
			require.NoError(t, e)
			require.Equal(t, FrameType(1337), ft)
			if !hijack {
				return false, nil
			}
			b := make([]byte, 6)
			_, err = io.ReadFull(r, b)
			require.NoError(t, err)
			require.Equal(t, "foobar", string(b))
			return true, nil
		},
	}
	f, err := fp.ParseNext(nil)
	require.True(t, called)
	if hijack {
		require.ErrorIs(t, err, errHijacked)
		return
	}
	require.NoError(t, err)
	require.IsType(t, &dataFrame{}, f)
	df := f.(*dataFrame)
	require.Equal(t, uint64(6), df.Length)
	payload := make([]byte, 6)
	_, err = io.ReadFull(r, payload)
	require.NoError(t, err)
	require.Equal(t, "foobar", string(payload))
}

type h3ContractErrorReader struct{ err error }

func (e h3ContractErrorReader) Read([]byte) (int, error) { return 0, e.err }

func TestReqHTTP3Contract_ParserHijackError(t *testing.T) {
	var called bool
	fp := frameParser{
		r: h3ContractErrorReader{err: assert.AnError},
		unknownFrameHandler: func(ft FrameType, e error) (hijacked bool, err error) {
			require.EqualError(t, e, assert.AnError.Error())
			require.Zero(t, ft)
			called = true
			return true, nil
		},
	}
	_, err := fp.ParseNext(nil)
	require.ErrorIs(t, err, errHijacked)
	require.True(t, called)
}
