//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// This audit calls existing production forwarding methods. The isolated
// upstream decodes the actual outbound JSON with encoding/json and can omit
// its response model, as existing compatible-provider fixtures already do.
// It does not call a live provider or claim to exercise funded settlement.
type duplicateModelAuditUpstream struct {
	HTTPUpstream
	rawBody      []byte
	decodedModel string
	omitModel    bool
}

func (u *duplicateModelAuditUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	var err error
	u.rawBody, err = io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var request struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(u.rawBody, &request); err != nil {
		return nil, err
	}
	u.decodedModel = request.Model
	response := map[string]any{
		"id": "resp_duplicate_model_audit", "status": "completed",
		"output": []any{map[string]any{
			"type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "audit output"}},
		}},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 2},
	}
	if strings.HasSuffix(req.URL.Path, "/chat/completions") {
		response = map[string]any{
			"id": "chatcmpl_duplicate_model_audit", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "audit output"},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 2},
		}
	}
	if !u.omitModel {
		response["model"] = u.decodedModel
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(encoded))),
	}, nil
}

func (u *duplicateModelAuditUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

func TestDuplicateModelAudit_WireAndPolicyAgreement(t *testing.T) {
	for _, route := range []string{"native_responses", "passthrough_responses", "raw_chat"} {
		for _, tc := range []struct {
			name, fields string
			ambiguous    bool
		}{
			{"single_model", `"model":"gpt-5.6-luna"`, false},
			{"single_costly_model", `"model":"gpt-6-astra"`, false},
			{"nested_model", `"model":"gpt-5.6-luna","metadata":{"model":"gpt-6-astra"}`, false},
			{"cheap_then_costly", `"model":"gpt-5.6-luna","model":"gpt-6-astra"`, true},
			{"costly_then_cheap", `"model":"gpt-6-astra","model":"gpt-5.6-luna"`, true},
			{"escaped_duplicate", `"model":"gpt-5.6-luna","\u006dodel":"gpt-6-astra"`, true},
			{"equal_duplicate", `"model":"gpt-5.6-luna","model":"gpt-5.6-luna"`, true},
			{"case_alias", `"model":"gpt-5.6-luna","Model":"gpt-6-astra"`, true},
			{"alias_only", `"Model":"gpt-6-astra"`, true},
			{"escaped_single_model", `"\u006dodel":"gpt-5.6-luna"`, false},
		} {
			for _, omitModel := range []bool{false, true} {
				responseShape := "honest_model"
				if omitModel {
					responseShape = "model_omitted"
				}
				t.Run(route+"/"+tc.name+"/"+responseShape, func(t *testing.T) {
					body := []byte(`{` + tc.fields + `,"stream":false,"input":"audit","messages":[{"role":"user","content":"audit"}]}`)
					c, rec := commandCodeClientToolsContext(body)
					upstream := &duplicateModelAuditUpstream{omitModel: omitModel}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
					account := commandCodeNativeAccount("https://api.example.com/v1")
					if route == "passthrough_responses" {
						account.Extra["openai_passthrough"] = true
					}
					var result *OpenAIForwardResult
					var err error
					if route == "raw_chat" {
						result, err = svc.forwardAsRawChatCompletions(context.Background(), c, account, body, "")
					} else {
						result, err = svc.Forward(context.Background(), c, account, body)
					}
					if err != nil {
						require.True(t, tc.ambiguous, "a normal single-model request must still work: %v", err)
						require.Nil(t, result)
						require.Empty(t, upstream.rawBody, "ambiguous input must be rejected before any upstream invocation")
						require.Equal(t, http.StatusBadRequest, rec.Code)
						require.NotContains(t, rec.Body.String(), "audit output", "rejected ambiguous request must not expose generated output")
						t.Logf("rejected ambiguous request: %v", err)
						return
					}
					require.False(t, tc.ambiguous, "ambiguous model input must be rejected before forwarding")
					require.NotNil(t, result)
					require.NotEmpty(t, upstream.rawBody, "must inspect the actual production outbound request")
					selectedModel := result.UpstreamModel
					if selectedModel == "" {
						selectedModel = result.Model
					}
					billingModel := result.BillingModel
					if billingModel == "" {
						billingModel = result.Model
					}
					t.Logf("outbound=%s selected=%s billing=%s model_omitted=%t", upstream.decodedModel, selectedModel, billingModel, omitModel)
					require.Equal(t, upstream.decodedModel, selectedModel, "accepted request must select the model the upstream actually decoded")
					require.Equal(t, upstream.decodedModel, billingModel, "unmapped accepted request must retain the same billing model")
				})
			}
		}
	}
}
