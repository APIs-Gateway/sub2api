package service

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type imagePermissionStagedConn struct{ *stagedPassthroughConn }

func (c *imagePermissionStagedConn) WriteJSON(ctx context.Context, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, payload)
}

func TestWSLaterImageFrameRejectsRevokedPermissionBeforeAdmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		for _, frame := range []string{`{"type":"response.create","model":"gpt-5.1","tools":[{"type":"image_generation"}]}`, `{"type":"session.update","session":{"tools":[{"type":"image_generation"}]}}`} {
			if mode == OpenAIWSIngressModeCtxPool && gjson.Get(frame, "type").String() == "session.update" {
				continue
			}
			t.Run(mode+gjson.Get(frame, "type").String(), func(t *testing.T) {
				ctx, cancel := context.WithCancelCause(context.Background())
				defer cancel(context.Canceled)
				upstream := newStagedPassthroughConn()
				cfg := passthroughLifecycleConfig()
				cfg.RunMode = config.RunModeSimple
				svc := newPassthroughLifecycleService(cfg, upstream)
				account := passthroughLifecycleAccount()
				if mode == OpenAIWSIngressModeCtxPool {
					account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
					cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
					cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
					cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
					pool := newOpenAIWSConnPool(cfg)
					pool.setClientDialerForTest(&stagedPassthroughDialer{conn: &imagePermissionStagedConn{upstream}})
					svc.openaiWSPool = pool
				}
				var admissionCalls, permissionCalls atomic.Int32
				hooks := &OpenAIWSIngressHooks{
					BeforeImagePermission: func() (*Group, error) {
						permissionCalls.Add(1)
						return &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, SimpleModeAutoImageEligible: false}, nil
					},
					BeforeRequest: func(int, []byte, string) error { admissionCalls.Add(1); return nil },
				}
				server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, func(*gin.Context) *OpenAIWSIngressHooks { return hooks })
				defer server.Close()
				client := dialPassthroughLifecycleClient(t, server)
				defer func() { _ = client.CloseNow() }()
				requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
				upstream.Send(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
				_, err := readPassthroughLifecycleFrame(t, client, time.Second)
				require.NoError(t, err)
				previousAdmissionCalls := admissionCalls.Load()
				writePassthroughBeforeTurnTestFrame(t, client, frame)
				// Native closes on parse rejection; passthrough first sends an error event.
				event, readErr := readPassthroughLifecycleFrame(t, client, time.Second)
				if mode == OpenAIWSIngressModePassthrough {
					require.NoError(t, readErr)
					require.Equal(t, ImageGenerationPermissionMessage(), gjson.GetBytes(event, "error.message").String())
				}
				_ = client.CloseNow()
				cancel(context.Canceled)
				select {
				case err := <-serverErr:
					require.Error(t, err)
					if mode == OpenAIWSIngressModeCtxPool {
						var closeErr *OpenAIWSClientCloseError
						require.ErrorAs(t, err, &closeErr)
						require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
					}
				case <-time.After(3 * time.Second):
					t.Fatal("image permission rejection did not terminate WS")
				}
				require.Equal(t, int32(1), permissionCalls.Load())
				require.Equal(t, previousAdmissionCalls, admissionCalls.Load())
				select {
				case payload := <-upstream.writes:
					t.Fatalf("revoked image request forwarded: %s", payload)
				default:
				}
			})
		}
	}
}

