package service

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"
)

// OAuth and custom providers own their account-specific capability declarations.
// Only the official public API has a documented Astra Ultrafast default.
func officialOpenAIAstraServiceTiers(account *Account) bool {
	if account == nil || !account.IsOpenAIApiKey() {
		return false
	}
	u, err := url.Parse(account.GetOpenAIBaseURL())
	return err == nil && u.Scheme == "https" && u.User == nil &&
		strings.EqualFold(u.Hostname(), "api.openai.com") && (u.Port() == "" || u.Port() == "443")
}

// The envelope was validated by the live-manifest reader. RawMessage keeps all
// provider metadata, and a present service_tiers (including null or []) wins.
func addOfficialAstraServiceTiers(body []byte) []byte {
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(body, &envelope)
	var models []json.RawMessage
	if json.Unmarshal(envelope["models"], &models) != nil {
		return body
	}
	for i, raw := range models {
		var model map[string]json.RawMessage
		if json.Unmarshal(raw, &model) != nil {
			continue
		}
		var slug string
		if json.Unmarshal(model["slug"], &slug) != nil || !isOpenAIGPT6AstraModel(normalizeKnownOpenAICodexModel(slug)) {
			continue
		}
		if _, exists := model["service_tiers"]; exists {
			continue
		}
		// Splice just the known field, preserving all other opaque metadata bytes.
		body, _ = sjson.SetRawBytes(body, "models."+strconv.Itoa(i)+".service_tiers", []byte(`[{"id":"priority","name":"Fast","description":"Priority processing for lower latency."},{"id":"ultrafast","name":"Ultrafast","description":"Lowest latency; 6x Standard token pricing."}]`))
	}
	return body
}
