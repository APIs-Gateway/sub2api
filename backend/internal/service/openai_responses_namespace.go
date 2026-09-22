package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIResponsesNamespaceNamesContextKey = "openai_responses_namespace_names"

// shouldFlattenOpenAIResponsesNamespaces 判定原生 Responses 转发前是否摊平
// Codex namespace 工具。WSv2 上游原生支持 namespace，且 WS 出口
// （openai_ws_forwarder_v2）原样转发上游事件、不经 HTTP 回程还原，摊平后的
// 平名无法还原会破坏客户端工具匹配，因此实际走 WSv2 分支的请求保持 namespace
// 原样。透传账号先于 WSv2 分支经 HTTP 转发返回，仍需摊平。
//
// 与 openai_responses_client_tools.go 的 ResponsesClientToolMapping 机制互不
// 重叠：那套只处理 type=apikey 二级中转账号（下游 relay 不认 custom /
// tool_search / namespace 三类工具），这里只处理 OAuth 账号（原生 Responses
// 上游本身不认 namespace 声明）。两者按账号类型互斥触发。
func shouldFlattenOpenAIResponsesNamespaces(account *Account, transport OpenAIUpstreamTransport, passthroughEnabled bool) bool {
	if account == nil || account.Type != AccountTypeOAuth {
		return false
	}
	if transport == OpenAIUpstreamTransportResponsesWebsocketV2 && !passthroughEnabled {
		return false
	}
	return true
}

// shouldStripOpenAIResponsesInputNamespaces removes direct input-item namespace
// fields for HTTP forwarding. Native WSv2 supports them without a round-trip.
func shouldStripOpenAIResponsesInputNamespaces(account *Account, transport OpenAIUpstreamTransport, passthroughEnabled bool) bool {
	if account == nil || (!account.IsOpenAIOAuth() && !account.IsOpenAIApiKey()) {
		return false
	}
	return transport != OpenAIUpstreamTransportResponsesWebsocketV2 || passthroughEnabled
}

// shouldKeepOpenAIResponsesToolCallNamespaces keeps namespace only on historical
// tool calls whose request also declares a real namespace tool. In particular,
// Responses Lite carries those declarations in input.additional_tools rather
// than the top-level tools array.
func shouldKeepOpenAIResponsesToolCallNamespaces(
	account *Account,
	transport OpenAIUpstreamTransport,
	passthroughEnabled bool,
	compactPath bool,
	body []byte,
) bool {
	if account == nil || compactPath {
		return false
	}
	if account.IsOpenAIApiKey() {
		return hasOpenAIResponsesNamespaceToolDeclaration(body)
	}
	if !account.IsOpenAIOAuth() {
		return false
	}
	// This fork still flattens ordinary OAuth namespace declarations. Lite
	// declarations have already moved to input.additional_tools at this point,
	// so that flatten pass has no mapping to rewrite their historical calls.
	return hasOpenAIResponsesNamespaceToolDeclaration(body) ||
		!shouldFlattenOpenAIResponsesNamespaces(account, transport, passthroughEnabled)
}

func hasOpenAIResponsesNamespaceToolDeclaration(body []byte) bool {
	hasNamespaceTool := func(tools gjson.Result) bool {
		if !tools.IsArray() {
			return false
		}
		found := false
		tools.ForEach(func(_, tool gjson.Result) bool {
			if strings.EqualFold(strings.TrimSpace(tool.Get("type").String()), "namespace") {
				found = true
				return false
			}
			return true
		})
		return found
	}
	if hasNamespaceTool(gjson.GetBytes(body, "tools")) {
		return true
	}

	// Responses Lite puts private namespace declarations in an
	// input.additional_tools carrier. Only a namespace declaration is enough to
	// opt in; a regular function that happens to contain a namespace field is not.
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if !strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "additional_tools") {
			return true
		}
		if hasNamespaceTool(item.Get("tools")) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isOpenAIResponsesToolCallItemType(itemType string) bool {
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "function_call", "tool_call", "custom_tool_call", "mcp_tool_call":
		return true
	default:
		return false
	}
}

