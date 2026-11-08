package req_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Windows denies renaming an open os.OpenFile destination. A Linux rename or
// a /proc descriptor count would not prove this public file-lifecycle contract.
func TestReqParallelDownloadWindowsClose(t *testing.T) {
	t.Run("owned_output_released", func(t *testing.T) {
		downloadGIsolated(t, "TestReqParallelDownloadWindowsClose", "owned_output_released", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			entity := downloadGEntity(37)
			root := downloadGTempDir(t)
			control := filepath.Join(root, "open-control.bin")
			controlHandle, err := os.OpenFile(control, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer controlHandle.Close()
			movedControl := filepath.Join(root, "renamed-control.bin")
			openRenameErr := os.Rename(control, movedControl)
			t.Logf("G_WINDOWS_OPEN_CONTROL renameError=%v", openRenameErr)
			if openRenameErr == nil {
				t.Fatal("G_FIXTURE_WINDOWS: same-mode open control could be renamed; this filesystem cannot prove the intended Close witness")
			}
			if err := controlHandle.Close(); err != nil {
				t.Fatalf("G_FIXTURE_WINDOWS: control Close failed: %v", err)
			}
			if err := os.Rename(control, movedControl); err != nil {
				t.Fatalf("G_FIXTURE_WINDOWS: same-mode closed control could not be renamed: %v", err)
			}
			if err := os.Remove(movedControl); err != nil {
				t.Fatalf("G_FIXTURE_WINDOWS: same-mode closed control could not be removed: %v", err)
			}
			t.Log("G_WINDOWS_CLOSED_CONTROL rename/remove completed")
			p := newDownloadGPeer(t, func(w http.ResponseWriter, r *http.Request, e *downloadGExchange) {
				if r.Method == http.MethodHead {
					downloadGReply(w, e, http.StatusOK, "", nil, len(entity))
					return
				}
				start, end, err := downloadGRange(e.Range, len(entity))
				if err != nil {
					e.FixtureError = err.Error()
					return
				}
				downloadGReply(w, e, http.StatusPartialContent, fmt.Sprintf("bytes %d-%d/%d", start, end, len(entity)), entity[start:end+1], end-start+1)
			})
			c := downloadGClient(t)
			filename := filepath.Join(root, "downloaded.bin")
			result := make(chan error, 1)
			temporarySegments := downloadGTempDir(t)
			go func() {
				result <- c.NewParallelDownload(p.server.URL + "/entity").SetOutputFile(filename).SetFileMode(0600).SetSegmentSize(11).SetConcurrency(2).SetTempRootDir(temporarySegments).Do(ctx)
			}()
			head := p.await(t, ctx, true, func(e downloadGExchange) bool { return e.Method == http.MethodHead })
			downloadGAssertWire(t, head)
			first := p.await(t, ctx, true, func(e downloadGExchange) bool { return e.Method == http.MethodGet })
			downloadGAssertWire(t, first)
			err = downloadGResult(t, ctx, result)
			p.close()
			_, finished := p.observations()
			for _, e := range finished {
				downloadGAssertWire(t, e)
			}
			if err != nil {
				t.Fatalf("G_DOWNLOAD_PRECONDITION: Windows Do failed before destination Close/rename witness; inspect the actual stage: %v", err)
			}
			actual, err := os.ReadFile(filename)
			if err != nil || !bytes.Equal(actual, entity) {
				t.Fatalf("G_DOWNLOAD_PRECONDITION: Windows destination bytes were not complete before the Close witness: bytes=%d error=%v", len(actual), err)
			}
			moved := filepath.Join(root, "renamed-download.bin")
			if err := os.Rename(filename, moved); err != nil {
				t.Fatalf("G_DOWNLOAD_FILE_CLOSE_ASSERT: completed library-owned output cannot be renamed; closed-file control succeeded: %v", err)
			}
			if err := os.Remove(moved); err != nil {
				t.Fatalf("G_DOWNLOAD_FILE_CLOSE_ASSERT: completed library-owned output cannot be removed: %v", err)
			}
		})
	})
}
