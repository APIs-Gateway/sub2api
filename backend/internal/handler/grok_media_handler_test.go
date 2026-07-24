//go:build unit

package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type grokMediaHandlerUpstream struct {
	service.HTTPUpstream
	lastReq  *http.Request
	lastBody []byte
	status   int
	body     string
}

type grokMediaStatusFailoverUpstream struct {
	service.HTTPUpstream
	accountIDs []int64
}

func (u *grokMediaStatusFailoverUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.accountIDs = append(u.accountIDs, accountID)
	status := http.StatusOK
	body := `{"id":"request-123","status":"completed"}`
	if len(u.accountIDs) == 1 {
		status = http.StatusNotFound
		body = `{"error":{"message":"video belongs to another account"}}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

type grokMediaAccountRepo struct {
	openAIImagesFailoverAccountRepo
}

type grokMediaFailingReader struct{}

type grokMediaBillingCacheStub struct {
	service.BillingCache
	balance float64
}

func (s grokMediaBillingCacheStub) GetUserBalance(context.Context, int64) (float64, error) {
	return s.balance, nil
}

type grokMediaRPMCacheStub struct {
	service.UserRPMCache
}

func (grokMediaRPMCacheStub) IncrementUserGroupRPM(context.Context, int64, int64) (int, error) {
	return 2, nil
}

func (grokMediaFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("request body read failed")
}

func (grokMediaAccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}

func (u *grokMediaHandlerUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.lastReq = req
	if req.Body != nil {
		u.lastBody, _ = io.ReadAll(req.Body)
	}
	status := u.status
	if status == 0 {
		status = http.StatusOK
	}
	body := u.body
	if body == "" {
		body = `{"id":"grok-media-req","status":"completed"}`
	}
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"grok-media-req"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}, nil
}

func newGrokMediaHandler(t *testing.T, upstream service.HTTPUpstream) *OpenAIGatewayHandler {
	return newGrokMediaHandlerWithOptions(
		t,
		upstream,
		[]service.Account{newGrokMediaTestAccount()},
		[]bool{true},
		[]bool{true},
	)
}

func newGrokMediaTestAccount() service.Account {
	return service.Account{
		ID:          7402,
		Name:        "grok-media",
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "grok-key",
			"base_url": "https://xai.test/v1",
			"model_mapping": map[string]any{
				"grok-imagine":           "grok-imagine",
				"grok-imagine-edit":      "grok-imagine-edit",
				"grok-imagine-video-1.5": "grok-imagine-video-1.5",
			},
		},
	}
}

func newGrokMediaHandlerWithOptions(t *testing.T, upstream service.HTTPUpstream, accounts []service.Account, accountSeq, userSeq []bool) *OpenAIGatewayHandler {
	t.Helper()
	accountRepo := grokMediaAccountRepo{
		openAIImagesFailoverAccountRepo: openAIImagesFailoverAccountRepo{accounts: accounts},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		nil,
		nil,
		nil,
		upstream,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billingService.Stop)
	h := NewOpenAIGatewayHandler(
		gatewayService,
		service.NewConcurrencyService(&helperConcurrencyCacheStub{
			accountSeq: accountSeq,
			userSeq:    userSeq,
		}),
		billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, cfg,
	)
	h.maxAccountSwitches = 1
	return h
}

func newGrokMediaHandlerContext(t *testing.T, path, body string, allowImages bool) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	groupID := int64(7401)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      7403,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			Platform:             service.PlatformGrok,
			AllowImageGeneration: allowImages,
		},
		User: &service.User{ID: 7404},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7404, Concurrency: 1})
	return c, rec
}

func TestGrokMediaHandlerVideoGenerationForwardsAndRecordsResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &grokMediaHandlerUpstream{}
	h := newGrokMediaHandler(t, upstream)
	h.errorPassthroughService = &service.ErrorPassthroughService{}
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5","prompt":"waves"}`, true)
	h.GrokVideoGeneration(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "POST", upstream.lastReq.Method)
	require.Equal(t, "https://xai.test/v1/videos/generations", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer grok-key", upstream.lastReq.Header.Get("Authorization"))
	require.Contains(t, string(upstream.lastBody), "grok-imagine-video-1.5")
}

func TestGrokMediaHandlerImagesAndStatusForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		path        string
		body        string
		requestID   string
		endpoint    service.GrokMediaEndpoint
		method      string
		expectedURL string
	}{
		{
			name:        "image generation",
			path:        "/v1/images/generations",
			body:        `{"model":"grok-imagine","prompt":"cat"}`,
			endpoint:    service.GrokMediaEndpointImagesGenerations,
			method:      http.MethodPost,
			expectedURL: "https://xai.test/v1/images/generations",
		},
		{
			name:        "image edit",
			path:        "/v1/images/edits",
			body:        `{"model":"grok-imagine-edit","prompt":"cat"}`,
			endpoint:    service.GrokMediaEndpointImagesEdits,
			method:      http.MethodPost,
			expectedURL: "https://xai.test/v1/images/edits",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &grokMediaHandlerUpstream{}
			h := newGrokMediaHandler(t, upstream)
			h.cfg.Gateway.ImageConcurrency.Enabled = true
			h.cfg.Gateway.ImageConcurrency.MaxConcurrentRequests = 1
			c, rec := newGrokMediaHandlerContext(t, tt.path, tt.body, true)

			h.GrokImages(c)

			require.Equal(t, http.StatusOK, rec.Code)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, tt.method, upstream.lastReq.Method)
			require.Equal(t, tt.expectedURL, upstream.lastReq.URL.String())
		})
	}

	upstream := &grokMediaHandlerUpstream{}
	h := newGrokMediaHandler(t, upstream)
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/request-123", "", true)
	c.Request.Method = http.MethodGet
	c.Params = gin.Params{{Key: "request_id", Value: "request-123"}}

	h.GrokVideoStatus(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, http.MethodGet, upstream.lastReq.Method)
	require.Equal(t, "https://xai.test/v1/videos/request-123", upstream.lastReq.URL.String())
}

