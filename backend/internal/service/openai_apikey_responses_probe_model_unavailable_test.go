package service

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
)

func TestResponsesProbeModelUnavailableKeepsCapabilityUnchanged(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"error_type", 404, `{"error":{"type":"model_not_found","message":"Model codex-auto-review is not supported by any configured account"}}`},
		{"model_name_only_in_message", 404, `{"error":{"message":"Model codex-auto-review is not supported by any configured account in this group"}}`},
		{"error_code_400", 400, `{"error":{"code":"model_not_available"}}`},
		{"nested_error_type", 404, `{"response":{"error":{"type":"unsupported_model"}}}`},
		{"invalid_model", 400, `{"error":{"code":"invalid_model"}}`},
		{"named_model_message", 404, `{"error":{"message":"The model missing does not exist"}}`},
		{"named_model_not_found_404", 404, `{"error":{"message":"The model gpt-5.5 not found"}}`},
		{"named_model_not_found_400", 400, `{"error":{"message":"model gpt-5.5 not found"}}`},
		{"nested_message", 404, `{"response":{"error":{"message":"The model missing is unavailable"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, responsesProbeVerdictIsConclusive(tc.status, []byte(tc.body)))
			require.True(t, decideResponsesProbeSupport(tc.status, []byte(tc.body)))
			require.Nil(t, runResponsesProbe(t, tc.status, tc.body),
				"model availability alone must not overwrite the account capability")
		})
	}
}

func TestResponsesProbeOtherErrorsRetainExistingVerdicts(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"plain_404", http.StatusNotFound, `{"error":{"message":"Not Found"}}`, false},
		{"endpoint_unsupported_404", http.StatusNotFound, `{"error":{"message":"The /v1/responses endpoint is not supported"}}`, false},
		{"endpoint_not_found_404", http.StatusNotFound, `{"error":{"message":"The /v1/responses endpoint was not found"}}`, false},
		{"method_not_allowed", http.StatusMethodNotAllowed, `{"error":{"code":"model_not_found"}}`, false},
		{"unrelated_400", http.StatusBadRequest, `{"error":{"message":"Model output is not supported"}}`, true},
		{"server_error", http.StatusInternalServerError, `{"error":{"code":"model_not_found"}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updates := runResponsesProbe(t, tc.status, tc.body)
			require.NotNil(t, updates)
			require.Equal(t, tc.want, updates[openai_compat.ExtraKeyResponsesSupported])
		})
	}
}

func TestSelectResponsesProbeModelPrefersGeneralTextModel(t *testing.T) {
	account := &Account{Credentials: map[string]any{"model_mapping": map[string]any{
		"review":     "codex-auto-review",
		"image":      "gpt-image-2",
		"audio":      "gpt-audio",
		"audio4o":    "gpt-4o-audio-preview",
		"realtime":   "gpt-4o-mini-realtime-preview",
		"transcribe": "gpt-4o-mini-transcribe",
		"tts":        "gpt-4o-mini-tts",
		"search":     "gpt-4o-mini-search-preview",
		"instruct":   "gpt-3.5-turbo-instruct",
		"text":       "gpt-5.5",
		"later":      "gpt-6-sol",
	}}}
	require.Equal(t, "gpt-5.5", selectResponsesProbeModel(account))

	// Without a suitable GPT text model, preserve the original lexical fallback.
	account.Credentials["model_mapping"] = map[string]any{
		"audio": "gpt-audio", "image": "gpt-image-2",
	}
	require.Equal(t, "gpt-audio", selectResponsesProbeModel(account))
}
