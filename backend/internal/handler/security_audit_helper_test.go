package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stagePromptEngine struct {
	mu     sync.Mutex
	stages []string
}

func (e *stagePromptEngine) EffectiveMode() securityaudit.Mode { return securityaudit.ModeBlocking }

func (e *stagePromptEngine) Enqueue(context.Context, securityaudit.Request) error { return nil }

func (e *stagePromptEngine) Evaluate(_ context.Context, req securityaudit.Request) (*securityaudit.PromptDecision, error) {
	e.mu.Lock()
	e.stages = append(e.stages, req.Stage)
	e.mu.Unlock()
	return &securityaudit.PromptDecision{Kind: securityaudit.DecisionAllow, AllowNextStage: true}, nil
}

func (e *stagePromptEngine) snapshotStages() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.stages...)
}

func TestSecurityAuditWebSocketStagesAreNotSuppressedByHTTPCompletionMarker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	groupID := int64(3)
	apiKey := &service.APIKey{ID: 9, UserID: 7, GroupID: &groupID}
	subject := middleware2.AuthSubject{UserID: 7}
	engine := &stagePromptEngine{}
	coordinator := securityaudit.NewCoordinator(nil, engine)
	body := []byte(`{"model":"gpt-test","input":[{"role":"user","content":"check me"}]}`)

	first := runSecurityAudit(ctx, nil, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", body, "first_turn")
	second := runSecurityAudit(ctx, nil, coordinator, nil, apiKey, subject, securityauditProtocolResponses, "gpt-test", body, "subsequent_turn")

	require.True(t, first.AllowNextStage)
	require.True(t, second.AllowNextStage)
	require.Equal(t, []string{"first_turn", "subsequent_turn"}, engine.snapshotStages())
}

const securityauditProtocolResponses = "openai_responses"
