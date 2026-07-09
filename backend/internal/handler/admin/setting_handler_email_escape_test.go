package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func TestBuildSMTPTestEmailBody_EscapesSiteName(t *testing.T) {
	body := buildSMTPTestEmailBody(`<script>alert("x")</script>`)

	if strings.Contains(body, `<script>alert("x")</script>`) {
		t.Fatalf("body contains unescaped site name: %s", body)
	}
	if !strings.Contains(body, `&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`) {
		t.Fatalf("body missing escaped site name: %s", body)
	}
}

func TestSettingHandlerSendTestEmail_BuildsBodyBeforeSendFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{
		values: map[string]string{
			service.SettingKeySiteName: `<script>alert("x")</script>`,
		},
	}
	settingSvc := service.NewSettingService(repo, &config.Config{})
	emailSvc := service.NewEmailService(repo, nil)
	handler := NewSettingHandler(settingSvc, emailSvc, nil, nil, nil, nil, nil)

	rawBody, err := json.Marshal(SendTestEmailRequest{
		Email:    "admin@example.com",
		SMTPHost: "127.0.0.1",
		SMTPPort: 1,
		SMTPFrom: "noreply@example.com",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/send-test-email", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.SendTestEmail(c)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status mismatch: got %d want %d, body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Failed to send test email") {
		t.Fatalf("response should report send failure, got %s", recorder.Body.String())
	}
}
