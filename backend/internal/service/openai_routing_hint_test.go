package service

import (
	"context"
	"net/http"
	"testing"
)

func openAIOAuthAccountForRoutingHintTest() *Account {
	return &Account{
		ID:       4242,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}
}

func TestSetOpenAICodexRoutingHint_NilHeaders(t *testing.T) {
	// Must not panic when headers is nil; there is nothing to mutate.
	setOpenAICodexRoutingHint(nil, openAIOAuthAccountForRoutingHintTest(), "gpt-5.6", "priority")
}

func TestSetOpenAICodexRoutingHint_NilAccountStripsExistingHint(t *testing.T) {
	headers := http.Header{}
	headers.Set(openAICodexRoutingHintHeader, "model=spoofed;tier=priority")

	setOpenAICodexRoutingHint(headers, nil, "gpt-5.6", "priority")

	if got := headers.Get(openAICodexRoutingHintHeader); got != "" {
		t.Fatalf("expected routing hint header to be stripped for nil account, got %q", got)
	}
}

func TestSetOpenAICodexRoutingHint_NonOAuthAccountStripsExistingHint(t *testing.T) {
	headers := http.Header{}
	// Simulate an inbound caller-supplied hint using a non-canonical casing to
	// verify the equal-fold strip covers raw lowercase keys too.
	headers["x-codex-routing-hint"] = []string{"model=spoofed;tier=priority"}

	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	setOpenAICodexRoutingHint(headers, account, "gpt-5.6", "priority")

	if got := headers.Get(openAICodexRoutingHintHeader); got != "" {
		t.Fatalf("expected routing hint header to be stripped for non-OAuth account, got %q", got)
	}
}

func TestSetOpenAICodexRoutingHint_EmptyModel(t *testing.T) {
	headers := http.Header{}
	setOpenAICodexRoutingHint(headers, openAIOAuthAccountForRoutingHintTest(), "   ", "priority")

	if got := headers.Get(openAICodexRoutingHintHeader); got != "" {
		t.Fatalf("expected no routing hint header for empty model, got %q", got)
	}
}

func TestSetOpenAICodexRoutingHint_ModelInjectionRejected(t *testing.T) {
	for _, model := range []string{"gpt-5.6;tier=flex", "gpt-5.6=x"} {
		headers := http.Header{}
		setOpenAICodexRoutingHint(headers, openAIOAuthAccountForRoutingHintTest(), model, "priority")
		if got := headers.Get(openAICodexRoutingHintHeader); got != "" {
			t.Fatalf("expected model %q containing ;/= to be rejected, got header %q", model, got)
		}
	}
}

func TestSetOpenAICodexRoutingHint_InvalidHeaderValueRejected(t *testing.T) {
	headers := http.Header{}
	// A model containing a control character is not filtered by the ";"/"="
	// check but must still fail httpguts.ValidHeaderFieldValue.
	setOpenAICodexRoutingHint(headers, openAIOAuthAccountForRoutingHintTest(), "gpt-5.6\r\nx-evil: 1", "priority")

	if got := headers.Get(openAICodexRoutingHintHeader); got != "" {
		t.Fatalf("expected invalid header value to be rejected, got %q", got)
	}
}

func TestSetOpenAICodexRoutingHint_TierCanonicalization(t *testing.T) {
	cases := []struct {
		name string
		tier string
		want string
	}{
		{"priority passthrough", "priority", "model=gpt-5.6;tier=priority"},
		{"fast normalizes to priority", "fast", "model=gpt-5.6;tier=priority"},
		{"flex passthrough", "flex", "model=gpt-5.6;tier=flex"},
		{"default sentinel omits tier", "default", "model=gpt-5.6"},
		{"auto omits tier", "auto", "model=gpt-5.6"},
		{"unknown tier omits tier", "bogus", "model=gpt-5.6"},
		{"empty tier omits tier", "", "model=gpt-5.6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			setOpenAICodexRoutingHint(headers, openAIOAuthAccountForRoutingHintTest(), "gpt-5.6", tc.tier)
			if got := headers.Get(openAICodexRoutingHintHeader); got != tc.want {
				t.Fatalf("tier=%q: got header %q, want %q", tc.tier, got, tc.want)
			}
		})
	}
}

func TestSetOpenAICodexRoutingHint_ReplacesPriorHint(t *testing.T) {
	headers := http.Header{}
	headers.Set(openAICodexRoutingHintHeader, "model=old;tier=flex")

	setOpenAICodexRoutingHint(headers, openAIOAuthAccountForRoutingHintTest(), "gpt-5.6", "priority")

	if got := headers.Get(openAICodexRoutingHintHeader); got != "model=gpt-5.6;tier=priority" {
		t.Fatalf("expected stale hint to be replaced, got %q", got)
	}
}

func TestDeleteOpenAIHeaderEqualFold(t *testing.T) {
	headers := http.Header{}
	headers["X-Codex-Routing-Hint"] = []string{"a"}
	headers["x-codex-routing-hint"] = []string{"b"}
	headers.Set("Other-Header", "keep")

	deleteOpenAIHeaderEqualFold(headers, openAICodexRoutingHintHeader)

	if len(headers) != 1 || headers.Get("Other-Header") != "keep" {
		t.Fatalf("expected only the routing hint header (any casing) to be removed, got %#v", headers)
	}

	// Nil-safe no-op.
	deleteOpenAIHeaderEqualFold(nil, openAICodexRoutingHintHeader)
}

func TestSetOpenAICodexRoutingHintFromBody(t *testing.T) {
	headers := http.Header{}
	body := []byte(`{"model":"gpt-5.6","service_tier":"flex"}`)

	setOpenAICodexRoutingHintFromBody(headers, openAIOAuthAccountForRoutingHintTest(), body)

	if got := headers.Get(openAICodexRoutingHintHeader); got != "model=gpt-5.6;tier=flex" {
		t.Fatalf("got header %q", got)
	}
}

func TestLogOpenAIRoutingDiagnostics_NoPanic(t *testing.T) {
	// These are observability-only side effects; exercise every input branch
	// (nil ctx, nil account, populated account) to ensure no panics.
	logOpenAIRoutingDiagnostics(nil, nil, "http", "gpt-5.6", "priority", true, "not_applicable")
	logOpenAIRoutingDiagnostics(context.Background(), openAIOAuthAccountForRoutingHintTest(), "ws_v2", "gpt-5.6", "flex", false, "soft_routing_hint")
}

func TestLogOpenAIRoutingDiagnosticsFromBody_NoPanic(t *testing.T) {
	headers := http.Header{}
	headers.Set(openAICodexRoutingHintHeader, "model=gpt-5.6;tier=priority")
	body := []byte(`{"model":"gpt-5.6","service_tier":"priority"}`)

	logOpenAIRoutingDiagnosticsFromBody(context.Background(), openAIOAuthAccountForRoutingHintTest(), "http", headers, body, "not_applicable")
}
