package service

import (
	"encoding/json"
	"fmt"
	"net/url"
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
func addOfficialAstraServiceTiers(body []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(body, &envelope)
	var models []json.RawMessage
	if json.Unmarshal(envelope["models"], &models) != nil {
		return body, nil
	}
	changed := false
	size := len(body) - len(envelope["models"]) + 2
	for i, raw := range models {
		var model map[string]json.RawMessage
		var slug string
		if json.Unmarshal(raw, &model) == nil && json.Unmarshal(model["slug"], &slug) == nil && isOpenAIGPT6AstraModel(normalizeKnownOpenAICodexModel(slug)) {
			if _, exists := model["service_tiers"]; !exists {
				raw, _ = sjson.SetRawBytes(raw, "service_tiers", []byte(`[{"id":"priority","name":"Fast","description":"Priority processing for lower latency."},{"id":"ultrafast","name":"Ultrafast","description":"Lowest latency; 6x Standard token pricing."}]`))
				changed = true
			}
		}
		models[i] = raw
		size += len(raw)
		if i > 0 {
			size++
		}
		if size > int(codexModelsManifestBodyLimit) {
			return nil, fmt.Errorf("astra service-tier manifest exceeds %d bytes", codexModelsManifestBodyLimit)
		}
	}
	if !changed {
		return body, nil
	}
	// Each model is spliced once; rebuild the array without re-encoding opaque
	// metadata, then replace the large envelope just once. Enforce the original
	// response bound before allocating its transformed representation.
	array := make([]byte, 0, size-(len(body)-len(envelope["models"])))
	array = append(array, '[')
	for i, raw := range models {
		if i > 0 {
			array = append(array, ',')
		}
		array = append(array, raw...)
	}
	array = append(array, ']')
	result, _ := sjson.SetRawBytes(body, "models", array)
	return result, nil
}
