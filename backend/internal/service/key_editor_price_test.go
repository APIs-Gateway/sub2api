//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// keyEditorSettingRepo 是内存版 SettingRepository，只实现编辑器报价用到的方法。
type keyEditorSettingRepo struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
	getErr error
	calls  int
}

func (r *keyEditorSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return "", r.getErr
	}
	v, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}

func (r *keyEditorSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.getErr != nil {
		return nil, r.getErr
	}
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (r *keyEditorSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

// stubKeyEditorQuoter 按 (GroupID, ServedGroupID) 返回预设报价。
type stubKeyEditorQuoter struct {
	mu     sync.Mutex
	calls  []QuoteRequest
	byPair map[[2]int64]*Quote
	err    error
}

func (q *stubKeyEditorQuoter) Quote(_ context.Context, req QuoteRequest) (*Quote, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls = append(q.calls, req)
	if q.err != nil {
		return nil, q.err
	}
	if quote, ok := q.byPair[[2]int64{req.GroupID, req.ServedGroupID}]; ok {
		return quote, nil
	}
	return &Quote{Priced: false}, nil
}

func pricedQuote(servedGroupID int64, officialIn, officialOut, multiplier float64) *Quote {
	return &Quote{
		Priced:              true,
		ServedGroupID:       servedGroupID,
		EffectiveMultiplier: multiplier,
		Prices:              &QuotePriceSet{PerMTok: QuoteUnitPrices{Input: officialIn, Output: officialOut}},
		FinalPrices:         &QuotePriceSet{PerMTok: QuoteUnitPrices{Input: officialIn * multiplier, Output: officialOut * multiplier}},
	}
}

func newKeyEditorTestService(quoter keyEditorQuoter, settings SettingRepository) (*KeyEditorPriceService, *time.Time) {
	now := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	return newKeyEditorPriceService(quoter, settings, func() time.Time { return now }), &now
}

func TestKeyEditorReferencePrice_BothCNYBasesAndNoMixing(t *testing.T) {
	settings := &keyEditorSettingRepo{values: map[string]string{
		SettingOfficialPriceCNYRate: "7.2",
		SettingBalanceRechargeMult:  "13",
	}}
	quoter := &stubKeyEditorQuoter{byPair: map[[2]int64]*Quote{
		{16, 21}: pricedQuote(21, 1.25, 10, 1.5),
	}}
	svc, _ := newKeyEditorTestService(quoter, settings)

	got := svc.ReferencePrice(context.Background(), "gpt-5.5", 16, 21, 7)

	require.True(t, got.Priced)
	require.InDelta(t, 1.875, *got.InputUSDPerMTok, 1e-9)
	require.InDelta(t, 15.0, *got.OutputUSDPerMTok, 1e-9)
	require.InDelta(t, 1.25, *got.OfficialInputUSDPerMTok, 1e-9)
	require.InDelta(t, 10.0, *got.OfficialOutputUSDPerMTok, 1e-9)
	// 余额价口径：额度价 ÷ 充值倍率，与价格页一致。
	require.InDelta(t, 1.875/13, got.CNY.InputPerMTok, 1e-4)
	require.InDelta(t, 15.0/13, got.CNY.OutputPerMTok, 1e-4)
	// 官方汇率口径：额度价 × OFFICIAL_PRICE_CNY_RATE。两个汇率各用各的，不互相混用。
	require.InDelta(t, 1.875*7.2, got.CNYOfficialRate.InputPerMTok, 1e-4)
	require.InDelta(t, 15.0*7.2, got.CNYOfficialRate.OutputPerMTok, 1e-4)

	require.Len(t, quoter.calls, 1)
	require.Equal(t, QuoteRequest{Model: "gpt-5.5", GroupID: 16, ServedGroupID: 21, UserID: 7}, quoter.calls[0])
}

func TestKeyEditorReferencePrice_DefaultRatesWhenSettingsMissing(t *testing.T) {
	quoter := &stubKeyEditorQuoter{byPair: map[[2]int64]*Quote{{16, 16}: pricedQuote(16, 2, 4, 1)}}
	svc, _ := newKeyEditorTestService(quoter, &keyEditorSettingRepo{})

	got := svc.ReferencePrice(context.Background(), "gpt-5.5", 16, 16, 7)

	require.True(t, got.Priced)
	// 汇率默认 7.2，充值倍率默认 1。
	require.InDelta(t, 2.0, got.CNY.InputPerMTok, 1e-9)
	require.InDelta(t, 2*DefaultOfficialPriceCNYRate, got.CNYOfficialRate.InputPerMTok, 1e-9)
}

func TestKeyEditorReferencePrice_DegradesToUnpriced(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		quote *Quote
		err   error
	}{
		{name: "priced=false", quote: &Quote{Priced: false}},
		{name: "per-request billing has no token prices", quote: &Quote{Priced: true}},
		{name: "quoter error", err: errors.New("boom")},
		{name: "group missing", err: ErrGroupNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quoter := &stubKeyEditorQuoter{err: tc.err, byPair: map[[2]int64]*Quote{{16, 21}: tc.quote}}
			svc, _ := newKeyEditorTestService(quoter, &keyEditorSettingRepo{})
			got := svc.ReferencePrice(ctx, "gpt-5.5", 16, 21, 7)
			require.Equal(t, KeyEditorReferencePrice{}, got)
		})
	}

	svc, _ := newKeyEditorTestService(&stubKeyEditorQuoter{}, &keyEditorSettingRepo{})
	require.False(t, svc.ReferencePrice(ctx, "", 16, 21, 7).Priced, "no reference model")
	require.False(t, svc.ReferencePrice(ctx, "gpt-5.5", 0, 21, 7).Priced)
	var nilSvc *KeyEditorPriceService
	require.False(t, nilSvc.ReferencePrice(ctx, "gpt-5.5", 16, 21, 7).Priced)

	unpriced, err := json.Marshal(KeyEditorReferencePrice{})
	require.NoError(t, err)
	require.Equal(t, `{"priced":false}`, string(unpriced), "unpriced items expose no price fields at all")
}

