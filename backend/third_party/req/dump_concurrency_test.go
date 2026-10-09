package req

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imroc/req/v3/internal/dump"
)

func reqDumpWait(t *testing.T, done <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func reqDumpTLSServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := C().EnableInsecureSkipVerify().SetTimeout(5 * time.Second).SetCommonRetryCount(0)
	client.GetTransport().SetProxy(nil)
	t.Cleanup(client.GetTransport().CloseIdleConnections)
	return server, client
}

func TestReqDumpSecurity_ResponseSnapshots(t *testing.T) {
	const chunk = "snapshot-stream-chunk\n"
	const chunks = 128
	release := make(chan struct{})
	var releaseOnce sync.Once
	open := func() { releaseOnce.Do(func() { close(release) }) }
	server, client := reqDumpTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			http.Error(w, "HTTP/2 required", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		for i := 0; i < chunks; i++ {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	})
	t.Cleanup(open)
	response, err := client.R().EnableDump().DisableAutoReadResponse().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatalf("response protocol = %s; want HTTP/2", response.Proto)
	}
	const readers = 16
	var ready, finished sync.WaitGroup
	ready.Add(readers)
	finished.Add(readers)
	stop := make(chan struct{})
	for i := 0; i < readers; i++ {
		go func() {
			defer finished.Done()
			// The response and Request are fully initialized before any reader.
			response.Dump()
			ready.Done()
			for {
				select {
				case <-stop:
					return
				default:
					response.Dump()
				}
			}
		}()
	}
	// Join readers on both success and failure before the test releases state.
	t.Cleanup(func() {
		close(stop)
		joined := make(chan struct{})
		go func() { finished.Wait(); close(joined) }()
		reqDumpWait(t, joined, "snapshot reader cleanup")
	})
	readersReady := make(chan struct{})
	go func() { ready.Wait(); close(readersReady) }()
	reqDumpWait(t, readersReady, "snapshot readers")
	var body []byte
	var readErr error
	bodyDone := make(chan struct{})
	go func() {
		defer close(bodyDone)
		body, readErr = io.ReadAll(response.Body)
	}()
	t.Cleanup(func() {
		response.Body.Close()
		open()
		reqDumpWait(t, bodyDone, "response body reader cleanup")
	})
	open()
	reqDumpWait(t, bodyDone, "streaming response body")
	if readErr != nil || string(body) != strings.Repeat(chunk, chunks) {
		t.Fatalf("streamed response: bytes=%d err=%v", len(body), readErr)
	}
	if count := strings.Count(response.Dump(), chunk); count != chunks {
		t.Fatalf("dump captured %d chunks; want %d", count, chunks)
	}
}

