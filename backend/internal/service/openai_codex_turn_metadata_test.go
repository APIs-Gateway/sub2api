package service

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexTurnMetadataRewritePreservesHeaderSafeJSON(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "test-account"}}

	rewriters := map[string]func(*testing.T, string) string{
		"account_header": func(t *testing.T, raw string) string {
			h := make(http.Header)
			h.Set(openAIWSTurnMetadataHeader, raw)
			applyCodexAccountIdentityHeaders(h, account, 77)
			return h.Get(openAIWSTurnMetadataHeader)
		},
		"account_body_map": func(t *testing.T, raw string) string {
			cm := map[string]any{openAIWSTurnMetadataHeader: raw}
			require.True(t, applyCodexAccountIdentityClientMetadataMap(map[string]any{"client_metadata": cm}, account, 77))
			value, ok := cm[openAIWSTurnMetadataHeader].(string)
			require.True(t, ok)
			return value
		},
		"account_body_raw": func(t *testing.T, raw string) string {
			body, err := json.Marshal(map[string]any{"client_metadata": map[string]any{openAIWSTurnMetadataHeader: raw}})
			require.NoError(t, err)
			updated, changed, err := applyCodexAccountIdentityClientMetadataRaw(body, account, 77)
			require.NoError(t, err)
			require.True(t, changed)
			return gjson.GetBytes(updated, "client_metadata."+openAIWSTurnMetadataHeader).String()
		},
	}

	for _, raw := range []string{
		`{"installation_id":"client-install","workspaces":{"C:\\work\\中文🚀":{}}}`,
		`{"installation_id":"client-install","workspaces":{"C:\\work\\中文🚀":{"label":"café"}},"literal":"\\u4e2d","quote":"\"\\","controls":"\b\f\n\r\t\u0000\u007f","timestamp":1789302780858,"enabled":true}`,
		`{"installation_id":"client-install","workspaces":{"C:\\work\\\u4e2d\u6587\ud83d\ude80":{"label":"caf\u00e9"}},"literal":"\\u4e2d","quote":"\"\\","controls":"\b\f\n\r\t\u0000\u007f","timestamp":1789302780858,"enabled":true}`,
		`{"installation_id":"client-install","workspaces":{"C:\\work\\ascii":{}},"literal":"\\u4e2d"}`,
	} {
		var original map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &original))
		for name, rewrite := range rewriters {
			t.Run(name, func(t *testing.T) {
				updated := rewrite(t, raw)
				var decoded map[string]any
				require.NoError(t, json.Unmarshal([]byte(updated), &decoded))
				for key, value := range original {
					if key != "installation_id" {
						require.Equal(t, value, decoded[key], key)
					}
				}
				require.NotEqual(t, original["installation_id"], decoded["installation_id"])
				for _, b := range []byte(updated) {
					require.True(t, b >= 0x20 && b < 0x7f, "non-printable ASCII header byte: 0x%02x", b)
				}
			})
		}
	}
}

// Fork regression: pin the exact escape form so BMP runes, astral runes
// (surrogate pairs) and DEL are all emitted as lowercase \uXXXX, and pure
// ASCII output is returned byte-for-byte as json.Marshal produced it.
func TestMarshalCodexTurnMetadataEscapesNonASCII(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{name: "ascii_passthrough", in: map[string]any{"a": "b", "n": 1}, want: `{"a":"b","n":1}`},
		{name: "bmp", in: map[string]any{"k": "中é"}, want: `{"k":"\u4e2d\u00e9"}`},
		{name: "astral_surrogate_pair", in: map[string]any{"k": "🚀"}, want: `{"k":"\ud83d\ude80"}`},
		{name: "del_control", in: map[string]any{"k": "x\u007fy"}, want: `{"k":"x\u007fy"}`},
		{name: "non_ascii_key_and_mixed", in: map[string]any{"中": "a\"b"}, want: `{"\u4e2d":"a\"b"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := marshalCodexTurnMetadata(tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(got))
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(got, &decoded))
			for key, value := range tc.in {
				if s, ok := value.(string); ok {
					require.Equal(t, s, decoded[key])
				}
			}
		})
	}

	_, err := marshalCodexTurnMetadata(map[string]any{"bad": func() {}})
	require.Error(t, err)
}
