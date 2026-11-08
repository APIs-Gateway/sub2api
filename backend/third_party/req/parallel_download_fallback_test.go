package req_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// This is an additional real public-API control. The original frozen G32/33
// fixtures remain unchanged, including their isolated failure observations.
type parallelFallbackWriter struct {
	mu     sync.Mutex
	body   bytes.Buffer
	closed bool
}

func (w *parallelFallbackWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}

func (w *parallelFallbackWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func (w *parallelFallbackWriter) snapshot() ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.body.Bytes()...), w.closed
}

type parallelFallbackTransportState struct {
	mu       sync.Mutex
	requests []string
	closed   bool
}

// A slice makes this value deliberately noncomparable as an interface. The
// wrapper still delegates every real HTTP exchange to the caller's transport.
type parallelFallbackTransport struct {
	next   http.RoundTripper
	state  *parallelFallbackTransportState
	marker []byte
}

func (tr parallelFallbackTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("X-Download-Transport", string(tr.marker))
	tr.state.mu.Lock()
	tr.state.requests = append(tr.state.requests, request.Method+" "+request.Header.Get("Range"))
	tr.state.mu.Unlock()
	return tr.next.RoundTrip(copy)
}

func (tr parallelFallbackTransport) CloseIdleConnections() {
	// This transport belongs to the caller. The downloader must not close it.
	tr.state.mu.Lock()
	tr.state.closed = true
	tr.state.mu.Unlock()
}

