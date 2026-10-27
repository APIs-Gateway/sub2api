package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func bridgeHistoryResponse(id string, output []string) string {
	var lines []string
	for _, item := range output {
		lines = append(lines, `data: {"type":"response.output_item.done","item":`+item+`}`, "")
	}
	lines = append(lines, fmt.Sprintf(`data: {"type":"response.completed","response":{"id":%q,"model":"gpt-5.1","output":[%s],"usage":{"input_tokens":9,"output_tokens":2}}}`, id, strings.Join(output, ",")), "", "")
	return strings.Join(lines, "\n")
}

// Uses the existing public WS entry point so this exact fixture also runs on
// unchanged production. Provider events deliberately repeat completed items.
func TestOpenAIWSHTTPBridgeHistory_ActualThreeTurnWire(t *testing.T) {
	for _, withTool := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("tool_%t/stream_%t", withTool, stream), func(t *testing.T) {
				gin.SetMode(gin.TestMode)
				first := []string{
					`{"type":"reasoning","id":"rs_keep_1","summary":[],"encrypted_content":"  opaque-雪-\\cipher  ","vendor_counter":9007199254740993}`,
					`{"type":"reasoning","id":"rs_missing","summary":[]}`,
					`{"type":"reasoning","id":"rs_null","encrypted_content":null}`,
					`{"type":"reasoning","id":"rs_empty","encrypted_content":""}`,
					`{"type":"reasoning","id":"rs_blank","encrypted_content":" \t\n "}`,
					`{"type":"reasoning","id":"rs_number","encrypted_content":123}`,
					`{"type":"reasoning","id":"rs_object","encrypted_content":{"cipher":"not-a-string"}}`,
					`{"type":"message","id":"msg_1","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"first plan"}],"opaque":{"large":9007199254740993,"text":"preserve"}}`,
				}
				second := []string{
					`{"type":"reasoning","id":"rs_keep_2","encrypted_content":"opaque-second"}`,
					`{"type":"message","id":"msg_2","role":"assistant","content":[{"type":"output_text","text":"second plan"}]}`,
				}
				if withTool {
					first = append(first, `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec","arguments":"{\"large\":9007199254740993}"}`)
					second = append(second, `{"type":"function_call","id":"fc_2","call_id":"call_2","name":"exec","arguments":"{}"}`)
				}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{
					{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(bridgeHistoryResponse("resp_history_1", first)))},
					{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(bridgeHistoryResponse("resp_history_2", second)))},
					{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(bridgeHistoryResponse("resp_history_3", nil)))},
				}}
				cfg := &config.Config{}
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				cfg.Gateway.OpenAIWS.Enabled = true
				cfg.Gateway.OpenAIWS.APIKeyEnabled = true
				cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
				cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
				cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
				cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
				cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
				cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
				cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
				svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector()}
				account := &Account{ID: 9009, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-upstream"}, Extra: map[string]any{"responses_websockets_v2_enabled": true}, Concurrency: 1, Status: StatusActive, Schedulable: true}
				errCh := make(chan error, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := coderws.Accept(w, r, nil)
					if err != nil {
						errCh <- err
						return
					}
					defer func() { _ = conn.CloseNow() }()
					ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
					defer cancel()
					_, initial, err := conn.Read(ctx)
					if err != nil {
						errCh <- err
						return
					}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = r.Clone(ctx)
					errCh <- svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "sk-upstream", initial, nil)
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
				require.NoError(t, err)
				defer func() { _ = conn.CloseNow() }()
				for turn := 1; turn <= 3; turn++ {
					input := `"initial"`
					previous := ""
					if turn > 1 {
						previous = fmt.Sprintf(`,"previous_response_id":"resp_history_%d"`, turn-1)
						input = fmt.Sprintf(`[{"role":"user","content":"followup %d"}]`, turn)
						if withTool {
							input = fmt.Sprintf(`[{"type":"function_call_output","call_id":"call_%d","output":"done %d"}]`, turn-1, turn)
						}
					}
					payload := fmt.Sprintf(`{"type":"response.create","model":"gpt-5.1","stream":%t,"store":false,"tools":[{"type":"function","name":"exec","parameters":{"type":"object"}}],"input":%s%s}`, stream, input, previous)
					require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(payload)))
					for {
						_, event, err := conn.Read(ctx)
						require.NoError(t, err)
						require.NotEqual(t, "error", gjson.GetBytes(event, "type").String(), string(event))
						if gjson.GetBytes(event, "type").String() == "response.completed" {
							break
						}
					}
				}
				require.NoError(t, conn.Close(coderws.StatusNormalClosure, "done"))
				select {
				case err := <-errCh:
					require.NoError(t, err)
				case <-ctx.Done():
					t.Fatal("bridge session did not terminate")
				}
				require.Len(t, upstream.bodies, 3)
				for n := 1; n < 3; n++ {
					body := upstream.bodies[n]
					require.False(t, gjson.GetBytes(body, "previous_response_id").Exists())
					items := gjson.GetBytes(body, "input").Array()
					expected := 4 + (n-1)*3
					if withTool {
						expected += n
					}
					require.Len(t, items, expected)
					require.Equal(t, "initial", items[0].String())
					require.Equal(t, "rs_keep_1", items[1].Get("id").String())
					require.Equal(t, "  opaque-雪-\\cipher  ", items[1].Get("encrypted_content").String())
					require.Equal(t, "9007199254740993", items[1].Get("vendor_counter").Raw)
					require.Equal(t, "msg_1", items[2].Get("id").String())
					require.Equal(t, "9007199254740993", items[2].Get("opaque.large").Raw)
					for _, dropped := range []string{"rs_missing", "rs_null", "rs_empty", "rs_blank", "rs_number", "rs_object"} {
						require.NotContains(t, string(body), dropped)
					}
					for _, id := range []string{"rs_keep_1", "msg_1"} {
						require.Equal(t, 1, strings.Count(string(body), `"id":"`+id+`"`))
					}
					if withTool {
						require.Equal(t, "function_call", items[3].Get("type").String())
						require.Equal(t, "call_1", items[3].Get("call_id").String())
						require.Equal(t, "function_call_output", items[4].Get("type").String())
					}
					if n == 2 {
						start := 4
						if withTool {
							start = 5
						}
						require.Equal(t, "rs_keep_2", items[start].Get("id").String())
						require.Equal(t, "msg_2", items[start+1].Get("id").String())
						if withTool {
							require.Equal(t, "call_2", items[start+2].Get("call_id").String())
							require.Equal(t, "call_2", items[start+3].Get("call_id").String())
						}
					}
				}
			})
		}
	}
}

func TestOpenAIWSHTTPBridgeHistory_NumericAndSingleObjectControls(t *testing.T) {
	for _, value := range []string{"0", "-2", "1.25", "1e3", "9007199254740993"} {
		t.Run(value, func(t *testing.T) {
			body, err := prepareOpenAIWSHTTPBridgeBody([]byte(`{"type":"response.create","input":[{"role":"user","content":"hi","opaque":` + value + `}],"max_output_tokens":8}`))
			require.NoError(t, err)
			require.Equal(t, value, gjson.GetBytes(body, "input.0.opaque").Raw)
			require.EqualValues(t, 8, gjson.GetBytes(body, "max_output_tokens").Int())
			require.True(t, gjson.GetBytes(body, "stream").Bool())
			require.False(t, gjson.GetBytes(body, "type").Exists())
		})
	}
	for _, payload := range []string{`null`, `[]`, `{"input":"hi"} {}`, `{"input":"hi"} 1`, `{"input":"hi"}junk`} {
		t.Run(payload, func(t *testing.T) { _, err := prepareOpenAIWSHTTPBridgeBody([]byte(payload)); require.Error(t, err) })
	}
}
