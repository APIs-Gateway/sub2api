package common

import "encoding/json"

// Unmarshal decodes JSON through the repository's shared JSON boundary.
func Unmarshal(data []byte, value any) error {
	return json.Unmarshal(data, value)
}
