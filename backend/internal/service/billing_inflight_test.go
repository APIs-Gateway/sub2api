//go:build unit

package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
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

func TestBillingInflight_ImageModelMatchesCompletionPricingPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, source, mapped, original string
		paid                           bool
	}{
		{"default_image_first", "", "", "gpt-5", true},
		{"upstream_image_first", BillingModelSourceUpstream, "", "gpt-5", true},
		{"requested_explicit_free_chat", BillingModelSourceRequested, "", "gpt-5", false},
		{"channel_explicit_free_chat", BillingModelSourceChannelMapped, "gpt-5", "requested-alias", false},
		{"unchanged_channel_still_image", BillingModelSourceChannelMapped, "gpt-5", "gpt-5", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := inflightTestConfig()
			repo := &inflightCaptureRepo{allow: true}
			zero, paid := 0.0, 0.5
			cs := newTestChannelServiceWithCache(t, &channelCache{
				pricingByGroupModel: map[channelModelKey]*ChannelModelPricing{
					{groupID: 2, model: "gpt-5"}:       {BillingMode: BillingModePerRequest, PerRequestPrice: &zero},
					{groupID: 2, model: "gpt-image-2"}: {BillingMode: BillingModePerRequest, PerRequestPrice: &paid},
				}, channelByGroupID: map[int64]*Channel{2: {ID: 1, Status: StatusActive}}, groupPlatform: map[int64]string{2: ""},
				wildcardByGroupPlatform: map[channelGroupPlatformKey][]*wildcardPricingEntry{}, mappingByGroupModel: map[channelModelKey]string{}, wildcardMappingByGP: map[channelGroupPlatformKey][]*wildcardMappingEntry{}, byID: map[int64]*Channel{},
			})
			billing := NewBillingService(cfg, nil)
			svc := &OpenAIGatewayService{cfg: cfg, usageBillingRepo: repo, billingService: billing, resolver: NewModelPricingResolver(cs, billing)}
			groupID := int64(2)
			key := &APIKey{User: &User{ID: 1}, GroupID: &groupID, Group: &Group{ID: 2, RateMultiplier: 1}}
			lease, err := svc.ReserveBillingInflight(context.Background(), BillingInflightRequest{APIKey: key, Model: "gpt-5", Body: []byte(`{"model":"gpt-5","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2"}]}`), ChannelUsageFields: ChannelUsageFields{BillingModelSource: tc.source, ChannelMappedModel: tc.mapped, OriginalModel: tc.original}})
			require.NoError(t, err)
			if tc.paid {
				require.NotNil(t, lease)
				defer lease.HandlerDone()
				require.InDelta(t, 0.5, repo.amount, 1e-8)
			} else {
				require.Nil(t, lease, "explicit completion billing override is truly free")
			}
		})
	}
}

func TestBillingInflight_NoChargeProofDoesNotInferTransportCost(t *testing.T) {
	require.False(t, IsBillingInflightNoChargeError(&UpstreamFailoverError{StatusCode: 502, RetryableOnSameAccount: true}))
	require.False(t, IsBillingInflightNoChargeError(errors.New("header timeout")))
	require.True(t, IsBillingInflightNoChargeError(&UpstreamFailoverError{StatusCode: 529, BillingNoCharge: true}))
	require.True(t, IsBillingInflightNoChargeError(&GrokContentPolicyRejectionError{message: "safe refusal"}))
}

func TestBillingInflight_ProviderRefusalRequiresCompleteUsageFreeBody(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		readErr    error
		release    bool
	}{
		{"auth", `{"error":{"type":"authentication_error","code":"invalid_api_key"}}`, 401, nil, true},
		{"permission", `{"error":{"type":"permission_error"}}`, 403, nil, true},
		{"gemini_auth", `{"error":{"code":401,"status":"UNAUTHENTICATED","message":"auth"}}`, 401, nil, true},
		{"gemini_permission", `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"auth"}}`, 403, nil, true},
		{"partial_image_output", `{"type":"response.failed","error":{"type":"permission_error"},"output":[{"type":"image_generation_call","result":"partial"}]}`, 403, nil, false},
		{"nested_image", `{"error":{"type":"permission_error"},"nested":{"image_count":1}}`, 403, nil, false},
		{"transport502", `{"error":{"type":"authentication_error"}}`, 502, nil, false},
		{"partial_body", `{"error":{"type":"authentication_error"}}`, 401, errors.New("read reset"), false},
		{"invalid_json", `{"error":{"type":"authentication_error"}`, 401, nil, false},
		{"observed_usage", `{"error":{"type":"authentication_error"},"usage":{"input_tokens":1}}`, 401, nil, false},
		{"escaped_usage", `{"error":{"type":"authentication_error"},"nested":{"\u0075sage":{"input_tokens":1}}}`, 401, nil, false},
		{"duplicate_error", `{"error":{"type":"authentication_error"},"error":{"type":"server_error"}}`, 401, nil, false},
		{"conflicting_top", `{"type":"permission_denied","error":{"type":"server_error"}}`, 403, nil, false},
		{"conflicting_type_code", `{"error":{"type":"server_error","code":"invalid_api_key"}}`, 401, nil, false},
		{"unknown403", `{"error":{"type":"unknown"}}`, 403, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &inflightCaptureRepo{allow: true}
			lease, err := newBillingInflightLease(context.Background(), repo, inflightTestConfig(), 1, 1, false)
			require.NoError(t, err)
			lease.MarkDispatched()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			c.Request = c.Request.WithContext(WithBillingInflightLease(c.Request.Context(), lease))
			markBillingInflightProviderRefusal(c, tc.status, []byte(tc.body), tc.readErr)
			lease.HandlerDone()
			if tc.release {
				require.Equal(t, 1, repo.releases)
			} else {
				require.Zero(t, repo.releases)
			}
		})
	}
}
