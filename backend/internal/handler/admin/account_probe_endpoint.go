package admin

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const defaultOpenAIProbeBaseURL = "https://api.openai.com"

// openAIEndpointSnapshot 保存账号连接相关配置的快照，用于判断更新后是否需要重新探测上游能力。
// 仅在内存中比较，绝不写入日志。
type openAIEndpointSnapshot struct {
	baseURL string
	apiKey  string
	proxyID int64 // 0 表示未设置代理
	// capabilityUnknown: 能力标记缺失且未手动设置 openai_responses_mode，需要探测来自愈。
	capabilityUnknown bool
}

func snapshotOpenAIEndpoint(a *service.Account) openAIEndpointSnapshot {
	s := openAIEndpointSnapshot{baseURL: a.GetCredential("base_url"), apiKey: a.GetCredential("api_key")}
	if a.ProxyID != nil {
		s.proxyID = *a.ProxyID
	}
	if a.Extra == nil {
		s.capabilityUnknown = true
		return s
	}
	mode, _ := a.Extra[openai_compat.ExtraKeyResponsesMode].(string)
	s.capabilityUnknown = strings.TrimSpace(mode) == "" &&
		openai_compat.ResolveResponsesSupport(a.Extra) == openai_compat.ResponsesSupportUnknown
	return s
}

func normalizeProbeBaseURL(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if s == "" {
		return defaultOpenAIProbeBaseURL
	}
	return s
}

// endpointChangedForProbe 判断是否需要重新探测：
// base_url（规范化，空值等同官方默认）、api_key、proxy_id 任一变化；
// 或更新前能力标记未知（且未手动设模式）时仍探测以保留自愈。
// 已确定 true/false 的账号只改 model_mapping 等字段时不重探。
func endpointChangedForProbe(before, after openAIEndpointSnapshot) bool {
	if before.capabilityUnknown {
		return true
	}
	if normalizeProbeBaseURL(before.baseURL) != normalizeProbeBaseURL(after.baseURL) {
		return true
	}
	return before.apiKey != after.apiKey || before.proxyID != after.proxyID
}
