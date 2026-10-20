//go:build unit

package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSystemSettingsJSONIncludesForwardedClientIPHeaders(t *testing.T) {
	payload, err := json.Marshal(SystemSettings{
		APIKeyACLTrustForwardedIP: true,
		ForwardedClientIPHeaders:  []string{"X-Cdn-Client-IP"},
	})
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Equal(t, true, decoded["api_key_acl_trust_forwarded_ip"])
	require.Equal(t, []any{"X-Cdn-Client-IP"}, decoded["forwarded_client_ip_headers"])
}
