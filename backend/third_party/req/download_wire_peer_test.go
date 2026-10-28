package req_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	req "github.com/imroc/req/v3"
)

// G fixtures exercise only exported req APIs against a real HTTP peer. They do
// not share F's client fixtures or inspect private task/file/channel state.
type downloadGExchange struct {
	Method        string
	Range         string
	IfRange       string
	Status        int
	ContentRange  string
	ContentLength string
	Body          []byte
	Written       int
	WriteError    string
	FlushError    string
	Closed        bool
	CloseError    string
	ContextError  string
	FixtureError  string
}

type downloadGPeer struct {
	server    *httptest.Server
	mu        sync.Mutex
	started   []downloadGExchange
	finished  []downloadGExchange
	changed   chan struct{}
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
	t.Cleanup(func() {
		p.close()
		p.assertFixtures(t)
	})
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

func downloadGWireComplete(e downloadGExchange) bool {
	return e.FixtureError == "" && e.WriteError == "" && e.FlushError == "" && e.CloseError == "" && (e.Method == http.MethodHead || e.Written == len(e.Body))
}

func downloadGAssertWire(t *testing.T, e downloadGExchange) {
	t.Helper()
	if !downloadGWireComplete(e) {
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
	// The explicitly registered parent suite reuses these exact wire assertions.
	// Original G scenarios still execute in isolated child processes below.
	if top == "TestReqParallelDownloadContract" && t.Name() == "TestReqParallelDownloadInProcessContract/"+scenario {
		t.Logf("G_IN_PROCESS_EXECUTED %s", t.Name())
		body(t)
		t.Logf("G_IN_PROCESS_BODY_DONE %s", t.Name())
		return
	}
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
	profileDir := ""
	if root := os.Getenv("SUB2API_G_DOWNLOAD_PROFILE_DIR"); root != "" {
		var err error
		profileDir, err = downloadGPrepareProfile(root)
		if err != nil {
			t.Fatalf("G_FIXTURE_PROFILE: cannot prepare optional raw child evidence: %v", err)
		}
		cmd.Args = append(cmd.Args, "-test.coverprofile="+filepath.Join(profileDir, "child.atomic.out"))
	}
	output, err := cmd.CombinedOutput()
	t.Logf("G_CHILD_ACTUAL scenario=%s exit=%v\n%s", scenario, err, output)
	if profileDir != "" {
		t.Logf("G_CHILD_EVIDENCE_STATE watchdog=%v contextError=%v processError=%v", ctx.Err() != nil, ctx.Err(), err)
		if saveErr := downloadGSaveProfile(profileDir, t.Name(), top, scenario, cmd, output, err, ctx.Err() != nil); saveErr != nil {
			t.Fatalf("G_FIXTURE_PROFILE: raw child output was printed above; evidence save/completeness failed: %v", saveErr)
		}
	}
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
		t.Fatalf("G_DOWNLOAD_COMPLETION_TIMEOUT: Do did not return after an observed controlled transfer/cancellation: %v", ctx.Err())
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
	if root == "" && strings.HasPrefix(t.Name(), "TestReqParallelDownloadInProcessContract/") {
		return t.TempDir()
	}
	if root == "" {
		t.Fatal("G_FIXTURE_CHILD: no parent-owned scratch directory")
	}
	dir, err := os.MkdirTemp(root, "download-g-")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// OnAfterResponse observes only exported response/request metadata. It does not
// replace the transport, consume a body, change Err or implement range recovery.
type downloadGReceived struct {
	Method        string
	Range         string
	Status        int
	ContentRange  string
	ContentLength string
	HasResponse   bool
	Error         string
	ErrorType     string
	cause         error
}

type downloadGResponses struct {
	mu    sync.Mutex
	items []downloadGReceived
}

func downloadGObserve(c *req.Client) *downloadGResponses {
	r := &downloadGResponses{}
	c.OnAfterResponse(func(_ *req.Client, response *req.Response) error {
		if response == nil {
			r.mu.Lock()
			r.items = append(r.items, downloadGReceived{Error: "nil public response callback"})
			r.mu.Unlock()
			return nil
		}
		item := downloadGReceived{Error: downloadGError(response.Err), ErrorType: fmt.Sprintf("%T", response.Err), cause: response.Err}
		if response.Request != nil {
			item.Method = response.Request.Method
			if response.Request.RawRequest != nil {
				item.Range = response.Request.RawRequest.Header.Get("Range")
			}
		}
		if response.Response != nil {
			item.HasResponse = true
			item.Status = response.StatusCode
			item.ContentRange = response.Header.Get("Content-Range")
			item.ContentLength = response.Header.Get("Content-Length")
		}
		r.mu.Lock()
		r.items = append(r.items, item)
		r.mu.Unlock()
		return nil
	})
	return r
}

func (r *downloadGResponses) snapshot() []downloadGReceived {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]downloadGReceived(nil), r.items...)
}

func (p *downloadGPeer) assertFixtures(t *testing.T) {
	t.Helper()
	_, finished := p.observations()
	for _, item := range finished {
		if item.FixtureError != "" {
			t.Fatalf("G_FIXTURE_TRANSFER: invalid controlled exchange, regardless of Do error: %+v", item)
		}
	}
}

func downloadGFindTarget(p *downloadGPeer, responses *downloadGResponses, match func(downloadGExchange) bool) (downloadGReceived, bool) {
	_, finished := p.observations()
	var fallback downloadGReceived
	found := false
	for _, sent := range finished {
		if !match(sent) {
			continue
		}
		for _, actual := range responses.snapshot() {
			if actual.HasResponse && actual.Method == sent.Method && actual.Range == sent.Range && actual.Status == sent.Status && actual.ContentRange == sent.ContentRange && actual.ContentLength == sent.ContentLength {
				if actual.cause == nil {
					return actual, true
				}
				if !found || (downloadGUnrelatedError(fallback.cause) != "" && downloadGUnrelatedError(actual.cause) == "") {
					fallback, found = actual, true
				}
			}
		}
	}
	return fallback, found
}

func downloadGTarget(t *testing.T, p *downloadGPeer, responses *downloadGResponses, match func(downloadGExchange) bool) downloadGReceived {
	t.Helper()
	p.assertFixtures(t)
	actual, ok := downloadGFindTarget(p, responses, match)
	if !ok {
		_, finished := p.observations()
		t.Fatalf("G_FIXTURE_TARGET_RESPONSE: client did not receive the intended controlled response: finished=%+v received=%+v", finished, responses.snapshot())
	}
	t.Logf("G_CLIENT_RESPONSE %+v", actual)
	return actual
}

// A url.Error is a wrapper, not proof of a network failure: inspect its cause.
// Actual protocol rejection may legitimately be wrapped. With a real target
// response prerequisite, reject concrete context/network/I/O causes instead of
// matching a production error string or requiring a particular rejection type.
func downloadGUnrelatedError(err error) string {
	if err == nil {
		return ""
	}
	for _, other := range []error{context.Canceled, context.DeadlineExceeded, io.EOF, io.ErrUnexpectedEOF, io.ErrClosedPipe, io.ErrShortWrite, net.ErrClosed} {
		if errors.Is(err, other) {
			return fmt.Sprintf("context/network/I/O cause %v", other)
		}
	}
	var timed net.Error
	if errors.As(err, &timed) && timed.Timeout() {
		return "network timeout"
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return "network operation error"
	}
	var path *os.PathError
	if errors.As(err, &path) {
		return "filesystem path error"
	}
	var link *os.LinkError
	if errors.As(err, &link) {
		return "filesystem link error"
	}
	var system syscall.Errno
	if errors.As(err, &system) {
		return "system I/O error"
	}
	var wrapped *url.Error
	if errors.As(err, &wrapped) {
		if wrapped.Err == nil {
			return "URL error without a cause"
		}
		// errors.Is/As above walk the complete chain. A remaining wrapped
		// protocol error (including http.ProtocolError) is judged using the
		// actual response, not its wrapper or a fixed production error string.
	}
	return ""
}

func downloadGClearRejection(t *testing.T, err error, response downloadGReceived, responses *downloadGResponses) {
	t.Helper()
	if err == nil {
		t.Fatal("G_FIXTURE_REJECTION: no actual rejection error supplied")
	}
	for _, actual := range responses.snapshot() {
		if !actual.HasResponse && actual.cause != nil && (errors.Is(err, actual.cause) || errors.Is(actual.cause, err)) {
			t.Fatalf("G_DOWNLOAD_UNEXPECTED_ERROR: rejection error is the actual no-response transport failure, not the controlled HTTP response: %v", err)
		}
	}
	if why := downloadGUnrelatedError(err); why != "" {
		t.Fatalf("G_DOWNLOAD_UNEXPECTED_ERROR: unrelated failure cannot count as protocol rejection: %s: %T: %v", why, err, err)
	}
	if why := downloadGUnrelatedError(response.cause); why != "" {
		t.Fatalf("G_DOWNLOAD_UNEXPECTED_ERROR: target response failed outside the tested metadata contract: %s: %v", why, response.cause)
	}
	t.Logf("G_CONTROLLED_REJECTION status=%d range=%q contentRange=%q errorType=%T error=%v", response.Status, response.Range, response.ContentRange, err, err)
}

func downloadGPrepareProfile(root string) (string, error) {
	if testing.CoverMode() != "atomic" {
		return "", fmt.Errorf("optional child profiles require an atomic-instrumented binary, actual mode %q", testing.CoverMode())
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("optional profile root is not absolute: %q", root)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("optional profile root must be an existing real directory: %q", root)
	}
	// CI owns and freezes this external root. Do not create/overwrite files in
	// the checkout, and do not silently redirect a malformed root into it.
	workspace := os.Getenv("GITHUB_WORKSPACE")
	if workspace == "" {
		workspace, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(workspace) {
		return "", fmt.Errorf("source checkout path is not absolute: %q", workspace)
	}
	relative, err := filepath.Rel(workspace, root)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("optional profile root is inside the source checkout: %q", root)
	}
	return os.MkdirTemp(root, "download-g-")
}

type downloadGProcessEvidence struct {
	ParentTest             string   `json:"parentTest"`
	Top                    string   `json:"top"`
	Scenario               string   `json:"scenario"`
	Arguments              []string `json:"arguments"`
	CoverageMode           string   `json:"coverageMode"`
	Started                bool     `json:"started"`
	Exited                 bool     `json:"exited"`
	ExitCode               *int     `json:"exitCode"`
	ProcessError           string   `json:"processError"`
	Watchdog               bool     `json:"watchdog"`
	ExecutedMarker         bool     `json:"executedMarker"`
	CompletedMarker        bool     `json:"completedMarker"`
	RuntimeFailureMarkers  []string `json:"runtimeFailureMarkers"`
	OutputBytes            int      `json:"outputBytes"`
	OutputSHA256           string   `json:"outputSHA256"`
	ProfilePath            string   `json:"profilePath"`
	ProfileExists          bool     `json:"profileExists"`
	ProfileBytes           int      `json:"profileBytes"`
	ProfileSHA256          string   `json:"profileSHA256"`
	ProfileReadError       string   `json:"profileReadError"`
	ProfileStatError       string   `json:"profileStatError"`
	AtomicHeaderAndRecords bool     `json:"atomicHeaderAndRecords"`
	Classification         string   `json:"classification"`
	Limitation             string   `json:"limitation"`
}

// This records standard runtime-produced bytes only. It never rewrites profiles,
// maps paths, combines counters or supplies source/head identity from an env var.
func downloadGSaveProfile(dir, parent, top, scenario string, cmd *exec.Cmd, output []byte, processErr error, watchdog bool) error {
	e := downloadGProcessEvidence{
		ParentTest: parent, Top: top, Scenario: scenario,
		Arguments: append([]string(nil), cmd.Args...), CoverageMode: testing.CoverMode(),
		Started: cmd.Process != nil, ProcessError: downloadGError(processErr), Watchdog: watchdog,
		ExecutedMarker:  bytes.Contains(output, []byte("G_CHILD_EXECUTED "+top+"/"+scenario)),
		CompletedMarker: bytes.Contains(output, []byte("G_CHILD_BODY_DONE "+top+"/"+scenario)),
		OutputBytes:     len(output), OutputSHA256: downloadGHash(output),
		ProfilePath:    filepath.Join(dir, "child.atomic.out"),
		Classification: "failed-test-forensics-only",
		Limitation:     "Not a coverage or test PASS attestation. Exact source/binary/MVS binding and full profile integrity remain independent CI review responsibilities; counters are not merged here.",
	}
	if cmd.ProcessState != nil {
		e.Exited = cmd.ProcessState.Exited()
		code := cmd.ProcessState.ExitCode()
		e.ExitCode = &code
	}
	for _, marker := range []string{"WARNING: DATA RACE", "panic:", "fatal error:", "G_DOWNLOAD_COMPLETION_TIMEOUT", "G_FIXTURE_"} {
		if bytes.Contains(output, []byte(marker)) {
			e.RuntimeFailureMarkers = append(e.RuntimeFailureMarkers, marker)
		}
	}
	if _, statErr := os.Lstat(e.ProfilePath); statErr == nil {
		e.ProfileExists = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		e.ProfileStatError = statErr.Error()
	}
	profile, profileErr := os.ReadFile(e.ProfilePath)
	if profileErr == nil {
		e.ProfileExists = true
		e.ProfileBytes = len(profile)
		e.ProfileSHA256 = downloadGHash(profile)
		header := []byte("mode: atomic\n")
		e.AtomicHeaderAndRecords = bytes.HasPrefix(profile, header) && len(bytes.TrimSpace(bytes.TrimPrefix(profile, header))) > 0
	} else {
		e.ProfileReadError = profileErr.Error()
	}
	candidate := processErr == nil && e.Started && e.Exited && e.ExitCode != nil && *e.ExitCode == 0 && !watchdog && e.ExecutedMarker && e.CompletedMarker && len(e.RuntimeFailureMarkers) == 0 && e.ProfileExists && e.AtomicHeaderAndRecords
	if candidate {
		e.Classification = "candidate-pass-data-pending-independent-integrity-review"
	} else if watchdog || !e.Exited || len(e.RuntimeFailureMarkers) != 0 {
		e.Classification = "invalid-interrupted-or-runtime-failure-forensics-only"
	} else if !e.ExecutedMarker || (processErr == nil && !e.CompletedMarker) {
		e.Classification = "invalid-execution-identity-forensics-only"
	} else if !e.ProfileExists || !e.AtomicHeaderAndRecords {
		e.Classification = "invalid-profile-forensics-only"
	}
	var failures []error
	if err := os.WriteFile(filepath.Join(dir, "child.stdout-stderr.txt"), output, 0600); err != nil {
		failures = append(failures, err)
	}
	metadata, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		failures = append(failures, err)
	} else if err := os.WriteFile(filepath.Join(dir, "process.json"), append(metadata, '\n'), 0600); err != nil {
		failures = append(failures, err)
	}
	if processErr == nil && !candidate {
		failures = append(failures, fmt.Errorf("zero-exit child has incomplete or invalid evidence: %s", e.Classification))
	}
	return errors.Join(failures...)
}
