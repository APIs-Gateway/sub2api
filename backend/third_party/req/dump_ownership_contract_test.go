package req

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReqDumpOwnershipWindowsCalibration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-create-control.dump")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := io.WriteString(file, "real-os-create-control"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		ownedDumpWindowsDenied(t, os.Remove(path), "open os.Create control")
	} else {
		t.Log("DUMP1752_CALIBRATION: Windows denial control is not applicable on this OS")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	ownedDumpRemove(t, path)
}

func TestReqDumpOwnershipFileLifecycle(t *testing.T) {
	for _, mode := range []string{"request_sync", "request_manual", "client_sync", "client_async"} {
		t.Run(mode, func(t *testing.T) {
			server, client, calls := ownedDumpPeer(t)
			path := filepath.Join(t.TempDir(), "owned.dump")
			var owner any = client
			request := client.R().SetBodyString("owned-lifecycle-upload")
			if strings.HasPrefix(mode, "request_") {
				request.EnableDumpToFile(path)
				owner = request
			} else {
				client.EnableDumpAllToFile(path)
				if mode == "client_async" {
					client.EnableDumpAllAsync()
				}
			}
			if mode == "request_manual" {
				request.DisableAutoReadResponse()
			}
			response, err := request.Post(server.URL + "/entity")
			if err != nil || response.StatusCode != http.StatusOK || response.ProtoMajor != 1 || response.Header.Get("X-Dump-Peer") != "real-http-peer" {
				t.Fatalf("wire prerequisite: response=%v err=%v", response, err)
			}
			if mode == "request_manual" {
				body, readErr := io.ReadAll(response.Body)
				closeErr := response.Body.Close()
				if readErr != nil || closeErr != nil || string(body) != "peer:/entity:owned-lifecycle-upload" {
					t.Fatalf("manual body: %q read=%v close=%v", body, readErr, closeErr)
				}
			} else if response.String() != "peer:/entity:owned-lifecycle-upload" {
				t.Fatalf("body=%q", response.String())
			}
			ownedDumpClose(t, owner)
			data := ownedDumpRead(t, path)
			if !bytes.Contains(data, []byte("owned-lifecycle-upload")) || !bytes.Contains(data, []byte("peer:/entity:owned-lifecycle-upload")) || calls.Load() != 1 {
				t.Fatalf("actual complete exchange not captured: calls=%d output=%q", calls.Load(), data)
			}
			ownedDumpRemove(t, path)
		})
	}
}

func TestReqDumpOwnershipCloseBeforeManualBody(t *testing.T) {
	server, client, _ := ownedDumpPeer(t)
	path := filepath.Join(t.TempDir(), "manual.dump")
	type contextKey struct{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), contextKey{}, "caller-value"), 5*time.Second)
	defer cancel()
	request := client.R().SetContext(ctx).EnableDumpToFile(path).DisableAutoReadResponse().SetBodyString("manual-upload")
	response, err := request.Post(server.URL + "/entity")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("manual request failed: response=%v err=%v", response, err)
	}
	defer response.Body.Close()
	ownedDumpClose(t, request)
	before := ownedDumpRead(t, path)
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "peer:/entity:manual-upload" {
		t.Fatalf("CloseDump altered actual manual body: %q %v", body, err)
	}
	if !bytes.Equal(before, ownedDumpRead(t, path)) {
		t.Fatal("late body callback wrote after CloseDump")
	}
	if request.Context().Value(contextKey{}) != "caller-value" {
		t.Fatal("CloseDump lost caller context value")
	}
	if got, ok := request.Context().Deadline(); !ok || got.IsZero() {
		t.Fatal("CloseDump lost caller deadline")
	}
	cancel()
	if !errors.Is(request.Context().Err(), context.Canceled) {
		t.Fatal("CloseDump lost caller cancellation")
	}
	ownedDumpRemove(t, path)
}

