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
