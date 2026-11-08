// Public redirect/cookie behavior adapted from pinned Go net/http client tests.
// See CLIENT-BEHAVIOR-PROVENANCE.json and CLIENT-BEHAVIOR-LICENSE.
package req

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"reflect"
	"testing"
)

// Every virtual authority resolves only to this owned listener. An unexpected
// authority is rejected before any DNS lookup or external connection is possible.
func clientBehaviorVirtualClient(t *testing.T, peer *clientBehaviorPeer, hosts ...string) *Client {
	t.Helper()
	allowed := make(map[string]bool)
	for _, host := range hosts {
		allowed[host+":80"] = true
	}
	return newClientBehaviorClient(t).SetDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !allowed[addr] {
			return nil, fmt.Errorf("unowned authority %q", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, peer.server.Listener.Addr().String())
	})
}

func TestReqClientRedirect_AllowedAuthorities(t *testing.T) {
	for _, tc := range []struct {
		name, source, destination string
		policy                    RedirectPolicy
	}{
		{name: "same_host", source: "same.owned.test", destination: "same.owned.test", policy: SameHostRedirectPolicy()},
		{name: "same_domain", source: "first.owned.test", destination: "second.owned.test", policy: SameDomainRedirectPolicy()},
		{name: "allowed_host", source: "start.source.test", destination: "finish.destination.test", policy: AllowedHostRedirectPolicy("finish.destination.test")},
		{name: "allowed_domain", source: "start.source.test", destination: "finish.destination.test", policy: AllowedDomainRedirectPolicy("destination.test")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, r *http.Request, o clientBehaviorObservation) {
				if o.uri == "/start" {
					http.Redirect(w, r, "http://"+tc.destination+"/finish", http.StatusFound)
					return
				}
				_, _ = io.WriteString(w, "owned destination")
			})
			client := clientBehaviorVirtualClient(t, peer, tc.source, tc.destination).SetRedirectPolicy(tc.policy)
			resp, err := client.Do(clientBehaviorRequest(t, http.MethodGet, "http://"+tc.source+"/start", ""))
			if got := clientBehaviorBody(t, resp, err); got != "owned destination" || resp.StatusCode != http.StatusOK {
				t.Fatalf("redirect response=%v body=%q", resp, got)
			}
			seen := peer.observations(t, 2)
			if seen[0].host != tc.source || seen[0].uri != "/start" || seen[1].host != tc.destination || seen[1].uri != "/finish" {
				t.Fatalf("actual redirect authority chain=%+v", seen)
			}
		})
	}
}

func TestReqClientRedirect_SensitiveHeaderBoundary(t *testing.T) {
	peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, r *http.Request, o clientBehaviorObservation) {
		switch o.host + o.uri {
		case "source.owned.test/start":
			http.Redirect(w, r, "http://other.destination.test/middle", http.StatusFound)
		case "other.destination.test/middle":
			http.Redirect(w, r, "http://source.owned.test/finish", http.StatusFound)
		case "source.owned.test/finish":
			_, _ = io.WriteString(w, "finished credential boundary")
		default:
			http.Error(w, "unexpected owned hop", http.StatusBadRequest)
		}
	})
	client := clientBehaviorVirtualClient(t, peer, "source.owned.test", "other.destination.test")
	req := clientBehaviorRequest(t, http.MethodGet, "http://source.owned.test/start", "")
	req.Header.Set("Authorization", "Bearer owned-secret")
	req.Header.Set("Cookie", "owned_source=secret")
	req.Header.Set("X-Owned-Trace", "allowed-public-value")
	resp, err := client.Do(req)
	if got := clientBehaviorBody(t, resp, err); got != "finished credential boundary" {
		t.Fatalf("boundary final body=%q", got)
	}
	seen := peer.observations(t, 3)
	if seen[0].header.Get("Authorization") != "Bearer owned-secret" || seen[0].header.Get("Cookie") != "owned_source=secret" {
		t.Fatalf("initial credentials absent: %+v", seen[0])
	}
	for i, o := range seen[1:] {
		if o.header.Get("Authorization") != "" || o.header.Get("Cookie") != "" || o.header.Get("X-Owned-Trace") != "allowed-public-value" {
			t.Fatalf("sensitive headers crossed boundary or returned at hop%d: %+v", i+1, o)
		}
	}
}

func TestReqClientRedirect_ExplicitHeaderPolicy(t *testing.T) {
	for _, keepDestination := range []bool{false, true} {
		name := "copy_configured_only"
		if keepDestination {
			name = "preserve_destination_value"
		}
		t.Run(name, func(t *testing.T) {
			peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, r *http.Request, o clientBehaviorObservation) {
				if o.uri == "/start" {
					http.Redirect(w, r, "http://other.destination.test/finish", http.StatusFound)
					return
				}
				_, _ = io.WriteString(w, "explicit policy destination")
			})
			client := clientBehaviorVirtualClient(t, peer, "source.owned.test", "other.destination.test")
			policies := []RedirectPolicy{MaxRedirectPolicy(3)}
			if keepDestination {
				policies = append(policies, func(r *http.Request, _ []*http.Request) error {
					r.Header.Set("Authorization", "Bearer owned-destination")
					return nil
				})
			}
			policies = append(policies, AlwaysCopyHeaderRedirectPolicy("Authorization"))
			client.SetRedirectPolicy(policies...)
			req := clientBehaviorRequest(t, http.MethodGet, "http://source.owned.test/start", "")
			req.Header.Set("Authorization", "Bearer owned-source")
			req.Header.Set("Cookie", "owned_source=secret")
			resp, err := client.Do(req)
			if got := clientBehaviorBody(t, resp, err); got != "explicit policy destination" {
				t.Fatalf("explicit redirect body=%q", got)
			}
			seen := peer.observations(t, 2)
			want := "Bearer owned-source"
			if keepDestination {
				want = "Bearer owned-destination"
			}
			if seen[1].header.Get("Authorization") != want || seen[1].header.Get("Cookie") != "" {
				t.Fatalf("explicit header policy wire headers=%v want Authorization=%q", seen[1].header, want)
			}
		})
	}
}

