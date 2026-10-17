package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type oauthWebSearchSocketDialer struct {
	endpoint string
}

func (d *oauthWebSearchSocketDialer) Dial(ctx context.Context, _ string, headers http.Header, _ string) (openAIWSClientConn, int, http.Header, error) {
	conn, response, err := coderws.Dial(ctx, d.endpoint, &coderws.DialOptions{HTTPHeader: headers})
	if err != nil {
		return nil, 0, nil, err
	}
	return &coderOpenAIWSClientConn{conn: conn}, response.StatusCode, response.Header, nil
}

func oauthWebSearchDeclared(body []byte) bool {
	if gjsonToolsContainWebSearch(gjson.GetBytes(body, "tools")) {
		return true
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "additional_tools" && gjsonToolsContainWebSearch(item.Get("tools")) {
			return true
		}
	}
	return false
}

func oauthWebSearchCompleted(turn int) []byte {
	return []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_search_%d","model":"gpt-5.4","status":"completed","output":[{"type":"compaction","encrypted_content":"sealed_history"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"compacted"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`, turn))
}

// Both sides are real sockets. The provider emits each terminal event only
// after receiving that turn's production-normalized request, without sleeps.
func TestOAuthWebSearchHistoryWS_ActualTwoTurnWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"ctx_pool", "passthrough", "http_bridge"} {
		for _, lite := range []bool{false, true} {
			for _, namespace := range []bool{false, true} {
				for _, apiKey := range []bool{false, true} {
					if apiKey && namespace {
						continue // API-key bridge has an existing namespace lowering dialect.
					}
					t.Run(fmt.Sprintf("%s/lite_%t/namespace_%t/api_key_%t", route, lite, namespace, apiKey), func(t *testing.T) {
						captures := make(chan oauthWebSearchWireCapture, 4)
						provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if route == "http_bridge" {
								body, err := io.ReadAll(r.Body)
								if err != nil {
									w.WriteHeader(http.StatusBadRequest)
									return
								}
								select {
								case captures <- oauthWebSearchWireCapture{body, r.Header.Clone()}:
								default:
									w.WriteHeader(http.StatusInternalServerError)
									return
								}
								if !apiKey && !oauthWebSearchDeclared(body) {
									w.WriteHeader(http.StatusBadRequest)
									_, _ = io.WriteString(w, `{"error":{"message":"response protection is unavailable","type":"invalid_request_error"}}`)
									return
								}
								w.Header().Set("Content-Type", "text/event-stream")
								_, _ = fmt.Fprintf(w, "data: %s\n\n", oauthWebSearchCompleted(1))
								return
							}
							conn, err := coderws.Accept(w, r, nil)
							if err != nil {
								return
							}
							defer func() { _ = conn.CloseNow() }()
							ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
							defer cancel()
							for turn := 1; turn <= 2; turn++ {
								_, body, err := conn.Read(ctx)
								if err != nil {
									return
								}
								select {
								case captures <- oauthWebSearchWireCapture{body, r.Header.Clone()}:
								case <-ctx.Done():
									return
								}
								if !apiKey && !oauthWebSearchDeclared(body) {
									_ = conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.failed","response":{"error":{"message":"response protection is unavailable"}}}`))
									return
								}
								if err := conn.Write(ctx, coderws.MessageText, oauthWebSearchCompleted(turn)); err != nil {
									return
								}
							}
							// Keep the pooled lease alive until the gateway closes the session.
							_, _, _ = conn.Read(ctx)
						}))
						defer provider.Close()
						cfg := &config.Config{}
						cfg.Security.URLAllowlist.AllowInsecureHTTP = true
						cfg.Gateway.OpenAIWS.Enabled = true
						cfg.Gateway.OpenAIWS.OAuthEnabled = true
						cfg.Gateway.OpenAIWS.APIKeyEnabled = true
						cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
						cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
						cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
						cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
						cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
						cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
						cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
						cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
						cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
						cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = route == "http_bridge"
						cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
						endpoint, err := url.Parse(provider.URL)
						require.NoError(t, err)
						dialer := &oauthWebSearchSocketDialer{endpoint: "ws" + strings.TrimPrefix(provider.URL, "http")}
						svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &oauthWebSearchSocketUpstream{client: &http.Client{Timeout: 5 * time.Second}, target: endpoint}, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPassthroughDialer: dialer}
						if route == "ctx_pool" {
							svc.openaiWSPool = newOpenAIWSConnPool(cfg)
							svc.openaiWSPool.setClientDialerForTest(dialer)
						}
						defer svc.CloseOpenAIWSPool()
						mode := OpenAIWSIngressModeCtxPool
						if route == "passthrough" {
							mode = OpenAIWSIngressModePassthrough
						}
						account := &Account{ID: 7939, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"access_token": "fixture-token", "chatgpt_account_id": "fixture-account"}, Extra: map[string]any{"openai_oauth_responses_websockets_v2_mode": mode}}
						if apiKey {
							account.Type = AccountTypeAPIKey
							account.Credentials["api_key"] = "fixture-key"
							account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
						}
						results := make(chan *OpenAIForwardResult, 4)
						hooks := &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
							if err == nil && result != nil {
								results <- result
							}
						}}
						errCh := make(chan error, 1)
						gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							conn, err := coderws.Accept(w, r, nil)
							if err != nil {
								errCh <- err
								return
							}
							defer func() { _ = conn.CloseNow() }()
							ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
							defer cancel()
							_, initial, err := conn.Read(ctx)
							if err != nil {
								errCh <- err
								return
							}
							c, _ := gin.CreateTestContext(httptest.NewRecorder())
							c.Request = r.Clone(ctx)
							errCh <- svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "fixture-token", initial, hooks)
						}))
						defer gateway.Close()
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http"), nil)
						require.NoError(t, err)
						defer func() { _ = conn.CloseNow() }()
						for turn := 1; turn <= 2; turn++ {
							tools, choice, metadata := `[]`, `"auto"`, `{}`
							if namespace {
								tools = `[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}]`
								choice = `{"type":"namespace","name":"collaboration"}`
							}
							if lite {
								metadata = `{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}`
							}
							payload := []byte(fmt.Sprintf(`{"type":"response.create","model":"gpt-5.4","store":false,"input":[{"type":"web_search_call","id":"ws_%d","status":"completed","unknown":9007199254740993},{"type":"compaction_trigger"}],"tools":%s,"tool_choice":%s,"client_metadata":%s,"custom_extension":{"large":123456789012345678901234567890}}`, turn, tools, choice, metadata))
							kind := coderws.MessageText
							if turn == 2 {
								kind = coderws.MessageBinary
							}
							require.NoError(t, conn.Write(ctx, kind, payload))
							for {
								_, event, err := conn.Read(ctx)
								require.NoError(t, err)
								eventType := gjson.GetBytes(event, "type").String()
								require.NotContains(t, []string{"error", "response.failed"}, eventType, string(event))
								if eventType == "response.completed" {
									require.Contains(t, string(event), "sealed_history")
									break
								}
							}
							select {
							case capture := <-captures:
								wire := capture.body
								items := gjson.GetBytes(wire, "input").Array()
								require.GreaterOrEqual(t, len(items), 2)
								require.Equal(t, "compaction_trigger", items[len(items)-1].Get("type").String())
								require.Equal(t, "9007199254740993", items[0].Get("unknown").Raw)
								require.Equal(t, "123456789012345678901234567890", gjson.GetBytes(wire, "custom_extension.large").Raw)
								if namespace {
									require.JSONEq(t, choice, gjson.GetBytes(wire, "tool_choice").Raw)
								} else {
									wantChoice := "none"
									if apiKey {
										wantChoice = "auto"
									}
									require.Equal(t, wantChoice, gjson.GetBytes(wire, "tool_choice").String())
								}
								if apiKey {
									require.False(t, oauthWebSearchDeclared(wire), "API key WS must not inject hosted tools")
									continue
								}
								var search gjson.Result
								if lite {
									require.False(t, gjsonToolsContainWebSearch(gjson.GetBytes(wire, "tools")))
									search = items[len(items)-2].Get(`tools.#(type=="web_search")`)
									require.Equal(t, "all_turns", gjson.GetBytes(wire, "reasoning.context").String())
									if namespace {
										require.Equal(t, "collaboration", items[len(items)-2].Get("tools.1.name").String())
									}
									if route == "http_bridge" {
										require.Equal(t, "true", capture.header.Get(responsesLiteHeader))
									}
								} else {
									search = gjson.GetBytes(wire, `tools.#(type=="web_search")`)
								}
								require.True(t, search.Exists())
								require.True(t, search.Get("external_web_access").Exists())
								require.False(t, search.Get("external_web_access").Bool())
							case <-ctx.Done():
								t.Fatal("provider did not receive turn")
							}
						}
						require.NoError(t, conn.Close(coderws.StatusNormalClosure, "done"))
						select {
						case err := <-errCh:
							if err != nil {
								require.Contains(t, err.Error(), "StatusNormalClosure")
							}
						case <-ctx.Done():
							t.Fatal("gateway session did not terminate")
						}
						for turn := 1; turn <= 2; turn++ {
							select {
							case result := <-results:
								require.Equal(t, 10, result.Usage.InputTokens)
								require.Equal(t, 5, result.Usage.OutputTokens)
							case <-ctx.Done():
								t.Fatal("completed turn was not accounted")
							}
						}
						require.Empty(t, captures, "exactly two actual upstream requests")
						require.Empty(t, results, "exactly two metered turns")
					})
				}
			}
		}
	}
}