func TestReqDumpOwnershipCloseAndReconfigure(t *testing.T) {
	for _, mode := range []string{"request", "client"} {
		t.Run(mode, func(t *testing.T) {
			server, client, _ := ownedDumpPeer(t)
			path := filepath.Join(t.TempDir(), "generation.dump")
			request := client.R()
			var owner any = client
			if mode == "request" {
				request.EnableDumpToFile(path)
				owner = request
			} else {
				client.EnableDumpAllToFile(path)
			}
			response, err := request.SetBodyString("generation-one").Post(server.URL + "/entity")
			if err != nil || response.String() != "peer:/entity:generation-one" {
				t.Fatalf("first exchange: response=%v err=%v", response, err)
			}
			ownedDumpClose(t, owner)
			before := ownedDumpRead(t, path)
			if mode == "request" {
				request.EnableDump()
			} else {
				client.DisableDumpAll().EnableDumpAll()
			}
			response, err = request.SetBodyString("must-not-reopen").Post(server.URL + "/entity")
			if err != nil || response.String() != "peer:/entity:must-not-reopen" || !bytes.Equal(before, ownedDumpRead(t, path)) {
				t.Fatalf("bare enable reopened dump or broke request: response=%v err=%v", response, err)
			}
			if mode == "request" {
				request.EnableDumpToFile(path)
			} else {
				client.EnableDumpAllToFile(path)
			}
			response, err = request.SetBodyString("generation-two").Post(server.URL + "/entity")
			if err != nil || response.String() != "peer:/entity:generation-two" {
				t.Fatalf("new generation exchange: response=%v err=%v", response, err)
			}
			ownedDumpClose(t, owner)
			data := ownedDumpRead(t, path)
			if !bytes.Contains(data, []byte("generation-two")) || bytes.Contains(data, []byte("generation-one")) || bytes.Contains(data, []byte("must-not-reopen")) {
				t.Fatalf("new generation not isolated: %q", data)
			}
			ownedDumpRemove(t, path)
		})
	}
}

func TestReqDumpOwnershipCloneLeases(t *testing.T) {
	for _, mode := range []string{"original_first", "clone_first", "disabled_clone", "transport_clone"} {
		t.Run(mode, func(t *testing.T) {
			server, client, _ := ownedDumpPeer(t)
			path := filepath.Join(t.TempDir(), "clones.dump")
			client.EnableDumpAllToFile(path)
			ownedDumpPost(t, client, server.URL+"/entity", "before-clone")
			if mode == "disabled_clone" {
				client.DisableDumpAll()
			}
			clone := client.Clone()
			t.Cleanup(clone.GetTransport().CloseIdleConnections)
			t.Cleanup(func() { ownedDumpClose(t, clone) })
			if mode == "transport_clone" {
				// A bare snapshot has no owner Close entry point. Stopping its
				// independent queue must not leave an unreachable file lease.
				snapshot := client.GetTransport().Dump.Clone()
				snapshot.Stop()
				// Exercise the direct public Transport.Clone, rather than only
				// inheriting the embedded call in Client.Clone.
				ownedDumpClose(t, clone)
				clone = C()
				clone.Transport = client.GetTransport().Clone()
				clone.GetClient().Transport = clone.Transport
			}
			t.Cleanup(clone.GetTransport().CloseIdleConnections)
			if mode == "disabled_clone" {
				clone.EnableDumpAll()
			}
			first, remaining := client, clone
			if mode == "clone_first" {
				first, remaining = clone, client
			}
			ownedDumpClose(t, first)
			ownedDumpRetained(t, path)
			ownedDumpPost(t, remaining, server.URL+"/entity", "remaining-owner")
			ownedDumpClose(t, remaining)
			data := ownedDumpRead(t, path)
			if !bytes.Contains(data, []byte("before-clone")) || !bytes.Contains(data, []byte("remaining-owner")) {
				t.Fatalf("independent clone lost shared output: %q", data)
			}
			ownedDumpRemove(t, path)
		})
	}
}

func TestReqDumpOwnershipDisableRetainsFile(t *testing.T) {
	server, client, _ := ownedDumpPeer(t)
	path := filepath.Join(t.TempDir(), "disabled.dump")
	client.EnableDumpAllToFile(path)
	ownedDumpPost(t, client, server.URL+"/entity", "before-disable")
	client.DisableDumpAll()
	before := ownedDumpRead(t, path)
	ownedDumpRetained(t, path)
	ownedDumpPost(t, client, server.URL+"/entity", "disabled-exchange")
	if !bytes.Equal(before, ownedDumpRead(t, path)) {
		t.Fatal("disabled dumper still wrote")
	}
	client.EnableDumpAll()
	ownedDumpPost(t, client, server.URL+"/entity", "after-enable")
	ownedDumpClose(t, client)
	data := ownedDumpRead(t, path)
	if !bytes.HasPrefix(data, before) || !bytes.Contains(data, []byte("after-enable")) || bytes.Contains(data, []byte("disabled-exchange")) {
		t.Fatalf("disable/enable reopened/truncated or admitted disabled writes: %q", data)
	}
	ownedDumpRemove(t, path)
}

