package tokenest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEstimateText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want int
	}{
		{name: "empty", in: "", want: 0},
		{name: "whitespace only", in: "   \n\t ", want: 0},
		{name: "ascii rounds up", in: "hello", want: 2},           // 5 runes -> ceil(5/4)
		{name: "ascii exact", in: "abcd", want: 1},                // 4 runes -> 1
		{name: "cjk one per rune", in: "你好世界", want: 4},           // CJK -> 1 token per rune
		{name: "mixed leans cjk", in: "hi 你好世界啊", want: 8},        // ascii ratio < 0.8 -> 1 per rune
		{name: "mixed leans ascii", in: "hello world 你", want: 4}, // ascii ratio >= 0.8
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, EstimateText(tt.in))
		})
	}
}

func TestEstimateJSONBodyCoversEveryFormat(t *testing.T) {
	t.Parallel()

	// The same prompt expressed in four different request shapes must all be
	// counted, proving the walk does not depend on per-format field names.
	bodies := map[string]string{
		"openai responses": `{"model":"gpt-5.5","instructions":"be brief","input":"abcdefgh"}`,
		"openai chat":      `{"model":"gpt-5.5","messages":[{"role":"user","content":"abcdefgh"}]}`,
		"anthropic":        `{"system":"be brief","messages":[{"role":"user","content":[{"type":"text","text":"abcdefgh"}]}]}`,
		"gemini":           `{"contents":[{"parts":[{"text":"abcdefgh"}]}]}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// "abcdefgh" alone is 2 tokens; every shape must report at least that.
			require.GreaterOrEqual(t, EstimateJSONBody([]byte(body)), 2)
		})
	}
}

func TestEstimateJSONBodyNestedAndNonJSON(t *testing.T) {
	t.Parallel()

	require.Equal(t, 0, EstimateJSONBody([]byte("not json at all")))
	require.Equal(t, 0, EstimateJSONBody(nil))

	// Deeply nested strings are still summed.
	nested := `{"a":{"b":{"c":["abcd","efgh"]}}}`
	require.Equal(t, 2, EstimateJSONBody([]byte(nested)))

	// Numbers and booleans contribute nothing.
	require.Equal(t, 0, EstimateJSONBody([]byte(`{"n":123456789,"b":true,"nil":null}`)))
}

func TestWithinLimitDisabled(t *testing.T) {
	t.Parallel()

	huge := []byte(`{"input":"` + strings.Repeat("a", 10_000) + `"}`)
	for _, limit := range []int{0, -1} {
		est, ok := WithinLimit(huge, limit)
		require.True(t, ok, "limit %d must disable the check", limit)
		require.Zero(t, est)
	}
}

func TestWithinLimitShortCircuitsSmallBodies(t *testing.T) {
	t.Parallel()

	// A body shorter than the limit in bytes can never exceed it in tokens,
	// so it is accepted without being parsed (estimate stays zero).
	body := []byte(`{"input":"` + strings.Repeat("a", 400) + `"}`)
	est, ok := WithinLimit(body, 1000)
	require.True(t, ok)
	require.Zero(t, est, "small bodies must skip estimation entirely")
}

func TestWithinLimitRejectsOversizedPrompt(t *testing.T) {
	t.Parallel()

	// 8000 ASCII chars -> ~2000 tokens, over a 100-token limit.
	body := []byte(`{"input":"` + strings.Repeat("a", 8000) + `"}`)
	est, ok := WithinLimit(body, 100)
	require.False(t, ok)
	require.Greater(t, est, 100)
}

func TestWithinLimitAcceptsLargeButUnderLimit(t *testing.T) {
	t.Parallel()

	// 8000 ASCII chars -> ~2000 tokens. Body exceeds the byte shortcut
	// (len > limit) so it is really parsed, and still passes a 5000 limit.
	body := []byte(`{"input":"` + strings.Repeat("a", 8000) + `"}`)
	require.Greater(t, len(body), 5000, "body must be long enough to defeat the shortcut")

	est, ok := WithinLimit(body, 5000)
	require.True(t, ok)
	require.Greater(t, est, 0, "estimation must actually have run")
	require.LessOrEqual(t, est, 5000)
}

func TestWithinLimitCJKIsNotUndercounted(t *testing.T) {
	t.Parallel()

	// CJK costs one token per rune while occupying 3 bytes in UTF-8. The byte
	// shortcut must not let such a prompt slip past the limit.
	runes := 4000
	body := []byte(`{"input":"` + strings.Repeat("好", runes) + `"}`)
	require.Greater(t, len(body), runes, "CJK body is longer in bytes than in runes")

	est, ok := WithinLimit(body, 1000)
	require.False(t, ok, "4000 CJK runes must exceed a 1000 token limit")
	require.GreaterOrEqual(t, est, runes)
}

func BenchmarkWithinLimitTypicalRequest(b *testing.B) {
	// A realistic prompt is far below the limit and must cost nothing.
	body := []byte(fmt.Sprintf(`{"model":"gpt-5.5","input":%q}`, strings.Repeat("word ", 2000)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = WithinLimit(body, 1_000_000)
	}
}
