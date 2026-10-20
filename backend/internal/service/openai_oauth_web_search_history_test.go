package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Codex local compaction replays hosted web_search_call history with tools:[];
// ChatGPT then fails with "response protection is unavailable" (#7927).
const openAIWebSearchHistoryCompactionBody = `{"model":"gpt-5.5","instructions":"x","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look it up"}]},{"type":"web_search_call","id":"ws_123","status":"completed","action":{"type":"search","query":"kwin inputmethod"}},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"found"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"summarize"}]}],"tools":[],"tool_choice":"auto","parallel_tool_calls":false,"stream":true,"store":false}`

func TestOAuthWebSearchHistoryToolAcrossPaths(t *testing.T) {
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	transform := func(compact bool) func([]byte) ([]byte, bool, error) {
		return func(b []byte) ([]byte, bool, error) {
			var req map[string]any
			if err := json.Unmarshal(b, &req); err != nil {
				return nil, false, err
			}
			result := applyCodexOAuthTransform(req, true, compact)
			out, err := json.Marshal(req)
			return out, result.Modified, err
		}
	}
	tests := []struct {
		name      string
		normalize func([]byte) ([]byte, bool, error)
		inject    bool
	}{
		{"transformed OAuth", transform(false), true},
		{"transformed OAuth compact", transform(true), false},
		{"OAuth websocket", func(b []byte) ([]byte, bool, error) {
			return normalizeOpenAIOAuthWebSearchHistoryForAccount(b, oauth, false, false)
		}, true},
		{"OAuth passthrough compact", func(b []byte) ([]byte, bool, error) {
			return normalizeOpenAIOAuthWebSearchHistoryForAccount(b, oauth, false, true)
		}, false},
		{"API key websocket", func(b []byte) ([]byte, bool, error) {
			return normalizeOpenAIOAuthWebSearchHistoryForAccount(b, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, false, false)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := tt.normalize([]byte(openAIWebSearchHistoryCompactionBody))
			require.NoError(t, err)
			require.Equal(t, "web_search_call", gjson.GetBytes(out, "input.1.type").String())
			if !tt.inject {
				require.False(t, gjsonToolsContainWebSearch(gjson.GetBytes(out, "tools")))
				return
			}
			tools := gjson.GetBytes(out, "tools").Array()
			require.Len(t, tools, 1)
			require.Equal(t, "web_search", tools[0].Get("type").String())
			require.False(t, tools[0].Get("external_web_access").Bool())
			require.True(t, tools[0].Get("external_web_access").Exists())
			require.Equal(t, "none", gjson.GetBytes(out, "tool_choice").String())
		})
	}
}

func TestEnsureOpenAIOAuthWebSearchToolForHistory(t *testing.T) {
	const (
		user    = `{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]}`
		search  = `{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}}`
		trigger = `{"type":"compaction_trigger"}`
		history = `[` + user + `,` + search + `]`
	)
	tests := []struct {
		name       string
		body       string
		lite       bool
		changed    bool
		toolsPath  string
		toolCount  int
		toolChoice string
		itemTypes  []string
	}{
		{"tools absent", `{"input":` + history + `}`, false, true, "tools", 1, "none", nil},
		{"tools null", `{"input":` + history + `,"tools":null,"tool_choice":null}`, false, true, "tools", 1, "none", nil},
		{"explicit none kept", `{"input":` + history + `,"tools":[],"tool_choice":"none"}`, false, true, "tools", 1, "none", nil},
		{"required not rewritten", `{"input":` + history + `,"tools":[],"tool_choice":"required"}`, false, true, "tools", 1, "required", nil},
		{"caller tools keep choice", `{"input":` + history + `,"tools":[{"type":"function","name":"echo"}],"tool_choice":"auto"}`, false, true, "tools", 2, "auto", nil},
		{"web_search already declared", `{"input":` + history + `,"tools":[{"type":"web_search"}]}`, false, false, "", 0, "", nil},
		{"web_search_preview declared", `{"input":` + history + `,"tools":[{"type":"web_search_preview"}]}`, false, false, "", 0, "", nil},
		{"declared via additional_tools", `{"input":[{"type":"additional_tools","tools":[{"type":"web_search"}]},` + search + `]}`, false, false, "", 0, "", nil},
		{"no web_search_call item", `{"input":[{"type":"message","role":"user","content":"web_search_call"}],"tools":[]}`, false, false, "", 0, "", nil},
		{"string input", `{"input":"web_search_call"}`, false, false, "", 0, "", nil},
		{"lite inserts before compaction trigger", `{"input":[` + user + `,` + search + `,` + trigger + `]}`, true, true, "input.2.tools", 1, "none",
			[]string{"message", "web_search_call", "additional_tools", "compaction_trigger"}},
		{"lite appends without trigger", `{"input":` + history + `,"tool_choice":"auto"}`, true, true, "input.2.tools", 1, "none",
			[]string{"message", "web_search_call", "additional_tools"}},
		{"lite extends caller additional_tools", `{"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"echo"}]},` + search + `,` + trigger + `],"tool_choice":"auto"}`, true, true, "input.0.tools", 2, "auto",
			[]string{"additional_tools", "web_search_call", "compaction_trigger"}},
		{"lite fills empty additional_tools", `{"input":[{"type":"additional_tools","role":"developer"},` + search + `]}`, true, true, "input.0.tools", 1, "none",
			[]string{"additional_tools", "web_search_call"}},
		{"lite keeps top-level tools untouched", `{"input":` + history + `,"tools":[{"type":"function","name":"echo"}]}`, true, true, "input.2.tools", 1, "",
			[]string{"message", "web_search_call", "additional_tools"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changed, err := ensureOpenAIOAuthWebSearchToolForHistoryBody([]byte(tt.body), tt.lite)
			require.NoError(t, err)
			require.Equal(t, tt.changed, changed)
			var req map[string]any
			require.NoError(t, json.Unmarshal([]byte(tt.body), &req))
			require.Equal(t, tt.changed, ensureOpenAIOAuthWebSearchToolForHistory(req, tt.lite))
			if !tt.changed {
				require.JSONEq(t, tt.body, string(out))
				return
			}
			require.True(t, gjson.ValidBytes(out))
			tools := gjson.GetBytes(out, tt.toolsPath).Array()
			require.Len(t, tools, tt.toolCount)
			require.Equal(t, "web_search", tools[len(tools)-1].Get("type").String())
			require.Equal(t, tt.toolChoice, gjson.GetBytes(out, "tool_choice").String())
			if tt.lite {
				require.False(t, gjsonToolsContainWebSearch(gjson.GetBytes(out, "tools")), "Lite rejects top-level hosted tools")
				var types []string
				for _, item := range gjson.GetBytes(out, "input").Array() {
					types = append(types, item.Get("type").String())
				}
				require.Equal(t, tt.itemTypes, types)
			}

			// The map variant used by the Codex transform must agree.
			mapOut, err := json.Marshal(req)
			require.NoError(t, err)
			require.JSONEq(t, string(out), string(mapOut))
		})
	}
}

