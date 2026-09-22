package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const openAIOAuthInputMetadataField = "internal_chat_message_metadata_passthrough"

func TestOAuthResponsesInputInternalMetadataIsStrippedAtOAuthBoundaries(t *testing.T) {
	const body = `{"model":"gpt-5.5","input":[{"role":"user","content":[{"type":"input_text","text":"hello","internal_chat_message_metadata_passthrough":{"keep":true}}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["text"]}},{"type":"function_call","call_id":"fc_123","name":"echo","arguments":"{\"internal_chat_message_metadata_passthrough\":true}","internal_chat_message_metadata_passthrough":null},{"type":"function_call_output","call_id":"fc_123","output":"result"}],"internal_chat_message_metadata_passthrough":{"keep":true}}`

	tests := []struct {
		name      string
		normalize func([]byte) ([]byte, bool, error)
	}{
		{"transformed OAuth", func(b []byte) ([]byte, bool, error) {
			var req map[string]any
			if err := json.Unmarshal(b, &req); err != nil {
				return nil, false, err
			}
			result := applyCodexOAuthTransform(req, false, false)
			out, err := json.Marshal(req)
			return out, result.Modified, err
		}},
		{"OAuth passthrough", func(b []byte) ([]byte, bool, error) { return normalizeOpenAIPassthroughOAuthBody(b, false) }},
		{"OAuth compact", func(b []byte) ([]byte, bool, error) { return normalizeOpenAIPassthroughOAuthBody(b, true) }},
		{"OAuth websocket", func(b []byte) ([]byte, bool, error) {
			out, changed := stripOpenAIOAuthResponsesInputItemMetadata(b)
			return out, changed, nil
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changed, err := tt.normalize([]byte(body))
			require.NoError(t, err)
			require.True(t, changed)
			require.False(t, gjson.GetBytes(out, "input.0."+openAIOAuthInputMetadataField).Exists())
			require.False(t, gjson.GetBytes(out, "input.1."+openAIOAuthInputMetadataField).Exists())
			require.Equal(t, "hello", gjson.GetBytes(out, "input.0.content.0.text").String())
			require.True(t, gjson.GetBytes(out, "input.0.content.0."+openAIOAuthInputMetadataField+".keep").Bool())
			require.True(t, gjson.GetBytes(out, openAIOAuthInputMetadataField+".keep").Bool())
			require.Equal(t, `{"internal_chat_message_metadata_passthrough":true}`, gjson.GetBytes(out, "input.1.arguments").String())
			require.Equal(t, "fc_123", gjson.GetBytes(out, "input.1.call_id").String())
			require.Equal(t, "result", gjson.GetBytes(out, "input.2.output").String())
		})
	}
}

func TestOAuthResponsesInputInternalMetadataSkipsNonArrayInputAndPromptAlias(t *testing.T) {
	for _, body := range []string{
		`{"input":"hello"}`,
		`{"input":{"internal_chat_message_metadata_passthrough":true}}`,
		`{"input":[null,"hello",{"role":"user","content":"hello"}]}`,
		`{"input":[{"content":{"internal_chat_message_metadata_passthrough":true}}]}`,
		`{"prompt":[{"role":"user","content":"hello","internal_chat_message_metadata_passthrough":{}}],"previous_response_id":"resp_123"}`,
	} {
		t.Run(body, func(t *testing.T) {
			out, changed := stripOpenAIOAuthResponsesInputItemMetadata([]byte(body))
			require.False(t, changed)
			require.Equal(t, body, string(out))
		})
	}
}

func TestStripOpenAIOAuthInputInternalMetadataSkipsNonObjectItems(t *testing.T) {
	input := []any{nil, "plain text", []any{"nested array"}}
	require.False(t, stripOpenAIOAuthInputInternalMetadata(input))
}

func TestOAuthResponsesWebSocketBinaryInputMetadataCleanup(t *testing.T) {
	payload := []byte(`{"type":"response.create","input":[{"role":"user","internal_chat_message_metadata_passthrough":{"remove":true}}]}`)
	oauthAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKeyAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	out, changed := stripOpenAIOAuthResponsesWebSocketFrameMetadata(oauthAccount, coderws.MessageBinary, payload)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "input.0."+openAIOAuthInputMetadataField).Exists())

	out, changed = stripOpenAIOAuthResponsesWebSocketFrameMetadata(apiKeyAccount, coderws.MessageBinary, payload)
	require.False(t, changed)
	require.Equal(t, payload, out)

	nonJSONBinary := []byte{0x00, 0xff, 0x10}
	out, changed = stripOpenAIOAuthResponsesWebSocketFrameMetadata(oauthAccount, coderws.MessageBinary, nonJSONBinary)
	require.False(t, changed)
	require.Equal(t, nonJSONBinary, out)
}

func TestAPIKeyResponsesInputInternalMetadataIsPreserved(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.5","stream":false,"input":[{"type":"message","role":"user","content":"hello","internal_chat_message_metadata_passthrough":{"keep":true}}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"capture request"}}`)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          933,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://api.example.test",
		},
		Extra:       map[string]any{"openai_passthrough": true, "openai_responses_supported": true},
		Status:      StatusActive,
		Schedulable: true,
	}

	_, err := svc.Forward(context.Background(), c, account, body)
	require.Error(t, err)
	require.True(t, gjson.GetBytes(upstream.lastBody, "input.0."+openAIOAuthInputMetadataField+".keep").Bool())
}
