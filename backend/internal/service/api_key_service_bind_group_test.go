//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// per-day：分组仅管路由、无「订阅型分组」禁绑/豁免概念。绑定一律按标准 AllowedGroups/IsExclusive
// 逻辑，与历史 subscription_type 取值无关（取代旧「订阅型一律禁绑」契约）。
func TestAPIKeyService_CanBindGroup_IgnoresSubscriptionType(t *testing.T) {
	svc := &APIKeyService{}
	ctx := context.Background()

	// 历史上标了 subscription_type 的非专属分组：per-day 当普通路由组 → 可绑定。
	nonExclusive := &Group{ID: 10, Status: StatusActive, IsExclusive: false, SubscriptionType: SubscriptionTypeSubscription}
	require.True(t, svc.canUserBindGroup(ctx, &User{ID: 1}, nonExclusive), "非专属组应可绑定，与 subscription_type 无关")
	require.True(t, svc.canUserBindGroupInternal(&User{ID: 1}, nonExclusive))

	// 专属分组仍按 AllowedGroups 控制（与 subscription_type 无关）。
	exclusive := &Group{ID: 11, Status: StatusActive, IsExclusive: true, SubscriptionType: SubscriptionTypeSubscription}
	require.False(t, svc.canUserBindGroup(ctx, &User{ID: 1}, exclusive), "专属且未授权 → 禁绑")
	require.True(t, svc.canUserBindGroup(ctx, &User{ID: 1, AllowedGroups: []int64{11}}, exclusive), "专属且已授权 → 可绑")
}

func TestAPIKeyService_CanBindGroup_StandardGroupUnchanged(t *testing.T) {
	svc := &APIKeyService{}
	ctx := context.Background()

	publicGroup := &Group{ID: 1, Status: StatusActive, IsExclusive: false, SubscriptionType: SubscriptionTypeStandard}
	exclusiveAllowed := &Group{ID: 2, Status: StatusActive, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard}
	exclusiveDenied := &Group{ID: 3, Status: StatusActive, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard}

	user := &User{ID: 1, AllowedGroups: []int64{2}}

	require.True(t, svc.canUserBindGroup(ctx, user, publicGroup))
	require.True(t, svc.canUserBindGroupInternal(user, publicGroup))

	require.True(t, svc.canUserBindGroup(ctx, user, exclusiveAllowed))
	require.True(t, svc.canUserBindGroupInternal(user, exclusiveAllowed))

	require.False(t, svc.canUserBindGroup(ctx, user, exclusiveDenied))
	require.False(t, svc.canUserBindGroupInternal(user, exclusiveDenied))
}
