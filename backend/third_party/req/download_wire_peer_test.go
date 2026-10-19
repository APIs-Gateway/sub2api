package req_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	req "github.com/imroc/req/v3"
)

// G fixtures exercise only exported req APIs against a real HTTP peer. They do
// not share F's client fixtures or inspect private task/file/channel state.
type downloadGExchange struct {
	Method string
	Range string
	IfRange string
	Status int
	ContentRange string
	ContentLength string
	Body []byte
	Written int
	WriteError string
	FlushError string
	Closed bool
	CloseError string
	ContextError string
	FixtureError string
}

type downloadGPeer struct {
	server *httptest.Server
	mu sync.Mutex
	started []downloadGExchange
	finished []downloadGExchange
	changed chan struct{}
	closeOnce sync.Once
}

func newDownloadGPeer(t *testing.T, serve func(http.ResponseWriter, *http.Request, *downloadGExchange)) *downloadGPeer {
	t.Helper()
	p := &downloadGPeer{changed: make(chan struct{}, 128)}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := downloadGExchange{Method: r.Method, Range: r.Header.Get("Range"), IfRange: r.Header.Get("If-Range")}
		p.mu.Lock()
		p.started = append(p.started, e)
		p.mu.Unlock()
		p.notify()
		serve(w, r, &e)
		e.Body = append([]byte(nil), e.Body...)
		p.mu.Lock()
		p.finished = append(p.finished, e)
		p.mu.Unlock()
		p.notify()
	}))
	t.Cleanup(p.close)
	return p
}

func (p *downloadGPeer) notify() {
	select {
	case p.changed <- struct{}{}:
	default:
	}
}

func (p *downloadGPeer) close() {
	p.closeOnce.Do(func() {
		p.server.CloseClientConnections()
		p.server.Close()
	})
}

func (p *downloadGPeer) observations() ([]downloadGExchange, []downloadGExchange) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]downloadGExchange(nil), p.started...), append([]downloadGExchange(nil), p.finished...)
}

func (p *downloadGPeer) await(t *testing.T, ctx context.Context, complete bool, matches func(downloadGExchange) bool) downloadGExchange {
	t.Helper()
	for {
		started, finished := p.observations()
		values := started
		if complete {
			values = finished
		}
		for _, e := range values {
			if matches(e) {
				return e
			}
		}
		select {
		case <-p.changed:
		case <-ctx.Done():
			t.Fatalf("G_FIXTURE_OBSERVATION: required peer event missing: complete=%v started=%+v finished=%+v: %v", complete, started, finished, ctx.Err())
		}
	}
}

func downloadGClient(t *testing.T) *req.Client {
	t.Helper()
	c := req.C().SetProxy(nil).SetTimeout(3 * time.Second).EnableForceHTTP1().DisableAutoDecode()
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func downloadGEntity(size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte((i*73 + 19) % 251)
	}
	return b
}

func downloadGHash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func downloadGError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func downloadGReply(w http.ResponseWriter, e *downloadGExchange, status int, contentRange string, body []byte, length int) {
	e.Status, e.ContentRange = status, contentRange
	e.ContentLength = strconv.Itoa(length)
	e.Body = append([]byte(nil), body...)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", e.ContentLength)
	w.Header().Set("ETag", `"g-download-v1"`)
	w.Header().Set("Accept-Ranges", "bytes")
	if contentRange != "" {
		w.Header().Set("Content-Range", contentRange)
	}
	w.WriteHeader(status)
	if e.Method != http.MethodHead {
		n, err := w.Write(body)
		e.Written, e.WriteError = n, downloadGError(err)
	}
	e.FlushError = downloadGError(http.NewResponseController(w).Flush())
}

// This is the peer's HTTP single-range parser, not the downloader's partitioning
// algorithm: no expected request count, segment-size reconstruction or task IDs.
func downloadGRange(value string, length int) (int, int, error) {
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, 0, fmt.Errorf("expected one bytes range: %q", value)
	}
	a, b, ok := strings.Cut(strings.TrimPrefix(value, "bytes="), "-")
	if !ok {
		return 0, 0, fmt.Errorf("missing range separator: %q", value)
	}
	start, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0, err
	}
	end := length - 1
	if b != "" {
		end, err = strconv.Atoi(b)
		if err != nil {
			return 0, 0, err
		}
	}
	if start < 0 || start > end || end >= length {
		return 0, 0, fmt.Errorf("range outside fixed entity: %q length=%d", value, length)
	}
	return start, end, nil
}

