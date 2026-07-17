package service

import (
	"context"
	"testing"
)

func TestWithHTTPUpstreamProfile_DefaultKeepsContext(t *testing.T) {
	ctx := context.Background()
	got := WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileDefault)
	if got != ctx {
		t.Fatal("default profile should not wrap context")
	}
}

func TestWithHTTPUpstreamProfile_OpenAI(t *testing.T) {
	ctx := WithHTTPUpstreamProfile(context.TODO(), HTTPUpstreamProfileOpenAI)
	if profile := HTTPUpstreamProfileFromContext(ctx); profile != HTTPUpstreamProfileOpenAI {
		t.Fatalf("expected profile %q, got %q", HTTPUpstreamProfileOpenAI, profile)
	}
}

func TestHTTPUpstreamRedirectsDisabledContext(t *testing.T) {
	var nilContext context.Context
	if HTTPUpstreamRedirectsDisabled(nilContext) {
		t.Fatal("nil context must not disable redirects")
	}
	if HTTPUpstreamRedirectsDisabled(context.Background()) {
		t.Fatal("plain context must not disable redirects")
	}
	ctx := WithHTTPUpstreamRedirectsDisabled(nilContext)
	if !HTTPUpstreamRedirectsDisabled(ctx) {
		t.Fatal("wrapped context should disable redirects")
	}
}
