package service

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	blockTypeServerToolUse       = "server_tool_use"
	blockTypeWebSearchToolResult = "web_search_tool_result"
)

var (
	patternServerToolUse       = []byte(`"server_tool_use"`)
	patternWebSearchToolResult = []byte(`"web_search_tool_result"`)
)

func FilterWebSearchHistoryBlocks(body []byte, mappedModel string) []byte {
	if !bytes.Contains(body, patternServerToolUse) && !bytes.Contains(body, patternWebSearchToolResult) {
		return body
	}

	stripAll := ResolveThinkingProtocol(mappedModel) == ThinkingProtocolPassbackRequired
	msgsRes := gjson.GetBytes(body, "messages")
	if !msgsRes.Exists() || !msgsRes.IsArray() {
		return body
	}

	modified := false
	messages := msgsRes.Array()
	newMessages := make([]string, 0, len(messages))
	for _, msg := range messages {
		content := msg.Get("content")
		if !content.IsArray() {
			newMessages = append(newMessages, msg.Raw)
			continue
		}

		blocks := content.Array()
		var newContent []string
		for i, block := range blocks {
			if shouldStripWebSearchBlock(block, stripAll) {
				if newContent == nil {
					newContent = make([]string, 0, len(blocks))
					for _, kept := range blocks[:i] {
						newContent = append(newContent, kept.Raw)
					}
				}
				continue
			}
			if newContent != nil {
				newContent = append(newContent, block.Raw)
			}
		}
		if newContent == nil {
			newMessages = append(newMessages, msg.Raw)
			continue
		}
		modified = true
		if len(newContent) == 0 {
			role := msg.Get("role").String()
			placeholder := "(content removed)"
			if role == "assistant" {
				placeholder = "(assistant content removed)"
			}
			newContent = []string{`{"type":"text","text":"` + placeholder + `"}`}
		}
		msgRaw, err := sjson.SetRaw(msg.Raw, "content", "["+strings.Join(newContent, ",")+"]")
		if err != nil {
			return body
		}
		newMessages = append(newMessages, msgRaw)
	}

	if !modified {
		return body
	}

	out, err := sjson.SetRawBytes(body, "messages", []byte("["+strings.Join(newMessages, ",")+"]"))
	if err != nil {
		return body
	}
	return out
}

func shouldStripWebSearchBlock(block gjson.Result, stripAll bool) bool {
	if !block.IsObject() {
		return false
	}
	blockType := block.Get("type").String()
	switch blockType {
	case blockTypeServerToolUse:
		if stripAll {
			return true
		}
		return strings.HasPrefix(block.Get("id").String(), webSearchToolUseIDPrefix)
	case blockTypeWebSearchToolResult:
		if stripAll {
			return true
		}
		return strings.HasPrefix(block.Get("tool_use_id").String(), webSearchToolUseIDPrefix)
	default:
		return false
	}
}
