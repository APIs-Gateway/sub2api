package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestRequestNeedsWeChatJSAPICompatibility(t *testing.T) {
	tests := []struct {
		name string
		req  CreateOrderRequest
		want bool
	}{
		{
			name: "non wxpay never needs compatibility",
			req:  CreateOrderRequest{PaymentType: payment.TypeAlipay, IsWeChatBrowser: true, OpenID: "openid"},
			want: false,
		},
		{
			name: "wxpay browser needs compatibility",
			req:  CreateOrderRequest{PaymentType: payment.TypeWxpay, IsWeChatBrowser: true},
			want: true,
		},
		{
			name: "wxpay openid needs compatibility",
			req:  CreateOrderRequest{PaymentType: payment.TypeWxpay, OpenID: "  openid  "},
			want: true,
		},
		{
			name: "wxpay without browser or openid does not need compatibility",
			req:  CreateOrderRequest{PaymentType: payment.TypeWxpay},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requestNeedsWeChatJSAPICompatibility(tt.req); got != tt.want {
				t.Fatalf("requestNeedsWeChatJSAPICompatibility() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequiresWeChatJSAPICompatibleSelection(t *testing.T) {
	tests := []struct {
		name string
		req  CreateOrderRequest
		sel  *payment.InstanceSelection
		want bool
	}{
		{
			name: "nil selection",
			req:  CreateOrderRequest{PaymentType: payment.TypeWxpay, IsWeChatBrowser: true},
			sel:  nil,
			want: false,
		},
		{
			name: "non wxpay provider",
			req:  CreateOrderRequest{PaymentType: payment.TypeWxpay, IsWeChatBrowser: true},
			sel:  &payment.InstanceSelection{ProviderKey: payment.TypeEasyPay},
			want: false,
		},
		{
			name: "wxpay browser",
			req:  CreateOrderRequest{PaymentType: payment.TypeWxpay, IsWeChatBrowser: true},
			sel:  &payment.InstanceSelection{ProviderKey: payment.TypeWxpay},
			want: true,
		},
		{
			name: "wxpay openid",
			req:  CreateOrderRequest{PaymentType: payment.TypeWxpay, OpenID: "openid"},
			sel:  &payment.InstanceSelection{ProviderKey: payment.TypeWxpay},
			want: true,
		},
		{
			name: "wxpay qrcode",
			req:  CreateOrderRequest{PaymentType: payment.TypeWxpay},
			sel:  &payment.InstanceSelection{ProviderKey: payment.TypeWxpay},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiresWeChatJSAPICompatibleSelection(tt.req, tt.sel); got != tt.want {
				t.Fatalf("requiresWeChatJSAPICompatibleSelection() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUsesOfficialWxpayVisibleMethodDerivesFromEnabledProviderInstance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeWxpay).
		SetName("Official WeChat").
		SetConfig("{}").
		SetSupportedTypes("wxpay").
		SetEnabled(true).
		SetSortOrder(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create official wxpay instance: %v", err)
	}

	svc := &PaymentService{
		configService: &PaymentConfigService{entClient: client},
	}

	if !svc.usesOfficialWxpayVisibleMethod(ctx) {
		t.Fatal("expected official wxpay visible method to be detected from enabled provider instance")
	}
}

func TestUsesOfficialWxpayVisibleMethodRespectsConfiguredSourceWhenMultipleProvidersEnabled(t *testing.T) {
	tests := []struct {
		name         string
		source       string
		wantOfficial bool
	}{
		{
			name:         "official source selected",
			source:       VisibleMethodSourceOfficialWechat,
			wantOfficial: true,
		},
		{
			name:         "easypay source selected",
			source:       VisibleMethodSourceEasyPayWechat,
			wantOfficial: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)

			_, err := client.PaymentProviderInstance.Create().
				SetProviderKey(payment.TypeWxpay).
				SetName("Official WeChat").
				SetConfig("{}").
				SetSupportedTypes("wxpay").
				SetEnabled(true).
				SetSortOrder(1).
				Save(ctx)
			if err != nil {
				t.Fatalf("create official wxpay instance: %v", err)
			}

			_, err = client.PaymentProviderInstance.Create().
				SetProviderKey(payment.TypeEasyPay).
				SetName("EasyPay WeChat").
				SetConfig("{}").
				SetSupportedTypes("wxpay").
				SetEnabled(true).
				SetSortOrder(2).
				Save(ctx)
			if err != nil {
				t.Fatalf("create easypay wxpay instance: %v", err)
			}

			svc := &PaymentService{
				configService: &PaymentConfigService{
					entClient: client,
					settingRepo: &paymentConfigSettingRepoStub{
						values: map[string]string{
							SettingPaymentVisibleMethodWxpaySource: tt.source,
						},
					},
				},
			}

			if got := svc.usesOfficialWxpayVisibleMethod(ctx); got != tt.wantOfficial {
				t.Fatalf("usesOfficialWxpayVisibleMethod() = %v, want %v", got, tt.wantOfficial)
			}
		})
	}
}
