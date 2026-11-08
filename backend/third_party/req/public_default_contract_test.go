package req_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	req "github.com/imroc/req/v3"
)

// These public API contracts use their own wire peer. They intentionally run
// sequentially because the package-level wrappers share the default client.
type publicDefaultExchange struct {
	Method     string
	URI        string
	Header     http.Header
	Body       string
	ReadError  error
	WriteError error
}

type publicDefaultPeer struct {
	server    *httptest.Server
	exchanges chan publicDefaultExchange
	requests  atomic.Int32
	overflow  atomic.Bool
}

func newPublicDefaultPeer(t *testing.T, status int, contentType, response string) *publicDefaultPeer {
	t.Helper()
	p := &publicDefaultPeer{exchanges: make(chan publicDefaultExchange, 8)}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.requests.Add(1)
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 65537))
		if len(body) > 65536 {
			readErr = fmt.Errorf("public contract peer request exceeds fixture bound")
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, writeErr := io.WriteString(w, response)
		e := publicDefaultExchange{Method: r.Method, URI: r.RequestURI,
			Header: r.Header.Clone(), Body: string(body), ReadError: readErr, WriteError: writeErr}
		select {
		case p.exchanges <- e:
		default:
			p.overflow.Store(true)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *publicDefaultPeer) exchange(t *testing.T) publicDefaultExchange {
	t.Helper()
	select {
	case e := <-p.exchanges:
		if p.overflow.Load() {
			t.Fatal("PUBLIC_DEFAULT_FIXTURE: unexpected request overflow; all request starts remain counted")
		}
		if e.ReadError != nil || e.WriteError != nil {
			t.Fatalf("PUBLIC_DEFAULT_FIXTURE: read=%v write=%v", e.ReadError, e.WriteError)
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("PUBLIC_DEFAULT_FIXTURE: completed real peer exchange missing")
		return publicDefaultExchange{}
	}
}

func installPublicDefault(t *testing.T, base string) *req.Client {
	t.Helper()
	previous := req.DefaultClient()
	client := req.C().SetProxy(nil).EnableForceHTTP1().DisableAutoDecode().SetTimeout(3 * time.Second)
	req.SetDefaultClient(client)
	req.SetBaseURL(base)
	t.Cleanup(func() {
		req.SetDefaultClient(previous)
		client.CloseIdleConnections()
	})
	return client
}

func publicDefaultResponse(t *testing.T, response *req.Response, err error, status int, body string) {
	t.Helper()
	if err != nil || response == nil {
		t.Fatalf("PUBLIC_DEFAULT_ASSERT: response=%v error=%v", response, err)
	}
	if response.StatusCode != status || response.String() != body {
		t.Fatalf("PUBLIC_DEFAULT_ASSERT: status=%d body=%q, want %d %q", response.StatusCode, response.String(), status, body)
	}
}

func TestReqPublicDefaultContract(t *testing.T) {
	t.Run("replacement_binds_new_calls_only", func(t *testing.T) {
		a := newPublicDefaultPeer(t, http.StatusOK, "text/plain", "peer-A")
		b := newPublicDefaultPeer(t, http.StatusOK, "text/plain", "peer-B")
		first := installPublicDefault(t, a.server.URL)
		req.SetCommonHeader("X-Default", "A")
		held := req.SetHeader("X-Call", "held")
		req.SetDefaultClient(nil)
		if req.DefaultClient() != first {
			t.Fatal("PUBLIC_DEFAULT_ASSERT: nil replacement discarded the active default client")
		}
		installPublicDefault(t, b.server.URL)
		req.SetCommonHeader("X-Default", "B")
		response, err := req.Get("/new")
		publicDefaultResponse(t, response, err, http.StatusOK, "peer-B")
		response, err = held.Get("/held")
		publicDefaultResponse(t, response, err, http.StatusOK, "peer-A")
		ea, eb := a.exchange(t), b.exchange(t)
		if ea.Method != "GET" || ea.URI != "/held" || ea.Header.Get("X-Default") != "A" || ea.Header.Get("X-Call") != "held" ||
			eb.Method != "GET" || eb.URI != "/new" || eb.Header.Get("X-Default") != "B" || eb.Header.Get("X-Call") != "" ||
			a.requests.Load() != 1 || b.requests.Load() != 1 {
			t.Fatalf("PUBLIC_DEFAULT_ASSERT: incorrect default routing/leakage: A=%+v B=%+v counts=%d/%d", ea, eb, a.requests.Load(), b.requests.Load())
		}
	})

	t.Run("escaped_path_query_and_header_precedence", func(t *testing.T) {
		peer := newPublicDefaultPeer(t, http.StatusOK, "text/plain", "escaped-ok")
		installPublicDefault(t, peer.server.URL)
		req.SetCommonPathParam("tenant", "team space")
		req.SetCommonPathParam("item", "must-not-win")
		req.SetCommonQueryString("scope=common&retain=kept")
		req.SetCommonHeader("X-Layer", "common")
		req.SetCommonHeader("X-Retain", "common-only")
		response, err := req.SetPathParam("item", "a/b ?#").SetQueryParam("scope", "local +&").
			AddQueryParams("tag", "x/y", "two words").SetHeader("X-Layer", "request").
			Get("/v1/{tenant}/{item}?fixed=one%2Btwo")
		publicDefaultResponse(t, response, err, http.StatusOK, "escaped-ok")
		e := peer.exchange(t)
		const wantURI = "/v1/team%20space/a%2Fb%20%3F%23?fixed=one%2Btwo&retain=kept&scope=local+%2B%26&tag=x%2Fy&tag=two+words"
		if e.Method != "GET" || e.URI != wantURI || e.Body != "" || e.Header.Get("X-Layer") != "request" || e.Header.Get("X-Retain") != "common-only" || peer.requests.Load() != 1 {
			t.Fatalf("PUBLIC_DEFAULT_ASSERT: escaping/default precedence differs: %+v count=%d", e, peer.requests.Load())
		}
	})

	t.Run("form_auth_and_cookie_wire", func(t *testing.T) {
		peer := newPublicDefaultPeer(t, http.StatusCreated, "text/plain", "form-created")
		installPublicDefault(t, peer.server.URL)
		req.SetCommonBasicAuth("alice", "p:ss")
		req.SetCommonCookies(&http.Cookie{Name: "common", Value: "c1"})
		req.SetCommonFormData(map[string]string{"source": "default"})
		response, err := req.SetFormDataFromValues(url.Values{"name": {"A & B"}, "tag": {"one", "two words"}}).
			SetCookies(&http.Cookie{Name: "local", Value: "l1"}).Post("/form")
		publicDefaultResponse(t, response, err, http.StatusCreated, "form-created")
		e := peer.exchange(t)
		if e.Method != "POST" || e.URI != "/form" || e.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
			e.Header.Get("Authorization") != "Basic YWxpY2U6cDpzcw==" || e.Header.Get("Cookie") != "local=l1; common=c1" ||
			e.Body != "name=A+%26+B&source=default&tag=one&tag=two+words" || peer.requests.Load() != 1 {
			t.Fatalf("PUBLIC_DEFAULT_ASSERT: form/auth/cookie wire differs: %+v count=%d", e, peer.requests.Load())
		}
	})

	t.Run("json_request_and_success_result", func(t *testing.T) {
		const wireResponse = `{"id":7,"label":"stored"}`
		peer := newPublicDefaultPeer(t, http.StatusCreated, "application/json", wireResponse)
		installPublicDefault(t, peer.server.URL)
		req.SetCommonBearerAuthToken("request-token")
		var result struct {
			ID    int    `json:"id"`
			Label string `json:"label"`
		}
		payload := struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		}{Name: "wire + /", Count: 2}
		response, err := req.SetBodyJsonMarshal(payload).SetSuccessResult(&result).Post("/objects")
		publicDefaultResponse(t, response, err, http.StatusCreated, wireResponse)
		e := peer.exchange(t)
		if e.Method != "POST" || e.URI != "/objects" || e.Header.Get("Content-Type") != "application/json; charset=utf-8" ||
			e.Header.Get("Authorization") != "Bearer request-token" || e.Body != `{"name":"wire + /","count":2}` ||
			result.ID != 7 || result.Label != "stored" || response.SuccessResult() != &result || response.ErrorResult() != nil || !response.IsSuccessState() || peer.requests.Load() != 1 {
			t.Fatalf("PUBLIC_DEFAULT_ASSERT: JSON request/result differs: exchange=%+v result=%+v count=%d", e, result, peer.requests.Load())
		}
	})

	t.Run("http_error_result_preserves_body", func(t *testing.T) {
		const wireResponse = `{"code":"invalid","detail":"literal rejection"}`
		peer := newPublicDefaultPeer(t, http.StatusUnprocessableEntity, "application/json", wireResponse)
		installPublicDefault(t, peer.server.URL)
		var problem struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		success := struct {
			Value string `json:"value"`
		}{Value: "untouched"}
		response, err := req.SetErrorResult(&problem).SetSuccessResult(&success).Get("/invalid")
		publicDefaultResponse(t, response, err, http.StatusUnprocessableEntity, wireResponse)
		e := peer.exchange(t)
		if e.Method != "GET" || e.URI != "/invalid" || e.Body != "" || problem.Code != "invalid" || problem.Detail != "literal rejection" ||
			success.Value != "untouched" || response.ErrorResult() != &problem || response.SuccessResult() != nil || !response.IsErrorState() || peer.requests.Load() != 1 {
			t.Fatalf("PUBLIC_DEFAULT_ASSERT: HTTP error decoding differs: exchange=%+v problem=%+v success=%+v count=%d", e, problem, success, peer.requests.Load())
		}
	})

	t.Run("context_cancellation_reaches_peer", func(t *testing.T) {
		entered := make(chan publicDefaultExchange, 1)
		peerCanceled := make(chan error, 1)
		release := make(chan struct{})
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			select {
			case entered <- publicDefaultExchange{Method: r.Method, URI: r.RequestURI, Header: r.Header.Clone()}:
			default:
			}
			select {
			case <-r.Context().Done():
				select {
				case peerCanceled <- r.Context().Err():
				default:
				}
			case <-release:
			}
		}))
		t.Cleanup(server.Close)
		t.Cleanup(func() { close(release) })
		installPublicDefault(t, server.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		t.Cleanup(cancel)
		finished := make(chan error, 1)
		go func() {
			_, err := req.SetContext(ctx).Get("/wait")
			finished <- err
		}()
		select {
		case e := <-entered:
			if e.Method != "GET" || e.URI != "/wait" {
				t.Fatalf("PUBLIC_DEFAULT_ASSERT: cancellation request differs: %+v", e)
			}
		case <-ctx.Done():
			t.Fatal("PUBLIC_DEFAULT_FIXTURE: peer did not receive the cancellable request")
		}
		cancel()
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("PUBLIC_DEFAULT_ASSERT: cancellation error=%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("PUBLIC_DEFAULT_ASSERT: canceled request failed to return")
		}
		select {
		case err := <-peerCanceled:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("PUBLIC_DEFAULT_ASSERT: peer context error=%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("PUBLIC_DEFAULT_ASSERT: cancellation did not close the real request at peer")
		}
		if requests.Load() != 1 {
			t.Fatalf("PUBLIC_DEFAULT_ASSERT: cancellation requests=%d", requests.Load())
		}
	})
}
