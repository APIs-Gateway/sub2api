//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type inflightFailureRepo struct {
	*inflightCaptureRepo
	stageErr  error
	finishErr error
	renewMu   sync.Mutex
	renewals  int
}

func (r *inflightFailureRepo) RenewBillingInflight(context.Context, int64, string, time.Duration) (bool, error) {
	r.renewMu.Lock()
	defer r.renewMu.Unlock()
	r.renewals++
	switch r.renewals {
	case 1:
		return false, errors.New("temporary PG outage")
	case 2:
		return true, nil
	default:
		return false, nil // expired owner CAS must end renewal, never recreate it
	}
}

func (r *inflightFailureRepo) StageBillingInflight(ctx context.Context, user int64, owner, attempt string, cmd *UsageBillingCommand, ttl time.Duration) (string, error) {
	if r.stageErr != nil {
		return "", r.stageErr
	}
	return r.inflightCaptureRepo.StageBillingInflight(ctx, user, owner, attempt, cmd, ttl)
}

func (r *inflightFailureRepo) FinishBillingInflightAttempt(ctx context.Context, user int64, owner, attempt string) error {
	if r.finishErr != nil {
		return r.finishErr
	}
	return r.inflightCaptureRepo.FinishBillingInflightAttempt(ctx, user, owner, attempt)
}

func TestBillingInflight_RenewalOutageThenExpiredOwnerStops(t *testing.T) {
	repo := &inflightFailureRepo{inflightCaptureRepo: &inflightCaptureRepo{allow: true}}
	lease := &BillingInflightLease{repo: repo, userID: 1, id: "expired-owner", ttl: time.Millisecond, stop: make(chan struct{})}
	done := make(chan struct{})
	go func() { lease.renew(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("renewal must terminate after the owner expires")
	}
	repo.renewMu.Lock()
	require.Equal(t, 3, repo.renewals, "transient failure retries, live CAS continues, expired CAS terminates")
	repo.renewMu.Unlock()
	require.Empty(t, repo.owner, "renewal may not reserve or resurrect an owner")
	// Cancellation ends a queued worker's timer without waiting for the TTL.
	lease.stop = make(chan struct{})
	close(lease.stop)
	lease.renew()
	repo.renewMu.Lock()
	require.Equal(t, 3, repo.renewals)
	repo.renewMu.Unlock()
}

func TestBillingInflight_ResizeFailureKeepsAttemptAndDenial(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		allowed bool
		want    error
	}{
		{"daily", ErrDailyLimitExceeded, false, ErrDailyLimitExceeded},
		{"weekly", ErrWeeklyLimitExceeded, false, ErrWeeklyLimitExceeded},
		{"monthly", ErrMonthlyLimitExceeded, false, ErrMonthlyLimitExceeded},
		{"wallet", ErrInsufficientBalance, false, ErrInsufficientBalance},
		{"capacity", nil, false, ErrInsufficientBalance},
		{"infrastructure", errors.New("database unavailable"), false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &inflightCaptureRepo{allow: true}
			lease, err := newBillingInflightLease(context.Background(), repo, inflightTestConfig(), 1, 0.5, false)
			require.NoError(t, err)
			lease.MarkDispatched()
			original := lease.attemptID
			repo.err, repo.allow = tc.err, tc.allowed
			err = lease.Resize(context.Background(), 1, false)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, original, lease.attemptID, "failed CAS cannot invent a funded new attempt")
			lease.HandlerDone()
			require.Zero(t, repo.releases, "unknown dispatched old attempt retains its bounded lease")
		})
	}
}

func TestBillingInflight_KnownCostStageFailureRetainsUnknownLease(t *testing.T) {
	repo := &inflightFailureRepo{inflightCaptureRepo: &inflightCaptureRepo{allow: true}, stageErr: errors.New("PG unavailable")}
	lease, err := newBillingInflightLease(context.Background(), repo, inflightTestConfig(), 1, 0.5, false)
	require.NoError(t, err)
	lease.MarkDispatched()
	attach, finish := AcquireBillingInflightTask(WithBillingInflightLease(context.Background(), lease))
	ctx := attach(context.Background())
	cmd := &UsageBillingCommand{OfficialCost: 1, RateMultiplier: 2}
	stageBillingInflight(ctx, cmd)
	require.Empty(t, cmd.InflightObligationID, "failed stage cannot claim a persisted actual-cost obligation")
	lease.mu.Lock()
	require.Zero(t, lease.attempts[lease.attemptID].priced)
	lease.mu.Unlock()
	lease.HandlerDone()
	finish(true) // a returned worker without a settlement success marker is unknown
	require.Empty(t, repo.finishes)
	require.Zero(t, repo.releases)
}

