package req

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/tls"
	"io"
	"net/http"
	"strconv"
	"testing"

	peerhttp3 "github.com/quic-go/quic-go/http3"
)

// This public contract keeps the failing implementation intact for an actual
// remote RED witness. The owned peer sends real HTTP3 response DATA; neither
// the private transport nor its response body is replaced by a test double.
func TestReqHTTP3Decompression_PublicResponseBody(t *testing.T) {
	for _, tc := range []struct {
		name          string
		encoding      string
		auto          bool
		automaticGzip bool
	}{
		{name: "enabled_identity", encoding: "identity", auto: true},
		{name: "enabled_manual_gzip", encoding: "gzip", auto: true},
		{name: "enabled_manual_deflate", encoding: "deflate", auto: true},
		{name: "disabled_identity", encoding: "identity"},
		{name: "disabled_manual_gzip", encoding: "gzip"},
		{name: "disabled_manual_deflate", encoding: "deflate"},
		{name: "enabled_automatic_gzip", encoding: "gzip", auto: true, automaticGzip: true},
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
			}
			resp, requestErr := client.Do(req)
			if resp != nil && resp.Body != nil {
				t.Cleanup(func() { _ = resp.Body.Close() })
			}
			observation := h3WireWait(t, observed, "decompression peer response")
			if observation.protocol != 3 || observation.alpn != peerhttp3.NextProtoH3 || observation.tlsVersion != tls.VersionTLS13 || observation.acceptEncoding != tc.encoding || observation.written != len(wire) || observation.err != nil {
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
				wantEncoding = ""
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
