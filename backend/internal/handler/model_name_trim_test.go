package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 只有空白的 model 去掉空白后等同于缺失：各入口必须在调度、转发之前就返回 400，
// 而不是带着一个看不见的名字往下走。
func TestGatewayEntrypoints_BlankModelIsRejectedAsMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	openAIHandler := newOpenAIImageChatRejectionHandler(t)
	cases := []struct {
		name string
		path string
		body string
		call func(c *gin.Context)
	}{
		{
			name: "gateway chat completions",
			path: "/v1/chat/completions",
			body: `{"model":"   ","messages":[{"role":"user","content":"hi"}]}`,
			call: (&GatewayHandler{}).ChatCompletions,
		},
		{
			name: "gateway responses",
			path: "/v1/responses",
			body: `{"model":"   ","input":"hi"}`,
			call: (&GatewayHandler{}).Responses,
		},
		{
			name: "openai chat completions",
			path: "/openai/v1/chat/completions",
			body: `{"model":"   ","messages":[{"role":"user","content":"hi"}]}`,
			call: openAIHandler.ChatCompletions,
		},
		{
			name: "openai responses",
			path: "/openai/v1/responses",
			body: `{"model":"   ","input":"hi"}`,
			call: openAIHandler.Responses,
		},
		{
			name: "openai messages",
			path: "/openai/v1/messages",
			body: `{"model":"   ","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			call: openAIHandler.Messages,
		},
		{
			name: "openai embeddings",
			path: "/openai/v1/embeddings",
			body: `{"model":"   ","input":"hi"}`,
			call: openAIHandler.Embeddings,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			setImageChatTestAuth(c)

			tc.call(c)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "model is required")
		})
	}
}

// 带首尾空白的 model 走完整个 OpenAI 入口后，用量日志里的名字、请求模型和费用必须与干净名字完全一致：
// 入口只去一次空白，之后的映射、调度、转发、计价、日志看到的是同一个名字。
func TestOpenAIEntrypoints_TrimModelWhitespaceBeforeRecordingUsage(t *testing.T) {
	cases := []struct {
		name        string
		route       string
		inboundWant string
		body        func(model string) string
		register    func(router *gin.Engine, h *OpenAIGatewayHandler)
	}{
		{
			name:        "chat completions",
			route:       "/openai/v1/chat/completions",
			inboundWant: EndpointChatCompletions,
			body: func(model string) string {
				return `{"model":"` + model + `","messages":[{"role":"user","content":"hello"}]}`
			},
			register: func(router *gin.Engine, h *OpenAIGatewayHandler) {
				router.POST("/openai/v1/chat/completions", h.ChatCompletions)
			},
		},
		{
			name:        "responses",
			route:       "/openai/v1/responses",
			inboundWant: EndpointResponses,
			body: func(model string) string {
				return `{"model":"` + model + `","input":"hello"}`
			},
			register: func(router *gin.Engine, h *OpenAIGatewayHandler) {
				router.POST("/openai/v1/responses", h.Responses)
			},
		},
		{
			name:        "messages",
			route:       "/openai/v1/messages",
			inboundWant: EndpointMessages,
			body: func(model string) string {
				return `{"model":"` + model + `","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`
			},
			register: func(router *gin.Engine, h *OpenAIGatewayHandler) {
				router.POST("/openai/v1/messages", h.Messages)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := func(model string) *service.UsageLog {
				return runOpenAIRawChatUsageEndpointCase(t, openAIRawChatUsageEndpointCase{
					route:       tc.route,
					inboundWant: tc.inboundWant,
					body:        tc.body(model),
					register:    tc.register,
				})
			}

			clean := run("gpt-4o-mini")
			dirty := run("  gpt-4o-mini ")

			require.Equal(t, strings.TrimSpace(dirty.Model), dirty.Model, "用量日志的 model 不能带空白")
			require.Equal(t, strings.TrimSpace(dirty.RequestedModel), dirty.RequestedModel, "用量日志的 requested_model 不能带空白")
			require.Equal(t, clean.Model, dirty.Model)
			require.Equal(t, clean.RequestedModel, dirty.RequestedModel)
			require.Equal(t, clean.TotalCost, dirty.TotalCost)
			require.Equal(t, clean.ActualCost, dirty.ActualCost)
		})
	}
}

func TestParseGeminiModelAction_TrimsModelWhitespace(t *testing.T) {
	tests := []struct {
		name       string
		rest       string
		wantModel  string
		wantAction string
	}{
		{name: "colon form", rest: "gemini-2.5-pro:generateContent", wantModel: "gemini-2.5-pro", wantAction: "generateContent"},
		{name: "space before colon", rest: "gemini-2.5-pro :streamGenerateContent", wantModel: "gemini-2.5-pro", wantAction: "streamGenerateContent"},
		{name: "space around whole path", rest: " gemini-2.5-pro:generateContent ", wantModel: "gemini-2.5-pro", wantAction: "generateContent"},
		{name: "slash form", rest: "gemini-2.5-pro /generateContent", wantModel: "gemini-2.5-pro", wantAction: "generateContent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, action, err := parseGeminiModelAction(tt.rest)
			require.NoError(t, err)
			require.Equal(t, tt.wantModel, model)
			require.Equal(t, tt.wantAction, action)
		})
	}

	for _, rest := range []string{"", "   ", "gemini-2.5-pro"} {
		_, _, err := parseGeminiModelAction(rest)
		require.Error(t, err, "%q", rest)
	}
}
