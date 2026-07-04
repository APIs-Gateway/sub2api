package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func TestPcParseFloat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		defaultVal float64
		expected   float64
	}{
		{"empty string returns default", "", 1.0, 1.0},
		{"valid float", "3.14", 0, 3.14},
		{"valid integer as float", "42", 0, 42.0},
		{"invalid string returns default", "notanumber", 9.99, 9.99},
		{"zero value", "0", 5.0, 0},
		{"negative value", "-10.5", 0, -10.5},
		{"very large value", "99999999.99", 0, 99999999.99},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pcParseFloat(tt.input, tt.defaultVal)
			if got != tt.expected {
				t.Fatalf("pcParseFloat(%q, %v) = %v, want %v", tt.input, tt.defaultVal, got, tt.expected)
			}
		})
	}
}

func TestPcParseInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		defaultVal int
		expected   int
	}{
		{"empty string returns default", "", 30, 30},
		{"valid int", "10", 0, 10},
		{"invalid string returns default", "abc", 5, 5},
		{"float string returns default", "3.14", 0, 0},
		{"zero value", "0", 99, 0},
		{"negative value", "-1", 0, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pcParseInt(tt.input, tt.defaultVal)
			if got != tt.expected {
				t.Fatalf("pcParseInt(%q, %v) = %v, want %v", tt.input, tt.defaultVal, got, tt.expected)
			}
		})
	}
}

