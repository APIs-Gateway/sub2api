//go:build unit

package service

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClientModelViewScrubText(t *testing.T) {
	pro, _ := newClientModelView("deepseek-v4-pro", "deepseek-v4.1-flash", "combo/deepseek-v4-1-flash", "workbuddy/deepseek-v4.1-flash (0.11x)")
	require.Equal(t, "model deepseek-v4-pro returned 429", pro.scrubText("model deepseek-v4.1-flash returned 429")) // 1
	require.Equal(t, "deepseek-v4-pro overloaded", pro.scrubText("combo/deepseek-v4-1-flash overloaded"))           // 2
	require.Equal(t, "deepseek-v4-pro busy", pro.scrubText("workbuddy/deepseek-v4.1-flash (0.11x) busy"))           // 3
	glm, _ := newClientModelView("glm-5.3-flash", "cn:glm-5.3-flash")
	require.Equal(t, "glm-5.3-flash", glm.scrubText("cn:glm-5.3-flash")) // 4
	require.Equal(t, "glm-5.3-flash ok", glm.scrubText("glm-5.3-flash ok"))
	v1017, _ := newClientModelView("deepseek-v4.1-flash-1017", "deepseek-v4.1-flash") // 5
	require.Equal(t, "deepseek-v4.1-flash-1017 ok", v1017.scrubText("deepseek-v4.1-flash-1017 ok"))
	once := v1017.scrubText("use deepseek-v4.1-flash.")
	require.Equal(t, "use deepseek-v4.1-flash-1017.", once)
	require.Equal(t, once, v1017.scrubText(once))
	luna, _ := newClientModelView("gpt-6-luna-x", "gpt-6-luna") // 6
	require.Equal(t, "gpt-6-lunar", luna.scrubText("gpt-6-lunar"))
	none, _ := newClientModelView("glm-5.3-flash") // 7
	require.Equal(t, "anything deepseek-v4.1-flash", none.scrubText("anything deepseek-v4.1-flash"))
	_, ok := newClientModelView("  ")
	require.False(t, ok)
}

func TestClientModelViewScrubTextExtra(t *testing.T) {
	v, _ := newClientModelView("deepseek-v4-pro", "deepseek-v4.1-flash")
	// 不区分大小写、两处命中、左边界
	require.Equal(t, "deepseek-v4-pro and deepseek-v4-pro", v.scrubText("DeepSeek-V4.1-Flash and deepseek_v4_1_flash"))
	require.Equal(t, "xdeepseek-v4.1-flash", v.scrubText("xdeepseek-v4.1-flash"))
	// 右边界：后接 `.字母数字` 或 `-` 不命中，句末点命中
	require.Equal(t, "deepseek-v4.1-flash.x", v.scrubText("deepseek-v4.1-flash.x"))
	require.Equal(t, "deepseek-v4-pro, deepseek-v4-pro.", v.scrubText("deepseek-v4.1-flash, deepseek-v4.1-flash."))
	// 倍率后缀与连续前缀
	require.Equal(t, "a deepseek-v4-pro b", v.scrubText("a us:hub/deepseek-v4.1-flash(1.5x) b"))
	// 冒号前缀只认 1–4 位字母：普通词 `model:` 不吞，`/` 前缀可多段
	require.Equal(t, "model:deepseek-v4-pro", v.scrubText("model:deepseek-v4.1-flash"))
	require.Equal(t, "vendor:deepseek-v4-pro", v.scrubText("vendor:deepseek-v4.1-flash"))
	require.Equal(t, "us-east:deepseek-v4-pro", v.scrubText("us-east:deepseek-v4.1-flash"))
	require.Equal(t, "deepseek-v4-pro", v.scrubText("a/b/c/deepseek-v4.1-flash"))
	require.Equal(t, "deepseek-v4-pro", v.scrubText("cn:deepseek-v4.1-flash"))
	// 倍率后缀不生成匹配器，靠吞并规则处理；裸写的 `0.11x` 不被吞
	require.Equal(t, "deepseek-v4-pro 0.11x", v.scrubText("deepseek-v4.1-flash 0.11x"))
	withMul, _ := newClientModelView("deepseek-v4-pro", "workbuddy/deepseek-v4.1-flash (0.11x)")
	require.Equal(t, "deepseek-v4-pro 0.11x", withMul.scrubText("deepseek-v4.1-flash 0.11x"))
	require.Equal(t, "deepseek-v4-pro busy", withMul.scrubText("deepseek-v4.1-flash (0.11x) busy"))
	// 非法后缀不吞
	require.Equal(t, "deepseek-v4-pro (fast)", v.scrubText("deepseek-v4.1-flash (fast)"))
	// 幂等
	s := v.scrubText("deepseek-v4.1-flash / combo/deepseek-v4-1-flash")
	require.Equal(t, s, v.scrubText(s))
	// 空串与无命中返回原值
	require.Equal(t, "", v.scrubText(""))
	// 仅带前缀才匹配：A 的大小写变体同样视为 A
	glm, _ := newClientModelView("glm-5.3-flash", "cn:GLM-5.3-flash")
	require.Equal(t, "GLM-5.3-flash", glm.scrubText("GLM-5.3-flash"))
	require.Equal(t, "glm-5.3-flash", glm.scrubText("cn:GLM-5.3-flash"))
	// withForeign 不修改原值
	base, _ := newClientModelView("a-b", "x-y")
	ext := base.withForeign("z-w")
	require.Equal(t, "z-w", base.scrubText("z-w"))
	require.Equal(t, "a-b", ext.scrubText("z-w"))
	require.Equal(t, "a-b", ext.scrubText("x-y"))
}

