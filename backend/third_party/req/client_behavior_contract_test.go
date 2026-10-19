// Public HTTP contracts adapted from the pinned sources listed in
// CLIENT-BEHAVIOR-PROVENANCE.json. See CLIENT-BEHAVIOR-LICENSE.
package req

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReqClientDigest_ReplayAndCache(t *testing.T) {
	for _, tc := range []struct {
		name, algorithm, qop, method, body string
		stream bool
	}{
		{name: "md5_post", algorithm: "MD5", qop: "auth", method: http.MethodPost, body: "owned payload + &"},
		{name: "sha256_auth_int", algorithm: "SHA-256", qop: "auth-int", method: http.MethodPost, body: "entity hash includes these bytes"},
		{name: "bodyless_get", algorithm: "MD5", qop: "auth", method: http.MethodGet},
		{name: "stream_without_getbody", algorithm: "MD5", qop: "auth", method: http.MethodPost, body: "one owned stream", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := clientBehaviorDigestPeer(t, tc.algorithm, tc.qop, "nonce-replay")
			client := newClientBehaviorClient(t).SetCommonDigestAuth(clientBehaviorUser, clientBehaviorPassword)
			req := clientBehaviorRequest(t, tc.method, peer.url+"/escaped%2Ftarget?x=a%2Bb&y=2", tc.body)
			var source *clientBehaviorSource
			var factoryBodies []*clientBehaviorSource
			var factoryMu sync.Mutex
			if tc.qop == "auth-int" {
				req.GetBody = func() (io.ReadCloser, error) {
					body := newClientBehaviorSource(tc.body)
					factoryMu.Lock()
					factoryBodies = append(factoryBodies, body)
					factoryMu.Unlock()
					return body, nil
				}
			}
			if tc.stream {
				source = newClientBehaviorSource(tc.body)
				req.Body, req.GetBody, req.ContentLength = source, nil, -1
			}
			resp, err := client.Do(req)
			if got := clientBehaviorBody(t, resp, err); got != "authorized:"+tc.body {
				t.Fatalf("authenticated replay body=%q", got)
			}
			closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancelClose)
			if source != nil { clientBehaviorAwaitSourceClose(t, closeCtx, source, "buffered original stream") }
			if tc.qop == "auth-int" {
				factoryMu.Lock()
				opened := append([]*clientBehaviorSource(nil), factoryBodies...)
				factoryMu.Unlock()
				if len(opened) == 0 { t.Fatal("auth-int request did not use the public body factory") }
				for i, body := range opened { clientBehaviorAwaitSourceClose(t, closeCtx, body, fmt.Sprintf("factory body%d", i)) }
			}
			if req.Header.Get("Authorization") != "" {
				t.Fatal("digest wrapper mutated the caller's Authorization")
			}
			second := clientBehaviorRequest(t, tc.method, req.URL.String(), tc.body)
			resp, err = client.Do(second)
			if got := clientBehaviorBody(t, resp, err); got != "authorized:"+tc.body {
				t.Fatalf("cached request body=%q", got)
			}
			seen := peer.observations(t, 3)
			for i, o := range seen {
				if o.method != tc.method || o.uri != "/escaped%2Ftarget?x=a%2Bb&y=2" || o.body != tc.body || o.err != nil {
					t.Fatalf("wire request %d differs from caller: %+v", i, o)
				}
				if i > 0 {
					cred, err := clientBehaviorCredentials(o, tc.algorithm, tc.qop, "nonce-replay")
					if err != nil || cred.Nc != i {
						t.Fatalf("wire request %d nonce count/hash: cred=%+v err=%v", i, cred, err)
					}
				}
				if o.connID != seen[0].connID {
					t.Fatalf("challenge response was not drained for actual connection reuse: %+v", seen)
				}
			}
		})
	}
}

