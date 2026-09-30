package openai

import (
	"strings"
	"testing"
)

func TestGPT61SolIdentityAndEffort(t *testing.T) {
	found := false
	for _, id := range DefaultModelIDs() {
		if id == "gpt-6.1-sol" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("gpt-6.1-sol missing from DefaultModels")
	}

	for _, id := range []string{"gpt-6.1-sol", "openai/gpt-6.1-sol-max", "GPT_6.1_SOL", "gpt-6.1-sol-openai-compact", "gpt-6.1-sol-2026-09-29", "gpt-6.1-sol-minimal"} {
		if !IsGPT61SolModelSpelling(id) {
			t.Errorf("IsGPT61SolModelSpelling(%q) = false, want true", id)
		}
		if IsGPT6SolOrLunaModelSpelling(id) {
			t.Errorf("IsGPT6SolOrLunaModelSpelling(%q) = true, want false", id)
		}
	}
	for _, id := range []string{"gpt-6.1", "gpt-6.1-solitude", "gpt-6.1-sol-preview", "gpt-6-sol", ""} {
		if IsGPT61SolModelSpelling(id) {
			t.Errorf("IsGPT61SolModelSpelling(%q) = true, want false", id)
		}
	}

	for _, effort := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		if err := ValidateGPT61SolReasoningEffort("gpt-6.1-sol", effort); err != nil {
			t.Errorf("effort %q rejected: %v", effort, err)
		}
	}
	for _, effort := range []string{"none", "minimal", " None "} {
		if err := ValidateGPT61SolReasoningEffort("gpt-6.1-sol", effort); err == nil {
			t.Errorf("effort %q accepted for gpt-6.1-sol", effort)
		}
		if err := ValidateGPT61SolReasoningEffort("gpt-6-sol", effort); err != nil {
			t.Errorf("effort %q rejected for gpt-6-sol: %v", effort, err)
		}
	}
}

func TestGPT61SolBaseInstructions(t *testing.T) {
	got := CodexBaseInstructionsForModel("openai/gpt-6.1-sol-high")
	if !strings.Contains(got, "based on GPT-6") {
		t.Fatalf("gpt-6.1-sol instructions do not look like the GPT-6.1 Sol prompt: %.80q", got)
	}
	if got == CodexBaseInstructionsForModel("gpt-6-sol") {
		t.Fatal("gpt-6.1-sol should not share the gpt-6-sol fallback prompt")
	}
}
