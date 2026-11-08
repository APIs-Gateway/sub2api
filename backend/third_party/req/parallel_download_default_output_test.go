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

// This separately registered public contract leaves output filename, segment
// size and concurrency unset. It uses real HTTP and caller-owned directories.
func TestReqParallelDownloadDefaultOutput(t *testing.T) {
	t.Run("url_path_filename", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		entity := []byte("default parallel download entity\n")
		outputDirectory, temporarySegments := t.TempDir(), t.TempDir()
		destination := filepath.Join(outputDirectory, "report.bin")
		neighbor := filepath.Join(outputDirectory, "neighbor.txt")
		sentinel := []byte("caller-owned neighbor must remain\n")
		if err := os.WriteFile(destination, bytes.Repeat([]byte{0xee}, len(entity)+57), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(neighbor, sentinel, 0600); err != nil {
			t.Fatal(err)
		}
		p := newDownloadGPeer(t, func(w http.ResponseWriter, r *http.Request, e *downloadGExchange) {
			if r.URL.EscapedPath() != "/assets/report.bin" || r.URL.RawQuery != "download=1" {
				e.FixtureError = "download request changed the literal path/query: " + r.URL.RequestURI()
				return
			}
			switch r.Method {
			case http.MethodHead:
				downloadGReply(w, e, http.StatusOK, "", nil, len(entity))
			case http.MethodGet:
				start, end, err := downloadGRange(e.Range, len(entity))
				if err != nil {
					e.FixtureError = err.Error()
					return
				}
				downloadGReply(w, e, http.StatusPartialContent, fmt.Sprintf("bytes %d-%d/%d", start, end, len(entity)), entity[start:end+1], end-start+1)
			default:
				e.FixtureError = "unexpected download method: " + r.Method
			}
		})
		c := downloadGClient(t)
		c.SetOutputDirectory(outputDirectory)
		responses := downloadGObserve(c)
		result := make(chan error, 1)
		go func() {
			// Do not provide a filename, writer or segment/concurrency settings.
			result <- c.NewParallelDownload(p.server.URL + "/assets/report.bin?download=1").
				SetTempRootDir(temporarySegments).Do(ctx)
		}()
		head := p.await(t, ctx, true, func(e downloadGExchange) bool { return e.Method == http.MethodHead })
		downloadGAssertWire(t, head)
		partial := p.await(t, ctx, true, func(e downloadGExchange) bool { return e.Method == http.MethodGet })
		downloadGAssertWire(t, partial)
		err := downloadGResult(t, ctx, result)
		p.close()
		p.assertFixtures(t)
		_, finished := p.observations()
		for _, exchange := range finished {
			downloadGAssertWire(t, exchange)
		}
		actualHead := downloadGTarget(t, p, responses, func(e downloadGExchange) bool { return e.Method == http.MethodHead })
		actualPartial := downloadGTarget(t, p, responses, func(e downloadGExchange) bool {
			return e.Method == http.MethodGet && e.Range != "" && e.Status == http.StatusPartialContent
		})
		if err != nil || head.Status != http.StatusOK || partial.Status != http.StatusPartialContent || actualHead.cause != nil || actualPartial.cause != nil {
			t.Fatalf("G1748_DEFAULT_OUTPUT_ASSERT: default public download did not complete its real responses: Do=%v HEAD=%+v GET=%+v", err, actualHead, actualPartial)
		}
		actual, err := os.ReadFile(destination)
		if err != nil || !bytes.Equal(actual, entity) || downloadGHash(actual) != downloadGHash(entity) {
			t.Fatalf("G1748_DEFAULT_OUTPUT_ASSERT: URL filename or truncation lost complete entity: bytes=%d want=%d error=%v", len(actual), len(entity), err)
		}
		remaining, err := os.ReadFile(neighbor)
		if err != nil || !bytes.Equal(remaining, sentinel) {
			t.Fatalf("G1748_DEFAULT_OUTPUT_ASSERT: caller neighbor changed: bytes=%q error=%v", remaining, err)
		}
		entries, err := os.ReadDir(outputDirectory)
		if err != nil || len(entries) != 2 || entries[0].Name() != "neighbor.txt" || entries[1].Name() != "report.bin" || entries[0].IsDir() || entries[1].IsDir() {
			t.Fatalf("G1748_DEFAULT_OUTPUT_ASSERT: URL query or another filename escaped the expected output: entries=%v error=%v", entries, err)
		}
		segments, err := os.ReadDir(temporarySegments)
		if err != nil || len(segments) != 0 {
			t.Fatalf("G1748_DEFAULT_OUTPUT_ASSERT: completed Do left temporary work: entries=%v error=%v", segments, err)
		}
		t.Logf("G1748_DEFAULT_OUTPUT_COMPLETE %s filename=report.bin bytes=%d hash=%s query=download=1 tempEntries=0", t.Name(), len(actual), downloadGHash(actual))
	})
}
