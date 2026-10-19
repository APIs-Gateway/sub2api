// Public response/charset contracts informed by pinned req and x/net tests.
// See CLIENT-BEHAVIOR-PROVENANCE.json and CLIENT-BEHAVIOR-LICENSE.
package req

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type clientBehaviorClosedBody struct {
	io.ReadCloser
	closed atomic.Int32
}

func (b *clientBehaviorClosedBody) Close() error {
	b.closed.Add(1)
	return b.ReadCloser.Close()
}

// Wrap only the actual transport response, retaining every real read/error.
// Closure observation never supplies bytes or replaces transport behavior.
func clientBehaviorTrackBody(client *Client) <-chan *clientBehaviorClosedBody {
	observed := make(chan *clientBehaviorClosedBody, 1)
	client.GetTransport().WrapRoundTripFunc(func(next http.RoundTripper) HttpRoundTripFunc {
		return func(r *http.Request) (*http.Response, error) {
			resp, err := next.RoundTrip(r)
			if resp != nil && resp.Body != nil {
				body := &clientBehaviorClosedBody{ReadCloser: resp.Body}
				resp.Body = body
				observed <- body
			}
			return resp, err
		}
	})
	return observed
}

type clientBehaviorDocument struct {
	Value string `json:"value" xml:"value"`
}

func TestReqClientResponse_ManualDecoding(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, wire string
		explicitXML, invalid    bool
	}{
		{name: "xml_by_content_type", contentType: "application/xml", wire: `<doc><value>owned XML</value></doc>`},
		{name: "explicit_xml", contentType: "application/octet-stream", wire: `<doc><value>owned XML</value></doc>`, explicitXML: true},
		{name: "unknown_type_falls_back_json", contentType: "application/x-owned", wire: `{"value":"owned JSON"}`},
		{name: "invalid_xml", contentType: "application/xml", wire: `<doc><value></doc>`, invalid: true},
		{name: "invalid_json", contentType: "application/json", wire: `{"value":]`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, _ *http.Request, _ clientBehaviorObservation) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = io.WriteString(w, tc.wire)
			})
			client := newClientBehaviorClient(t)
			closed := clientBehaviorTrackBody(client)
			resp, err := client.R().DisableAutoReadResponse().Get(peer.url + "/manual-decode")
			if err != nil || resp == nil || resp.Body == nil {
				t.Fatalf("real response=%v err=%v", resp, err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			var got clientBehaviorDocument
			var parseErr error
			if tc.explicitXML {
				parseErr = resp.UnmarshalXml(&got)
			} else {
				parseErr = resp.Unmarshal(&got)
			}
			if tc.invalid {
				var expected clientBehaviorDocument
				var want error
				if tc.contentType == "application/xml" {
					want = xml.Unmarshal([]byte(tc.wire), &expected)
				} else {
					want = json.Unmarshal([]byte(tc.wire), &expected)
				}
				if parseErr == nil || want == nil || reflect.TypeOf(parseErr) != reflect.TypeOf(want) || parseErr.Error() != want.Error() {
					t.Fatalf("actual parser error=%T %v want=%T %v", parseErr, parseErr, want, want)
				}
			} else {
				want := "owned JSON"
				if tc.explicitXML || tc.contentType == "application/xml" {
					want = "owned XML"
				}
				if parseErr != nil || got.Value != want {
					t.Fatalf("manual parse=%+v err=%v want=%q", got, parseErr, want)
				}
			}
			if string(resp.Bytes()) != tc.wire || resp.SuccessResult() != nil || resp.ErrorResult() != nil {
				t.Fatalf("manual body/result ownership: body=%q success=%v error=%v", resp.Bytes(), resp.SuccessResult(), resp.ErrorResult())
			}
			if body := <-closed; body.closed.Load() != 1 {
				t.Fatalf("manual actual body Close=%d want=1", body.closed.Load())
			}
			peer.observations(t, 1)
		})
	}
}

