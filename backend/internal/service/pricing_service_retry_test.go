package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type pricingRemoteClientStub struct {
	hash func(context.Context) (string, error)
	json func(context.Context) ([]byte, error)
}

func (c pricingRemoteClientStub) FetchHashText(ctx context.Context, _ string) (string, error) {
	return c.hash(ctx)
}

func (c pricingRemoteClientStub) FetchPricingJSON(ctx context.Context, _ string) ([]byte, error) {
	return c.json(ctx)
}

func retryPricingService(t *testing.T, client PricingRemoteClient) *PricingService {
	t.Helper()
	return NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			DataDir:   t.TempDir(),
			HashURL:   "https://example.com/hash",
			RemoteURL: "https://example.com/pricing.json",
		},
	}, client)
}

func noPricingRetryDelay(_ context.Context, _ time.Duration) error { return nil }

func TestPricingRemoteRetryTransientAndPermanentErrors(t *testing.T) {
	timeoutError := &net.DNSError{IsTimeout: true}
	tests := []struct {
		name    string
		failure error
		retries bool
	}{
		{"eof", io.EOF, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"reset", syscall.ECONNRESET, true},
		{"refused", syscall.ECONNREFUSED, true},
		{"timeout", timeoutError, true},
		{"HTTP 408", &PricingRemoteHTTPStatusError{StatusCode: 408}, true},
		{"HTTP 429", &PricingRemoteHTTPStatusError{StatusCode: 429}, true},
		{"HTTP 503", &PricingRemoteHTTPStatusError{StatusCode: 503}, true},
		{"HTTP 404", &PricingRemoteHTTPStatusError{StatusCode: 404}, false},
		{"canceled", context.Canceled, false},
		{"permanent proxy wraps EOF", fmt.Errorf("%w: %w", ErrPricingRemoteProxySetup, io.EOF), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			waits := []time.Duration{}
			client := pricingRemoteClientStub{hash: func(context.Context) (string, error) {
				calls++
				return "", tt.failure
			}}
			svc := retryPricingService(t, client)
			_, err := svc.fetchRemoteHashWithContext(context.Background(), pricingPeriodicHashBudget,
				func(_ context.Context, delay time.Duration) error {
					waits = append(waits, delay)
					return nil
				})
			require.ErrorIs(t, err, tt.failure)
			if tt.retries {
				require.Equal(t, 3, calls)
				require.Equal(t, []time.Duration{250 * time.Millisecond, 500 * time.Millisecond}, waits)
			} else {
				require.Equal(t, 1, calls)
				require.Empty(t, waits)
			}
		})
	}
}

func TestPricingDownloadRetriesTransientBodyWithoutChangingSavedData(t *testing.T) {
	hashCalls, jsonCalls := 0, 0
	body := []byte(`{"retry-model":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`)
	client := pricingRemoteClientStub{
		hash: func(context.Context) (string, error) {
			hashCalls++
			return "remote-anchor", nil
		},
		json: func(context.Context) ([]byte, error) {
			jsonCalls++
			if jsonCalls < 3 {
				return nil, io.EOF
			}
			return body, nil
		},
	}
	svc := retryPricingService(t, client)
	require.NoError(t, svc.downloadPricingDataWithContext(context.Background(), noPricingRetryDelay))
	require.Equal(t, 1, hashCalls)
	require.Equal(t, 3, jsonCalls)
	require.Equal(t, "remote-anchor", svc.localHash)
	require.InDelta(t, 0.000001, svc.GetModelPricing("retry-model").InputCostPerToken, 1e-12)
	saved, err := os.ReadFile(filepath.Join(svc.cfg.Pricing.DataDir, "model_pricing.json"))
	require.NoError(t, err)
	require.Equal(t, body, saved)
}

func TestPricingDownloadSharesParentDeadlineAcrossHashAndBody(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	parentDeadline, _ := parent.Deadline()
	var hashDeadline, bodyDeadline time.Time
	client := pricingRemoteClientStub{
		hash: func(ctx context.Context) (string, error) {
			hashDeadline, _ = ctx.Deadline()
			return "remote-anchor", nil
		},
		json: func(ctx context.Context) ([]byte, error) {
			bodyDeadline, _ = ctx.Deadline()
			return []byte(`{"retry-model":{"input_cost_per_token":0.000001}}`), nil
		},
	}
	svc := retryPricingService(t, client)
	require.NoError(t, svc.downloadPricingDataWithContext(parent, noPricingRetryDelay))
	require.WithinDuration(t, time.Now().Add(pricingStartupHashBudget), hashDeadline, time.Second)
	require.Equal(t, parentDeadline, bodyDeadline)
}

func TestPricingDownloadProductionBudgetCoversHashAndBody(t *testing.T) {
	started := time.Now()
	var hashDeadline, bodyDeadline time.Time
	client := pricingRemoteClientStub{
		hash: func(ctx context.Context) (string, error) {
			hashDeadline, _ = ctx.Deadline()
			return "remote-anchor", nil
		},
		json: func(ctx context.Context) ([]byte, error) {
			bodyDeadline, _ = ctx.Deadline()
			return []byte(`{"retry-model":{"input_cost_per_token":0.000001}}`), nil
		},
	}
	svc := retryPricingService(t, client)
	require.NoError(t, svc.downloadPricingData())
	require.Equal(t, bodyDeadline, hashDeadline)
	require.WithinDuration(t, started.Add(pricingDownloadBudget), bodyDeadline, time.Second)
}

func TestPricingHashBudgetsDifferAtStartupAndPeriodicSync(t *testing.T) {
	var deadline time.Time
	client := pricingRemoteClientStub{hash: func(ctx context.Context) (string, error) {
		deadline, _ = ctx.Deadline()
		return "unchanged", nil
	}}
	svc := retryPricingService(t, client)
	svc.localHash = "unchanged"

	started := time.Now()
	_, err := svc.fetchRemoteHash()
	require.NoError(t, err)
	require.WithinDuration(t, started.Add(pricingStartupHashBudget), deadline, time.Second)

	started = time.Now()
	require.NoError(t, svc.syncWithRemote())
	require.WithinDuration(t, started.Add(pricingPeriodicHashBudget), deadline, time.Second)
}

func TestPricingRetryStopsAtParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	calls := 0
	svc := retryPricingService(t, pricingRemoteClientStub{
		hash: func(context.Context) (string, error) {
			calls++
			cancel()
			return "", io.EOF
		},
	})
	_, err := svc.fetchRemoteHashWithContext(parent, pricingPeriodicHashBudget, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, io.EOF) || errors.Is(err, context.Canceled))
	require.Equal(t, 1, calls)
}
