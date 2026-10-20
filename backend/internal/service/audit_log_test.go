package service

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestMaskAuditCredential(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "short", raw: "abc", want: "****"},
		{name: "boundary", raw: "12345678901234", want: "****"},
		{name: "long", raw: "sk-ant-api03-abcdefghijklmnop1234", want: "sk-ant****1234"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := MaskAuditCredential(test.raw)
			if got != test.want {
				t.Fatalf("MaskAuditCredential(%q) = %q, want %q", test.raw, got, test.want)
			}
			if len(test.raw) > 14 && strings.Contains(got, test.raw) {
				t.Fatalf("masked value contains the full credential: %q", got)
			}
		})
	}
}

func TestRedactAuditBody_JSONRedactsCredentialVariants(t *testing.T) {
	value := map[string]any{
		"name":     "payment-channel",
		"base_url": "https://provider.example.com",
		"credentials": map[string]any{
			"api_key":              "sk-secret-123",
			"privateKey":           "private-key-value",
			"service_account_json": map[string]any{"private_key": "nested-secret"},
		},
		"proxy_key":    "http|127.0.0.1|8080|user|password",
		"customKey":    "custom-secret",
		"api-v3-key":   "wx-secret",
		"new_password": "hunter2",
		"totp_code":    "123456",
		"nested":       []any{map[string]any{"access_token": "tok_abc"}},
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	out := RedactAuditBody(raw, "application/json")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("redacted output is not valid JSON: %v\n%s", err, out)
	}

	for _, secret := range []string{
		"sk-secret-123",
		"private-key-value",
		"nested-secret",
		"http|127.0.0.1|8080|user|password",
		"custom-secret",
		"wx-secret",
		"hunter2",
		"123456",
		"tok_abc",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("redacted body still contains %q: %s", secret, out)
		}
	}
	for _, visible := range []string{"provider.example.com", "payment-channel"} {
		if !strings.Contains(out, visible) {
			t.Fatalf("non-sensitive field %q should remain visible: %s", visible, out)
		}
	}
}

func TestRedactAuditBody_NonJSONAndOversizedBodiesAreOmitted(t *testing.T) {
	nonJSON := RedactAuditBody([]byte("username=admin&password=secret"), "application/x-www-form-urlencoded")
	if strings.Contains(nonJSON, "secret") || !strings.Contains(nonJSON, "omitted") {
		t.Fatalf("non-JSON body must be omitted without leaking content: %s", nonJSON)
	}

	oversized := append([]byte(strings.Repeat("x", AuditRequestBodyCaptureLimit)), []byte("secret-token")...)
	omitted := RedactAuditBody(oversized, "application/json")
	if !strings.Contains(omitted, "exceeds") || strings.Contains(omitted, "secret-token") {
		t.Fatalf("oversized body must be omitted without leaking content: %s", omitted)
	}
}

func TestRedactAuditBody_LimitsDepthAndOutputLength(t *testing.T) {
	value := map[string]any{"visible": strings.Repeat("x", auditRequestBodyMaxBytes*2)}
	for depth := 0; depth < auditRedactMaxDepth+2; depth++ {
		value = map[string]any{"nested": value}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	out := RedactAuditBody(raw, "application/json")
	if len(out) > auditRequestBodyMaxBytes {
		t.Fatalf("redacted body length = %d, max %d", len(out), auditRequestBodyMaxBytes)
	}
	if !strings.Contains(out, "depth limit exceeded") && !strings.Contains(out, "truncated") {
		t.Fatalf("expected depth or output limit marker, got %q", out)
	}
}

func TestRedactAuditQuery_ReplacesSensitiveValuesStructurally(t *testing.T) {
	out := RedactAuditQuery("page=2&token=secret-token&api-v3-key=wx-secret&note=visible")
	values, err := url.ParseQuery(out)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("page") != "2" || values.Get("note") != "visible" {
		t.Fatalf("non-sensitive query values changed: %v", values)
	}
	for _, key := range []string{"token", "api-v3-key"} {
		if values.Get(key) != auditRedactedPlaceholder {
			t.Fatalf("query key %q was not redacted: %v", key, values)
		}
	}
}

func TestRedactAuditQuery_MalformedQueryStillRedactsKnownKeys(t *testing.T) {
	out := RedactAuditQuery("api-v3-key=wx-secret%ZZ&note=visible")
	if strings.Contains(out, "wx-secret") {
		t.Fatalf("malformed query leaked sensitive value: %s", out)
	}
}

func TestRedactAuditBody_Empty(t *testing.T) {
	if got := RedactAuditBody(nil, "application/json"); got != "" {
		t.Fatalf("empty body = %q, want empty", got)
	}
}