func TestReqDumpOwnershipConfiguredNeverSent(t *testing.T) {
	for _, mode := range []string{"request", "client"} {
		t.Run(mode, func(t *testing.T) {
			_, client, calls := ownedDumpPeer(t)
			path := filepath.Join(t.TempDir(), "never-sent.dump")
			var owner any = client
			if mode == "request" {
				owner = client.R().EnableDumpToFile(path)
			} else {
				client.EnableDumpAllToFile(path)
			}
			ownedDumpClose(t, owner)
			if len(ownedDumpRead(t, path)) != 0 || calls.Load() != 0 {
				t.Fatal("unsent configuration unexpectedly performed I/O to peer")
			}
			ownedDumpRemove(t, path)
		})
	}
}

func TestReqDumpOwnershipCloseDrains(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_%t", async), func(t *testing.T) {
			server, client, _ := ownedDumpPeer(t)
			gate := &ownedDumpGate{entered: make(chan struct{}), release: make(chan struct{})}
			var openOnce sync.Once
			open := func() { openOnce.Do(func() { close(gate.release) }) }
			defer open()
			// Only body output is gated: Do returns with the real manual body.
			client.SetCommonDumpOptions(&DumpOptions{Output: io.Discard, ResponseBodyOutput: gate, ResponseBody: true, Async: async}).EnableDumpAll()
			response, err := client.R().DisableAutoReadResponse().Get(server.URL + "/entity")
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("manual response=%v err=%v", response, err)
			}
			defer response.Body.Close()
			readDone := make(chan struct{})
			var body []byte
			var readErr error
			go func() {
				defer close(readDone)
				body, readErr = io.ReadAll(response.Body)
			}()
			reqDumpWait(t, gate.entered, "real body output entering caller writer")
			closed := make(chan struct{})
			var closeErr error
			go func() {
				defer close(closed)
				if closer, ok := any(client).(interface{ CloseDump() error }); ok {
					closeErr = closer.CloseDump()
				}
			}()
			select {
			case <-closed:
				t.Fatal("CloseDump returned while an admitted actual write was blocked")
			case <-time.After(100 * time.Millisecond):
			}
			open()
			reqDumpWait(t, closed, "CloseDump draining actual body write")
			reqDumpWait(t, readDone, "actual manual body EOF")
			gate.mu.Lock()
			captured, callerClosed := gate.body.String(), gate.closed
			gate.mu.Unlock()
			if closeErr != nil || readErr != nil || string(body) != "peer:/entity:" || !strings.Contains(captured, "peer:/entity:") || callerClosed {
				t.Fatalf("drain/body/caller ownership: close=%v read=%v body=%q dump=%q callerClosed=%v", closeErr, readErr, body, captured, callerClosed)
			}
		})
	}
}

