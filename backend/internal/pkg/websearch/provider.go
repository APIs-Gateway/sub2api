package websearch

import (
	"context"
	"fmt"
)

// providerHTTPStatusError preserves the existing provider error for the
// production search path. Admin test diagnostics use only its numeric status.
type providerHTTPStatusError struct {
	provider string
	status   int
	body     string
}

func (e *providerHTTPStatusError) Error() string {
	return fmt.Sprintf("%s: status %d: %s", e.provider, e.status, e.body)
}

type providerDecodeError struct {
	provider string
	cause    error
}

func (e *providerDecodeError) Error() string {
	return fmt.Sprintf("%s: decode response: %v", e.provider, e.cause)
}

func (e *providerDecodeError) Unwrap() error { return e.cause }

// Provider is the interface every search backend must implement.
type Provider interface {
	// Name returns the provider identifier ("brave" or "tavily").
	Name() string
	// Search executes a web search and returns results.
	Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)
}
