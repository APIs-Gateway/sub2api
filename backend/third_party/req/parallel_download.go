package req

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type ParallelDownload struct {
	url         string
	client      *Client
	concurrency int
	output      io.Writer
	filename    string
	segmentSize int64
	perm        os.FileMode
	tempRootDir string
	tempDir     string
}

func md5Sum(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (pd *ParallelDownload) ensure() error {
	if pd.concurrency <= 0 {
		pd.concurrency = 5
	}
	if pd.segmentSize <= 0 {
		pd.segmentSize = 1073741824 // Preserve the original 1 GiB default.
	}
	if pd.perm == 0 {
		pd.perm = 0777
	}
	if pd.tempRootDir == "" {
		pd.tempRootDir = os.TempDir()
	}
	if err := os.MkdirAll(pd.tempRootDir, os.ModePerm); err != nil {
		return err
	}
	// Each invocation owns its directory, including concurrent downloads of
	// the same URL. Cleanup must never delete another invocation's segments.
	var err error
	pd.tempDir, err = os.MkdirTemp(pd.tempRootDir, "req-"+md5Sum(pd.url)+"-")
	return err
}

func (pd *ParallelDownload) SetSegmentSize(segmentSize int64) *ParallelDownload {
	pd.segmentSize = segmentSize
	return pd
}

func (pd *ParallelDownload) SetTempRootDir(tempRootDir string) *ParallelDownload {
	pd.tempRootDir = tempRootDir
	return pd
}

func (pd *ParallelDownload) SetFileMode(perm os.FileMode) *ParallelDownload {
	pd.perm = perm
	return pd
}

func (pd *ParallelDownload) SetConcurrency(concurrency int) *ParallelDownload {
	pd.concurrency = concurrency
	return pd
}

func (pd *ParallelDownload) SetOutput(output io.Writer) *ParallelDownload {
	if output != nil {
		pd.output = output
	}
	return pd
}

func (pd *ParallelDownload) SetOutputFile(filename string) *ParallelDownload {
	pd.filename = filename
	return pd
}

func getRangeTempFile(rangeStart, rangeEnd int64, workerDir string) string {
	return filepath.Join(workerDir, fmt.Sprintf("temp-%d-%d", rangeStart, rangeEnd))
}

type downloadTask struct {
	rangeStart, rangeEnd int64
}

var errParallelRangeIgnored = errors.New("server ignored the requested byte range")

func parallelRangeEnd(start, total, size int64) int64 {
	// Subtract before adding so even a maximum int64 size cannot overflow.
	if remaining := total - start; size > remaining {
		size = remaining
	}
	return start + size - 1
}

func validateParallelRange(resp *Response, task downloadTask, total int64) error {
	values := resp.Header.Values("Content-Range")
	if len(values) != 1 {
		return fmt.Errorf("expected one Content-Range for bytes %d-%d", task.rangeStart, task.rangeEnd)
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bytes") {
		return fmt.Errorf("unsupported Content-Range: %q", values[0])
	}
	span, length, ok := strings.Cut(parts[1], "/")
	first, last, spanOK := strings.Cut(span, "-")
	if !ok || !spanOK {
		return fmt.Errorf("invalid Content-Range: %q", values[0])
	}
	numbers := [3]int64{}
	for i, value := range []string{first, last, length} {
		if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return fmt.Errorf("invalid Content-Range number: %q", values[0])
		}
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid Content-Range number: %q", values[0])
		}
		numbers[i] = number
	}
	if numbers[0] != task.rangeStart || numbers[1] != task.rangeEnd || numbers[2] != total {
		return fmt.Errorf("Content-Range %q does not match bytes %d-%d/%d", values[0], task.rangeStart, task.rangeEnd, total)
	}
	return nil
}

// copyParallelBody requires exactly expected entity bytes and an actual EOF.
// Reading one extra byte detects oversized chunked responses without expected+1
// arithmetic, which could overflow for a maximum int64 entity size.
func copyParallelBody(file io.Writer, body io.Reader, expected int64) error {
	if _, err := io.CopyN(file, body, expected); err != nil {
		if errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	n, err := io.CopyN(io.Discard, body, 1)
	if n != 0 {
		return fmt.Errorf("download body exceeds expected length %d", expected)
	}
	if err != io.EOF {
		return err
	}
	return nil
}

func (pd *ParallelDownload) contentLength(ctx context.Context) (length int64, err error) {
	resp := pd.client.Head(pd.url).SetHeader("Accept-Encoding", "identity").DisableAutoReadResponse().Do(ctx)
	if resp.Response != nil && resp.Body != nil {
		defer func() {
			if closeErr := resp.Body.Close(); err == nil {
				err = closeErr
			}
		}()
	}
	if resp.Err != nil {
		return 0, resp.Err
	}
	if resp.Response == nil || resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("parallel download HEAD returned HTTP %d", resp.GetStatusCode())
	}
	if resp.ContentLength <= 0 {
		return 0, fmt.Errorf("bad content length: %d", resp.ContentLength)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return 0, fmt.Errorf("parallel download requires identity encoding, got %q", encoding)
	}
	return resp.ContentLength, nil
}

