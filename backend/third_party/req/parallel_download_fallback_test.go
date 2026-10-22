package req_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
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
					before: len(before), cookie: cookieValue, caller: r.Header.Get("X-Download-Caller")})
				mu.Unlock()
			}
			if cookieValue != "caller-session" || r.Header.Get("X-Download-Caller") != "retained" {
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
			return e.Method == http.MethodGet && e.Range == "" && e.Status == http.StatusOK
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
			return e.Method == http.MethodGet && e.Range == "" && e.Status == http.StatusOK && bytes.Equal(e.Body, entity) && downloadGWireComplete(e)
		})
		if err != nil || actualResponse.cause != nil {
			t.Fatalf("G1748_DOWNLOAD_ASSERT: ordinary non-Range server recovery failed: Do=%v response=%v", err, actualResponse.cause)
		}
		mu.Lock()
		fullObserved := append([]fullRequest(nil), fullRequests...)
		mu.Unlock()
		if len(fullObserved) != 1 || fullObserved[0].hadRange || fullObserved[0].hadIfRange || fullObserved[0].before != 0 ||
			fullObserved[0].cookie != "caller-session" || fullObserved[0].caller != "retained" {
			t.Fatalf("G1748_DOWNLOAD_ASSERT: recovery must be one actual full GET before any output, preserving caller context: %+v", fullObserved)
		}
		actual, closed := output.snapshot()
		if closed || !bytes.Equal(actual, entity) || downloadGHash(actual) != downloadGHash(entity) {
			t.Fatalf("G1748_DOWNLOAD_ASSERT: recovery corrupted output or closed caller writer: bytes=%d want=%d closed=%v", len(actual), len(entity), closed)
		}
		if c.Headers.Get("Range") != "bytes=0-0" || c.Headers.Get("If-Range") != "\"caller-validator\"" || c.GetClient().Jar != jar {
			t.Fatal("G1748_DOWNLOAD_ASSERT: recovery mutated caller client configuration")
		}
		t.Logf("G1748_FULL_GET_COMPLETE bytes=%d hash=%s callerWriterClosed=%v", len(actual), downloadGHash(actual), closed)
	})
}