func TestGrokMediaHandlerRejectsGenerationWithoutImagePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newGrokMediaHandler(t, &grokMediaHandlerUpstream{})
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5","prompt":"waves"}`, false)

	h.GrokVideoGeneration(c)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), service.VideoGenerationPermissionMessage())
}

func TestGrokMediaHandlerRejectsMissingVideoRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newGrokMediaHandler(t, &grokMediaHandlerUpstream{})
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/", "", true)
	c.Request.Method = http.MethodGet

	h.GrokVideoStatus(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "request_id is required")
}

func TestGrokMediaHandlerRejectsMissingAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &OpenAIGatewayHandler{}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/request-123", nil).WithContext(context.Background())

	h.GrokVideoStatus(c)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestGrokMediaHandlerRejectsMissingModelAndUserContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newGrokMediaHandler(t, &grokMediaHandlerUpstream{})
	c, rec := newGrokMediaHandlerContext(t, "/v1/images/generations", `{"prompt":"cat"}`, true)

	h.GrokImages(c)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "model is required")

	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", strings.NewReader(`{"model":"grok-imagine-video-1.5"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	groupID := int64(7401)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformGrok, AllowImageGeneration: true},
	})

	h.GrokVideoGeneration(c)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "User context not found")
}

func TestGrokMediaHandlerMissingDependenciesReturnServiceUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", strings.NewReader(`{"model":"grok-imagine-video-1.5"}`))
	groupID := int64(7401)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformGrok, AllowImageGeneration: true},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7404, Concurrency: 1})

	(&OpenAIGatewayHandler{}).GrokVideoGeneration(c)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "Service temporarily unavailable")
}

func TestGrokMediaHandlerRejectsEmptyBodyAndConcurrencyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newGrokMediaHandler(t, &grokMediaHandlerUpstream{})
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/generations", "", true)

	h.GrokVideoGeneration(c)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Request body is empty")

	h = newGrokMediaHandlerWithOptions(
		t,
		&grokMediaHandlerUpstream{},
		[]service.Account{newGrokMediaTestAccount()},
		[]bool{true},
		[]bool{false},
	)
	c, rec = newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5"}`, true)

	h.GrokVideoGeneration(c)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestGrokMediaHandlerRejectsRequestBodyReadFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newGrokMediaHandler(t, &grokMediaHandlerUpstream{})
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5"}`, true)
	c.Request.Body = io.NopCloser(grokMediaFailingReader{})

	h.GrokVideoGeneration(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Failed to read request body")
}

func TestGrokMediaHandlerRejectsOversizedRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newGrokMediaHandler(t, &grokMediaHandlerUpstream{})
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5"}`, true)
	c.Request.Body = http.MaxBytesReader(rec, c.Request.Body, 4)

	h.GrokVideoGeneration(c)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Contains(t, rec.Body.String(), "Request body too large")
}

func TestGrokMediaHandlerRejectsImageConcurrencyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newGrokMediaHandler(t, &grokMediaHandlerUpstream{})
	h.cfg.Gateway.ImageConcurrency.Enabled = true
	h.cfg.Gateway.ImageConcurrency.MaxConcurrentRequests = 1
	release, acquired := h.imageLimiter.TryAcquire(true, 1)
	require.True(t, acquired)
	defer release()
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5"}`, true)

	h.GrokVideoGeneration(c)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Contains(t, rec.Body.String(), "Image generation concurrency limit exceeded")
}

func TestGrokMediaHandlerBillingEligibilityFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newGrokMediaHandler(t, &grokMediaHandlerUpstream{})
	cfg := &config.Config{RunMode: config.RunModeStandard}
	billing := service.NewBillingCacheService(grokMediaBillingCacheStub{}, nil, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billing.Stop)
	h.billingCacheService = billing
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5"}`, true)

	h.GrokVideoGeneration(c)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "billing_error")
}