func (pd *ParallelDownload) fetchTask(ctx context.Context, task downloadTask, total int64, full bool) (err error) {
	client := pd.client
	if full {
		// A client-wide Range must not turn the recovery into another range
		// request. Clone only in that case, preserving the original cookie jar.
		for name := range client.Headers {
			if strings.EqualFold(name, "Range") || strings.EqualFold(name, "If-Range") {
				client = client.Clone()
				client.httpClient.Jar = pd.client.httpClient.Jar
				// Client.Clone installs its cloned req transport. Preserve an
				// explicitly installed custom (or nil/default) http transport.
				// Compare only typed pointers: a caller's RoundTripper value
				// need not be comparable as an interface value.
				original := pd.client.httpClient.Transport
				if installed, ok := original.(*Transport); !ok || installed != pd.client.Transport {
					client.httpClient.Transport = original
				}
				for key := range client.Headers {
					if strings.EqualFold(key, "Range") || strings.EqualFold(key, "If-Range") {
						delete(client.Headers, key)
					}
				}
				// This clone owns a separate forced-HTTP3 transport/socket.
				// Register cleanup before the response-body defer below, so
				// LIFO closes the body first. Never close the caller transport.
				owned := client.Transport
				defer func() {
					if closeErr := client.CloseDump(); err == nil {
						err = closeErr
					}
					owned.CloseIdleConnections()
					if owned.t3 != nil {
						if closeErr := owned.t3.Close(); err == nil {
							err = closeErr
						}
					}
				}()
				break
			}
		}
	}
	request := client.Get(pd.url).SetHeader("Accept-Encoding", "identity").DisableAutoReadResponse()
	if !full {
		request.SetHeader("Range", fmt.Sprintf("bytes=%d-%d", task.rangeStart, task.rangeEnd))
	}
	resp := request.Do(ctx)
	if resp.Response != nil && resp.Body != nil {
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil && (err == nil || errors.Is(err, errParallelRangeIgnored)) {
				err = closeErr
			}
		}()
	}
	if resp.Err != nil {
		return resp.Err
	}
	if resp.Response == nil || resp.Body == nil {
		return fmt.Errorf("parallel download has no response body")
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return fmt.Errorf("parallel download requires identity encoding, got %q", encoding)
	}
	if !full && resp.StatusCode == http.StatusOK {
		// Never append a full representation as one of several segments.
		// The coordinator cancels/joins every worker before a fresh full GET.
		return errParallelRangeIgnored
	}
	if full {
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Range") != "" {
			return fmt.Errorf("full download returned an unusable HTTP %d response", resp.StatusCode)
		}
	} else {
		if resp.StatusCode != http.StatusPartialContent {
			return fmt.Errorf("range download returned HTTP %d", resp.StatusCode)
		}
		if err := validateParallelRange(resp, task, total); err != nil {
			return err
		}
	}
	expected := task.rangeEnd - task.rangeStart + 1
	if resp.ContentLength >= 0 && resp.ContentLength != expected {
		return fmt.Errorf("download Content-Length %d does not match expected %d", resp.ContentLength, expected)
	}
	file, err := os.OpenFile(getRangeTempFile(task.rangeStart, task.rangeEnd, pd.tempDir), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	return copyParallelBody(file, resp.Body, expected)
}

func (pd *ParallelDownload) fetchRanges(ctx context.Context, total int64) error {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tasks := make(chan downloadTask)
	workers := pd.concurrency
	if count := 1 + (total-1)/pd.segmentSize; count < int64(workers) {
		workers = int(count)
	}
	// Each worker reports at most one error, so reporting cannot block a
	// worker that the coordinator must join. A recoverable 200 signal must
	// not hide another worker's real I/O or protocol failure.
	type taskError struct {
		err             error
		secondaryCancel bool
	}
	errorsFound := make(chan taskError, workers)
	var workersDone sync.WaitGroup
	workersDone.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer workersDone.Done()
			for {
				select {
				case <-workCtx.Done():
					return
				case task, ok := <-tasks:
					if !ok || workCtx.Err() != nil {
						return
					}
					if err := pd.fetchTask(workCtx, task, total, false); err != nil {
						errorsFound <- taskError{
							err:             err,
							secondaryCancel: errors.Is(err, context.Canceled) && workCtx.Err() != nil && ctx.Err() == nil,
						}
						cancel()
						return
					}
				}
			}
		}()
	}

