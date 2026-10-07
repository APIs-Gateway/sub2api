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
					upstream := &openAIWSCaptureConn{
						readDelays: []time.Duration{0, 2 * time.Second},
						events: [][]byte{
							[]byte(`{"type":"response.completed","response":{"id":"resp_model_field_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
							[]byte(`{"type":"response.completed","response":{"id":"resp_model_field_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
						},
					}
					svc, dialer := newPassthroughBeforeTurnTestService(upstream)
					account := newPassthroughBeforeTurnTestAccount()
					account.Extra["openai_oauth_responses_websockets_v2_mode"] = mode
					if mode == OpenAIWSIngressModeCtxPool {
						pool := newOpenAIWSConnPool(svc.cfg)
						pool.setClientDialerForTest(dialer)
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
					writePassthroughBeforeTurnTestFrame(t, client, `{"type":"response.create","model":"gpt-5.1","input":[]}`)
					require.Equal(t, "resp_model_field_1", gjson.GetBytes(readPassthroughBeforeTurnTestFrame(t, client), "response.id").String())
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					require.NoError(t, client.Write(ctx, messageType, []byte(tc.payload)))
					if tc.invalid {
						select {
						case err := <-serverErrors:
							var rejection *OpenAIWSLocalRejection
							require.ErrorAs(t, err, &rejection)
							require.Equal(t, 400, rejection.HTTPStatus)
							require.Contains(t, rejection.Message, "canonical field name")
						case <-ctx.Done():
							t.Fatal("ambiguous later frame was not rejected promptly")
						}
						upstream.mu.Lock()
						writes := len(upstream.writes)
						upstream.mu.Unlock()
						require.Equal(t, 1, writes, "ambiguous frame must not be written to the provider")
						return
					}
					_, event, err := client.Read(ctx)
					require.NoError(t, err)
					require.Equal(t, "resp_model_field_2", gjson.GetBytes(event, "response.id").String())
					for range 2 {
						select {
						case result := <-results:
							require.Equal(t, "gpt-5.1", result.Model, "later absent model must retain inherited usage model")
						case <-ctx.Done():
							t.Fatal("inherited model turn did not record a forwarding result")
						}
					}
					upstream.mu.Lock()
						writes := len(upstream.writes)
						upstream.mu.Unlock()
						require.Equal(t, 2, writes)
				})
			}
		}
	}
}
