//go:build unit

package handler

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 钉钉登录同步会把姓名 / 邮箱 / 部门写进管理员在设置里指定的属性 key。如果管理员把 key 误配成特权属性
// billing_lockfree，用户把钉钉姓名改成 "true" 就能拿到「计费免锁特权」，所以同步写属性时必须跳过这个 key。

type dingtalkAttrDefRepo struct {
	service.UserAttributeDefinitionRepository
	defs map[string]*service.UserAttributeDefinition
}

func (r *dingtalkAttrDefRepo) GetByKey(_ context.Context, key string) (*service.UserAttributeDefinition, error) {
	if d, ok := r.defs[key]; ok {
		return d, nil
	}
	return nil, service.ErrAttributeDefinitionNotFound
}

func (r *dingtalkAttrDefRepo) List(context.Context, bool) ([]service.UserAttributeDefinition, error) {
	out := make([]service.UserAttributeDefinition, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, *d)
	}
	return out, nil
}

type dingtalkAttrValueRepo struct {
	service.UserAttributeValueRepository
	mu      sync.Mutex
	upserts []service.UpdateUserAttributeInput
}

func (r *dingtalkAttrValueRepo) UpsertBatch(_ context.Context, _ int64, values []service.UpdateUserAttributeInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upserts = append(r.upserts, values...)
	return nil
}

func newDingTalkAttrHandler(keys ...string) (*AuthHandler, *dingtalkAttrValueRepo) {
	defs := map[string]*service.UserAttributeDefinition{}
	for i, k := range keys {
		defs[k] = &service.UserAttributeDefinition{ID: int64(i + 1), Key: k, Name: k, Type: service.AttributeTypeText, Enabled: true}
	}
	values := &dingtalkAttrValueRepo{}
	return &AuthHandler{userAttributeService: service.NewUserAttributeService(&dingtalkAttrDefRepo{defs: defs}, values)}, values
}

func TestDingTalkSyncNeverWritesBillingLockFreeAttribute(t *testing.T) {
	for _, key := range []string{
		service.BillingLockFreeAttributeKey, "BILLING_LOCKFREE", "Billing_LockFree", "  billing_lockfree  ", "\tbilling_lockfree\n",
	} {
		t.Run(key, func(t *testing.T) {
			h, values := newDingTalkAttrHandler(service.BillingLockFreeAttributeKey, key)
			require.NoError(t, h.setUserAttributeByKey(context.Background(), 7, key, "true"))
			require.Empty(t, values.upserts, "特权属性不得被同步写入（key=%q）", key)
		})
	}
}

// 其它属性 key 照常写入，不受影响。
func TestDingTalkSyncStillWritesOrdinaryAttributes(t *testing.T) {
	h, values := newDingTalkAttrHandler("dingtalk_name", "dingtalk_dept", "billing_lockfree_note", "xbilling_lockfree")
	for _, key := range []string{"dingtalk_name", "dingtalk_dept", "billing_lockfree_note", "xbilling_lockfree"} {
		require.NoError(t, h.setUserAttributeByKey(context.Background(), 7, key, "value-"+key))
	}
	require.Len(t, values.upserts, 4)
}
