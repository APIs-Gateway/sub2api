package claude

import "testing"

func TestDefaultModelsContainsOpus55(t *testing.T) {
	for _, model := range DefaultModels {
		if model.ID == "claude-opus-5-5" {
			if model.DisplayName != "Claude Opus 5.5" || model.CreatedAt != "2026-09-22T00:00:00Z" {
				t.Fatalf("unexpected Opus 5.5 descriptor: %+v", model)
			}
			return
		}
	}
	t.Fatal("claude-opus-5-5 missing")
}

func TestIsOpus55(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "Claude-Opus-5-5", "anthropic/claude-opus-5-5", "claude-opus-5-5-20260922", "claude-opus-5-5-thinking", "models/claude-opus-5-5"} {
		if !IsOpus55(model) {
			t.Fatalf("IsOpus55(%q) = false, want true", model)
		}
	}
	for _, model := range []string{"claude-opus-5", "claude-opus-4-5", "claude-opus-4-5-20251101", "claude-opus-5-50", "claude-sonnet-5", ""} {
		if IsOpus55(model) {
			t.Fatalf("IsOpus55(%q) = true, want false", model)
		}
	}
}
