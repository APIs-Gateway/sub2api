package req_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	req "github.com/imroc/req/v3"
)

func TestReqPublicAPI_ResultCodecs(t *testing.T) {
	type result struct {
		Value string `json:"value" xml:"value"`
	}
	for _, name := range []string{"deprecated_success_alias", "deprecated_error_alias", "common_error_type", "custom_result_state"} {
		t.Run(name, func(t *testing.T) {
			publicAPIInstall(t)
			status := 200
			if name == "deprecated_error_alias" || name == "common_error_type" {
				status = 422
			}
			if name == "custom_result_state" {
				status = 202
			}
			peer := publicAPINewPeer(t, func(int, publicAPIExchange) (int, string, string) {
				return status, "application/json", `{"value":"from-peer"}`
			})
			var value result
			var r *req.Request
			switch name {
			case "deprecated_success_alias":
				r = req.SetResult(&value)
			case "deprecated_error_alias":
				r = req.SetError(&value)
			case "common_error_type":
				req.SetCommonError(result{})
				r = req.R()
			case "custom_result_state":
				req.SetResultStateCheckFunc(func(response *req.Response) req.ResultState {
					if response.StatusCode == 202 {
						return req.ErrorState
					}
					return req.UnknownState
				})
				req.SetCommonErrorResult(result{})
				r = req.R()
			}
			response, err := r.Get(peer.server.URL + "/result")
			publicAPIResponse(t, response, err, status, `{"value":"from-peer"}`)
			peer.receive(t)
			if name == "deprecated_success_alias" {
				if response.Result() != &value || value.Value != "from-peer" || response.ErrorResult() != nil {
					t.Fatalf("success alias ownership result=%v value=%+v", response.Result(), value)
				}
			} else {
				decoded, ok := response.ErrorResult().(*result)
				if !ok || decoded.Value != "from-peer" || response.SuccessResult() != nil || !response.IsErrorState() {
					t.Fatalf("error ownership result=%v success=%v", response.ErrorResult(), response.SuccessResult())
				}
				if name == "deprecated_error_alias" && decoded != &value {
					t.Fatal("deprecated error alias replaced caller target")
				}
			}
			if peer.requests.Load() != 1 {
				t.Fatalf("unexpected attempts=%d", peer.requests.Load())
			}
		})
	}
	for _, name := range []string{"json_marshal_error", "xml_marshal_error", "json_unmarshal_error", "xml_unmarshal_error"} {
		t.Run(name, func(t *testing.T) {
			publicAPIInstall(t)
			sentinel := errors.New("owned codec sentinel")
			isXML := strings.HasPrefix(name, "xml")
			encode := strings.Contains(name, "marshal_error") && !strings.Contains(name, "unmarshal")
			ctype, reply := "application/json", `{"value":"from-peer"}`
			if isXML {
				ctype, reply = "application/xml", "<result><value>from-peer</value></result>"
			}
			peer := publicAPINewPeer(t, func(int, publicAPIExchange) (int, string, string) { return 200, ctype, reply })
			var value result
			var r *req.Request
			if encode {
				if isXML {
					req.SetXmlMarshal(func(any) ([]byte, error) { return nil, sentinel })
					r = req.SetBodyXmlMarshal(result{Value: "outgoing"})
				} else {
					req.SetJsonMarshal(func(any) ([]byte, error) { return nil, sentinel })
					r = req.SetBodyJsonMarshal(result{Value: "outgoing"})
				}
			} else {
				if isXML {
					req.SetXmlUnmarshal(func([]byte, any) error { return sentinel })
				} else {
					req.SetJsonUnmarshal(func([]byte, any) error { return sentinel })
				}
				r = req.SetSuccessResult(&value)
			}
			response, err := r.Post(peer.server.URL + "/codec")
			if response == nil || !errors.Is(err, sentinel) || !errors.Is(response.Err, sentinel) || value.Value != "" {
				t.Fatalf("codec error identity response=%v err=%v target=%+v", response, err, value)
			}
			if encode {
				if peer.requests.Load() != 0 {
					t.Fatal("failed encoding reached peer")
				}
			} else {
				e := peer.receive(t)
				if e.method != "POST" || response.StatusCode != 200 || response.String() != reply || peer.requests.Load() != 1 {
					t.Fatalf("decode failure lost real response: %+v response=%v", e, response)
				}
				if response.Body != nil {
					if closeErr := response.Body.Close(); closeErr != nil {
						t.Fatal(closeErr)
					}
				}
			}
		})
	}
	t.Run("body_transformer", func(t *testing.T) {
		publicAPIInstall(t)
		peer := publicAPIPlainPeer(t)
		var observed []byte
		var request *req.Request
		var transformedResponse *req.Response
		req.SetResponseBodyTransformer(func(raw []byte, r *req.Request, response *req.Response) ([]byte, error) {
			observed = append([]byte(nil), raw...)
			request = r
			transformedResponse = response
			return []byte(`{"value":"transformed"}`), nil
		})
		// The transformer changes bytes, not the peer's text/plain Content-Type.
		response, err := req.Get(peer.server.URL + "/transform")
		publicAPIResponse(t, response, err, 200, `{"value":"transformed"}`)
		peer.receive(t)
		if string(observed) != "public-api-response" || request != response.Request || transformedResponse != response {
			t.Fatal("transformer lost original bytes or request/response identity")
		}
	})
}