func clientBehaviorCookieValues(cookies []*http.Cookie) map[string][]string {
	values := make(map[string][]string)
	for _, cookie := range cookies {
		values[cookie.Name] = append(values[cookie.Name], cookie.Value)
	}
	return values
}

func TestReqClientCookie_RedirectReplacementAndDeletion(t *testing.T) {
	peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, r *http.Request, o clientBehaviorObservation) {
		switch o.uri {
		case "/start":
			http.SetCookie(w, &http.Cookie{Name: "replace", Value: "new", Path: "/"})
			http.SetCookie(w, &http.Cookie{Name: "remove", MaxAge: -1, Path: "/"})
			http.Redirect(w, r, "/finish", http.StatusFound)
		case "/finish":
			_, _ = io.WriteString(w, "cookies updated")
		default:
			http.Error(w, "unexpected path", http.StatusBadRequest)
		}
	})
	client := newClientBehaviorClient(t)
	req := clientBehaviorRequest(t, http.MethodGet, peer.url+"/start", "")
	for _, cookie := range []*http.Cookie{{Name: "keep", Value: "manual"}, {Name: "replace", Value: "old"}, {Name: "remove", Value: "old"}} {
		req.AddCookie(cookie)
	}
	resp, err := client.Do(req)
	if got := clientBehaviorBody(t, resp, err); got != "cookies updated" {
		t.Fatalf("cookie redirect=%q", got)
	}
	seen := peer.observations(t, 2)
	for i, want := range []map[string][]string{
		{"keep": {"manual"}, "replace": {"old"}, "remove": {"old"}},
		{"keep": {"manual"}, "replace": {"new"}},
	} {
		wire := (&http.Request{Header: seen[i].header}).Cookies()
		if got := clientBehaviorCookieValues(wire); !reflect.DeepEqual(got, want) {
			t.Fatalf("hop%d cookies=%v want=%v", i, got, want)
		}
	}
	cookies, err := client.GetCookies(peer.url)
	if err != nil || !reflect.DeepEqual(clientBehaviorCookieValues(cookies), map[string][]string{"replace": {"new"}}) {
		t.Fatalf("actual final cookie jar=%v err=%v", cookies, err)
	}
}

func TestReqClientCookie_CloneAndClear(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "factory_isolates_clone_and_clear"
		if shared {
			name = "caller_jar_shared_and_preserved"
		}
		t.Run(name, func(t *testing.T) {
			peer := newClientBehaviorPeer(t, func(w http.ResponseWriter, _ *http.Request, o clientBehaviorObservation) {
				if o.uri == "/set" {
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "from-real-response", Path: "/"})
				}
				_, _ = io.WriteString(w, "cookie observation")
			})
			client := newClientBehaviorClient(t)
			if shared {
				jar, err := cookiejar.New(nil)
				if err != nil {
					t.Fatal(err)
				}
				client.SetCookieJar(jar)
			} else {
				client.SetCookieJarFactory(func() *cookiejar.Jar { jar, _ := cookiejar.New(nil); return jar })
			}
			// Exercise the clone's public request middleware against a real peer.
			client.WrapRoundTripFunc(func(next RoundTripper) RoundTripFunc {
				return func(r *Request) (*Response, error) { r.SetHeader("X-Owned-Wrapper", "ran"); return next.RoundTrip(r) }
			})
			client.SetCommonCookies(&http.Cookie{Name: "common", Value: "owned-common"})
			resp, err := client.R().Get(peer.url + "/set")
			if err != nil || resp == nil || resp.String() != "cookie observation" {
				t.Fatalf("set cookie request=%v err=%v", resp, err)
			}
			clone := client.Clone()
			t.Cleanup(func() { clone.GetTransport().CloseIdleConnections() })
			for i, c := range []*Client{clone, client, clone} {
				if i == 1 {
					client.ClearCookies()
				}
				resp, err := c.R().Get(peer.url + "/observe")
				if err != nil || resp == nil || resp.String() != "cookie observation" {
					t.Fatalf("cookie observation=%v err=%v", resp, err)
				}
			}
			seen := peer.observations(t, 4)
			for i, o := range seen {
				if o.header.Get("X-Owned-Wrapper") != "ran" {
					t.Fatalf("wrapper absent on real request%d: %+v", i, o)
				}
			}
			wantClone := map[string][]string{"common": {"owned-common"}}
			wantCleared := map[string][]string{}
			if shared {
				wantClone["session"] = []string{"from-real-response"}
				wantCleared["session"] = []string{"from-real-response"}
			}
			for i, want := range map[int]map[string][]string{1: wantClone, 2: wantCleared, 3: wantClone} {
				if got := clientBehaviorCookieValues((&http.Request{Header: seen[i].header}).Cookies()); !reflect.DeepEqual(got, want) {
					t.Fatalf("clone/clear actual request%d cookies=%v want=%v", i, got, want)
				}
			}
			for _, c := range []*Client{client, clone} {
				cookies, err := c.GetCookies(peer.url)
				want := map[string][]string{}
				if shared {
					want["session"] = []string{"from-real-response"}
				}
				if err != nil || !reflect.DeepEqual(clientBehaviorCookieValues(cookies), want) {
					t.Fatalf("cookie jar after clone/clear=%v err=%v want=%v", cookies, err, want)
				}
			}
		})
	}
}