func TestReqClientDigest_NonceRotation(t *testing.T) {
	peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, _ *http.Request, o clientBehaviorObservation) {
		nonce := "nonce-old"
		if o.uri == "/rotated" {
			nonce = "nonce-new"
		}
		cred, err := clientBehaviorCredentials(o, "MD5", "auth", nonce)
		if err != nil {
			w.Header().Set("WWW-Authenticate", clientBehaviorChallenge("MD5", "auth", nonce))
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "rotate this owned nonce")
			return
		}
		_, _ = fmt.Fprintf(w, "accepted:%s:%d", nonce, cred.Nc)
	})
	client := newClientBehaviorClient(t).SetCommonDigestAuth(clientBehaviorUser, clientBehaviorPassword)
	for i, path := range []string{"/initial", "/rotated"} {
		resp, err := client.Do(clientBehaviorRequest(t, http.MethodGet, peer.url+path, ""))
		want := "accepted:nonce-old:1"
		if i == 1 { want = "accepted:nonce-new:1" }
		if got := clientBehaviorBody(t, resp, err); got != want { t.Fatalf("nonce response=%q want=%q", got, want) }
	}
	seen := peer.observations(t, 4)
	old, err := clientBehaviorCredentials(seen[2], "MD5", "auth", "nonce-old")
	if err != nil || old.Nc != 2 { t.Fatalf("cached rejected request: %+v %v", old, err) }
	fresh, err := clientBehaviorCredentials(seen[3], "MD5", "auth", "nonce-new")
	if err != nil || fresh.Nc != 1 { t.Fatalf("rotated request: %+v %v", fresh, err) }
}

// A normal 401 remains a readable HTTP response even when an earlier digest
// challenge was cached. A closed response body is not an acceptable substitute.
func TestReqClientDigest_NoChallengeEvictsCache(t *testing.T) {
	writes := make(chan clientBehaviorWriteObservation, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, _ *http.Request, o clientBehaviorObservation) {
		if o.uri != "/prime" {
			w.WriteHeader(http.StatusUnauthorized)
			clientBehaviorObserveWrite(w, o.uri, "owned ordinary unauthorized body", writes)
			return
		}
		if o.header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", clientBehaviorChallenge("MD5", "auth", "nonce-prime"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "primed")
	})
	client := newClientBehaviorClient(t).SetCommonDigestAuth(clientBehaviorUser, clientBehaviorPassword)
	resp, err := client.Do(clientBehaviorRequest(t, http.MethodGet, peer.url+"/prime", ""))
	if got := clientBehaviorBody(t, resp, err); got != "primed" { t.Fatalf("prime body=%q", got) }
	for i, path := range []string{"/expired", "/after-eviction"} {
		resp, err = client.Do(clientBehaviorRequest(t, http.MethodGet, peer.url+path, ""))
		clientBehaviorAwaitWrite(t, ctx, writes, path, "owned ordinary unauthorized body")
		peer.observations(t, i+3)
		clientBehaviorReadable401(t, resp, err, "owned ordinary unauthorized body")
	}
	seen := peer.observations(t, 4)
	if seen[2].header.Get("Authorization") == "" || seen[3].header.Get("Authorization") != "" { t.Fatalf("nonce cache not evicted on ordinary 401: %+v", seen) }
}

func TestReqClientDigest_BoundedRejection(t *testing.T) {
	peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, _ *http.Request, _ clientBehaviorObservation) {
		w.Header().Set("WWW-Authenticate", clientBehaviorChallenge("MD5", "auth", "nonce-wrong-password"))
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "still unauthorized")
	})
	client := newClientBehaviorClient(t).SetCommonDigestAuth(clientBehaviorUser, "wrong-owned-password")
	resp, err := client.Do(clientBehaviorRequest(t, http.MethodPost, peer.url+"/reject", "must replay once"))
	if got := clientBehaviorBody(t, resp, err); got != "still unauthorized" || resp.StatusCode != http.StatusUnauthorized { t.Fatalf("rejected response=%v body=%q", resp, got) }
	seen := peer.observations(t, 2)
	if seen[0].header.Get("Authorization") != "" || seen[1].header.Get("Authorization") == "" || seen[0].body != "must replay once" || seen[1].body != seen[0].body { t.Fatalf("bounded challenge/replay: %+v", seen) }
}

func TestReqClientDigest_GetBodyFailure(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("clone_%d", failAt), func(t *testing.T) {
			peer := clientBehaviorDigestPeer(t, "MD5", "auth", "nonce-clone")
			client := newClientBehaviorClient(t).SetCommonDigestAuth(clientBehaviorUser, clientBehaviorPassword)
			sentinel := errors.New("owned body factory failure")
			calls := 0
			var factoryMu sync.Mutex
			req := clientBehaviorRequest(t, http.MethodPost, peer.url+"/clone", "owned body")
			original := newClientBehaviorSource("owned body")
			req.Body = original
			var opened []*clientBehaviorSource
			req.GetBody = func() (io.ReadCloser, error) {
				factoryMu.Lock()
				defer factoryMu.Unlock()
				calls++
				if calls == failAt { return nil, sentinel }
				body := newClientBehaviorSource("owned body")
				opened = append(opened, body)
				return body, nil
			}
			resp, err := client.Do(req)
			if resp != nil && resp.Body != nil { _ = resp.Body.Close() }
			factoryMu.Lock()
			actualCalls := calls
			created := append([]*clientBehaviorSource(nil), opened...)
			factoryMu.Unlock()
			if !errors.Is(err, sentinel) || actualCalls != failAt { t.Fatalf("GetBody failure: resp=%v err=%v calls=%d", resp, err, actualCalls) }
			peer.observations(t, failAt-1)
			closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancelClose)
			for i, body := range created { clientBehaviorAwaitSourceClose(t, closeCtx, body, fmt.Sprintf("already opened replay body%d", i)) }
			clientBehaviorAwaitSourceClose(t, closeCtx, original, "caller body on failed public request")
			if req.Header.Get("Authorization") != "" { t.Fatal("clone failure mutated caller auth") }
		})
	}
}

