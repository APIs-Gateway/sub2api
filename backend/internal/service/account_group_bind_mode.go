package service

import (
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// AccountGroupBindMode 描述批量修改账号分组时，对账号已有分组绑定的处理方式。
type AccountGroupBindMode string

const (
	// AccountGroupBindModeAppend 只补充缺少的分组绑定，账号原有的其他分组保持不变。
	AccountGroupBindModeAppend AccountGroupBindMode = "append"
	// AccountGroupBindModeRemove 只移除列出的分组绑定，其余分组保持不变。
	AccountGroupBindModeRemove AccountGroupBindMode = "remove"
	// AccountGroupBindModeReplace 用列出的分组整体替换账号现有的全部分组绑定。
	// 这是旧版批量编辑的行为，也是请求未带 group_mode 时的缺省值。
	AccountGroupBindModeReplace AccountGroupBindMode = "replace"
)

var (
	ErrInvalidAccountGroupMode = infraerrors.BadRequest(
		"INVALID_GROUP_MODE",
		"group_mode must be one of append, remove, replace",
	)
	ErrAccountGroupIDsRequired = infraerrors.BadRequest(
		"GROUP_IDS_REQUIRED",
		"group_ids must contain at least one group when group_mode is append or remove",
	)
)

// ParseAccountGroupBindMode 解析请求里的 group_mode。空字符串按 replace 处理，保持对旧调用方的兼容。
func ParseAccountGroupBindMode(raw string) (AccountGroupBindMode, error) {
	switch mode := AccountGroupBindMode(strings.TrimSpace(raw)); mode {
	case "":
		return AccountGroupBindModeReplace, nil
	case AccountGroupBindModeAppend, AccountGroupBindModeRemove, AccountGroupBindModeReplace:
		return mode, nil
	default:
		return "", ErrInvalidAccountGroupMode
	}
}
