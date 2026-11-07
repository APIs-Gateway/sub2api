package openai

import (
	"strings"
	"testing"
)

func TestGPT61TerraIdentity(t *testing.T) {
	for _, id := range []string{"gpt-6.1-terra", "openai/GPT-6.1-Terra-max", "gpt_6.1_terra", "gpt-6.1-terra-none", "gpt-6.1-terra-openai-compact", "gpt-6.1-terra-minimal", "gpt-6.1-terra-2026-10-27"} {
		if !IsGPT61TerraModelSpelling(id) {
			t.Errorf("IsGPT61TerraModelSpelling(%q) = false, want true", id)
		}
		if IsGPT61SolModelSpelling(id) || IsGPT6SolOrLunaModelSpelling(id) {
			t.Errorf("%q must not be recognized as Sol/Luna", id)
		}
		if !RequiresResponsesForToolCalls(id) {
			t.Errorf("RequiresResponsesForToolCalls(%q) = false", id)
		}
	}
	for _, id := range []string{"", "gpt-6.1", "gpt-5.6-terra", "gpt-6-terra", "gpt-6.1-sol", "gpt-6.1-terra-pro", "gpt-6.1-terra-ultra", "gpt-6.1-terra-2026-1-27", "gpt-6.1-terra-preview", "gpt-6.1-terrace"} {
		if IsGPT61TerraModelSpelling(id) {
			t.Errorf("IsGPT61TerraModelSpelling(%q) = true, want false", id)
		}
	}
	if !RequiresResponsesForToolCalls("gpt-6.1-sol") || RequiresResponsesForToolCalls("gpt-6-sol") || RequiresResponsesForToolCalls("gpt-5.6-terra") {
		t.Error("RequiresResponsesForToolCalls must cover only GPT-6.1 Sol/Terra")
	}
	found := false
	for _, id := range DefaultModelIDs() {
		found = found || id == "gpt-6.1-terra"
	}
	if !found {
		t.Fatal("gpt-6.1-terra missing from DefaultModels")
	}
	// Terra accepts none; Sol validation stays Sol-only.
	if err := ValidateGPT61SolReasoningEffort("gpt-6.1-terra", "none"); err != nil {
		t.Errorf("Terra none rejected: %v", err)
	}
}

func TestGPT61TerraUsesLatestBaseInstructions(t *testing.T) {
	// 上游没有 Terra 专用 prompt，落到默认的最新 Codex prompt；不得拿到 Sol / Astra 的 prompt。
	got := CodexBaseInstructionsForModel("openai/gpt-6.1-terra-high")
	if strings.TrimSpace(got) == "" {
		t.Fatal("empty instructions")
	}
	if got != latestCodexInstructions() {
		t.Fatal("gpt-6.1-terra should use the latest default Codex instructions")
	}
	if got == CodexBaseInstructionsForModel("gpt-6.1-sol") || got == CodexBaseInstructionsForModel("gpt-6") {
		t.Fatal("gpt-6.1-terra must not share the Sol/Astra prompt")
	}
}