func TestReqDumpSecurity_RetryRetiresOldAttempt(t *testing.T) {
	server, client := reqDumpTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "current-upload" || r.ProtoMajor != 2 {
			http.Error(w, "retry request body/protocol mismatch", http.StatusBadRequest)
			return
		}
		if r.Header.Get("X-Attempt") == "0" {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "first-attempt-response")
			return
		}
		io.WriteString(w, "current-attempt-response")
	})
	request := client.R()
	buffer := request.getDumpBuffer()
	if _, err := buffer.Write([]byte("before-first-attempt\n")); err != nil {
		t.Fatal(err)
	}
	request.SetDumpOptions(&DumpOptions{
		Output: buffer, RequestOutput: buffer, ResponseOutput: buffer,
		RequestHeaderOutput: buffer, RequestBodyOutput: buffer,
		ResponseHeaderOutput: buffer, ResponseBodyOutput: buffer,
		RequestHeader: true, RequestBody: true, ResponseHeader: true, ResponseBody: true,
	}).EnableDump().SetRetryCount(1).
		SetRetryFixedInterval(0).
		SetRetryCondition(func(response *Response, err error) bool {
			return err == nil && response.StatusCode == http.StatusServiceUnavailable
		})
	var old *dump.Dumper
	oldWrites := make(chan struct{})
	lateWrite := make(chan struct{})
	lateDone := make(chan struct{})
	var openLate sync.Once
	open := func() { openLate.Do(func() { close(lateWrite) }) }
	t.Cleanup(open)
	captureAttempt := func(r *Request) error {
		r.SetHeader("X-Attempt", fmt.Sprint(r.RetryAttempt))
		current, ok := r.Context().Value(dump.DumperKey).(*dump.Dumper)
		if !ok {
			return fmt.Errorf("enabled attempt has no dumper")
		}
		if r.RetryAttempt == 0 {
			old = current
			return nil
		}
		if current == old {
			return fmt.Errorf("retry reused the retired dumper")
		}
		t.Cleanup(func() {
			open()
			reqDumpWait(t, lateDone, "retired writer cleanup")
		})
		// The client wrapper observes the sink after attempt configuration is
		// frozen, and forwards that exact context to the real transport.
		go func() {
			defer close(lateDone)
			for i := 0; i < 64; i++ {
				old.DumpRequestBody([]byte("retired-attempt-write\n"))
			}
			close(oldWrites)
			<-lateWrite
			old.DumpResponseBody([]byte("retired-after-return\n"))
		}()
		return nil
	}
	client.WrapRoundTripFunc(func(next RoundTripper) RoundTripFunc {
		return func(r *Request) (*Response, error) {
			if err := captureAttempt(r); err != nil {
				return r.newErrorResponse(err), err
			}
			return next.RoundTrip(r)
		}
	})
	response, err := request.SetBodyString("current-upload").Post(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	reqDumpWait(t, oldWrites, "retired writes during retry")
	if response.ProtoMajor != 2 || request.RetryAttempt != 1 || response.String() != "current-attempt-response" {
		t.Fatalf("retry response: protocol=%s attempt=%d body=%q", response.Proto, request.RetryAttempt, response.String())
	}
	before := response.Dump()
	open()
	reqDumpWait(t, lateDone, "retired write after response return")
	after := response.Dump()
	if strings.Contains(after, "retired-") || strings.Contains(after, "first-attempt-response") || strings.Contains(after, "before-first-attempt") {
		t.Fatalf("retry dump contains retired output: %q", after)
	}
	if before != after || !strings.Contains(after, "current-upload") || !strings.Contains(after, "current-attempt-response") {
		t.Fatalf("current attempt dump changed or lost content: before=%q after=%q", before, after)
	}
}