// stripOpenAIResponsesInputNamespaces removes namespace from direct input items
// while retaining historical tool-call namespace when the selected upstream
// explicitly declared the matching namespace extension.
func stripOpenAIResponsesInputNamespaces(body []byte, keepToolCallNamespaces bool) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"namespace"`)) {
		return body, nil
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}

	var rebuilt bytes.Buffer
	rebuilt.Grow(len(input.Raw))
	_ = rebuilt.WriteByte('[')
	changed := false
	first := true
	var stripErr error
	input.ForEach(func(_, item gjson.Result) bool {
		if !first {
			_ = rebuilt.WriteByte(',')
		}
		first = false
		itemBody := []byte(item.Raw)
		if item.IsObject() && item.Get("namespace").Exists() &&
			(!keepToolCallNamespaces || !isOpenAIResponsesToolCallItemType(item.Get("type").String())) {
			itemBody, stripErr = sjson.DeleteBytes(itemBody, "namespace")
			if stripErr != nil {
				return false
			}
			changed = true
		}
		_, _ = rebuilt.Write(itemBody)
		return true
	})
	_ = rebuilt.WriteByte(']')
	if stripErr != nil {
		return body, fmt.Errorf("delete OpenAI input namespace: %w", stripErr)
	}
	if !changed {
		return body, nil
	}
	stripped, err := sjson.SetRawBytes(body, "input", rebuilt.Bytes())
	if err != nil {
		return body, fmt.Errorf("replace OpenAI input after namespace deletion: %w", err)
	}
	return stripped, nil
}

// flattenOpenAIResponsesNamespaces 摊平请求体里的 namespace 工具声明，并把摊平
// 映射表挂在 gin.Context 上供响应侧还原使用。绝大多数请求不带 namespace 工具，
// bytes.Contains 快速路径避免为它们付出一次全量 Unmarshal + 重新编码。
func flattenOpenAIResponsesNamespaces(c *gin.Context, body []byte) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"namespace"`)) {
		return body, nil
	}
	var requestBody map[string]any
	if err := json.Unmarshal(body, &requestBody); err != nil {
		return body, fmt.Errorf("decode OpenAI namespace body: %w", err)
	}
	names, changed, err := apicompat.FlattenResponsesNamespacesExcept(requestBody, map[string]bool{"image_gen": true})
	if err != nil {
		return body, err
	}
	if !changed {
		return body, nil
	}
	rebuilt, err := marshalOpenAIUpstreamJSON(requestBody)
	if err != nil {
		return body, fmt.Errorf("encode OpenAI namespace body: %w", err)
	}
	setOpenAIResponsesNamespaceNames(c, names)
	return rebuilt, nil
}

func setOpenAIResponsesNamespaceNames(c *gin.Context, names map[string]apicompat.ResponsesNamespaceName) {
	if c != nil && len(names) > 0 {
		c.Set(openAIResponsesNamespaceNamesContextKey, names)
	}
}

func openAIResponsesNamespaceNames(c *gin.Context) map[string]apicompat.ResponsesNamespaceName {
	if c == nil {
		return nil
	}
	value, ok := c.Get(openAIResponsesNamespaceNamesContextKey)
	if !ok {
		return nil
	}
	names, _ := value.(map[string]apicompat.ResponsesNamespaceName)
	return names
}

// restoreOpenAIResponsesNamespacePayload 把响应里摊平后的工具名还原成客户端认识
// 的 namespace/name 形态。没有摊平映射（未命中 flatten，或非 OAuth 账号）时是
// no-op，不产生额外解析开销。
func restoreOpenAIResponsesNamespacePayload(c *gin.Context, payload []byte) ([]byte, error) {
	names := openAIResponsesNamespaceNames(c)
	if len(names) == 0 || !json.Valid(payload) {
		return payload, nil
	}
	restored, changed, err := apicompat.RestoreResponsesNamespaceCalls(payload, names)
	if err != nil {
		return payload, err
	}
	if changed {
		return restored, nil
	}
	return payload, nil
}
