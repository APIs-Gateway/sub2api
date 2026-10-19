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
			control := filepath.Join(root, "closed-control.bin")
			if err := os.WriteFile(control, []byte("closed control"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(control, filepath.Join(root, "renamed-control.bin")); err != nil {
				t.Fatalf("G_FIXTURE_WINDOWS: rename of closed owned control failed: %v", err)
			}
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
			err := downloadGResult(t, ctx, result)
			p.close()
			_, finished := p.observations()
			for _, e := range finished {
				downloadGAssertWire(t, e)
			}
			if err != nil {
				t.Fatalf("G_DOWNLOAD_ASSERT: valid Windows download failed: %v", err)
			}
			actual, err := os.ReadFile(filename)
			if err != nil || !bytes.Equal(actual, entity) {
				t.Fatalf("G_DOWNLOAD_ASSERT: Windows destination was not the complete entity: bytes=%d error=%v", len(actual), err)
			}
			moved := filepath.Join(root, "renamed-download.bin")
			if err := os.Rename(filename, moved); err != nil {
				t.Fatalf("G_DOWNLOAD_ASSERT: completed library-owned output cannot be renamed; closed-file control succeeded: %v", err)
			}
			if err := os.Remove(moved); err != nil {
				t.Fatalf("G_DOWNLOAD_ASSERT: completed library-owned output cannot be removed: %v", err)
			}
		})
	})
}