func TestKeyEditorReferencePrice_ZeroRateServedGroupRequotedOnItsOwn(t *testing.T) {
	// Quoter 在服务分组倍率为 0 时回落到主分组计价（返回 ServedGroupID=主分组）；
	// 编辑器要展示该分组自己的价格（免费），所以会改用它自己作为计价分组再报一次。
	quoter := &stubKeyEditorQuoter{byPair: map[[2]int64]*Quote{
		{16, 40}: pricedQuote(16, 1.25, 10, 1),
		{40, 0}:  pricedQuote(40, 1.25, 10, 0),
	}}
	svc, _ := newKeyEditorTestService(quoter, &keyEditorSettingRepo{})

	got := svc.ReferencePrice(context.Background(), "gpt-5.5", 16, 40, 7)

	require.True(t, got.Priced)
	require.Equal(t, 0.0, *got.InputUSDPerMTok)
	require.Equal(t, 0.0, got.CNY.InputPerMTok)
	require.Len(t, quoter.calls, 2)
	require.Equal(t, QuoteRequest{Model: "gpt-5.5", GroupID: 40, UserID: 7}, quoter.calls[1])
}

func TestKeyEditorReferencePrice_CachedForThirtySeconds(t *testing.T) {
	quoter := &stubKeyEditorQuoter{byPair: map[[2]int64]*Quote{{16, 21}: pricedQuote(21, 1, 2, 1)}}
	svc, now := newKeyEditorTestService(quoter, &keyEditorSettingRepo{})
	ctx := context.Background()

	svc.ReferencePrice(ctx, "gpt-5.5", 16, 21, 7)
	svc.ReferencePrice(ctx, "gpt-5.5", 16, 21, 7)
	require.Len(t, quoter.calls, 1)

	// 不同用户（专属倍率不同）不共用缓存。
	svc.ReferencePrice(ctx, "gpt-5.5", 16, 21, 8)
	require.Len(t, quoter.calls, 2)

	*now = now.Add(keyEditorPriceCacheTTL + time.Second)
	svc.ReferencePrice(ctx, "gpt-5.5", 16, 21, 7)
	require.Len(t, quoter.calls, 3)
}

