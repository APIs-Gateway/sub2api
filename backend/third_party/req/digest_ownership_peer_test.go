// Child1743 public CommonDigestAuth ownership observations. Real HTTP peers and
// pinned source anchors are recorded in DIGEST-OWNERSHIP-PROVENANCE.json.
package req

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type digestOwnedBody struct {
	label    string
	reader   io.Reader
	readErr  error
	closeErr error
	calls    atomic.Int32
	closed   chan struct{}
	signal   sync.Once
}

func (b *digestOwnedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if b.readErr != nil && (n > 0 || err == io.EOF) {
		return n, b.readErr
	}
	return n, err
}

func (b *digestOwnedBody) Close() error {
	// Count every call, including calls after the first. Only the completion
	// channel is protected; the ownership witness is not idempotent.
	b.calls.Add(1)
	b.signal.Do(func() { close(b.closed) })
	return b.closeErr
}

type digestBodyLedger struct {
	mu     sync.Mutex
	bodies []*digestOwnedBody
}

func (l *digestBodyLedger) body(label, payload string, readErr, closeErr error) *digestOwnedBody {
	b := &digestOwnedBody{label: label, reader: strings.NewReader(payload), readErr: readErr,
		closeErr: closeErr, closed: make(chan struct{})}
	l.mu.Lock()
	l.bodies = append(l.bodies, b)
	l.mu.Unlock()
	return b
}

func (l *digestBodyLedger) verify(t *testing.T) {
	t.Helper()
	l.mu.Lock()
	bodies := append([]*digestOwnedBody(nil), l.bodies...)
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var failures []string
	for _, body := range bodies {
		select {
		case <-body.closed:
		default:
			select {
			case <-body.closed:
			case <-ctx.Done():
			}
		}
		if count := body.calls.Load(); count != 1 {
			failures = append(failures, fmt.Sprintf("%s Close=%d want1", body.label, count))
		}
	}
	if len(failures) != 0 {
		t.Fatalf("DIGEST_OWNERSHIP_CLOSE_ASSERT: %s", strings.Join(failures, "; "))
	}
}

// Read and close each caller-owned response exactly once. Request-body tests
// do not register a second cleanup Close on either request or response bodies.
func digestOwnershipResponse(t *testing.T, resp *http.Response, requestErr error, want string) {
	t.Helper()
	if requestErr != nil || resp == nil || resp.Body == nil {
		t.Fatalf("owned request response=%v err=%v", resp, requestErr)
	}
	body, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if resp.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || string(body) != want {
		t.Fatalf("owned HTTP response status=%d body=%q read=%v close=%v want=%q", resp.StatusCode, body, readErr, closeErr, want)
	}
}

func digestOwnershipPrime(t *testing.T, client *Client, peer *clientBehaviorPeer) {
	t.Helper()
	resp, err := client.Do(clientBehaviorRequest(t, http.MethodGet, peer.url+"/prime", ""))
	digestOwnershipResponse(t, resp, err, "authorized:")
	peer.observations(t, 2)
}

type digestObservedResponse struct {
	io.ReadCloser
	status    int
	uri       string
	calls     atomic.Int32
	readBytes atomic.Int64
	closed    chan struct{}
	signal    sync.Once
}

func (b *digestObservedResponse) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.readBytes.Add(int64(n))
	return n, err
}

func (b *digestObservedResponse) Close() error {
	b.calls.Add(1)
	err := b.ReadCloser.Close()
	b.signal.Do(func() { close(b.closed) })
	return err
}

type digestResponseLedger struct {
	mu     sync.Mutex
	bodies []*digestObservedResponse
}

// Register before CommonDigestAuth: this read-only wrapper observes the actual
// peer bodies inside Digest. It never constructs or substitutes an HTTP reply.
func (l *digestResponseLedger) observe(client *Client) {
	client.GetTransport().WrapRoundTripFunc(func(next http.RoundTripper) HttpRoundTripFunc {
		return func(request *http.Request) (*http.Response, error) {
			resp, err := next.RoundTrip(request)
			if resp != nil && resp.Body != nil {
				body := &digestObservedResponse{ReadCloser: resp.Body, status: resp.StatusCode,
					uri: request.URL.RequestURI(), closed: make(chan struct{})}
				l.mu.Lock()
				l.bodies = append(l.bodies, body)
				l.mu.Unlock()
				resp.Body = body
			}
			return resp, err
		}
	})
}

func (l *digestResponseLedger) observations(t *testing.T, count int) []*digestObservedResponse {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.bodies) != count {
		t.Fatalf("real transport responses=%d want%d", len(l.bodies), count)
	}
	return append([]*digestObservedResponse(nil), l.bodies...)
}
