package claude

import "testing"

func TestDefaultModelsIncludeSonnet5(t *testing.T) {
	t.Parallel()

	for _, model := range DefaultModels {
		if model.ID == "claude-sonnet-5" {
			if model.DisplayName != "Claude Sonnet 5" {
				t.Fatalf("Sonnet 5 display name = %q", model.DisplayName)
			}
			return
		}
	}

	t.Fatal("default Claude models do not include Sonnet 5")
}
