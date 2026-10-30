// Child1743 regression contracts: see DIGEST-OWNERSHIP-PROVENANCE.json.
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

func TestReqDigestOwnership_OriginalBody(t *testing.T) {
	for _, name := range []string{"getbody_success", "getbody_close_error_preserves_success", "stream_success",
		"stream_read_error", "stream_read_and_close_error", "stream_close_error", "nil_body", "http_no_body"} {
		t.Run(name, func(t *testing.T) {
			const payload = "distinct owned request payload"
			peer := clientBehaviorDigestPeer(t, "MD5", "auth", "ownership-original")
			client := newClientBehaviorClient(t).SetCommonDigestAuth(clientBehaviorUser, clientBehaviorPassword)
			request := clientBehaviorRequest(t, http.MethodPost, peer.url+"/original", payload)
			var ledger digestBodyLedger
			readFailure, closeFailure := errors.New("owned original read failure"), errors.New("owned original Close failure")
			var wantError error
			wantWire, wantBody := 2, payload
			switch name {
			case "nil_body", "http_no_body":
				request.Body, request.GetBody, request.ContentLength = nil, nil, 0
				if name == "http_no_body" {
					request.Body = http.NoBody
				}
				wantBody = ""
			default:
				var readErr, closeErr error
				if name == "stream_read_error" || name == "stream_read_and_close_error" {
					readErr, wantError, wantWire = readFailure, readFailure, 0
				}
				if name == "stream_close_error" || name == "stream_read_and_close_error" || name == "getbody_close_error_preserves_success" {
					closeErr = closeFailure
				}
				if name == "stream_close_error" {
					wantError, wantWire = closeFailure, 0
				}
				request.Body = ledger.body("original", payload, readErr, closeErr)
				if strings.HasPrefix(name, "stream_") {
					request.GetBody, request.ContentLength = nil, -1
				} else {
					request.GetBody = func() (io.ReadCloser, error) {
						return ledger.body("replay factory body", payload, nil, nil), nil
					}
				}
			}
			resp, err := client.Do(request)
			if wantError != nil {
				if resp != nil && resp.Body != nil {
					_ = resp.Body.Close()
				}
				if !errors.Is(err, wantError) {
					t.Fatalf("primary ReadAll/Close error=%v want=%v", err, wantError)
				}
			} else {
				digestOwnershipResponse(t, resp, err, "authorized:"+wantBody)
			}
			seen := peer.observations(t, wantWire)
			for _, observation := range seen {
				if observation.body != wantBody || observation.err != nil {
					t.Fatalf("actual original/replay wire payload=%+v", observation)
				}
			}
			if request.Header.Get("Authorization") != "" {
				t.Fatal("original caller Authorization changed")
			}
			ledger.verify(t)
		})
	}
}

func TestReqDigestOwnership_FactoryErrorBody(t *testing.T) {
	for _, name := range []string{"first_clone", "second_clone", "fresh_auth_int_prepare", "cached_auth_int_prepare"} {
		t.Run(name, func(t *testing.T) {
			const payload = "error factory uses a distinct owned reader"
			qop, failAt, wantWire := "auth", 1, 0
			if name == "second_clone" {
				failAt, wantWire = 2, 1
			}
			if name == "fresh_auth_int_prepare" {
				qop, failAt, wantWire = "auth-int", 3, 1
			}
			if name == "cached_auth_int_prepare" {
				qop, failAt, wantWire = "auth-int", 2, 2
			}
			peer := clientBehaviorDigestPeer(t, "MD5", qop, "ownership-factory")
			client := newClientBehaviorClient(t).SetCommonDigestAuth(clientBehaviorUser, clientBehaviorPassword)
			if name == "cached_auth_int_prepare" {
				digestOwnershipPrime(t, client, peer)
			}
			var ledger digestBodyLedger
			primary, cleanup := errors.New("owned factory primary failure"), errors.New("owned error-reader Close failure")
			request := clientBehaviorRequest(t, http.MethodPost, peer.url+"/factory", payload)
			request.Body = ledger.body("original", payload, nil, cleanup)
			var factoryMu sync.Mutex
			calls := 0
			request.GetBody = func() (io.ReadCloser, error) {
				factoryMu.Lock()
				defer factoryMu.Unlock()
				calls++
				if calls == failAt {
					return ledger.body("reader returned with factory error", payload, nil, cleanup), primary
				}
				closeErr := error(nil)
				if (name == "fresh_auth_int_prepare" && calls == 2) || (name == "cached_auth_int_prepare" && calls == 1) {
					closeErr = cleanup // only an unsent clone; never a sent transport body
				}
				return ledger.body(fmt.Sprintf("successful factory body%d", calls), payload, nil, closeErr), nil
			}
			resp, err := client.Do(request)
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			factoryMu.Lock()
			actualCalls := calls
			factoryMu.Unlock()
			if !errors.Is(err, primary) || errors.Is(err, cleanup) || actualCalls != failAt {
				t.Fatalf("selected factory failure err=%v primary=%v cleanup=%v calls=%d boundary=%d", err, primary, cleanup, actualCalls, failAt)
			}
			peer.observations(t, wantWire)
			if request.Header.Get("Authorization") != "" {
				t.Fatal("error factory changed caller Authorization")
			}
			ledger.verify(t)
		})
	}
}

