//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// gateway.responses_force_encrypted_reasoning：Forward 最终发往上游的 body 给推理模型补
// include "reasoning.encrypted_content"。

func runForceEncForward(t *testing.T, enabled bool, account *Account, body string) []byte {
	t.Helper()
	upstream := newForcedEffortSSEUpstream(gjson.Get(body, "model").String())
	svc := newOpenAIImageGenerationControlTestService(upstream)
	svc.cfg.Gateway.ResponsesForceEncryptedReasoning = enabled
	c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	result, err := svc.Forward(context.Background(), c, account, []byte(body))
	require.NoError(t, err)
	require.NotNil(t, result)
	return upstream.lastBody
}

func TestResponsesForceEncryptedReasoning_Forward(t *testing.T) {
	const noInclude = `{"model":"gpt-6-luna","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`

	t.Run("no_include_gets_created", func(t *testing.T) {
		got := runForceEncForward(t, true, forcedEffortAccount(nil), noInclude)
		require.JSONEq(t, `["reasoning.encrypted_content"]`, gjson.GetBytes(got, "include").Raw)
	})
	t.Run("existing_include_appended", func(t *testing.T) {
		body := `{"model":"gpt-6-luna","stream":true,"include":["web_search_call.results"],"input":"hi"}`
		got := runForceEncForward(t, true, forcedEffortAccount(nil), body)
		require.JSONEq(t, `["web_search_call.results","reasoning.encrypted_content"]`, gjson.GetBytes(got, "include").Raw)
	})
	t.Run("already_included_bytes_unchanged", func(t *testing.T) {
		body := `{"model":"gpt-6-luna","stream":true,"include":["reasoning.encrypted_content"],"input":"hi"}`
		on := runForceEncForward(t, true, forcedEffortAccount(nil), body)
		off := runForceEncForward(t, false, forcedEffortAccount(nil), body)
		require.Equal(t, string(off), string(on))
		require.JSONEq(t, `["reasoning.encrypted_content"]`, gjson.GetBytes(on, "include").Raw)
	})
	t.Run("non_reasoning_model_unchanged", func(t *testing.T) {
		body := `{"model":"gpt-4.1","stream":true,"input":"hi"}`
		on := runForceEncForward(t, true, forcedEffortAccount(nil), body)
		off := runForceEncForward(t, false, forcedEffortAccount(nil), body)
		require.Equal(t, string(off), string(on))
		require.False(t, gjson.GetBytes(on, "include").Exists())
	})
	t.Run("effort_none_without_force_unchanged", func(t *testing.T) {
		on := runForceEncForward(t, true, forcedEffortAccount(nil), forcedEffortResponsesBodyNone)
		off := runForceEncForward(t, false, forcedEffortAccount(nil), forcedEffortResponsesBodyNone)
		require.Equal(t, string(off), string(on))
		require.False(t, gjson.GetBytes(on, "include").Exists())
	})
	t.Run("effort_none_raised_by_force_gets_include", func(t *testing.T) {
		got := runForceEncForward(t, true, forcedEffortAccount("high"), forcedEffortResponsesBodyNone)
		require.Equal(t, "high", gjson.GetBytes(got, "reasoning.effort").String())
		require.JSONEq(t, `["reasoning.encrypted_content"]`, gjson.GetBytes(got, "include").Raw)
	})
	t.Run("switch_off_unchanged", func(t *testing.T) {
		got := runForceEncForward(t, false, forcedEffortAccount(nil), noInclude)
		require.False(t, gjson.GetBytes(got, "include").Exists())
	})
	t.Run("store_and_other_fields_untouched", func(t *testing.T) {
		body := `{"model":"gpt-6-luna","stream":true,"store":true,"metadata":{"a":"b"},"input":"hi"}`
		on := runForceEncForward(t, true, forcedEffortAccount(nil), body)
		off := runForceEncForward(t, false, forcedEffortAccount(nil), body)
		require.Equal(t, gjson.GetBytes(off, "store").Raw, gjson.GetBytes(on, "store").Raw)
		require.Equal(t, gjson.GetBytes(off, "metadata").Raw, gjson.GetBytes(on, "metadata").Raw)
	})
}

func TestEnsureResponsesEncryptedReasoningInclude_Branches(t *testing.T) {
	// include 为 null：新建。
	out, added, err := ensureResponsesEncryptedReasoningInclude([]byte(`{"include":null}`), "gpt-6-luna")
	require.NoError(t, err)
	require.True(t, added)
	require.JSONEq(t, `["reasoning.encrypted_content"]`, gjson.GetBytes(out, "include").Raw)
	// include 不是数组：不动。
	in := []byte(`{"include":"x"}`)
	out, added, err = ensureResponsesEncryptedReasoningInclude(in, "gpt-6-luna")
	require.NoError(t, err)
	require.False(t, added)
	require.Equal(t, string(in), string(out))
	// sjson 写入失败（reasoning 是数组也不影响 include；这里 body 不是对象）。
	_, added, err = ensureResponsesEncryptedReasoningInclude([]byte(`[1]`), "gpt-6-luna")
	require.Error(t, err)
	require.False(t, added)

	var nilSvc *OpenAIGatewayService
	require.False(t, nilSvc.responsesForceEncryptedReasoningEnabled())
}