func TestEnsureOpenAIOAuthWebSearchToolForHistoryDoesNotShareToolMap(t *testing.T) {
	first := map[string]any{"input": []any{map[string]any{"type": "web_search_call"}}}
	second := map[string]any{"input": []any{map[string]any{"type": "web_search_call"}}}
	require.True(t, ensureOpenAIOAuthWebSearchToolForHistory(first, false))
	require.True(t, ensureOpenAIOAuthWebSearchToolForHistory(second, false))
	firstTools, ok := first["tools"].([]any)
	require.True(t, ok)
	firstTool, ok := firstTools[0].(map[string]any)
	require.True(t, ok)
	firstTool["external_web_access"] = true
	secondTools, ok := second["tools"].([]any)
	require.True(t, ok)
	secondTool, ok := secondTools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, secondTool["external_web_access"])
	require.Equal(t, false, openAIWebSearchHistoryTool["external_web_access"])
}

func TestOAuthWebSearchHistoryMalformedDeclarationsStayInvalid(t *testing.T) {
	for index, body := range []string{
		`{"input":[{"type":"web_search_call"}],"tools":{}}`,
		`{"input":[{"type":"web_search_call"}],"tools":"bad"}`,
		`{"input":[{"type":"web_search_call"}],"tools":4}`,
		`{"input":[{"type":"web_search_call"},{"type":"additional_tools","tools":{}}]}`,
		`{"input":[{"type":"web_search_call"},{"type":"additional_tools","tools":"bad"}]}`,
	} {
		for _, lite := range []bool{false, true} {
			t.Run(fmt.Sprintf("declaration_%d/lite_%t", index, lite), func(t *testing.T) {
				out, changed, err := ensureOpenAIOAuthWebSearchToolForHistoryBody([]byte(body), lite)
				require.NoError(t, err)
				require.False(t, changed)
				require.Equal(t, body, string(out))
				var mapped map[string]any
				require.NoError(t, json.Unmarshal([]byte(body), &mapped))
				require.False(t, ensureOpenAIOAuthWebSearchToolForHistory(mapped, lite))
				encoded, err := json.Marshal(mapped)
				require.NoError(t, err)
				require.JSONEq(t, body, string(encoded))
			})
		}
	}
}

