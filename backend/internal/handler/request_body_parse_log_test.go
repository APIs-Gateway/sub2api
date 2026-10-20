//go:build unit

package handler

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newObservedWarnLogger(t *testing.T) (*zap.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.WarnLevel)
	return zap.New(core), logs
}

func parseFailureLoggedFields(t *testing.T, logs *observer.ObservedLogs) map[string]any {
	t.Helper()
	entries := logs.All()
	require.Len(t, entries, 1)

	fields := map[string]any{}
	for _, field := range entries[0].Context {
		switch field.Key {
		case "body_len":
			fields[field.Key] = int(field.Integer)
		case "error":
			fields[field.Key] = field.Interface.(error).Error()
		default:
			fields[field.Key] = field.String
		}
	}
	return fields
}

func TestLogRequestBodyParseFailure_DerivesErrorWhenNil(t *testing.T) {
	log, logs := newObservedWarnLogger(t)
	body := []byte(`{"model": bad}`)

	logRequestBodyParseFailure(log, body, nil)

	fields := parseFailureLoggedFields(t, logs)
	require.Equal(t, len(body), fields["body_len"])
	require.Contains(t, fields["error"], "invalid json")
	require.Contains(t, fields["error"], "offset=11")
}

func TestLogRequestBodyParseFailure_DoesNotLogBodyContent(t *testing.T) {
	log, logs := newObservedWarnLogger(t)
	secret := "sk-super-secret-value"
	body := []byte(`{"api_key":"` + secret + `","broken":`)

	logRequestBodyParseFailure(log, body, nil)

	fields := parseFailureLoggedFields(t, logs)
	require.Equal(t, len(body), fields["body_len"])
	require.NotContains(t, fields, "body_head")
	require.NotContains(t, fields, "body_tail")
	for _, value := range fields {
		if text, ok := value.(string); ok {
			require.NotContains(t, text, secret)
		}
	}
}

func TestLogRequestBodyParseFailure_NilLoggerNoPanic(t *testing.T) {
	require.NotPanics(t, func() {
		logRequestBodyParseFailure(nil, []byte(`{`), nil)
	})
}
