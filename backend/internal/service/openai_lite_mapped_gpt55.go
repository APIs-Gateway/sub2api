package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyMappedGPT55LiteCompatibility drops the Responses Lite marker when an
// OAuth account's final upstream model is gpt-5.5. The account model mapping
// changes the model, not the upstream capability: the Lite endpoint does not
// serve gpt-5.5, while the Codex non-Lite endpoint accepts additional_tools,
// namespaces and reasoning.context=all_turns, so the Lite-normalized body and
// all history/tool results are preserved.
//
// Apply at the final HTTP request boundary, after account/model resolution.
// Never mutate the ingress headers/body: failover may select a native account.
func applyMappedGPT55LiteCompatibility(req *http.Request, account *Account, body []byte) error {
	if req == nil || account == nil || !account.IsOpenAIOAuth() {
		return nil
	}
	if strings.TrimSpace(gjson.GetBytes(body, "model").String()) != "gpt-5.5" {
		return nil
	}
	liteMetadata := isOpenAIResponsesLiteWebSocketPayload(body)
	if !isOpenAIResponsesLiteHeader(req.Header.Get(responsesLiteHeader)) && !liteMetadata {
		return nil
	}
	if liteMetadata {
		var err error
		body, err = sjson.DeleteBytes(body, "client_metadata."+responsesLiteWSMetadataKey)
		if err != nil {
			return fmt.Errorf("remove mapped GPT-5.5 Lite metadata: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		savedBody := append([]byte(nil), body...)
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(savedBody)), nil }
	}
	req.Header.Del(responsesLiteHeader)
	return nil
}
