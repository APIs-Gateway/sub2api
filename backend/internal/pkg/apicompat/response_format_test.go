package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestChatResponseFormatToResponsesTextFormat_PreservesUnsupportedFormats(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want json.RawMessage
	}{
		{name: "empty", raw: json.RawMessage(" \t\n"), want: nil},
		{name: "array", raw: json.RawMessage(`["json_schema"]`), want: json.RawMessage(`["json_schema"]`)},
		{name: "missing schema", raw: json.RawMessage(`{"type":"json_schema"}`), want: json.RawMessage(`{"type":"json_schema"}`)},
		{name: "non-object schema", raw: json.RawMessage(`{"type":"json_schema","json_schema":[]}`), want: json.RawMessage(`{"type":"json_schema","json_schema":[]}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, chatResponseFormatToResponsesTextFormat(tt.raw))
		})
	}
}

func TestResponsesTextFormatToChatResponseFormat_PreservesUnsupportedFormats(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want json.RawMessage
	}{
		{name: "empty", raw: json.RawMessage(" \t\n"), want: nil},
		{name: "array", raw: json.RawMessage(`["json_schema"]`), want: json.RawMessage(`["json_schema"]`)},
		{name: "already chat shape", raw: json.RawMessage(`{"type":"json_schema","json_schema":{"name":"answer"}}`), want: json.RawMessage(`{"type":"json_schema","json_schema":{"name":"answer"}}`)},
		{name: "missing schema", raw: json.RawMessage(`{"type":"json_schema"}`), want: json.RawMessage(`{"type":"json_schema"}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, responsesTextFormatToChatResponseFormat(tt.raw))
		})
	}
}