func TestKeyEditorReferenceModel_SettingOverridesDefaultAndQueryOverridesBoth(t *testing.T) {
	ctx := context.Background()
	settings := &keyEditorSettingRepo{}
	svc, _ := newKeyEditorTestService(&stubKeyEditorQuoter{}, settings)

	require.Equal(t, defaultKeyEditorReferenceModels[PlatformOpenAI], svc.ReferenceModel(ctx, PlatformOpenAI, ""))

	_, err := svc.SetReferenceModels(ctx, map[string]string{PlatformOpenAI: "gpt-5.6-sol"})
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-sol", svc.ReferenceModel(ctx, PlatformOpenAI, ""))
	// 其它平台仍是内置默认。
	require.Equal(t, defaultKeyEditorReferenceModels[PlatformAnthropic], svc.ReferenceModel(ctx, PlatformAnthropic, ""))
	// ?model= 覆盖一切。
	require.Equal(t, "gpt-5.4", svc.ReferenceModel(ctx, PlatformOpenAI, " gpt-5.4 "))

	// 存储的是 JSON 对象，键为 key_editor_reference_model。
	require.JSONEq(t, `{"openai":"gpt-5.6-sol"}`, settings.values[SettingKeyEditorReferenceModel])

	// 空串删除覆盖。
	_, err = svc.SetReferenceModels(ctx, map[string]string{PlatformOpenAI: ""})
	require.NoError(t, err)
	require.Equal(t, defaultKeyEditorReferenceModels[PlatformOpenAI], svc.ReferenceModel(ctx, PlatformOpenAI, ""))
}

func TestKeyEditorReferenceModel_CorruptSettingFallsBackToDefaults(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{"not json", `["a"]`, `{"openai":"bad model!"}`, `{"openai":""}`} {
		svc, _ := newKeyEditorTestService(&stubKeyEditorQuoter{}, &keyEditorSettingRepo{values: map[string]string{SettingKeyEditorReferenceModel: raw}})
		require.Equal(t, defaultKeyEditorReferenceModels[PlatformOpenAI], svc.ReferenceModel(ctx, PlatformOpenAI, ""), raw)
	}
	svc, _ := newKeyEditorTestService(&stubKeyEditorQuoter{}, &keyEditorSettingRepo{getErr: errors.New("db down")})
	require.Equal(t, defaultKeyEditorReferenceModels[PlatformOpenAI], svc.ReferenceModel(ctx, PlatformOpenAI, ""))
}

func TestKeyEditorSetReferenceModels_Validation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newKeyEditorTestService(&stubKeyEditorQuoter{}, &keyEditorSettingRepo{})

	_, err := svc.SetReferenceModels(ctx, map[string]string{"unknown-platform": "gpt-5.5"})
	require.Equal(t, "FALLBACK_INVALID_PLATFORM", routeCode(t, err))

	_, err = svc.SetReferenceModels(ctx, map[string]string{PlatformOpenAI: "bad model!"})
	require.Equal(t, "FALLBACK_INVALID_MODEL", routeCode(t, err))
}

func TestNormalizeKeyEditorModel(t *testing.T) {
	long := ""
	for i := 0; i < keyEditorModelMaxLen+1; i++ {
		long += "a"
	}
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "  ", want: ""},
		{in: " gpt-5.5 ", want: "gpt-5.5"},
		{in: "claude-opus-4.8", want: "claude-opus-4.8"},
		{in: "vendor/model:tag_1", want: "vendor/model:tag_1"},
		{in: "-leading-dash", wantErr: true},
		{in: "has space", wantErr: true},
		{in: "semi;colon", wantErr: true},
		{in: "中文", wantErr: true},
		{in: long, wantErr: true},
	}
	for _, tc := range cases {
		got, err := NormalizeKeyEditorModel(tc.in)
		if tc.wantErr {
			require.Equal(t, "FALLBACK_INVALID_MODEL", routeCode(t, err), tc.in)
			continue
		}
		require.NoError(t, err, tc.in)
		require.Equal(t, tc.want, got)
	}
}
