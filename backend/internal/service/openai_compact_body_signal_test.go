package service

import "testing"

func TestHasCompactionTriggerInInput(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{
			name: "detects input compaction trigger",
			body: []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user"},{"type":"compaction_trigger"}]}`),
			want: true,
		},
		{
			name: "nested content is ignored",
			body: []byte(`{"input":[{"type":"message","content":[{"type":"compaction_trigger"}]}]}`),
			want: false,
		},
		{
			name: "non array input is ignored",
			body: []byte(`{"input":"compaction_trigger"}`),
			want: false,
		},
		{
			name: "invalid json is ignored",
			body: []byte(`{`),
			want: false,
		},
		{
			name: "empty body is ignored",
			body: nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasCompactionTriggerInInput(tt.body); got != tt.want {
				t.Fatalf("HasCompactionTriggerInInput() = %v, want %v", got, tt.want)
			}
		})
	}
}
