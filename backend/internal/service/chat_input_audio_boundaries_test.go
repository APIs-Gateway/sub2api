//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestChatInputAudio_LocalRejectPreservesSchedulerHealth(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	svc := &OpenAIGatewayService{rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("true")}
	ttft := 123
	svc.ReportOpenAIAccountScheduleResult(159801, false, nil)
	svc.ReportOpenAIAccountScheduleResult(159801, true, &ttft)
	beforeRate, beforeTTFT, beforeHas := svc.openaiAccountStats.snapshot(159801)
	beforeMetrics := svc.SnapshotOpenAIAccountSchedulerMetrics()
	for _, refusal := range []error{apicompat.ErrUnsupportedInputAudio, apicompat.ErrInvalidInputAudio} {
		svc.ReportOpenAIAccountScheduleError(159801, fmt.Errorf("conversion: %w", refusal))
		rate, latency, has := svc.openaiAccountStats.snapshot(159801)
		require.Equal(t, beforeRate, rate)
		require.Equal(t, beforeTTFT, latency)
		require.Equal(t, beforeHas, has)
		require.Equal(t, beforeMetrics, svc.SnapshotOpenAIAccountSchedulerMetrics())
	}
	svc.ReportOpenAIAccountScheduleError(159801, errors.New("actual provider fault"))
	afterRate, _, _ := svc.openaiAccountStats.snapshot(159801)
	require.Greater(t, afterRate, beforeRate)
}

type chatAudioCancelWriter struct {
	gin.ResponseWriter
	writes, flushes int
}

func (w *chatAudioCancelWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.ResponseWriter.Write(p)
}
func (w *chatAudioCancelWriter) WriteString(p string) (int, error) {
	w.writes++
	return w.ResponseWriter.WriteString(p)
}
func (w *chatAudioCancelWriter) Flush() { w.flushes++; w.ResponseWriter.Flush() }

func TestChatInputAudio_CanceledWriterDoesNoIOOrOpsMarker(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed_%t", committed), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if committed {
				c.Header("Content-Type", "text/event-stream")
				_, err := c.Writer.WriteString(": ping\n\n")
				require.NoError(t, err)
				c.Writer.Flush()
			}
			initial := recorder.Body.String()
			writer := &chatAudioCancelWriter{ResponseWriter: c.Writer}
			c.Writer = writer
			ctx, cancel := context.WithCancel(c.Request.Context())
			c.Request = c.Request.WithContext(ctx)
			cancel()
			writeChatInputAudioError(c, "invalid input_audio")
			require.Zero(t, writer.writes)
			require.Zero(t, writer.flushes)
			require.Equal(t, initial, recorder.Body.String())
			_, marked := c.Get(OpsStreamErrorKey)
			require.False(t, marked)
		})
	}
}

func TestChatInputAudio_CanceledConvertersPreserveTypedLocalRejection(t *testing.T) {
	for _, platform := range []string{PlatformGemini, PlatformAnthropic, PlatformOpenAI} {
		t.Run(platform, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			ctx, cancel := context.WithCancel(c.Request.Context())
			c.Request = c.Request.WithContext(ctx)
			cancel()
			account := &Account{ID: 1598, Type: AccountTypeAPIKey, Platform: platform, Extra: map[string]any{"openai_responses_supported": true}}
			body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"%%%","format":"wav"}}]}]}`)
			var err error
			if platform == PlatformOpenAI {
				result, forwardErr := (&OpenAIGatewayService{cfg: &config.Config{}}).ForwardAsChatCompletions(ctx, c, account, body, "", "")
				require.Nil(t, result)
				err = forwardErr
			} else if platform == PlatformGemini {
				result, forwardErr := (&GeminiMessagesCompatService{}).ForwardAsChatCompletions(ctx, c, account, body)
				require.Nil(t, result)
				err = forwardErr
			} else {
				result, forwardErr := (&GatewayService{}).ForwardAsChatCompletions(ctx, c, account, body, nil)
				require.Nil(t, result)
				err = forwardErr
			}
			expected := apicompat.ErrUnsupportedInputAudio
			if platform == PlatformGemini {
				expected = apicompat.ErrInvalidInputAudio
			}
			require.ErrorIs(t, err, expected)
			require.Empty(t, rec.Body.String())
			_, marked := c.Get(OpsStreamErrorKey)
			require.False(t, marked)
		})
	}
}
