// Public behavior contracts informed by icholy/digest v1.1.0 transport_test.go
// (b1b6f62b9a55549e464151d0d5ab78f8cbe2bbf4, MIT) and Go go1.26.9
// client_test.go (2ae494ef9fb90e0da0073c31800ba38870249994, BSD).
// See CLIENT-BEHAVIOR-PROVENANCE.json and CLIENT-BEHAVIOR-LICENSE.
package req

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/icholy/digest"
)

type clientBehaviorConnKey struct{}

type clientBehaviorObservation struct {
	method string
	uri    string
	host   string
	body   string
	header http.Header
	connID int64
	err    error
}

type clientBehaviorPeer struct {
	url    string
	server *httptest.Server
	mu     sync.Mutex
	seen   []clientBehaviorObservation
}

func newClientBehaviorPeer(t *testing.T, handler func(http.ResponseWriter, *http.Request, clientBehaviorObservation)) *clientBehaviorPeer {
	t.Helper()
	p := &clientBehaviorPeer{}
	var nextConn atomic.Int64
	p.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		observation := clientBehaviorObservation{
			method: r.Method, uri: r.URL.RequestURI(), host: r.Host,
			body: string(body), header: r.Header.Clone(), err: err,
			connID: r.Context().Value(clientBehaviorConnKey{}).(int64),
		}
		p.mu.Lock()
		p.seen = append(p.seen, observation)
		p.mu.Unlock()
		if err != nil {
			http.Error(w, "owned peer request read: "+err.Error(), http.StatusBadRequest)
			return
		}
		handler(w, r, observation)
	}))
	p.server.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, clientBehaviorConnKey{}, nextConn.Add(1))
	}
	p.server.Start()
	p.url = p.server.URL
	t.Cleanup(p.server.Close)
	return p
}

func (p *clientBehaviorPeer) observations(t *testing.T, count int) []clientBehaviorObservation {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) != count {
		t.Fatalf("actual peer requests=%d want=%d: %+v", len(p.seen), count, p.seen)
	}
	return append([]clientBehaviorObservation(nil), p.seen...)
}

func newClientBehaviorClient(t *testing.T) *Client {
	t.Helper()
	client := C().SetProxy(nil).SetTimeout(5 * time.Second).EnableForceHTTP1().DisableAutoDecode()
	t.Cleanup(func() { client.GetTransport().CloseIdleConnections() })
	return client
}

func clientBehaviorBody(t *testing.T, resp *http.Response, requestErr error) string {
	t.Helper()
	if resp != nil && resp.Body != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	if requestErr != nil || resp == nil || resp.Body == nil {
		t.Fatalf("actual public request: response=%v error=%v", resp, requestErr)
	}
	body, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("actual response Read/Close: %v/%v", readErr, closeErr)
	}
	return string(body)
}

func clientBehaviorRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

const clientBehaviorRealm = "owned-client-realm"
const clientBehaviorUser = "owned-user"
const clientBehaviorPassword = "owned-password"

func clientBehaviorChallenge(algorithm, qop, nonce string) string {
	return fmt.Sprintf(`Digest realm=%q, nonce=%q, algorithm=%s, qop=%q, opaque="owned-opaque"`, clientBehaviorRealm, nonce, algorithm, qop)
}

// Parse the independent peer's observed header, then compute the expected
// response with stdlib hashes. Calling digest.Digest as the peer's oracle would
// hide a shared request-target/entity-hash mistake.
func clientBehaviorCredentials(o clientBehaviorObservation, algorithm, qop, nonce string) (*digest.Credentials, error) {
	c, err := digest.ParseCredentials(o.header.Get("Authorization"))
	if err != nil {
		return nil, err
	}
	if c.Username != clientBehaviorUser || c.Realm != clientBehaviorRealm || c.Nonce != nonce || c.URI != o.uri || c.Algorithm != algorithm || c.QOP != qop || c.Cnonce == "" || c.Nc < 1 || c.Opaque != "owned-opaque" {
		return nil, fmt.Errorf("actual digest fields disagree with method/target/owned challenge: %+v", c)
	}
	hash := func(value string) string {
		if algorithm == "SHA-256" {
			valueHash := sha256.Sum256([]byte(value))
			return hex.EncodeToString(valueHash[:])
		}
		valueHash := md5.Sum([]byte(value))
		return hex.EncodeToString(valueHash[:])
	}
	a1 := hash(clientBehaviorUser + ":" + clientBehaviorRealm + ":" + clientBehaviorPassword)
	a2 := o.method + ":" + o.uri
	if qop == "auth-int" {
		a2 += ":" + hash(o.body)
	}
	want := hash(fmt.Sprintf("%s:%s:%08x:%s:%s:%s", a1, nonce, c.Nc, c.Cnonce, qop, hash(a2)))
	if c.Response != want {
		return nil, fmt.Errorf("actual digest response=%q want independently computed=%q", c.Response, want)
	}
	return c, nil
}

func clientBehaviorDigestPeer(t *testing.T, algorithm, qop, nonce string) *clientBehaviorPeer {
	t.Helper()
	return newClientBehaviorPeer(t, func(w http.ResponseWriter, _ *http.Request, o clientBehaviorObservation) {
		if o.header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", clientBehaviorChallenge(algorithm, qop, nonce))
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "owned initial challenge payload")
			return
		}
		if _, err := clientBehaviorCredentials(o, algorithm, qop, nonce); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "authorized:"+o.body)
	})
}
