//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func chatAudioOuterCases() []struct{ name, fields, selected string } {
	const audio = `[{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`
	var cases []struct{ name, fields, selected string }
	for _, key := range []struct{ name, content, messages, role string }{
		{"duplicate", `"content"`, `"messages"`, `"role"`},
		{"casefold", `"CONTENT"`, `"MESSAGES"`, `"ROLE"`},
		{"escaped", `"\u0063ontent"`, `"\u006dessages"`, `"\u0072ole"`},
	} {
		for _, reverse := range []bool{false, true} {
			first, last, selected := audio, `"hidden"`, "text"
			if reverse {
				first, last, selected = last, first, "audio"
			}
			cases = append(cases, struct{ name, fields, selected string }{
				fmt.Sprintf("content_%s/reverse_%t", key.name, reverse),
				`"messages":[{"role":"user","content":` + first + `,` + key.content + `:` + last + `}]`, selected,
			})
			cases = append(cases, struct{ name, fields, selected string }{
				fmt.Sprintf("messages_%s/reverse_%t", key.name, reverse),
				`"messages":[{"role":"user","content":` + first + `}],` + key.messages + `:[{"role":"user","content":` + last + `}]`, selected,
			})
			firstRole, lastRole := "assistant", "user"
			selected = "audio"
			if reverse {
				firstRole, lastRole, selected = lastRole, firstRole, "already_unsupported"
			}
			cases = append(cases, struct{ name, fields, selected string }{
				fmt.Sprintf("role_%s/reverse_%t", key.name, reverse),
				`"messages":[{"role":"` + firstRole + `",` + key.role + `:"` + lastRole + `","content":` + audio + `}]`, selected,
			})
		}
	}
	cases = append(cases, struct{ name, fields, selected string }{"plain_positive", `"messages":[{"role":"user","content":"hidden"}]`, "control"})
	return cases
}

func TestChatInputAudioOuter_ConvertedRealOutbound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"gemini", "anthropic", "openai"} {
		for _, tc := range chatAudioOuterCases() {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				body := []byte(`{"model":"public-small",` + tc.fields + `}`)
				c, rec := commandCodeClientToolsContext(body)
				account := &Account{ID: 1702, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://api.example.com"}, Extra: map[string]any{"openai_responses_supported": true}}
				upstream := &otherModelAuditUpstream{}
				var err error
				switch route {
				case "gemini":
					account.Platform = PlatformGemini
					account.Credentials["model_mapping"] = map[string]any{"public-small": "gemini-2.5-flash"}
					upstream.response = `{"candidates":[{"content":{"parts":[{"text":"audit"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`
					_, err = (&GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}).ForwardAsChatCompletions(context.Background(), c, account, body)
				case "anthropic":
					account.Platform = PlatformAnthropic
					account.Credentials["model_mapping"] = map[string]any{"public-small": "claude-sonnet-4-5"}
					upstream.response = namespaceToolAnthropicStream()
					_, err = (&GatewayService{cfg: &config.Config{}, httpUpstream: upstream}).ForwardAsChatCompletions(context.Background(), c, account, body, nil)
				case "openai":
					account.Platform = PlatformOpenAI
					account.Credentials["model_mapping"] = map[string]any{"public-small": "gpt-5.4"}
					response := openAICompatSSECompletedResponse("outer-audit", "gpt-5.4")
					raw, readErr := io.ReadAll(response.Body)
					require.NoError(t, readErr)
					upstream.response = string(raw)
					_, err = (&OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}).ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
				}
				// Every old outbound failure first proves the actual selected wire.
				// Already-unsupported selected audio is a retained refusal control.
				if upstream.calls != 0 {
					require.NoError(t, err)
					require.Equal(t, 1, upstream.calls)
					require.Equal(t, account.ID, upstream.accountID)
					if route == "gemini" {
						require.Equal(t, "https://api.example.com/v1beta/models/gemini-2.5-flash:generateContent", upstream.requestURL)
						if tc.selected == "audio" {
							require.JSONEq(t, `[{"inlineData":{"mimeType":"audio/wav","data":"aA=="}}]`, gjson.GetBytes(upstream.rawBody, "contents.0.parts").Raw)
						} else {
							require.JSONEq(t, `[{"text":"hidden"}]`, gjson.GetBytes(upstream.rawBody, "contents.0.parts").Raw)
						}
						require.Equal(t, int64(10), gjson.GetBytes(rec.Body.Bytes(), "usage.prompt_tokens").Int())
						require.Equal(t, int64(5), gjson.GetBytes(rec.Body.Bytes(), "usage.completion_tokens").Int())
					} else {
						require.Contains(t, string(upstream.rawBody), `"hidden"`)
					}
					if tc.selected != "control" {
						t.Logf("OUTER_ACTUAL_OLD_OUTBOUND route=%s calls=1 selected=%s", route, tc.selected)
					}
				}
				if tc.selected == "control" {
					require.NoError(t, err)
					require.Equal(t, 1, upstream.calls)
					require.Equal(t, "public-small", gjson.GetBytes(rec.Body.Bytes(), "model").String())
					if route == "anthropic" {
						require.Equal(t, "claude-sonnet-4-5", gjson.GetBytes(upstream.rawBody, "model").String())
						require.Equal(t, int64(10), gjson.GetBytes(rec.Body.Bytes(), "usage.prompt_tokens").Int())
						require.Equal(t, int64(5), gjson.GetBytes(rec.Body.Bytes(), "usage.completion_tokens").Int())
					} else if route == "openai" {
						require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.rawBody, "model").String())
						require.Equal(t, int64(5), gjson.GetBytes(rec.Body.Bytes(), "usage.prompt_tokens").Int())
						require.Equal(t, int64(2), gjson.GetBytes(rec.Body.Bytes(), "usage.completion_tokens").Int())
					}
					return
				}
				require.Zero(t, upstream.calls)
				require.Error(t, err)
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			})
		}
	}
}