func TestOAuthWebSearchHistoryWS_ActualSessionInheritance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, lite := range []bool{false, true} {
		for _, namespace := range []bool{false, true} {
			for _, override := range []bool{false, true} {
				t.Run(fmt.Sprintf("lite_%t/namespace_%t/override_%t", lite, namespace, override), func(t *testing.T) {
					captures := make(chan []byte, 4)
					provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						conn, err := coderws.Accept(w, r, nil)
						if err != nil {
							return
						}
						defer func() { _ = conn.CloseNow() }()
						ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
						defer cancel()
						turn := 1
						for frame := 0; frame < 3; frame++ {
							_, body, err := conn.Read(ctx)
							if err != nil {
								return
							}
							captures <- body
							event := []byte(`{"type":"session.updated"}`)
							if gjson.GetBytes(body, "type").String() == "response.create" {
								event = oauthWebSearchCompleted(turn)
								turn++
							}
							if err := conn.Write(ctx, coderws.MessageText, event); err != nil {
								return
							}
						}
						_, _, _ = conn.Read(ctx)
					}))
					defer provider.Close()
					cfg := &config.Config{}
					cfg.Gateway.OpenAIWS.Enabled = true
					cfg.Gateway.OpenAIWS.OAuthEnabled = true
					cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
					cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
					cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
					cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
					cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
					svc := &OpenAIGatewayService{cfg: cfg, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), openaiWSPassthroughDialer: &oauthWebSearchSocketDialer{endpoint: "ws" + strings.TrimPrefix(provider.URL, "http")}}
					account := &Account{ID: 7939, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"access_token": "fixture-token"}, Extra: map[string]any{"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough}}
					errCh := make(chan error, 1)
					gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						conn, err := coderws.Accept(w, r, nil)
						if err != nil {
							errCh <- err
							return
						}
						defer func() { _ = conn.CloseNow() }()
						ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
						defer cancel()
						_, initial, err := conn.Read(ctx)
						if err != nil {
							errCh <- err
							return
						}
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = r.Clone(ctx)
						errCh <- svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "fixture-token", initial, nil)
					}))
					defer gateway.Close()
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http"), nil)
					require.NoError(t, err)
					defer func() { _ = conn.CloseNow() }()
					writeReadCapture := func(frame string, wantEvent string) []byte {
						t.Helper()
						require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(frame)))
						_, event, err := conn.Read(ctx)
						require.NoError(t, err)
						require.Equal(t, wantEvent, gjson.GetBytes(event, "type").String(), string(event))
						select {
						case body := <-captures:
							return body
						case <-ctx.Done():
							t.Fatal("actual upstream frame missing")
							return nil
						}
					}
					_ = writeReadCapture(`{"type":"response.create","model":"gpt-5.4","input":"initial"}`, "response.completed")
					tools := `[{"type":"function","name":"echo","parameters":{"type":"object"},"unknown":9007199254740993}]`
					choice := `{"type":"function","name":"echo"}`
					if namespace {
						tools = `[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}],"unknown":9007199254740993}]`
						choice = `{"type":"namespace","name":"collaboration"}`
					}
					update := fmt.Sprintf(`{"type":"session.update","session":{"tools":%s,"tool_choice":%s}}`, tools, choice)
					updateWire := writeReadCapture(update, "session.updated")
					require.Equal(t, tools, gjson.GetBytes(updateWire, "session.tools").Raw)
					require.Equal(t, choice, gjson.GetBytes(updateWire, "session.tool_choice").Raw)
					metadata, explicit := `{}`, ""
					if lite {
						metadata = `{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}`
					}
					if override {
						explicit = `,"tools":[],"tool_choice":"auto"`
					}
					followup := fmt.Sprintf(`{"type":"response.create","model":"gpt-5.4","input":[{"type":"web_search_call","unknown":9007199254740993},{"type":"compaction_trigger"}],"client_metadata":%s%s}`, metadata, explicit)
					wire := writeReadCapture(followup, "response.completed")
					require.True(t, oauthWebSearchDeclared(wire))
					require.Equal(t, "9007199254740993", gjson.GetBytes(wire, "input.0.unknown").Raw)
					items := gjson.GetBytes(wire, "input").Array()
					require.Equal(t, "compaction_trigger", items[len(items)-1].Get("type").String())
					if override {
						require.Equal(t, "none", gjson.GetBytes(wire, "tool_choice").String())
						require.NotContains(t, string(wire), "echo")
						require.NotContains(t, string(wire), "collaboration")
					} else {
						require.JSONEq(t, choice, gjson.GetBytes(wire, "tool_choice").Raw)
						path := "tools.0"
						if lite && namespace {
							path = "input.1.tools.1"
						}
						require.Equal(t, "9007199254740993", gjson.GetBytes(wire, path+".unknown").Raw)
						require.Equal(t, gjson.Parse(tools).Array()[0].Get("name").String(), gjson.GetBytes(wire, path+".name").String())
					}
					require.NoError(t, conn.Close(coderws.StatusNormalClosure, "done"))
					select {
					case err := <-errCh:
						if err != nil {
							require.Contains(t, err.Error(), "StatusNormalClosure")
						}
					case <-ctx.Done():
						t.Fatal("session did not terminate")
					}
					require.Empty(t, captures)
				})
			}
		}
	}
}
