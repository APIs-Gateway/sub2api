//go:build unit

package service

import (
	"context"
	"fmt"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
				hooks.BeforePassthroughUpstreamTurn = func(turn int, wire []byte, model string, imageInput int, _ bool) error {
					metadataCalls++
					require.Equal(t, 3, turn)
					require.Equal(t, body, wire)
					require.Equal(t, "frozen", model)
					require.Positive(t, imageInput)
					return nil
				}
			}
			require.NoError(t, beforeOpenAIPassthroughUpstreamTurn(hooks, &openAIWSImageInputEstimates{}, 3, body, "frozen", "frozen"))
			require.Equal(t, 1, legacyCalls+metadataCalls)
			require.Equal(t, modern, metadataCalls == 1)
		})
	}
	require.NoError(t, beforeOpenAIPassthroughUpstreamTurn(nil, &openAIWSImageInputEstimates{}, 1, nil, "", ""))
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

func TestWSImageInputEstimate_ActualAdapterQueuedFramesUseDistinctTurns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := newStagedPassthroughConn()
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 15
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 5
	type admission struct {
		turn, image int
		body        []byte
	}
	admissions := make(chan admission, 4)
	type completion struct{ turn, image int }
	completed := make(chan completion, 4)
	gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var releases [2]sync.Once
	defer func() {
		for n := range gates {
			releases[n].Do(func() { close(gates[n]) })
		}
	}()
	var legacy atomic.Int32
	server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, newPassthroughLifecycleService(cfg, upstream), passthroughLifecycleAccount(), func(*gin.Context) *OpenAIWSIngressHooks {
		return &OpenAIWSIngressHooks{
			BeforeUpstreamTurn: func(int, []byte, string) error { legacy.Add(1); return nil },
			BeforePassthroughUpstreamTurn: func(turn int, body []byte, _ string, image int, _ bool) error {
				admissions <- admission{turn, image, append([]byte(nil), body...)}
				return nil
			},
			AfterTurn: func(turn int, result *OpenAIForwardResult, _ error) {
				completed <- completion{turn, result.Usage.ImageInputTokens}
				if turn <= 2 {
					select {
					case <-gates[turn-1]:
					case <-ctx.Done():
					}
				}
			},
		}
	})
	defer server.Close()
	client := dialPassthroughLifecycleClientWithPayload(t, server, `{"type":"response.create","model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_image","image_url":"opaque"}]}]}`)
	defer func() { _ = client.CloseNow(); cancel() }()
	readAdmission := func() admission {
		select {
		case a := <-admissions:
			return a
		case <-time.After(3 * time.Second):
			t.Fatal("actual v2 admission hook not reached")
			return admission{}
		}
	}
	first := readAdmission()
	require.Equal(t, 1, first.turn)
	require.Positive(t, first.image)
	select {
	case <-upstream.writes:
	case <-time.After(3 * time.Second):
		t.Fatal("initial provider write missing")
	}
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_estimated_image","usage":{"input_tokens":3,"output_tokens":1}}}`)
	select {
	case done := <-completed:
		require.Equal(t, 1, done.turn)
	case <-time.After(3 * time.Second):
		t.Fatal("first completion not reached")
	}
	write := func(body string) {
		writeCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		require.NoError(t, client.Write(writeCtx, coderws.MessageText, []byte(body)))
	}
	// Queue a new text chain while AfterTurn blocks before the first terminal is
	// written. The adapter must not reuse first-turn metadata or numbering.
	write(`{"type":"response.create","model":"gpt-5.4","input":"new text chain"}`)
	releases[0].Do(func() { close(gates[0]) })
	_, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
	require.NoError(t, err)
	second := readAdmission()
	require.Equal(t, 2, second.turn)
	require.Zero(t, second.image)
	select {
	case <-upstream.writes:
	case <-time.After(3 * time.Second):
		t.Fatal("second provider write missing")
	}
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_estimated_text","usage":{"input_tokens":3,"output_tokens":1}}}`)
	select {
	case done := <-completed:
		require.Equal(t, 2, done.turn)
	case <-time.After(3 * time.Second):
		t.Fatal("second completion not reached")
	}
	releases[1].Do(func() { close(gates[1]) })
	_, err = readPassthroughLifecycleFrame(t, client, 3*time.Second)
	require.NoError(t, err)
	write(`{"type":"response.create","model":"gpt-5.5","previous_response_id":"resp_estimated_image","input":"continue image"}`)
	third := readAdmission()
	require.Equal(t, 3, third.turn)
	require.Greater(t, third.image, first.image, "zero actual image usage does not erase input lineage")
	select {
	case wire := <-upstream.writes:
		require.Contains(t, string(wire), `"previous_response_id":"resp_estimated_image"`)
		require.Contains(t, string(wire), `"model":"gpt-5.5"`)
	case <-time.After(3 * time.Second):
		t.Fatal("third provider write missing")
	}
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_estimated_final","usage":{"input_tokens":4,"output_tokens":1,"input_tokens_details":{"image_tokens":3}}}}`)
	_, err = readPassthroughLifecycleFrame(t, client, 3*time.Second)
	require.NoError(t, err)
	select {
	case done := <-completed:
		require.Equal(t, 3, done.turn)
		require.Equal(t, 3, done.image)
	case <-time.After(3 * time.Second):
		t.Fatal("third completion missing")
	}
	require.Zero(t, legacy.Load())
	require.Empty(t, admissions)
	_ = client.CloseNow()
	cancel()
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("adapter did not terminate")
	}
}

