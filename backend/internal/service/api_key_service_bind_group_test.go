//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// burn-down 模型下订阅型分组不再可作为渠道绑定到 API Key：
// canUserBindGroup / canUserBindGroupInternal 对订阅型分组一律返回 false，
// 标准型分组沿用原有的公开 / 专属（AllowedGroups）逻辑。
func TestAPIKeyService_CanBindGroup_SubscriptionAlwaysRejected(t *testing.T) {
	svc := &APIKeyService{}
	ctx := context.Background()

	subGroup := &Group{ID: 10, Status: StatusActive, IsExclusive: false, SubscriptionType: SubscriptionTypeSubscription}
	// 即便专属分组已在 AllowedGroups 中，订阅型也应被拒绝。
	user := &User{ID: 1, AllowedGroups: []int64{10}}

	require.False(t, svc.canUserBindGroup(ctx, user, subGroup), "subscription group must not be bindable")
	require.False(t, svc.canUserBindGroupInternal(user, subGroup), "subscription group must be excluded from available list")
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