func TestOAuthWebSearchHistoryRawPrecisionChoiceAndIdempotency(t *testing.T) {
	for _, lite := range []bool{false, true} {
		t.Run(fmt.Sprintf("lite_%t", lite), func(t *testing.T) {
			body := []byte(`{"input":[{"type":"web_search_call","id":"ws_1","unknown":9007199254740993,"action":{"query":"<tag>&value"}},{"type":"compaction_trigger"}],"unknown":{"n":123456789012345678901234567890},"tools":[{"type":"function","name":"echo","unknown":9007199254740993}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"echo"}]}}`)
			out, changed, err := ensureOpenAIOAuthWebSearchToolForHistoryBody(body, lite)
			require.NoError(t, err)
			require.True(t, changed)
			for _, path := range []string{"input.0", "unknown", "tool_choice", "tools.0"} {
				require.Equal(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(out, path).Raw)
			}
			items := gjson.GetBytes(out, "input").Array()
			require.Equal(t, "compaction_trigger", items[len(items)-1].Get("type").String())
			again, changed, err := ensureOpenAIOAuthWebSearchToolForHistoryBody(out, lite)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, out, again)
			var mapped map[string]any
			decoder := json.NewDecoder(strings.NewReader(string(body)))
			decoder.UseNumber()
			require.NoError(t, decoder.Decode(&mapped))
			require.True(t, ensureOpenAIOAuthWebSearchToolForHistory(mapped, lite))
			encoded, err := marshalOpenAIUpstreamJSON(mapped)
			require.NoError(t, err)
			require.JSONEq(t, string(out), string(encoded))
		})
	}
}

func TestOAuthWebSearchHistoryLiteNamespaceCarrierPrecedesTrigger(t *testing.T) {
	body := []byte(`{"input":[{"type":"web_search_call"},{"type":"compaction_trigger"}],"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}],"tool_choice":{"type":"namespace","name":"collaboration"}}`)
	out, changed, err := normalizeOpenAIOAuthWebSearchHistoryForAccount(body, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, true, false)
	require.NoError(t, err)
	require.True(t, changed)
	out, changed, err = normalizeOpenAIResponsesLiteToolsPayload(out)
	require.NoError(t, err)
	require.True(t, changed)
	items := gjson.GetBytes(out, "input").Array()
	require.Len(t, items, 3)
	require.Equal(t, "web_search_call", items[0].Get("type").String())
	require.Equal(t, "additional_tools", items[1].Get("type").String())
	require.Equal(t, "compaction_trigger", items[2].Get("type").String())
	require.Equal(t, "web_search", items[1].Get("tools.0.type").String())
	require.False(t, items[1].Get("tools.0.external_web_access").Bool())
	require.Equal(t, "collaboration", items[1].Get("tools.1.name").String())
	require.Equal(t, gjson.GetBytes(body, "tool_choice").Raw, gjson.GetBytes(out, "tool_choice").Raw)
}

func TestOAuthWebSearchHistorySession_InheritedAndExplicitBoundaries(t *testing.T) {
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	tools := `[{"type":"function","name":"echo","unknown":9007199254740993}]`
	choice := `{"type":"function","name":"echo"}`
	for _, lite := range []bool{false, true} {
		for _, tc := range []struct {
			name, body, sessionTools, sessionChoice, wantChoice string
			changed, inherited                                  bool
		}{
			{"inherit_tools_choice", `{"input":[{"type":"web_search_call"}]}`, tools, choice, choice, true, true},
			{"explicit_tools_choice_override", `{"input":[{"type":"web_search_call"}],"tools":[],"tool_choice":"auto"}`, tools, choice, `"none"`, true, false},
			{"explicit_choice_retained", `{"input":[{"type":"web_search_call"}],"tool_choice":"none"}`, tools, choice, `"none"`, true, true},
			{"session_search_already_declared", `{"input":[{"type":"web_search_call"}]}`, `[{"type":"web_search","external_web_access":true}]`, choice, "", false, false},
			{"no_history_unchanged", `{"input":[{"type":"message","role":"user","content":"hi"}]}`, tools, choice, "", false, false},
			{"malformed_session_tools_unchanged", `{"input":[{"type":"web_search_call"}]}`, `{"bad":true}`, choice, "", false, false},
		} {
			t.Run(fmt.Sprintf("%s/lite_%t", tc.name, lite), func(t *testing.T) {
				out, changed, err := normalizeOpenAIOAuthWebSearchHistoryForSession([]byte(tc.body), oauth, lite, tc.sessionTools, tc.sessionChoice)
				require.NoError(t, err)
				require.Equal(t, tc.changed, changed)
				if !changed {
					require.Equal(t, tc.body, string(out))
					return
				}
				require.Equal(t, tc.wantChoice, gjson.GetBytes(out, "tool_choice").Raw)
				if tc.inherited {
					require.Equal(t, "echo", gjson.GetBytes(out, "tools.0.name").String())
					require.Equal(t, "9007199254740993", gjson.GetBytes(out, "tools.0.unknown").Raw)
				} else {
					require.NotContains(t, string(out), "echo")
				}
			})
		}
	}
	body := []byte(`{"input":[{"type":"web_search_call"}]}`)
	out, changed, err := normalizeOpenAIOAuthWebSearchHistoryForSession(body, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, false, tools, choice)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, out)
}
