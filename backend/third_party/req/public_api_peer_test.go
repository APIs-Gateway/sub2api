package req_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	req "github.com/imroc/req/v3"
)

type publicAPIExchange struct {
	method     string
	uri        string
	headers    http.Header
	body       string
	readError  error
	writeError error
}

type publicAPIPeer struct {
	server          *httptest.Server
	exchanges       chan publicAPIExchange
	requests        atomic.Int32
	received        atomic.Int32
	overflow        atomic.Bool
	mu              sync.Mutex
	overflowRecords []publicAPIExchange
}

func publicAPIInstall(t *testing.T) *req.Client {
	t.Helper()
	previous := req.DefaultClient()
	client := req.C().SetProxy(nil).EnableForceHTTP1().DisableAutoDecode().SetTimeout(5 * time.Second)
	t.Cleanup(func() { req.SetDefaultClient(previous) })
	t.Cleanup(func() {
		if err := client.CloseDump(); err != nil {
			t.Errorf("owned client dump close: %v", err)
		}
		client.CloseIdleConnections()
	})
	req.SetDefaultClient(client)
	return client
}

func publicAPINewPeer(t *testing.T, reply func(int, publicAPIExchange) (int, string, string), responseHeaders ...http.Header) *publicAPIPeer {
	t.Helper()
	p := &publicAPIPeer{exchanges: make(chan publicAPIExchange, 16)}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := int(p.requests.Add(1))
		body, err := io.ReadAll(io.LimitReader(r.Body, 65537))
		if len(body) > 65536 {
			err = fmt.Errorf("public API peer entity exceeded bound")
		}
		e := publicAPIExchange{method: r.Method, uri: r.RequestURI, headers: r.Header.Clone(), body: string(body), readError: err}
		status, contentType, response := reply(attempt, e)
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Public-Response", "response-header-token")
		for _, headers := range responseHeaders {
			for name, values := range headers {
				for _, value := range values {
					w.Header().Add(name, value)
				}
			}
		}
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			_, e.writeError = io.WriteString(w, response)
		}
		select {
		case p.exchanges <- e:
		default:
			p.overflow.Store(true)
			// Preserve every rejected exchange and its actual fixture errors;
			// do not retain an unnecessary second copy of the bounded entity.
			e.body = ""
			p.mu.Lock()
			p.overflowRecords = append(p.overflowRecords, e)
			p.mu.Unlock()
		}
	}))
	t.Cleanup(func() {
		// Close joins all handlers before inspecting the complete ledger.
		p.server.Close()
		close(p.exchanges)
		for e := range p.exchanges {
			t.Errorf("unconsumed peer exchange method=%s uri=%s read=%v write=%v", e.method, e.uri, e.readError, e.writeError)
		}
		p.mu.Lock()
		overflowRecords := append([]publicAPIExchange(nil), p.overflowRecords...)
		p.mu.Unlock()
		for _, e := range overflowRecords {
			t.Errorf("overflowed peer exchange method=%s uri=%s read=%v write=%v", e.method, e.uri, e.readError, e.writeError)
		}
		if p.overflow.Load() {
			t.Errorf("public API peer overflowed; retained extra records=%d", len(overflowRecords))
		}
		if p.requests.Load() != p.received.Load() {
			t.Errorf("peer final request ledger requests=%d consumed=%d", p.requests.Load(), p.received.Load())
		}
		t.Logf("PUBLIC_API_PEER_COMPLETE requests=%d consumed=%d overflow=%v", p.requests.Load(), p.received.Load(), p.overflow.Load())
	})
	return p
}

func publicAPIPlainPeer(t *testing.T) *publicAPIPeer {
	t.Helper()
	return publicAPINewPeer(t, func(int, publicAPIExchange) (int, string, string) { return 200, "text/plain", "public-api-response" })
}

func (p *publicAPIPeer) receive(t *testing.T) publicAPIExchange {
	t.Helper()
	select {
	case e := <-p.exchanges:
		p.received.Add(1)
		if p.overflow.Load() || e.readError != nil || e.writeError != nil {
			t.Fatalf("peer error overflow=%v read=%v write=%v", p.overflow.Load(), e.readError, e.writeError)
		}
		return e
	case <-time.After(6 * time.Second):
		t.Fatal("completed public API peer exchange missing")
		return publicAPIExchange{}
	}
}

func publicAPIResponse(t *testing.T, response *req.Response, err error, status int, body string) {
	t.Helper()
	if err != nil || response == nil {
		t.Fatalf("response=%v error=%v", response, err)
	}
	if response.StatusCode != status || response.String() != body {
		t.Fatalf("status/body=%d/%q want%d/%q", response.StatusCode, response.String(), status, body)
	}
	if response.Body != nil {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Fatalf("response body close: %v", closeErr)
		}
	}
}