dispatch:
	for start := int64(0); start < total; {
		end := parallelRangeEnd(start, total, pd.segmentSize)
		select {
		case tasks <- downloadTask{rangeStart: start, rangeEnd: end}:
		case <-workCtx.Done():
			break dispatch
		}
		start = end + 1
	}
	close(tasks)
	workersDone.Wait()
	close(errorsFound)
	ignoredRange := false
	var firstError error
	for result := range errorsFound {
		if errors.Is(result.err, errParallelRangeIgnored) {
			ignoredRange = true
		} else if !result.secondaryCancel && firstError == nil {
			firstError = result.err
		}
	}
	if firstError != nil {
		return firstError
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ignoredRange {
		return errParallelRangeIgnored
	}
	return nil
}

type parallelContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r parallelContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func copyParallelFile(ctx context.Context, output io.Writer, filename string) (err error) {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	_, err = io.Copy(output, parallelContextReader{ctx: ctx, reader: file})
	return err
}

func (pd *ParallelDownload) mergeFile(ctx context.Context, total, segmentSize int64) (err error) {
	output, closeOutput, err := pd.getOutputFile()
	if err != nil {
		return err
	}
	if closeOutput != nil {
		defer func() {
			if closeErr := closeOutput(); err == nil {
				err = closeErr
			}
		}()
	}
	for start := int64(0); start < total; {
		end := parallelRangeEnd(start, total, segmentSize)
		if err := copyParallelFile(ctx, output, getRangeTempFile(start, end, pd.tempDir)); err != nil {
			return err
		}
		start = end + 1
	}
	return ctx.Err()
}

func (pd *ParallelDownload) Do(ctx ...context.Context) error {
	// Run state contains no locks and belongs to this call, not to reusable
	// configuration. Concurrent calls never share queues or temporary files.
	run := *pd
	return run.do(ctx...)
}

func (pd *ParallelDownload) do(contexts ...context.Context) (err error) {
	ctx := context.Background()
	if len(contexts) > 0 && contexts[0] != nil {
		ctx = contexts[0]
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := pd.ensure(); err != nil {
		return err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(pd.tempDir); err == nil {
			err = cleanupErr
		}
	}()
	total, err := pd.contentLength(ctx)
	if err != nil {
		return err
	}
	segmentSize := pd.segmentSize
	err = pd.fetchRanges(ctx, total)
	if errors.Is(err, errParallelRangeIgnored) {
		if err := ctx.Err(); err != nil {
			return err
		}
		// fetchRanges has joined all workers and closed every segment/body.
		// Discard all partial work before the single no-Range recovery GET.
		if err := os.RemoveAll(pd.tempDir); err != nil {
			return err
		}
		pd.tempDir, err = os.MkdirTemp(pd.tempRootDir, "req-"+md5Sum(pd.url)+"-")
		if err != nil {
			return err
		}
		err = pd.fetchTask(ctx, downloadTask{rangeStart: 0, rangeEnd: total - 1}, total, true)
		segmentSize = total
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return pd.mergeFile(ctx, total, segmentSize)
}

func (pd *ParallelDownload) getOutputFile() (io.Writer, func() error, error) {
	if pd.output != nil {
		// The caller retains ownership, even when its writer is also a Closer.
		return pd.output, nil, nil
	}
	filename := pd.filename
	if filename == "" {
		u, err := urlpkg.Parse(pd.url)
		if err != nil {
			return nil, nil, err
		}
		paths := strings.Split(u.Path, "/")
		for i := len(paths) - 1; i > 0; i-- {
			if paths[i] != "" {
				filename = paths[i]
				break
			}
		}
		if filename == "" {
			filename = "download"
		}
	}
	if pd.client.outputDirectory != "" && !filepath.IsAbs(filename) {
		filename = filepath.Join(pd.client.outputDirectory, filename)
	}
	file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, pd.perm)
	if err != nil {
		return nil, nil, err
	}
	return file, file.Close, nil
}
