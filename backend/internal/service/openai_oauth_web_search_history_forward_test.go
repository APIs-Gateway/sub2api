package service

import (
	"bytes"
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
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type oauthWebSearchWireCapture struct {
	body   []byte
	header http.Header
}

// The transport preserves the production-built request for inspection, then
// sends it over an actual HTTP socket to a local fixture provider in remote CI.
type oauthWebSearchSocketUpstream struct {
	client      *http.Client
	target      *url.URL
	originalURL string
}

func (u *oauthWebSearchSocketUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.originalURL = req.URL.String()
	next := req.Clone(req.Context())
	endpoint := *req.URL
	endpoint.Scheme, endpoint.Host = u.target.Scheme, u.target.Host
	next.URL = &endpoint
	next.Host = u.target.Host
	return u.client.Do(next)
}

func (u *oauthWebSearchSocketUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func TestOAuthWebSearchHistoryForward_ActualHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, lite := range []bool{false, true} {
			for _, declaration := range []string{"none", "function", "namespace"} {
				t.Run(fmt.Sprintf("passthrough_%t/lite_%t/%s", passthrough, lite, declaration), func(t *testing.T) {
					captures := make(chan oauthWebSearchWireCapture, 4)
					provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						select {
						case captures <- oauthWebSearchWireCapture{body: body, header: r.Header.Clone()}:
						default:
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
						declared := gjsonToolsContainWebSearch(gjson.GetBytes(body, "tools"))
						for _, item := range gjson.GetBytes(body, "input").Array() {
							if item.Get("type").String() == "additional_tools" && gjsonToolsContainWebSearch(item.Get("tools")) {
								declared = true
							}
						}
						if !declared {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusBadRequest)
							_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"response protection is unavailable"}}`)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"resp_history","model":"gpt-5.4","status":"completed","output":[{"type":"compaction","encrypted_content":"sealed_history"},{"type":"message","id":"msg_history","role":"assistant","content":[{"type":"output_text","text":"compacted"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`+"\n\n")
					}))
					t.Cleanup(provider.Close)
					target, err := url.Parse(provider.URL)
					require.NoError(t, err)
					upstream := &oauthWebSearchSocketUpstream{client: &http.Client{Timeout: 5 * time.Second}, target: target}
					cfg := &config.Config{}
					svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
					account := &Account{ID: 7939, Name: "history", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1, Status: StatusActive, Schedulable: true, RateMultiplier: f64p(1), Credentials: map[string]any{"access_token": "fixture-token", "chatgpt_account_id": "fixture-account"}, Extra: map[string]any{"openai_passthrough": passthrough}}
					tools, choice := `[]`, `"auto"`
					switch declaration {
					case "function":
						tools = `[{"type":"function","name":"echo","parameters":{"type":"object"}}]`
						choice = `{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"echo"}]}`
					case "namespace":
						tools = `[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}]`
						choice = `{"type":"namespace","name":"collaboration"}`
					}
					body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","instructions":"summarize","stream":false,"input":[{"type":"web_search_call","id":"ws_history","status":"completed","action":{"type":"search","query":"q"},"unknown":9007199254740993},{"type":"compaction_trigger","unknown":"keep"}],"tools":%s,"tool_choice":%s,"parallel_tool_calls":false,"custom_extension":{"large":123456789012345678901234567890}}`, tools, choice))
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
					c.Request.Header.Set("User-Agent", "codex_cli_rs/0.159.0")
					if lite {
						c.Request.Header.Set(responsesLiteHeader, "true")
					}
					result, err := svc.Forward(context.Background(), c, account, body)
					require.NoError(t, err, recorder.Body.String())
					require.NotNil(t, result)
					require.Equal(t, 10, result.Usage.InputTokens)
					require.Equal(t, 5, result.Usage.OutputTokens)
					require.Contains(t, recorder.Body.String(), "sealed_history")
					require.Contains(t, upstream.originalURL, "chatgpt.com/backend-api/codex/responses")
					var captured oauthWebSearchWireCapture
					select {
					case captured = <-captures:
					case <-time.After(time.Second):
						t.Fatal("actual provider received no request")
					}
					wire := captured.body
					require.Equal(t, "Bearer fixture-token", captured.header.Get("Authorization"))
					items := gjson.GetBytes(wire, "input").Array()
					require.Equal(t, "web_search_call", items[0].Get("type").String())
					require.Equal(t, "compaction_trigger", items[len(items)-1].Get("type").String())
					require.Equal(t, "9007199254740993", items[0].Get("unknown").Raw)
					require.Equal(t, "123456789012345678901234567890", gjson.GetBytes(wire, "custom_extension.large").Raw)
					if declaration == "none" {
						require.Equal(t, "none", gjson.GetBytes(wire, "tool_choice").String())
					} else {
						require.JSONEq(t, choice, gjson.GetBytes(wire, "tool_choice").Raw)
					}
					var search gjson.Result
					if lite {
						require.False(t, gjsonToolsContainWebSearch(gjson.GetBytes(wire, "tools")))
						require.Equal(t, "additional_tools", items[len(items)-2].Get("type").String())
						search = items[len(items)-2].Get(`tools.#(type=="web_search")`)
						require.Equal(t, "true", captured.header.Get(responsesLiteHeader))
						require.False(t, gjson.GetBytes(wire, "parallel_tool_calls").Bool())
						require.Equal(t, "all_turns", gjson.GetBytes(wire, "reasoning.context").String())
						if declaration == "namespace" {
							require.Equal(t, "collaboration", items[len(items)-2].Get("tools.1.name").String())
						}
					} else {
						search = gjson.GetBytes(wire, `tools.#(type=="web_search")`)
					}
					require.True(t, search.Exists())
					require.True(t, search.Get("external_web_access").Exists())
					require.False(t, search.Get("external_web_access").Bool())
					require.Empty(t, captures, "one upstream attempt and no replay")
				})
			}
		}
	}
}