func TestGrokMediaHandlerBillingRPMFailureSetsRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newGrokMediaHandler(t, &grokMediaHandlerUpstream{})
	cfg := &config.Config{RunMode: config.RunModeStandard}
	billing := service.NewBillingCacheService(
		grokMediaBillingCacheStub{balance: 1},
		nil,
		nil,
		nil,
		grokMediaRPMCacheStub{},
		nil,
		cfg,
		nil,
		nil,
	)
	t.Cleanup(billing.Stop)
	h.billingCacheService = billing
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5"}`, true)
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	require.True(t, ok)
	apiKey.Group.RPMLimit = 1

	h.GrokVideoGeneration(c)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Contains(t, rec.Body.String(), "rate_limit_exceeded")
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
}

func TestGrokMediaHandlerSecurityAuditBlocksGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newGrokMediaHandler(t, &grokMediaHandlerUpstream{})
	h.securityAuditCoordinator = securityaudit.NewCoordinator(nil, blockedPromptEngine{})
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5","prompt":"blocked"}`, true)

	h.GrokVideoGeneration(c)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "permission_error")
}

func TestGrokMediaHandlerRejectsMissingAccountAndAccountSlot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newGrokMediaHandlerWithOptions(t, &grokMediaHandlerUpstream{}, nil, []bool{true}, []bool{true})
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5"}`, true)

	h.GrokVideoGeneration(c)
	require.NotEmpty(t, rec.Body.String())

	h = newGrokMediaHandlerWithOptions(
		t,
		&grokMediaHandlerUpstream{},
		[]service.Account{newGrokMediaTestAccount()},
		[]bool{false},
		[]bool{true},
	)
	c, rec = newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5"}`, true)

	h.GrokVideoGeneration(c)
	require.NotEmpty(t, rec.Body.String())
}

func TestGrokMediaHandlerFailsOverOnUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &grokMediaHandlerUpstream{
		status: http.StatusBadGateway,
		body:   `{"error":{"message":"upstream down"}}`,
	}
	h := newGrokMediaHandler(t, upstream)
	h.maxAccountSwitches = 0
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5"}`, true)

	h.GrokVideoGeneration(c)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "temporarily unavailable")
}

func TestGrokMediaHandlerVideoStatus404SwitchesAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &grokMediaStatusFailoverUpstream{}
	first := newGrokMediaTestAccount()
	second := newGrokMediaTestAccount()
	second.ID = first.ID + 1
	second.Name = "grok-media-2"
	h := newGrokMediaHandlerWithOptions(t, upstream, []service.Account{first, second}, []bool{true, true}, []bool{true, true})
	h.maxAccountSwitches = 1
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/request-123", "", true)
	c.Request.Method = http.MethodGet
	c.Params = gin.Params{{Key: "request_id", Value: "request-123"}}

	h.GrokVideoStatus(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []int64{first.ID, second.ID}, upstream.accountIDs)
}

func TestGrokMediaHandlerReturnsBadGatewayForInvalidUpstreamURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newGrokMediaTestAccount()
	account.Credentials["base_url"] = "https://xai.test/v1?unsafe=1"
	h := newGrokMediaHandlerWithOptions(t, &grokMediaHandlerUpstream{}, []service.Account{account}, []bool{true}, []bool{true})
	c, rec := newGrokMediaHandlerContext(t, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5"}`, true)

	h.GrokVideoGeneration(c)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "Upstream request failed")
}

func TestGrokMediaModerationBody(t *testing.T) {
	valid := []byte(`{"model":"grok-imagine"}`)
	require.Equal(t, valid, grokMediaModerationBody("application/json", valid))
	require.Nil(t, grokMediaModerationBody("application/json", []byte("not-json")))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "grok-imagine-edit"))
	require.NoError(t, writer.WriteField("prompt", "blocked multipart prompt"))
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="image"; filename="source.png"`},
		"Content-Type":        {"image/png"},
	})
	require.NoError(t, err)
	_, err = part.Write([]byte("fake-image"))
	require.NoError(t, err)
	contentType := writer.FormDataContentType()
	require.NoError(t, writer.Close())

	moderationBody := grokMediaModerationBody(contentType, body.Bytes())
	input := service.ExtractContentModerationInput(service.ContentModerationProtocolOpenAIImages, moderationBody)
	require.Equal(t, "blocked multipart prompt", input.Text)
	require.Len(t, input.Images, 1)
}

func TestGrokMediaHandlerSecurityAuditBlocksMultipartGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &grokMediaHandlerUpstream{}
	h := newGrokMediaHandler(t, upstream)
	h.securityAuditCoordinator = securityaudit.NewCoordinator(nil, blockedPromptEngine{})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "grok-imagine-edit"))
	require.NoError(t, writer.WriteField("prompt", "blocked multipart prompt"))
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="image"; filename="source.png"`},
		"Content-Type":        {"image/png"},
	})
	require.NoError(t, err)
	_, err = part.Write([]byte("fake-image"))
	require.NoError(t, err)
	contentType := writer.FormDataContentType()
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	groupID := int64(7401)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID: 7403, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformGrok, AllowImageGeneration: true},
		User:  &service.User{ID: 7404},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7404, Concurrency: 1})

	h.GrokImages(c)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "permission_error")
	require.Nil(t, upstream.lastReq)
}
