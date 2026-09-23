package openai

import "testing"

// TestCodexUsageProbeModelIsRegistered 保证 CodexUsageProbeModel 使用的模型名
// 始终是 DefaultModels 里注册的真实模型，避免探针请求的模型名与目录出现漂移
// 而重新触发上游 #5759 修复的那个 400（部分账号 Codex 额度查询失败）。
func TestCodexUsageProbeModelIsRegistered(t *testing.T) {
	if CodexUsageProbeModel != "codex-auto-review" {
		t.Fatalf("CodexUsageProbeModel = %q, want %q", CodexUsageProbeModel, "codex-auto-review")
	}

	found := false
	for _, id := range DefaultModelIDs() {
		if id == CodexUsageProbeModel {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("CodexUsageProbeModel %q not found in DefaultModelIDs()", CodexUsageProbeModel)
	}
}

// TestDefaultModelsIncludeGPT6Astra 保证 GPT-6 Astra 及其裸别名 gpt-6 都注册在
// DefaultModels 目录里，避免账号/分组下拉列表漏掉新上线的模型。
func TestDefaultModelsIncludeGPT6Astra(t *testing.T) {
	ids := DefaultModelIDs()
	found6Astra := false
	found6 := false
	for _, id := range ids {
		if id == "gpt-6-astra" {
			found6Astra = true
		}
		if id == "gpt-6" {
			found6 = true
		}
	}
	if !found6Astra {
		t.Fatalf("gpt-6-astra not found in DefaultModelIDs()")
	}
	if !found6 {
		t.Fatalf("gpt-6 not found in DefaultModelIDs()")
	}
}

func TestGPT6SolLunaModelIdentity(t *testing.T) {
	ids := map[string]bool{}
	for _, id := range DefaultModelIDs() {
		ids[id] = true
	}
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		if !ids[model] {
			t.Fatalf("%s not found in DefaultModelIDs()", model)
		}
	}
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna", "openai/gpt-6-sol-max", "GPT-6-Luna-openai-compact", "gpt_6_sol_high", "gpt-6-luna-2026-09-22"} {
		if !IsGPT6SolOrLunaModelSpelling(model) {
			t.Fatalf("IsGPT6SolOrLunaModelSpelling(%q) = false, want true", model)
		}
	}
	for _, model := range []string{"gpt-6", "gpt-6-astra", "gpt-6-solitude", "gpt-6-luna-preview", "gpt-6-sol-2026-9-22", "gpt-5.6-sol", "gpt-5.6-luna"} {
		if IsGPT6SolOrLunaModelSpelling(model) {
			t.Fatalf("IsGPT6SolOrLunaModelSpelling(%q) = true, want false", model)
		}
	}
}

func TestDefaultModelsIncludeGPTImage25(t *testing.T) {
	ids := make(map[string]bool)
	for _, id := range DefaultModelIDs() {
		ids[id] = true
	}
	for _, want := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		if !ids[want] {
			t.Fatalf("DefaultModelIDs() missing %q", want)
		}
	}
}
