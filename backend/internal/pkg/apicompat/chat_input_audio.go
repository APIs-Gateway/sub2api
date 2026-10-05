package apicompat

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// A private intermediate type distinguishes validated Chat audio from ordinary
// file/file_id parts, whose existing filtering behavior must not change.
const geminiChatAudioFileType = "sub2api_audio_file"

var ErrUnsupportedInputAudio = errors.New("input_audio is not supported on this conversion route")
var ErrInvalidInputAudio = errors.New("invalid input_audio")

// ChatCompletionsToResponsesForGemini preserves audio only for the existing
// Gemini conversion chain. Other converters must reject instead of dropping it.
func ChatCompletionsToResponsesForGemini(req *ChatCompletionsRequest) (*ResponsesRequest, error) {
	converted, err := normalizeChatInputAudio(req, true)
	if err != nil {
		return nil, err
	}
	return chatCompletionsToResponses(converted)
}

func normalizeChatInputAudio(req *ChatCompletionsRequest, allowAudio bool) (*ChatCompletionsRequest, error) {
	converted := *req
	converted.Messages = append([]ChatMessage(nil), req.Messages...)
	for messageIndex, message := range req.Messages {
		var rawParts []json.RawMessage
		if json.Unmarshal(message.Content, &rawParts) != nil {
			continue // Ordinary string content keeps the existing conversion behavior.
		}
		hasAudio := false
		for _, rawPart := range rawParts {
			var probe struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(rawPart, &probe) == nil {
				if probe.Type == geminiChatAudioFileType {
					return nil, fmt.Errorf("%w: reserved intermediate content type", ErrInvalidInputAudio)
				}
				if probe.Type == "input_audio" {
					hasAudio = true
				}
			}
		}
		if !hasAudio {
			continue
		}
		if !allowAudio || message.Role != "user" {
			return nil, ErrUnsupportedInputAudio
		}
		for _, rawPart := range rawParts {
			var fields map[string]json.RawMessage
			if json.Unmarshal(rawPart, &fields) != nil || fields == nil {
				return nil, fmt.Errorf("%w: content parts must be objects", ErrInvalidInputAudio)
			}
			var contentType string
			if json.Unmarshal(fields["type"], &contentType) != nil || contentType == "" {
				return nil, fmt.Errorf("%w: content part type must be a non-empty string", ErrInvalidInputAudio)
			}
			for _, name := range []string{"image_url", "file", "input_audio"} {
				if raw, exists := fields[name]; exists {
					var descriptor map[string]json.RawMessage
					if json.Unmarshal(raw, &descriptor) != nil || descriptor == nil {
						return nil, fmt.Errorf("%w: %s must be an object", ErrInvalidInputAudio, name)
					}
					for _, key := range []string{"url", "file_id", "file_data", "filename"} {
						if field, exists := descriptor[key]; exists {
							var value *string
							if json.Unmarshal(field, &value) != nil || value == nil {
								return nil, fmt.Errorf("%w: %s.%s must be a string", ErrInvalidInputAudio, name, key)
							}
						}
					}
				}
			}
			if text, exists := fields["text"]; exists {
				var value *string
				if json.Unmarshal(text, &value) != nil || value == nil {
					return nil, fmt.Errorf("%w: content part text must be a string", ErrInvalidInputAudio)
				}
			}
		}
		// A malformed sibling must not make the existing typed-content decoder
		// fall back to text and silently discard an otherwise valid audio part.
		var parts []ChatContentPart
		if err := json.Unmarshal(message.Content, &parts); err != nil {
			return nil, fmt.Errorf("%w: malformed message content", ErrInvalidInputAudio)
		}
		for partIndex, part := range parts {
			if part.Type != "input_audio" {
				continue
			}
			var audio struct {
				Data   *string `json:"data"`
				Format *string `json:"format"`
			}
			if json.Unmarshal(part.InputAudio, &audio) != nil || audio.Data == nil || audio.Format == nil {
				return nil, fmt.Errorf("%w: expected data and format strings", ErrInvalidInputAudio)
			}
			mime := map[string]string{
				"wav": "audio/wav", "mp3": "audio/mpeg", "ogg": "audio/ogg",
				"flac": "audio/flac", "aac": "audio/aac", "mp4": "audio/mp4", "m4a": "audio/mp4",
			}[*audio.Format]
			if mime == "" {
				return nil, fmt.Errorf("%w: unsupported format %q", ErrInvalidInputAudio, *audio.Format)
			}
			decoded, err := base64.StdEncoding.Strict().DecodeString(*audio.Data)
			if err != nil || len(decoded) == 0 {
				return nil, fmt.Errorf("%w: expected non-empty base64 data", ErrInvalidInputAudio)
			}
			replacement, err := json.Marshal(ChatContentPart{
				Type: geminiChatAudioFileType, PromptCacheBreakpoint: part.PromptCacheBreakpoint,
				File: &ChatFile{FileData: "data:" + mime + ";base64," + *audio.Data},
			})
			if err != nil {
				return nil, err
			}
			rawParts[partIndex] = replacement
		}
		content, err := json.Marshal(rawParts)
		if err != nil {
			return nil, err
		}
		converted.Messages[messageIndex].Content = content
	}
	return &converted, nil
}
