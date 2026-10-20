package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type captureOpenAIAccountScheduler struct {
	requests []OpenAIAccountScheduleRequest
}

func (s *captureOpenAIAccountScheduler) Select(_ context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	s.requests = append(s.requests, req)
	return &AccountSelectionResult{Account: &Account{ID: 1}}, OpenAIAccountScheduleDecision{}, nil
}

func (*captureOpenAIAccountScheduler) ReportResult(int64, bool, *int) {}

func (*captureOpenAIAccountScheduler) ReportSwitch() {}

func (*captureOpenAIAccountScheduler) SnapshotMetrics() OpenAIAccountSchedulerMetricsSnapshot {
	return OpenAIAccountSchedulerMetricsSnapshot{}
}

func newCaptureOpenAISchedulerService(scheduler OpenAIAccountScheduler) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg:              &config.Config{},
		rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("true"),
		openaiScheduler:  scheduler,
	}
}

func TestSelectAccountWithSchedulerForCapabilityPropagatesUpstreamCostSignal(t *testing.T) {
	ctx := context.Background()
	for _, useUpstreamTokenCost := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[useUpstreamTokenCost], func(t *testing.T) {
			capture := &captureOpenAIAccountScheduler{}
			svc := newCaptureOpenAISchedulerService(capture)

			_, _, err := svc.SelectAccountWithSchedulerForCapability(
				ctx,
				nil,
				"",
				"",
				"gpt-5.6",
				nil,
				OpenAIUpstreamTransportAny,
				OpenAIEndpointCapabilityChatCompletions,
				false,
				useUpstreamTokenCost,
			)

			require.NoError(t, err)
			require.Len(t, capture.requests, 1)
			require.Equal(t, useUpstreamTokenCost, capture.requests[0].UseUpstreamTokenCost)
		})
	}
}

func TestSelectAccountWithSchedulerStablePropagatesUpstreamCostSignal(t *testing.T) {
	ctx := context.Background()
	for _, useUpstreamTokenCost := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[useUpstreamTokenCost], func(t *testing.T) {
			capture := &captureOpenAIAccountScheduler{}
			svc := newCaptureOpenAISchedulerService(capture)

			_, _, err := svc.SelectAccountWithSchedulerStable(
				ctx,
				nil,
				nil,
				"",
				"",
				"gpt-5.6",
				nil,
				OpenAIUpstreamTransportAny,
				OpenAIEndpointCapabilityChatCompletions,
				false,
				useUpstreamTokenCost,
				StablePriorityIntent{},
			)

			require.NoError(t, err)
			require.Len(t, capture.requests, 1)
			require.Equal(t, useUpstreamTokenCost, capture.requests[0].UseUpstreamTokenCost)
		})
	}
}
