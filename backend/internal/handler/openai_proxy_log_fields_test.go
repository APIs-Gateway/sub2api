package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func proxyIDAccount(id int64) *service.Account {
	return &service.Account{ProxyID: &id}
}

func TestAppendOpenAIProxyLogFields(t *testing.T) {
	tests := []struct {
		name     string
		account  *service.Account
		wantKeys []string
	}{
		{
			name: "full proxy",
			account: &service.Account{Proxy: &service.Proxy{
				ID: 12, Name: "edge", Host: "proxy.example", Port: 8443,
			}},
			wantKeys: []string{"proxy_id", "proxy_name", "proxy_host", "proxy_port"},
		},
		{
			name:     "proxy id fallback",
			account:  proxyIDAccount(21),
			wantKeys: []string{"proxy_id"},
		},
		{
			name:     "no proxy",
			account:  &service.Account{},
			wantKeys: []string{},
		},
		{
			name:     "nil account",
			account:  nil,
			wantKeys: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := appendOpenAIProxyLogFields(tt.account)
			keys := make([]string, 0, len(fields))
			for _, field := range fields {
				keys = append(keys, field.Key)
			}
			require.Equal(t, tt.wantKeys, keys)

			if tt.name == "full proxy" {
				require.Equal(t, "edge", fields[1].String)
				require.Equal(t, "proxy.example", fields[2].String)
				require.Equal(t, int64(12), fields[0].Integer)
				require.Equal(t, int64(8443), fields[3].Integer)
			}
			if tt.name == "proxy id fallback" {
				require.Equal(t, int64(21), fields[0].Integer)
			}
		})
	}
}