func TestParsePaymentConfig(t *testing.T) {
	t.Parallel()

	svc := &PaymentConfigService{}

	t.Run("empty vals uses defaults", func(t *testing.T) {
		t.Parallel()
		cfg := svc.parsePaymentConfig(map[string]string{})
		if cfg.Enabled {
			t.Fatal("expected Enabled=false by default")
		}
		if cfg.MinAmount != 1 {
			t.Fatalf("expected MinAmount=1, got %v", cfg.MinAmount)
		}
		if cfg.MaxAmount != 0 {
			t.Fatalf("expected MaxAmount=0 (no limit), got %v", cfg.MaxAmount)
		}
		if cfg.SubscriptionMinDaily != 30 || cfg.SubscriptionMaxDaily != 510 || cfg.SubscriptionMaxDays != 360 {
			t.Fatalf("subscription pricing defaults = min %v max %v days %d, want 30/510/360",
				cfg.SubscriptionMinDaily, cfg.SubscriptionMaxDaily, cfg.SubscriptionMaxDays)
		}
		if cfg.SubscriptionPayMultiplier != 1 || cfg.SubscriptionMinPlanRatio != 2 || cfg.SubscriptionMaxPlanRatio != 1 {
			t.Fatalf("subscription multipliers defaults = pay %v min ratio %v max ratio %v, want 1/2/1",
				cfg.SubscriptionPayMultiplier, cfg.SubscriptionMinPlanRatio, cfg.SubscriptionMaxPlanRatio)
		}
		if cfg.OrderTimeoutMin != 30 {
			t.Fatalf("expected OrderTimeoutMin=30, got %v", cfg.OrderTimeoutMin)
		}
		if cfg.MaxPendingOrders != 3 {
			t.Fatalf("expected MaxPendingOrders=3, got %v", cfg.MaxPendingOrders)
		}
		if cfg.LoadBalanceStrategy != payment.DefaultLoadBalanceStrategy {
			t.Fatalf("expected LoadBalanceStrategy=%s, got %q", payment.DefaultLoadBalanceStrategy, cfg.LoadBalanceStrategy)
		}
		if len(cfg.EnabledTypes) != 0 {
			t.Fatalf("expected empty EnabledTypes, got %v", cfg.EnabledTypes)
		}
	})

	t.Run("all values populated", func(t *testing.T) {
		t.Parallel()
		vals := map[string]string{
			SettingPaymentEnabled:       "true",
			SettingMinRechargeAmount:    "5.00",
			SettingMaxRechargeAmount:    "1000.00",
			SettingDailyRechargeLimit:   "5000.00",
			SettingOrderTimeoutMinutes:  "15",
			SettingMaxPendingOrders:     "5",
			SettingEnabledPaymentTypes:  "alipay,wxpay,stripe",
			SettingBalancePayDisabled:   "true",
			SettingSubscriptionPayMult:  "1.98",
			SettingRefundFeeRate:        "1.25",
			SettingLoadBalanceStrategy:  "least_amount",
			SettingProductNamePrefix:    "PRE",
			SettingProductNameSuffix:    "SUF",
			SettingSubscriptionMinDaily: "30.00",
			SettingSubscriptionMaxDaily: "90.00",
			SettingSubscriptionMaxDays:  "720",
			SettingSubscriptionMinRatio: "2.50",
			SettingSubscriptionMaxRatio: "1.20",
		}
		cfg := svc.parsePaymentConfig(vals)

		if !cfg.Enabled {
			t.Fatal("expected Enabled=true")
		}
		if cfg.MinAmount != 5 {
			t.Fatalf("MinAmount = %v, want 5", cfg.MinAmount)
		}
		if cfg.MaxAmount != 1000 {
			t.Fatalf("MaxAmount = %v, want 1000", cfg.MaxAmount)
		}
		if cfg.DailyLimit != 5000 {
			t.Fatalf("DailyLimit = %v, want 5000", cfg.DailyLimit)
		}
		if cfg.OrderTimeoutMin != 15 {
			t.Fatalf("OrderTimeoutMin = %v, want 15", cfg.OrderTimeoutMin)
		}
		if cfg.RefundFeeRate != 1.25 {
			t.Fatalf("RefundFeeRate = %v, want 1.25", cfg.RefundFeeRate)
		}
		if cfg.MaxPendingOrders != 5 {
			t.Fatalf("MaxPendingOrders = %v, want 5", cfg.MaxPendingOrders)
		}
		if len(cfg.EnabledTypes) != 3 {
			t.Fatalf("EnabledTypes len = %d, want 3", len(cfg.EnabledTypes))
		}
		if cfg.EnabledTypes[0] != "alipay" || cfg.EnabledTypes[1] != "wxpay" || cfg.EnabledTypes[2] != "stripe" {
			t.Fatalf("EnabledTypes = %v, want [alipay wxpay stripe]", cfg.EnabledTypes)
		}
		if !cfg.BalanceDisabled {
			t.Fatal("expected BalanceDisabled=true")
		}
		if cfg.LoadBalanceStrategy != "least_amount" {
			t.Fatalf("LoadBalanceStrategy = %q, want %q", cfg.LoadBalanceStrategy, "least_amount")
		}
		if cfg.ProductNamePrefix != "PRE" {
			t.Fatalf("ProductNamePrefix = %q, want %q", cfg.ProductNamePrefix, "PRE")
		}
		if cfg.ProductNameSuffix != "SUF" {
			t.Fatalf("ProductNameSuffix = %q, want %q", cfg.ProductNameSuffix, "SUF")
		}
		if cfg.SubscriptionMinDaily != 30 || cfg.SubscriptionMaxDaily != 90 || cfg.SubscriptionMaxDays != 720 {
			t.Fatalf("subscription pricing = min %v max %v days %d, want 30/90/720",
				cfg.SubscriptionMinDaily, cfg.SubscriptionMaxDaily, cfg.SubscriptionMaxDays)
		}
		if cfg.SubscriptionPayMultiplier != 1.98 || cfg.SubscriptionMinPlanRatio != 2.5 || cfg.SubscriptionMaxPlanRatio != 1.2 {
			t.Fatalf("subscription multipliers = pay %v min ratio %v max ratio %v, want 1.98/2.5/1.2",
				cfg.SubscriptionPayMultiplier, cfg.SubscriptionMinPlanRatio, cfg.SubscriptionMaxPlanRatio)
		}
	})

	t.Run("enabled types with spaces are trimmed", func(t *testing.T) {
		t.Parallel()
		vals := map[string]string{
			SettingEnabledPaymentTypes: " alipay , wxpay ",
		}
		cfg := svc.parsePaymentConfig(vals)
		if len(cfg.EnabledTypes) != 2 {
			t.Fatalf("EnabledTypes len = %d, want 2", len(cfg.EnabledTypes))
		}
		if cfg.EnabledTypes[0] != "alipay" || cfg.EnabledTypes[1] != "wxpay" {
			t.Fatalf("EnabledTypes = %v, want [alipay wxpay]", cfg.EnabledTypes)
		}
	})

	t.Run("enabled types are normalized to visible methods and deduplicated", func(t *testing.T) {
		t.Parallel()
		vals := map[string]string{
			SettingEnabledPaymentTypes: "alipay_direct, alipay, wxpay_direct, wxpay",
		}
		cfg := svc.parsePaymentConfig(vals)
		if len(cfg.EnabledTypes) != 2 {
			t.Fatalf("EnabledTypes len = %d, want 2", len(cfg.EnabledTypes))
		}
		if cfg.EnabledTypes[0] != "alipay" || cfg.EnabledTypes[1] != "wxpay" {
			t.Fatalf("EnabledTypes = %v, want [alipay wxpay]", cfg.EnabledTypes)
		}
	})

	t.Run("empty enabled types string", func(t *testing.T) {
		t.Parallel()
		vals := map[string]string{
			SettingEnabledPaymentTypes: "",
		}
		cfg := svc.parsePaymentConfig(vals)
		if len(cfg.EnabledTypes) != 0 {
			t.Fatalf("expected empty EnabledTypes for empty string, got %v", cfg.EnabledTypes)
		}
	})
}

func TestGetBasePaymentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{payment.TypeEasyPay, payment.TypeEasyPay},
		{payment.TypeStripe, payment.TypeStripe},
		{payment.TypeCard, payment.TypeStripe},
		{payment.TypeLink, payment.TypeStripe},
		{payment.TypeAlipay, payment.TypeAlipay},
		{payment.TypeAlipayDirect, payment.TypeAlipay},
		{payment.TypeWxpay, payment.TypeWxpay},
		{payment.TypeWxpayDirect, payment.TypeWxpay},
		{"unknown", "unknown"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := payment.GetBasePaymentType(tt.input)
			if got != tt.expected {
				t.Fatalf("GetBasePaymentType(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestApplyVisibleMethodRoutingToEnabledTypes(t *testing.T) {
	t.Parallel()

	base := []string{"alipay", "wxpay", "stripe"}
	vals := map[string]string{
		SettingPaymentVisibleMethodAlipayEnabled: "true",
		SettingPaymentVisibleMethodAlipaySource:  VisibleMethodSourceOfficialAlipay,
		SettingPaymentVisibleMethodWxpayEnabled:  "true",
		SettingPaymentVisibleMethodWxpaySource:   VisibleMethodSourceOfficialWechat,
	}
	available := map[string]bool{
		VisibleMethodSourceOfficialAlipay: true,
		VisibleMethodSourceOfficialWechat: false,
	}

	got := applyVisibleMethodRoutingToEnabledTypes(base, vals, available)
	want := []string{"alipay", "stripe"}
	if len(got) != len(want) {
		t.Fatalf("applyVisibleMethodRoutingToEnabledTypes len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("applyVisibleMethodRoutingToEnabledTypes[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

func TestApplyVisibleMethodRoutingAddsConfiguredVisibleMethod(t *testing.T) {
	t.Parallel()

	base := []string{"stripe"}
	vals := map[string]string{
		SettingPaymentVisibleMethodAlipayEnabled: "true",
		SettingPaymentVisibleMethodAlipaySource:  VisibleMethodSourceEasyPayAlipay,
	}
	available := map[string]bool{
		VisibleMethodSourceEasyPayAlipay: true,
	}

	got := applyVisibleMethodRoutingToEnabledTypes(base, vals, available)
	want := []string{"stripe", "alipay"}
	if len(got) != len(want) {
		t.Fatalf("applyVisibleMethodRoutingToEnabledTypes len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("applyVisibleMethodRoutingToEnabledTypes[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

func TestBuildVisibleMethodSourceAvailability(t *testing.T) {
	t.Parallel()

	instances := []*dbent.PaymentProviderInstance{
		{ProviderKey: payment.TypeAlipay, SupportedTypes: "alipay"},
		{ProviderKey: payment.TypeEasyPay, SupportedTypes: "wxpay_direct, alipay"},
		{ProviderKey: payment.TypeWxpay, SupportedTypes: "wxpay_direct"},
	}

	got := buildVisibleMethodSourceAvailability(instances)
	if !got[VisibleMethodSourceOfficialAlipay] {
		t.Fatalf("expected %q to be available", VisibleMethodSourceOfficialAlipay)
	}
	if !got[VisibleMethodSourceEasyPayAlipay] {
		t.Fatalf("expected %q to be available", VisibleMethodSourceEasyPayAlipay)
	}
	if !got[VisibleMethodSourceOfficialWechat] {
		t.Fatalf("expected %q to be available", VisibleMethodSourceOfficialWechat)
	}
	if !got[VisibleMethodSourceEasyPayWechat] {
		t.Fatalf("expected %q to be available", VisibleMethodSourceEasyPayWechat)
	}
}

func TestGetPaymentConfigKeepsStoredEnabledTypes(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeEasyPay).
		SetName("EasyPay Alipay").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create easypay instance: %v", err)
	}

	svc := &PaymentConfigService{
		entClient: client,
		settingRepo: &paymentConfigSettingRepoStub{
			values: map[string]string{
				SettingEnabledPaymentTypes: "alipay,wxpay,stripe",
			},
		},
	}

	cfg, err := svc.GetPaymentConfig(ctx)
	if err != nil {
		t.Fatalf("GetPaymentConfig returned error: %v", err)
	}

	want := []string{payment.TypeAlipay, payment.TypeWxpay, payment.TypeStripe}
	if len(cfg.EnabledTypes) != len(want) {
		t.Fatalf("EnabledTypes len = %d, want %d (%v)", len(cfg.EnabledTypes), len(want), cfg.EnabledTypes)
	}
	for i := range want {
		if cfg.EnabledTypes[i] != want[i] {
			t.Fatalf("EnabledTypes[%d] = %q, want %q (full=%v)", i, cfg.EnabledTypes[i], want[i], cfg.EnabledTypes)
		}
	}
}

func newPaymentConfigServiceTestClient(t *testing.T) *dbent.Client {
	t.Helper()

	dbName := fmt.Sprintf(
		"file:%s?mode=memory&cache=shared",
		strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()),
	)
	db, err := sql.Open("sqlite", dbName)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

type paymentConfigSettingRepoStub struct {
	values  map[string]string
	updates map[string]string
}

func (s *paymentConfigSettingRepoStub) Get(context.Context, string) (*Setting, error) {
	return nil, nil
}
func (s *paymentConfigSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}
func (s *paymentConfigSettingRepoStub) Set(context.Context, string, string) error { return nil }
func (s *paymentConfigSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = s.values[key]
	}
	return out, nil
}
func (s *paymentConfigSettingRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	s.updates = make(map[string]string, len(values))
	for key, value := range values {
		s.updates[key] = value
		if s.values == nil {
			s.values = map[string]string{}
		}
		s.values[key] = value
	}
	return nil
}
func (s *paymentConfigSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return s.values, nil
}
func (s *paymentConfigSettingRepoStub) Delete(context.Context, string) error { return nil }

func TestUpdatePaymentConfig_PersistsVisibleMethodRouting(t *testing.T) {
	repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
	svc := &PaymentConfigService{settingRepo: repo}

	alipayEnabled := true
	wxpayEnabled := false
	err := svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{
		VisibleMethodAlipayEnabled: &alipayEnabled,
		VisibleMethodAlipaySource:  paymentConfigStrPtr(VisibleMethodSourceEasyPayAlipay),
		VisibleMethodWxpayEnabled:  &wxpayEnabled,
		VisibleMethodWxpaySource:   paymentConfigStrPtr(VisibleMethodSourceOfficialWechat),
	})
	if err != nil {
		t.Fatalf("UpdatePaymentConfig returned error: %v", err)
	}

	if repo.values[SettingPaymentVisibleMethodAlipayEnabled] != "true" {
		t.Fatalf("alipay enabled = %q, want true", repo.values[SettingPaymentVisibleMethodAlipayEnabled])
	}
	if repo.values[SettingPaymentVisibleMethodAlipaySource] != VisibleMethodSourceEasyPayAlipay {
		t.Fatalf("alipay source = %q, want %q", repo.values[SettingPaymentVisibleMethodAlipaySource], VisibleMethodSourceEasyPayAlipay)
	}
	if repo.values[SettingPaymentVisibleMethodWxpayEnabled] != "false" {
		t.Fatalf("wxpay enabled = %q, want false", repo.values[SettingPaymentVisibleMethodWxpayEnabled])
	}
	if repo.values[SettingPaymentVisibleMethodWxpaySource] != VisibleMethodSourceOfficialWechat {
		t.Fatalf("wxpay source = %q, want %q", repo.values[SettingPaymentVisibleMethodWxpaySource], VisibleMethodSourceOfficialWechat)
	}
}

func TestUpdatePaymentConfig_PreservesFineSubscriptionPlanRatios(t *testing.T) {
	repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
	svc := &PaymentConfigService{settingRepo: repo}

	minRatio := 0.055
	maxRatio := 0.035
	if err := svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{
		SubscriptionMinPlanRatio: &minRatio,
		SubscriptionMaxPlanRatio: &maxRatio,
	}); err != nil {
		t.Fatalf("UpdatePaymentConfig returned error: %v", err)
	}

	if repo.values[SettingSubscriptionMinRatio] != "0.055" {
		t.Fatalf("min ratio stored as %q, want 0.055", repo.values[SettingSubscriptionMinRatio])
	}
	if repo.values[SettingSubscriptionMaxRatio] != "0.035" {
		t.Fatalf("max ratio stored as %q, want 0.035", repo.values[SettingSubscriptionMaxRatio])
	}
}

func TestUpdatePaymentConfig_PersistsSubscriptionBillingSettings(t *testing.T) {
	repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
	svc := &PaymentConfigService{settingRepo: repo}

	subPayMultiplier := 1.25
	refundFeeRate := 2.5
	minDaily := 30.0
	maxDaily := 120.0
	maxDays := 720
	minPlanRatio := 2.75
	maxPlanRatio := 1.25
	kyrenSecret := "  secret-token  "
	err := svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{
		SubscriptionPayMultiplier: &subPayMultiplier,
		RefundFeeRate:             &refundFeeRate,
		SubscriptionMinDaily:      &minDaily,
		SubscriptionMaxDaily:      &maxDaily,
		SubscriptionMaxDays:       &maxDays,
		SubscriptionMinPlanRatio:  &minPlanRatio,
		SubscriptionMaxPlanRatio:  &maxPlanRatio,
		KyrenWebhookSecret:        &kyrenSecret,
	})
	if err != nil {
		t.Fatalf("UpdatePaymentConfig returned error: %v", err)
	}

	want := map[string]string{
		SettingSubscriptionPayMult:  "1.25",
		SettingRefundFeeRate:        "2.50",
		SettingSubscriptionMinDaily: "30.00",
		SettingSubscriptionMaxDaily: "120.00",
		SettingSubscriptionMaxDays:  "720",
		SettingSubscriptionMinRatio: "2.75",
		SettingSubscriptionMaxRatio: "1.25",
		SettingKyrenWebhookSecret:   "secret-token",
	}
	for key, expected := range want {
		if repo.values[key] != expected {
			t.Fatalf("%s stored as %q, want %q", key, repo.values[key], expected)
		}
	}
}

func TestUpdatePaymentConfig_RejectsInvalidSubscriptionBillingSettings(t *testing.T) {
	tests := []struct {
		name       string
		req        UpdatePaymentConfigRequest
		wantReason string
	}{
		{
			name:       "subscription pay multiplier must be positive",
			req:        UpdatePaymentConfigRequest{SubscriptionPayMultiplier: paymentConfigFloatPtr(0)},
			wantReason: "INVALID_SUBSCRIPTION_PAYMENT_MULTIPLIER",
		},
		{
			name:       "refund fee rate must be a valid percent",
			req:        UpdatePaymentConfigRequest{RefundFeeRate: paymentConfigFloatPtr(100.001)},
			wantReason: "INVALID_REFUND_FEE_RATE",
		},
		{
			name:       "subscription minimum daily must use configured step",
			req:        UpdatePaymentConfigRequest{SubscriptionMinDaily: paymentConfigFloatPtr(31)},
			wantReason: "INVALID_SUBSCRIPTION_MIN_DAILY_AMOUNT",
		},
		{
			name:       "subscription maximum daily must use configured step",
			req:        UpdatePaymentConfigRequest{SubscriptionMaxDaily: paymentConfigFloatPtr(59)},
			wantReason: "INVALID_SUBSCRIPTION_MAX_DAILY_AMOUNT",
		},
		{
			name: "subscription maximum daily must be at least minimum daily",
			req: UpdatePaymentConfigRequest{
				SubscriptionMinDaily: paymentConfigFloatPtr(120),
				SubscriptionMaxDaily: paymentConfigFloatPtr(30),
			},
			wantReason: "INVALID_SUBSCRIPTION_DAILY_AMOUNT_RANGE",
		},
		{
			name:       "subscription maximum validity cannot be below minimum",
			req:        UpdatePaymentConfigRequest{SubscriptionMaxDays: paymentConfigIntPtr(29)},
			wantReason: "INVALID_SUBSCRIPTION_MAX_VALIDITY_DAYS",
		},
		{
			name:       "subscription minimum plan ratio must be positive",
			req:        UpdatePaymentConfigRequest{SubscriptionMinPlanRatio: paymentConfigFloatPtr(0)},
			wantReason: "INVALID_SUBSCRIPTION_MIN_PLAN_RATIO",
		},
		{
			name:       "subscription maximum plan ratio must be positive",
			req:        UpdatePaymentConfigRequest{SubscriptionMaxPlanRatio: paymentConfigFloatPtr(-1)},
			wantReason: "INVALID_SUBSCRIPTION_MAX_PLAN_RATIO",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
			svc := &PaymentConfigService{settingRepo: repo}

			err := svc.UpdatePaymentConfig(context.Background(), tt.req)
			if err == nil {
				t.Fatal("UpdatePaymentConfig returned nil error")
			}
			if got := infraerrors.Reason(err); got != tt.wantReason {
				t.Fatalf("Reason(err) = %q, want %q", got, tt.wantReason)
			}
			if len(repo.updates) != 0 {
				t.Fatalf("settings were written for invalid request: %v", repo.updates)
			}
		})
	}
}

func TestGetKyrenWebhookSecret_ReturnsTrimmedConfiguredSecret(t *testing.T) {
	svc := &PaymentConfigService{
		settingRepo: &paymentConfigSettingRepoStub{
			values: map[string]string{
				SettingKyrenWebhookSecret: "  abc  ",
			},
		},
	}

	if got := svc.GetKyrenWebhookSecret(context.Background()); got != "abc" {
		t.Fatalf("GetKyrenWebhookSecret() = %q, want abc", got)
	}
}

func paymentConfigStrPtr(value string) *string {
	return &value
}

func paymentConfigFloatPtr(value float64) *float64 {
	return &value
}

func paymentConfigIntPtr(value int) *int {
	return &value
}
