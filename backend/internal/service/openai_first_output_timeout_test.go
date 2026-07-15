package service

import (
	"bytes"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIFirstOutputTimeoutUsesHighEffortOverride(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		OpenAIFirstOutputTimeoutSeconds:           90,
		OpenAIHighEffortFirstOutputTimeoutSeconds: 240,
	}}}

	require.Equal(t, 90, int(svc.openAIFirstOutputTimeout("medium").Seconds()))
	require.Equal(t, 240, int(svc.openAIFirstOutputTimeout("high").Seconds()))
	require.Equal(t, 240, int(svc.openAIFirstOutputTimeout("xhigh").Seconds()))
	require.Equal(t, 240, int(svc.openAIFirstOutputTimeout("max").Seconds()))
}

func TestOpenAIFirstOutputStageCommitAndCleanup(t *testing.T) {
	stage := newOpenAIFirstOutputStage(128)
	require.NoError(t, stage.prepareWrite(0))
	_, err := stage.WriteString("data: response.created\n\n")
	require.NoError(t, err)

	var dst bytes.Buffer
	require.NoError(t, stage.CommitTo(&dst))
	require.Equal(t, "data: response.created\n\n", dst.String())
	require.Zero(t, stage.Buffered())
	require.True(t, stage.closed)
	require.NoError(t, stage.Close())
}

func TestOpenAIFirstOutputStageRejectsOversizedWrite(t *testing.T) {
	stage := newOpenAIFirstOutputStage(4)
	_, err := stage.WriteString("12345")
	require.Error(t, err)
	require.True(t, errors.Is(err, errOpenAIFirstOutputStageLimit))
}

func TestOpenAIFirstOutputDynamicScanLinesStopsAtGuardLimit(t *testing.T) {
	active := atomic.Bool{}
	active.Store(true)
	split := openAIFirstOutputDynamicScanLines(&active)

	_, _, err := split(bytes.Repeat([]byte{'x'}, openAIFirstOutputStageMaxBytes+openAIFirstOutputScannerFramingAllowance), false)
	require.Error(t, err)
	require.True(t, errors.Is(err, errOpenAIFirstOutputScannerLimit))

	active.Store(false)
	advance, token, err := split([]byte("ok\n"), false)
	require.NoError(t, err)
	require.Equal(t, 3, advance)
	require.Equal(t, "ok", string(token))
}
