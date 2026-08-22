//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestStripRedundantGrokChatViewImageToolLeavesNonTargetRequestsByteExact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{
			name: "current turn has no inline image",
			body: `{"messages":[{"role":"user","content":"Inspect a local image"}],"tools":[{"type":"function","function":{"name":"view_image"}}]}`,
		},
		{
			name: "inline image is only historical",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]},{"role":"assistant","content":"Done"},{"role":"user","content":"Inspect another local image"}],"tools":[{"type":"function","function":{"name":"view_image"}}]}`,
		},
		{
			name: "view image is explicitly selected",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],"tools":[{"type":"function","function":{"name":"view_image"}}],"tool_choice":{"type":"function","function":{"name":"view_image"}}}`,
		},
		{
			name: "required with view image as the only tool",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],"tools":[{"type":"function","function":{"name":"view_image"}}],"tool_choice":"required"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(tt.body)
			patched, err := stripRedundantGrokChatViewImageTool(body)
			require.NoError(t, err)
			require.Equal(t, body, patched)
		})
	}
}

func TestStripRedundantGrokChatViewImageToolDropsOnlyToolMetadata(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],
		"tools":[{"type":"function","function":{"name":"view_image"}}],
		"tool_choice":"auto",
		"parallel_tool_calls":true
	}`)

	patched, err := stripRedundantGrokChatViewImageTool(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, "tools").Exists())
	require.False(t, gjson.GetBytes(patched, "tool_choice").Exists())
	require.False(t, gjson.GetBytes(patched, "parallel_tool_calls").Exists())
}

func TestStripRedundantGrokChatViewImageToolLeavesNonArrayOrEmptyMessagesAlone(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "messages is not an array", body: `{"messages":"not-an-array"}`},
		{name: "messages is missing", body: `{}`},
		{name: "messages is an empty array", body: `{"messages":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(tt.body)
			patched, err := stripRedundantGrokChatViewImageTool(body)
			require.NoError(t, err)
			require.Equal(t, body, patched)
		})
	}
}

func TestStripRedundantGrokChatViewImageToolLeavesMissingOrNonArrayToolsAlone(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]
	}`)
	patched, err := stripRedundantGrokChatViewImageTool(body)
	require.NoError(t, err)
	require.Equal(t, body, patched)
}

// tool_choice.function.name 为空时回退读顶层 tool_choice.name；这是移植自
// 上游的兼容写法，不同上游/客户端对 function tool_choice 的字段命名不一致。
func TestStripRedundantGrokChatViewImageToolFallsBackToTopLevelToolChoiceName(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],
		"tools":[{"type":"function","function":{"name":"view_image"}}],
		"tool_choice":{"type":"function","name":"view_image"}
	}`)
	patched, err := stripRedundantGrokChatViewImageTool(body)
	require.NoError(t, err)
	require.Equal(t, body, patched)
}

// tool.function.name 为空时回退读顶层 tool.name（同一份兼容逻辑用于 tools 数组里
// 逐项识别 view_image，而不是只用于 tool_choice）。
func TestStripRedundantGrokChatViewImageToolFallsBackToTopLevelToolName(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],
		"tools":[{"type":"function","name":"view_image"},{"type":"function","function":{"name":"shell_command"}}]
	}`)
	patched, err := stripRedundantGrokChatViewImageTool(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, `tools.#(function.name=="view_image")`).Exists())
	require.False(t, gjson.GetBytes(patched, `tools.#(name=="view_image")`).Exists())
	require.Equal(t, "shell_command", gjson.GetBytes(patched, "tools.0.function.name").String())
}

func TestStripRedundantGrokChatViewImageToolNoViewImagePresentLeavesToolsUnchanged(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],
		"tools":[{"type":"function","function":{"name":"shell_command"}}]
	}`)
	patched, err := stripRedundantGrokChatViewImageTool(body)
	require.NoError(t, err)
	require.Equal(t, body, patched)
}

// 与既有的 TestStripRedundantGrokChatViewImageToolDropsOnlyToolMetadata 互补：
// 那个测试覆盖"移除后 tools 为空"的整段删除路径；这里覆盖"移除后仍剩其他
// 工具"的部分改写路径（sjson.SetRawBytes 而不是 DeleteBytes）。
func TestStripRedundantGrokChatViewImageToolKeepsOtherToolsAfterRemoval(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],
		"tools":[
			{"type":"function","function":{"name":"view_image"}},
			{"type":"function","function":{"name":"shell_command"}}
		],
		"tool_choice":"auto"
	}`)
	patched, err := stripRedundantGrokChatViewImageTool(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, `tools.#(function.name=="view_image")`).Exists())
	require.Equal(t, "shell_command", gjson.GetBytes(patched, "tools.0.function.name").String())
	require.Equal(t, "auto", gjson.GetBytes(patched, "tool_choice").String(), "tool_choice 只在 tools 被整段删除时才清理")
}
