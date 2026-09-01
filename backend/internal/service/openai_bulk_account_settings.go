package service

import (
	"fmt"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
)

// bulkOpenAISettings 记录一次批量更新请求里显式携带了哪些 OpenAI 专属设置，
// 供 BulkUpdateAccounts 在预取目标账号后做统一的合法性校验。
//
// 注意：上游 #5721 同时还完成了 OpenAI 长上下文计费开关（extra.openai_long_context_billing_enabled）
// 的批量校验与影子账号继承计数，但该特性（含 openAILongContextBillingEnabledKey 常量、
// ValidateOpenAILongContextBillingExtra 校验函数、影子账号 ParentAccountID/IsShadow 概念、
// 175_default_openai_long_context_billing.sql 迁移、admin handler 侧暴露）在本 fork 中完全不存在，
// 涉及 handler/migrations/ent 等 scope 外的改动，本次按 svc-17 §5.4 red line 不吸收，仅落地
// endpoint capabilities 与 responses mode 这两块纯 service 层、自包含的校验补全。
type bulkOpenAISettings struct {
	endpointCapabilities    bool
	responsesMode           bool
	capabilitiesIncludeChat bool
	forcedResponsesMode     bool
}

func (s bulkOpenAISettings) any() bool {
	return s.endpointCapabilities || s.responsesMode
}

// normalizeBulkOpenAISettings 校验并归一化批量更新请求里的 openai_capabilities /
// openai_responses_mode 字段。归一化后的值写回 input，供后续 repoUpdates 构建复用。
//
// 该函数只做与目标账号无关的格式/取值校验；账号平台与类型相关的校验交给
// validateBulkOpenAISettingsTargets（需要先加载目标账号）。
func normalizeBulkOpenAISettings(input *BulkUpdateAccountsInput) (bulkOpenAISettings, error) {
	var settings bulkOpenAISettings
	if input == nil {
		return settings, nil
	}

	if raw, exists := input.Credentials[openAIEndpointCapabilitiesCredentialKey]; exists {
		settings.endpointCapabilities = true
		capabilities, includeChat, err := normalizeBulkOpenAIEndpointCapabilities(raw)
		if err != nil {
			return settings, err
		}
		settings.capabilitiesIncludeChat = includeChat
		input.Credentials[openAIEndpointCapabilitiesCredentialKey] = capabilities
	}

	if raw, exists := input.Extra[openai_compat.ExtraKeyResponsesMode]; exists {
		settings.responsesMode = true
		mode, forced, err := normalizeBulkOpenAIResponsesMode(raw)
		if err != nil {
			return settings, err
		}
		settings.forcedResponsesMode = forced
		input.Extra[openai_compat.ExtraKeyResponsesMode] = mode
	}

	// 同批请求把账号改成仅支持 embeddings 时，强制的 Responses 覆盖模式必然失效，
	// 主动清空以免残留一个无法生效的强制模式。
	if settings.endpointCapabilities && !settings.capabilitiesIncludeChat {
		if settings.forcedResponsesMode {
			return settings, infraerrors.BadRequest(
				"OPENAI_RESPONSES_MODE_INVALID",
				"a forced Responses route requires the chat_completions endpoint capability",
			)
		}
		if input.Extra == nil {
			input.Extra = make(map[string]any, 1)
		}
		input.Extra[openai_compat.ExtraKeyResponsesMode] = nil
		settings.responsesMode = true
	}

	return settings, nil
}

