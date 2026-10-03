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
				defer client.CloseNow()
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
	defer client.CloseNow()
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
