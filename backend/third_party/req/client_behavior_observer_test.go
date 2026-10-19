// Owned lifecycle/wire observations for public client contracts.
// See CLIENT-BEHAVIOR-PROVENANCE.json and CLIENT-BEHAVIOR-LICENSE.
package req

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type clientBehaviorSource struct {
	io.Reader
	closed chan struct{}
	once sync.Once
}

func newClientBehaviorSource(payload string) *clientBehaviorSource {
	return &clientBehaviorSource{Reader: strings.NewReader(payload), closed: make(chan struct{})}
}

func (s *clientBehaviorSource) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// The RoundTripper contract permits closure after RoundTrip returns. Wait for
// the owned source's completion signal instead of assuming synchronous Close.
func clientBehaviorAwaitSourceClose(t *testing.T, ctx context.Context, body *clientBehaviorSource, label string) {
	t.Helper()
	select {
	case <-body.closed:
		return
	default:
	}
	select {
	case <-body.closed:
	case <-ctx.Done():
		t.Fatalf("REQ_DIGEST_BODY_CLOSE_ASSERT: %s did not close before bounded context: %v", label, ctx.Err())
	}
}

type clientBehaviorWriteObservation struct {
	uri string
	payload string
	written int
	writeErr error
	flushErr error
}

func clientBehaviorObserveWrite(w http.ResponseWriter, uri, payload string, observed chan<- clientBehaviorWriteObservation) {
	n, err := io.WriteString(w, payload)
	flushErr := http.NewResponseController(w).Flush()
	observed <- clientBehaviorWriteObservation{uri: uri, payload: payload, written: n, writeErr: err, flushErr: flushErr}
}

func clientBehaviorAwaitWrite(t *testing.T, ctx context.Context, observed <-chan clientBehaviorWriteObservation, uri, payload string) {
	t.Helper()
	select {
	case o := <-observed:
		if o.uri != uri || o.payload != payload || o.written != len(payload) || o.writeErr != nil || o.flushErr != nil {
			t.Fatalf("owned401 peer write/flush fixture incomplete: %+v wantURI=%q wantPayload=%q", o, uri, payload)
		}
	case <-ctx.Done():
		t.Fatalf("owned401 peer write/flush fixture did not complete: %v", ctx.Err())
	}
}

// Call only after the owned peer's exact payload write and flush are verified.
// The assertion marker then identifies lost response bytes, not a broken peer.
func clientBehaviorReadable401(t *testing.T, resp *http.Response, requestErr error, payload string) {
	t.Helper()
	if resp != nil && resp.Body != nil { t.Cleanup(func() { _ = resp.Body.Close() }) }
	if requestErr != nil || resp == nil || resp.Body == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ordinary401 request/status after validated wire: response=%v err=%v", resp, requestErr)
	}
	body, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil || string(body) != payload {
		t.Fatalf("REQ_DIGEST_401_BODY_ASSERT: validated owned401 payload lost: body=%q want=%q readErr=%v", body, payload, readErr)
	}
	if closeErr != nil { t.Fatalf("actual ordinary401 response Close: %v", closeErr) }
}
