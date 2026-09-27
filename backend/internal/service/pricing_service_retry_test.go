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

func TestPricingRetryWaitHonorsTimerAndCancellation(t *testing.T) {
	require.NoError(t, waitForPricingRetry(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, waitForPricingRetry(ctx, time.Hour), context.Canceled)
}

func TestPricingRetryStopsBeforeAttemptOrBackoffWhenBudgetEnds(t *testing.T) {
	t.Run("already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		attempts, err := retryPricingRemote(ctx, "hash", noPricingRetryDelay, func(context.Context) error {
			calls++
			return io.EOF
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, attempts)
		require.Zero(t, calls)
	})
	t.Run("deadline shorter than next backoff", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		calls, waits := 0, 0
		attempts, err := retryPricingRemote(ctx, "hash", func(context.Context, time.Duration) error {
			waits++
			return nil
		}, func(context.Context) error {
			calls++
			return io.EOF
		})
		require.ErrorIs(t, err, io.EOF)
		require.Equal(t, 1, attempts)
		require.Equal(t, 1, calls)
		require.Zero(t, waits)
	})
	t.Run("canceled during backoff", func(t *testing.T) {
		calls, waits := 0, 0
		attempts, err := retryPricingRemote(context.Background(), "hash", func(context.Context, time.Duration) error {
			waits++
			return context.Canceled
		}, func(context.Context) error {
			calls++
			return io.EOF
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, attempts)
		require.Equal(t, 1, calls)
		require.Equal(t, 1, waits)
	})
}

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
	require.WithinDuration(t, started.Add(pricingStartupHashBudget), hashDeadline, time.Second)
	require.WithinDuration(t, started.Add(pricingDownloadBudget), bodyDeadline, time.Second)
	require.True(t, hashDeadline.Before(bodyDeadline))
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

func TestPricingStopCancelsActivePeriodicSync(t *testing.T) {
	tests := []struct {
		name string
		hash func(context.Context, chan<- struct{}) (string, error)
		json func(context.Context, chan<- struct{}) ([]byte, error)
	}{
		{
			name: "hash request",
			hash: func(ctx context.Context, started chan<- struct{}) (string, error) {
				started <- struct{}{}
				<-ctx.Done()
				return "", ctx.Err()
			},
		},
		{
			name: "catalog request after changed hash",
			hash: func(context.Context, chan<- struct{}) (string, error) {
				return "changed", nil
			},
			json: func(ctx context.Context, started chan<- struct{}) ([]byte, error) {
				started <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			jsonCalls := 0
			svc := retryPricingService(t, pricingRemoteClientStub{
				hash: func(ctx context.Context) (string, error) { return tt.hash(ctx, started) },
				json: func(ctx context.Context) ([]byte, error) {
					jsonCalls++
					return tt.json(ctx, started)
				},
			})
			svc.localHash = "old"
			svc.wg.Add(1)
			go func() {
				defer svc.wg.Done()
				_ = svc.syncWithRemote()
			}()

			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("periodic request did not start")
			}
			stopped := make(chan struct{})
			go func() {
				svc.Stop()
				close(stopped)
			}()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("Stop must cancel the active remote request")
			}
			if tt.json == nil {
				require.Zero(t, jsonCalls)
			} else {
				require.Equal(t, 1, jsonCalls)
			}
		})
	}
}

func TestPricingPeriodicSyncWithoutHashDownloadsMissingOrStaleCatalog(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "missing"
		if stale {
			name = "stale"
		}
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			if stale {
				path := filepath.Join(dataDir, "model_pricing.json")
				require.NoError(t, os.WriteFile(path, []byte(`{"old":{"input_cost_per_token":0.000001}}`), 0644))
				old := time.Now().Add(-2 * time.Hour)
				require.NoError(t, os.Chtimes(path, old, old))
			}
			jsonCalls := 0
			svc := NewPricingService(&config.Config{
				Pricing: config.PricingConfig{
					DataDir:             dataDir,
					RemoteURL:           "https://example.com/pricing.json",
					UpdateIntervalHours: 1,
				},
			}, pricingRemoteClientStub{
				json: func(context.Context) ([]byte, error) {
					jsonCalls++
					return []byte(`{"new":{"input_cost_per_token":0.000002}}`), nil
				},
			})
			require.NoError(t, svc.syncWithRemote())
			require.Equal(t, 1, jsonCalls)
			require.InDelta(t, 0.000002, svc.GetModelPricing("new").InputCostPerToken, 1e-12)
		})
	}
}
