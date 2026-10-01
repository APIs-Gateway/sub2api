//go:build unit

package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactAuditBody_ReplacesSensitiveKeysWithPlaceholder(t *testing.T) {
	// One key per word of the spec: password, secret, token, api_key, apikey,
	// key, credential, cookie, authorization, private.
	raw := `{
		"password": "p1",
		"client_secret": "s1",
		"refresh_token": "t1",
		"api_key": "k1",
		"apikey": "k2",
		"ApiKey": "k3",
		"key": "k4",
		"credentials": {"access_key": "c1", "region": "us"},
		"credential": "c2",
		"Cookie": "ck",
		"Authorization": "Bearer abc",
		"private_key": "pk",
		"name": "visible"
	}`
	out := RedactAuditBody([]byte(raw), "application/json")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed), out)
	for _, key := range []string{
		"password", "client_secret", "refresh_token", "api_key", "apikey", "ApiKey",
		"key", "credentials", "credential", "Cookie", "Authorization", "private_key",
	} {
		require.Equal(t, "[REDACTED]", parsed[key], key)
	}
	require.Equal(t, "visible", parsed["name"])
	for _, secret := range []string{"p1", "s1", "t1", "k1", "k2", "k3", "k4", "c1", "c2", "Bearer abc", `"pk"`, `"us"`} {
		require.NotContains(t, out, secret)
	}
}

func TestRedactAuditBody_RecursesThroughObjectsAndArrays(t *testing.T) {
	raw := `{
		"accounts": [
			{"name": "a", "credentials": {"api_key": "sk-1"}},
			{"name": "b", "extra": [[{"token": "tok-2"}], {"deep": {"password": "pw-3"}}]}
		],
		"list": ["plain", {"secret": "sec-4"}],
		"count": 12345678901234567890
	}`
	out := RedactAuditBody([]byte(raw), "application/json")
	for _, secret := range []string{"sk-1", "tok-2", "pw-3", "sec-4"} {
		require.NotContains(t, out, secret)
	}
	require.Equal(t, 4, strings.Count(out, "[REDACTED]"))
	require.Contains(t, out, `"name":"a"`)
	require.Contains(t, out, `"plain"`)
	require.Contains(t, out, "12345678901234567890", "numbers are kept verbatim")

	// A top-level array is handled too.
	arr := RedactAuditBody([]byte(`[{"password":"x"},{"ok":1}]`), "application/json")
	require.NotContains(t, arr, `"x"`)
	require.Contains(t, arr, `"ok":1`)
}

func TestRedactAuditBody_SniffsJSONWithoutContentType(t *testing.T) {
	out := RedactAuditBody([]byte(`{"password":"hunter2","a":1}`), "")
	require.NotContains(t, out, "hunter2")
	require.Contains(t, out, "[REDACTED]")
}

func TestRedactAuditBody_NonJSONRecordsOnlyLength(t *testing.T) {
	out := RedactAuditBody([]byte("password=hunter2&x=1"), "application/x-www-form-urlencoded")
	require.NotContains(t, out, "hunter2")
	require.Contains(t, out, "20 bytes")

	// Invalid JSON labelled as JSON is not echoed either.
	out = RedactAuditBody([]byte(`{"password": "hunter2"`), "application/json")
	require.NotContains(t, out, "hunter2")
	require.Contains(t, out, "bytes")
}

func TestRedactAuditBody_TruncatesToEightKilobytes(t *testing.T) {
	require.Equal(t, 8*1024, auditRequestBodyMaxBytes)

	raw := `{"note":"` + strings.Repeat("x", 30000) + `","password":"hunter2"}`
	out := RedactAuditBody([]byte(raw), "application/json")
	require.LessOrEqual(t, len(out), 8*1024)
	require.True(t, strings.HasSuffix(out, "...<truncated>"))
	require.NotContains(t, out, "hunter2")

	// A body that fits is stored whole.
	small := RedactAuditBody([]byte(`{"note":"short"}`), "application/json")
	require.Equal(t, `{"note":"short"}`, small)

	// Multi-byte text is cut on a character boundary.
	cjk := RedactAuditBody([]byte(`{"note":"`+strings.Repeat("理", 9000)+`"}`), "application/json")
	require.LessOrEqual(t, len(cjk), 8*1024)
	require.True(t, strings.ToValidUTF8(cjk, "") == cjk, "truncation must not split a rune")
}

const auditTestCodexAuthJSON = `{"OPENAI_API_KEY":null,"tokens":{` +
	`"id_token":"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyLTEifQ.c2lnbmF0dXJlLWlk",` +
	`"access_token":"eyJhbGciOiJSUzI1NiJ9.eyJzY3AiOlsib3BlbmlkIl19.YWNjZXNzLXNpZw",` +
	`"refresh_token":"rt_9f8e7d6c5b4a39281706f5e4d3c2b1a0"},"last_refresh":"2026-10-01T00:00:00Z"}`