func TestPassthroughSessionImageToolsRecheckPermissionAfterAdminDisable(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	upstream := newStagedPassthroughConn()
	cfg := passthroughLifecycleConfig()
	cfg.RunMode = config.RunModeSimple
	svc := newPassthroughLifecycleService(cfg, upstream)
	var allowed atomic.Bool
	allowed.Store(true)
	var checks atomic.Int32
	server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, svc, passthroughLifecycleAccount(), func(*gin.Context) *OpenAIWSIngressHooks {
		return &OpenAIWSIngressHooks{BeforeImagePermission: func() (*Group, error) {
			checks.Add(1)
			return &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, SimpleModeAutoImageEligible: allowed.Load()}, nil
		}}
	})
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = client.CloseNow() }()
	requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	_, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	writePassthroughBeforeTurnTestFrame(t, client, `{"type":"session.update","session":{"tools":[{"type":"image_generation"}]}}`)
	requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
	allowed.Store(false)
	writePassthroughBeforeTurnTestFrame(t, client, `{"type":"response.create","model":"gpt-5.1"}`)
	event, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	require.Equal(t, ImageGenerationPermissionMessage(), gjson.GetBytes(event, "error.message").String())
	require.Equal(t, int32(2), checks.Load())
	_ = client.CloseNow()
	cancel(context.Canceled)
	select {
	case err := <-serverErr:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("session permission rejection did not terminate")
	}
	select {
	case payload := <-upstream.writes:
		t.Fatalf("inherited revoked image tools forwarded: %s", payload)
	default:
	}
}

func TestPassthroughImageSessionModelWithEmptyToolsDoesNotPoisonLaterText(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	upstream := newStagedPassthroughConn()
	cfg := passthroughLifecycleConfig()
	cfg.RunMode = config.RunModeSimple
	svc := newPassthroughLifecycleService(cfg, upstream)
	var allowed atomic.Bool
	allowed.Store(true)
	var checks atomic.Int32
	server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, svc, passthroughLifecycleAccount(), func(*gin.Context) *OpenAIWSIngressHooks {
		return &OpenAIWSIngressHooks{BeforeImagePermission: func() (*Group, error) {
			checks.Add(1)
			return &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, SimpleModeAutoImageEligible: allowed.Load()}, nil
		}}
	})
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = client.CloseNow() }()
	requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	_, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	writePassthroughBeforeTurnTestFrame(t, client, `{"type":"session.update","session":{"model":"gpt-image-1","tools":[]}}`)
	requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
	writePassthroughBeforeTurnTestFrame(t, client, `{"type":"session.update","session":{"model":"gpt-5.1"}}`)
	requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
	allowed.Store(false)
	writePassthroughBeforeTurnTestFrame(t, client, `{"type":"response.create","model":"gpt-5.1"}`)
	text := requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
	require.Equal(t, "response.create", gjson.GetBytes(text, "type").String())
	require.Equal(t, int32(1), checks.Load())
	_ = client.CloseNow()
	cancel(context.Canceled)
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("text session did not terminate after explicit close")
	}
}

