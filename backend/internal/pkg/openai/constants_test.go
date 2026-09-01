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
