package handler

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayHandlerImagesJSONKeepaliveInterval(t *testing.T) {
	require.Zero(t, (&OpenAIGatewayHandler{}).openAIImagesJSONKeepaliveInterval())
	require.Zero(t, (&OpenAIGatewayHandler{cfg: &config.Config{}}).openAIImagesJSONKeepaliveInterval())

	h := &OpenAIGatewayHandler{cfg: &config.Config{}}
	h.cfg.Gateway.ImageNonstreamKeepaliveInterval = 3
	require.Equal(t, 3*time.Second, h.openAIImagesJSONKeepaliveInterval())
}
