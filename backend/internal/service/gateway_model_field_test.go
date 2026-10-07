//go:build unit

package service

import (
	"errors"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGatewayModelField_ParserAgreement(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		invalid    bool
	}{
		{"single", `{"model":"gpt-5.6-luna"}`, false},
		{"single_escaped", `{"\u006dodel":"gpt-5.6-luna"}`, false},
		{"nested", `{"model":"gpt-5.6-luna","metadata":{"Model":"other","model":"other"}}`, false},
		{"unknown_duplicate", `{"model":"gpt-5.6-luna","x":1,"x":2}`, false},
		{"omitted_for_inheritance", `{"type":"response.create","input":[]}`, false},
		{"equal", `{"model":"a","model":"a"}`, true},
		{"unequal", `{"model":"a","model":"b"}`, true},
		{"escaped", `{"model":"a","\u006dodel":"b"}`, true},
		{"escaped_first", `{"\u006dodel":"a","model":"b"}`, true},
		{"case_alias", `{"model":"a","Model":"b"}`, true},
		{"alias_first", `{"MODEL":"a","model":"b"}`, true},
		{"alias_only", `{"Model":"a"}`, true},
		{"escaped_alias", `{"model":"a","\u004dodel":"b"}`, true},
		{"nonstring_duplicate", `{"model":"a","model":null}`, true},
		{"nonobject", `["model","a"]`, true},
		{"invalid_json", `{"model":"a"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			before := string(body)
			err := ValidateGatewayModelField(body)
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				if tc.name == "single_escaped" {
					require.Equal(t, "gpt-5.6-luna", gjson.GetBytes(body, "model").String())
				}
			}
			require.Equal(t, before, string(body), "validation must not rewrite payload fields")
		})
	}
}

func TestGatewayModelField_WSSessionInheritance(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		invalid    bool
	}{
		{"inherit", `{"type":"response.create","input":[]}`, false},
		{"session_single", `{"type":"session.update","session":{"model":"gpt-6-astra"}}`, false},
		{"session_nested", `{"session":{"model":"a","metadata":{"model":"b","model":"c"}}}`, false},
		{"session_escaped", `{"session":{"\u006dodel":"a"}}`, false},
		{"session_null_control", `{"type":"session.update","session":null}`, false},
		{"duplicate_session", `{"session":{"model":"a"},"session":{"model":"b"}}`, true},
		{"escaped_session", `{"session":{"model":"a"},"\u0073ession":{"model":"b"}}`, true},
		{"session_alias", `{"Session":{"model":"a"}}`, true},
		{"nested_duplicate", `{"session":{"model":"a","model":"b"}}`, true},
		{"nested_alias", `{"session":{"model":"a","MODEL":"b"}}`, true},
		{"nested_escaped_duplicate", `{"session":{"model":"a","\u006dodel":"b"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGatewayWSModelPayload([]byte(tc.body))
			if !tc.invalid {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var rejection *OpenAIWSLocalRejection
			require.True(t, errors.As(err, &rejection), "invalid client input must retain local rejection attribution")
			require.Equal(t, 400, rejection.HTTPStatus)
			var closeErr *OpenAIWSClientCloseError
			require.ErrorAs(t, err, &closeErr)
			require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		})
	}
}