func TestReqDigestOwnership_PrepareReaders(t *testing.T) {
	for _, timing := range []string{"fresh", "cached"} {
		for _, mode := range []string{"read_error", "read_and_close_error", "close_error_only"} {
			t.Run(timing+"_"+mode, func(t *testing.T) {
				const payload = "actual auth-int entity payload"
				peer := clientBehaviorDigestPeer(t, "SHA-256", "auth-int", "ownership-hashing")
				client := newClientBehaviorClient(t).SetCommonDigestAuth(clientBehaviorUser, clientBehaviorPassword)
				hashAt, wireBefore := 3, 0
				if timing == "cached" {
					digestOwnershipPrime(t, client, peer)
					hashAt, wireBefore = 2, 2
				}
				var ledger digestBodyLedger
				readFailure, closeFailure := errors.New("owned hash reader Read failure"), errors.New("owned hash/unsent Close failure")
				request := clientBehaviorRequest(t, http.MethodPost, peer.url+"/hashing", payload)
				request.Body = ledger.body("original", payload, nil, closeFailure)
				var factoryMu sync.Mutex
				calls := 0
				request.GetBody = func() (io.ReadCloser, error) {
					factoryMu.Lock()
					defer factoryMu.Unlock()
					calls++
					if calls == hashAt {
						var readErr, closeErr error
						if mode != "close_error_only" {
							readErr = readFailure
						}
						if mode != "read_error" {
							closeErr = closeFailure
						}
						return ledger.body("helper-owned hashing reader", payload, readErr, closeErr), nil
					}
					var closeErr error
					if mode != "close_error_only" && calls == hashAt-1 {
						closeErr = closeFailure // unsent clone only when hashing will fail
					}
					return ledger.body(fmt.Sprintf("replay factory body%d", calls), payload, nil, closeErr), nil
				}
				resp, err := client.Do(request)
				wantWire := wireBefore
				if timing == "fresh" {
					wantWire++ // the real initial challenge
				}
				if mode == "close_error_only" {
					digestOwnershipResponse(t, resp, err, "authorized:"+payload)
					wantWire++
				} else {
					if resp != nil && resp.Body != nil {
						_ = resp.Body.Close()
					}
					if !errors.Is(err, readFailure) || errors.Is(err, closeFailure) {
						t.Fatalf("auth-int primary Read error=%v must retain=%v and not Close=%v", err, readFailure, closeFailure)
					}
				}
				factoryMu.Lock()
				actualCalls := calls
				factoryMu.Unlock()
				if actualCalls < hashAt {
					t.Fatalf("selected hashing boundary not reached: calls=%d boundary=%d", actualCalls, hashAt)
				}
				peer.observations(t, wantWire)
				if request.Header.Get("Authorization") != "" {
					t.Fatal("hash preparation changed caller Authorization")
				}
				ledger.verify(t)
			})
		}
	}
}

