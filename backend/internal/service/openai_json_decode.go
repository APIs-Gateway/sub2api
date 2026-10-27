package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// decodeOpenAIJSONUseNumber decodes exactly one JSON value into dst while
// keeping numbers as json.Number, so a later re-encode preserves integers
// beyond float64 precision (e.g. 19+ digit IDs in metadata). Trailing data
// after the first value is rejected.
func decodeOpenAIJSONUseNumber(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
