package apicompat

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func chatAudioShapeInvalidSiblings() []struct{ name, part string } {
	return []struct{ name, part string }{
		{"missing_text", `{"type":"text"}`},
		{"missing_image_descriptor", `{"type":"image_url"}`},
		{"empty_image_descriptor", `{"type":"image_url","image_url":{}}`},
		{"missing_image_url", `{"type":"image_url","image_url":{"detail":"auto"}}`},
		{"empty_image_url", `{"type":"image_url","image_url":{"url":""}}`},
		{"empty_image_base64", `{"type":"image_url","image_url":{"url":"data:image/png;base64,"}}`},
		{"missing_file_descriptor", `{"type":"file"}`},
		{"empty_file_descriptor", `{"type":"file","file":{}}`},
		{"filename_only_file", `{"type":"file","file":{"filename":"note.pdf"}}`},
		{"empty_file_data", `{"type":"file","file":{"file_data":""}}`},
		{"empty_file_id", `{"type":"file","file":{"file_id":""}}`},
	}
}

func TestChatInputAudioShape_RequiredKnownFields(t *testing.T) {
	const audio = `{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}`
	for _, tc := range chatAudioShapeInvalidSiblings() {
		for _, before := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/before_%t", tc.name, before), func(t *testing.T) {
				content := "[" + audio + "," + tc.part + "]"
				if before {
					content = "[" + tc.part + "," + audio + "]"
				}
				req := &ChatCompletionsRequest{Model: "gemini-2.5-flash", Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(content)}}}
				original, err := json.Marshal(req)
				require.NoError(t, err)
				got, err := ChatCompletionsToResponsesForGemini(req)
				after, marshalErr := json.Marshal(req)
				require.NoError(t, marshalErr)
				require.Equal(t, original, after)
				t.Logf("SHAPE_ACTUAL_CONVERTER nil_error=%t caller_immutable=true", err == nil)
				require.ErrorIs(t, err, ErrInvalidInputAudio)
				require.Nil(t, got)
			})
		}
	}
}

func TestChatInputAudioShape_NoAudioKeepsOriginalFiltering(t *testing.T) {
	for _, tc := range chatAudioShapeInvalidSiblings() {
		t.Run(tc.name, func(t *testing.T) {
			req := &ChatCompletionsRequest{Model: "gemini-2.5-flash", Messages: []ChatMessage{{Role: "user", Content: json.RawMessage("[" + tc.part + "]")}}}
			for _, converter := range []func(*ChatCompletionsRequest) (*ResponsesRequest, error){ChatCompletionsToResponses, ChatCompletionsToResponsesForGemini} {
				got, err := converter(req)
				require.NoError(t, err)
				require.JSONEq(t, `[{"type":"message","role":"user","content":""}]`, string(got.Input))
			}
		})
	}
}

func TestChatInputAudioShape_KnownPartsAndWhitelistControl(t *testing.T) {
	content := `[{"type":"text","text":""},{"type":"text","text":"before"},{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}},{"type":"file","file":{"file_data":"data:application/pdf;base64,aA=="}},{"type":"file","file":{"file_id":"existing-file"}},{"type":"provider_extra","opaque":42},{"type":"text","text":"after"}]`
	req := &ChatCompletionsRequest{Model: "gemini-2.5-flash", Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(content)}}}
	original, err := json.Marshal(req)
	require.NoError(t, err)
	responses, err := ChatCompletionsToResponsesForGemini(req)
	require.NoError(t, err)
	got, err := ResponsesToAnthropicRequestForGemini(responses)
	require.NoError(t, err)
	require.Len(t, got.Messages, 1)
	require.JSONEq(t, `[{"type":"text","text":"before"},{"type":"document","source":{"type":"base64","media_type":"audio/wav","data":"aA=="}},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}},{"type":"text","text":"after"}]`, string(got.Messages[0].Content))
	after, err := json.Marshal(req)
	require.NoError(t, err)
	require.Equal(t, original, after)
}
