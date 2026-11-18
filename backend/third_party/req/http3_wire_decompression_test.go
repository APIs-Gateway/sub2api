package req

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	peerhttp3 "github.com/quic-go/quic-go/http3"
)

// The owned peer sends real HTTP3 response DATA; neither the private transport
// nor its response body is replaced by a test double. The original seven cases
// retain the actual RED witness from CI run 38735340631.
func TestReqHTTP3Decompression_PublicResponseBody(t *testing.T) {
	for _, tc := range []struct {
		name          string
		encoding      string
		auto          bool
		automaticGzip bool
		codingHeader  string
	}{
		{name: "enabled_identity", encoding: "identity", auto: true},
		{name: "enabled_manual_gzip", encoding: "gzip", auto: true},
		{name: "enabled_manual_deflate", encoding: "deflate", auto: true},
		{name: "disabled_identity", encoding: "identity"},
		{name: "disabled_manual_gzip", encoding: "gzip"},
		{name: "disabled_manual_deflate", encoding: "deflate"},
		{name: "enabled_automatic_gzip", encoding: "gzip", auto: true, automaticGzip: true},
		{name: "enabled_unknown_coding", encoding: "identity", auto: true, codingHeader: "x-owned-unknown"},
		{name: "disabled_unknown_coding", encoding: "identity", codingHeader: "x-owned-unknown"},
		{name: "enabled_identity_coding", encoding: "identity", auto: true, codingHeader: "identity"},
		{name: "disabled_identity_coding", encoding: "identity", codingHeader: "identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte("owned HTTP3 response body survives automatic decompression\n")
			wire := payload
			if tc.encoding != "identity" {
				var encoded bytes.Buffer
				var compressor io.WriteCloser
				if tc.encoding == "gzip" {
					compressor = gzip.NewWriter(&encoded)
				} else {
					var err error
					// Match the library's existing raw-DEFLATE reader dialect.
					compressor, err = flate.NewWriter(&encoded, flate.DefaultCompression)
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err := compressor.Write(payload); err != nil {
					t.Fatal(err)
				}
				if err := compressor.Close(); err != nil {
					t.Fatal(err)
				}
				wire = encoded.Bytes()
			}
			type peerObservation struct {
				protocol       int
				alpn           string
				tlsVersion     uint16
				acceptEncoding string
				written        int
				err            error
			}
			observed := make(chan peerObservation, 1)
			peer := h3WireNewPeer(t, func(w http.ResponseWriter, r *http.Request, _ <-chan struct{}) {
				observation := peerObservation{protocol: r.ProtoMajor, acceptEncoding: r.Header.Get("Accept-Encoding")}
				if r.TLS != nil {
					observation.alpn = r.TLS.NegotiatedProtocol
					observation.tlsVersion = r.TLS.Version
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", strconv.Itoa(len(wire)))
				if tc.encoding != "identity" {
					w.Header().Set("Content-Encoding", tc.encoding)
				}
				if tc.codingHeader != "" {
					w.Header().Set("Content-Encoding", tc.codingHeader)
				}
				observation.written, observation.err = w.Write(wire)
				observed <- observation
			})
			client := h3WireNewClient(t, peer.clientTLS)
			if tc.auto {
				client.EnableAutoDecompress()
			} else {
				client.DisableAutoDecompress()
			}
			req, err := http.NewRequest(http.MethodGet, peer.url+"/decompression/"+tc.name, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.automaticGzip {
				// Explicit negotiation keeps requestedGzip false, reaching the
				// public AutoDecompression branch rather than its gzip shortcut.
				req.Header.Set("Accept-Encoding", tc.encoding)
				if tc.codingHeader != "" {
					req.Header.Set("Accept-Encoding", tc.codingHeader)
				}
			}
			resp, requestErr := client.Do(req)
			if resp != nil && resp.Body != nil {
				t.Cleanup(func() { _ = resp.Body.Close() })
			}
			observation := h3WireWait(t, observed, "decompression peer response")
			wantAcceptEncoding := tc.encoding
			if tc.codingHeader != "" {
				wantAcceptEncoding = tc.codingHeader
			}
			if observation.protocol != 3 || observation.alpn != peerhttp3.NextProtoH3 || observation.tlsVersion != tls.VersionTLS13 || observation.acceptEncoding != wantAcceptEncoding || observation.written != len(wire) || observation.err != nil {
				t.Fatalf("owned peer did not send the exact HTTP3 TLS/encoding/body contract: %+v", observation)
			}
			t.Logf("actual peer: protocol=%d ALPN=%s TLS=%#x Accept-Encoding=%s response-bytes=%d", observation.protocol, observation.alpn, observation.tlsVersion, observation.acceptEncoding, observation.written)
			if requestErr != nil {
				t.Fatalf("HTTP3_DECOMPRESSION_ASSERT: public mode %s lost its actual response body: %v", tc.name, requestErr)
			}
			if resp == nil || resp.Body == nil {
				t.Fatalf("HTTP3_DECOMPRESSION_ASSERT: public mode %s returned a nil response/body", tc.name)
			}
			if resp.StatusCode != http.StatusOK || resp.ProtoMajor != 3 || resp.TLS == nil || !resp.TLS.HandshakeComplete || resp.TLS.Version != tls.VersionTLS13 || resp.TLS.NegotiatedProtocol != peerhttp3.NextProtoH3 || len(resp.TLS.VerifiedChains) == 0 {
				t.Fatalf("response is not a verified TLS1.3 HTTP3 success: %+v", resp)
			}
			data, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			wantBody := wire
			wantEncoding := tc.encoding
			wantLength := int64(len(wire))
			wantUncompressed := tc.auto && tc.encoding != "identity"
			if tc.encoding == "identity" {
				wantEncoding = tc.codingHeader
			} else if wantUncompressed {
				wantBody = payload
				wantEncoding = ""
				wantLength = -1
			}
			if readErr != nil || closeErr != nil || !bytes.Equal(data, wantBody) {
				t.Fatalf("HTTP3_DECOMPRESSION_ASSERT: mode=%s decoded body=%q want=%q read=%v close=%v", tc.name, data, wantBody, readErr, closeErr)
			}
			if resp.Header.Get("Content-Encoding") != wantEncoding || resp.ContentLength != wantLength || resp.Uncompressed != wantUncompressed {
				t.Fatalf("mode=%s response metadata: encoding=%q length=%d uncompressed=%v", tc.name, resp.Header.Get("Content-Encoding"), resp.ContentLength, resp.Uncompressed)
			}
			wantLengthHeader := strconv.Itoa(len(wire))
			if wantUncompressed {
				wantLengthHeader = ""
			}
			if resp.Header.Get("Content-Length") != wantLengthHeader {
				t.Fatalf("mode=%s Content-Length=%q want=%q", tc.name, resp.Header.Get("Content-Length"), wantLengthHeader)
			}
		})
	}
}

// A decoder must preserve Close on the owned HTTP3 stream after its first Read.
// Keep the caller context live until the peer observes the body's Close, so
// request cancellation, client timeouts and fixture cleanup cannot satisfy it.
func TestReqHTTP3DeflateClose_PublicStreamRetirement(t *testing.T) {
	for _, mode := range []string{"unread_close", "partial_read_close"} {
		t.Run(mode, func(t *testing.T) {
			ready := make(chan error, 1)
			peerError := make(chan error, 1)
			peer := h3WireNewPeer(t, func(w http.ResponseWriter, r *http.Request, stop <-chan struct{}) {
				if r.ProtoMajor != 3 || r.TLS == nil || r.TLS.Version != tls.VersionTLS13 || r.TLS.NegotiatedProtocol != peerhttp3.NextProtoH3 || r.Header.Get("Accept-Encoding") != "deflate" {
					ready <- errors.New("peer did not receive the real HTTP3 TLS1.3 deflate request")
					return
				}
				w.Header().Set("Content-Encoding", "deflate")
				w.Header().Set("Content-Type", "application/octet-stream")
				deflater, err := flate.NewWriter(w, flate.DefaultCompression)
				if err != nil {
					ready <- err
					return
				}
				defer deflater.Close()
				_, err = io.WriteString(deflater, "prefix:owned-nonfinal-deflate-stream")
				if err == nil {
					err = deflater.Flush()
				}
				if err == nil {
					err = http.NewResponseController(w).Flush()
				}
				ready <- err
				if err != nil {
					return
				}
				select {
				case <-r.Context().Done():
					// The compressed stream was flushed but never finalized.
					// Demand a real remote stream error on the next peer write.
					_, err := io.WriteString(deflater, "after-close")
					if err == nil {
						err = deflater.Flush()
					}
					if err == nil {
						err = http.NewResponseController(w).Flush()
					}
					peerError <- err
				case <-stop:
					peerError <- errors.New("fixture cleanup preceded the deflate body Close contract")
				}
			})
			client := h3WireNewClient(t, peer.clientTLS).EnableAutoDecompress().SetTimeout(0)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, peer.url+"/deflate-close/"+mode, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Accept-Encoding", "deflate")
			result := h3WireDo(t, client, req, cancel)
			resp := result.response
			if result.err != nil || resp == nil || resp.Body == nil {
				t.Fatalf("real deflate response before Close: response=%v error=%v", resp, result.err)
			}
			if resp.ProtoMajor != 3 || resp.TLS == nil || !resp.TLS.HandshakeComplete || resp.TLS.NegotiatedProtocol != peerhttp3.NextProtoH3 || len(resp.TLS.VerifiedChains) == 0 || !resp.Uncompressed || resp.Header.Get("Content-Encoding") != "" {
				t.Fatal("response did not enter the verified public HTTP3 deflate decoder")
			}
			if err := h3WireWait(t, ready, "nonfinal deflate peer flush"); err != nil {
				t.Fatal(err)
			}
			if mode == "partial_read_close" {
				var prefix [6]byte
				if err := h3WireReadFull(t, resp.Body, prefix[:], cancel); err != nil || string(prefix[:]) != "prefix" {
					t.Fatalf("real partial decoded payload=%q read=%v", prefix, err)
				}
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatalf("public deflate body Close: %v", err)
			}
			select {
			case err := <-peerError:
				var remote *peerhttp3.Error
				if !errors.As(err, &remote) || !remote.Remote || remote.ErrorCode != peerhttp3.ErrCodeRequestCanceled || ctx.Err() != nil {
					t.Fatalf("HTTP3_DEFLATE_CLOSE_ASSERT: body Close must deliver remote H3_REQUEST_CANCELLED with caller context live: peer=%v caller=%v", err, ctx.Err())
				}
				t.Logf("actual deflate Close: mode=%s peer=%v caller-context=%v", mode, err, ctx.Err())
			case <-time.After(8 * time.Second):
				t.Fatalf("HTTP3_DEFLATE_CLOSE_ASSERT: mode=%s body Close returned but the real nonfinal peer stream remained open; caller-context=%v", mode, ctx.Err())
			}
		})
	}
}
