package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type chatAudioRawField struct {
	name  string
	value json.RawMessage
}

// ValidateChatInputAudioRaw checks the outer fields before typed decoding can
// hide audio behind another messages/content value or change its role. It does
// not change ordinary text decoding or the existing strict content-part rules.
func ValidateChatInputAudioRaw(body []byte, allowAudio bool) error {
	// Leave malformed JSON to each route's existing parse error handling.
	if !json.Valid(body) {
		return nil
	}
	fields := chatAudioRawFields(body)
	messagesFields, hasAudio, ambiguous := 0, false, false
	for _, field := range fields {
		if !strings.EqualFold(field.name, "messages") {
			continue
		}
		messagesFields++
		var messages []json.RawMessage
		if json.Unmarshal(field.value, &messages) != nil {
			continue
		}
		for _, message := range messages {
			contents, roles, messageAudio := 0, 0, false
			for _, member := range chatAudioRawFields(message) {
				if strings.EqualFold(member.name, "role") {
					roles++
				}
				if !strings.EqualFold(member.name, "content") {
					continue
				}
				contents++
				var parts []json.RawMessage
				if json.Unmarshal(member.value, &parts) != nil {
					continue
				}
				for _, part := range parts {
					audio, _ := chatAudioPartTypes(part)
					messageAudio = messageAudio || audio
				}
			}
			hasAudio = hasAudio || messageAudio
			ambiguous = ambiguous || (messageAudio && (contents > 1 || roles > 1))
		}
	}
	if !hasAudio || (!ambiguous && messagesFields <= 1) {
		return nil
	}
	if !allowAudio {
		return ErrUnsupportedInputAudio
	}
	return fmt.Errorf("%w: ambiguous messages, content or role fields", ErrInvalidInputAudio)
}

// Only the fixed request/message/content levels are inspected. Raw descriptor
// values are not recursively walked or logged, and the request is not rebuilt.
func chatAudioRawFields(raw json.RawMessage) []chatAudioRawField {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil
	}
	var fields []chatAudioRawField
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil
		}
		name, ok := key.(string)
		if !ok {
			return nil
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil
		}
		fields = append(fields, chatAudioRawField{name: name, value: value})
	}
	return fields
}
