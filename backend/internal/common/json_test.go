package common

import "testing"

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
