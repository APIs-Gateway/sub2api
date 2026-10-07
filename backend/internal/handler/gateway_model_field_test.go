package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGatewayModelField_HTTPRejectsBeforeRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	openAI := &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: &service.BillingCacheService{},
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   &ConcurrencyHelper{concurrencyService: &service.ConcurrencyService{}},
	}
	gateway := &GatewayHandler{}
	for _, route := range []struct {
		name, path string
		handle     func(*gin.Context)
	}{
		{"responses", "/v1/responses", openAI.Responses},
		{"compact", "/v1/responses/compact", openAI.Responses},
		{"chat", "/v1/chat/completions", openAI.ChatCompletions},
		{"openai_messages", "/v1/messages", openAI.Messages},
		{"anthropic_responses", "/v1/responses", gateway.Responses},
		{"anthropic_messages", "/v1/messages", gateway.Messages},
	} {
		for _, fields := range []string{
			`"model":"gpt-5.6-luna","model":"gpt-6-astra"`,
			`"model":"gpt-6-astra","model":"gpt-5.6-luna"`,
			`"model":"gpt-5.6-luna","model":"gpt-5.6-luna"`,
			`"model":"gpt-5.6-luna","\u006dodel":"gpt-6-astra"`,
			`"model":"gpt-5.6-luna","Model":"gpt-6-astra"`,
			`"MODEL":"gpt-6-astra","model":"gpt-5.6-luna"`,
			`"Model":"gpt-6-astra"`,
		} {
			t.Run(route.name+"/"+fields, func(t *testing.T) {
				body := []byte(`{` + fields + `,"stream":false,"input":"audit","messages":[{"role":"user","content":"audit"}]}`)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, route.path, bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
					ID: 7890, User: &service.User{ID: 7890},
					Group: &service.Group{Platform: service.PlatformOpenAI, AllowMessagesDispatch: true},
				})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7890, Concurrency: 1})
				// No usable scheduler/billing backend is installed. Any attempt
				// to reach routing instead of rejecting cannot pass this case.
				route.handle(c)
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
				require.Contains(t, rec.Body.String(), "canonical field name")
			})
		}
	}
}

func TestGatewayModelField_WSFirstFrameRejectsBeforeProxy(t *testing.T) {
	for _, messageType := range []coderws.MessageType{coderws.MessageText, coderws.MessageBinary} {
		t.Run(map[coderws.MessageType]string{coderws.MessageText: "text", coderws.MessageBinary: "binary"}[messageType], func(t *testing.T) {
			reports := make(chan bool, 1)
			h := newOpenAIResponsesWebSocketAttributionHandler(t, nil, reports)
			called := make(chan struct{}, 1)
			h.responsesWebSocketProxy = func(context.Context, *gin.Context, *coderws.Conn, *service.Account, string, []byte, *service.OpenAIWSIngressHooks) error {
				called <- struct{}{}
				return nil
			}
			server := newOpenAIResponsesWebSocketAttributionServer(t, h)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+server.URL[len("http"):]+"/openai/v1/responses", nil)
			require.NoError(t, err)
			defer func() { _ = client.CloseNow() }()
			require.NoError(t, client.Write(ctx, messageType, []byte(`{"type":"response.create","model":"gpt-5.6-luna","\u006dodel":"gpt-6-astra","input":[]}`)))
			_, event, err := client.Read(ctx)
			require.NoError(t, err)
			require.Equal(t, "invalid_request_error", gjson.GetBytes(event, "error.type").String())
			_, _, err = client.Read(ctx)
			require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
			require.Empty(t, called, "invalid first frame must not enter the forwarding proxy")
			require.Empty(t, reports, "client model ambiguity must not penalize an upstream account")
		})
	}
}