func TestReqDigestOwnership_ResponseBody(t *testing.T) {
	for _, name := range []string{"ordinary_401_caller_owned", "basic_only_caller_owned", "unsupported_digest_caller_owned",
		"malformed_closed_once", "replay_challenge_closed_once"} {
		t.Run(name, func(t *testing.T) {
			const payload = "owned actual unauthorized payload"
			const success = "owned authenticated response"
			writes := make(chan clientBehaviorWriteObservation, 2)
			peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, _ *http.Request, observation clientBehaviorObservation) {
				if name == "replay_challenge_closed_once" && observation.header.Get("Authorization") != "" {
					if _, err := clientBehaviorCredentials(observation, "MD5", "auth", "ownership-response"); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					clientBehaviorObserveWrite(w, observation.uri, success, writes)
					return
				}
				switch name {
				case "basic_only_caller_owned":
					w.Header().Set("WWW-Authenticate", `Basic realm="owned-realm"`)
				case "unsupported_digest_caller_owned":
					w.Header().Set("WWW-Authenticate", `Digest realm="owned", nonce="owned", algorithm=OWNED-UNSUPPORTED, qop="auth"`)
				case "malformed_closed_once":
					w.Header().Set("WWW-Authenticate", `Digest realm="unterminated`)
				case "replay_challenge_closed_once":
					w.Header().Set("WWW-Authenticate", clientBehaviorChallenge("MD5", "auth", "ownership-response"))
				}
				w.WriteHeader(http.StatusUnauthorized)
				clientBehaviorObserveWrite(w, observation.uri, payload, writes)
			})
			client := newClientBehaviorClient(t)
			var observed digestResponseLedger
			observed.observe(client) // inner real-peer body observer before Digest registration
			client.SetCommonDigestAuth(clientBehaviorUser, clientBehaviorPassword)
			request := clientBehaviorRequest(t, http.MethodGet, peer.url+"/response-owned", "")
			resp, err := client.Do(request)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			clientBehaviorAwaitWrite(t, ctx, writes, "/response-owned", payload)
			wantCount := 1
			if name == "replay_challenge_closed_once" {
				wantCount = 2
				clientBehaviorAwaitWrite(t, ctx, writes, "/response-owned", success)
			}
			peer.observations(t, wantCount)
			var failures []string
			if name == "malformed_closed_once" {
				if resp != nil && resp.Body != nil {
					_ = resp.Body.Close()
				}
				if err == nil || !strings.Contains(err.Error(), "digest: invalid challenge:") {
					t.Fatalf("actual malformed challenge err=%v response=%v", err, resp)
				}
			} else {
				if err != nil || resp == nil || resp.Body == nil {
					t.Fatalf("owned response/status fixture: response=%v err=%v", resp, err)
				}
				body, readErr := io.ReadAll(resp.Body)
				closeErr := resp.Body.Close() // one caller Close, with no second cleanup
				wantBody, wantStatus := payload, http.StatusUnauthorized
				if wantCount == 2 {
					wantBody, wantStatus = success, http.StatusOK
				}
				if readErr != nil || closeErr != nil || string(body) != wantBody || resp.StatusCode != wantStatus {
					failures = append(failures, fmt.Sprintf("caller body=%q read=%v close=%v status=%d want=%q/%d", body, readErr, closeErr, resp.StatusCode, wantBody, wantStatus))
				}
			}
			for i, body := range observed.observations(t, wantCount) {
				select {
				case <-body.closed:
				case <-ctx.Done():
				}
				wantBytes, wantStatus := len(payload), http.StatusUnauthorized
				if i == 1 {
					wantBytes, wantStatus = len(success), http.StatusOK
				}
				if body.calls.Load() != 1 || body.readBytes.Load() != int64(wantBytes) || body.status != wantStatus || body.uri != "/response-owned" {
					failures = append(failures, fmt.Sprintf("response%d Close=%d bytes=%d status=%d uri=%s want1/%d/%d", i, body.calls.Load(), body.readBytes.Load(), body.status, body.uri, wantBytes, wantStatus))
				}
			}
			if len(failures) != 0 {
				t.Fatalf("DIGEST_OWNERSHIP_RESPONSE_ASSERT: %s", strings.Join(failures, "; "))
			}
		})
	}
}