func TestReqParallelDownloadFallback(t *testing.T) {
	t.Run("ordinary_server_full_get", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		entity := downloadGEntity(97)
		output := &parallelFallbackWriter{}
		type fullRequest struct {
			hadRange   bool
			hadIfRange bool
			before     int
			cookie     string
			caller     string
			transport  string
		}
		var mu sync.Mutex
		var fullRequests []fullRequest
		p := newDownloadGPeer(t, func(w http.ResponseWriter, r *http.Request, e *downloadGExchange) {
			cookie, _ := r.Cookie("download-session")
			cookieValue := ""
			if cookie != nil {
				cookieValue = cookie.Value
			}
			if r.Method == http.MethodGet && e.Range == "" {
				before, _ := output.snapshot()
				_, hadRange := r.Header["Range"]
				_, hadIfRange := r.Header["If-Range"]
				mu.Lock()
				fullRequests = append(fullRequests, fullRequest{hadRange: hadRange, hadIfRange: hadIfRange,
					before: len(before), cookie: cookieValue, caller: r.Header.Get("X-Download-Caller"),
					transport: r.Header.Get("X-Download-Transport")})
				mu.Unlock()
			}
			if cookieValue != "caller-session" || r.Header.Get("X-Download-Caller") != "retained" || r.Header.Get("X-Download-Transport") != "caller-wrapper" {
				body := []byte("authentication context missing")
				downloadGReply(w, e, http.StatusUnauthorized, "", body, len(body))
				return
			}
			switch r.Method {
			case http.MethodHead:
				downloadGReply(w, e, http.StatusOK, "", nil, len(entity))
			case http.MethodGet:
				// A normal static endpoint is allowed to ignore Range entirely.
				downloadGReply(w, e, http.StatusOK, "", entity, len(entity))
			default:
				e.FixtureError = "unexpected method: " + r.Method
			}
		})
		c := downloadGClient(t)
		c.SetCommonHeader("X-Download-Caller", "retained")
		c.SetCommonHeader("Range", "bytes=0-0")
		c.SetCommonHeader("If-Range", "\"caller-validator\"")
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		origin, err := url.Parse(p.server.URL)
		if err != nil {
			t.Fatal(err)
		}
		jar.SetCookies(origin, []*http.Cookie{{Name: "download-session", Value: "caller-session", Path: "/"}})
		c.GetClient().Jar = jar
		transportState := &parallelFallbackTransportState{}
		c.GetClient().Transport = parallelFallbackTransport{
			next: c.GetTransport(), state: transportState, marker: []byte("caller-wrapper"),
		}
		responses := downloadGObserve(c)
		temporary := t.TempDir()
		result := make(chan error, 1)
		go func() {
			result <- c.NewParallelDownload(p.server.URL + "/entity.bin").SetSegmentSize(17).
				SetConcurrency(3).SetTempRootDir(temporary).SetOutput(output).Do(ctx)
		}()
		head := p.await(t, ctx, true, func(e downloadGExchange) bool { return e.Method == http.MethodHead })
		downloadGAssertWire(t, head)
		full := p.await(t, ctx, true, func(e downloadGExchange) bool {
			return e.Method == http.MethodGet && e.Range == ""
		})
		downloadGAssertWire(t, full)
		err = downloadGResult(t, ctx, result)
		p.close()
		p.assertFixtures(t)
		_, finished := p.observations()
		ignoredRangeObserved := false
		for _, e := range finished {
			if e.Method == http.MethodGet && e.Range != "" && e.Status == http.StatusOK {
				ignoredRangeObserved = true
				// Discarded requests may be closed during coordinated recovery.
				t.Logf("G1748_DISCARDED_RANGE %+v", e)
			}
		}
		if !ignoredRangeObserved {
			t.Fatal("G1748_PRECONDITION: no real ignored Range/200 response preceded recovery")
		}
		actualResponse := downloadGTarget(t, p, responses, func(e downloadGExchange) bool {
			return e.Method == http.MethodGet && e.Range == "" && e.Status == full.Status && bytes.Equal(e.Body, full.Body) && downloadGWireComplete(e)
		})
		if full.Status != http.StatusOK || err != nil || actualResponse.cause != nil {
			t.Fatalf("G1748_DOWNLOAD_ASSERT: ordinary non-Range server recovery failed: status=%d Do=%v response=%v", full.Status, err, actualResponse.cause)
		}
		mu.Lock()
		fullObserved := append([]fullRequest(nil), fullRequests...)
		mu.Unlock()
		if len(fullObserved) != 1 || fullObserved[0].hadRange || fullObserved[0].hadIfRange || fullObserved[0].before != 0 ||
			fullObserved[0].cookie != "caller-session" || fullObserved[0].caller != "retained" || fullObserved[0].transport != "caller-wrapper" {
			t.Fatalf("G1748_DOWNLOAD_ASSERT: recovery must be one actual full GET before any output, preserving caller context: %+v", fullObserved)
		}
		actual, closed := output.snapshot()
		if closed || !bytes.Equal(actual, entity) || downloadGHash(actual) != downloadGHash(entity) {
			t.Fatalf("G1748_DOWNLOAD_ASSERT: recovery corrupted output or closed caller writer: bytes=%d want=%d closed=%v", len(actual), len(entity), closed)
		}
		if c.Headers.Get("Range") != "bytes=0-0" || c.Headers.Get("If-Range") != "\"caller-validator\"" || c.GetClient().Jar != jar {
			t.Fatal("G1748_DOWNLOAD_ASSERT: recovery mutated caller client configuration")
		}
		installed, ok := c.GetClient().Transport.(parallelFallbackTransport)
		if !ok || installed.state != transportState {
			t.Fatal("G1748_DOWNLOAD_ASSERT: recovery replaced caller transport")
		}
		transportState.mu.Lock()
		requests := append([]string(nil), transportState.requests...)
		transportClosed := transportState.closed
		transportState.mu.Unlock()
		fullThroughWrapper, headThroughWrapper, rangeThroughWrapper := 0, false, false
		for _, request := range requests {
			switch {
			case request == "GET ":
				fullThroughWrapper++
			case request == "HEAD bytes=0-0":
				headThroughWrapper = true
			case strings.HasPrefix(request, "GET bytes="):
				rangeThroughWrapper = true
			}
		}
		if transportClosed || fullThroughWrapper != 1 || !headThroughWrapper || !rangeThroughWrapper {
			t.Fatalf("G1748_DOWNLOAD_ASSERT: caller transport ownership or actual HEAD/range/full traversal lost: closed=%v requests=%v", transportClosed, requests)
		}
		t.Logf("G1748_FULL_GET_COMPLETE bytes=%d hash=%s callerWriterClosed=%v callerTransportClosed=%v requests=%v", len(actual), downloadGHash(actual), closed, transportClosed, requests)
	})
}
