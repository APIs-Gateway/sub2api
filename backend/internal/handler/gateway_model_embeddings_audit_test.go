//go:build unit

package handler

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type embeddingsModelAuditAccountRepo struct {
	openAIImagesFailoverAccountRepo
	routingCalls atomic.Int64
}

func (r *embeddingsModelAuditAccountRepo) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	r.routingCalls.Add(1)
	return r.openAIImagesFailoverAccountRepo.GetByID(ctx, id)
}

func (r *embeddingsModelAuditAccountRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]service.Account, error) {
	r.routingCalls.Add(1)
	return r.openAIImagesFailoverAccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, groupID, platform)
}

func (r *embeddingsModelAuditAccountRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	r.routingCalls.Add(1)
	return r.openAIImagesFailoverAccountRepo.ListSchedulableByPlatform(ctx, platform)
}

func (r *embeddingsModelAuditAccountRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	r.routingCalls.Add(1)
	return r.openAIImagesFailoverAccountRepo.ListSchedulableUngroupedByPlatform(ctx, platform)
}

func (r *embeddingsModelAuditAccountRepo) ListByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	r.routingCalls.Add(1)
	return r.openAIImagesFailoverAccountRepo.ListByPlatform(ctx, platform)
}

func TestGatewayModelEmbeddings_HandlerRejectsBeforeRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, fields string
		ambiguous    bool
	}{
		{"plain_duplicate", `"model":"text-embedding-3-small","model":"text-embedding-3-large"`, true},
		{"escaped_duplicate", `"model":"text-embedding-3-small","\u006dodel":"text-embedding-3-large"`, true},
		{"case_duplicate", `"model":"text-embedding-3-small","Model":"text-embedding-3-large"`, true},
		{"equal_duplicate", `"model":"text-embedding-3-small","model":"text-embedding-3-small"`, true},
		{"canonical_control", `"model":"text-embedding-3-small"`, false},
		{"nested_control", `"model":"text-embedding-3-small","metadata":{"model":"nested-first","model":"nested-last"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// All dependencies are usable on OLD. Its actual upstream attempt
			// cancels the request just like the existing failover fixture, so
			// this regression fails on real routing, never on nil dependencies.
			upstream := &openAIResponsesFailoverCancelUpstream{onFirstDo: cancel}
			repo := &embeddingsModelAuditAccountRepo{openAIImagesFailoverAccountRepo: openAIImagesFailoverAccountRepo{
				accounts: []service.Account{{
					ID: 7890, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "sk-embeddings-fixture", "openai_capabilities": []any{"embeddings"}},
				}},
			}}
			h := newOpenAIResponsesFailoverTestHandlerWithRepo(t, upstream, repo)
			c, rec := newOpenAIFailoverTestContext(t, ctx, "/v1/embeddings", `{`+tc.fields+`,"input":["hello","world"]}`, false)
			require.NotPanics(t, func() { h.Embeddings(c) })
			t.Logf("routing_calls=%d upstream_accounts=%v status=%d body=%s", repo.routingCalls.Load(), upstream.calls(), c.Writer.Status(), rec.Body.String())
			if tc.ambiguous {
				require.Zero(t, repo.routingCalls.Load(), "ambiguous input must stop before real account routing")
				require.Empty(t, upstream.calls(), "ambiguous input must not invoke upstream")
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
				require.Contains(t, rec.Body.String(), "canonical field name")
				return
			}
			require.Positive(t, repo.routingCalls.Load(), "normal control must prove routing is actually usable")
			require.Equal(t, []int64{7890}, upstream.calls())
			require.Equal(t, statusClientClosedRequest, c.Writer.Status())
		})
	}
}
