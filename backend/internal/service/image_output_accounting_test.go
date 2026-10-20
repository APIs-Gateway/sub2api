//go:build unit

package service

import "testing"

func TestOpenAIImageOutputCounter_TextOnlyResponseDoesNotCountImages(t *testing.T) {
	sseBody := `data: {"type":"response.output_item.done","item":{"id":"item_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}}

data: {"type":"response.completed","response":{"id":"resp_1","output":[{"id":"item_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":10,"output_tokens":5}}}

data: [DONE]`

	if got := countOpenAIImageOutputsFromSSEBody(sseBody); got != 0 {
		t.Fatalf("text-only responses must not count image outputs, got %d", got)
	}
}

func TestOpenAIImageOutputCounter_DataArrayRequiresActualImageOutput(t *testing.T) {
	sseBody := `data: {"type":"response.completed","response":{"id":"resp_1","output":[{"id":"item_1","type":"message","content":[{"type":"output_text","text":"Hello"}]}]},"data":[{"id":"not_an_image","status":"done"}]}

data: [DONE]`

	if got := countOpenAIImageOutputsFromSSEBody(sseBody); got != 0 {
		t.Fatalf("data array entries without url or b64_json must not count as images, got %d", got)
	}

	jsonBody := `{
		"id": "resp_1",
		"object": "response",
		"output": [{"type": "message", "content": [{"type": "output_text", "text": "Hello"}]}],
		"data": [{"id": "not_an_image", "status": "done"}]
	}`

	if got := countOpenAIResponseImageOutputsFromJSONBytes([]byte(jsonBody)); got != 0 {
		t.Fatalf("JSON data array entries without url or b64_json must not count as images, got %d", got)
	}

	sseWithImageURL := `data: {"type":"response.completed","response":{"id":"resp_2","output":[]},"data":[{"url":"https://example.com/img.png"}]}

data: [DONE]`
	if got := countOpenAIImageOutputsFromSSEBody(sseWithImageURL); got != 1 {
		t.Fatalf("data array entry with url should count as one image, got %d", got)
	}

	jsonWithB64 := `{"id":"resp_2","object":"response","output":[],"data":[{"b64_json":"aGVsbG8="}]}`
	if got := countOpenAIResponseImageOutputsFromJSONBytes([]byte(jsonWithB64)); got != 1 {
		t.Fatalf("JSON data array entry with b64_json should count as one image, got %d", got)
	}
}

func TestOpenAIImageOutputCounter_ImageGenerationCompletedRequiresResult(t *testing.T) {
	sseBody := `data: {"type":"image_generation.completed","item":{"type":"image_generation.completed","id":"call_1"}}

data: [DONE]`

	if got := countOpenAIImageOutputsFromSSEBody(sseBody); got != 0 {
		t.Fatalf("image_generation.completed without result must not count as an output image, got %d", got)
	}
}
