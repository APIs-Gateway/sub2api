package service

import (
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func parseForwardedClientIPHeadersSetting(value string) ([]string, error) {
	var headers []string
	if err := json.Unmarshal([]byte(value), &headers); err != nil {
		return nil, fmt.Errorf("parse forwarded_client_ip_headers: %w", err)
	}
	if headers == nil {
		return nil, fmt.Errorf("parse forwarded_client_ip_headers: value must be a JSON array")
	}
	normalized, err := config.NormalizeForwardedClientIPHeaders(headers)
	if err != nil {
		return nil, fmt.Errorf("parse forwarded_client_ip_headers: %w", err)
	}
	return normalized, nil
}