func TestReqDumpOwnershipCallerFiles(t *testing.T) {
	for _, mode := range []string{"request", "client", "transport"} {
		t.Run(mode, func(t *testing.T) {
			server, client, _ := ownedDumpPeer(t)
			path := filepath.Join(t.TempDir(), "caller.dump")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			request := client.R().SetBodyString("caller-upload")
			var owner any = client
			if mode == "request" {
				request.EnableDumpTo(file)
				owner = request
			} else if mode == "transport" {
				client.GetTransport().EnableDump(&DumpOptions{Output: file, RequestHeader: true, RequestBody: true, ResponseHeader: true, ResponseBody: true})
				owner = client.GetTransport()
			} else {
				client.EnableDumpAllTo(file)
			}
			response, err := request.Post(server.URL + "/entity")
			if err != nil || response.String() != "peer:/entity:caller-upload" {
				t.Fatalf("caller writer exchange: response=%v err=%v", response, err)
			}
			ownedDumpClose(t, owner)
			if _, err := io.WriteString(file, "caller-still-open"); err != nil {
				t.Fatalf("CloseDump closed caller file: %v", err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(ownedDumpRead(t, path), []byte("caller-still-open")) {
				t.Fatal("caller append missing")
			}
			ownedDumpRemove(t, path)
		})
	}
}

func TestReqDumpOwnershipRetarget(t *testing.T) {
	for _, mode := range []string{"file_to_file", "file_to_caller", "same_path", "hardlink_with_clone", "failed_client_open", "failed_request_open"} {
		t.Run(mode, func(t *testing.T) {
			server, client, _ := ownedDumpPeer(t)
			directory := t.TempDir()
			path, nextPath := filepath.Join(directory, "first.dump"), filepath.Join(directory, "second.dump")
			if mode == "failed_request_open" {
				request := client.R().EnableDumpToFile(path).DisableAutoReadResponse().SetBodyString("before-failed-open")
				response, err := request.Post(server.URL + "/entity")
				if err != nil || response.StatusCode != http.StatusOK {
					t.Fatalf("manual exchange: response=%v err=%v", response, err)
				}
				defer response.Body.Close()
				request.EnableDumpToFile(directory)
				// Existing setter errors remain preparation errors. The already
				// returned real body must still write to the prior file/scope.
				failed, failedErr := request.Get(server.URL + "/entity")
				var pathErr *os.PathError
				if failedErr == nil || failed == nil || !errors.As(failedErr, &pathErr) {
					t.Fatalf("failed setter lost existing error channel: %v", failedErr)
				}
				body, readErr := io.ReadAll(response.Body)
				if readErr != nil || string(body) != "peer:/entity:before-failed-open" {
					t.Fatalf("prior manual body: %q %v", body, readErr)
				}
				ownedDumpClose(t, request)
				if !bytes.Contains(ownedDumpRead(t, path), body) {
					t.Fatal("failed candidate retired the prior file/options")
				}
				ownedDumpRemove(t, path)
				return
			}
			client.EnableDumpAllToFile(path)
			ownedDumpPost(t, client, server.URL+"/entity", "old-file-marker")
			before := ownedDumpRead(t, path)
			var caller bytes.Buffer
			var clone *Client
			switch mode {
			case "file_to_caller":
				client.EnableDumpAllTo(&caller)
			case "same_path":
				nextPath = path
				client.EnableDumpAllToFile(nextPath)
			case "hardlink_with_clone":
				clone = client.Clone()
				t.Cleanup(clone.GetTransport().CloseIdleConnections)
				t.Cleanup(func() { ownedDumpClose(t, clone) })
				if err := os.Link(path, nextPath); err != nil {
					t.Fatalf("same-filesystem real hardlink prerequisite: %v", err)
				}
				client.EnableDumpAllToFile(nextPath)
				if !bytes.Equal(before, ownedDumpRead(t, path)) {
					t.Fatal("same-file retarget truncated a clone's retained file")
				}
			case "failed_client_open":
				client.EnableDumpAllToFile(directory)
			default:
				client.EnableDumpAllToFile(nextPath)
			}
			ownedDumpPost(t, client, server.URL+"/entity", "new-file-marker")
			ownedDumpClose(t, client)
			if clone != nil {
				ownedDumpRetained(t, path)
				ownedDumpPost(t, clone, server.URL+"/entity", "clone-after-retarget")
				ownedDumpClose(t, clone)
			}
			data := ownedDumpRead(t, path)
			switch mode {
			case "file_to_caller":
				if !bytes.Equal(before, data) || !strings.Contains(caller.String(), "new-file-marker") {
					t.Fatalf("file/caller retarget output: file=%q caller=%q", data, caller.String())
				}
			case "same_path":
				if bytes.Contains(data, []byte("old-file-marker")) || !bytes.Contains(data, []byte("new-file-marker")) {
					t.Fatalf("sole-owner same-file generation did not restart: %q", data)
				}
			case "hardlink_with_clone":
				if !bytes.HasPrefix(data, before) || !bytes.Contains(data, []byte("new-file-marker")) || !bytes.Contains(data, []byte("clone-after-retarget")) || !bytes.Equal(data, ownedDumpRead(t, nextPath)) {
					t.Fatalf("alias/clone file identity lost: %q", data)
				}
				ownedDumpRemove(t, nextPath)
			case "failed_client_open":
				if !bytes.HasPrefix(data, before) || !bytes.Contains(data, []byte("new-file-marker")) {
					t.Fatalf("failed candidate changed prior output: %q", data)
				}
			default:
				if !bytes.Equal(before, data) {
					t.Fatal("retired file received new-generation writes")
				}
				if next := ownedDumpRead(t, nextPath); !bytes.Contains(next, []byte("new-file-marker")) || bytes.Contains(next, []byte("old-file-marker")) {
					t.Fatalf("new target contents: %q", next)
				}
				ownedDumpRemove(t, nextPath)
			}
			ownedDumpRemove(t, path)
		})
	}
}

func TestReqDumpOwnershipOptionsRetarget(t *testing.T) {
	for _, mode := range []string{"request", "client", "transport"} {
		t.Run(mode, func(t *testing.T) {
			server, client, _ := ownedDumpPeer(t)
			path := filepath.Join(t.TempDir(), "options.dump")
			request := client.R().SetBodyString("old-options")
			var owner any = client
			if mode == "request" {
				request.EnableDumpToFile(path)
				owner = request
			} else {
				client.EnableDumpAllToFile(path)
			}
			response, err := request.Post(server.URL + "/entity")
			if err != nil || response.String() != "peer:/entity:old-options" {
				t.Fatalf("first exchange: response=%v err=%v", response, err)
			}
			before := ownedDumpRead(t, path)
			var output bytes.Buffer
			options := &DumpOptions{Output: &output, RequestHeader: true, RequestBody: true, ResponseHeader: true, ResponseBody: true}
			switch mode {
			case "request":
				request.SetDumpOptions(options)
			case "transport":
				client.GetTransport().EnableDump(options)
				owner = client.GetTransport()
			default:
				client.SetCommonDumpOptions(options)
			}
			response, err = request.SetBodyString("new-options").Post(server.URL + "/entity")
			if err != nil || response.String() != "peer:/entity:new-options" {
				t.Fatalf("options exchange: response=%v err=%v", response, err)
			}
			ownedDumpClose(t, owner)
			if !bytes.Equal(before, ownedDumpRead(t, path)) || !strings.Contains(output.String(), "new-options") {
				t.Fatalf("options did not retarget actual output: %q", output.String())
			}
			ownedDumpRemove(t, path)
		})
	}
}

func TestReqDumpOwnershipRetryAndReuse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || r.ProtoMajor != 1 || string(body) != "retry-owned-upload" {
			http.Error(w, "retry wire prerequisite", http.StatusBadRequest)
			return
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "retry-first-response")
			return
		}
		io.WriteString(w, "retry-complete-response")
	}))
	defer server.Close()
	client := C().EnableForceHTTP1().SetTimeout(5 * time.Second)
	client.GetTransport().SetProxy(nil)
	defer client.GetTransport().CloseIdleConnections()
	path := filepath.Join(t.TempDir(), "retry.dump")
	request := client.R().EnableDumpToFile(path).SetBodyString("retry-owned-upload").SetRetryCount(1).
		SetRetryFixedInterval(0).SetRetryCondition(func(response *Response, err error) bool {
		return err == nil && response.StatusCode == http.StatusServiceUnavailable
	})
	for invocation := 0; invocation < 2; invocation++ {
		response, err := request.Post(server.URL)
		if err != nil || response.StatusCode != http.StatusOK || response.String() != "retry-complete-response" {
			t.Fatalf("retry/reuse=%d response=%v err=%v", invocation, response, err)
		}
	}
	ownedDumpClose(t, request)
	data := ownedDumpRead(t, path)
	if calls.Load() != 3 || strings.Count(string(data), "retry-owned-upload") != 3 || !bytes.Contains(data, []byte("retry-first-response")) || strings.Count(string(data), "retry-complete-response") != 2 {
		t.Fatalf("retry/reuse ownership or actual attempts lost: calls=%d output=%q", calls.Load(), data)
	}
	// A leaked ref per attempt would keep this file open after configuration Close.
	ownedDumpRemove(t, path)
}

func TestReqDumpOwnershipCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "before-cancellation")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := C().EnableForceHTTP1().SetTimeout(5 * time.Second).SetCommonRetryCount(0)
	client.GetTransport().SetProxy(nil)
	defer client.GetTransport().CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "cancel.dump")
	request := client.R().SetContext(ctx).EnableDumpToFile(path).DisableAutoReadResponse()
	response, err := request.Get(server.URL)
	if err != nil || response.StatusCode != http.StatusOK || response.ProtoMajor != 1 {
		t.Fatalf("cancel prerequisite: response=%v err=%v", response, err)
	}
	defer response.Body.Close()
	prefix := make([]byte, len("before-cancellation"))
	if _, err := io.ReadFull(response.Body, prefix); err != nil || string(prefix) != "before-cancellation" {
		t.Fatalf("actual flushed prefix=%q error=%v", prefix, err)
	}
	ownedDumpClose(t, request)
	before := ownedDumpRead(t, path)
	cancel()
	_, readErr := io.ReadAll(response.Body)
	if !errors.Is(readErr, context.Canceled) || !errors.Is(request.Context().Err(), context.Canceled) || !bytes.Equal(before, ownedDumpRead(t, path)) {
		t.Fatalf("CloseDump changed cancellation/body or admitted late output: %v", readErr)
	}
	ownedDumpRemove(t, path)
}

