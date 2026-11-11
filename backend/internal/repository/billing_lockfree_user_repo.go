package repository

import (
	"context"
	"database/sql"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// billingLockFreeQuerier 是 *sql.DB / *sql.Tx 共有的最小查询接口（测试可传事务）。
type billingLockFreeQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type billingLockFreeUserLister struct {
	db billingLockFreeQuerier
}

// NewBillingLockFreeUserLister 是「计费免锁特权」名单（service.BillingLockFreeUsers）的数据源：
// 读后台「用户属性」里 key = billing_lockfree 的定义下所有用户的值。
func NewBillingLockFreeUserLister(sqlDB *sql.DB) service.BillingLockFreeUserLister {
	return &billingLockFreeUserLister{db: sqlDB}
}

// 定义必须存在、enabled = true、deleted_at IS NULL（与后台 UserAttributeDefinition 的启用 / 软删语义一致）；
// 值是否算 "true" 由 service 层统一判断（strings.TrimSpace + 大小写不敏感），这里只取原始值。
const billingLockFreeAttributeValuesQuery = `
SELECT v.user_id, v.value
FROM user_attribute_values v
JOIN user_attribute_definitions d ON d.id = v.attribute_id
WHERE d.key = $1
  AND d.enabled = TRUE
  AND d.deleted_at IS NULL`

func (r *billingLockFreeUserLister) ListBillingLockFreeAttributeValues(ctx context.Context) ([]service.BillingLockFreeAttributeValue, error) {
	rows, err := r.db.QueryContext(ctx, billingLockFreeAttributeValuesQuery, service.BillingLockFreeAttributeKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []service.BillingLockFreeAttributeValue
	for rows.Next() {
		var userID int64
		var value sql.NullString
		if err := rows.Scan(&userID, &value); err != nil {
			return nil, err
		}
		out = append(out, service.BillingLockFreeAttributeValue{UserID: userID, Value: value.String})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
