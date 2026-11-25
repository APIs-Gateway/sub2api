package apicompat

import "testing"

func TestIsReasoningModel_Exported(t *testing.T) {
	for _, m := range []string{"gpt-5", "gpt-5.3-codex", "gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol", "GPT-6-LUNA"} {
		if !IsReasoningModel(m) {
			t.Errorf("IsReasoningModel(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"gpt-4.1", "gpt-4o", "gpt-image-1", "claude-sonnet-5", ""} {
		if IsReasoningModel(m) {
			t.Errorf("IsReasoningModel(%q) = true, want false", m)
		}
	}
}
