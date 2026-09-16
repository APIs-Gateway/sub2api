package service

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
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