func TestClientModelViewAlignPayload(t *testing.T) {
	v, _ := newClientModelView("deepseek-v4-pro", "deepseek-v4.1-flash", "combo/deepseek-v4-1-flash")
	// 1 模型字段全改，正文和回显不动
	in := `{"type":"response.completed","response":{"model":"combo/deepseek-v4-1-flash","usage":{"quota":{"model":"deepseek-v4.1-flash"}},"metadata":{"x-codex-turn-metadata":"{\"model\":\"deepseek-v4.1-flash\"}"},"output":[{"type":"message","content":[{"type":"output_text","text":"I am deepseek-v4.1-flash"}]}],"instructions":"you are deepseek-v4.1-flash"}}`
	out := v.alignPayload([]byte(in))
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(out, "response.model").String())
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(out, "response.usage.quota.model").String())
	require.Equal(t, "deepseek-v4-pro", gjson.Get(gjson.GetBytes(out, `response.metadata.x-codex-turn-metadata`).String(), "model").String())
	require.Equal(t, "I am deepseek-v4.1-flash", gjson.GetBytes(out, "response.output.0.content.0.text").String())
	require.Equal(t, "you are deepseek-v4.1-flash", gjson.GetBytes(out, "response.instructions").String())
	// 2 错误帧文案
	require.Equal(t,
		`{"type":"error","error":{"message":"model deepseek-v4-pro context length exceeded","code":"context_length_exceeded"}}`,
		string(v.alignPayload([]byte(`{"type":"error","error":{"message":"model deepseek-v4.1-flash context length exceeded","code":"context_length_exceeded"}}`))))
	// 3 faster_model 例外
	fm := `{"x-codex-safety-buffering-faster-model":"gpt-6-luna","model":"deepseek-v4-pro"}`
	require.Equal(t, fm, string(v.alignPayload([]byte(fm))))
	// 4 delta 不扫
	d := []byte(`{"type":"response.output_text.delta","delta":"deepseek-v4.1-flash"}`)
	require.Equal(t, string(d), string(v.alignPayload(d)))
	// 4b 捷径守卫：type 以 .delta 结尾时不做逐字段扫描（msg 不是 skip 键，没有捷径就会被改），只对齐顶层 model
	dm := v.alignPayload([]byte(`{"type":"x.delta","msg":"deepseek-v4.1-flash","model":"other"}`))
	require.Equal(t, "deepseek-v4.1-flash", gjson.GetBytes(dm, "msg").String())
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(dm, "model").String())
	// 4c 对照：同载荷 type 不是 .delta 时 msg 会被文本替换
	nd := v.alignPayload([]byte(`{"type":"x.done","msg":"deepseek-v4.1-flash","model":"other"}`))
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(nd, "msg").String())
	// 5 无改动返回原切片
	same := []byte(`{"model":"deepseek-v4-pro","id":"x"}`)
	got := v.alignPayload(same)
	require.True(t, &got[0] == &same[0])
}

func TestClientModelViewAlignPayloadExtra(t *testing.T) {
	v, _ := newClientModelView("deepseek-v4-pro", "deepseek-v4.1-flash")
	// delta 事件的顶层 model 仍走现有对齐
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(v.alignPayload([]byte(`{"type":"response.created.delta","model":"other"}`)), "model").String())
	// 键名带 . * ? 与数字键、modelId / modelName 后缀、嵌套数组
	in := `{"a.b":{"c*d?":{"model":"x"}},"0":{"modelId":"y","upstream_model_name":"z"},"list":[{"model":"w"},{"msg":"hit deepseek-v4.1-flash"}]}`
	out := v.alignPayload([]byte(in))
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(out, `a\.b.c\*d\?.model`).String())
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(out, `0.modelId`).String())
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(out, `0.upstream_model_name`).String())
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(out, `list.0.model`).String())
	require.Equal(t, "hit deepseek-v4-pro", gjson.GetBytes(out, `list.1.msg`).String())
	// 空 model、非字符串 model 不动
	keep := []byte(`{"model":"","usage":{"model":3}}`)
	require.Equal(t, string(keep), string(v.alignPayload(keep)))
	// 内嵌 JSON 字符串里的 text 子树不改，改了才回写
	nested := `{"meta":"{\"text\":\"deepseek-v4.1-flash\"}"}`
	require.Equal(t, nested, string(v.alignPayload([]byte(nested))))
	// 非 JSON 载荷原样
	require.Equal(t, "not json deepseek-v4.1-flash", string(v.alignPayload([]byte("not json deepseek-v4.1-flash"))))
	// 零值 view 原样
	var zero clientModelView
	require.Equal(t, string(keep), string(zero.alignPayload(keep)))
}