func TestReqClientResponse_TruncatedBodyOwnership(t *testing.T) {
	peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, _ *http.Request, _ clientBehaviorObservation) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "40")
		_, _ = io.WriteString(w, `{"value":"partial`)
		// net/http closes the real connection when the handler returns without
		// satisfying the promised Content-Length.
	})
	client := newClientBehaviorClient(t)
	closed := clientBehaviorTrackBody(client)
	resp, err := client.R().DisableAutoReadResponse().Get(peer.url + "/truncated")
	if err != nil || resp == nil || resp.Body == nil {
		t.Fatalf("headers before truncated read: response=%v err=%v", resp, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, readErr := resp.ToBytes()
	if !errors.Is(readErr, io.ErrUnexpectedEOF) || string(body) != `{"value":"partial` || !bytes.Equal(resp.Bytes(), body) || resp.Err != readErr {
		t.Fatalf("partial bytes/error ownership: body=%q stored=%q readErr=%v storedErr=%v", body, resp.Bytes(), readErr, resp.Err)
	}
	for _, later := range []func() error{
		func() error { _, err := resp.ToBytes(); return err },
		func() error { return resp.UnmarshalJson(&clientBehaviorDocument{}) },
		func() error { return resp.UnmarshalXml(&clientBehaviorDocument{}) },
		func() error { return resp.Unmarshal(&clientBehaviorDocument{}) },
	} {
		if err := later(); err != readErr {
			t.Fatalf("terminal body error was replaced: got=%v want=%v", err, readErr)
		}
	}
	if body := <-closed; body.closed.Load() != 1 {
		t.Fatalf("truncated real body Close=%d want=1", body.closed.Load())
	}
	if resp.SuccessResult() != nil {
		t.Fatalf("truncated response published success: %v", resp.SuccessResult())
	}
	peer.observations(t, 1)
}

func TestReqClientResponse_TransformerFailure(t *testing.T) {
	peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, _ *http.Request, _ clientBehaviorObservation) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"value":"real wire body"}`)
	})
	client := newClientBehaviorClient(t)
	closed := clientBehaviorTrackBody(client)
	sentinel := errors.New("owned transformer rejected real bytes")
	calls := 0
	client.SetResponseBodyTransformer(func(body []byte, r *Request, resp *Response) ([]byte, error) {
		calls++
		if string(body) != `{"value":"real wire body"}` || r.RawRequest.URL.Path != "/transform" || resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("transformer did not receive real HTTP response: body=%q", body)
		}
		return body, sentinel
	})
	var result clientBehaviorDocument
	resp, err := client.R().DisableAutoReadResponse().SetSuccessResult(&result).Get(peer.url + "/transform")
	if !errors.Is(err, sentinel) || resp == nil || resp.Err != sentinel || calls != 1 || result.Value != "" || resp.SuccessResult() != nil {
		t.Fatalf("transformer failure ownership: response=%v err=%v calls=%d result=%+v", resp, err, calls, result)
	}
	if string(resp.Bytes()) != `{"value":"real wire body"}` {
		t.Fatalf("real rejected bytes lost: %q", resp.Bytes())
	}
	if _, err := resp.ToBytes(); err != sentinel {
		t.Fatalf("transform error lost on ToBytes: %v", err)
	}
	if err := resp.Unmarshal(&result); err != sentinel {
		t.Fatalf("transform error lost on Unmarshal: %v", err)
	}
	if body := <-closed; body.closed.Load() != 1 {
		t.Fatalf("transformer actual body Close=%d want=1", body.closed.Load())
	}
	peer.observations(t, 1)
}

func TestReqClientResponse_AutomaticResultBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, wire string
		status                  int
		customError, invalid    bool
	}{
		{name: "unknown_type_json", contentType: "application/x-owned", wire: `{"value":"owned result"}`, status: http.StatusOK},
		{name: "invalid_json_no_success", contentType: "application/json", wire: `{"value":]`, status: http.StatusOK, invalid: true},
		{name: "xml_error_result", contentType: "application/xml", wire: `<doc><value>owned result</value></doc>`, status: http.StatusBadRequest},
		{name: "custom_error_204_has_no_body", contentType: "application/json", status: http.StatusNoContent, customError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, _ *http.Request, _ clientBehaviorObservation) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.wire)
			})
			client := newClientBehaviorClient(t)
			if tc.customError {
				client.SetResultStateCheckFunc(func(*Response) ResultState { return ErrorState })
			}
			var result, errorResult clientBehaviorDocument
			resp, err := client.R().DisableAutoReadResponse().SetSuccessResult(&result).SetErrorResult(&errorResult).Get(peer.url + "/auto-result")
			if resp == nil {
				t.Fatalf("automatic response is nil: %v", err)
			}
			t.Cleanup(func() {
				if resp.Body != nil {
					_ = resp.Body.Close()
				}
			})
			if tc.invalid {
				var syntax *json.SyntaxError
				if !errors.As(err, &syntax) || resp.Err != err || resp.SuccessResult() != nil {
					t.Fatalf("invalid JSON published success or hid parser error: resp=%v err=%v", resp, err)
				}
			} else if tc.customError {
				if err != nil || resp.ErrorResult() != nil || errorResult.Value != "" {
					t.Fatalf("204 attempted error parsing: resp=%v err=%v result=%+v", resp, err, errorResult)
				}
			} else if tc.status == http.StatusBadRequest {
				if err != nil || resp.ErrorResult() != &errorResult || errorResult.Value != "owned result" || resp.SuccessResult() != nil {
					t.Fatalf("XML error result: resp=%v err=%v value=%+v", resp, err, errorResult)
				}
			} else if err != nil || resp.SuccessResult() != &result || result.Value != "owned result" {
				t.Fatalf("fallback JSON result: resp=%v err=%v value=%+v", resp, err, result)
			}
			peer.observations(t, 1)
		})
	}
}

// Coalesce the first production decoder input chunk to the owned fixture size,
// so packet fragmentation cannot choose whether metadata is fully sniffed.
// io.ReadFull controls that chunk's error semantics; later reads are delegated.
// The production decoder and exact final byte/EOF assertions remain in use.
type clientBehaviorFirstRead struct {
	io.ReadCloser
	size  int
	first []byte
	read  bool
}

func (r *clientBehaviorFirstRead) Read(p []byte) (int, error) {
	if r.read {
		return r.ReadCloser.Read(p)
	}
	r.read = true
	first := p
	if len(first) > r.size {
		first = first[:r.size]
	}
	n, err := io.ReadFull(r.ReadCloser, first)
	r.first = append([]byte(nil), first[:n]...)
	return n, err
}

func TestReqClientResponse_CharsetShortBodies(t *testing.T) {
	for _, tc := range []struct{ name, contentType, wire, want string }{
		{name: "windows1252_meta", contentType: "text/html", wire: `<meta charset="windows-1252">R` + "\xe9sum\xe9", want: `<meta charset="windows-1252">Résumé`},
		{name: "windows1252_header", contentType: "text/plain; charset=windows-1252", wire: "R\xe9sum\xe9", want: "Résumé"},
		{name: "utf16le_bom", contentType: "text/html", wire: "\xff\xfeS0\x8c0o0\"oW[g0Y0\x020", want: "これは漢字です。"},
		{name: "unknown_meta_raw", contentType: "text/html", wire: `<meta charset="x-owned-unknown">R` + "\xe9", want: `<meta charset="x-owned-unknown">R` + "\xe9"},
		{name: "no_detection_raw", contentType: "text/html", wire: "owned raw \xe9 bytes", want: "owned raw \xe9 bytes"},
	} {
		for _, size := range []int{64, 512} {
			t.Run(fmt.Sprintf("%s_buffer_%d", tc.name, size), func(t *testing.T) {
				peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, _ *http.Request, _ clientBehaviorObservation) {
					w.Header().Set("Content-Type", tc.contentType)
					_, _ = io.WriteString(w, tc.wire)
				})
				client := newClientBehaviorClient(t).EnableAutoDecode()
				resp, err := client.R().DisableAutoReadResponse().Get(peer.url + "/short-charset")
				if err != nil || resp == nil || resp.Body == nil {
					t.Fatalf("real charset response=%v err=%v", resp, err)
				}
				t.Cleanup(func() { _ = resp.Body.Close() })
				var first *clientBehaviorFirstRead
				if !strings.Contains(tc.contentType, "charset=") {
					decoder, ok := resp.Body.(*autoDecodeReadCloser)
					if !ok {
						t.Fatalf("actual response did not select production sniffing reader: %T", resp.Body)
					}
					first = &clientBehaviorFirstRead{ReadCloser: decoder.ReadCloser, size: len(tc.wire)}
					decoder.ReadCloser = first
				}
				var got []byte
				buffer := bytes.Repeat([]byte{0xa5}, size)
				for reads := 0; ; reads++ {
					if reads == 32 {
						t.Fatal("short charset response never reached real EOF")
					}
					n, readErr := resp.Body.Read(buffer)
					if n < 0 || n > len(buffer) {
						t.Fatalf("invalid reader length=%d", n)
					}
					got = append(got, buffer[:n]...)
					if readErr == io.EOF {
						break
					}
					if readErr != nil {
						t.Fatalf("real charset read=%v", readErr)
					}
				}
				if first != nil && !bytes.Equal(first.first, []byte(tc.wire)) {
					t.Fatalf("coalesced first decoder chunk=%q want short fixture=%q", first.first, tc.wire)
				}
				if string(got) != tc.want {
					t.Fatalf("actual decoded bytes=%q len=%d want=%q len=%d", got, len(got), tc.want, len(tc.want))
				}
				if err := resp.Body.Close(); err != nil {
					t.Fatal(err)
				}
				peer.observations(t, 1)
			})
		}
	}
}