func downloadGShortReply(w http.ResponseWriter, e *downloadGExchange, contentRange string, body []byte, announced int) {
	h, ok := w.(http.Hijacker)
	if !ok {
		e.FixtureError = "loopback HTTP1 peer does not support hijack"
		return
	}
	conn, rw, err := h.Hijack()
	if err != nil {
		e.FixtureError = err.Error()
		return
	}
	e.Status, e.ContentRange, e.ContentLength = http.StatusPartialContent, contentRange, strconv.Itoa(announced)
	e.Body = append([]byte(nil), body...)
	_, err = fmt.Fprintf(rw, "HTTP/1.1 206 Partial Content\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\nContent-Range: %s\r\nConnection: close\r\n\r\n", announced, contentRange)
	if err != nil {
		e.FixtureError = err.Error()
	}
	e.Written, err = rw.Write(body)
	e.WriteError = downloadGError(err)
	e.FlushError = downloadGError(rw.Flush())
	e.CloseError = downloadGError(conn.Close())
	e.Closed = e.CloseError == ""
}

func downloadGAssertWire(t *testing.T, e downloadGExchange) {
	t.Helper()
	if e.FixtureError != "" || e.WriteError != "" || e.FlushError != "" || e.CloseError != "" || (e.Method != http.MethodHead && e.Written != len(e.Body)) {
		t.Fatalf("G_FIXTURE_TRANSFER: incomplete controlled peer transfer: %+v", e)
	}
	encoded, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("G_WIRE_COMPLETE %s", encoded)
}

var downloadGWriterError = errors.New("G controlled destination failure")

type downloadGFailedWriter struct{}

func (downloadGFailedWriter) Write([]byte) (int, error) {
	return 0, downloadGWriterError
}

// Existing ParallelDownload error paths can strand internal goroutines. Every
// parallel scenario owns a child test process so those goroutines cannot pollute
// another case. The watchdog is a separate failure, never passing/RED evidence.
func downloadGIsolated(t *testing.T, top, scenario string, body func(*testing.T)) {
	t.Helper()
	const key = "SUB2API_G_DOWNLOAD_CHILD"
	if os.Getenv(key) == top+"/"+scenario {
		t.Logf("G_CHILD_EXECUTED %s/%s", top, scenario)
		body(t)
		t.Logf("G_CHILD_BODY_DONE %s/%s", top, scenario)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+top+"$/^"+scenario+"$", "-test.v", "-test.timeout=12s")
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, key+"=") && !strings.HasPrefix(item, "SUB2API_G_DOWNLOAD_SCRATCH=") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Env = append(cmd.Env, key+"="+top+"/"+scenario, "SUB2API_G_DOWNLOAD_SCRATCH="+t.TempDir())
	output, err := cmd.CombinedOutput()
	t.Logf("G_CHILD_ACTUAL scenario=%s exit=%v\n%s", scenario, err, output)
	if ctx.Err() != nil {
		t.Fatalf("G_CHILD_WATCHDOG: child did not exit; this is not a passing fixture or a business assertion: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("G_CHILD_FAILED: inspect actual child assertion/fixture/race evidence above: %v", err)
	}
	if !strings.Contains(string(output), "G_CHILD_EXECUTED "+top+"/"+scenario) || !strings.Contains(string(output), "G_CHILD_BODY_DONE "+top+"/"+scenario) {
		t.Fatal("G_FIXTURE_CHILD: child exited without executing and completing its exact scenario")
	}
}

func downloadGResult(t *testing.T, ctx context.Context, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		t.Fatalf("G_DOWNLOAD_ASSERT: Do did not return after an observed controlled transfer/cancellation: %v", ctx.Err())
		return ctx.Err()
	}
}

var _ io.Writer = downloadGFailedWriter{}

// The parent owns this directory until the child process has exited. This also
// permits deterministic cleanup on Windows when an old source leaks a file
// handle: no finalizer/GC is used to make the Close witness pass.
func downloadGTempDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("SUB2API_G_DOWNLOAD_SCRATCH")
	if root == "" {
		t.Fatal("G_FIXTURE_CHILD: no parent-owned scratch directory")
	}
	dir, err := os.MkdirTemp(root, "download-g-")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