func TestReqDumpSecurity_RetryContextLifetime(t *testing.T) {
	for _, mode := range []string{"caller_context", "opaque_context"} {
		t.Run(mode, func(t *testing.T) {
			const retries = 64
			var calls atomic.Int32
			server, client := reqDumpTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
				attempt := calls.Add(1) - 1
				if r.ProtoMajor != 2 || r.Header.Get("X-Context-Attempt") != fmt.Sprint(attempt) {
					http.Error(w, "retry attempt/protocol mismatch", http.StatusBadRequest)
					return
				}
				if attempt < retries {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
				fmt.Fprintf(w, "context-attempt-%02d", attempt)
			})
			type callerKey struct{}
			type opaqueKey struct{}
			deadline := time.Now().Add(30 * time.Second)
			caller, cancel := context.WithDeadline(context.WithValue(context.Background(), callerKey{}, "caller-value"), deadline)
			t.Cleanup(cancel)
			request := client.R().SetContext(caller)
			parent := context.Context(caller)
			if mode == "opaque_context" {
				// This caller-owned value context wraps a published dump context.
				// Its semantics require retaining that parent, not unwrapping it.
				request.EnableDump().SetContextData(opaqueKey{}, "opaque-value")
				parent = request.Context()
			}
			request.EnableDump().SetRetryCount(-1).SetRetryFixedInterval(0).
				SetRetryCondition(func(response *Response, err error) bool {
					return err == nil && response.StatusCode == http.StatusServiceUnavailable
				})
			var contexts []context.Context
			var dumpers []*dump.Dumper
			captureAttempt := func(r *Request) error {
				if r != request {
					return nil
				}
				current := r.Context()
				if current.Value(callerKey{}) != "caller-value" || current.Done() != caller.Done() || current.Err() != nil {
					return fmt.Errorf("attempt %d lost caller context semantics", r.RetryAttempt)
				}
				if got, ok := current.Deadline(); !ok || !got.Equal(deadline) {
					return fmt.Errorf("attempt %d changed caller deadline", r.RetryAttempt)
				}
				if mode == "opaque_context" && current.Value(opaqueKey{}) != "opaque-value" {
					return fmt.Errorf("attempt %d lost opaque caller value", r.RetryAttempt)
				}
				// Parent identity is the retention assertion. It supplements the
				// actual HTTP retries and immutable published-context checks below.
				owned, ok := current.(*requestDumpContext)
				if !ok || owned.Context != parent {
					return fmt.Errorf("attempt %d accumulated dump context parents", r.RetryAttempt)
				}
				d, ok := current.Value(dump.DumperKey).(*dump.Dumper)
				if !ok {
					return fmt.Errorf("attempt %d has no dumper", r.RetryAttempt)
				}
				for _, prior := range dumpers {
					if prior == d {
						return fmt.Errorf("attempt %d reused a published dumper", r.RetryAttempt)
					}
				}
				contexts = append(contexts, current)
				dumpers = append(dumpers, d)
				r.SetHeader("X-Context-Attempt", fmt.Sprint(r.RetryAttempt))
				return nil
			}
			client.WrapRoundTripFunc(func(next RoundTripper) RoundTripFunc {
				return func(r *Request) (*Response, error) {
					if err := captureAttempt(r); err != nil {
						return r.newErrorResponse(err), err
					}
					return next.RoundTrip(r)
				}
			})
			response, err := request.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != retries+1 || len(contexts) != retries+1 || request.RetryAttempt != retries || response.String() != "context-attempt-64" {
				t.Fatalf("actual retries: calls=%d contexts=%d retry=%d body=%q", calls.Load(), len(contexts), request.RetryAttempt, response.String())
			}
			before := response.Dump()
			for i := 0; i < retries; i++ {
				if contexts[i].Value(dump.DumperKey) != dumpers[i] {
					t.Fatalf("published context %d changed its dumper", i)
				}
				dumpers[i].DumpDefault([]byte("retired-context-write\n"))
			}
			if after := response.Dump(); after != before || strings.Contains(after, "retired-context-write") || !strings.Contains(after, "context-attempt-64") {
				t.Fatalf("retired contexts changed the final attempt dump: %q", after)
			}
			cancel()
			for i, published := range contexts {
				if published.Done() != caller.Done() || !errors.Is(published.Err(), context.Canceled) || published.Value(dump.DumperKey) != dumpers[i] {
					t.Fatalf("published context %d lost cancellation or immutable values", i)
				}
			}
			_, err = client.R().SetContext(request.Context()).EnableDump().Get(server.URL)
			if !errors.Is(err, context.Canceled) || calls.Load() != retries+1 {
				t.Fatalf("canceled inherited context sent another request: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

// The gate blocks only the terminal delimiter; it does not serialize writes
// to the bare output buffer or hide a late upload write from the race detector.
type reqDumpTerminalGate struct {
	output  *bytes.Buffer
	entered chan struct{}
	release chan struct{}
}

func (w *reqDumpTerminalGate) Write(p []byte) (int, error) {
	if bytes.Equal(p, []byte("\r\n\r\n")) {
		close(w.entered)
		<-w.release
	}
	return w.output.Write(p)
}

func TestReqDumpSecurity_FinalDelimiter(t *testing.T) {
	for _, mode := range []string{"final_data", "empty_data", "trailer_headers", "large_final_data", "large_trailer_headers"} {
		t.Run(mode, func(t *testing.T) {
			payload := "upload"
			if mode == "large_final_data" {
				payload = strings.Repeat("u", 8192)
			}
			trailer := mode == "trailer_headers" || mode == "large_trailer_headers"
			trailerValue := "done"
			if mode == "large_trailer_headers" {
				// Repeated 't' occupies five HPACK Huffman bits, so this value
				// alone encodes to 5120 bytes and exceeds bufio's 4 KiB buffer.
				trailerValue = strings.Repeat("t", 8192)
			}
			endReceived := make(chan struct{})
			peerReading := make(chan struct{})
			server, client := reqDumpTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
				close(peerReading)
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != payload || r.ProtoMajor != 2 {
					http.Error(w, "request body/protocol mismatch", http.StatusBadRequest)
					return
				}
				if trailer && r.Trailer.Get("X-Dump-Trailer") != trailerValue {
					http.Error(w, "missing trailer", http.StatusBadRequest)
					return
				}
				close(endReceived)
				io.WriteString(w, "ok")
			})
			if trailer {
				client.GetTransport().WrapRoundTripFunc(func(next http.RoundTripper) HttpRoundTripFunc {
					return func(r *http.Request) (*http.Response, error) {
						r.Trailer = http.Header{"X-Dump-Trailer": []string{trailerValue}}
						return next.RoundTrip(r)
					}
				})
			}
			var output bytes.Buffer
			var dumpOutput io.Writer = &output
			var gate *reqDumpTerminalGate
			var openOnce sync.Once
			open := func() {}
			if strings.HasPrefix(mode, "large_") {
				gate = &reqDumpTerminalGate{output: &output, entered: make(chan struct{}), release: make(chan struct{})}
				dumpOutput = gate
				open = func() { openOnce.Do(func() { close(gate.release) }) }
			}
			request := client.R().SetDumpOptions(&DumpOptions{Output: dumpOutput, RequestBody: true}).EnableDump()
			if mode == "empty_data" {
				// Unknown length: the first Read returns data without EOF, so a
				// separate empty DATA frame terminates this upload.
				request.SetBody(io.NopCloser(strings.NewReader("upload")))
			} else {
				// Known length allows the EOF probe to put END_STREAM on DATA.
				request.SetBodyString(payload)
			}
			var response *Response
			var err error
			returned := make(chan struct{})
			go func() {
				defer close(returned)
				response, err = request.Post(server.URL)
			}()
			t.Cleanup(func() {
				open()
				reqDumpWait(t, returned, "terminal request cleanup")
			})
			if gate != nil {
				reqDumpWait(t, gate.entered, "terminal delimiter writer gate")
				reqDumpWait(t, peerReading, "peer request-body read stage")
				// Keep the gate closed for a bounded observation window after
				// the peer is ready to read. Expiry is only absence of a forbidden
				// event; setup/join timeouts elsewhere always fail the fixture.
				observation := time.NewTimer(100 * time.Millisecond)
				select {
				case <-endReceived:
					observation.Stop()
					t.Fatal("peer received END_STREAM before the final dump delimiter was published")
				case <-returned:
					observation.Stop()
					t.Fatal("Post returned while the final dump delimiter was blocked")
				case <-observation.C:
				}
				select {
				case <-endReceived:
					t.Fatal("peer received END_STREAM during the delimiter observation window")
				case <-returned:
					t.Fatal("Post returned while the final dump delimiter was blocked")
				default:
				}
				open()
			}
			reqDumpWait(t, returned, "completed terminal upload")
			if err != nil {
				t.Fatal(err)
			}
			if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK || response.String() != "ok" {
				t.Fatalf("response: %s status=%d body=%q", response.Proto, response.StatusCode, response.String())
			}
			// The caller can inspect a bare writer as soon as auto-read Post
			// returns. No upload goroutine may append the final delimiter later.
			want := payload + "\r\n\r\n"
			if trailer {
				// encodeTrailers historically emits its header through body dumpers.
				want = payload + "x-dump-trailer: " + trailerValue + "\r\n\r\n\r\n"
			}
			for i := 0; i < 64; i++ {
				if got := output.String(); got != want {
					t.Fatalf("request dump after return = %q; want one final delimiter", got)
				}
			}
		})
	}
}

func TestReqDumpSecurity_EarlyResponse(t *testing.T) {
	for _, mode := range []string{"auto_read", "manual_full_duplex"} {
		t.Run(mode, func(t *testing.T) {
			server, client := reqDumpTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				if mode == "manual_full_duplex" {
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != "sent-after-response-headers" {
						return
					}
				}
				io.WriteString(w, "early-response")
			})
			reader, writer := io.Pipe()
			t.Cleanup(func() { reader.Close(); writer.Close() })
			request := client.R().EnableDump().SetBody(reader)
			if mode == "manual_full_duplex" {
				request.DisableAutoReadResponse()
			}
			var response *Response
			var requestErr error
			returned := make(chan struct{})
			go func() {
				defer close(returned)
				response, requestErr = request.Post(server.URL)
			}()
			t.Cleanup(func() {
				reader.Close()
				writer.Close()
				reqDumpWait(t, returned, "early response request cleanup")
			})
			// The pipe has no data or EOF yet. Waiting for upload completion here
			// would deadlock both early-response modes.
			reqDumpWait(t, returned, "early HTTP/2 response before upload EOF")
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			defer response.Body.Close()
			if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
				t.Fatalf("early response: %s status=%d", response.Proto, response.StatusCode)
			}
			if mode == "auto_read" {
				if response.String() != "early-response" {
					t.Fatalf("auto-read body = %q", response.String())
				}
				return
			}
			if len(response.body) != 0 {
				t.Fatal("DisableAutoReadResponse consumed the response body")
			}
			var writeErr error
			written := make(chan struct{})
			go func() {
				defer close(written)
				_, writeErr = io.WriteString(writer, "sent-after-response-headers")
				writer.Close()
			}()
			t.Cleanup(func() {
				reader.Close()
				writer.Close()
				reqDumpWait(t, written, "full duplex upload writer cleanup")
			})
			reqDumpWait(t, written, "full duplex upload after response headers")
			if writeErr != nil {
				t.Fatal(writeErr)
			}
			body, err := io.ReadAll(response.Body)
			if err != nil || string(body) != "early-response" {
				t.Fatalf("manual response body = %q, err=%v", body, err)
			}
			if !strings.Contains(response.Dump(), "sent-after-response-headers") || !strings.Contains(response.Dump(), "early-response") {
				t.Fatalf("full duplex dump lost data: %q", response.Dump())
			}
		})
	}
}

