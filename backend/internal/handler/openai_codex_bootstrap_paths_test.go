package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	codexAutomationBootstrapOutput = "Automation: Nightly report\n" +
		"Automation ID: nightly-report\n" +
		"Automation memory: $CODEX_HOME/automations/nightly-report/memory.md\n" +
		"Last run: never\n" +
		"\n" +
		"Summarize the open issues."
	codexHeartbeatOutput = "<heartbeat><automation_id>nightly-report</automation_id></heartbeat>"
)

func codexCallOutputItem(namespace, name, output string) map[string]any {
	return map[string]any{
		"type":      "function_call_output",
		"namespace": namespace,
		"name":      name,
		"output":    output,
	}
}

func marshalCodexBootstrapBody(t *testing.T, fields map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	return string(raw)
}

func runOpenAIResponsesForBootstrapTest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	groupID := int64(2)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID:      101,
		GroupID: &groupID,
		User:    &service.User{ID: 1},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{
		UserID:      1,
		Concurrency: 1,
	})

	h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
	h.Responses(c)
	return w
}

// The normalizers run in Responses() right after the model is read and before
// stream parsing, so an invalid stream field still reports its own error after
// a bootstrap body has been normalized.
func TestOpenAIResponses_CodexBootstrapNormalizedBeforeStreamValidation(t *testing.T) {
	tests := []struct {
		name string
		item map[string]any
	}{
		{name: "automation", item: codexCallOutputItem("codex_app", "automation_update", codexAutomationBootstrapOutput)},
		{name: "heartbeat", item: codexCallOutputItem("codex_app", "automation_update", codexHeartbeatOutput)},
		{name: "delegation", item: codexCallOutputItem("codex_app", "create_thread", delegationEnvelope)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := marshalCodexBootstrapBody(t, map[string]any{
				"model":  "gpt-5.1",
				"stream": "yes",
				"input":  []any{tt.item},
			})
			w := runOpenAIResponsesForBootstrapTest(t, body)
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Contains(t, w.Body.String(), invalidStreamFieldTypeMessage)
		})
	}
}

// Delegation may be normalized alongside previous_response_id; the HTTP path
// then rejects previous_response_id exactly as before (WS v2 only).
func TestOpenAIResponses_CodexDelegationWithPreviousResponseIDStillRequiresWSv2(t *testing.T) {
	body := marshalCodexBootstrapBody(t, map[string]any{
		"model":                "gpt-5.1",
		"stream":               false,
		"previous_response_id": "resp_123456",
		"input":                []any{codexCallOutputItem("codex_tui", "send_message_to_thread", delegationEnvelope)},
	})
	w := runOpenAIResponsesForBootstrapTest(t, body)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "Responses WebSocket v2")
}

func TestNormalizeCodexCallOutputBootstrap_RejectsMalformedRequests(t *testing.T) {
	delegation := codexCallOutputItem("codex_app", "create_thread", delegationEnvelope)
	tests := []struct {
		name string
		body string
	}{
		{name: "trailing data", body: `{"input":[]} {}`},
		{name: "invalid json", body: `{"input":`},
		{name: "top-level scalar", body: `"x"`},
		{name: "previous_response_id not string", body: marshalCodexBootstrapBody(t, map[string]any{"previous_response_id": 1, "input": []any{delegation}})},
		{name: "input not array", body: `{"input":"hello"}`},
		{name: "call_id not string", body: marshalCodexBootstrapBody(t, map[string]any{"input": []any{map[string]any{
			"type": "function_call_output", "namespace": "codex_app", "name": "create_thread", "output": delegationEnvelope, "call_id": 7,
		}}})},
		{name: "bare item_reference", body: marshalCodexBootstrapBody(t, map[string]any{"input": []any{delegation, map[string]any{"type": "item_reference"}}})},
		{name: "unanchored call", body: marshalCodexBootstrapBody(t, map[string]any{"input": []any{delegation, map[string]any{"type": "function_call"}}})},
		{name: "unanchored tool_search_output", body: marshalCodexBootstrapBody(t, map[string]any{"input": []any{delegation, map[string]any{"type": "tool_search_output"}}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := normalizeCodexDelegationBootstrap([]byte(tt.body))
			require.False(t, changed)
			require.Equal(t, tt.body, string(got))
		})
	}
}

func TestNormalizeCodexCallOutputBootstrap_SkipsNonCandidateItems(t *testing.T) {
	body := marshalCodexBootstrapBody(t, map[string]any{
		"input": []any{"plain string item", map[string]any{"type": "message", "role": "user", "content": "hi"}},
	})
	got, changed := normalizeCodexDelegationBootstrap([]byte(body))
	require.False(t, changed)
	require.Equal(t, body, string(got))
}

func TestNormalizeCodexAutomationBootstrap_RejectsHistoricalContext(t *testing.T) {
	item := codexCallOutputItem("codex_app", "automation_update", codexAutomationBootstrapOutput)
	for _, body := range []string{
		marshalCodexBootstrapBody(t, map[string]any{"previous_response_id": "resp_1", "input": []any{item}}),
		marshalCodexBootstrapBody(t, map[string]any{"input": []any{item, map[string]any{"type": "item_reference", "id": "msg_1"}}}),
		marshalCodexBootstrapBody(t, map[string]any{"input": []any{item, map[string]any{"type": "function_call", "call_id": "call_1"}}}),
	} {
		_, changed := normalizeCodexAutomationBootstrap([]byte(body))
		require.False(t, changed, body)
	}
}

func TestValidCodexAutomationBootstrap_RejectsMalformedHeaders(t *testing.T) {
	valid := strings.Split(codexAutomationBootstrapOutput, "\n")
	with := func(index int, line string) string {
		lines := append([]string(nil), valid...)
		lines[index] = line
		return strings.Join(lines, "\n")
	}
	tests := map[string]string{
		"bare carriage return":  strings.Replace(codexAutomationBootstrapOutput, "\n", "\r", 1),
		"too few lines":         "Automation: x\nAutomation ID: y",
		"missing name header":   with(0, "Name: Nightly report"),
		"padded name header":    with(0, "Automation:  Nightly report"),
		"missing id header":     with(1, "ID: nightly-report"),
		"id with slash":         with(1, "Automation ID: ../etc"),
		"id dot":                with(1, "Automation ID: ."),
		"memory path mismatch":  with(2, "Automation memory: /tmp/memory.md"),
		"last run not rfc3339":  with(3, "Last run: yesterday (1)"),
		"last run no epoch":     with(3, "Last run: 2026-09-01T00:00:00Z"),
		"last run bad epoch":    with(3, "Last run: 2026-09-01T00:00:00Z (abc)"),
		"last run epoch differ": with(3, "Last run: 2026-09-01T00:00:00Z (1)"),
		"separator not blank":   with(4, "not blank"),
		"empty prompt":          with(5, "   "),
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			require.False(t, validCodexAutomationBootstrap(value))
		})
	}
	require.True(t, validCodexAutomationBootstrap(strings.ReplaceAll(codexAutomationBootstrapOutput, "\n", "\r\n")))
	require.True(t, validCodexAutomationBootstrap(with(3, "Last run: 2026-09-01T00:00:00Z (1788220800000)")))
}