func TestWSImageInputEstimate_OversizedIdentityIsNotRetainedOrFree(t *testing.T) {
	s := &openAIWSImageInputEstimates{}
	huge := "resp_" + strings.Repeat("x", openAIWSImageInputEstimateIDBytes)
	s.prepare(1, []byte(`{"input":[{"type":"input_image","image_url":"opaque"}]}`))
	s.complete(1, huge, 4)
	require.Empty(t, s.byResponse)
	require.Empty(t, s.order)
	require.Empty(t, s.pending)
	require.Positive(t, s.prepare(2, []byte(fmt.Sprintf(`{"previous_response_id":%q,"input":"continue"}`, huge))))
	s.complete(2, "", 0)
	require.Empty(t, s.pending)
	require.Zero(t, s.prepare(3, []byte(`{"input":"text"}`)))
	s.complete(3, "resp_observed", 100)
	require.Greater(t, s.prepare(4, []byte(`{"previous_response_id":"resp_observed","input":"continue"}`)), 100, "positive observed image usage preserves hidden image context")
	s.complete(4, "resp_observed", 0)
	require.Positive(t, s.byResponse["resp_observed"])
}

func TestWSImageInputEstimate_CurrentWireModelDeterminesGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, body, session string
		want                bool
	}{
		{"new_text", `{"model":"gpt-5.4","input":"text"}`, "gpt-image-1", false},
		{"inherited_image_model", `{"input":"generate"}`, "gpt-image-1", true},
		{"explicit_image_model", `{"model":"gpt-image-1","input":"generate"}`, "gpt-5.4", true},
		{"current_tool", `{"model":"gpt-5.4","tools":[{"type":"image_generation"}]}`, "gpt-5.4", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			hook := &OpenAIWSIngressHooks{BeforePassthroughUpstreamTurn: func(_ int, body []byte, original string, _ int, potential bool) error {
				called = true
				require.Equal(t, "frozen-price-model", original)
				require.Equal(t, tc.want, potential)
				require.Equal(t, tc.body, string(body))
				return nil
			}}
			require.NoError(t, beforeOpenAIPassthroughUpstreamTurn(hook, &openAIWSImageInputEstimates{}, 1, []byte(tc.body), "frozen-price-model", tc.session))
			require.True(t, called)
		})
	}
}
