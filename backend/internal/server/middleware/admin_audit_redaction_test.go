//go:build unit

package middleware

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// A whole Codex auth.json, as an operator pastes it into the session import
// and re-auth forms. Every token in it must be unreachable from the audit
// trail, which a read-scope token is allowed to list.
const codexAuthJSONFixture = `{"OPENAI_API_KEY":null,"tokens":{` +
	`"id_token":"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyLTEifQ.c2lnbmF0dXJlLWlk",` +
	`"access_token":"eyJhbGciOiJSUzI1NiJ9.eyJzY3AiOlsib3BlbmlkIl19.YWNjZXNzLXNpZw",` +
	`"refresh_token":"rt_9f8e7d6c5b4a39281706f5e4d3c2b1a0",` +
	`"account_id":"acct-123"},"last_refresh":"2026-10-01T00:00:00Z"}`

var codexAuthJSONSecrets = []string{
	"eyJhbGciOiJSUzI1NiJ9",
	"eyJzdWIiOiJ1c2VyLTEifQ",
	"c2lnbmF0dXJlLWlk",
	"YWNjZXNzLXNpZw",
	"rt_9f8e7d6c5b4a39281706f5e4d3c2b1a0",
}

// requireNoSecretAnywhere checks the whole stored row, not only the body
// field: path, extra, everything that would be persisted and listed.
func requireNoSecretAnywhere(t *testing.T, entry *service.AuditLog, secrets []string) {
	t.Helper()
	stored, err := json.Marshal(entry)
	require.NoError(t, err)
	for _, secret := range secrets {
		require.NotContains(t, string(stored), secret)
	}
}

func marshalBody(t *testing.T, fields map[string]string) string {
	t.Helper()
	body, err := json.Marshal(fields)
	require.NoError(t, err)
	return string(body)
}

func TestAdminAuditNeverStoresTheBodyOfCredentialRoutes(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, _ := env.mint(t, "ops-bot", service.AdminTokenScopeWrite, nil)

	for _, tc := range []struct{ name, path, field string }{
		{"codex import, content", "/api/v1/admin/accounts/import/codex-session", "content"},
		{"codex import, contents", "/api/v1/admin/accounts/import/codex-session", "contents"},
		{"codex import, innocuous field", "/api/v1/admin/accounts/import/codex-session", "note"},
		{"codex reauth", "/api/v1/admin/accounts/5/reauth/codex-session", "content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := marshalBody(t, map[string]string{tc.field: codexAuthJSONFixture, "update_existing": "true"})
			w := env.do(adminTokenTestRequest{
				method: http.MethodPost, path: tc.path,
				header: map[string]string{"x-api-key": plaintext, "Content-Type": "application/json"},
				body:   body,
			})
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			entries := env.sink.all()
			entry := entries[len(entries)-1]
			require.Empty(t, entry.RequestBody, "the body of a credential route is not stored at all")
			require.Equal(t, true, entry.Extra["body_omitted"])
			require.EqualValues(t, len(body), entry.Extra["body_bytes"])
			requireNoSecretAnywhere(t, entry, codexAuthJSONSecrets)
		})
	}
}

// Defence in depth: on a route that is not on the omitted list, a Codex
// auth.json submitted as a string value is still unreadable in the trail.
func TestAdminAuditRedactsAuthJSONSubmittedAsAString(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, _ := env.mint(t, "ops-bot", service.AdminTokenScopeWrite, nil)
	path := "/api/v1/admin/things/7/notes"
	header := map[string]string{"x-api-key": plaintext, "Content-Type": "application/json"}

	t.Run("under an exact sensitive key (content)", func(t *testing.T) {
		env.do(adminTokenTestRequest{method: http.MethodPost, path: path, header: header,
			body: marshalBody(t, map[string]string{"content": codexAuthJSONFixture, "title": "visible"})})
		entry := env.sink.all()[len(env.sink.all())-1]
		require.Contains(t, entry.RequestBody, `"content":"[REDACTED]"`)
		require.Contains(t, entry.RequestBody, `"title":"visible"`)
		requireNoSecretAnywhere(t, entry, codexAuthJSONSecrets)
	})

	t.Run("under an innocent key: the string is parsed as JSON and redacted by key", func(t *testing.T) {
		env.do(adminTokenTestRequest{method: http.MethodPost, path: path, header: header,
			body: marshalBody(t, map[string]string{"payload": codexAuthJSONFixture})})
		entry := env.sink.all()[len(env.sink.all())-1]
		requireNoSecretAnywhere(t, entry, codexAuthJSONSecrets)
		require.Contains(t, entry.RequestBody, "last_refresh", "fields that are not secret survive")
	})

	t.Run("under an innocent key and not valid JSON: masked by shape", func(t *testing.T) {
		pasted := "tokens -> eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyLTEifQ.c2lnbmF0dXJlLWlk and rt_9f8e7d6c5b4a39281706f5e4d3c2b1a0"
		env.do(adminTokenTestRequest{method: http.MethodPost, path: path, header: header,
			body: marshalBody(t, map[string]string{"remark": pasted})})
		entry := env.sink.all()[len(env.sink.all())-1]
		requireNoSecretAnywhere(t, entry, codexAuthJSONSecrets)
		require.Contains(t, entry.RequestBody, "[REDACTED]")
	})
}

func TestAdminAuditHandlerSuppliedTargetAndExtra(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, _ := env.mint(t, "ops-bot", service.AdminTokenScopeWrite, nil)

	w := env.do(adminTokenTestRequest{method: http.MethodPost, path: "/api/v1/admin/things/7/spawn", header: apiKey(plaintext)})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	entry := env.sink.only(t)
	require.Equal(t, "things", entry.TargetType)
	require.Equal(t, "99", entry.TargetID, "the handler's target replaces the one derived from the route")
	require.Equal(t, "child", entry.Extra["spawned_name"])
}
