package service

import (
	"net/http"
	"strings"
)

const (
	AnthropicAPIKeyAuthSchemeXAPIKey             = "x_api_key"
	AnthropicAPIKeyAuthSchemeAuthorizationBearer = "authorization_bearer"
)

func (a *Account) GetAnthropicAPIKeyAuthScheme() string {
	if a == nil {
		return AnthropicAPIKeyAuthSchemeXAPIKey
	}
	switch strings.ToLower(strings.TrimSpace(a.GetExtraString("anthropic_apikey_auth_scheme"))) {
	case AnthropicAPIKeyAuthSchemeAuthorizationBearer:
		return AnthropicAPIKeyAuthSchemeAuthorizationBearer
	default:
		return AnthropicAPIKeyAuthSchemeXAPIKey
	}
}

func setAnthropicAPIKeyAuthHeader(header http.Header, account *Account, token string) {
	deleteHeaderAllForms(header, "authorization")
	deleteHeaderAllForms(header, "x-api-key")
	if account.GetAnthropicAPIKeyAuthScheme() == AnthropicAPIKeyAuthSchemeAuthorizationBearer {
		setHeaderRaw(header, "authorization", "Bearer "+token)
		return
	}
	setHeaderRaw(header, "x-api-key", token)
}

func setAnthropicAPIKeyAuthHeaderCanonical(header http.Header, account *Account, token string) {
	deleteHeaderAllForms(header, "authorization")
	deleteHeaderAllForms(header, "x-api-key")
	if account.GetAnthropicAPIKeyAuthScheme() == AnthropicAPIKeyAuthSchemeAuthorizationBearer {
		header.Set("Authorization", "Bearer "+token)
		return
	}
	header.Set("x-api-key", token)
}
