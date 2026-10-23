package compress

import (
	"errors"
	"io"
	"testing"
)

type deflateCloseProbe struct {
	err   error
	calls int
}

func (*deflateCloseProbe) Read([]byte) (int, error) { return 0, io.EOF }

func (p *deflateCloseProbe) Close() error {
	p.calls++
	return p.err
}

// The probes cover resource closure and error precedence deterministically.
// Actual decoder initialization and remote stream cancellation remain covered
// by TestReqHTTP3DeflateClose_PublicStreamRetirement against real QUIC peers.
func TestReqDeflateClose_ResourcesAndErrors(t *testing.T) {
	decoderErr := errors.New("owned deflate decoder close failure")
	bodyErr := errors.New("owned response body close failure")
	for _, tc := range []struct {
		name       string
		decoder    bool
		decoderErr error
		bodyErr    error
		wantErr    error
		closes     int
	}{
		{name: "unread_success", closes: 1},
		{name: "unread_body_error", bodyErr: bodyErr, wantErr: bodyErr, closes: 1},
		{name: "initialized_success", decoder: true, closes: 1},
		{name: "decoder_error", decoder: true, decoderErr: decoderErr, wantErr: decoderErr, closes: 1},
		{name: "body_error", decoder: true, bodyErr: bodyErr, wantErr: bodyErr, closes: 1},
		{name: "both_errors_decoder_first", decoder: true, decoderErr: decoderErr, bodyErr: bodyErr, wantErr: decoderErr, closes: 1},
		{name: "repeated_initialized_close", decoder: true, closes: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &deflateCloseProbe{err: tc.bodyErr}
			decoder := &deflateCloseProbe{err: tc.decoderErr}
			reader := NewDeflateReader(body)
			if tc.decoder {
				reader.dr = decoder
			}
			for call := 1; call <= tc.closes; call++ {
				if err := reader.Close(); err != tc.wantErr {
					t.Fatalf("Close %d returned %v, want exact error %v", call, err, tc.wantErr)
				}
				if body.calls != call {
					t.Fatalf("Close %d left underlying body close count=%d", call, body.calls)
				}
				wantDecoderCalls := 0
				if tc.decoder {
					wantDecoderCalls = call
				}
				if decoder.calls != wantDecoderCalls {
					t.Fatalf("Close %d decoder close count=%d want=%d", call, decoder.calls, wantDecoderCalls)
				}
				if !tc.decoder && reader.dr != nil {
					t.Fatal("closing an unread response initialized its decoder")
				}
			}
		})
	}
}
