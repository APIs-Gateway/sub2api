package service

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func BenchmarkResponsesNamespaceLazyRebuild(b *testing.B) {
	for _, size := range []int{1 << 20, 8 << 20} {
		message := `{"type":"message","content":"` + strings.Repeat("x", size) + `"}`
		call := `{"type":"function_call","namespace":"files","name":"read"}`
		custom := `{"type":"custom_tool_call","namespace":"files","name":"read"}`
		cases := []struct {
			name       string
			input      string
			keep       bool
			itemType   string
			stripIndex int
		}{
			{"tools_only", message, false, "", -1},
			{"nested_only", `{"content":{"namespace":"nested"}},` + message, false, "", -1},
			{"keep_calls", call + "," + message, true, "", -1},
			{"strip_first", call + "," + message, false, "", 0},
			{"strip_last", message + "," + call, false, "", 1},
			{"oftype_no_match", call + "," + message, false, "custom_tool_call", -1},
			{"oftype_only_match", custom + "," + message + "," + call, false, "function_call", 2},
		}
		for _, tc := range cases {
			b.Run(fmt.Sprintf("%s/%dMiB", tc.name, size>>20), func(b *testing.B) {
				body := []byte(`{"tools":[{"type":"namespace","name":"files","tools":[]}],"input":[` + tc.input + `]}`)
				transform := func() ([]byte, error) {
					if tc.itemType != "" {
						return stripOpenAIResponsesInputNamespacesOfType(body, tc.itemType)
					}
					return stripOpenAIResponsesInputNamespaces(body, tc.keep)
				}
				out, err := transform()
				if err != nil {
					b.Fatal(err)
				}
				if tc.stripIndex < 0 {
					if !bytes.Equal(body, out) {
						b.Fatal("no-change transformation altered the body")
					}
				} else if !gjson.ValidBytes(out) || gjson.GetBytes(out, fmt.Sprintf("input.%d.namespace", tc.stripIndex)).Exists() {
					b.Fatal("target namespace was not removed")
				}
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					out, err = transform()
					if err != nil {
						b.Fatal(err)
					}
					runtime.KeepAlive(out)
				}
			})
		}
	}
}