func TestValidCodexAutomationID_Bounds(t *testing.T) {
	require.False(t, validCodexAutomationID(""))
	require.False(t, validCodexAutomationID(".."))
	require.False(t, validCodexAutomationID(strings.Repeat("a", 129)))
	require.False(t, validCodexAutomationID("has space"))
	require.True(t, validCodexAutomationID("A-z_0.9"))
}

func TestValidCodexAutomationHeartbeat_RejectsUnsafeXML(t *testing.T) {
	tests := map[string]string{
		"malformed":         "<heartbeat><automation_id>x</heartbeat>",
		"namespaced start":  `<x:heartbeat xmlns:x="urn:x"><automation_id>x</automation_id></x:heartbeat>`,
		"attribute":         `<heartbeat a="1"><automation_id>x</automation_id></heartbeat>`,
		"too deep":          "<heartbeat><automation_id><b>x</b></automation_id></heartbeat>",
		"wrong root":        "<beat><automation_id>x</automation_id></beat>",
		"second root":       "<heartbeat><automation_id>x</automation_id></heartbeat><heartbeat></heartbeat>",
		"wrong child":       "<heartbeat><id>x</id></heartbeat>",
		"duplicate child":   "<heartbeat><automation_id>x</automation_id><automation_id>y</automation_id></heartbeat>",
		"root text":         "<heartbeat>text<automation_id>x</automation_id></heartbeat>",
		"comment":           "<heartbeat><!-- c --><automation_id>x</automation_id></heartbeat>",
		"processing instr":  `<?xml version="1.0"?><heartbeat><automation_id>x</automation_id></heartbeat>`,
		"directive":         "<!DOCTYPE heartbeat><heartbeat><automation_id>x</automation_id></heartbeat>",
		"padded id":         "<heartbeat><automation_id> x </automation_id></heartbeat>",
		"invalid id":        "<heartbeat><automation_id>a/b</automation_id></heartbeat>",
		"missing id":        "<heartbeat></heartbeat>",
		"stray end element": "</heartbeat>",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			require.False(t, validCodexAutomationHeartbeat(value))
		})
	}
	require.True(t, validCodexAutomationHeartbeat(codexHeartbeatOutput))
}

func TestValidCodexDelegationEnvelope_RejectsUnsafeXML(t *testing.T) {
	tests := map[string]string{
		"malformed":         "<codex_delegation><input>x</codex_delegation>",
		"namespaced root":   `<x:codex_delegation xmlns:x="urn:x"><source_thread_id>t</source_thread_id><input>x</input></x:codex_delegation>`,
		"attribute":         `<codex_delegation a="1"><source_thread_id>t</source_thread_id><input>x</input></codex_delegation>`,
		"wrong root":        "<delegation><source_thread_id>t</source_thread_id><input>x</input></delegation>",
		"too deep":          "<codex_delegation><input><b>x</b></input><source_thread_id>t</source_thread_id></codex_delegation>",
		"second root":       delegationEnvelope + "<codex_delegation></codex_delegation>",
		"unknown child":     "<codex_delegation><source_thread_id>t</source_thread_id><note>n</note><input>x</input></codex_delegation>",
		"empty child":       "<codex_delegation><source_thread_id> </source_thread_id><input>x</input></codex_delegation>",
		"duplicate source":  "<codex_delegation><source_thread_id>t</source_thread_id><source_thread_id>u</source_thread_id><input>x</input></codex_delegation>",
		"duplicate input":   "<codex_delegation><source_thread_id>t</source_thread_id><input>x</input><input>y</input></codex_delegation>",
		"root text":         "<codex_delegation>text<source_thread_id>t</source_thread_id><input>x</input></codex_delegation>",
		"comment":           "<codex_delegation><!-- c --><source_thread_id>t</source_thread_id><input>x</input></codex_delegation>",
		"processing instr":  `<?xml version="1.0"?>` + delegationEnvelope,
		"directive":         "<!DOCTYPE codex_delegation>" + delegationEnvelope,
		"missing input":     "<codex_delegation><source_thread_id>t</source_thread_id></codex_delegation>",
		"stray end element": "</codex_delegation>",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			require.False(t, validCodexDelegationEnvelope(value))
		})
	}
	require.True(t, validCodexDelegationEnvelope(delegationEnvelope))
}