func TestBillingInflight_ProofLimitsPreserveDispatchedEstimate(t *testing.T) {
	for _, body := range []string{
		`{"error":{"type":"authentication_error"}} {"extra":true}`,
		`["authentication_error"]`,
		`{"error":{"type":"authentication_error"},"nested":[{"usage":{"input_tokens":10}}]}`,
		`{"error":{"type":"authentication_error"},"nested":[{"safe":1}]}`, // array itself is valid, no refusal conflict
		`{"error":{"type":"authentication_error"},"nested":[`,
		`{"error":{"type":"authentication_error"},"nested":` + strings.Repeat(`{"a":`, 66) + `0` + strings.Repeat(`}`, 66) + `}`,
		`{"error":{"type":"authentication_error"}}`,
	} {
		for _, status := range []int{401, 529} {
			want := status == 401 && (body == `{"error":{"type":"authentication_error"}}` || strings.Contains(body, `"safe":1`))
			require.Equal(t, want, markBillingInflightProviderRefusal(nil, status, []byte(body), nil), body)
		}
	}
	require.False(t, markBillingInflightProviderRefusal(nil, 403, []byte(`{"error":{"type":"overloaded_error"}}`), nil))
	require.True(t, markBillingInflightProviderRefusal(nil, 529, []byte(`{"error":{"type":"overloaded_error"}}`), nil))
	for _, tc := range []struct {
		name, body string
		limit      int64
		incomplete bool
	}{
		{"at_limit", `{"error":{"type":"authentication_error"}}`, 0, true},
		{"truncated", `{"error":{"type":"authentication_error"}} trailing`, 39, true},
		{"complete", `{"error":{"type":"authentication_error"}}`, 100, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.limit == 0 {
				tc.limit = int64(len(tc.body))
			}
			body, err := readBillingInflightErrorBody(strings.NewReader(tc.body), tc.limit)
			if tc.incomplete {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, !tc.incomplete, markBillingInflightProviderRefusal(nil, 401, body, err))
		})
	}
	// A captured prefix replay must still report its original non-EOF error.
	original := errors.New("upstream reset after valid JSON prefix")
	replay := &billingInflightProviderErrorBody{Reader: bytes.NewReader([]byte(`{"error":{"type":"authentication_error"}}`)), readErr: original}
	body, err := io.ReadAll(replay)
	require.ErrorIs(t, err, original)
	require.False(t, markBillingInflightProviderRefusal(nil, 401, body, err))
}

func TestBillingInflight_NonFinitePriceCannotBypassUnknownExclusive(t *testing.T) {
	for _, amount := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.5} {
		repo := &inflightCaptureRepo{allow: true}
		lease, err := applyInflightEstimate(context.Background(), repo, inflightTestConfig(), BillingInflightRequest{APIKey: &APIKey{User: &User{ID: 1}}}, amount, false)
		require.NoError(t, err)
		require.NotNil(t, lease)
		require.Zero(t, repo.amount)
		require.True(t, repo.exclusive, "invalid price must hold unknown funding, not act as known-free")
		lease.HandlerDone()
	}
}

func TestBillingInflight_StableServedEstimateUsesEffectiveGroupWithoutMutatingKey(t *testing.T) {
	cfg := inflightTestConfig()
	zero, price := 0.0, 2.0
	channels := newTestChannelServiceWithCache(t, &channelCache{
		pricingByGroupModel: map[channelModelKey]*ChannelModelPricing{
			{groupID: 1, model: "gpt-5"}: {BillingMode: BillingModePerRequest, PerRequestPrice: &zero},
			{groupID: 2, model: "gpt-5"}: {BillingMode: BillingModePerRequest, PerRequestPrice: &price},
		},
		channelByGroupID:        map[int64]*Channel{1: {ID: 1, Status: StatusActive}, 2: {ID: 2, Status: StatusActive}},
		groupPlatform:           map[int64]string{1: "", 2: ""},
		wildcardByGroupPlatform: map[channelGroupPlatformKey][]*wildcardPricingEntry{}, mappingByGroupModel: map[channelModelKey]string{}, wildcardMappingByGP: map[channelGroupPlatformKey][]*wildcardMappingEntry{}, byID: map[int64]*Channel{},
	})
	repo := &inflightCaptureRepo{allow: true}
	billing := NewBillingService(cfg, nil)
	svc := &OpenAIGatewayService{cfg: cfg, usageBillingRepo: repo, billingService: billing, resolver: NewModelPricingResolver(channels, billing)}
	groupID := int64(1)
	key := &APIKey{User: &User{ID: 1}, GroupID: &groupID, Group: &Group{ID: 1, RateMultiplier: 1}}
	request := BillingInflightRequest{APIKey: key, Model: "gpt-5", Body: []byte(`{"model":"gpt-5","input":"hello"}`)}
	lease, err := svc.ReserveBillingInflight(context.Background(), request)
	require.NoError(t, err)
	require.Nil(t, lease, "original free group needs no funding hold")
	request.StableDecision = &OpenAIAccountScheduleDecision{StableServedGroupID: 2, StableServedRateMultiplier: 3, StableServedImageRateIndependent: true, StableServedImageRateMultiplier: 4, StableServedImagePrice1K: &price, StableServedImagePrice2K: &price, StableServedImagePrice4K: &price}
	lease, err = svc.ReserveBillingInflight(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, lease)
	defer lease.HandlerDone()
	require.InDelta(t, 6, repo.amount, 1e-8, "effective served group price and multiplier must match completion billing")
	require.False(t, repo.exclusive)
	require.Equal(t, int64(1), *key.GroupID)
	require.Equal(t, int64(1), key.Group.ID)
	require.Equal(t, 1.0, key.Group.RateMultiplier)
	require.False(t, key.Group.ImageRateIndependent, "estimate must not mutate the authenticated key used by other requests")
}
