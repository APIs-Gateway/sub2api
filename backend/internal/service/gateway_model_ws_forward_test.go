//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGatewayModelField_WSLaterFramesBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{OpenAIWSIngressModePassthrough, OpenAIWSIngressModeCtxPool} {
		for _, messageType := range []coderws.MessageType{coderws.MessageText, coderws.MessageBinary} {
			for _, tc := range []struct {
				name, payload string
				invalid       bool
			}{
				{"duplicate", `{"type":"response.create","model":"gpt-5.1","model":"gpt-6-astra","input":[]}`, true},
				{"escaped", `{"type":"response.create","model":"gpt-5.1","\u006dodel":"gpt-6-astra","input":[]}`, true},
				{"case_alias", `{"type":"response.create","model":"gpt-5.1","Model":"gpt-6-astra","input":[]}`, true},
				{"inherit", `{"type":"response.create","input":[]}`, false},
			} {
				t.Run(fmt.Sprintf("%s/%d/%s", mode, messageType, tc.name), func(t *testing.T) {
					// Keep the upstream alive between turns. An exhausted capture
					// array emits EOF, which breaks a pooled connection before its
					// first response can reach the turn owner.
					upstream := newStagedPassthroughConn()
					cfg := passthroughLifecycleConfig()
					cfg.Gateway.OpenAIWS.OAuthEnabled = true
					svc := newPassthroughLifecycleService(cfg, upstream)
					account := newPassthroughBeforeTurnTestAccount()
					account.Extra["openai_oauth_responses_websockets_v2_mode"] = mode
					if mode == OpenAIWSIngressModeCtxPool {
						pool := newOpenAIWSConnPool(cfg)
						pool.setClientDialerForTest(&stagedPassthroughDialer{conn: upstream})
						svc.openaiWSPool = pool
						defer pool.Close()
					}
					results := make(chan *OpenAIForwardResult, 4)
					hooks := &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, _ error) {
						if result != nil {
							results <- result
						}
					}}
					server, serverErrors := startPassthroughBeforeTurnTestServer(t, svc, account, hooks)
					defer server.Close()
					client := dialPassthroughBeforeTurnTestClient(t, server)
					defer func() { _ = client.CloseNow() }()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					readWrite := func() []byte {
						select {
						case body := <-upstream.writes:
							return body
						case <-ctx.Done():
							t.Fatal("upstream did not receive the valid control request")
							return nil
						}
					}
					readResult := func() {
						select {
						case result := <-results:
							require.Equal(t, "gpt-5.1", result.Model, "usage model must retain first/later inheritance")
						case <-ctx.Done():
							t.Fatal("valid turn did not produce a forwarding result")
						}
					}
					complete := func(id string) {
						upstream.Send(`{"type":"response.completed","response":{"id":"` + id + `","model":"gpt-5.1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"model field control"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`)
					}
					writePassthroughBeforeTurnTestFrame(t, client, `{"type":"response.create","model":"gpt-5.1","input":[]}`)
					require.Equal(t, "gpt-5.1", gjson.GetBytes(readWrite(), "model").String())
					complete("resp_model_field_1")
					require.Equal(t, "resp_model_field_1", gjson.GetBytes(readPassthroughBeforeTurnTestFrame(t, client), "response.id").String())
					readResult()
					require.NoError(t, client.Write(ctx, messageType, []byte(tc.payload)))
					if tc.invalid {
						// The proxy writes its local error event synchronously; keep
						// reading while awaiting termination so error delivery itself
						// cannot deadlock the test harness.
						readDone := make(chan struct{})
						go func() {
							_, _, _ = client.Read(ctx)
							close(readDone)
						}()
						select {
						case err := <-serverErrors:
							var rejection *OpenAIWSLocalRejection
							require.ErrorAs(t, err, &rejection)
							require.Equal(t, 400, rejection.HTTPStatus)
							require.Contains(t, rejection.Message, "canonical field name")
						case <-ctx.Done():
							t.Fatal("ambiguous later frame was not rejected promptly")
						}
						select {
						case <-readDone:
						case <-ctx.Done():
							t.Fatal("local rejection read did not finish")
						}
						require.Empty(t, upstream.writes, "ambiguous frame must not reach the provider")
						return
					}
					secondWrite := readWrite()
					if mode == OpenAIWSIngressModeCtxPool {
						require.Equal(t, "gpt-5.1", gjson.GetBytes(secondWrite, "model").String())
					} else {
						require.False(t, gjson.GetBytes(secondWrite, "model").Exists(), "passthrough retains provider session inheritance")
					}
					complete("resp_model_field_2")
					_, event, err := client.Read(ctx)
					require.NoError(t, err)
					require.Equal(t, "resp_model_field_2", gjson.GetBytes(event, "response.id").String())
					readResult()
				})
			}
		}
	}
}
