//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 用真 Postgres 验证「计费免锁特权」名单的查询：契约
//   存在 key='billing_lockfree'、enabled=true、deleted_at IS NULL 的定义，
//   且用户在该定义下的值 TrimSpace 后与 "true" 大小写不敏感相等；其余一律无特权。

func lockFreeMustUser(t *testing.T, tag string) int64 {
	t.Helper()
	u := mustCreateUser(t, testEntClient(t), &service.User{
		Email: fmt.Sprintf("lockfree-%s-%d@example.com", tag, time.Now().UnixNano()),
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", u.ID)
	})
	return u.ID
}

// lockFreeDefinition 建一条用户属性定义；deleted 为 true 时软删（deleted_at = NOW()）。
func lockFreeDefinition(t *testing.T, key string, enabled, deleted bool) int64 {
	t.Helper()
	var id int64
	err := integrationDB.QueryRowContext(context.Background(), `
INSERT INTO user_attribute_definitions (key, name, type, enabled, deleted_at)
VALUES ($1, $1, 'text', $2, CASE WHEN $3::boolean THEN NOW() ELSE NULL END)
RETURNING id`, key, enabled, deleted).Scan(&id)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM user_attribute_definitions WHERE id = $1", id)
	})
	return id
}

func lockFreeSetValue(t *testing.T, userID, defID int64, value string) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(),
		"INSERT INTO user_attribute_values (user_id, attribute_id, value) VALUES ($1, $2, $3)", userID, defID, value)
	require.NoError(t, err)
}

func lockFreeLoad(t *testing.T) *service.BillingLockFreeUsers {
	t.Helper()
	r := service.NewBillingLockFreeUsers(NewBillingLockFreeUserLister(integrationDB))
	t.Cleanup(r.Close)
	require.NoError(t, r.Refresh(context.Background()))
	return r
}

func TestBillingLockFreeUserLister_ValueMatching(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"true", "true", true},
		{"upper", "TRUE", true},
		{"mixed", "TrUe", true},
		{"spaces", "  true  ", true},
		{"tab_newline", "\ttrue\n", true},
		{"empty", "", false},
		{"only_spaces", "   ", false},
		{"one", "1", false},
		{"on", "on", false},
		{"yes", "yes", false},
		{"chinese_on", "开启", false},
		{"false", "false", false},
		{"prefix", "truee", false},
		{"suffix", "xtrue", false},
		{"quoted", `"true"`, false},
		{"inner_space", "tr ue", false},
		{"json_array", `["true"]`, false},
	}
	def := lockFreeDefinition(t, service.BillingLockFreeAttributeKey, true, false)
	ids := make([]int64, len(cases))
	for i, tc := range cases {
		ids[i] = lockFreeMustUser(t, "val")
		lockFreeSetValue(t, ids[i], def, tc.value)
	}
	// 没有任何属性值的用户。
	noValue := lockFreeMustUser(t, "noval")

	r := lockFreeLoad(t)
	for i, tc := range cases {
		require.Equal(t, tc.want, r.IsBillingLockFreeUser(ids[i]), "value=%q", tc.value)
	}
	require.False(t, r.IsBillingLockFreeUser(noValue), "没有属性值的用户无特权")
	require.False(t, r.IsBillingLockFreeUser(0))
	require.False(t, r.IsBillingLockFreeUser(-1))
}

func TestBillingLockFreeUserLister_DefinitionStates(t *testing.T) {
	cases := []struct {
		name    string
		key     string // 空表示不建定义
		enabled bool
		deleted bool
		want    bool
	}{
		{"live_enabled", service.BillingLockFreeAttributeKey, true, false, true},
		{"missing_definition", "", false, false, false},
		{"disabled", service.BillingLockFreeAttributeKey, false, false, false},
		{"soft_deleted", service.BillingLockFreeAttributeKey, true, true, false},
		{"disabled_and_soft_deleted", service.BillingLockFreeAttributeKey, false, true, false},
		{"different_key", "billing_lockfree2", true, false, false},
		{"key_case_differs", "BILLING_LOCKFREE", true, false, false},
		{"key_with_prefix", "x_billing_lockfree", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uid := lockFreeMustUser(t, "def")
			if tc.key != "" {
				def := lockFreeDefinition(t, tc.key, tc.enabled, tc.deleted)
				lockFreeSetValue(t, uid, def, "true")
			}
			r := lockFreeLoad(t)
			require.Equal(t, tc.want, r.IsBillingLockFreeUser(uid))
		})
	}
}

