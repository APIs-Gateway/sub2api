package service

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Only the ordinary HTTP Responses request builder calls this adapter. The
// passthrough builder, WebSocket forwarding, Chat fallback and compact endpoint
// retain their existing payloads.
func shouldAliasDeepSeekHTTPResponsesImages(c *gin.Context, account *Account, targetURL string) bool {
	if account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey ||
		GetOpenAIClientTransport(c) != OpenAIClientTransportHTTP ||
		!isOpenAIResponsesInboundPath(c) || isOpenAIResponsesCompactPath(c) ||
		isOpenAICompatMessagesBridgeContext(c) {
		return false
	}
	target, err := url.Parse(targetURL)
	return err == nil && strings.EqualFold(target.Scheme, "https") &&
		strings.EqualFold(target.Hostname(), "api.deepseek.com") &&
		strings.HasSuffix(target.Path, "/responses")
}

// DeepSeek's Responses decoder requires `url`, while Codex/OpenAI image parts
// usually carry `image_url`. Supply both fields only when an actual image URL
// exists. sjson preserves unrelated JSON values and the caller's raw body when
// no image needs conversion.
func aliasDeepSeekResponsesInputImages(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	updated := body
	changed := false
	index := 0
	input.ForEach(func(_, item gjson.Result) bool {
		itemPath := "input." + strconv.Itoa(index)
		index++
		var didChange bool
		updated, didChange = aliasDeepSeekImagePartAtPath(updated, itemPath, item)
		changed = changed || didChange
		parts := item.Get("content")
		if parts.IsArray() {
			partIndex := 0
			parts.ForEach(func(_, part gjson.Result) bool {
				path := itemPath + ".content." + strconv.Itoa(partIndex)
				partIndex++
				updated, didChange = aliasDeepSeekImagePartAtPath(updated, path, part)
				changed = changed || didChange
				return true
			})
		}
		return true
	})
	if !changed {
		return body
	}
	return updated
}

func aliasDeepSeekImagePartAtPath(body []byte, path string, part gjson.Result) ([]byte, bool) {
	partType := strings.TrimSpace(part.Get("type").String())
	switch partType {
	case "input_image", "image_url", "image":
	default:
		return body, false
	}
	imageURL := deepSeekImageURL(part)
	if imageURL == "" {
		return body, false
	}
	if partType == "input_image" && part.Get("image_url").Type == gjson.String &&
		part.Get("image_url").String() == imageURL && part.Get("url").Type == gjson.String &&
		part.Get("url").String() == imageURL {
		return body, false
	}
	updated := body
	for _, field := range []string{"type", "image_url", "url"} {
		value := imageURL
		if field == "type" {
			value = "input_image"
		}
		patched, err := sjson.SetBytes(updated, path+"."+field, value)
		if err != nil {
			return body, false
		}
		updated = patched
	}
	return updated, true
}

func deepSeekImageURL(part gjson.Result) string {
	for _, field := range []string{"url", "image_url", "image"} {
		value := part.Get(field)
		if raw := deepSeekJSONString(value); raw != "" {
			return raw
		}
		if value.IsObject() {
			for _, nested := range []string{"url", "image_url"} {
				if raw := deepSeekJSONString(value.Get(nested)); raw != "" {
					return raw
				}
			}
		}
	}
	source := part.Get("source")
	if !source.IsObject() {
		return ""
	}
	if raw := deepSeekJSONString(source.Get("url")); raw != "" {
		return raw
	}
	data := deepSeekJSONString(source.Get("data"))
	if data == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(data), "data:") {
		return data
	}
	mediaType := deepSeekJSONString(source.Get("media_type"))
	if mediaType == "" {
		mediaType = "image/png"
	}
	return "data:" + mediaType + ";base64," + data
}

func deepSeekJSONString(value gjson.Result) string {
	if value.Type != gjson.String {
		return ""
	}
	return strings.TrimSpace(value.String())
}
