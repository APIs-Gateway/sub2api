package req

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// The producer cannot send its second piece until the TCP peer consumes the
// first. This proves that unknown-length uploads flush while still open,
// instead of merely comparing a final, already-buffered payload.
func TestReqHTTP1Lifecycle_ChunkedUploadTrailers(t *testing.T) {
	const first = "first-piece-before-upload-eof"
	const second = "second-piece-after-peer-read"
	consumedFirst := make(chan struct{})
	var received, trailer string
	peer := newReqH1LifecyclePeer(t, func(p *reqH1LifecyclePeer) error {
		conn, err := p.accept()
		if err != nil {
			return err
		}
		request, err := reqH1LifecycleReadRequest(bufio.NewReader(conn), "/stream-upload")
		if err != nil {
			return err
		}
		defer request.Body.Close()
		if request.Method != http.MethodPost || request.ContentLength != -1 || len(request.TransferEncoding) != 1 || request.TransferEncoding[0] != "chunked" {
			return fmt.Errorf("stream upload method=%s length=%d encodings=%v", request.Method, request.ContentLength, request.TransferEncoding)
		}
		if request.Trailer.Get("X-Upload-End") != "" {
			return fmt.Errorf("upload trailer visible before EOF: %v", request.Trailer)
		}
		prefix := make([]byte, len(first))
		if _, err := io.ReadFull(request.Body, prefix); err != nil {
			return err
		}
		if string(prefix) != first {
			return fmt.Errorf("first streamed upload=%q", prefix)
		}
		close(consumedFirst)
		rest, err := io.ReadAll(request.Body)
		if err != nil {
			return err
		}
		received = string(prefix) + string(rest)
		trailer = request.Trailer.Get("X-Upload-End")
		if received != first+second || trailer != "complete" {
			return fmt.Errorf("upload=%q trailer=%q", received, trailer)
		}
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 8\r\nConnection: close\r\n\r\naccepted")
		return err
	})
	client := reqH1LifecycleClient(t)
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	uploadDone := make(chan struct{})
	uploadResult := make(chan error, 1)
	t.Cleanup(func() {
		cancel()
		_ = reader.Close()
		_ = writer.Close()
		select {
		case <-uploadDone:
		case <-time.After(3 * time.Second):
			t.Error("stream upload producer did not join")
		}
	})
	go func() {
		defer close(uploadDone)
		defer writer.Close()
		if _, err := io.WriteString(writer, first); err != nil {
			uploadResult <- err
			return
		}
		select {
		case <-consumedFirst:
		case <-ctx.Done():
			uploadResult <- ctx.Err()
			return
		case <-time.After(3 * time.Second):
			uploadResult <- fmt.Errorf("peer did not consume first upload before EOF")
			return
		}
		_, err := io.WriteString(writer, second)
		uploadResult <- err
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+peer.listener.Addr().String()+"/stream-upload", reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Trailer = http.Header{"X-Upload-End": []string{"complete"}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	result, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(result) != "accepted" {
		t.Fatalf("upload response status=%d bytes=%q error=%v", response.StatusCode, result, err)
	}
	select {
	case err := <-uploadResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upload producer did not report completion")
	}
	<-uploadDone
	peer.join(t)
	if received != first+second || trailer != "complete" {
		t.Fatalf("joined upload=%q trailer=%q", received, trailer)
	}
}
