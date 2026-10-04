//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTrimRequestBodyModel_TrimsSurroundingWhitespace(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "leading space", body: `{"model":" gpt-5.6-sol","stream":true}`, want: "gpt-5.6-sol"},
		{name: "trailing space", body: `{"model":"gpt-5.6-sol ","stream":true}`, want: "gpt-5.6-sol"},
		{name: "both sides", body: `{"model":"  gpt-5.6-sol  ","stream":true}`, want: "gpt-5.6-sol"},
		{name: "escaped tab and newline", body: `{"model":"\t gpt-5.6-sol\n","stream":true}`, want: "gpt-5.6-sol"},
		{name: "no-break space", body: "{\"model\":\" gpt-5.6-sol\",\"stream\":true}", want: "gpt-5.6-sol"},
		{name: "ideographic space", body: "{\"model\":\"gpt-5.6-sol　\",\"stream\":true}", want: "gpt-5.6-sol"},
		{name: "keeps inner space", body: `{"model":" my model ","stream":true}`, want: "my model"},
		{name: "whitespace only becomes empty", body: `{"model":"   ","stream":true}`, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := TrimRequestBodyModel([]byte(tt.body))
			require.True(t, gjson.ValidBytes(out))
			require.Equal(t, tt.want, gjson.GetBytes(out, "model").String())
			require.True(t, gjson.GetBytes(out, "stream").Bool(), "其余字段必须原样保留")
		})
	}
}

func TestTrimRequestBodyModel_OnlyReplacesTheModelValue(t *testing.T) {
	body := `{"model":" gpt-5.6-sol ","temperature":0.7,"input":"你好\n","tools":[{"type":"function","model":" nested "}]}`
	want := `{"model":"gpt-5.6-sol","temperature":0.7,"input":"你好\n","tools":[{"type":"function","model":" nested "}]}`

	require.Equal(t, want, string(TrimRequestBodyModel([]byte(body))))
}

func TestTrimRequestBodyModel_KeepsEscapedCharactersInsideModel(t *testing.T) {
	out := TrimRequestBodyModel([]byte(`{"model":" a\"b "}`))

	require.Equal(t, `a"b`, gjson.GetBytes(out, "model").String())
}

func TestTrimRequestBodyModel_ReturnsSameSliceWhenNothingToTrim(t *testing.T) {
	bodies := []string{
		`{"model":"gpt-5.6-sol","stream":true}`,
		`{"model":""}`,
		`{"stream":true}`,
		`{"model":123}`,
		`{"model":null}`,
		`{"input":[{"model":" nested "}]}`,
		`not json`,
	}

	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			in := []byte(body)
			out := TrimRequestBodyModel(in)

			require.Equal(t, body, string(out))
			require.Same(t, &in[0], &out[0], "不需要改写时不应复制请求体")
		})
	}
}
