//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type inflightCaptureRepo struct {
	UsageBillingRepository
	mu        sync.Mutex
	amount    float64
	exclusive bool
	owner     string
	stages    []string
	finishes  []string
	releases  int
	allow     bool
	err       error
}

func (r *inflightCaptureRepo) ReserveBillingInflight(_ context.Context, _ int64, owner string, amount float64, exclusive bool, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owner = owner
	r.amount = amount
	r.exclusive = exclusive
	return r.allow, r.err
}
func (r *inflightCaptureRepo) ResizeBillingInflight(_ context.Context, _ int64, _ string, _ string, amount float64, exclusive bool, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.amount = amount
	r.exclusive = exclusive
	return r.allow, r.err
}
func (r *inflightCaptureRepo) RenewBillingInflight(context.Context, int64, string, time.Duration) (bool, error) {
	return true, nil
}
func (r *inflightCaptureRepo) ReleaseBillingInflight(context.Context, int64, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releases++
	return nil
}
func (r *inflightCaptureRepo) StageBillingInflight(_ context.Context, _ int64, _ string, attempt string, _ *UsageBillingCommand, _ time.Duration) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stages = append(r.stages, attempt)
	return "obligation", nil
}
func (r *inflightCaptureRepo) FinishBillingInflightAttempt(_ context.Context, _ int64, _ string, attempt string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishes = append(r.finishes, attempt)
	return nil
}

func inflightTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	cfg.Billing.InflightReservation.Enabled = true
	cfg.Billing.InflightReservation.TTLSeconds = 900
	return cfg
}

func TestBillingInflight_ImmutableAttemptHandoff(t *testing.T) {
	repo := &inflightCaptureRepo{allow: true}
	cfg := inflightTestConfig()
	ctx := context.Background()
	lease, err := newBillingInflightLease(ctx, repo, cfg, 1, 1, false)
	require.NoError(t, err)
	defer lease.HandlerDone()
	first := lease.attemptID
	parent := WithBillingInflightLease(ctx, lease)
	attachOld, finishOld := AcquireBillingInflightTask(parent)
	require.NoError(t, lease.Resize(ctx, 2, false))
	second := lease.attemptID
	require.NotEqual(t, first, second)
	attachNew, finishNew := AcquireBillingInflightTask(parent)
	lease.HandlerDone() // worker queue starts after both attempt handlers leave
	oldCtx := attachOld(ctx)
	stageBillingInflight(oldCtx, &UsageBillingCommand{BalanceCost: 1})
	CompleteBillingInflightTask(oldCtx)
	finishOld(true)
	require.Equal(t, []string{first}, repo.stages, "late old cost must retain its immutable attempt identity")
	require.Zero(t, repo.releases, "second queued obligation still owns the request")
	newCtx := attachNew(ctx)
	stageBillingInflight(newCtx, &UsageBillingCommand{BalanceCost: 2})
	CompleteBillingInflightTask(newCtx)
	finishNew(true)
	finishNew(true)
	require.Equal(t, []string{first, second}, repo.stages)
	require.Equal(t, 1, repo.releases, "release once only after every queued financial task settles")
	require.ElementsMatch(t, []string{first, second}, repo.finishes)
}

func TestBillingInflight_TaskFailureAndNoChargeLifecycle(t *testing.T) {
	for _, ending := range []string{"success", "drop", "panic", "unknown", "no_charge"} {
		t.Run(ending, func(t *testing.T) {
			repo := &inflightCaptureRepo{allow: true}
			ctx := context.Background()
			lease, err := newBillingInflightLease(ctx, repo, inflightTestConfig(), 1, 1, false)
			require.NoError(t, err)
			parent := WithBillingInflightLease(ctx, lease)
			lease.MarkDispatched()
			switch ending {
			case "success":
				attach, finish := AcquireBillingInflightTask(parent)
				lease.HandlerDone()
				CompleteBillingInflightTask(attach(ctx))
				finish(true)
			case "drop":
				_, finish := AcquireBillingInflightTask(parent)
				lease.HandlerDone()
				finish(false)
			case "panic":
				_, finish := AcquireBillingInflightTask(parent)
				lease.HandlerDone()
				finish(true)
			case "unknown":
				lease.HandlerDone()
			case "no_charge":
				MarkBillingInflightAttemptNoCharge(parent)
				lease.HandlerDone()
			}
			if ending == "success" || ending == "no_charge" {
				require.Equal(t, 1, repo.releases)
			} else {
				require.Zero(t, repo.releases, "missing cost proof must retain bounded TTL")
			}
		})
	}
}

func TestBillingInflight_BusinessDenialAndInfrastructureFailOpen(t *testing.T) {
	for _, denial := range []error{ErrInsufficientBalance, ErrDailyLimitExceeded, ErrWeeklyLimitExceeded, ErrMonthlyLimitExceeded} {
		repo := &inflightCaptureRepo{allow: false, err: denial}
		lease, err := newBillingInflightLease(context.Background(), repo, inflightTestConfig(), 1, 1, false)
		require.Nil(t, lease)
		require.ErrorIs(t, err, denial)
	}
	repo := &inflightCaptureRepo{err: errors.New("PG unavailable")}
	lease, err := newBillingInflightLease(context.Background(), repo, inflightTestConfig(), 1, 1, false)
	require.NoError(t, err)
	require.Nil(t, lease)
}

func TestBillingInflight_EstimatePaidImagesWithFreeText(t *testing.T) {
	cfg := inflightTestConfig()
	repo := &inflightCaptureRepo{allow: true}
	key := &APIKey{User: &User{ID: 1}, GroupID: new(int64), Group: &Group{ID: 2, RateMultiplier: 0, ImageRateIndependent: true, ImageRateMultiplier: 1}}
	*key.GroupID = 2
	svc := &OpenAIGatewayService{cfg: cfg, usageBillingRepo: repo, billingService: NewBillingService(cfg, nil)}
	request := BillingInflightRequest{APIKey: key, Model: "gpt-5", Body: []byte(`{"model":"gpt-5","input":"hello"}`)}
	lease, err := svc.ReserveBillingInflight(context.Background(), request)
	require.NoError(t, err)
	require.Nil(t, lease, "known-free text must not consume funding")
	request.Body = []byte(`{"model":"gpt-5","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1024x1024"}]}`)
	lease, err = svc.ReserveBillingInflight(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, lease)
	defer lease.HandlerDone()
	require.Positive(t, repo.amount, "independent paid image rate must not inherit free text admission")
}

func TestBillingInflight_UnpricedExclusiveAndNilGroup(t *testing.T) {
	cfg := inflightTestConfig()
	repo := &inflightCaptureRepo{allow: true}
	key := &APIKey{User: &User{ID: 1}}
	svc := &OpenAIGatewayService{cfg: cfg, usageBillingRepo: repo, billingService: NewBillingService(cfg, nil)}
	for _, model := range []string{"gpt-5", "unknown-unpriced-1515"} {
		lease, err := svc.ReserveBillingInflight(context.Background(), BillingInflightRequest{APIKey: key, Model: model, Body: []byte(`{"input":"hello"}`)})
		require.NoError(t, err)
		require.NotNil(t, lease)
		if model == "gpt-5" {
			require.Positive(t, repo.amount)
			require.False(t, repo.exclusive)
		} else {
			require.True(t, repo.exclusive, "unknown models cannot bypass admission with a zero estimate")
		}
		lease.HandlerDone()
	}
}
