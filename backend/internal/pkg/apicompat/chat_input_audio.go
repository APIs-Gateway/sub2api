package apicompat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
			audio, reserved := chatAudioPartTypes(rawPart)
			if reserved {
				return nil, fmt.Errorf("%w: reserved intermediate content type", ErrInvalidInputAudio)
			}
			hasAudio = hasAudio || audio
		}
		if !hasAudio {
			continue
		}
		if !allowAudio || message.Role != "user" {
			return nil, ErrUnsupportedInputAudio
		}
		for _, rawPart := range rawParts {
			if err := chatAudioUniqueJSONKeys(json.NewDecoder(bytes.NewReader(rawPart))); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidInputAudio, err)
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(rawPart, &fields) != nil || fields == nil {
				return nil, fmt.Errorf("%w: content parts must be objects", ErrInvalidInputAudio)
			}
			if !chatAudioCanonicalFields(fields, "type", "text", "image_url", "file", "input_audio", "prompt_cache_breakpoint") {
				return nil, fmt.Errorf("%w: noncanonical content field", ErrInvalidInputAudio)
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
					if !chatAudioCanonicalFields(descriptor, "url", "detail", "file_id", "file_data", "filename", "data", "format") {
						return nil, fmt.Errorf("%w: noncanonical descriptor field", ErrInvalidInputAudio)
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

// Inspect every root type member before typed decoding can hide one behind a
// duplicate or case-insensitive alias. Ordinary no-audio parts retain their
// original decoding behavior.
func chatAudioPartTypes(raw json.RawMessage) (audio, reserved bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false, false
	}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return audio, reserved
		}
		name, ok := token.(string)
		if !ok {
			return audio, reserved
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return audio, reserved
		}
		if strings.EqualFold(name, "type") {
			var kind string
			if json.Unmarshal(value, &kind) == nil {
				audio = audio || kind == "input_audio"
				reserved = reserved || kind == geminiChatAudioFileType
			}
		}
	}
	return audio, reserved
}

func chatAudioCanonicalFields(fields map[string]json.RawMessage, canonical ...string) bool {
	for name := range fields {
		for _, expected := range canonical {
			if name != expected && strings.EqualFold(name, expected) {
				return false
			}
		}
	}
	return true
}

// Decoder tokens expose decoded keys, including escaped duplicates, before a
// map or struct can choose one conflicting value.
func chatAudioUniqueJSONKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate or invalid content key")
			}
			seen[name] = true
			if err := chatAudioUniqueJSONKeys(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case json.Delim('['):
		for decoder.More() {
			if err := chatAudioUniqueJSONKeys(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	}
	return nil
}