func TestChatInputAudioOuter_ModelAndMalformedPriority(t *testing.T) {
	for _, route := range []string{"gemini", "anthropic", "openai"} {
		for _, malformed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/malformed_%t", route, malformed), func(t *testing.T) {
				body := []byte(`{"model":"first","model":"last","messages":[{"role":"user","content":[{"type":"input_audio"}],"content":"hidden"}]}`)
				if malformed {
					body = []byte(`{"model":`)
				}
				c, rec := commandCodeClientToolsContext(body)
				account := &Account{ID: 1702, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_responses_supported": true}}
				var err error
				switch route {
				case "gemini":
					_, err = (&GeminiMessagesCompatService{}).ForwardAsChatCompletions(context.Background(), c, account, body)
				case "anthropic":
					_, err = (&GatewayService{}).ForwardAsChatCompletions(context.Background(), c, account, body, nil)
				default:
					_, err = (&OpenAIGatewayService{cfg: &config.Config{}}).ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
				}
				require.Error(t, err)
				if !malformed {
					require.Equal(t, "model", gjson.GetBytes(rec.Body.Bytes(), "error.param").String())
					require.Contains(t, err.Error(), "canonical field name")
				} else if route == "gemini" {
					require.Equal(t, "Failed to parse request body", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
				} else {
					require.True(t, strings.HasPrefix(err.Error(), "parse chat completions request:"))
					require.Empty(t, rec.Body.String())
				}
			})
		}
	}
}

func TestChatInputAudioOuter_RawOpenAIAndGrokKeepExistingPolicy(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGrok} {
		for _, sanitize := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sanitize_%t", platform, sanitize), func(t *testing.T) {
				fields := chatAudioOuterCases()[0].fields
				body := []byte(`{"model":"gpt-5",` + fields + `}`)
				if sanitize {
					body = append(body[:len(body)-1], []byte(`,"presence_penalty":0.5}`)...)
				}
				c, rec := commandCodeClientToolsContext(body)
				account := &Account{ID: 1702, Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://api.example.com"}, Extra: map[string]any{"openai_responses_supported": false}}
				upstream := &otherModelAuditUpstream{response: `{"id":"outer-raw","model":"gpt-5","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`}
				result, err := (&OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}).ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 1, upstream.calls)
				require.Equal(t, account.ID, upstream.accountID)
				require.Equal(t, "gpt-5", result.Model)
				require.Equal(t, "gpt-5", result.UpstreamModel)
				require.Equal(t, 10, result.Usage.InputTokens)
				require.Equal(t, 5, result.Usage.OutputTokens)
				require.Equal(t, http.StatusOK, rec.Code)
				if platform == PlatformGrok && sanitize {
					// Existing Grok sanitation rewrites and applies last-key wins.
					require.False(t, gjson.GetBytes(upstream.rawBody, "presence_penalty").Exists())
					require.Equal(t, "hidden", gjson.GetBytes(upstream.rawBody, "messages.0.content").String())
				} else {
					require.Equal(t, string(body), string(upstream.rawBody))
				}
			})
		}
	}
}

func TestChatInputAudioOuter_ResponsesShapeRetained(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_text","text":"input_audio"}]}],"stream":false}`)
	c, rec := commandCodeClientToolsContext(body)
	upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("outer-responses", "gpt-5.4")}
	account := &Account{ID: 1702, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-key"}, Extra: map[string]any{"openai_responses_supported": true}}
	result, err := (&OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}).ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, gjson.GetBytes(body, "input").Raw, gjson.GetBytes(upstream.lastBody, "input").Raw)
	require.Equal(t, http.StatusOK, rec.Code)
}
