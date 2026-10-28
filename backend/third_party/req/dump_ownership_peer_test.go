package req

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// This same fixture compiles on OLD. Absence of the additive API is diagnostic,
// never a substitute for real wire/body/output and same-process resource checks.
func ownedDumpClose(t *testing.T, owner any) {
	t.Helper()
	closer, ok := owner.(interface{ CloseDump() error })
	if !ok {
		t.Log("DUMP1752_CAPABILITY: CloseDump absent; continue actual resource oracle")
		return
	}
	if err := closer.CloseDump(); err != nil {
		t.Fatalf("DUMP1752_CLOSE: %v", err)
	}
	if err := closer.CloseDump(); err != nil {
		t.Fatalf("DUMP1752_REPEAT_CLOSE: %v", err)
	}
}

func ownedDumpRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("DUMP1752_OUTPUT bytes=%d sha256=%x", len(data), sha256.Sum256(data))
	return data
}

func ownedDumpRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("DUMP1752_RESOURCE_REMOVE: %T %v", err, err)
	}
	if runtime.GOOS != "windows" {
		t.Log("DUMP1752_RESOURCE: unlink success is not proof of descriptor close on this OS")
	}
}

func ownedDumpRetained(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
		return
	}
	ownedDumpWindowsDenied(t, os.Remove(path), "retained owner")
}

func ownedDumpWindowsDenied(t *testing.T, err error, label string) {
	t.Helper()
	var pathErr *os.PathError
	var errno syscall.Errno
	if !errors.As(err, &pathErr) || !errors.As(err, &errno) || (errno != 5 && errno != 32) {
		t.Fatalf("DUMP1752_CALIBRATION %s: want Windows access/sharing denial, got %T %v", label, err, err)
	}
	t.Logf("DUMP1752_RESOURCE_DENIED %s: type=%T operation=%s errno=%d", label, err, pathErr.Op, errno)
}

func ownedDumpWindowsRenameDenied(t *testing.T, err error) {
	t.Helper()
	var linkErr *os.LinkError
	var errno syscall.Errno
	if !errors.As(err, &linkErr) || !errors.As(err, &errno) || (errno != 5 && errno != 32) {
		t.Fatalf("DUMP1752_RENAME_CALIBRATION: want Windows access/sharing denial, got %T %v", err, err)
	}
	t.Logf("DUMP1752_RENAME_DENIED: type=%T operation=%s errno=%d", err, linkErr.Op, errno)
}

func ownedDumpPeer(t *testing.T) (*httptest.Server, *Client, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || r.ProtoMajor != 1 {
			http.Error(w, "wire prerequisite failed", http.StatusBadRequest)
			return
		}
		calls.Add(1)
		w.Header().Set("X-Dump-Peer", "real-http-peer")
		fmt.Fprintf(w, "peer:%s:%s", r.URL.Path, body)
	}))
	t.Cleanup(server.Close)
	client := C().EnableForceHTTP1().SetTimeout(5 * time.Second).SetCommonRetryCount(0)
	client.GetTransport().SetProxy(nil)
	t.Cleanup(client.GetTransport().CloseIdleConnections)
	t.Cleanup(func() { ownedDumpClose(t, client) })
	return server, client, calls
}

func ownedDumpPost(t *testing.T, client *Client, endpoint, marker string) {
	t.Helper()
	response, err := client.R().SetBodyString(marker).Post(endpoint)
	if err != nil || response.StatusCode != http.StatusOK || response.ProtoMajor != 1 ||
		response.Header.Get("X-Dump-Peer") != "real-http-peer" || response.String() != "peer:/entity:"+marker {
		t.Fatalf("DUMP1752_WIRE: response=%v error=%v", response, err)
	}
}

// This is a caller writer even though it implements Close. CloseDump must drain
// admitted writes, while never invoking this Close method.
type ownedDumpGate struct {
	mu      sync.Mutex
	body    bytes.Buffer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	closed  bool
}

func (w *ownedDumpGate) Write(p []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}

func (w *ownedDumpGate) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return errors.New("caller Close must not run")
}
