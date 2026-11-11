//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 结算命令里的 BillingLockFree：特权用户为 true，无特权用户与解析器为 nil 时为 false。
// 两个网关（Anthropic 系 / OpenAI）都经 applyUsageBilling -> buildUsageBillingCommand，这里各走一遍真实的 RecordUsage。

func lockFreeCommandKinds() map[string]func(t *testing.T, resolver *BillingLockFreeUsers, userID int64) *UsageBillingCommand {
	return map[string]func(t *testing.T, resolver *BillingLockFreeUsers, userID int64) *UsageBillingCommand{
		"gateway_claude": func(t *testing.T, resolver *BillingLockFreeUsers, userID int64) *UsageBillingCommand {
			repo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			svc.billingLockFree = resolver
			require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result:  &ForwardResult{RequestID: "req-cmd-claude", Usage: ClaudeUsage{InputTokens: 1200, OutputTokens: 600}, Model: "claude-sonnet-4", Duration: time.Second},
				APIKey:  &APIKey{ID: 501, Quota: 100},
				User:    &User{ID: userID},
				Account: &Account{ID: 701},
			}))
			applied := repo.applied()
			require.Len(t, applied, 1)
			require.Equal(t, userID, applied[0].UserID)
			require.Greater(t, applied[0].BalanceCost, 0.0)
			return &applied[0]
		},
		"openai": func(t *testing.T, resolver *BillingLockFreeUsers, userID int64) *UsageBillingCommand {
			repo := &lockFreeSettleRepo{inflightCaptureRepo: inflightCaptureRepo{allow: true}}
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.billingLockFree = resolver
			require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result:  &OpenAIForwardResult{RequestID: "req-cmd-openai", Usage: OpenAIUsage{InputTokens: 1200, OutputTokens: 600}, Model: "gpt-5", Duration: time.Second},
				APIKey:  &APIKey{ID: 501, Quota: 100},
				User:    &User{ID: userID},
				Account: &Account{ID: 701},
			}))
			applied := repo.applied()
			require.Len(t, applied, 1)
			require.Equal(t, userID, applied[0].UserID)
			require.Greater(t, applied[0].BalanceCost, 0.0)
			return &applied[0]
		},
	}
}

func TestUsageBillingCommandBillingLockFreeAssignment(t *testing.T) {
	const userID = int64(601)
	for name, record := range lockFreeCommandKinds() {
		t.Run(name, func(t *testing.T) {
			require.True(t, record(t, lockFreeTestResolver(t, userID), userID).BillingLockFree, "特权用户")
			require.True(t, record(t, lockFreeTestResolver(t, 1, userID, 3), userID).BillingLockFree, "特权用户（名单里有多人）")
			require.False(t, record(t, lockFreeTestResolver(t, 999), userID).BillingLockFree, "名单里是别人")
			require.False(t, record(t, lockFreeTestResolver(t), userID).BillingLockFree, "名单为空")
			require.False(t, record(t, nil, userID).BillingLockFree, "解析器为 nil（依赖没注入）")
			require.False(t, record(t, NewBillingLockFreeUsers(nil), userID).BillingLockFree, "解析器没有数据源")

			// 从未成功加载过名单（fail-closed）。
			neverLoaded := NewBillingLockFreeUsers(&scriptedLockFreeLister{rows: trueRows(userID), err: context.DeadlineExceeded})
			defer neverLoaded.Close()
			require.False(t, record(t, neverLoaded, userID).BillingLockFree, "名单从未加载成功：无特权")
		})
	}
}

// BillingLockFree 不进幂等指纹：同一请求有无特权算出的指纹相同，不会因为名单变化而误报 fingerprint conflict。
func TestUsageBillingCommandBillingLockFreeNotInFingerprint(t *testing.T) {
	a := &UsageBillingCommand{RequestID: "r", APIKeyID: 1, UserID: 2, AccountID: 3, BalanceCost: 1, OfficialCost: 1, RateMultiplier: 1}
	b := *a
	b.BillingLockFree = true
	a.Normalize()
	b.Normalize()
	require.Equal(t, a.RequestFingerprint, b.RequestFingerprint)
}

// 纯构建函数不查名单：默认 false，由 applyUsageBilling 按解析器赋值。
func TestBuildUsageBillingCommandDefaultsBillingLockFreeFalse(t *testing.T) {
	p := &postUsageBillingParams{Cost: &CostBreakdown{ActualCost: 1, TotalCost: 1}, User: &User{ID: 601}, APIKey: &APIKey{ID: 5}, Account: &Account{ID: 7}}
	cmd := buildUsageBillingCommand("req", nil, p)
	require.NotNil(t, cmd)
	require.False(t, cmd.BillingLockFree)
}
