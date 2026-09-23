package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildOpenAIWSCurrentTurnRetryPayloadWithoutAccumulatedInput(t *testing.T) {
	retryPayload, retrySafe, err := buildOpenAIWSCurrentTurnRetryPayload(
		[]byte(`{"type":"response.create","model":"gpt-5"}`), nil, false, "gpt-5",
	)

	require.NoError(t, err)
	require.False(t, retrySafe)
	require.Nil(t, retryPayload)
}

func TestBuildOpenAIWSCurrentTurnRetryPayloadRebuildsFullContext(t *testing.T) {
	payload := []byte(`{"type":"response.create","model":"mapped-model","previous_response_id":"resp_old","input":[{"role":"user","content":"second"}]}`)
	fullInput := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"first"}`),
		json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"inspect","arguments":"{}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"call_1","output":"done"}`),
	}

	t.Run("restores original model", func(t *testing.T) {
		retryPayload, retrySafe, err := buildOpenAIWSCurrentTurnRetryPayload(payload, fullInput, true, "  gpt-5.6-sol  ")
		require.NoError(t, err)
		require.True(t, retrySafe)
		require.False(t, gjson.GetBytes(retryPayload, "previous_response_id").Exists())
		require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(retryPayload, "model").String())
		require.Len(t, gjson.GetBytes(retryPayload, "input").Array(), 3)
	})

	t.Run("keeps payload model when original model is empty", func(t *testing.T) {
		retryPayload, retrySafe, err := buildOpenAIWSCurrentTurnRetryPayload(payload, fullInput, true, " ")
		require.NoError(t, err)
		require.True(t, retrySafe)
		require.Equal(t, "mapped-model", gjson.GetBytes(retryPayload, "model").String())
	})

}

func TestOpenAIWSCurrentTurnFailoverErrorWrapsCause(t *testing.T) {
	cause := &UpstreamFailoverError{StatusCode: 429}
	payload := []byte(`{"type":"response.create"}`)
	err := newOpenAIWSCurrentTurnFailoverError(cause, payload)
	payload[0] = 'x'

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Same(t, cause, failoverErr)
	require.Equal(t, cause.Error(), err.Error())

	wrapped := fmt.Errorf("proxy: %w", err)
	retryPayload, ok := OpenAIWSCurrentTurnRetryPayload(wrapped)
	require.True(t, ok)
	require.Equal(t, `{"type":"response.create"}`, string(retryPayload), "constructor must copy the payload")
	retryPayload[0] = 'y'
	again, _ := OpenAIWSCurrentTurnRetryPayload(err)
	require.Equal(t, byte('{'), again[0], "accessor must return an isolated copy")

	emptyErr := newOpenAIWSCurrentTurnFailoverError(cause, nil)
	emptyPayload, ok := OpenAIWSCurrentTurnRetryPayload(emptyErr)
	require.True(t, ok)
	require.Empty(t, emptyPayload)
}

func TestOpenAIWSCurrentTurnFailoverErrorNilSafety(t *testing.T) {
	var nilErr *openAIWSCurrentTurnFailoverError
	require.Equal(t, "openai websocket current-turn failover", nilErr.Error())
	require.Nil(t, nilErr.Unwrap())
	require.Equal(t, "openai websocket current-turn failover", (&openAIWSCurrentTurnFailoverError{}).Error())

	payload, ok := OpenAIWSCurrentTurnRetryPayload(errors.New("plain"))
	require.False(t, ok)
	require.Nil(t, payload)
	payload, ok = OpenAIWSCurrentTurnRetryPayload(nil)
	require.False(t, ok)
	require.Nil(t, payload)
}

func TestOpenAIWSToolCallReplayCollectorAllItems(t *testing.T) {
	collector := &openAIWSToolCallReplayCollector{}
	collector.AddEvent("response.output_item.done", []byte(`{"item":{"id":"msg_1","type":"message","role":"assistant"}}`))
	collector.AddEvent("response.output_item.done", []byte(`{"item":{"type":"function_call","call_id":"call_1","name":"inspect"}}`))
	collector.AddEvent("response.output_item.done", []byte(`{"item":{"type":"reasoning","summary":[]}}`))
	// Ignored shapes: missing item, scalar item, array item, object without type.
	collector.AddEvent("response.output_item.done", []byte(`{}`))
	collector.AddEvent("response.output_item.done", []byte(`{"item":"text"}`))
	collector.AddEvent("response.output_item.done", []byte(`{"item":[1]}`))
	collector.AddEvent("response.output_item.done", []byte(`{"item":{"id":"no_type"}}`))
	// Terminal event repeats items already seen (dedupe by id, call_id and raw).
	collector.AddEvent("response.completed", []byte(`{"response":{"output":[
		{"id":"msg_1","type":"message","role":"assistant"},
		{"type":"function_call","call_id":"call_1","name":"inspect"},
		{"type":"reasoning","summary":[]},
		{"id":"msg_2","type":"message","role":"assistant"}
	]}}`))
	collector.AddEvent("response.done", []byte(`{"response":{"output":"not-array"}}`))

	all := collector.AllItems()
	require.Len(t, all, 4)
	require.Equal(t, "msg_1", gjson.GetBytes(all[0], "id").String())
	require.Equal(t, "call_1", gjson.GetBytes(all[1], "call_id").String())
	require.Equal(t, "reasoning", gjson.GetBytes(all[2], "type").String())
	require.Equal(t, "msg_2", gjson.GetBytes(all[3], "id").String())

	// Bodies are shared immutable replay items; only the header slice is fresh.
	all[0] = nil
	require.Equal(t, "msg_1", gjson.GetBytes(collector.AllItems()[0], "id").String(), "AllItems must return a fresh header slice")
}
