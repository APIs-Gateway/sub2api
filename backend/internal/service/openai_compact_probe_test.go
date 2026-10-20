package service

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestNormalizeAccountTestMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: AccountTestModeDefault},
		{input: "default", want: AccountTestModeDefault},
		{input: " compact ", want: AccountTestModeCompact},
		{input: "COMPACT", want: AccountTestModeCompact},
		{input: "unknown", want: AccountTestModeDefault},
	}

	for _, tt := range tests {
		if got := normalizeAccountTestMode(tt.input); got != tt.want {
			t.Fatalf("normalizeAccountTestMode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_SuccessMarksSupported(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	updates := buildOpenAICompactProbeExtraUpdates(&http.Response{StatusCode: http.StatusOK}, []byte(`{"output":[{"type":"compaction"}]}`), nil, true, now)

	if got := updates["openai_compact_supported"]; got != true {
		t.Fatalf("openai_compact_supported = %v, want true", got)
	}
	if got := updates["openai_compact_last_status"]; got != http.StatusOK {
		t.Fatalf("openai_compact_last_status = %v, want %d", got, http.StatusOK)
	}
	if got := updates["openai_compact_last_error"]; got != "" {
		t.Fatalf("openai_compact_last_error = %v, want empty string", got)
	}
	if got := updates["openai_compact_checked_at"]; got != now.Format(time.RFC3339) {
		t.Fatalf("openai_compact_checked_at = %v, want %s", got, now.Format(time.RFC3339))
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_2xxWithoutCompactionItemMarksUnsupported(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	updates := buildOpenAICompactProbeExtraUpdates(&http.Response{StatusCode: http.StatusOK}, []byte(`{"output":[]}`), nil, false, now)

	if got := updates["openai_compact_supported"]; got != false {
		t.Fatalf("openai_compact_supported = %v, want false", got)
	}
	if got, _ := updates["openai_compact_last_error"].(string); got == "" {
		t.Fatalf("expected openai_compact_last_error to explain the missing compaction item")
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_404MarksUnsupported(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	body := []byte(`404 page not found`)
	updates := buildOpenAICompactProbeExtraUpdates(&http.Response{StatusCode: http.StatusNotFound}, body, nil, false, now)

	if got := updates["openai_compact_supported"]; got != false {
		t.Fatalf("openai_compact_supported = %v, want false", got)
	}
	if got := updates["openai_compact_last_status"]; got != http.StatusNotFound {
		t.Fatalf("openai_compact_last_status = %v, want %d", got, http.StatusNotFound)
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_502DoesNotMarkUnsupported(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	updates := buildOpenAICompactProbeExtraUpdates(&http.Response{StatusCode: http.StatusBadGateway}, []byte(`Upstream request failed`), nil, false, now)

	if _, exists := updates["openai_compact_supported"]; exists {
		t.Fatalf("did not expect openai_compact_supported for 502 response")
	}
	if got := updates["openai_compact_last_status"]; got != http.StatusBadGateway {
		t.Fatalf("openai_compact_last_status = %v, want %d", got, http.StatusBadGateway)
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_RequestErrorDoesNotMarkUnsupported(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	updates := buildOpenAICompactProbeExtraUpdates(nil, nil, errors.New("dial tcp timeout"), false, now)

	if _, exists := updates["openai_compact_supported"]; exists {
		t.Fatalf("did not expect openai_compact_supported for request error")
	}
	if got, exists := updates["openai_compact_last_status"]; !exists || got != nil {
		t.Fatalf("openai_compact_last_status = %v, want nil key", got)
	}
	if got := updates["openai_compact_last_error"]; got == "" {
		t.Fatalf("expected openai_compact_last_error to be populated")
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_NoResponseClearsLastStatus(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	updates := buildOpenAICompactProbeExtraUpdates(nil, nil, nil, false, now)

	if got, exists := updates["openai_compact_last_status"]; !exists || got != nil {
		t.Fatalf("openai_compact_last_status = %v, want nil key", got)
	}
	if got := updates["openai_compact_last_error"]; got != "compact probe failed" {
		t.Fatalf("openai_compact_last_error = %v, want compact probe failed", got)
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_UnknownModelDoesNotMarkUnsupported(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	body := []byte(`{"error":{"message":"unknown model gpt-5.4-openai-compact"}}`)
	updates := buildOpenAICompactProbeExtraUpdates(&http.Response{StatusCode: http.StatusBadRequest}, body, nil, false, now)

	if _, exists := updates["openai_compact_supported"]; exists {
		t.Fatalf("did not expect openai_compact_supported for unknown-model diagnostics")
	}
	if got := updates["openai_compact_last_status"]; got != http.StatusBadRequest {
		t.Fatalf("openai_compact_last_status = %v, want %d", got, http.StatusBadRequest)
	}
}

func TestBuildOpenAICompactProbeExtraUpdates_EmptyFailureBodyFallsBackToHTTPStatus(t *testing.T) {
	now := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	updates := buildOpenAICompactProbeExtraUpdates(&http.Response{StatusCode: http.StatusServiceUnavailable}, nil, nil, false, now)

	if got := updates["openai_compact_last_status"]; got != http.StatusServiceUnavailable {
		t.Fatalf("openai_compact_last_status = %v, want %d", got, http.StatusServiceUnavailable)
	}
	if got := updates["openai_compact_last_error"]; got != "HTTP 503" {
		t.Fatalf("openai_compact_last_error = %v, want HTTP 503", got)
	}
}

func TestCreateOpenAICompactProbePayload(t *testing.T) {
	oauthPayload := createOpenAICompactProbePayload(" gpt-5.4-codex ", true)
	if got := oauthPayload["model"]; got != "gpt-5.4-codex" {
		t.Fatalf("model = %v, want trimmed model", got)
	}
	if got := oauthPayload["stream"]; got != true {
		t.Fatalf("stream = %v, want true", got)
	}
	if got, ok := oauthPayload["store"]; !ok || got != false {
		t.Fatalf("store = %v (present=%v), want false for OAuth", got, ok)
	}
	input, ok := oauthPayload["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("input = %#v, want 2 items", oauthPayload["input"])
	}
	trigger, ok := input[1].(map[string]any)
	if !ok || trigger["type"] != "compaction_trigger" {
		t.Fatalf("input[1] = %#v, want compaction_trigger item", input[1])
	}

	apiKeyPayload := createOpenAICompactProbePayload("gpt-4o", false)
	if _, exists := apiKeyPayload["store"]; exists {
		t.Fatalf("did not expect store field for non-OAuth probe payload")
	}
}

func TestOpenAICompactProbeFoundCompactionItem(t *testing.T) {
	if openAICompactProbeFoundCompactionItem(nil) {
		t.Fatal("empty body must not report a compaction item")
	}
	if openAICompactProbeFoundCompactionItem([]byte(`{"output":[]}`)) {
		t.Fatal("empty output array must not report a compaction item")
	}
	// ① SSE output_item.done 主形态
	sse := []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"id\":\"cmp_1\"}}\n\ndata: [DONE]\n\n")
	if !openAICompactProbeFoundCompactionItem(sse) {
		t.Fatal("expected SSE output_item.done compaction item to be found")
	}
	// ③ 整体 JSON output[] 兜底形态（老网关链降级成 unary）
	if !openAICompactProbeFoundCompactionItem([]byte(`{"output":[{"type":"compaction","id":"cmp_2"}]}`)) {
		t.Fatal("expected whole-JSON output[] compaction item to be found")
	}
}

func TestCompactProbeSessionID(t *testing.T) {
	if got := compactProbeSessionID(0); got == "" {
		t.Fatal("expected a non-empty anonymous session id")
	}
	if got := compactProbeSessionID(-5); got != compactProbeSessionID(0) {
		t.Fatalf("negative account id must fall back to the same anonymous id: got %q", got)
	}
	a := compactProbeSessionID(42)
	b := compactProbeSessionID(42)
	if a != b {
		t.Fatalf("compactProbeSessionID must be stable for the same account id: %q != %q", a, b)
	}
	if a == compactProbeSessionID(43) {
		t.Fatal("compactProbeSessionID must differ across account ids")
	}
	// UUIDv4 形态：36 字符，含 4 个连字符。
	if len(a) != 36 {
		t.Fatalf("compactProbeSessionID length = %d, want 36 (UUID form)", len(a))
	}
}