func TestReqDumpOwnershipParallelRecoveryClone(t *testing.T) {
	const entity = "owned-dump-parallel-recovery-entity"
	var ranged, full atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			http.Error(w, "HTTP1 prerequisite", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(entity)))
		if r.Method == http.MethodHead {
			return
		}
		if r.Header.Get("Range") != "" {
			ranged.Add(1)
		} else {
			full.Add(1)
		}
		io.WriteString(w, entity) // Actual ordinary server ignores Range.
	}))
	defer server.Close()
	client := C().EnableForceHTTP1().SetTimeout(5 * time.Second).SetCommonRetryCount(0)
	client.GetTransport().SetProxy(nil)
	defer client.GetTransport().CloseIdleConnections()
	client.SetCommonHeader("Range", "bytes=0-0").SetCommonHeader("If-Range", "caller-validator")
	path := filepath.Join(t.TempDir(), "parallel.dump")
	client.EnableDumpAllToFile(path).EnableDumpAllAsync()
	var output bytes.Buffer
	err := client.NewParallelDownload(server.URL).SetConcurrency(1).SetSegmentSize(7).
		SetTempRootDir(t.TempDir()).SetOutput(&output).Do(context.Background())
	if err != nil || output.String() != entity || sha256.Sum256(output.Bytes()) != sha256.Sum256([]byte(entity)) || ranged.Load() == 0 || full.Load() != 1 {
		t.Fatalf("real full recovery: err=%v output=%q ranged=%d full=%d", err, output.String(), ranged.Load(), full.Load())
	}
	if client.Headers.Get("Range") != "bytes=0-0" || client.Headers.Get("If-Range") != "caller-validator" {
		t.Fatal("recovery modified caller configuration")
	}
	// The private clone must be gone, but the original scope remains writable.
	response := client.Get(server.URL).Do()
	if response.Err != nil || response.String() != entity {
		t.Fatalf("caller after recovery: response=%v err=%v", response, response.Err)
	}
	ownedDumpClose(t, client)
	if !bytes.Contains(ownedDumpRead(t, path), []byte(entity)) {
		t.Fatal("recovery/caller dump did not capture real entity")
	}
	// Windows detects a private clone lease leaked beyond full recovery.
	ownedDumpRemove(t, path)
}

func TestReqDumpOwnershipManualSuffixAfterDo(t *testing.T) {
	suffix := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(suffix) }) }
	defer open()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "manual-prefix:")
		w.(http.Flusher).Flush()
		select {
		case <-suffix:
			io.WriteString(w, "suffix-after-Do")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	client := C().EnableForceHTTP1().SetTimeout(5 * time.Second).SetCommonRetryCount(0)
	client.GetTransport().SetProxy(nil)
	defer client.GetTransport().CloseIdleConnections()
	path := filepath.Join(t.TempDir(), "late-suffix.dump")
	request := client.R().EnableDumpToFile(path).DisableAutoReadResponse()
	response, err := request.Get(server.URL)
	if err != nil || response.StatusCode != http.StatusOK || response.ProtoMajor != 1 {
		t.Fatalf("manual prefix response=%v error=%v", response, err)
	}
	defer response.Body.Close()
	prefix := make([]byte, len("manual-prefix:"))
	if _, err := io.ReadFull(response.Body, prefix); err != nil || string(prefix) != "manual-prefix:" {
		t.Fatalf("actual prefix=%q error=%v", prefix, err)
	}
	before := ownedDumpRead(t, path)
	if !bytes.Contains(before, prefix) || bytes.Contains(before, []byte("suffix-after-Do")) {
		t.Fatalf("manual suffix barrier prerequisite: %q", before)
	}
	open()
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil || string(body) != "suffix-after-Do" {
		t.Fatalf("actual suffix=%q error=%v", body, readErr)
	}
	ownedDumpClose(t, request)
	data := ownedDumpRead(t, path)
	if !bytes.Contains(data, []byte("manual-prefix:suffix-after-Do")) {
		t.Fatalf("dump did not survive Do until explicit close: %q", data)
	}
	ownedDumpRemove(t, path)
}