func requireNoCodexSecrets(t *testing.T, out string) {
	t.Helper()
	for _, secret := range []string{
		"eyJhbGciOiJSUzI1NiJ9", "eyJzdWIiOiJ1c2VyLTEifQ", "c2lnbmF0dXJlLWlk", "YWNjZXNzLXNpZw",
		"rt_9f8e7d6c5b4a39281706f5e4d3c2b1a0",
	} {
		require.NotContains(t, out, secret)
	}
}

func marshalAuditTestBody(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

func TestRedactAuditBody_ContentAndContentsAreSensitiveKeys(t *testing.T) {
	out := RedactAuditBody(marshalAuditTestBody(t, map[string]any{
		"content":  auditTestCodexAuthJSON,
		"contents": []string{auditTestCodexAuthJSON},
		"title":    "visible",
	}), "application/json")
	requireNoCodexSecrets(t, out)
	require.Contains(t, out, `"content":"[REDACTED]"`)
	require.Contains(t, out, `"contents":"[REDACTED]"`)
	require.Contains(t, out, `"title":"visible"`)
}

func TestRedactAuditBody_AuthJSONAsAStringUnderAnInnocentKey(t *testing.T) {
	out := RedactAuditBody(marshalAuditTestBody(t, map[string]any{
		"payload": auditTestCodexAuthJSON,
		"items":   []string{auditTestCodexAuthJSON},
	}), "application/json")
	requireNoCodexSecrets(t, out)
	require.Contains(t, out, "last_refresh", "the string was parsed and only its secrets were removed")
	require.Contains(t, out, "[REDACTED]")

	// A top-level JSON string works too.
	out = RedactAuditBody(marshalAuditTestBody(t, auditTestCodexAuthJSON), "application/json")
	requireNoCodexSecrets(t, out)

	// JSON inside JSON inside JSON.
	nested := string(marshalAuditTestBody(t, map[string]any{"inner": auditTestCodexAuthJSON}))
	out = RedactAuditBody(marshalAuditTestBody(t, map[string]any{"outer": nested}), "application/json")
	requireNoCodexSecrets(t, out)
}

func TestRedactAuditBody_MasksCredentialShapedValues(t *testing.T) {
	cases := map[string]string{
		"jwt":           "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyLTEifQ.c2lnbmF0dXJlLWlk",
		"refresh token": "rt_9f8e7d6c5b4a39281706f5e4d3c2b1a0",
		"openai key":    "sk-proj-abcdefghijklmnop",
		"anthropic key": "sk-ant-api03-abcdefghijklmnop",
		"admin token":   "s2a_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"legacy key":    "admin-0123456789abcdef0123456789abcdef",
	}
	for name, secret := range cases {
		t.Run(name, func(t *testing.T) {
			out := RedactAuditBody(marshalAuditTestBody(t, map[string]any{
				"remark": "pasted: " + secret + " (end)",
				"list":   []string{secret},
			}), "application/json")
			require.NotContains(t, out, secret)
			require.Contains(t, out, "pasted: [REDACTED] (end)")
		})
	}

	// Ordinary words that merely resemble a prefix are left alone.
	plain := RedactAuditBody([]byte(`{"a":"start_of_the_story","b":"task-force-assignments","c":"short sk-1"}`), "application/json")
	require.Contains(t, plain, "start_of_the_story")
	require.Contains(t, plain, "task-force-assignments")
	require.Contains(t, plain, "short sk-1")
}

func TestRedactAuditBody_SafeKeyNamesAreKeptButSecretsStayRedacted(t *testing.T) {
	out := RedactAuditBody([]byte(`{
		"api_key_id": 12,
		"group_key": "pro",
		"key_prefix": "sk-abcd",
		"public_key": "ssh-ed25519 AAAA",
		"user_key_ids": [1,2],
		"api_key": "k1",
		"key": "k2",
		"secret_key_id": "s1",
		"token_key_prefix": "t1"
	}`), "application/json")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed), out)

	require.EqualValues(t, 12, parsed["api_key_id"])
	require.Equal(t, "pro", parsed["group_key"])
	require.Equal(t, "sk-abcd", parsed["key_prefix"])
	// public_key is on the payment providers' sensitive config list
	// (Alipay publicKey), which takes precedence over the safe-name list, so it
	// stays redacted: over-redacting is the intended failure mode here.
	require.Equal(t, "[REDACTED]", parsed["public_key"])
	require.NotEqual(t, "[REDACTED]", parsed["user_key_ids"])
	for _, key := range []string{"api_key", "key", "secret_key_id", "token_key_prefix"} {
		require.Equal(t, "[REDACTED]", parsed[key], key)
	}
}
