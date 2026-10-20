package service

import (
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOpenAIImageIntentHintTestContext(transport OpenAIClientTransport) *gin.Context {
	c := &gin.Context{}
	SetOpenAIClientTransport(c, transport)
	return c
}

func TestGetSetOpenAIImageIntentHintRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
	_, known := getOpenAIImageIntentHint(c)
	require.False(t, known, "no hint recorded yet")

	SetOpenAIImageIntentHint(c, false)
	cached, known := getOpenAIImageIntentHint(c)
	require.True(t, known)
	require.False(t, cached)

	SetOpenAIImageIntentHint(c, true)
	cached, known = getOpenAIImageIntentHint(c)
	require.True(t, known)
	require.True(t, cached)
}

func TestSetGetOpenAIImageIntentHintNilContext(t *testing.T) {
	// Must not panic; nil gin.Context is defensive-only, never hit on the real
	// request path, but Forward() callers should stay safe regardless.
	SetOpenAIImageIntentHint(nil, true)
	imageIntent, known := getOpenAIImageIntentHint(nil)
	require.False(t, known)
	require.False(t, imageIntent)
}

func TestOpenAIImageIntentHintExcludesNonHTTPTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, transport := range []OpenAIClientTransport{OpenAIClientTransportWS, OpenAIClientTransportUnknown} {
		c := newOpenAIImageIntentHintTestContext(transport)
		SetOpenAIImageIntentHint(c, true)
		_, known := getOpenAIImageIntentHint(c)
		require.False(t, known, "transport %q must not cache the hint", transport)
	}
}

func TestResolveOpenAIResponsesImageIntentHintCachesFirstReading(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name  string
		model string
		body  []byte
		want  bool
	}{
		{
			name:  "explicit image tool",
			model: "gpt-5.4",
			body:  []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation"}]}`),
			want:  true,
		},
		{
			name:  "plain text request",
			model: "gpt-5.4",
			body:  []byte(`{"model":"gpt-5.4","input":"write code"}`),
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)

			got := resolveOpenAIResponsesImageIntentHint(c, tt.model, tt.body)
			require.Equal(t, tt.want, got)

			cached, known := getOpenAIImageIntentHint(c)
			require.True(t, known)
			require.Equal(t, tt.want, cached)

			// Repeated calls with the same inputs are stable.
			require.Equal(t, tt.want, resolveOpenAIResponsesImageIntentHint(c, tt.model, tt.body))
		})
	}
}

func TestResolveOpenAIResponsesImageIntentHintStaysStickyOnceTrue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)

	// Attempt 1: the client's original body carries the passive Codex
	// "image_gen" namespace tool advertisement -- IsExplicitImageGenerationIntent
	// ignores it at the handler layer (#483), but IsImageGenerationIntent (used
	// here) still flags it, exactly the signal the disabled gate must key off.
	namespaceBody := []byte(`{"model":"gpt-5.4","tools":[{"type":"namespace","name":"image_gen"}]}`)
	require.True(t, resolveOpenAIResponsesImageIntentHint(c, "gpt-5.4", namespaceBody))

	// Attempt 2 (failover to a different account): simulate that this
	// attempt's own account-specific pre-Forward body handling produced a
	// plain-text-looking view of what is still, logically, the same request.
	// Without the sticky cache this would read as false and the disabled gate
	// would inconsistently let the retry through.
	plainBody := []byte(`{"model":"gpt-5.4","input":"list files"}`)
	require.True(t, resolveOpenAIResponsesImageIntentHint(c, "gpt-5.4", plainBody),
		"once any attempt observes image intent in the client body, later attempts must keep observing it")

	cached, known := getOpenAIImageIntentHint(c)
	require.True(t, known)
	require.True(t, cached)
}

func TestResolveOpenAIResponsesImageIntentHintKeepsRefiningFalseToTrue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)

	plainBody := []byte(`{"model":"gpt-5.4","input":"list files"}`)
	require.False(t, resolveOpenAIResponsesImageIntentHint(c, "gpt-5.4", plainBody))
	cached, known := getOpenAIImageIntentHint(c)
	require.True(t, known)
	require.False(t, cached)

	imageBody := []byte(`{"model":"gpt-5.4","tool_choice":{"type":"image_generation"}}`)
	require.True(t, resolveOpenAIResponsesImageIntentHint(c, "gpt-5.4", imageBody))
	cached, known = getOpenAIImageIntentHint(c)
	require.True(t, known)
	require.True(t, cached, "a later attempt observing a real signal must upgrade the sticky cache")

	// And it never regresses back to false afterwards.
	require.True(t, resolveOpenAIResponsesImageIntentHint(c, "gpt-5.4", plainBody))
}

func TestResolveOpenAIResponsesImageIntentHintExcludesNonHTTPTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, transport := range []OpenAIClientTransport{OpenAIClientTransportWS, OpenAIClientTransportUnknown} {
		c := newOpenAIImageIntentHintTestContext(transport)
		imageBody := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation"}]}`)

		require.True(t, resolveOpenAIResponsesImageIntentHint(c, "gpt-5.4", imageBody))
		_, known := getOpenAIImageIntentHint(c)
		require.False(t, known, "non-HTTP transport must not persist the hint across calls")

		// Because nothing is cached, a later plain-text reading is not forced
		// true; each call is independently classified on non-HTTP transports.
		plainBody := []byte(`{"model":"gpt-5.4","input":"list files"}`)
		require.False(t, resolveOpenAIResponsesImageIntentHint(c, "gpt-5.4", plainBody))
	}
}

func TestResolveOpenAIResponsesImageIntentHintConcurrentRequestsAreIsolated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const requests = 32
	var wg sync.WaitGroup
	results := make([][2]bool, requests)

	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(index int, wantImage bool) {
			defer wg.Done()
			c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
			body := []byte(`{"model":"gpt-5.4","input":"write code"}`)
			if wantImage {
				body = []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation"}]}`)
			}
			results[index][0] = resolveOpenAIResponsesImageIntentHint(c, "gpt-5.4", body)
			results[index][1] = resolveOpenAIResponsesImageIntentHint(c, "gpt-5.4", body)
		}(i, i%2 == 0)
	}
	wg.Wait()

	for i, result := range results {
		require.Equal(t, i%2 == 0, result[0])
		require.Equal(t, result[0], result[1])
	}
}