func TestReqDumpOwnershipRetargetRetiresCallbacks(t *testing.T) {
	for _, mode := range []string{"request", "client"} {
		t.Run(mode, func(t *testing.T) {
			server, client, _ := ownedDumpPeer(t)
			directory := t.TempDir()
			firstPath, secondPath := filepath.Join(directory, "retired.dump"), filepath.Join(directory, "current.dump")
			request := client.R().SetBodyString("old-callback-upload").DisableAutoReadResponse()
			var owner any = client
			if mode == "request" {
				request.EnableDumpToFile(firstPath)
				owner = request
			} else {
				client.EnableDumpAllToFile(firstPath)
			}
			old, err := request.Post(server.URL + "/entity")
			if err != nil || old.StatusCode != http.StatusOK {
				t.Fatalf("old callback response=%v err=%v", old, err)
			}
			defer old.Body.Close()
			if mode == "request" {
				request.EnableDumpToFile(secondPath)
			} else {
				client.EnableDumpAllToFile(secondPath)
			}
			before := ownedDumpRead(t, firstPath)
			body, err := io.ReadAll(old.Body)
			if err != nil || string(body) != "peer:/entity:old-callback-upload" {
				t.Fatalf("retarget changed old body: %q %v", body, err)
			}
			request.SetBodyString("current-callback-upload")
			current, err := request.Post(server.URL + "/entity")
			if err != nil || current.StatusCode != http.StatusOK {
				t.Fatalf("new callback response=%v err=%v", current, err)
			}
			defer current.Body.Close()
			body, err = io.ReadAll(current.Body)
			if err != nil || string(body) != "peer:/entity:current-callback-upload" {
				t.Fatalf("new callback body: %q %v", body, err)
			}
			ownedDumpClose(t, owner)
			if !bytes.Equal(before, ownedDumpRead(t, firstPath)) {
				t.Fatal("retired callback wrote old file after retarget")
			}
			data := ownedDumpRead(t, secondPath)
			if bytes.Contains(data, []byte("old-callback-upload")) || !bytes.Contains(data, []byte("current-callback-upload")) {
				t.Fatalf("retired callback contaminated current target: %q", data)
			}
			ownedDumpRemove(t, firstPath)
			ownedDumpRemove(t, secondPath)
		})
	}
}

func TestReqDumpOwnershipMultipleOutputs(t *testing.T) {
	server, client, _ := ownedDumpPeer(t)
	directory := t.TempDir()
	path, nextPath := filepath.Join(directory, "response-body.dump"), filepath.Join(directory, "headers.dump")
	client.EnableDumpAllToFile(path)
	ownedDumpPost(t, client, server.URL+"/entity", "before-multiple-output")
	ownedFile := client.GetTransport().Dump.Output()
	client.SetCommonDumpOptions(&DumpOptions{Output: ownedFile, ResponseBodyOutput: ownedFile,
		RequestHeader: true, ResponseHeader: true, ResponseBody: true})
	client.EnableDumpAllToFile(nextPath)
	ownedDumpPost(t, client, server.URL+"/entity", "multiple-output-upload")
	ownedDumpClose(t, client)
	if data := ownedDumpRead(t, path); !bytes.Contains(data, []byte("peer:/entity:multiple-output-upload")) {
		t.Fatalf("retained owned per-field writer lost lease: %q", data)
	}
	if data := ownedDumpRead(t, nextPath); !bytes.Contains(data, []byte("POST /entity")) || bytes.Contains(data, []byte("peer:/entity:multiple-output-upload")) {
		t.Fatalf("default vs per-field outputs not preserved: %q", data)
	}
	ownedDumpRemove(t, path)
	ownedDumpRemove(t, nextPath)
}

func TestReqDumpOwnershipCurrentFileIdentity(t *testing.T) {
	server, client, _ := ownedDumpPeer(t)
	directory := t.TempDir()
	path, movedPath := filepath.Join(directory, "current-path.dump"), filepath.Join(directory, "actual-open-file.dump")
	client.EnableDumpAllToFile(path)
	ownedDumpPost(t, client, server.URL+"/entity", "before-path-change")
	if err := os.Rename(path, movedPath); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		ownedDumpWindowsRenameDenied(t, err)
		ownedDumpClose(t, client)
		ownedDumpRemove(t, path)
		return
	}
	sentinel := []byte("caller replacement at old filename")
	if err := os.WriteFile(path, sentinel, 0600); err != nil {
		t.Fatal(err)
	}
	ownedDumpPost(t, client, server.URL+"/entity", "after-path-change")
	ownedDumpClose(t, client)
	if !bytes.Equal(ownedDumpRead(t, path), sentinel) {
		t.Fatal("dump reopened caller replacement by pathname")
	}
	data := ownedDumpRead(t, movedPath)
	if !bytes.Contains(data, []byte("before-path-change")) || !bytes.Contains(data, []byte("after-path-change")) {
		t.Fatalf("dump lost opened file identity: %q", data)
	}
	ownedDumpRemove(t, path)
	ownedDumpRemove(t, movedPath)
}