func TestPassthroughSessionImageChoiceRechecksAfterAdminDisable(t *testing.T) {
	for _, tt := range []struct {
		name, payload string
		blocked       bool
	}{
		{"inherited choice", `{"type":"response.create","model":"gpt-5.1"}`, true},
		{"tools overridden choice inherited", `{"type":"response.create","model":"gpt-5.1","tools":[{"type":"namespace","name":"image_gen"}]}`, true},
		{"choice overridden with none", `{"type":"response.create","model":"gpt-5.1","tool_choice":"none"}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) { exercisePassthroughSessionImageChoiceRevocation(t, tt.payload, tt.blocked) })
	}
}

func exercisePassthroughSessionImageChoiceRevocation(t *testing.T, payload string, blocked bool) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	upstream := newStagedPassthroughConn()
	cfg := passthroughLifecycleConfig()
	cfg.RunMode = config.RunModeSimple
	svc := newPassthroughLifecycleService(cfg, upstream)
	var allowed atomic.Bool
	allowed.Store(true)
	var checks, admissions atomic.Int32
	server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, svc, passthroughLifecycleAccount(), func(*gin.Context) *OpenAIWSIngressHooks {
		return &OpenAIWSIngressHooks{
			BeforeImagePermission: func() (*Group, error) {
				checks.Add(1)
				return &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, SimpleModeAutoImageEligible: allowed.Load()}, nil
			},
			BeforeRequest: func(int, []byte, string) error { admissions.Add(1); return nil },
		}
	})
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = client.CloseNow() }()
	requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	_, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	writePassthroughBeforeTurnTestFrame(t, client, `{"type":"session.update","session":{"tools":[{"type":"namespace","name":"image_gen"}]}}`)
	requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
	require.Zero(t, checks.Load(), "passive namespace must retain its existing permission behavior")
	writePassthroughBeforeTurnTestFrame(t, client, `{"type":"session.update","session":{"tool_choice":{"type":"namespace","name":"image_gen"}}}`)
	requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
	require.Equal(t, int32(1), checks.Load())
	allowed.Store(false)
	oldAdmissions := admissions.Load()
	writePassthroughBeforeTurnTestFrame(t, client, payload)
	if blocked {
		event, err := readPassthroughLifecycleFrame(t, client, time.Second)
		require.NoError(t, err)
		require.Equal(t, ImageGenerationPermissionMessage(), gjson.GetBytes(event, "error.message").String())
		require.Equal(t, int32(2), checks.Load())
		require.Equal(t, oldAdmissions, admissions.Load())
	} else {
		forwarded := requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
		require.Equal(t, "none", gjson.GetBytes(forwarded, "tool_choice").String())
		require.Equal(t, int32(1), checks.Load())
		require.Equal(t, oldAdmissions+1, admissions.Load())
	}
	_ = client.CloseNow()
	cancel(context.Canceled)
	select {
	case <-serverErr:
	case <-time.After(3 * time.Second):
		t.Fatal("session tool choice rejection did not terminate")
	}
	select {
	case payload := <-upstream.writes:
		t.Fatalf("inherited revoked image choice forwarded: %s", payload)
	default:
	}
}

func TestPassthroughImagePermissionRejectsInitialAndMappedLaterFrames(t *testing.T) {
	for _, initial := range []bool{true, false} {
		t.Run(map[bool]string{true: "initial permission", false: "mapped model global gate"}[initial], func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			upstream := newStagedPassthroughConn()
			cfg := passthroughLifecycleConfig()
			cfg.RunMode = config.RunModeSimple
			account := passthroughLifecycleAccount()
			if !initial {
				cfg.Gateway.DisableOpenAIResponsesImageGeneration = true
				account.Credentials["model_mapping"] = map[string]any{"draw-alias": "gpt-image-1"}
			}
			svc := newPassthroughLifecycleService(cfg, upstream)
			var checks, admissions atomic.Int32
			hooks := &OpenAIWSIngressHooks{BeforeImagePermission: func() (*Group, error) {
				checks.Add(1)
				return &Group{ID: 1, Hydrated: true, Status: StatusActive, Platform: PlatformOpenAI}, nil
			}, BeforeRequest: func(int, []byte, string) error { admissions.Add(1); return nil }}
			server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, func(*gin.Context) *OpenAIWSIngressHooks { return hooks })
			defer server.Close()
			var client *coderws.Conn
			message := ImageGenerationPermissionMessage()
			if initial {
				client = dialPassthroughLifecycleClientWithPayload(t, server, `{"type":"response.create","model":"gpt-5.1","tools":[{"type":"image_generation"}]}`)
			} else {
				client = dialPassthroughLifecycleClient(t, server)
				requireStagedPassthroughUpstreamWrite(t, upstream, time.Second)
				upstream.Send(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
				_, err := readPassthroughLifecycleFrame(t, client, time.Second)
				require.NoError(t, err)
				writePassthroughBeforeTurnTestFrame(t, client, `{"type":"response.create","model":"draw-alias"}`)
				message = OpenAIResponsesImageGenerationDisabledMessage()
			}
			defer func() { _ = client.CloseNow() }()
			event, err := readPassthroughLifecycleFrame(t, client, time.Second)
			require.NoError(t, err)
			require.Equal(t, message, gjson.GetBytes(event, "error.message").String())
			if initial {
				require.Equal(t, int32(1), checks.Load())
			} else {
				require.Zero(t, checks.Load())
			}
			require.Zero(t, admissions.Load())
			_ = client.CloseNow()
			cancel(context.Canceled)
			select {
			case err := <-serverErr:
				require.Error(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("image gate rejection did not terminate")
			}
			select {
			case payload := <-upstream.writes:
				t.Fatalf("rejected image request forwarded: %s", payload)
			default:
			}
		})
	}
}
