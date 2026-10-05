//go:build unit

package service

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestWSImageInputEstimate_LineageAndReset(t *testing.T) {
	s := &openAIWSImageInputEstimates{}
	image := []byte(`{"type":"response.create","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/image"}]}]}`)
	first := s.prepare(1, image)
	require.Positive(t, first)
	s.complete(1, "resp_image", 2)
	inherited := s.prepare(1, []byte(`{"previous_response_id":"resp_image","input":"describe it"}`))
	require.Greater(t, inherited, first)
	s.complete(1, "resp_followup", 0)
	require.Positive(t, s.prepare(1, []byte(`{"previous_response_id":"resp_followup","input":"another question"}`)))
	for _, body := range []string{`{"input":"new chain"}`, `{"previous_response_id":null,"input":"new chain"}`, `{"input":[{"type":"message","content":[{"type":"input_text","text":"input_image is a word"}]}],"tools":[{"type":"function","name":"image","parameters":{"type":"input_image"}}]}`} {
		require.Zero(t, s.prepare(1, []byte(body)), body)
	}
	s.complete(1, "resp_text", 0)
	require.Zero(t, s.prepare(1, []byte(`{"previous_response_id":"resp_text","input":"plain continuation"}`)))
	require.Positive(t, s.prepare(1, []byte(`{"previous_response_id":"resp_unknown","input":"unresolved persisted state"}`)))
	require.Positive(t, s.prepare(1, []byte(`{"conversation":"conv_stored","input":"persisted state"}`)))
	for _, part := range []string{"input_file", "item_reference", "compaction"} {
		require.Positive(t, s.prepare(1, []byte(fmt.Sprintf(`{"input":[{"type":%q,"opaque":"not inspected"}]}`, part))))
	}
}

func TestWSImageInputEstimate_BoundedEvictionCannotBecomeFree(t *testing.T) {
	s := &openAIWSImageInputEstimates{}
	for n := 0; n < openAIWSImageInputEstimateLimit+1; n++ {
		s.prepare(1, []byte(`{"input":"text"}`))
		s.complete(1, fmt.Sprintf("resp_%d", n), 0)
	}
	require.Len(t, s.byResponse, openAIWSImageInputEstimateLimit)
	require.Len(t, s.order, openAIWSImageInputEstimateLimit)
	require.Positive(t, s.prepare(1, []byte(`{"previous_response_id":"resp_0","input":"expired lineage"}`)))
	require.Zero(t, s.prepare(1, []byte(`{"previous_response_id":"resp_1","input":"known text"}`)))
}

func TestWSImageInputEstimate_OnlyOneAdmissionHook(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(fmt.Sprintf("metadata_%t", modern), func(t *testing.T) {
			legacyCalls, metadataCalls := 0, 0
			body := []byte(`{"input":[{"type":"input_image","image_url":"opaque"}]}`)
			hooks := &OpenAIWSIngressHooks{BeforeUpstreamTurn: func(turn int, wire []byte, model string) error {
				legacyCalls++
				require.Equal(t, 3, turn)
				require.Equal(t, body, wire)
				require.Equal(t, "frozen", model)
				return nil
			}}
			if modern {
				hooks.BeforePassthroughUpstreamTurn = func(turn int, wire []byte, model string, imageInput int) error {
					metadataCalls++
					require.Equal(t, 3, turn)
					require.Equal(t, body, wire)
					require.Equal(t, "frozen", model)
					require.Positive(t, imageInput)
					return nil
				}
			}
			require.NoError(t, beforeOpenAIPassthroughUpstreamTurn(hooks, &openAIWSImageInputEstimates{}, 3, body, "frozen"))
			require.Equal(t, 1, legacyCalls+metadataCalls)
			require.Equal(t, modern, metadataCalls == 1)
		})
	}
	require.NoError(t, beforeOpenAIPassthroughUpstreamTurn(nil, &openAIWSImageInputEstimates{}, 1, nil, ""))
}

func TestWSImageInputEstimate_QueuedCompletionKeepsItsOwnContext(t *testing.T) {
	s := &openAIWSImageInputEstimates{}
	image := s.prepare(1, []byte(`{"input":[{"type":"input_image","image_url":"opaque"}]}`))
	require.Positive(t, image)
	require.Zero(t, s.prepare(2, []byte(`{"input":"independent text"}`)))
	s.complete(1, "resp_image", 0)
	s.complete(2, "resp_text", 0)
	require.Greater(t, s.prepare(3, []byte(`{"previous_response_id":"resp_image","input":"continue image"}`)), image)
	require.Zero(t, s.prepare(4, []byte(`{"previous_response_id":"resp_text","input":"continue text"}`)))
	s.complete(999, "resp_missing", 0)
	require.Positive(t, s.prepare(5, []byte(`{"previous_response_id":"resp_missing","input":"unknown"}`)))
	for n := 0; n < openAIWSImageInputEstimateLimit+1; n++ {
		s.prepare(100+n, []byte(`{"input":"plain"}`))
	}
	require.Len(t, s.pending, openAIWSImageInputEstimateLimit)
	s.complete(100+openAIWSImageInputEstimateLimit, "resp_overflow", 0)
	require.Positive(t, s.prepare(500, []byte(`{"previous_response_id":"resp_overflow","input":"unknown"}`)))
}

func TestWSImageInputEstimate_DeepUnknownInputIsBounded(t *testing.T) {
	require.True(t, openAIWSInputMayContainImage(gjson.Parse(`[{"content":[{"content":[{"content":{"content":{"type":"input_image"}}}]}]}]`)))
	require.False(t, openAIWSInputMayContainImage(gjson.Parse(`"ordinary text"`)))
}