// normalizeBulkOpenAIEndpointCapabilities 校验 openai_capabilities 取值，只允许
// chat_completions/embeddings 或二者组合；nil 表示清除覆盖（恢复默认双能力）。
func normalizeBulkOpenAIEndpointCapabilities(raw any) (any, bool, error) {
	if raw == nil {
		return nil, true, nil
	}

	values := make([]string, 0, 2)
	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return nil, false, invalidBulkOpenAIEndpointCapabilities()
			}
			values = append(values, value)
		}
	case []string:
		values = append(values, typed...)
	default:
		return nil, false, invalidBulkOpenAIEndpointCapabilities()
	}

	selected := make(map[string]bool, 2)
	for _, value := range values {
		switch OpenAIEndpointCapability(value) {
		case OpenAIEndpointCapabilityChatCompletions, OpenAIEndpointCapabilityEmbeddings:
			selected[value] = true
		default:
			return nil, false, invalidBulkOpenAIEndpointCapabilities()
		}
	}
	if len(selected) == 0 {
		return nil, false, invalidBulkOpenAIEndpointCapabilities()
	}

	includeChat := selected[string(OpenAIEndpointCapabilityChatCompletions)]
	if includeChat && selected[string(OpenAIEndpointCapabilityEmbeddings)] {
		return nil, true, nil
	}
	if includeChat {
		return []string{string(OpenAIEndpointCapabilityChatCompletions)}, true, nil
	}
	return []string{string(OpenAIEndpointCapabilityEmbeddings)}, false, nil
}

func invalidBulkOpenAIEndpointCapabilities() error {
	return infraerrors.BadRequest(
		"OPENAI_ENDPOINT_CAPABILITIES_INVALID",
		"openai_capabilities must contain chat_completions, embeddings, or both",
	)
}

// normalizeBulkOpenAIResponsesMode 校验 openai_responses_mode 取值。auto 归一化为
// nil（清除覆盖，跟随自动探测）；force_* 保留原值并标记为“强制模式”。
func normalizeBulkOpenAIResponsesMode(raw any) (any, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	mode, ok := raw.(string)
	if !ok {
		return nil, false, invalidBulkOpenAIResponsesMode()
	}
	switch openai_compat.ResponsesSupportMode(mode) {
	case openai_compat.ResponsesSupportModeAuto:
		return nil, false, nil
	case openai_compat.ResponsesSupportModeForceResponses,
		openai_compat.ResponsesSupportModeForceChatCompletions:
		return mode, true, nil
	default:
		return nil, false, invalidBulkOpenAIResponsesMode()
	}
}

func invalidBulkOpenAIResponsesMode() error {
	return infraerrors.BadRequest(
		"OPENAI_RESPONSES_MODE_INVALID",
		"openai_responses_mode must be auto, force_responses, force_chat_completions, or null",
	)
}

// validateBulkOpenAISettingsTargets 校验批量目标账号是否满足 openai_capabilities /
// openai_responses_mode 的适用条件（必须是 OpenAI API-key 账号），并在写入前拒绝
// 不满足条件的目标，避免部分账号写入成功、部分静默失败。
func validateBulkOpenAISettingsTargets(
	input *BulkUpdateAccountsInput,
	settings bulkOpenAISettings,
	targetsByID map[int64]*Account,
) error {
	if input == nil || !settings.any() {
		return nil
	}

	for _, accountID := range input.AccountIDs {
		account, ok := targetsByID[accountID]
		if !ok || account == nil {
			return invalidBulkOpenAITarget(accountID, "account does not exist")
		}

		if account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey {
			return invalidBulkOpenAITarget(accountID, "endpoint capabilities and Responses routing require an OpenAI API-key account")
		}

		if settings.forcedResponsesMode && !settings.capabilitiesIncludeChat &&
			!settings.endpointCapabilities &&
			!account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions) {
			return invalidBulkOpenAITarget(accountID, "a forced Responses route requires the chat_completions endpoint capability")
		}
	}

	return nil
}

func invalidBulkOpenAITarget(accountID int64, message string) error {
	return infraerrors.BadRequest(
		"OPENAI_BULK_TARGET_INVALID",
		fmt.Sprintf("account %d: %s", accountID, message),
	).WithMetadata(map[string]string{"account_id": strconv.FormatInt(accountID, 10)})
}