func TestOAuthWebSearchHistoryAccountAndCompactGuards(t *testing.T) {
	body := []byte(openAIWebSearchHistoryCompactionBody)
	for _, account := range []*Account{nil, {Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, {Platform: PlatformGrok, Type: AccountTypeOAuth}, {Platform: PlatformOpenAI, Type: AccountTypeOAuth}} {
		for _, compact := range []bool{false, true} {
			if account != nil && account.IsOpenAIOAuth() && !compact {
				continue
			}
			out, changed, err := normalizeOpenAIOAuthWebSearchHistoryForAccount(body, account, true, compact)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, body, out)
		}
	}
	require.False(t, strings.Contains(string(body), `"external_web_access"`))
}

func TestOAuthWebSearchHistoryForward_ActualGuardedHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, passthrough := range []bool{false, true} {
			for _, lite := range []bool{false, true} {
				for _, compact := range []bool{false, true} {
					if accountType == AccountTypeOAuth && !compact {
						continue
					}
					t.Run(fmt.Sprintf("%s/passthrough_%t/lite_%t/compact_%t", accountType, passthrough, lite, compact), func(t *testing.T) {
						captures := make(chan oauthWebSearchWireCapture, 4)
						provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, `{"id":"resp_guarded","object":"response","status":"completed","model":"gpt-5.4","output":[{"type":"compaction","encrypted_content":"sealed_guarded"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`)
						}))
						defer provider.Close()
						target, err := url.Parse(provider.URL)
						require.NoError(t, err)
						upstream := &oauthWebSearchSocketUpstream{client: &http.Client{Timeout: 5 * time.Second}, target: target}
						svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
						account := &Account{ID: 7939, Platform: PlatformOpenAI, Type: accountType, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "fixture-key", "access_token": "fixture-token", "chatgpt_account_id": "fixture-account"}, Extra: map[string]any{"openai_passthrough": passthrough}}
						body := []byte(`{"model":"gpt-5.4","instructions":"summarize","stream":false,"input":[{"type":"web_search_call","unknown":9007199254740993},{"type":"compaction_trigger"}],"tools":[],"tool_choice":"auto","parallel_tool_calls":false}`)
						path := "/v1/responses"
						if compact {
							path += "/compact"
						}
						recorder := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(recorder)
						c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
						c.Request.Header.Set("User-Agent", "codex_cli_rs/0.159.0")
						if lite {
							c.Request.Header.Set(responsesLiteHeader, "true")
						}
						result, err := svc.Forward(context.Background(), c, account, body)
						require.NoError(t, err)
						require.NotNil(t, result)
						require.Contains(t, recorder.Body.String(), "sealed_guarded")
						select {
						case capture := <-captures:
							require.False(t, oauthWebSearchDeclared(capture.body), "API key and dedicated compact must not inject hosted tools")
							require.Equal(t, "auto", gjson.GetBytes(capture.body, "tool_choice").String())
							require.Equal(t, "9007199254740993", gjson.GetBytes(capture.body, "input.0.unknown").Raw)
							require.Equal(t, "compaction_trigger", gjson.GetBytes(capture.body, "input.1.type").String())
							if compact {
								require.True(t, strings.HasSuffix(upstream.originalURL, "/responses/compact"))
							}
						case <-time.After(time.Second):
							t.Fatal("guarded request never reached provider")
						}
						require.Empty(t, captures)
					})
				}
			}
		}
	}
}
