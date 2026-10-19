package req_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReqDownloadContract(t *testing.T) {
	for _, mode := range []string{"replace_existing_relative", "absolute_path", "empty_entity", "range_suffix_file", "if_range_200_visible", "range_416_visible", "short_body_error", "writer_error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entity := downloadGEntity(97)
			const offset = 29
			p := newDownloadGPeer(t, func(w http.ResponseWriter, r *http.Request, e *downloadGExchange) {
				switch mode {
				case "empty_entity":
					downloadGReply(w, e, http.StatusOK, "", nil, 0)
				case "range_suffix_file":
					downloadGReply(w, e, http.StatusPartialContent, fmt.Sprintf("bytes %d-%d/%d", offset, len(entity)-1, len(entity)), entity[offset:], len(entity)-offset)
				case "range_416_visible":
					body := []byte("controlled unsatisfied range")
					downloadGReply(w, e, http.StatusRequestedRangeNotSatisfiable, fmt.Sprintf("bytes */%d", len(entity)), body, len(body))
				case "short_body_error":
					downloadGShortReply(w, e, fmt.Sprintf("bytes 0-%d/%d", len(entity)-1, len(entity)), entity[:13], len(entity))
				default:
					downloadGReply(w, e, http.StatusOK, "", entity, len(entity))
				}
			})
			c := downloadGClient(t)
			root := t.TempDir()
			var output bytes.Buffer
			request := c.Get(p.server.URL + "/entity").SetOutput(&output)
			var destination string
			switch mode {
			case "replace_existing_relative":
				c.SetOutputDirectory(root)
				destination = filepath.Join(root, "nested", "entity.bin")
				if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(destination, bytes.Repeat([]byte{0xee}, 200), 0600); err != nil {
					t.Fatal(err)
				}
				request = c.Get(p.server.URL + "/entity").SetOutputFile(filepath.Join("nested", "entity.bin"))
			case "absolute_path":
				c.SetOutputDirectory(filepath.Join(root, "other"))
				destination = filepath.Join(root, "chosen.bin")
				request = c.Get(p.server.URL + "/entity").SetOutputFile(destination)
			case "range_suffix_file":
				destination = filepath.Join(root, "continued.bin")
				if err := os.WriteFile(destination, entity[:offset], 0600); err != nil {
					t.Fatal(err)
				}
				file, err := os.OpenFile(destination, os.O_WRONLY|os.O_APPEND, 0600)
				if err != nil {
					t.Fatal(err)
				}
				// SetOutput downloads before Do returns its response. The caller
				// explicitly chooses append; no automatic validation/rollback claim.
				defer file.Close()
				request = c.Get(p.server.URL + "/entity").SetHeader("Range", fmt.Sprintf("bytes=%d-", offset)).SetHeader("If-Range", `"g-download-v1"`).SetOutput(file)
			case "if_range_200_visible":
				request.SetHeader("Range", fmt.Sprintf("bytes=%d-", offset)).SetHeader("If-Range", `"old-entity"`)
			case "range_416_visible":
				request.SetHeader("Range", "bytes=999-")
			case "writer_error":
				request.SetOutput(downloadGFailedWriter{})
			}
			response := request.Do(ctx)
			e := p.await(t, ctx, true, func(e downloadGExchange) bool { return e.Method == http.MethodGet })
			downloadGAssertWire(t, e)
			p.close()
			if response.Response == nil {
				t.Fatalf("G_DOWNLOAD_ASSERT: expected an actual HTTP response: %v", response.Err)
			}
			switch mode {
			case "short_body_error":
				if !e.Closed || !errors.Is(response.Err, io.ErrUnexpectedEOF) || !bytes.Equal(output.Bytes(), entity[:13]) {
					t.Fatalf("G_DOWNLOAD_ASSERT: short response error/prefix: closed=%v error=%v bytes=%x", e.Closed, response.Err, output.Bytes())
				}
				return
			case "writer_error":
				if !errors.Is(response.Err, downloadGWriterError) {
					t.Fatalf("G_DOWNLOAD_ASSERT: lost writer error: %v", response.Err)
				}
				return
			case "range_416_visible":
				if response.Err != nil || response.StatusCode != http.StatusRequestedRangeNotSatisfiable || response.Header.Get("Content-Range") != "bytes */97" || !bytes.Equal(output.Bytes(), []byte("controlled unsatisfied range")) || e.Range != "bytes=999-" {
					t.Fatalf("G_DOWNLOAD_ASSERT: ordinary 416 status/body/header not visible: response=%+v error=%v output=%q peer=%+v", response.Response, response.Err, output.Bytes(), e)
				}
				return
			}
			if response.Err != nil {
				t.Fatalf("G_DOWNLOAD_ASSERT: ordinary download failed: %v", response.Err)
			}
			want := entity
			if mode == "empty_entity" {
				want = nil
			}
			actual := output.Bytes()
			if destination != "" {
				var err error
				actual, err = os.ReadFile(destination)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(actual, want) || downloadGHash(actual) != downloadGHash(want) {
				t.Fatalf("G_DOWNLOAD_ASSERT: entity mismatch: bytes=%d want=%d hash=%s wantHash=%s", len(actual), len(want), downloadGHash(actual), downloadGHash(want))
			}
			if mode == "range_suffix_file" && (response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Range") != "bytes 29-96/97" || e.Range != "bytes=29-" || e.IfRange != `"g-download-v1"`) {
				t.Fatalf("G_DOWNLOAD_ASSERT: explicit resume range/validator metadata: status=%d peer=%+v", response.StatusCode, e)
			}
			if mode == "if_range_200_visible" && (response.StatusCode != http.StatusOK || e.Range != "bytes=29-" || e.IfRange != `"old-entity"`) {
				t.Fatalf("G_DOWNLOAD_ASSERT: changed If-Range 200 was not returned intact: status=%d peer=%+v", response.StatusCode, e)
			}
			if mode == "absolute_path" {
				if _, err := os.Stat(filepath.Join(root, "other")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("G_DOWNLOAD_ASSERT: absolute output unexpectedly used default output directory: %v", err)
				}
			}
		})
	}
}

func TestReqDownloadContractCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observe, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	ready := make(chan struct{})
	p := newDownloadGPeer(t, func(w http.ResponseWriter, r *http.Request, e *downloadGExchange) {
		e.Status = http.StatusOK
		e.ContentLength = "100"
		e.Body = []byte("prefix")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		n, err := w.Write(e.Body)
		e.Written, e.WriteError = n, downloadGError(err)
		e.FlushError = downloadGError(http.NewResponseController(w).Flush())
		close(ready)
		<-r.Context().Done()
		e.ContextError = r.Context().Err().Error()
	})
	c := downloadGClient(t)
	var output bytes.Buffer
	result := make(chan error, 1)
	go func() { result <- c.Get(p.server.URL).SetOutput(&output).Do(ctx).Err }()
	select {
	case <-ready:
	case <-observe.Done():
		t.Fatal("G_FIXTURE_OBSERVATION: response prefix never flushed")
	}
	cancel()
	e := p.await(t, observe, true, func(e downloadGExchange) bool { return e.Method == http.MethodGet && e.ContextError != "" })
	downloadGAssertWire(t, e)
	err := downloadGResult(t, observe, result)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("G_DOWNLOAD_ASSERT: caller cancellation not returned: %v", err)
	}
}
