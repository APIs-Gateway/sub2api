package apicompat

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatInputAudio_FormatsAndImmutableMixedContent(t *testing.T) {
	for format, mime := range map[string]string{"wav": "audio/wav", "mp3": "audio/mpeg", "ogg": "audio/ogg", "flac": "audio/flac", "aac": "audio/aac", "mp4": "audio/mp4", "m4a": "audio/mp4"} {
		t.Run(format, func(t *testing.T) {
			content := json.RawMessage(fmt.Sprintf(`[{"type":"text","text":"before"},{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":%q},"prompt_cache_breakpoint":{"type":"ephemeral"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}},{"type":"text","text":"after"}]`, format))
			req := &ChatCompletionsRequest{Model: "gemini-2.5-flash", Messages: []ChatMessage{{Role: "user", Content: content}}}
			before, err := json.Marshal(req)
			require.NoError(t, err)
			_, err = ChatCompletionsToResponses(req)
			require.ErrorIs(t, err, ErrUnsupportedInputAudio)
			got, err := ChatCompletionsToResponsesForGemini(req)
			require.NoError(t, err)
			var input []ResponsesInputItem
			require.NoError(t, json.Unmarshal(got.Input, &input))
			require.Len(t, input, 1)
			var parts []ResponsesContentPart
			require.NoError(t, json.Unmarshal(input[0].Content, &parts))
			require.Len(t, parts, 4)
			require.Equal(t, "before", parts[0].Text)
			require.Equal(t, geminiChatAudioFileType, parts[1].Type)
			require.Equal(t, "data:"+mime+";base64,aGVsbG8=", parts[1].FileData)
			require.JSONEq(t, `{"type":"ephemeral"}`, string(parts[1].PromptCacheBreakpoint))
			require.Equal(t, "input_image", parts[2].Type)
			require.Equal(t, "after", parts[3].Text)
			anthropic, err := ResponsesToAnthropicRequestForGemini(got)
			require.NoError(t, err)
			var blocks []AnthropicContentBlock
			require.NoError(t, json.Unmarshal(anthropic.Messages[0].Content, &blocks))
			require.Len(t, blocks, 4)
			require.Equal(t, "document", blocks[1].Type)
			require.Equal(t, mime, blocks[1].Source.MediaType)
			require.Equal(t, "aGVsbG8=", blocks[1].Source.Data)
			ordinary, err := ResponsesToAnthropicRequest(got)
			require.NoError(t, err)
			require.NotContains(t, string(ordinary.Messages[0].Content), "document", "default route must retain its original file whitelist")
			after, err := json.Marshal(req)
			require.NoError(t, err)
			require.Equal(t, before, after, "caller-owned messages/content must not mutate")
		})
	}
}

func TestChatInputAudio_RolesAndMalformedSiblings(t *testing.T) {
	for _, role := range []string{"user", "assistant", "system", "developer", "tool"} {
		for index, content := range []string{
			`[{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`,
			`[{"type":"text","text":123},{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`,
			`[{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}},{"type":"text","text":123}]`,
			`[null,{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`,
			`[{"type":"text","text":null},{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`,
			`[{"type":null},{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`,
			`[{}, {"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`,
			`[[], {"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`,
			`[{"type":"image_url","image_url":null},{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`,
			`[{"type":"file","file":null},{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`,
			`[{"type":"image_url","image_url":{"url":null}},{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`,
			`[{"type":"file","file":{"file_data":null}},{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`,
		} {
			t.Run(role+"/"+content, func(t *testing.T) {
				req := &ChatCompletionsRequest{Model: "gemini-2.5-flash", Messages: []ChatMessage{{Role: role, Content: json.RawMessage(content)}}}
				_, err := ChatCompletionsToResponses(req)
				require.ErrorIs(t, err, ErrUnsupportedInputAudio)
				_, err = ChatCompletionsToResponsesForGemini(req)
				if role != "user" {
					require.ErrorIs(t, err, ErrUnsupportedInputAudio)
				} else if index > 0 {
					require.ErrorIs(t, err, ErrInvalidInputAudio)
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestChatInputAudio_InvalidPayloadsAndOrdinaryControls(t *testing.T) {
	for _, payload := range []string{`null`, `{}`, `{"data":null,"format":"wav"}`, `{"data":12,"format":"wav"}`, `{"data":"aA==","format":12}`, `{"data":"aA==","format":null}`, `{"data":"","format":"wav"}`, `{"data":"aB==","format":"wav"}`, `{"data":"%%%","format":"wav"}`, `{"data":"aA==","format":"pcm16"}`, `"oops"`, `[]`} {
		t.Run(payload, func(t *testing.T) {
			req := &ChatCompletionsRequest{Model: "gemini-2.5-flash", Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`[{"type":"input_audio","input_audio":` + payload + `}]`)}}}
			_, err := ChatCompletionsToResponsesForGemini(req)
			require.ErrorIs(t, err, ErrInvalidInputAudio)
		})
	}
	for _, content := range []string{`"ordinary text"`, `[{"type":"text","text":"hello"}]`, `[{"type":"file","file":{"file_data":"data:application/pdf;base64,aA=="}}]`, `[null]`, `[{"type":"text","text":null}]`, `[{"type":"image_url","image_url":null}]`, `[{"type":"file","file":null}]`} {
		t.Run(content, func(t *testing.T) {
			req := &ChatCompletionsRequest{Model: "gemini-2.5-flash", Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(content)}}}
			ordinary, err := ChatCompletionsToResponses(req)
			require.NoError(t, err)
			audio, err := ChatCompletionsToResponsesForGemini(req)
			require.NoError(t, err)
			require.Equal(t, ordinary, audio)
		})
	}
}

func TestChatInputAudio_OrdinaryFilesKeepExistingWhitelist(t *testing.T) {
	for _, part := range []string{
		`{"type":"file","file":{"file_data":"data:audio/wav;base64,aA=="}}`,
		`{"type":"file","file":{"file_data":"data:application/pdf;base64,aA=="}}`,
		`{"type":"file","file":{"file_id":"file-existing"}}`,
	} {
		t.Run(part, func(t *testing.T) {
			req := &ChatCompletionsRequest{Model: "gemini-2.5-flash", Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"control"},` + part + `]`)}}}
			responses, err := ChatCompletionsToResponsesForGemini(req)
			require.NoError(t, err)
			ordinary, err := ResponsesToAnthropicRequest(responses)
			require.NoError(t, err)
			gemini, err := ResponsesToAnthropicRequestForGemini(responses)
			require.NoError(t, err)
			require.Equal(t, ordinary, gemini, "only generated validated audio may expand the Gemini intermediate whitelist")
			require.JSONEq(t, `[{"type":"text","text":"control"}]`, string(gemini.Messages[0].Content))
		})
	}
}

func TestChatInputAudio_RejectsForgedIntermediateType(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		req := &ChatCompletionsRequest{Model: "gemini-2.5-flash", Messages: []ChatMessage{{Role: role, Content: json.RawMessage(`[{"type":"sub2api_audio_file","file":{"file_data":"data:audio/wav;base64,%%%"}}]`)}}}
		_, err := ChatCompletionsToResponses(req)
		require.ErrorIs(t, err, ErrInvalidInputAudio)
		_, err = ChatCompletionsToResponsesForGemini(req)
		require.ErrorIs(t, err, ErrInvalidInputAudio)
	}
}
