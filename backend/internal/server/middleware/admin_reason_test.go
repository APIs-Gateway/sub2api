//go:build unit

package middleware

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestParseAdminReason(t *testing.T) {
	chinese := "补偿故障用户"
	cases := []struct {
		name    string
		reason  string
		encoded string
		want    string
		wantErr bool
	}{
		{name: "absent", want: ""},
		{name: "blank", reason: "   ", want: ""},
		{name: "plain ascii", reason: "refund order 1234", want: "refund order 1234"},
		{name: "raw utf-8", reason: chinese, want: chinese},
		{name: "trimmed", reason: "  keep me  ", want: "keep me"},
		{name: "control characters become spaces", reason: "a\x01b\tc", want: "a b c"},
		{name: "url encoded utf-8", reason: url.QueryEscape(chinese), encoded: "url", want: chinese},
		{name: "url encoded, case and spacing of the marker are forgiven", reason: url.QueryEscape(chinese), encoded: " URL ", want: chinese},
		{name: "url encoded plus is a space", reason: "refund+order+1", encoded: "url", want: "refund order 1"},
		{name: "url encoded %2B is a plus", reason: "a%2Bb+c", encoded: "url", want: "a+b c"},
		{name: "explicit none", reason: chinese, encoded: "none", want: chinese},
		{name: "unsupported encoding", reason: "abcd", encoded: "base64", wantErr: true},
		{name: "broken percent encoding", reason: "100%", encoded: "url", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			if tc.reason != "" {
				header.Set(AdminReasonHeader, tc.reason)
			}
			if tc.encoded != "" {
				header.Set(AdminReasonEncodingHeader, tc.encoded)
			}
			got, err := ParseAdminReason(header)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseAdminReasonLimitsLength(t *testing.T) {
	header := http.Header{}
	header.Set(AdminReasonHeader, strings.Repeat("理", AdminReasonMaxRunes+50))
	got, err := ParseAdminReason(header)
	require.NoError(t, err)
	require.Equal(t, AdminReasonMaxRunes, len([]rune(got)))
}

func TestAdminTokenWritesRequireReason(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, _ := env.mint(t, "ops-bot", service.AdminTokenScopeWrite, nil)
	post := func(header map[string]string) (int, string) {
		w := env.do(adminTokenTestRequest{
			method: http.MethodPost, path: "/api/v1/admin/things",
			header: header, omitReason: true,
		})
		return w.Code, errorCode(t, w)
	}
	with := func(extra map[string]string) map[string]string {
		header := apiKey(plaintext)
		for k, v := range extra {
			header[k] = v
		}
		return header
	}

	t.Run("missing", func(t *testing.T) {
		before := env.reached
		status, code := post(apiKey(plaintext))
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, "ADMIN_REASON_REQUIRED", code)
		require.Equal(t, before, env.reached, "the handler must not run")
	})
	t.Run("blank", func(t *testing.T) {
		status, code := post(with(map[string]string{"X-Reason": "    "}))
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, "ADMIN_REASON_REQUIRED", code)
	})
	t.Run("too short", func(t *testing.T) {
		status, code := post(with(map[string]string{"X-Reason": "abc"}))
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, "ADMIN_REASON_REQUIRED", code)
	})
	t.Run("exactly four characters is enough", func(t *testing.T) {
		status, _ := post(with(map[string]string{"X-Reason": "abcd"}))
		require.Equal(t, http.StatusOK, status)
	})
	t.Run("length counts characters, not bytes", func(t *testing.T) {
		status, _ := post(with(map[string]string{"X-Reason": "补偿用户"}))
		require.Equal(t, http.StatusOK, status)
		status, code := post(with(map[string]string{"X-Reason": "补偿用"}))
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, "ADMIN_REASON_REQUIRED", code)
	})
	t.Run("url encoded", func(t *testing.T) {
		status, _ := post(with(map[string]string{
			"X-Reason":         url.QueryEscape("补偿故障用户"),
			"X-Reason-Encoded": "url",
		}))
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, "补偿故障用户", env.sink.all()[len(env.sink.all())-1].Reason)
	})
	t.Run("invalid encoding", func(t *testing.T) {
		status, code := post(with(map[string]string{"X-Reason": "abcdef", "X-Reason-Encoded": "rot13"}))
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, "ADMIN_REASON_INVALID", code)
	})
	t.Run("a short reason is also rejected before the scope check passes the handler", func(t *testing.T) {
		before := env.reached
		w := env.do(adminTokenTestRequest{
			method: http.MethodPost, path: "/api/v1/admin/things",
			header: with(map[string]string{"X-Reason": "no"}), omitReason: true,
		})
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Equal(t, before, env.reached)
	})
	t.Run("reads never need a reason", func(t *testing.T) {
		w := env.do(adminTokenTestRequest{method: http.MethodGet, path: "/api/v1/admin/things", header: apiKey(plaintext)})
		require.Equal(t, http.StatusOK, w.Code)
	})
}

func TestReasonNotRequiredForJWTAndLegacyKey(t *testing.T) {
	env := newAdminTokenTestEnv(t)

	for name, header := range map[string]map[string]string{
		"jwt":        bearer(env.jwt(t)),
		"legacy key": apiKey(adminTokenTestLegacyKey),
	} {
		t.Run(name, func(t *testing.T) {
			w := env.do(adminTokenTestRequest{
				method: http.MethodPost, path: "/api/v1/admin/things",
				header: header, omitReason: true,
			})
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		})
	}
}