func TestReqDumpOwnershipConcurrentClose(t *testing.T) {
	for _, mode := range []string{"request", "client", "transport"} {
		t.Run(mode, func(t *testing.T) {
			server, client, _ := ownedDumpPeer(t)
			path := filepath.Join(t.TempDir(), "concurrent-close.dump")
			var owner any = client
			if mode == "request" {
				request := client.R().EnableDumpToFile(path).SetBodyString("before-concurrent-close")
				response, err := request.Post(server.URL + "/entity")
				if err != nil || response.String() != "peer:/entity:before-concurrent-close" {
					t.Fatalf("wire response=%v error=%v", response, err)
				}
				owner = request
			} else {
				client.EnableDumpAllToFile(path)
				ownedDumpPost(t, client, server.URL+"/entity", "before-concurrent-close")
				if mode == "transport" {
					owner = client.GetTransport()
				}
			}
			closer, available := owner.(interface{ CloseDump() error })
			const closers = 16
			errorsSeen := make(chan error, closers)
			start := make(chan struct{})
			for i := 0; i < closers; i++ {
				go func() {
					<-start
					if available {
						errorsSeen <- closer.CloseDump()
					} else {
						errorsSeen <- nil
					}
				}()
			}
			close(start)
			for i := 0; i < closers; i++ {
				select {
				case err := <-errorsSeen:
					if err != nil {
						t.Fatalf("concurrent CloseDump=%v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("concurrent CloseDump did not finish")
				}
			}
			if !bytes.Contains(ownedDumpRead(t, path), []byte("before-concurrent-close")) {
				t.Fatal("concurrent close lost accepted exchange")
			}
			ownedDumpRemove(t, path)
		})
	}
}

func TestReqDumpOwnershipBodyError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, "actual-short-body")
	}))
	defer server.Close()
	client := C().EnableForceHTTP1().SetTimeout(5 * time.Second).SetCommonRetryCount(0)
	client.GetTransport().SetProxy(nil)
	defer client.GetTransport().CloseIdleConnections()
	path := filepath.Join(t.TempDir(), "body-error.dump")
	request := client.R().EnableDumpToFile(path).DisableAutoReadResponse()
	response, err := request.Get(server.URL)
	if err != nil || response.StatusCode != http.StatusOK || response.ProtoMajor != 1 {
		t.Fatalf("short body headers: response=%v error=%v", response, err)
	}
	defer response.Body.Close()
	ownedDumpClose(t, request)
	before := ownedDumpRead(t, path)
	body, readErr := io.ReadAll(response.Body)
	if string(body) != "actual-short-body" || !errors.Is(readErr, io.ErrUnexpectedEOF) || !bytes.Equal(before, ownedDumpRead(t, path)) {
		t.Fatalf("CloseDump changed actual body failure: body=%q error=%v", body, readErr)
	}
	ownedDumpRemove(t, path)
}

func TestReqDumpOwnershipRetargetKeepsOtherClone(t *testing.T) {
	server, client, _ := ownedDumpPeer(t)
	directory := t.TempDir()
	firstPath, nextPath := filepath.Join(directory, "clone-original.dump"), filepath.Join(directory, "new-target.dump")
	client.EnableDumpAllToFile(firstPath)
	ownedDumpPost(t, client, server.URL+"/entity", "shared-before-retarget")
	clone := client.Clone()
	defer clone.GetTransport().CloseIdleConnections()
	defer ownedDumpClose(t, clone)
	client.EnableDumpAllToFile(nextPath)
	ownedDumpPost(t, client, server.URL+"/entity", "origin-new-target")
	ownedDumpClose(t, client)
	ownedDumpRetained(t, firstPath)
	ownedDumpPost(t, clone, server.URL+"/entity", "clone-old-target")
	ownedDumpClose(t, clone)
	first, next := ownedDumpRead(t, firstPath), ownedDumpRead(t, nextPath)
	if !bytes.Contains(first, []byte("shared-before-retarget")) || !bytes.Contains(first, []byte("clone-old-target")) || bytes.Contains(first, []byte("origin-new-target")) ||
		!bytes.Contains(next, []byte("origin-new-target")) || bytes.Contains(next, []byte("clone-old-target")) {
		t.Fatalf("retarget changed another clone's file: first=%q next=%q", first, next)
	}
	ownedDumpRemove(t, firstPath)
	ownedDumpRemove(t, nextPath)
}
