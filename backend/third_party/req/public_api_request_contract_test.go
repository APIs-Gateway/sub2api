package req_test

import (
	"encoding/xml"
	"net/url"
	"reflect"
	"strings"
	"testing"

	req "github.com/imroc/req/v3"
)

func TestReqPublicAPI_QueryConstruction(t *testing.T) {
	type queryInput struct {
		Name string   `url:"name"`
		Tag  []string `url:"tag"`
	}
	cases := []struct {
		name      string
		configure func() *req.Request
		want      url.Values
	}{
		{"common_multivalue", func() *req.Request {
			req.SetCommonQueryParams(map[string]string{"base": "one"})
			req.AddCommonQueryParam("tag", "first")
			req.AddCommonQueryParams("tag", "second", "x +&")
			req.SetCommonQueryParamsFromValues(url.Values{"other": {"two", "three"}})
			return req.R()
		}, url.Values{"base": {"one"}, "tag": {"first", "second", "x +&"}, "other": {"two", "three"}}},
		{"common_struct", func() *req.Request {
			req.SetCommonQueryParamsFromStruct(queryInput{"A +&", []string{"x", "y"}})
			return req.R()
		}, url.Values{"name": {"A +&"}, "tag": {"x", "y"}}},
		{"request_multivalue", func() *req.Request {
			return req.SetQueryParamsFromValues(url.Values{"tag": {"one", "two"}}).SetQueryParams(map[string]string{"scope": "request"}).SetQueryParamsAnyType(map[string]any{"count": 3}).AddQueryParam("tag", "three").AddQueryParams("tag", "four", "five")
		}, url.Values{"tag": {"one", "two", "three", "four", "five"}, "scope": {"request"}, "count": {"3"}}},
		{"request_struct", func() *req.Request {
			return req.SetQueryParamsFromStruct(queryInput{"n /?", []string{"a", "b"}}).SetQueryString("extra=x%2By")
		}, url.Values{"name": {"n /?"}, "tag": {"a", "b"}, "extra": {"x+y"}}},
		{"invalid_common_preserves_valid", func() *req.Request {
			req.SetCommonQueryParam("valid", "keep")
			req.SetCommonQueryString("%zz=bad")
			req.SetCommonQueryParamsFromStruct(17)
			return req.R()
		}, url.Values{"valid": {"keep"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publicAPIInstall(t)
			peer := publicAPIPlainPeer(t)
			req.SetBaseURL(peer.server.URL)
			r := tc.configure()
			response, err := r.Get("/query")
			publicAPIResponse(t, response, err, 200, "public-api-response")
			e := peer.receive(t)
			u, parseErr := url.ParseRequestURI(e.uri)
			if parseErr != nil || u.Path != "/query" || !reflect.DeepEqual(u.Query(), tc.want) || e.method != "GET" || peer.requests.Load() != 1 {
				t.Fatalf("query wire=%+v parsed=%v error=%v want=%v", e, u, parseErr, tc.want)
			}
		})
	}
	t.Run("independent_builders", func(t *testing.T) {
		client := publicAPIInstall(t)
		peer := publicAPIPlainPeer(t)
		req.SetBaseURL(peer.server.URL)
		req.SetCommonPathParams(map[string]string{"tenant": "common team"})
		req.SetCommonHeaders(map[string]string{"X-Public-Default": "stable"})
		req.SetUserAgent("public-api-contract")
		a := req.NewRequest().SetPathParams(map[string]string{"item": "a/b"}).SetQueryParam("private", "one").SetHeader("X-Private", "first")
		b := req.SetURL("/v/{tenant}/second")
		response, err := a.Get("/v/{tenant}/{item}")
		publicAPIResponse(t, response, err, 200, "public-api-response")
		response = b.Do()
		publicAPIResponse(t, response, response.Err, 200, "public-api-response")
		ea, eb := peer.receive(t), peer.receive(t)
		if a == b || req.GetClient() != client.GetClient() || ea.uri != "/v/common%20team/a%2Fb?private=one" || eb.uri != "/v/common%20team/second" || ea.headers.Get("X-Private") != "first" || eb.headers.Get("X-Private") != "" || eb.headers.Get("X-Public-Default") != "stable" || eb.headers.Get("User-Agent") != "public-api-contract" || peer.requests.Load() != 2 {
			t.Fatalf("builder isolation A=%+v B=%+v", ea, eb)
		}
	})
}

func TestReqPublicAPI_EntitySemantics(t *testing.T) {
	type xmlEntity struct {
		XMLName xml.Name `xml:"entry"`
		Name    string   `xml:"name"`
	}
	cases := []struct {
		name              string
		makeRequest       func() *req.Request
		body, contentType string
	}{
		{"reader", func() *req.Request { return req.SetBody(strings.NewReader("reader\x00payload")) }, "reader\x00payload", ""},
		{"bytes", func() *req.Request { return req.SetBodyBytes([]byte{'b', 0, 'y'}) }, "b\x00y", ""},
		{"string", func() *req.Request { return req.SetBodyString("literal +& string") }, "literal +& string", ""},
		{"json_literal_string", func() *req.Request { return req.SetBodyJsonString("{ \"n\": 3 }\n") }, "{ \"n\": 3 }\n", "application/json; charset=utf-8"},
		{"json_literal_bytes", func() *req.Request { return req.SetBodyJsonBytes([]byte("[ 1, 2 ]")) }, "[ 1, 2 ]", "application/json; charset=utf-8"},
		{"xml_literal_string", func() *req.Request { return req.SetBodyXmlString("<entry> a &amp; b </entry>") }, "<entry> a &amp; b </entry>", "text/xml; charset=utf-8"},
		{"xml_literal_bytes", func() *req.Request { return req.SetBodyXmlBytes([]byte("<entry>\nbyte\n</entry>")) }, "<entry>\nbyte\n</entry>", "text/xml; charset=utf-8"},
		{"xml_object", func() *req.Request { return req.SetBodyXmlMarshal(xmlEntity{Name: "A & B"}) }, "<entry><name>A &amp; B</name></entry>", "text/xml; charset=utf-8"},
		{"custom_content_type", func() *req.Request { return req.SetContentType("application/x-contract").SetBodyString("custom bytes") }, "custom bytes", "application/x-contract"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publicAPIInstall(t)
			peer := publicAPIPlainPeer(t)
			response, err := tc.makeRequest().Post(peer.server.URL + "/entity")
			publicAPIResponse(t, response, err, 200, "public-api-response")
			e := peer.receive(t)
			if e.method != "POST" || e.body != tc.body || (tc.contentType != "" && e.headers.Get("Content-Type") != tc.contentType) || peer.requests.Load() != 1 {
				t.Fatalf("entity=%+v want body=%q content-type=%q", e, tc.body, tc.contentType)
			}
		})
	}
	t.Run("get_payload_disabled_then_enabled", func(t *testing.T) {
		publicAPIInstall(t)
		peer := publicAPIPlainPeer(t)
		req.DisableAllowGetMethodPayload()
		response, err := req.SetBodyString("get-entity").Get(peer.server.URL + "/off")
		publicAPIResponse(t, response, err, 200, "public-api-response")
		req.EnableAllowGetMethodPayload()
		response, err = req.SetBodyString("get-entity").Get(peer.server.URL + "/on")
		publicAPIResponse(t, response, err, 200, "public-api-response")
		a, b := peer.receive(t), peer.receive(t)
		if a.method != "GET" || b.method != "GET" || a.body != "" || b.body != "get-entity" || peer.requests.Load() != 2 {
			t.Fatalf("GET payload policy off=%+v on=%+v", a, b)
		}
	})
}
