package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type quotaTestAccountRepo struct {
	AccountRepository
	account *Account
	err     error
}

func (r *quotaTestAccountRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	return r.account, r.err
}

func (r *quotaTestAccountRepo) SetError(_ context.Context, _ int64, _ string) error {
	return nil
}

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

func TestBuildCodexCommonHeadersIncludesFedRAMPOnlyWhenEnabled(t *testing.T) {
	standard := buildCodexCommonHeaders("token", "account", false)
	fedRAMP := buildCodexCommonHeaders("token", "account", true)

	require.Empty(t, standard["x-openai-fedramp"])
	require.Equal(t, "true", fedRAMP["x-openai-fedramp"])
}

func TestQueryResetCreditDetailsFailureIsNonFatal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client, err := newResetCreditTestClient(server.URL)
	require.NoError(t, err)

	service := &OpenAIQuotaService{}
	details := service.queryResetCreditDetails(context.Background(), client, "token", "account", false, 1)
	require.Nil(t, details)
}

func TestQueryResetCreditDetailsResponseHandling(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantCount  int
		wantList   bool
		wantNil    bool
	}{
		{name: "valid detail", statusCode: http.StatusOK, body: `{"available_count":0,"credits":[]` + `}`, wantCount: 0, wantList: true, wantNil: false},
		{name: "malformed credits preserve detail count", statusCode: http.StatusOK, body: `{"available_count":2,"credits":"malformed"}`, wantCount: 2, wantNil: false},
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
			details := (&OpenAIQuotaService{}).queryResetCreditDetails(context.Background(), client, "token", "account", false, 1)
			if tt.wantNil {
				require.Nil(t, details)
				return
			}
			require.NotNil(t, details)
			require.Equal(t, tt.wantCount, *details.AvailableCount)
			require.Equal(t, tt.wantList, details.CreditListPresent)
		})
	}
}

func TestOpenAIQuotaServicePATQueryAndResetIncludeFedRAMPHeaders(t *testing.T) {
	var requests []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Clone(r.Context()))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			_, _ = w.Write([]byte(`{"rate_limit_reset_credits":{"available_count":1,"credits":[]}}`))
		case "/backend-api/wham/rate-limit-reset-credits":
			_, _ = w.Write([]byte(`{"available_count":1,"credits":[]}`))
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			_, _ = w.Write([]byte(`{"code":"ok","windows_reset":2,"credit":{"id":"credit-1","status":"redeemed"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	account := &Account{
		ID:       77,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":               "at-test-token",
			"auth_mode":                  OpenAIAuthModePersonalAccessToken,
			"chatgpt_account_id":         "acct-test",
			"chatgpt_account_is_fedramp": true,
		},
	}
	repo := &quotaTestAccountRepo{account: account}
	provider := NewOpenAITokenProvider(repo, nil, nil)
	service := NewOpenAIQuotaService(repo, nil, provider, func(proxyURL string) (*req.Client, error) {
		require.Empty(t, proxyURL)
		return newResetCreditTestClient(server.URL)
	})

	usage, err := service.QueryUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.NotNil(t, usage.RateLimitResetCredits)
	require.Equal(t, 1, usage.RateLimitResetCredits.AvailableCount)

	reset, err := service.ResetCredit(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "ok", reset.Code)
	require.Equal(t, 2, reset.WindowsReset)
	require.Len(t, requests, 3)
	for _, request := range requests {
		require.Equal(t, "Bearer at-test-token", request.Header.Get("Authorization"))
		require.Equal(t, "acct-test", request.Header.Get("ChatGPT-Account-ID"))
		require.Equal(t, "true", request.Header.Get("X-OpenAI-Fedramp"))
	}
}

func TestOpenAIQuotaServicePrepareUpstreamCallValidation(t *testing.T) {
	_, _, _, _, err := (&OpenAIQuotaService{}).prepareUpstreamCall(context.Background(), 1)
	require.Error(t, err)

	tests := []struct {
		name    string
		account *Account
		repoErr error
	}{
		{name: "repository error", repoErr: errors.New("database unavailable")},
		{name: "nil account"},
		{name: "invalid platform", account: &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}},
		{name: "invalid type", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}},
		{name: "missing account id", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "token"}}},
		{
			name:    "token unavailable",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "expired", "chatgpt_account_id": "acct", "expires_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &quotaTestAccountRepo{account: tt.account, err: tt.repoErr}
			provider := NewOpenAITokenProvider(nil, nil, nil)
			service := NewOpenAIQuotaService(repo, nil, provider, func(string) (*req.Client, error) {
				return req.C(), nil
			})
			_, _, _, _, err := service.prepareUpstreamCall(context.Background(), 1)
			require.Error(t, err)
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
