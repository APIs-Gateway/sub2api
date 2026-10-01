package service

import (
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// AccountIDsMaxLimit 是「按筛选条件取账号 ID 列表」一次最多返回的账号数。
// 超过时直接报错而不是截断：截断会让后台以为选中了全部结果，却只处理了其中一部分。
const AccountIDsMaxLimit = 5000

// AccountIDsLimitExceededReason 是超出上限时返回给前端的错误 reason。
const AccountIDsLimitExceededReason = "ACCOUNT_IDS_LIMIT_EXCEEDED"

// AccountIDList 是一次筛选命中的账号 ID 及其平台、类型汇总。
// Platforms 和 Types 用于批量编辑弹窗判断哪些字段可用，不必再逐个账号取回。
type AccountIDList struct {
	IDs       []int64
	Total     int64
	Platforms []string
	Types     []string
}

// NewAccountIDsLimitExceededError 构造超出上限的错误，total 为筛选命中的实际账号数。
func NewAccountIDsLimitExceededError(total int64) error {
	return infraerrors.BadRequest(
		AccountIDsLimitExceededReason,
		"too many accounts match the current filters; narrow the filters and try again",
	).WithMetadata(map[string]string{
		"total": strconv.FormatInt(total, 10),
		"limit": strconv.Itoa(AccountIDsMaxLimit),
	})
}
