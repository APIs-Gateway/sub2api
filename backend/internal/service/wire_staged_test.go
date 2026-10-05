//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStagedPolicy_ProvideInjectsOneInstanceEverywhere(t *testing.T) {
	for _, withRecorder := range []bool{false, true} {
		gateway := &GatewayService{}
		openAI := &OpenAIGatewayService{}
		resolver := &ModelPricingResolver{}
		derive := NewPricingDerivationService(nil, nil, nil)
		var recorder *PricingShadowRecorder
		if withRecorder {
			recorder = NewPricingShadowRecorder(&shadowStoreFake{})
			t.Cleanup(recorder.Close)
		}

		policy := ProvideStagedGroupPolicy(nil, nil, nil, recorder, gateway, openAI, resolver, derive)
		require.NotNil(t, policy)
		require.Same(t, policy, gateway.groupPolicy())
		require.Same(t, policy, openAI.groupPolicy())
		require.Same(t, policy, resolver.groupPolicy())
		// 派生钩子的失效器必须是同一个 matrixPolicy，否则派生写入之后快照不会立刻刷新。
		require.Same(t, policy.matrix, derive.invalidator)
		if withRecorder {
			require.NotNil(t, policy.hub.sink)
		} else {
			require.Nil(t, policy.hub.sink)
		}
	}
}