func TestClientModelViewSSE(t *testing.T) {
	v, _ := newClientModelView("deepseek-v4-pro", "deepseek-v4.1-flash")
	line := `data: {"type":"error","error":{"message":"deepseek-v4.1-flash down"}}`
	require.Equal(t, `data: {"type":"error","error":{"message":"deepseek-v4-pro down"}}`, v.alignSSELine(line))
	for _, passthrough := range []string{"", "event: error", ": keepalive", "data: [DONE]", "data:"} {
		require.Equal(t, passthrough, v.alignSSELine(passthrough))
	}
	body := "event: x\n" + line + "\n\ndata: [DONE]\n"
	require.Equal(t, "event: x\ndata: {\"type\":\"error\",\"error\":{\"message\":\"deepseek-v4-pro down\"}}\n\ndata: [DONE]\n", v.alignSSEBody(body))
	require.Equal(t, "plain", v.alignSSEBody("plain"))
}

func TestClientModelViewContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Equal(t, "deepseek-v4.1-flash", scrubClientVisibleModelText(nil, "deepseek-v4.1-flash"))
	require.Equal(t, "deepseek-v4.1-flash", scrubClientVisibleModelText(c, "deepseek-v4.1-flash"))
	plain := []byte(`{"model":"deepseek-v4.1-flash","usage":{"quota":{"model":"deepseek-v4.1-flash"}}}`)
	require.Equal(t, string(alignClientVisibleModel(plain, "deepseek-v4-pro")), string(alignClientVisibleModelFor(c, plain, "deepseek-v4-pro")))
	v, _ := newClientModelView("deepseek-v4-pro", "deepseek-v4.1-flash")
	setClientModelView(c, v)
	noteClientModelViewForeign(c, "combo/deepseek-v4-1-flash")
	require.Equal(t, "deepseek-v4-pro down", scrubClientVisibleModelText(c, "combo/deepseek-v4-1-flash down"))
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(alignClientVisibleModelFor(c, plain, "ignored"), "usage.quota.model").String())
}

func TestClientModelViewContextSSEFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	line := `data: {"model":"deepseek-v4.1-flash","msg":"deepseek-v4.1-flash"}`
	require.Equal(t, alignClientVisibleModelInSSELine(line, "deepseek-v4-pro"), alignClientVisibleModelInSSELineFor(c, line, "deepseek-v4-pro"))
	require.Equal(t, alignClientVisibleModelInSSEBody(line+"\n", "deepseek-v4-pro"), alignClientVisibleModelInSSEBodyFor(c, line+"\n", "deepseek-v4-pro"))
	noteClientModelViewForeign(c, "deepseek-v4.1-flash") // 无 view 时 no-op
	_, ok := clientModelViewFrom(c)
	require.False(t, ok)
	v, _ := newClientModelView("deepseek-v4-pro", "deepseek-v4.1-flash")
	setClientModelView(c, v)
	require.Equal(t, `data: {"model":"deepseek-v4-pro","msg":"deepseek-v4-pro"}`, alignClientVisibleModelInSSELineFor(c, line, "ignored"))
	require.Equal(t, `data: {"model":"deepseek-v4-pro","msg":"deepseek-v4-pro"}`+"\n", alignClientVisibleModelInSSEBodyFor(c, line+"\n", "ignored"))
}

// noteClientModelViewForeign 只收「像模型名」的值：长度 >= 4，且含数字、`-`、`/` 之一。
// 上游回显的 `auto`、`ok` 这类普通词不能被当成外来名，否则会把正文里的同名词误换成 A。
func TestClientModelViewNoteForeignPlausibleOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	v, _ := newClientModelView("deepseek-v4-pro")
	setClientModelView(c, v)

	for _, junk := range []string{"auto", "ok", "", "  ", "default", "gpt"} {
		noteClientModelViewForeign(c, junk)
	}
	require.Equal(t, "auto mode default gpt ok", scrubClientVisibleModelText(c, "auto mode default gpt ok"))

	noteClientModelViewForeign(c, "auto", "o3-x", "vendor/Foo")
	require.Equal(t, "deepseek-v4-pro and deepseek-v4-pro, auto", scrubClientVisibleModelText(c, "o3-x and vendor/Foo, auto"))
	noteClientModelViewForeign(c, "glm5")
	require.Equal(t, "deepseek-v4-pro", scrubClientVisibleModelText(c, "glm5"))
}