func TestReqPublicAPI_MiddlewarePolicies(t *testing.T) {
	t.Run("wrapper_composition", func(t *testing.T) {
		publicAPIInstall(t)
		peer := publicAPIPlainPeer(t)
		var calls []string
		req.WrapRoundTrip(func(next req.RoundTripper) req.RoundTripper {
			return req.RoundTripFunc(func(r *req.Request) (*req.Response, error) {
				calls = append(calls, "inner-before")
				response, err := next.RoundTrip(r)
				calls = append(calls, "inner-after")
				return response, err
			})
		})
		req.WrapRoundTripFunc(func(next req.RoundTripper) req.RoundTripFunc {
			return func(r *req.Request) (*req.Response, error) {
				calls = append(calls, "outer-before")
				response, err := next.RoundTrip(r)
				calls = append(calls, "outer-after")
				return response, err
			}
		})
		req.OnBeforeRequest(func(_ *req.Client, r *req.Request) error { r.SetHeader("X-Middleware", "before"); return nil })
		response, err := req.Get(peer.server.URL + "/wrapped")
		publicAPIResponse(t, response, err, 200, "public-api-response")
		e := peer.receive(t)
		if !reflect.DeepEqual(calls, []string{"outer-before", "inner-before", "inner-after", "outer-after"}) || e.headers.Get("X-Middleware") != "before" || peer.requests.Load() != 1 {
			t.Fatalf("wrapper order=%v exchange=%+v", calls, e)
		}
	})
	for _, name := range []string{"before_error_blocks_wire", "after_error_preserves_response", "dial_error_identity", "pre_canceled"} {
		t.Run(name, func(t *testing.T) {
			publicAPIInstall(t)
			peer := publicAPIPlainPeer(t)
			sentinel := errors.New("owned middleware failure")
			r := req.R()
			var dialCalls atomic.Int32
			switch name {
			case "before_error_blocks_wire":
				req.OnBeforeRequest(func(*req.Client, *req.Request) error { return sentinel })
			case "after_error_preserves_response":
				req.OnAfterResponse(func(*req.Client, *req.Response) error { return sentinel })
			case "dial_error_identity":
				req.SetDial(func(context.Context, string, string) (net.Conn, error) { dialCalls.Add(1); return nil, sentinel })
			case "pre_canceled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				r = req.SetContext(ctx)
				sentinel = context.Canceled
			}
			response, err := r.Get(peer.server.URL + "/error")
			if response == nil || !errors.Is(err, sentinel) {
				t.Fatalf("lost error cause response=%v err=%v", response, err)
			}
			if name == "after_error_preserves_response" {
				e := peer.receive(t)
				if response.StatusCode != 200 || response.String() != "public-api-response" || e.uri != "/error" {
					t.Fatal("after-response error lost successful wire response")
				}
				if response.Body != nil {
					if closeErr := response.Body.Close(); closeErr != nil {
						t.Fatal(closeErr)
					}
				}
			} else if peer.requests.Load() != 0 {
				t.Fatal("failed request unexpectedly reached HTTP peer")
			}
			if name == "dial_error_identity" && dialCalls.Load() != 1 {
				t.Fatalf("dial calls=%d", dialCalls.Load())
			}
		})
	}
	t.Run("jar_clear", func(t *testing.T) {
		publicAPIInstall(t)
		peer := publicAPIPlainPeer(t)
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(peer.server.URL)
		if err != nil {
			t.Fatal(err)
		}
		jar.SetCookies(u, []*http.Cookie{{Name: "jar", Value: "manual"}})
		req.SetCookieJar(jar)
		req.SetCommonCookies(&http.Cookie{Name: "common", Value: "configured"})
		response, err := req.Get(peer.server.URL + "/before")
		publicAPIResponse(t, response, err, 200, "public-api-response")
		before := peer.receive(t)
		req.ClearCookies()
		response, err = req.Get(peer.server.URL + "/after")
		publicAPIResponse(t, response, err, 200, "public-api-response")
		after := peer.receive(t)
		cookies, cookieErr := req.GetCookies(peer.server.URL)
		// ClearCookies explicitly preserves a jar supplied via SetCookieJar.
		if !strings.Contains(before.headers.Get("Cookie"), "common=configured") || !strings.Contains(before.headers.Get("Cookie"), "jar=manual") || after.headers.Get("Cookie") != "jar=manual" || cookieErr != nil || len(cookies) != 1 || cookies[0].Name != "jar" {
			t.Fatalf("cookie ownership before=%v after=%v jar=%v error=%v", before.headers, after.headers, cookies, cookieErr)
		}
	})
	t.Run("redirect_denied", func(t *testing.T) {
		publicAPIInstall(t)
		destination := publicAPIPlainPeer(t)
		location := destination.server.URL + "/must-not-arrive"
		source := publicAPINewPeer(t, func(int, publicAPIExchange) (int, string, string) {
			return http.StatusFound, "text/plain", "bounded-redirect-body"
		}, http.Header{"Location": {location}})
		req.SetRedirectPolicy(req.NoRedirectPolicy())
		response, err := req.Get(source.server.URL + "/redirect")
		publicAPIResponse(t, response, err, 302, "bounded-redirect-body")
		exchange := source.receive(t)
		if response.Header.Get("Location") != location || source.requests.Load() != 1 || destination.requests.Load() != 0 || exchange.uri != "/redirect" {
			t.Fatalf("redirect policy location=%q source=%d destination=%d exchange=%+v", response.Header.Get("Location"), source.requests.Load(), destination.requests.Load(), exchange)
		}
	})
	t.Run("manual_read_and_reenable", func(t *testing.T) {
		publicAPIInstall(t)
		peer := publicAPIPlainPeer(t)
		req.DisableAutoReadResponse()
		response, err := req.Get(peer.server.URL + "/manual")
		if err != nil || response == nil || response.Body == nil {
			t.Fatalf("manual response=%v error=%v", response, err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || string(body) != "public-api-response" {
			t.Fatalf("manual body=%q read=%v close=%v", body, readErr, closeErr)
		}
		peer.receive(t)
		req.EnableAutoReadResponse()
		response, err = req.Get(peer.server.URL + "/automatic")
		publicAPIResponse(t, response, err, 200, "public-api-response")
		peer.receive(t)
	})
}

func TestReqPublicAPI_RetryReplay(t *testing.T) {
	for _, name := range []string{"common_fixed", "request_custom_interval", "request_backoff", "request_zero_overrides_common"} {
		t.Run(name, func(t *testing.T) {
			publicAPIInstall(t)
			peer := publicAPINewPeer(t, func(attempt int, _ publicAPIExchange) (int, string, string) {
				if attempt == 1 {
					return 503, "text/plain", "retry-first"
				}
				return 200, "text/plain", "retry-final"
			})
			var hooks []string
			var intervals []int
			condition := func(response *req.Response, err error) bool {
				return err == nil && response != nil && response.StatusCode == 503
			}
			req.SetCommonRetryCount(1)
			req.SetCommonRetryCondition(condition)
			req.SetCommonRetryFixedInterval(0)
			req.SetCommonRetryHook(func(response *req.Response, err error) { hooks = append(hooks, "set") })
			req.AddCommonRetryHook(func(response *req.Response, err error) { hooks = append(hooks, "add") })
			r := req.SetBodyString("identical replay +& body")
			switch name {
			case "request_custom_interval":
				r = req.SetRetryInterval(func(response *req.Response, attempt int) time.Duration {
					if response.StatusCode != 503 {
						t.Errorf("interval wrong status=%d", response.StatusCode)
					}
					intervals = append(intervals, attempt)
					return 0
				}).SetBodyString("identical replay +& body")
			case "request_backoff":
				r = req.SetRetryBackoffInterval(2*time.Nanosecond, 4*time.Nanosecond).SetBodyString("identical replay +& body")
			case "request_zero_overrides_common":
				r = req.SetRetryCount(0).SetBodyString("identical replay +& body")
			}
			response, err := r.Post(peer.server.URL + "/retry")
			if name == "request_zero_overrides_common" {
				publicAPIResponse(t, response, err, 503, "retry-first")
				if peer.requests.Load() != 1 || len(hooks) != 0 || response.Request.RetryAttempt != 0 {
					t.Fatal("request zero did not override common retries")
				}
				peer.receive(t)
				return
			}
			publicAPIResponse(t, response, err, 200, "retry-final")
			first, second := peer.receive(t), peer.receive(t)
			if first.body != "identical replay +& body" || second.body != first.body || peer.requests.Load() != 2 || response.Request.RetryAttempt != 1 || !reflect.DeepEqual(hooks, []string{"add", "set"}) {
				t.Fatalf("retry body first=%+v second=%+v hooks=%v attempt=%d", first, second, hooks, response.Request.RetryAttempt)
			}
			if name == "request_custom_interval" && !reflect.DeepEqual(intervals, []int{1}) {
				t.Fatalf("actual interval attempts=%v", intervals)
			}
		})
	}
}
