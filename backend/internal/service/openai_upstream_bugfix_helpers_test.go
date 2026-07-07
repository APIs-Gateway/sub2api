//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeGLMOpenAIReasoningEffort(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty omitted", raw: ""},
		{name: "unknown omitted", raw: "extreme"},
		{name: "low accepted", raw: " Low ", want: "low"},
		{name: "none accepted", raw: "NONE", want: "none"},
		{name: "high accepted", raw: "high", want: "high"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeGLMOpenAIReasoningEffort(tt.raw)
			if tt.want == "" {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tt.want, *got)
		})
	}
}

func TestNormalizeGLMOpenAIReasoningEffortForRawChatBody(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		body       string
		path       string
		want       string
		normalized bool
	}{
		{name: "non glm unchanged", model: "gpt-5", body: `{"reasoning_effort":"xhigh"}`},
		{name: "missing effort unchanged", model: "glm-5", body: `{"messages":[]}`},
		{name: "unknown effort unchanged", model: "glm-5", body: `{"reasoning_effort":"tiny"}`},
		{name: "top level medium maps high", model: " glm-5 ", body: `{"reasoning_effort":"medium"}`, path: "reasoning_effort", want: "high", normalized: true},
		{name: "nested x high maps max", model: "glm-5", body: `{"reasoning":{"effort":"x-high"}}`, path: "reasoning.effort", want: "max", normalized: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, normalized := normalizeGLMOpenAIReasoningEffortForRawChatBody([]byte(tt.body), tt.model)
			require.Equal(t, tt.normalized, normalized)
			if !tt.normalized {
				require.JSONEq(t, tt.body, string(out))
				return
			}
			require.Equal(t, tt.want, gjson.GetBytes(out, tt.path).String())
		})
	}
}

func TestBodyHasSSEFraming(t *testing.T) {
	require.False(t, bodyHasSSEFraming(nil))
	require.False(t, bodyHasSSEFraming([]byte(`{"data":"not framing"}`)))
	require.True(t, bodyHasSSEFraming([]byte("  data: {\"ok\":true}\n\n")))
	require.True(t, bodyHasSSEFraming([]byte("\tevent: response.completed\n")))
}

func TestOpenAIImagesDiagnosticsHelpers(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"first"}`,
		"",
		`data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"second"}]}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"third"}]}]}}`,
		"",
	}, "\n")

	refusal := extractOpenAIImagesModelRefusal([]byte(body))
	require.Equal(t, "first second third", refusal)

	summary := summarizeOpenAIImagesNoOutputBody([]byte(body))
	require.Contains(t, summary, "no_image_output")
	require.Contains(t, summary, "last_event=response.completed")
	require.Contains(t, summary, "status=completed")
	require.Contains(t, summary, "body=data:")
}

func TestOpenAIImagesIncompleteUpstreamError_Defaults(t *testing.T) {
	require.Nil(t, openAIImagesIncompleteUpstreamError(gjson.Result{}))

	err := openAIImagesIncompleteUpstreamError(gjson.Parse(`{"id":"resp_2"}`))
	require.NotNil(t, err)
	require.Equal(t, 502, err.StatusCode)
	require.Equal(t, "response_incomplete", err.Code)
	require.Equal(t, "resp_2", err.UpstreamRequestID)
}
