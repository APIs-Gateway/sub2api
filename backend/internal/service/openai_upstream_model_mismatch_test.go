//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUpstreamModelMatches(t *testing.T) {
	cases := []struct {
		name, sent, got string
		want            bool
	}{
		{"exact", "gpt-5.6-sol", "gpt-5.6-sol", true},
		{"case-insensitive", "GPT-5.6-Sol", "gpt-5.6-sol ", true},
		{"empty got passes", "gpt-5.6-sol", "", true},
		{"mapped account 3233: sent luna got luna", "gpt-5.6-luna", "gpt-5.6-luna", true},
		{"date snapshot dashed", "gpt-5.6-sol", "gpt-5.6-sol-2026-09-01", true},
		{"date snapshot compact", "gpt-5.6-sol", "gpt-5.6-sol-20260901", true},
		{"latest alias", "gpt-5.6-sol-latest", "gpt-5.6-sol-2026-09-01", true},
		{"swapped family", "gpt-5.6-sol", "gpt-6-sol", false},
		{"swapped tier", "gpt-5.6-sol", "gpt-5.6-luna", false},
		{"non-date suffix", "gpt-5.6-sol", "gpt-5.6-sol-mini", false},
		{"empty sent never blocks", "", "gpt-6-sol", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, upstreamModelMatches(tc.sent, tc.got))
		})
	}
}

func TestExtractUpstreamResponseModel(t *testing.T) {
	require.Equal(t, "gpt-6-sol", extractUpstreamResponseModel([]byte(`{"type":"response.created","response":{"id":"r","model":"gpt-6-sol"}}`)))
	require.Equal(t, "gpt-6-sol", extractUpstreamResponseModel([]byte(`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-6-sol"}`)))
	require.Equal(t, "", extractUpstreamResponseModel([]byte(`{"type":"response.output_text.delta","delta":"x"}`)))
	require.Equal(t, "", extractUpstreamResponseModel(nil))
}

func TestMarkOpsUpstreamModelMismatch_FirstWins_ClearResets(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
	MarkOpsUpstreamModelMismatch(c, UpstreamModelMismatchMark{SentModel: "a", ResponseModel: "b", AccountID: 1})
	MarkOpsUpstreamModelMismatch(c, UpstreamModelMismatchMark{SentModel: "x", ResponseModel: "y", AccountID: 2})
	require.Equal(t, int64(1), GetOpsUpstreamModelMismatch(c).AccountID)
	ClearOpsUpstreamModelMismatch(c)
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

func TestCheckUpstreamModelMismatch_DisabledStillMarks(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.DisableUpstreamModelMismatchBlock = true
	err := svc.checkUpstreamModelMismatch(c, &Account{ID: 7, Platform: PlatformOpenAI}, "req", "gpt-5.6-sol", "gpt-6-sol", true, true, OpenAIUsage{})
	require.Nil(t, err)
	require.NotNil(t, GetOpsUpstreamModelMismatch(c))
}

func TestCheckUpstreamModelMismatch_CannotBlockOnlyMarks(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	err := svc.checkUpstreamModelMismatch(c, &Account{ID: 7, Platform: PlatformOpenAI}, "req", "gpt-5.6-sol", "gpt-6-sol", true, false, OpenAIUsage{})
	require.Nil(t, err)
	require.Equal(t, "gpt-6-sol", GetOpsUpstreamModelMismatch(c).ResponseModel)
}

func TestCheckUpstreamModelMismatch_EnabledReturnsFailover(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	err := svc.checkUpstreamModelMismatch(c, &Account{ID: 7, Platform: PlatformOpenAI}, "req", "gpt-5.6-sol", "gpt-6-sol", true, true, OpenAIUsage{})
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadGateway, err.StatusCode)
	require.Contains(t, string(err.ResponseBody), "upstream_model_mismatch")
	mark := GetOpsUpstreamModelMismatch(c)
	require.Equal(t, "gpt-5.6-sol", mark.SentModel)
	require.Equal(t, "gpt-6-sol", mark.ResponseModel)
}
