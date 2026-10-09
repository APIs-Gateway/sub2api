package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
)

func rejectAmbiguousChatInputAudio(ctx context.Context, c *gin.Context, body []byte, allowAudio bool) error {
	if err := apicompat.ValidateChatInputAudioRaw(body, allowAudio); err != nil {
		MarkBillingInflightAttemptNoCharge(ctx)
		writeChatInputAudioError(c, err.Error())
		return err
	}
	return nil
}
