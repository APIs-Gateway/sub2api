//go:build unit

package handler

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateAPIKeyRequestDistinguishesOmittedAndEmptyIPRules(t *testing.T) {
	var omitted UpdateAPIKeyRequest
	require.NoError(t, json.Unmarshal([]byte(`{"quota":1}`), &omitted))
	require.Nil(t, omitted.IPWhitelist)
	require.Nil(t, omitted.IPBlacklist)

	var cleared UpdateAPIKeyRequest
	require.NoError(t, json.Unmarshal([]byte(`{"ip_whitelist":[],"ip_blacklist":[]}`), &cleared))
	require.NotNil(t, cleared.IPWhitelist)
	require.NotNil(t, cleared.IPBlacklist)
	require.Empty(t, *cleared.IPWhitelist)
	require.Empty(t, *cleared.IPBlacklist)
}