func TestReqDumpSecurity_OptionsAfterEnable(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "options_without_enable"
		if enabled {
			name = "enabled_rebind"
		}
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int32
			server, client := reqDumpTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
				if attempts.Add(1) == 1 && !enabled {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
				io.WriteString(w, "ok")
			})
			request := client.R()
			if enabled {
				request.EnableDump()
			}
			var output bytes.Buffer
			if enabled {
				request.SetDumpOptions(&DumpOptions{Output: &output, ResponseBody: true})
			} else {
				// A nil default Output initializes the request's internal buffer.
				// Retrying that request must still leave dumping disabled.
				request.SetDumpOptions(&DumpOptions{ResponseOutput: &output, ResponseBody: true}).
					SetRetryCount(1).SetRetryFixedInterval(0).
					SetRetryCondition(func(response *Response, err error) bool {
						return err == nil && response.StatusCode == http.StatusServiceUnavailable
					})
			}
			response, err := request.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if response.ProtoMajor != 2 || response.String() != "ok" {
				t.Fatalf("response = %s %q", response.Proto, response.String())
			}
			if enabled && output.String() != "ok\r\n" {
				t.Fatalf("changed options dump = %q; want response body only", output.String())
			}
			if !enabled && output.Len() != 0 {
				t.Fatalf("SetDumpOptions enabled dumping: %q", output.String())
			}
			if !enabled && (request.RetryAttempt != 1 || attempts.Load() != 2) {
				t.Fatalf("disabled options retry: attempts=%d retry=%d", attempts.Load(), request.RetryAttempt)
			}
			if response.Dump() != "" {
				t.Fatalf("old request buffer still received output: %q", response.Dump())
			}
		})
	}
}

func TestReqDumpSecurity_ExportedOptions(t *testing.T) {
	for _, mode := range []string{"before_send", "before_request_hook"} {
		t.Run(mode, func(t *testing.T) {
			server, client := reqDumpTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "ok")
			})
			var original, output bytes.Buffer
			options := &DumpOptions{Output: &original, RequestHeader: true, ResponseHeader: true, ResponseBody: true}
			request := client.R().SetDumpOptions(options).EnableDump()
			configure := func() {
				options.Output = &output
				options.RequestHeader = false
				options.ResponseHeader = false
			}
			if mode == "before_request_hook" {
				client.OnBeforeRequest(func(_ *Client, _ *Request) error {
					configure()
					return nil
				})
			} else {
				configure()
			}
			response, err := request.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if response.ProtoMajor != 2 || response.String() != "ok" || original.Len() != 0 || output.String() != "ok\r\n" {
				t.Fatalf("exported options: protocol=%s body=%q original=%q output=%q", response.Proto, response.String(), original.String(), output.String())
			}
			if snapshot := response.Dump(); snapshot != "" {
				t.Fatalf("external output unexpectedly populated request snapshot: %q", snapshot)
			}
		})
	}
}
