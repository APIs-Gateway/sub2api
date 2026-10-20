package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractOpenAIUsageMergesHostedImageGenToolUsage(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantInputImage  int
		wantOutputImage int
	}{
		{
			name:            "nested response usage",
			body:            `{"response":{"usage":{"input_tokens":43792,"output_tokens":1005},"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":7620},"output_tokens_details":{"image_tokens":186}}}}}`,
			wantInputImage:  7620,
			wantOutputImage: 186,
		},
		{
			name:            "top level usage",
			body:            `{"usage":{"input_tokens":5000,"output_tokens":200},"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":2800},"output_tokens_details":{"image_tokens":150}}}}`,
			wantInputImage:  2800,
			wantOutputImage: 150,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usage, ok := extractOpenAIUsageFromJSONBytes([]byte(test.body))
			require.True(t, ok)
			require.Equal(t, test.wantInputImage, usage.ImageInputTokens)
			require.Equal(t, test.wantOutputImage, usage.ImageOutputTokens)
		})
	}
}

func TestExtractOpenAIUsageHostedImageGenDoesNotOverrideExistingImageTokens(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":100,"output_tokens":50,"input_tokens_details":{"image_tokens":30},"output_tokens_details":{"image_tokens":40}},"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":300},"output_tokens_details":{"image_tokens":400}}}}`)

	usage, ok := extractOpenAIUsageFromJSONBytes(body)
	require.True(t, ok)
	require.Equal(t, 30, usage.ImageInputTokens)
	require.Equal(t, 40, usage.ImageOutputTokens)
}

func TestParseSSEUsageBytesMergesHostedImageGenToolUsage(t *testing.T) {
	data := []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":10000,"output_tokens":500},"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":3800},"output_tokens_details":{"image_tokens":186}}}}}`)
	usage := &OpenAIUsage{}

	(&OpenAIGatewayService{}).parseSSEUsageBytes(data, usage)

	require.Equal(t, 10000, usage.InputTokens)
	require.Equal(t, 500, usage.OutputTokens)
	require.Equal(t, 3800, usage.ImageInputTokens)
	require.Equal(t, 186, usage.ImageOutputTokens)
}
