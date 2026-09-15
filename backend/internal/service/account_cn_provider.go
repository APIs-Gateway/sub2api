package service

import "strings"

// APIProtocol 是国产（CN）供应商中转账号显式配置的上游 API 协议。
//
// 背景（issue #771 / #804）：upstream 引入了一整套"CN provider adaptive
// protocol"子系统（新增 Platform 枚举值 kimi/zhipu/deepseek、account_mode
// payg/coding、按供应商预置默认端点等），用于把国产模型中转账号作为一等公民
// platform 接入，覆盖真实流量转发、计费、配额、限流等一整条链路。
//
// 本文件只落地这两个 issue 实际需要的最小子集：账号分类
// （IsCNProvider）+ 显式协议选择（GetAPIProtocol），仅用于修复管理员"测试连接"
// 功能的路由 bug。国产供应商账号在 fork 中仍然是 platform=openai 的 APIKey
// 账号（经既有 OpenAI 网关转发），不引入新的 Platform 枚举值，不改变任何真实
// 流量转发、计费、配额或限流路径。若后续要吸收 upstream 完整的自适应协议子系统
// （新 Platform 值、account_mode、按供应商默认端点等），建议单独立项评估，
// 那是一个覆盖面大得多的产品特性。
type APIProtocol string

const (
	// APIProtocolChatCompletions 通过 OpenAI Chat Completions（/v1/chat/completions）探测。
	// 国产供应商账号未显式配置 api_protocol 时的默认值。
	APIProtocolChatCompletions APIProtocol = "chat_completions"
	// APIProtocolAnthropic 通过供应商原生 Anthropic 兼容端点（/v1/messages）探测。
	APIProtocolAnthropic APIProtocol = "anthropic"
	// APIProtocolResponses 通过 OpenAI Responses（/v1/responses）探测。
	APIProtocolResponses APIProtocol = "responses"
	// APIProtocolAdaptive 保留值，当前按 APIProtocolChatCompletions 处理。
	// fork 未实现"按入站请求协议动态选择供应商原生端点"（upstream 的 adaptive
	// 探测/转发逻辑），显式配置为 adaptive 的账号会退回 Chat Completions 探测，
	// 而不是报错或使用未定义行为。
	APIProtocolAdaptive APIProtocol = "adaptive"
)

const (
	// cnProviderCredentialKey 存储于 accounts.credentials["cn_provider"]（bool）。
	cnProviderCredentialKey = "cn_provider"
	// apiProtocolCredentialKey 存储于 accounts.credentials["api_protocol"]（string）。
	apiProtocolCredentialKey = "api_protocol"
)

// IsCNProvider 报告账号是否被显式标记为国产（CN）供应商中转账号。
//
// 仅 platform=openai 的 APIKey 账号可以被标记为国产供应商——这类账号本身就是
// "指向第三方 OpenAI 兼容上游的 Base URL + API Key"接入方式，国产供应商
// （DeepSeek / Kimi / 智谱 GLM 等）正是通过这种方式接入 fork 的既有 OpenAI
// 网关（参见 openai_gateway_chat_completions_raw.go 中已有的 GLM 专属兼容处理）。
// 标记本身不改变账号的真实流量转发路径，只影响管理员手动测试连接时的探测协议。
func (a *Account) IsCNProvider() bool {
	if a == nil || !a.IsOpenAIApiKey() {
		return false
	}
	if a.Credentials == nil {
		return false
	}
	enabled, ok := a.Credentials[cnProviderCredentialKey].(bool)
	return ok && enabled
}

// GetAPIProtocol 返回国产供应商账号显式配置的上游 API 协议。
// 非国产供应商账号，或配置缺失/非法时，返回默认值 APIProtocolChatCompletions。
func (a *Account) GetAPIProtocol() APIProtocol {
	if a == nil || !a.IsCNProvider() {
		return APIProtocolChatCompletions
	}
	switch APIProtocol(strings.TrimSpace(a.GetCredential(apiProtocolCredentialKey))) {
	case APIProtocolAnthropic:
		return APIProtocolAnthropic
	case APIProtocolResponses:
		return APIProtocolResponses
	case APIProtocolAdaptive:
		return APIProtocolAdaptive
	default:
		return APIProtocolChatCompletions
	}
}

// IsAnthropicAPIProtocol 报告国产供应商账号是否显式配置为原生 Anthropic 协议。
func (a *Account) IsAnthropicAPIProtocol() bool {
	return a.GetAPIProtocol() == APIProtocolAnthropic
}

// GetAnthropicProtocolBaseURL 返回国产供应商账号在 api_protocol=anthropic 下的
// 上游 base_url（供应商自己的 Anthropic 兼容端点）。
//
// 要求管理员显式配置 credentials["base_url"]，不回退到官方
// https://api.anthropic.com——避免把供应商的 API Key 发送给 Anthropic 官方服务
// （issue #804 描述的具体 bug）。非国产供应商账号，或未配置为 anthropic 协议时，
// 返回空字符串。
func (a *Account) GetAnthropicProtocolBaseURL() string {
	if a == nil || !a.IsCNProvider() || !a.IsAnthropicAPIProtocol() {
		return ""
	}
	return strings.TrimSpace(a.GetCredential("base_url"))
}
