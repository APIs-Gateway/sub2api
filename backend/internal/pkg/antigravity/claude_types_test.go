package antigravity

import "testing"

func TestDefaultModels_ContainsNewAndLegacyImageModels(t *testing.T) {
	t.Parallel()

	models := DefaultModels()
	byID := make(map[string]ClaudeModel, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}

	requiredIDs := []string{
		"claude-fable-5",
		"claude-opus-4-8",
		"claude-opus-4-6-thinking",
		"gemini-2.5-flash-image",
		"gemini-2.5-flash-image-preview",
		"gemini-3.1-flash-image",
		"gemini-3.1-flash-image-preview",
		"gemini-3-pro-image", // legacy compatibility
	}

	for _, id := range requiredIDs {
		if _, ok := byID[id]; !ok {
			t.Fatalf("expected model %q to be exposed in DefaultModels", id)
		}
	}
}

func TestIsGeminiReasoningModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modelID string
		want    bool
	}{
		{name: "gemini-3.1-pro-high is reasoning", modelID: "gemini-3.1-pro-high", want: true},
		{name: "gemini-3-pro-high is reasoning", modelID: "gemini-3-pro-high", want: true},
		{name: "gemini-2.5-flash-thinking is reasoning", modelID: "gemini-2.5-flash-thinking", want: true},
		{name: "gemini-3-pro-preview is reasoning", modelID: "gemini-3-pro-preview", want: true},
		{name: "case-insensitive match", modelID: "GEMINI-3.1-PRO-HIGH", want: true},
		{name: "substring match on prefixed model id", modelID: "models/gemini-3.1-pro-high", want: true},
		{name: "gemini-3.1-pro-low is not reasoning", modelID: "gemini-3.1-pro-low", want: false},
		{name: "gemini-3-pro-low is not reasoning", modelID: "gemini-3-pro-low", want: false},
		{name: "gemini-2.5-flash is not reasoning", modelID: "gemini-2.5-flash", want: false},
		{name: "claude model is not a gemini reasoning model", modelID: "claude-opus-4-6-thinking", want: false},
		{name: "empty model id is not reasoning", modelID: "", want: false},
		{name: "unknown model id is not reasoning", modelID: "gemini-9000", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := IsGeminiReasoningModel(tt.modelID)
			if got != tt.want {
				t.Fatalf("IsGeminiReasoningModel(%q) = %v, want %v", tt.modelID, got, tt.want)
			}
		})
	}
}