func TestReqClientDigest_ConcurrentCachedNonce(t *testing.T) {
	peer := clientBehaviorDigestPeer(t, "MD5", "auth", "nonce-parallel")
	client := newClientBehaviorClient(t).SetCommonDigestAuth(clientBehaviorUser, clientBehaviorPassword)
	resp, err := client.Do(clientBehaviorRequest(t, http.MethodGet, peer.url+"/prime", ""))
	if got := clientBehaviorBody(t, resp, err); got != "authorized:" { t.Fatalf("prime=%q", got) }
	const workers = 8
	results := make(chan error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, err := http.NewRequest(http.MethodGet, peer.url+"/parallel", nil)
			if err != nil { results <- err; return }
			resp, err := client.Do(req)
			if err != nil { results <- err; return }
			body, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			if readErr != nil || closeErr != nil || resp.StatusCode != http.StatusOK || string(body) != "authorized:" { results <- fmt.Errorf("parallel response status=%d body=%q read=%v close=%v", resp.StatusCode, body, readErr, closeErr); return }
			results <- nil
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results { if err != nil { t.Fatal(err) } }
	seen := peer.observations(t, workers+2)
	counts := make(map[int]bool)
	for _, o := range seen[2:] {
		cred, err := clientBehaviorCredentials(o, "MD5", "auth", "nonce-parallel")
		if err != nil || cred.Nc < 2 || cred.Nc > workers+1 || counts[cred.Nc] { t.Fatalf("concurrent cached credentials=%+v err=%v previous=%v", cred, err, counts) }
		counts[cred.Nc] = true
	}
}

func TestReqClientDigest_ChallengeBoundary(t *testing.T) {
	for _, tc := range []struct { name, challenge string; malformed bool }{
		{name: "basic_only", challenge: `Basic realm="owned-realm"`},
		{name: "unsupported_digest", challenge: `Digest realm="owned-realm", nonce="owned-nonce", algorithm=OWNED-UNSUPPORTED, qop="auth"`},
		{name: "malformed_digest", challenge: `Digest realm="unterminated`, malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := make(chan clientBehaviorWriteObservation, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, _ *http.Request, o clientBehaviorObservation) {
				w.Header().Set("WWW-Authenticate", tc.challenge)
				w.WriteHeader(http.StatusUnauthorized)
				clientBehaviorObserveWrite(w, o.uri, "owned unhandled challenge", writes)
			})
			client := newClientBehaviorClient(t).SetCommonDigestAuth(clientBehaviorUser, clientBehaviorPassword)
			req := clientBehaviorRequest(t, http.MethodPost, peer.url+"/challenge-boundary", "owned caller payload")
			req.Header.Set("X-Owned-Caller", "preserved")
			resp, err := client.Do(req)
			clientBehaviorAwaitWrite(t, ctx, writes, "/challenge-boundary", "owned unhandled challenge")
			peer.observations(t, 1)
			if tc.malformed {
				if resp != nil && resp.Body != nil { _ = resp.Body.Close() }
				if err == nil || !strings.Contains(err.Error(), "digest: invalid challenge:") { t.Fatalf("malformed wire challenge response=%v err=%v", resp, err) }
			} else {
				clientBehaviorReadable401(t, resp, err, "owned unhandled challenge")
			}
			seen := peer.observations(t, 1)
			if seen[0].body != "owned caller payload" || seen[0].header.Get("Authorization") != "" || req.Header.Get("Authorization") != "" || req.Header.Get("X-Owned-Caller") != "preserved" { t.Fatalf("challenge boundary mutated/replayed caller request: %+v caller=%v", seen, req.Header) }
		})
	}
}
