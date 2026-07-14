package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestParseOpenAIRateLimitResetCreditDetails(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantCount     *int
		wantList      bool
		wantAvailable int
		wantExpires   []string
	}{
		{
			name:          "object aliases and filters",
			body:          `{"availableCount":"2","credits":[{"reset_type":"codex_rate_limits","status":"redeemed","expires_at":"old"},{"reset_type":"codex_rate_limits","status":"available","expires_at":"new-1"},{"resetType":"codex_rate_limits","status":"available","expiresAt":"new-2"},{"reset_type":"other","status":"available","expires_at":"other"}]}`,
			wantCount:     resetCreditIntPtr(2),
			wantList:      true,
			wantAvailable: 2,
			wantExpires:   []string{"new-1", "new-2"},
		},
		{
			name:          "top level array",
			body:          `[{"status":"available","expires_at":"one"},{"status":"available"},null]`,
			wantList:      true,
			wantAvailable: 2,
			wantExpires:   []string{"one"},
		},
		{
			name:          "explicit zero",
			body:          `{"available_count":0,"credits":[]}`,
			wantCount:     resetCreditIntPtr(0),
			wantList:      true,
			wantAvailable: 0,
		},
		{
			name:      "no count or list",
			body:      `{}`,
			wantCount: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseOpenAIRateLimitResetCreditDetails([]byte(tt.body))
			require.NoError(t, err)
			require.Equal(t, tt.wantList, got.CreditListPresent)
			require.Equal(t, tt.wantAvailable, got.AvailableCreditCount)
			if tt.wantCount == nil {
				require.Nil(t, got.AvailableCount)
			} else {
				require.Equal(t, *tt.wantCount, *got.AvailableCount)
			}
			require.Len(t, got.Credits, len(tt.wantExpires))
			for i, want := range tt.wantExpires {
				require.Equal(t, want, got.Credits[i].ExpiresAt)
			}
		})
	}
}

func TestApplyOpenAIRateLimitResetCreditDetailsPrecedence(t *testing.T) {
	count := 0
	payload := &OpenAIQuotaUsage{
		RateLimitResetCredits: &OpenAIRateLimitResetCredits{AvailableCount: 7},
	}
	applyOpenAIRateLimitResetCreditDetails(payload, &openAIRateLimitResetCreditDetails{
		AvailableCount:       &count,
		AvailableCreditCount: 2,
		CreditListPresent:    true,
		Credits:              []OpenAIRateLimitResetCreditDetail{{ExpiresAt: "detail"}},
	})

	require.NotNil(t, payload.RateLimitResetCredits)
	require.Equal(t, 0, payload.RateLimitResetCredits.AvailableCount)
	require.Equal(t, []OpenAIRateLimitResetCreditDetail{{ExpiresAt: "detail"}}, payload.RateLimitResetCredits.Credits)
}

func TestApplyOpenAIRateLimitResetCreditDetailsUsesListCount(t *testing.T) {
	payload := &OpenAIQuotaUsage{}
	applyOpenAIRateLimitResetCreditDetails(payload, &openAIRateLimitResetCreditDetails{
		AvailableCreditCount: 2,
		CreditListPresent:    true,
		Credits:              []OpenAIRateLimitResetCreditDetail{{ExpiresAt: "one"}, {ExpiresAt: "two"}},
	})

	require.NotNil(t, payload.RateLimitResetCredits)
	require.Equal(t, 2, payload.RateLimitResetCredits.AvailableCount)
}

func TestApplyOpenAIRateLimitResetCreditDetailsIgnoresEmptyDetails(t *testing.T) {
	payload := &OpenAIQuotaUsage{}
	applyOpenAIRateLimitResetCreditDetails(payload, &openAIRateLimitResetCreditDetails{})
	applyOpenAIRateLimitResetCreditDetails(nil, &openAIRateLimitResetCreditDetails{CreditListPresent: true})

	require.Nil(t, payload.RateLimitResetCredits)
}

func TestQueryResetCreditDetailsFailureIsNonFatal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client, err := newResetCreditTestClient(server.URL)
	require.NoError(t, err)

	service := &OpenAIQuotaService{}
	details := service.queryResetCreditDetails(context.Background(), client, "token", "account", 1)
	require.Nil(t, details)
}

func TestQueryResetCreditDetailsResponseHandling(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantNil    bool
	}{
		{name: "valid detail", statusCode: http.StatusOK, body: `{"available_count":0,"credits":[]` + `}`, wantNil: false},
		{name: "empty detail", statusCode: http.StatusOK, body: `{}`, wantNil: true},
		{name: "malformed detail", statusCode: http.StatusOK, body: `{invalid`, wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client, err := newResetCreditTestClient(server.URL)
			require.NoError(t, err)
			details := (&OpenAIQuotaService{}).queryResetCreditDetails(context.Background(), client, "token", "account", 1)
			if tt.wantNil {
				require.Nil(t, details)
				return
			}
			require.NotNil(t, details)
			require.Equal(t, 0, *details.AvailableCount)
			require.True(t, details.CreditListPresent)
		})
	}
}

func TestParseOpenAIRateLimitResetCreditDetailsMalformedShapes(t *testing.T) {
	for _, body := range []string{"[", "{", `{"items":{}}`} {
		_, err := parseOpenAIRateLimitResetCreditDetails([]byte(body))
		require.Error(t, err)
	}
}

func TestParseOpenAIResetCreditAvailableCountSkipsInvalidValues(t *testing.T) {
	details, err := parseOpenAIRateLimitResetCreditDetails([]byte(`{"available_count":"invalid","availableCount":-1,"data":[]}`))
	require.NoError(t, err)
	require.Nil(t, details.AvailableCount)
	require.True(t, details.CreditListPresent)

	details, err = parseOpenAIRateLimitResetCreditDetails([]byte(`{"available_count":true,"availableCount":" 2 "}`))
	require.NoError(t, err)
	require.NotNil(t, details.AvailableCount)
	require.Equal(t, 2, *details.AvailableCount)
}

func newResetCreditTestClient(target string) (*req.Client, error) {
	base, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	client := req.C()
	client.WrapRoundTripFunc(func(next req.RoundTripper) req.RoundTripFunc {
		return func(request *req.Request) (*req.Response, error) {
			rewritten := *request.URL
			rewritten.Scheme = base.Scheme
			rewritten.Host = base.Host
			request.URL = &rewritten
			return next.RoundTrip(request)
		}
	})
	return client, nil
}

func resetCreditIntPtr(value int) *int {
	return &value
}
