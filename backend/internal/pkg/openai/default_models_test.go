package openai

import "testing"

func TestDefaultModelsIncludeGPT56SeriesFirst(t *testing.T) {
	want := []struct {
		id          string
		displayName string
	}{
		{id: "gpt-5.6-sol", displayName: "GPT-5.6 Sol"},
		{id: "gpt-5.6-terra", displayName: "GPT-5.6 Terra"},
		{id: "gpt-5.6-luna", displayName: "GPT-5.6 Luna"},
	}

	if len(DefaultModels) < len(want) {
		t.Fatalf("DefaultModels length = %d, want at least %d", len(DefaultModels), len(want))
	}
	for i, item := range want {
		if DefaultModels[i].ID != item.id {
			t.Fatalf("DefaultModels[%d].ID = %q, want %q", i, DefaultModels[i].ID, item.id)
		}
		if DefaultModels[i].DisplayName != item.displayName {
			t.Fatalf("DefaultModels[%d].DisplayName = %q, want %q", i, DefaultModels[i].DisplayName, item.displayName)
		}
	}
}
