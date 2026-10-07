//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise the actual ForwardEmbeddings outbound request, including a provider
// that omits response.model. This is protocol/usage attribution, not settlement.
type embeddingsModelAuditUpstream struct {
	HTTPUpstream
	calls         int
	accountID     int64
	concurrency   int
	rawBody       []byte
	decodedModel  string
	omitModel     bool
	requestURL    string
	authorization string
}

func (u *embeddingsModelAuditUpstream) Do(req *http.Request, _ string, accountID int64, concurrency int) (*http.Response, error) {
	u.calls++
	u.accountID = accountID
	u.concurrency = concurrency
	u.requestURL = req.URL.String()
	u.authorization = req.Header.Get("Authorization")
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
		"object": "list",
		"data":   []any{map[string]any{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2}}},
		"usage":  map[string]any{"prompt_tokens": 13, "total_tokens": 13},
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
		Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"embeddings-model-audit"}},
		Body:       io.NopCloser(strings.NewReader(string(encoded))),
	}, nil
}

func TestGatewayModelEmbeddings_ForwardWireAndPolicy(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		mappingName := "unmapped"
		if mapped {
			mappingName = "mapped"
		}
		for _, omitModel := range []bool{false, true} {
			responseName := "honest_model"
			if omitModel {
				responseName = "model_omitted"
			}
			for _, tc := range []struct {
				name, fields, input string
				ambiguous           bool
			}{
				{"plain_duplicate", `"model":"text-embedding-3-small","model":"text-embedding-3-large"`, `["hello","world"]`, true},
				{"escaped_duplicate", `"model":"text-embedding-3-small","\u006dodel":"text-embedding-3-large"`, `["hello","world"]`, true},
				{"case_duplicate", `"model":"text-embedding-3-small","Model":"text-embedding-3-large"`, `["hello","world"]`, true},
				{"equal_duplicate", `"model":"text-embedding-3-small","model":"text-embedding-3-small"`, `["hello","world"]`, true},
				{"canonical_batch", `"model":"text-embedding-3-small"`, `["hello","world"]`, false},
				{"nested_duplicates", `"model":"text-embedding-3-small","metadata":{"model":"nested-first","model":"nested-last","Model":"nested-case"}`, `["hello","world"]`, false},
				{"escaped_canonical", `"\u006dodel":"text-embedding-3-small"`, `["hello","world"]`, false},
				{"token_batch", `"model":"text-embedding-3-small"`, `[[101,202],[303,404]]`, false},
			} {
				t.Run(mappingName+"/"+responseName+"/"+tc.name, func(t *testing.T) {
					body := []byte(`{` + tc.fields + `,"input":` + tc.input + `,"encoding_format":"float","dimensions":256,"user":"opaque-user","opaque":{"number":1e+03,"items":[null,true]}}`)
					c, rec := commandCodeClientToolsContext(body)
					upstream := &embeddingsModelAuditUpstream{omitModel: omitModel}
					svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
					account := &Account{
						ID: 7890, Concurrency: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
						Credentials: map[string]any{"api_key": "sk-embeddings-fixture", "base_url": "https://api.example.com"},
					}
					if mapped {
						account.Credentials["model_mapping"] = map[string]any{"text-embedding-3-small": "provider-embedding"}
					}
					result, err := svc.ForwardEmbeddings(context.Background(), c, account, body, "")
					// Log before assertions so OLD records real outbound work rather
					// than a nil dependency panic or an inferred parser mismatch.
					t.Logf("calls=%d account=%d concurrency=%d outbound_model=%s result=%+v error=%v raw=%s", upstream.calls, upstream.accountID, upstream.concurrency, upstream.decodedModel, result, err, upstream.rawBody)
					if tc.ambiguous {
						require.Zero(t, upstream.calls, "ambiguous model must not enter actual HTTPUpstream.Do")
						require.Error(t, err)
						require.Nil(t, result)
						require.Equal(t, http.StatusBadRequest, rec.Code)
						require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
						require.Equal(t, "model", gjson.GetBytes(rec.Body.Bytes(), "error.param").String())
						require.Contains(t, rec.Body.String(), "canonical field name")
						return
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, 1, upstream.calls)
					require.Equal(t, account.ID, upstream.accountID)
					require.Equal(t, account.Concurrency, upstream.concurrency)
					require.Equal(t, "https://api.example.com/v1/embeddings", upstream.requestURL)
					require.Equal(t, "Bearer sk-embeddings-fixture", upstream.authorization)
					wantModel := "text-embedding-3-small"
					if mapped {
						wantModel = "provider-embedding"
					} else {
						require.Equal(t, body, upstream.rawBody, "unmapped payload must pass through byte for byte")
					}
					require.Equal(t, wantModel, upstream.decodedModel)
					require.Equal(t, "text-embedding-3-small", result.Model)
					require.Equal(t, wantModel, result.BillingModel)
					require.Equal(t, wantModel, result.UpstreamModel)
					require.Equal(t, 13, result.Usage.InputTokens)
					require.Zero(t, result.Usage.OutputTokens)
					require.Equal(t, "embeddings-model-audit", result.RequestID)
					require.Equal(t, http.StatusOK, rec.Code)
					require.Equal(t, !omitModel, gjson.GetBytes(rec.Body.Bytes(), "model").Exists())
					for _, field := range []string{"input", "metadata", "encoding_format", "dimensions", "user", "opaque"} {
						require.Equal(t, gjson.GetBytes(body, field).Raw, gjson.GetBytes(upstream.rawBody, field).Raw, "opaque field %s must retain its original raw value", field)
					}
				})
			}
		}
	}
}
