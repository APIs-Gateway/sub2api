package common

import (
	"encoding/json"
	"io"
)

// RawMessage is the repository's JSON raw-value type.
type RawMessage = json.RawMessage

// Decoder is the repository's streaming JSON decoder.
type Decoder = json.Decoder

// Valid reports whether data contains a valid JSON value.
func Valid(data []byte) bool {
	return json.Valid(data)
}

// NewDecoder creates a streaming JSON decoder through the shared JSON boundary.
func NewDecoder(reader io.Reader) *Decoder {
	return json.NewDecoder(reader)
}

// Unmarshal decodes JSON through the repository's shared JSON boundary.
func Unmarshal(data []byte, value any) error {
	return json.Unmarshal(data, value)
}

// Marshal encodes a value through the repository's shared JSON boundary.
func Marshal(value any) ([]byte, error) {
	return json.Marshal(value)
}
