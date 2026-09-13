//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEstimateOpenAIRequestInputTokens(t *testing.T) {
	// 32 个 ASCII 字符 ≈ 8 token（estimateTokensForText 按 4 字符/token）。
	text32 := strings.Repeat("abcd", 8)
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			name: "responses: instructions + input string",
			body: `{"model":"gpt-5","instructions":"` + text32 + `","input":"` + text32 + `"}`,
			// instructions 8 + input 字符串视为一条 message：8 + 4 结构开销
			want: 8 + 8 + 4,
		},
		{
			name: "responses: input array with content parts",
			body: `{"input":[{"role":"user","content":[{"type":"input_text","text":"` + text32 + `"},{"type":"input_image","image_url":"data:..."}]},{"role":"assistant","content":"` + text32 + `"},{"type":"function_call_output","call_id":"c1","output":"` + text32 + `"}]}`,
			// 第 1 条：8 + 4；图片跳过；第 2 条：8 + 4；第 3 条无 content 文本：4 结构开销
			want: 12 + 12 + 4,
		},
		{
			name: "chat completions: messages string and parts",
			body: `{"messages":[{"role":"system","content":"` + text32 + `"},{"role":"user","content":[{"type":"text","text":"` + text32 + `"},{"type":"image_url","image_url":{"url":"x"}}]}]}`,
			want: 12 + 12,
		},
		{
			name: "anthropic messages: system string + content parts",
			body: `{"system":"` + text32 + `","messages":[{"role":"user","content":[{"type":"text","text":"` + text32 + `"},{"type":"image","source":{}}]}]}`,
			want: 8 + 12,
		},
		{
			name: "anthropic messages: system blocks",
			body: `{"system":[{"type":"text","text":"` + text32 + `"}],"messages":[{"role":"user","content":"` + text32 + `"}]}`,
			want: 8 + 12,
		},
		{
			name: "mixed: tools description and parameters count as text",
			body: `{"messages":[{"role":"user","content":"` + text32 + `"}],"tools":[{"type":"function","function":{"name":"f","description":"` + text32 + `","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}},{"type":"function","name":"g","description":"` + text32 + `","parameters":{"type":"object"}}]}`,
			// message 12；tool1 描述 8 + parameters JSON 文本 `{"type":"object","properties":{"q":{"type":"string"}}}` 54 字符 → 14；
			// tool2 描述 8 + `{"type":"object"}` 17 字符 → 5
			want: 12 + 8 + 14 + 8 + 5,
		},
		{name: "empty body", body: ``, want: 0},
		{name: "invalid json", body: `{"messages":[`, want: 0},
		{name: "no text fields", body: `{"model":"gpt-5","stream":true}`, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, EstimateOpenAIRequestInputTokens([]byte(tc.body)))
		})
	}
}
