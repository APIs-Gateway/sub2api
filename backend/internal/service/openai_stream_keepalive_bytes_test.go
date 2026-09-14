//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// OpenAICompactKeepaliveAdjustedWrittenSize 扣除流式心跳字节（addOpenAIStreamKeepaliveBytes）。

func TestOpenAICompactKeepaliveAdjustedWrittenSize_StreamKeepaliveOnlyIsUnwritten(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, false)
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))

	n, err := c.Writer.WriteString(":\n\n")
	require.NoError(t, err)
	addOpenAIStreamKeepaliveBytes(c, n)
	require.True(t, c.Writer.Written())
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c), "只写过心跳等价于 gin 未写")

	n, err = c.Writer.WriteString(":\n\n")
	require.NoError(t, err)
	addOpenAIStreamKeepaliveBytes(c, n)
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c), "多次心跳累加后仍为未写")
	require.Equal(t, 6, openAIStreamKeepaliveBytesWritten(c))
}

func TestOpenAICompactKeepaliveAdjustedWrittenSize_StreamKeepalivePlusApplicationByte(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, false)
	n, err := c.Writer.WriteString(":\n\n")
	require.NoError(t, err)
	addOpenAIStreamKeepaliveBytes(c, n)
	_, err = c.Writer.WriteString("x")
	require.NoError(t, err)
	require.Equal(t, 1, OpenAICompactKeepaliveAdjustedWrittenSize(c), "心跳 + 1 字节业务 → 1")
}

func TestOpenAICompactKeepaliveAdjustedWrittenSize_NoStreamKeepaliveUnchanged(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, false)
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
	_, err := c.Writer.WriteString("direct")
	require.NoError(t, err)
	require.Equal(t, len("direct"), OpenAICompactKeepaliveAdjustedWrittenSize(c))

	// 计数 0 / 空 context / 错误类型都不影响原有语义
	addOpenAIStreamKeepaliveBytes(c, 0)
	addOpenAIStreamKeepaliveBytes(nil, 3)
	require.Equal(t, len("direct"), OpenAICompactKeepaliveAdjustedWrittenSize(c))
	c.Set(openAIStreamKeepaliveBytesKey, "wrong-type")
	require.Equal(t, 0, openAIStreamKeepaliveBytesWritten(c))
	require.Equal(t, len("direct"), OpenAICompactKeepaliveAdjustedWrittenSize(c))
}

func TestOpenAICompactKeepaliveAdjustedWrittenSize_CompactAndStreamKeepaliveBothExcluded(t *testing.T) {
	c, _, keepalive := startCompactKeepaliveForTest(t)
	require.True(t, keepalive.beat())
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))

	n, err := c.Writer.WriteString(":\n\n")
	require.NoError(t, err)
	addOpenAIStreamKeepaliveBytes(c, n)
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c), "compact 心跳 + 流式心跳都扣掉后仍为未写")

	_, err = c.Writer.WriteString("application-event")
	require.NoError(t, err)
	require.Equal(t, len("application-event"), OpenAICompactKeepaliveAdjustedWrittenSize(c))
}
