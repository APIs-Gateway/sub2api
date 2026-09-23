//go:build unit

package service

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func lineageInvalidSet(ciphers ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(ciphers))
	for _, cipher := range ciphers {
		set[openAIEncryptedContentDigest(cipher)] = struct{}{}
	}
	return set
}

func TestStripOpenAIInvalidEncryptedContentItemsPaths(t *testing.T) {
	invalid := lineageInvalidSet("bad")

	require.Zero(t, stripOpenAIInvalidEncryptedContentItems(nil, invalid))
	require.Zero(t, stripOpenAIInvalidEncryptedContentItems(map[string]any{"input": []any{}}, nil))
	require.Zero(t, stripOpenAIInvalidEncryptedContentItems(map[string]any{"model": "gpt"}, invalid))
	require.Zero(t, stripOpenAIInvalidEncryptedContentItems(map[string]any{"input": "plain text"}, invalid))

	// Array with no hit: non-map item, non-string cipher, other cipher.
	noHit := map[string]any{"input": []any{
		"text",
		map[string]any{"type": "reasoning", "encrypted_content": 42},
		map[string]any{"type": "reasoning", "encrypted_content": "good"},
	}}
	require.Zero(t, stripOpenAIInvalidEncryptedContentItems(noHit, invalid))
	require.Len(t, noHit["input"], 3)

	// Array where every item is dropped removes input entirely.
	allDropped := map[string]any{"input": []any{
		map[string]any{"type": "compaction", "encrypted_content": "bad"},
		map[string]any{"type": "reasoning", "encrypted_content": "bad"},
	}}
	require.Equal(t, 2, stripOpenAIInvalidEncryptedContentItems(allDropped, invalid))
	_, hasInput := allDropped["input"]
	require.False(t, hasInput)

	// Object input: compaction hit deletes input.
	objDrop := map[string]any{"input": map[string]any{"type": "compaction", "encrypted_content": "bad"}}
	require.Equal(t, 1, stripOpenAIInvalidEncryptedContentItems(objDrop, invalid))
	_, hasInput = objDrop["input"]
	require.False(t, hasInput)

	// Object input: reasoning hit keeps the skeleton.
	objKeep := map[string]any{"input": map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": "bad"}}
	require.Equal(t, 1, stripOpenAIInvalidEncryptedContentItems(objKeep, invalid))
	kept, ok := objKeep["input"].(map[string]any)
	require.True(t, ok)
	_, hasCipher := kept["encrypted_content"]
	require.False(t, hasCipher)

	// Object input: hit on an item type the sanitizer does not handle is a no-op.
	objAlien := map[string]any{"input": map[string]any{"type": "message", "encrypted_content": "bad"}}
	require.Zero(t, stripOpenAIInvalidEncryptedContentItems(objAlien, invalid))
}

func TestOpenAIRawPayloadHasInvalidEncryptedContentPaths(t *testing.T) {
	invalid := lineageInvalidSet("bad")

	require.False(t, openAIRawPayloadHasInvalidEncryptedContent(nil, invalid))
	require.False(t, openAIRawPayloadHasInvalidEncryptedContent([]byte(`{"input":[]}`), nil))
	require.False(t, openAIRawPayloadHasInvalidEncryptedContent([]byte(`{"model":"gpt"}`), invalid))
	require.False(t, openAIRawPayloadHasInvalidEncryptedContent([]byte(`{"input":"hello"}`), invalid))
	require.False(t, openAIRawPayloadHasInvalidEncryptedContent([]byte(`{"input":[{"type":"reasoning","encrypted_content":"good"},{"type":"reasoning","encrypted_content":""}]}`), invalid))
	require.True(t, openAIRawPayloadHasInvalidEncryptedContent([]byte(`{"input":{"type":"reasoning","encrypted_content":"bad"}}`), invalid))
	require.True(t, openAIRawPayloadHasInvalidEncryptedContent([]byte(`{"input":[{"type":"message"},{"type":"reasoning","encrypted_content":"bad"}]}`), invalid))
}

func TestStripOpenAIInvalidEncryptedContentFromReplayItemsPaths(t *testing.T) {
	invalid := lineageInvalidSet("bad")

	out, n := stripOpenAIInvalidEncryptedContentFromReplayItems(nil, invalid)
	require.Nil(t, out)
	require.Zero(t, n)

	// Only an unsupported item type hits: nothing is stripped, original slice is returned.
	alienOnly := []json.RawMessage{json.RawMessage(`{"type":"message","encrypted_content":"bad"}`)}
	out, n = stripOpenAIInvalidEncryptedContentFromReplayItems(alienOnly, invalid)
	require.Zero(t, n)
	require.Equal(t, alienOnly, out)

	items := []json.RawMessage{
		json.RawMessage(`{"broken"`),
		json.RawMessage(`{"type":"message","id":"m1"}`),
		json.RawMessage(`{"type":"reasoning","encrypted_content":"good"}`),
		json.RawMessage(`{"type":"message","encrypted_content":"bad"}`),
		json.RawMessage(`{"type":"compaction","encrypted_content":"bad"}`),
		json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"bad"}`),
	}
	original := string(items[5])
	out, n = stripOpenAIInvalidEncryptedContentFromReplayItems(items, invalid)
	require.Equal(t, 2, n)
	require.Len(t, out, 5)
	require.Equal(t, `{"broken"`, string(out[0]))
	require.Equal(t, "rs_1", gjson.GetBytes(out[4], "id").String())
	require.False(t, gjson.GetBytes(out[4], "encrypted_content").Exists())
	require.Equal(t, original, string(items[5]), "replay bodies must not be mutated in place")
}

func TestStripOpenAIInvalidEncryptedContentRawPaths(t *testing.T) {
	invalid := lineageInvalidSet("bad")

	broken := []byte(`{"input":[{"type":"reasoning","encrypted_content":"bad"}],}`)
	out, n, err := stripOpenAIInvalidEncryptedContentRaw(broken, invalid)
	require.Error(t, err)
	require.Zero(t, n)
	require.Equal(t, broken, out)

	alien := []byte(`{"input":[{"type":"message","encrypted_content":"bad"}]}`)
	out, n, err = stripOpenAIInvalidEncryptedContentRaw(alien, invalid)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, alien, out)

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	out, n = svc.stripSessionInvalidEncryptedContentLogged(broken, invalid, "test_strip", 1, 2)
	require.Zero(t, n)
	require.Equal(t, broken, out)

	hit := []byte(`{"input":[{"type":"compaction","encrypted_content":"bad"},{"type":"message","id":"m1"}]}`)
	out, n = svc.stripSessionInvalidEncryptedContentLogged(hit, invalid, "test_strip", 1, 2)
	require.Equal(t, 1, n)
	require.Equal(t, int64(1), gjson.GetBytes(out, "input.#").Int())
}

func TestOpenAIWSInvalidEncryptedContentLineageServicePaths(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var nilSvc *OpenAIGatewayService
	nilSvc.markOpenAIWSInvalidEncryptedContentLineage(1, "sess", []string{"d"})
	require.Nil(t, nilSvc.sessionInvalidEncryptedContentDigests(1, "sess"))

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.markOpenAIWSInvalidEncryptedContentLineage(1, "  ", []string{"d"})
	svc.markOpenAIWSInvalidEncryptedContentLineage(1, "sess", nil)
	require.False(t, svc.getOpenAIWSStateStore().HasAnySessionInvalidEncryptedContent())
	require.Nil(t, svc.sessionInvalidEncryptedContentDigests(1, ""))
	require.Nil(t, svc.sessionInvalidEncryptedContentDigests(1, "sess"))

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)

	// Payload without lineage-covered ciphers records nothing.
	svc.markOpenAIWSInvalidEncryptedContentLineageFromPayload(c, []byte(`{"input":[{"type":"message"}]}`), "test_mark", 1, 1)
	require.False(t, svc.getOpenAIWSStateStore().HasAnySessionInvalidEncryptedContent())

	// Without the ingress context key the session hash is derived from the body.
	body := []byte(`{"model":"gpt","input":[{"type":"reasoning","encrypted_content":"bad"}]}`)
	require.Equal(t, svc.GenerateSessionHash(c, body), svc.openAIWSLineageSessionHashFromContext(c, body))
	require.Equal(t, "", svc.openAIWSLineageSessionHashFromContext(nil, body))

	// The ingress context key wins and is used as the lineage key.
	c.Set(openAIWSIngressSessionHashContextKey, "ingress-session")
	require.Equal(t, "ingress-session", svc.openAIWSLineageSessionHashFromContext(c, body))
	svc.markOpenAIWSInvalidEncryptedContentLineageFromPayload(c, body, "test_mark", 1, 1)
	digests := svc.sessionInvalidEncryptedContentDigests(getOpenAIGroupIDFromContext(c), "ingress-session")
	require.Contains(t, digests, openAIEncryptedContentDigest("bad"))
}

func TestCleanupExpiredInvalidEncryptedBindings(t *testing.T) {
	now := time.Now()
	cleanupExpiredInvalidEncryptedBindings(nil, now, 10)

	bindings := map[string]openAIWSInvalidEncryptedBinding{
		"expired": {digests: map[string]struct{}{"a": {}}, expiresAt: now.Add(-time.Minute)},
		"live":    {digests: map[string]struct{}{"b": {}}, expiresAt: now.Add(time.Minute)},
	}
	cleanupExpiredInvalidEncryptedBindings(bindings, now, 0)
	require.Len(t, bindings, 2)

	cleanupExpiredInvalidEncryptedBindings(bindings, now, 1)
	require.GreaterOrEqual(t, len(bindings), 1)

	cleanupExpiredInvalidEncryptedBindings(bindings, now, 10)
	require.Len(t, bindings, 1)
	_, ok := bindings["live"]
	require.True(t, ok)
}
