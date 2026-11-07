//go:build unit

package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestEndpointChangedForProbe(t *testing.T) {
	base := openAIEndpointSnapshot{baseURL: "https://relay.example.com/v1", apiKey: "sk-old", proxyID: 1}
	with := func(f func(s *openAIEndpointSnapshot)) openAIEndpointSnapshot { s := base; f(&s); return s }
	cases := []struct {
		name          string
		before, after openAIEndpointSnapshot
		want          bool
	}{
		{"only model_mapping changed", base, base, false},
		{"base_url changed", base, with(func(s *openAIEndpointSnapshot) { s.baseURL = "https://other.example.com/v1" }), true},
		{"api_key replaced", base, with(func(s *openAIEndpointSnapshot) { s.apiKey = "sk-new" }), true},
		{"api_key same value", base, with(func(s *openAIEndpointSnapshot) { s.apiKey = "sk-old" }), false},
		{"base_url trailing slash only", base, with(func(s *openAIEndpointSnapshot) { s.baseURL = "https://relay.example.com/v1/" }), false},
		{"base_url whitespace only", base, with(func(s *openAIEndpointSnapshot) { s.baseURL = " https://relay.example.com/v1 " }), false},
		{"proxy changed", base, with(func(s *openAIEndpointSnapshot) { s.proxyID = 2 }), true},
		{"proxy cleared", base, with(func(s *openAIEndpointSnapshot) { s.proxyID = 0 }), true},
		{"proxy unchanged", base, with(func(s *openAIEndpointSnapshot) { s.proxyID = 1 }), false},
		{"unknown capability, only model_mapping", with(func(s *openAIEndpointSnapshot) { s.capabilityUnknown = true }), base, true},
		{"empty vs official base_url", openAIEndpointSnapshot{baseURL: ""}, openAIEndpointSnapshot{baseURL: "https://api.openai.com/"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := endpointChangedForProbe(tc.before, tc.after); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestSnapshotOpenAIEndpoint_CapabilityUnknown(t *testing.T) {
	pid := int64(7)
	mk := func(extra map[string]any) *service.Account {
		return &service.Account{ProxyID: &pid, Extra: extra, Credentials: map[string]any{"base_url": "https://x/v1", "api_key": "k"}}
	}
	cases := []struct {
		name  string
		extra map[string]any
		want  bool
	}{
		{"nil extra", nil, true},
		{"marker missing", map[string]any{"foo": 1}, true},
		{"marker true", map[string]any{"openai_responses_supported": true}, false},
		{"marker false", map[string]any{"openai_responses_supported": false}, false},
		{"mode set, marker missing", map[string]any{"openai_responses_mode": "force_responses"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := snapshotOpenAIEndpoint(mk(tc.extra))
			if s.capabilityUnknown != tc.want {
				t.Fatalf("capabilityUnknown got %v want %v", s.capabilityUnknown, tc.want)
			}
			if s.proxyID != 7 {
				t.Fatalf("proxyID got %d", s.proxyID)
			}
		})
	}
	// 能力已知 + 只改 model_mapping + 代理不变 => 不探测；unknown + 只改 model_mapping => 探测
	known := snapshotOpenAIEndpoint(mk(map[string]any{"openai_responses_supported": true}))
	if endpointChangedForProbe(known, known) {
		t.Fatal("known marker should not reprobe")
	}
	unknown := snapshotOpenAIEndpoint(mk(nil))
	if !endpointChangedForProbe(unknown, unknown) {
		t.Fatal("unknown marker should reprobe")
	}
}
