package req_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReqParallelDownloadContract(t *testing.T) {
	cases := []struct {
		name string
		size int
		segment int64
		concurrency int
	}{
		{"out_of_order", 97, 17, 3},
		{"one_byte", 1, 17, 2},
		{"exact_boundary", 64, 16, 2},
		{"final_short_segment", 65, 16, 3},
		{"segment_larger_than_entity", 23, 64, 2},
		{"relative_existing_file", 65, 16, 2},
		{"absolute_file", 65, 16, 2},
		{"writer_error", 23, 64, 1},
		{"head_zero_length", 0, 16, 1},
		{"head_unknown_length", 23, 16, 1},
		{"head_error_status", 97, 64, 2},
		{"range_ignored_200", 48, 16, 3},
		{"missing_content_range", 23, 64, 1},
		{"wrong_range_start", 23, 64, 1},
		{"reversed_range", 23, 64, 1},
		{"invalid_range_total", 23, 64, 1},
		{"unknown_range_unit", 23, 64, 1},
		{"range_416", 23, 64, 1},
		{"truncated_segment", 23, 64, 1},
		{"empty_segment", 23, 64, 1},
		{"cancel_head", 23, 64, 1},
		{"cancel_segment", 23, 64, 1},
		{"failure_during_dispatch", 48, 16, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			downloadGIsolated(t, "TestReqParallelDownloadContract", tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				observe, stop := context.WithTimeout(context.Background(), 6*time.Second)
				defer stop()
				entity := downloadGEntity(tc.size)
				laterCompleted := make(chan struct{})
				var laterOnce sync.Once
				p := newDownloadGPeer(t, func(w http.ResponseWriter, r *http.Request, e *downloadGExchange) {
					if r.Method == http.MethodHead {
						switch tc.name {
						case "cancel_head":
							<-r.Context().Done()
							e.ContextError = r.Context().Err().Error()
						case "head_unknown_length":
							e.Status = http.StatusOK
							w.WriteHeader(http.StatusOK)
							e.FlushError = downloadGError(http.NewResponseController(w).Flush())
						case "head_error_status":
							downloadGReply(w, e, http.StatusNotFound, "", nil, len("not-found"))
						default:
							downloadGReply(w, e, http.StatusOK, "", nil, len(entity))
						}
						return
					}
					if r.Method != http.MethodGet {
						e.FixtureError = "unexpected method: " + r.Method
						return
					}
					if tc.name == "cancel_segment" {
						<-r.Context().Done()
						e.ContextError = r.Context().Err().Error()
						return
					}
					// A full GET is allowed if a future downloader safely falls back
					// after a server ignores Range or sends unusable partial metadata.
					if e.Range == "" {
						downloadGReply(w, e, http.StatusOK, "", entity, len(entity))
						return
					}
					start, end, err := downloadGRange(e.Range, len(entity))
					if err != nil {
						e.FixtureError = err.Error()
						return
					}
					body := entity[start:end+1]
					rangeValue := fmt.Sprintf("bytes %d-%d/%d", start, end, len(entity))
					switch tc.name {
					case "out_of_order":
						if start == 0 {
							select {
							case <-laterCompleted:
							case <-r.Context().Done():
								e.FixtureError = "another real range never completed before the first range"
								return
							}
						}
					case "range_ignored_200":
						downloadGReply(w, e, http.StatusOK, "", entity, len(entity))
						return
					case "missing_content_range":
						rangeValue = ""
					case "wrong_range_start":
						rangeValue = fmt.Sprintf("bytes 1-%d/%d", end, len(entity))
						body = entity[1:end+1]
					case "reversed_range":
						rangeValue = fmt.Sprintf("bytes %d-%d/%d", end, start, len(entity))
					case "invalid_range_total":
						rangeValue = fmt.Sprintf("bytes %d-%d/%d", start, end, end)
					case "unknown_range_unit":
						rangeValue = fmt.Sprintf("widgets %d-%d/%d", start, end, len(entity))
					case "range_416":
						body = []byte("controlled range rejected")
						downloadGReply(w, e, http.StatusRequestedRangeNotSatisfiable, fmt.Sprintf("bytes */%d", len(entity)), body, len(body))
						return
					case "truncated_segment", "failure_during_dispatch":
						downloadGShortReply(w, e, rangeValue, body[:len(body)/2], len(body))
						return
					case "empty_segment":
						downloadGShortReply(w, e, rangeValue, nil, len(body))
						return
					}
					downloadGReply(w, e, http.StatusPartialContent, rangeValue, body, len(body))
					if tc.name == "out_of_order" && start != 0 && e.WriteError == "" && e.FlushError == "" {
						laterOnce.Do(func() { close(laterCompleted) })
					}
				})
				c := downloadGClient(t)
				var output bytes.Buffer
				pd := c.NewParallelDownload(p.server.URL + "/entity.bin").SetSegmentSize(tc.segment).SetConcurrency(tc.concurrency).SetTempRootDir(downloadGTempDir(t)).SetOutput(&output)
				var destination string
				switch tc.name {
				case "relative_existing_file":
					root := downloadGTempDir(t)
					c.SetOutputDirectory(root)
					destination = filepath.Join(root, "entity.bin")
					if err := os.WriteFile(destination, bytes.Repeat([]byte{0xee}, 150), 0600); err != nil {
						t.Fatal(err)
					}
					pd = c.NewParallelDownload(p.server.URL + "/entity.bin").SetSegmentSize(tc.segment).SetConcurrency(tc.concurrency).SetTempRootDir(downloadGTempDir(t)).SetOutputFile("entity.bin")
				case "absolute_file":
					root := downloadGTempDir(t)
					c.SetOutputDirectory(filepath.Join(root, "not-selected"))
					destination = filepath.Join(root, "chosen.bin")
					pd = c.NewParallelDownload(p.server.URL + "/entity.bin").SetSegmentSize(tc.segment).SetConcurrency(tc.concurrency).SetTempRootDir(downloadGTempDir(t)).SetOutputFile(destination)
				case "writer_error":
					pd.SetOutput(downloadGFailedWriter{})
				}
				result := make(chan error, 1)
				go func() { result <- pd.Do(ctx) }()
				if tc.name == "cancel_head" || tc.name == "cancel_segment" {
					method := http.MethodHead
					if tc.name == "cancel_segment" {
						method = http.MethodGet
					}
					started := p.await(t, observe, false, func(e downloadGExchange) bool { return e.Method == method })
					if method == http.MethodGet && started.Range == "" {
						t.Fatalf("G_FIXTURE_OBSERVATION: no actual segment Range before cancellation: %+v", started)
					}
					cancel()
					e := p.await(t, observe, true, func(e downloadGExchange) bool { return e.Method == method && e.ContextError != "" })
					downloadGAssertWire(t, e)
					err := downloadGResult(t, observe, result)
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("G_DOWNLOAD_ASSERT: parallel cancellation lost: %v", err)
					}
					return
				}
				// Confirm the actual HEAD and, for nonempty known lengths, at
				// least one controlled response BEFORE an assertion about Do.
				head := p.await(t, observe, true, func(e downloadGExchange) bool { return e.Method == http.MethodHead })
				downloadGAssertWire(t, head)
				if tc.name == "head_error_status" {
					err := downloadGResult(t, observe, result)
					if err != nil {
						return // Correct early HEAD rejection need not issue any GET.
					}
					p.close()
					_, finished := p.observations()
					for _, e := range finished {
						downloadGAssertWire(t, e)
					}
					if !bytes.Equal(output.Bytes(), entity) {
						t.Fatalf("G_DOWNLOAD_ASSERT: unsuccessful HEAD produced a successful incomplete entity: bytes=%d want=%d", output.Len(), len(entity))
					}
					return
				}
				var firstResponse *downloadGExchange
				if tc.name != "head_zero_length" && tc.name != "head_unknown_length" {
					e := p.await(t, observe, true, func(e downloadGExchange) bool { return e.Method == http.MethodGet })
					firstResponse = &e
					if tc.name == "truncated_segment" || tc.name == "empty_segment" || tc.name == "failure_during_dispatch" {
						downloadGAssertWire(t, e)
						if !e.Closed {
							t.Fatalf("G_FIXTURE_TRANSFER: intentional short response was not closed: %+v", e)
						}
					}
					if tc.name == "failure_during_dispatch" {
						// Both transfer failure and explicit caller cancellation must
						// be able to release the public Do operation. No private queue
						// state is observed, and a watchdog kill is never a PASS.
						cancel()
					}
				}
				err := downloadGResult(t, observe, result)
				p.close()
				_, finished := p.observations()
				for _, e := range finished {
					if err == nil {
						downloadGAssertWire(t, e)
					} else {
						// A correct early rejection may close concurrent responses.
						// Retain their actual result without requiring successful
						// delivery of bodies the client correctly declined.
						t.Logf("G_WIRE_FINISHED_AFTER_ERROR %+v", e)
					}
				}
				if err == nil && firstResponse != nil {
					downloadGAssertWire(t, *firstResponse)
				}
				switch tc.name {
				case "writer_error":
					if !errors.Is(err, downloadGWriterError) {
						if firstResponse != nil {
							downloadGAssertWire(t, *firstResponse)
						}
						t.Fatalf("G_DOWNLOAD_ASSERT: parallel writer error lost: %v", err)
					}
					return
				case "head_zero_length", "head_unknown_length", "range_416", "truncated_segment", "empty_segment", "failure_during_dispatch":
					if err == nil {
						t.Fatalf("G_DOWNLOAD_ASSERT: unsupported or failed transfer returned success: scenario=%s output=%x", tc.name, output.Bytes())
					}
					return
				}
				if err != nil {
					switch tc.name {
					case "range_ignored_200", "wrong_range_start", "missing_content_range", "reversed_range", "invalid_range_total", "unknown_range_unit":
						return // Correct rejection may leave merger work to unwind.
					default:
						for _, e := range finished {
							downloadGAssertWire(t, e)
						}
						t.Fatalf("G_DOWNLOAD_ASSERT: valid parallel download failed: %v", err)
					}
				}
				actual := output.Bytes()
				if destination != "" {
					var readError error
					actual, readError = os.ReadFile(destination)
					if readError != nil {
						t.Fatal(readError)
					}
				}
				switch tc.name {
				case "range_ignored_200", "wrong_range_start":
					// Successful fallback must still yield the exact full entity.
				case "missing_content_range", "reversed_range", "invalid_range_total", "unknown_range_unit":
					fullRecovery := false
					for _, e := range finished {
						fullRecovery = fullRecovery || (e.Method == http.MethodGet && e.Range == "" && e.Status == http.StatusOK && bytes.Equal(e.Body, entity))
					}
					if !fullRecovery {
						t.Fatalf("G_DOWNLOAD_ASSERT: unusable partial metadata was accepted without a safe full response: scenario=%s", tc.name)
					}
				}
				if !bytes.Equal(actual, entity) || downloadGHash(actual) != downloadGHash(entity) {
					t.Fatalf("G_DOWNLOAD_ASSERT: completed entity is corrupt: scenario=%s bytes=%d want=%d hash=%s wantHash=%s", tc.name, len(actual), len(entity), downloadGHash(actual), downloadGHash(entity))
				}
			})
		})
	}
}