// 同一个 key 先被软删又重建：旧定义（已软删）下的值不能带来特权，新定义下的才算。
func TestBillingLockFreeUserLister_SoftDeletedDefinitionDoesNotLeakIntoRecreatedOne(t *testing.T) {
	oldDef := lockFreeDefinition(t, service.BillingLockFreeAttributeKey, true, true)
	newDef := lockFreeDefinition(t, service.BillingLockFreeAttributeKey, true, false)
	viaOld := lockFreeMustUser(t, "old")
	viaNew := lockFreeMustUser(t, "new")
	viaNewButFalse := lockFreeMustUser(t, "newfalse")
	lockFreeSetValue(t, viaOld, oldDef, "true")
	lockFreeSetValue(t, viaNew, newDef, "true")
	lockFreeSetValue(t, viaNewButFalse, newDef, "false")

	r := lockFreeLoad(t)
	require.False(t, r.IsBillingLockFreeUser(viaOld), "旧的已软删定义下的值不算")
	require.True(t, r.IsBillingLockFreeUser(viaNew))
	require.False(t, r.IsBillingLockFreeUser(viaNewButFalse))
}

// 值挂在别的属性定义下（即使值是 true）不算。
func TestBillingLockFreeUserLister_ValueUnderOtherAttributeDoesNotCount(t *testing.T) {
	_ = lockFreeDefinition(t, service.BillingLockFreeAttributeKey, true, false)
	other := lockFreeDefinition(t, "some_other_flag", true, false)
	uid := lockFreeMustUser(t, "other")
	lockFreeSetValue(t, uid, other, "true")

	r := lockFreeLoad(t)
	require.False(t, r.IsBillingLockFreeUser(uid))
}

// 后台改动在下一次刷新后生效：撤销（改值 / 停用 / 软删）后用户失去特权，刷新前仍保持旧名单。
func TestBillingLockFreeUserLister_RevocationTakesEffectOnRefresh(t *testing.T) {
	ctx := context.Background()
	def := lockFreeDefinition(t, service.BillingLockFreeAttributeKey, true, false)
	uid := lockFreeMustUser(t, "revoke")
	lockFreeSetValue(t, uid, def, "true")

	r := lockFreeLoad(t)
	require.True(t, r.IsBillingLockFreeUser(uid))

	_, err := integrationDB.ExecContext(ctx, "UPDATE user_attribute_values SET value = 'false' WHERE user_id = $1", uid)
	require.NoError(t, err)
	require.True(t, r.IsBillingLockFreeUser(uid), "没刷新前是旧名单")
	require.NoError(t, r.Refresh(ctx))
	require.False(t, r.IsBillingLockFreeUser(uid))

	_, err = integrationDB.ExecContext(ctx, "UPDATE user_attribute_values SET value = 'true' WHERE user_id = $1", uid)
	require.NoError(t, err)
	require.NoError(t, r.Refresh(ctx))
	require.True(t, r.IsBillingLockFreeUser(uid))

	_, err = integrationDB.ExecContext(ctx, "UPDATE user_attribute_definitions SET enabled = FALSE WHERE id = $1", def)
	require.NoError(t, err)
	require.NoError(t, r.Refresh(ctx))
	require.False(t, r.IsBillingLockFreeUser(uid), "停用定义后失去特权")

	_, err = integrationDB.ExecContext(ctx, "UPDATE user_attribute_definitions SET enabled = TRUE WHERE id = $1", def)
	require.NoError(t, err)
	require.NoError(t, r.Refresh(ctx))
	require.True(t, r.IsBillingLockFreeUser(uid))

	_, err = integrationDB.ExecContext(ctx, "UPDATE user_attribute_definitions SET deleted_at = NOW() WHERE id = $1", def)
	require.NoError(t, err)
	require.NoError(t, r.Refresh(ctx))
	require.False(t, r.IsBillingLockFreeUser(uid), "软删定义后失去特权")
}
