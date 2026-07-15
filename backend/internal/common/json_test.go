package common

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	if !Valid([]byte(`{"value":7}`)) {
		t.Fatal("Valid() returned false for valid JSON")
	}
	if Valid([]byte(`{"value":`)) {
		t.Fatal("Valid() returned true for invalid JSON")
	}
}

func TestNewDecoder(t *testing.T) {
	decoder := NewDecoder(strings.NewReader(`{"value":7}`))
	var payload struct {
		Value int `json:"value"`
	}
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("NewDecoder().Decode() error = %v", err)
	}
	if payload.Value != 7 {
		t.Fatalf("NewDecoder() value = %d, want 7", payload.Value)
	}
}

func TestUnmarshal(t *testing.T) {
	var payload struct {
		Value int `json:"value"`
	}
	if err := Unmarshal([]byte(`{"value":7}`), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload.Value != 7 {
		t.Fatalf("Unmarshal() value = %d, want 7", payload.Value)
	}

	if err := Unmarshal([]byte(`{`), &payload); err == nil {
		t.Fatal("Unmarshal() error = nil, want syntax error")
	}
}

func TestMarshal(t *testing.T) {
	payload, err := Marshal(struct {
		Value int `json:"value"`
	}{Value: 7})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(payload) != `{"value":7}` {
		t.Fatalf("Marshal() = %s, want %s", payload, `{"value":7}`)
	}
}
