package handler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 本文件覆盖 issue #764 配套的 usage_record_worker_pool.go 改动：worker 池已停止
// （进程关停窗口）时，submitUsageRecordTask 必须降级为同步执行而不是静默丢弃计费
// 任务；显式配置的 drop/sample 溢出丢弃策略则维持原有语义不变。

func newStoppedUsageRecordPoolForTest() *service.UsageRecordWorkerPool {
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:    1,
		QueueSize:      1,
		TaskTimeout:    time.Second,
		OverflowPolicy: config.UsageRecordOverflowPolicySync,
	})
	pool.Stop()
	return pool
}

func TestGatewayHandlerSubmitUsageRecordTask_StoppedPoolFallsBackToSync(t *testing.T) {
	h := &GatewayHandler{usageRecordWorkerPool: newStoppedUsageRecordPoolForTest()}

	executed := false
	h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
		executed = true
	})
	require.True(t, executed, "池已停止时计费任务必须同步兜底执行，不能被静默丢弃")
}

func TestOpenAIGatewayHandlerSubmitUsageRecordTask_StoppedPoolFallsBackToSync(t *testing.T) {
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: newStoppedUsageRecordPoolForTest()}

	executed := false
	h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
		executed = true
	})
	require.True(t, executed, "池已停止时计费任务必须同步兜底执行，不能被静默丢弃")
}

func TestOpenAIGatewayHandlerSubmitMandatoryUsageRecordTask_StoppedPoolFallsBackToSync(t *testing.T) {
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: newStoppedUsageRecordPoolForTest()}

	executed := false
	h.submitMandatoryUsageRecordTask(context.Background(), func(ctx context.Context) {
		executed = true
	})
	require.True(t, executed, "mandatory 任务在池停止时也必须同步兜底执行")
}

// TestGatewayHandlerSubmitUsageRecordTask_DropPolicyOverflowStillDrops 验证：显式配置
// 的 drop 溢出策略在队列打满时仍按配置语义丢弃任务，不会被"池已停止才同步兜底"的新逻辑
// 误伤——两者是不同的丢弃原因（配置取舍 vs 关停窗口），必须区分对待。
func TestGatewayHandlerSubmitUsageRecordTask_DropPolicyOverflowStillDrops(t *testing.T) {
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:    1,
		QueueSize:      1,
		TaskTimeout:    time.Minute,
		OverflowPolicy: config.UsageRecordOverflowPolicyDrop,
	})
	t.Cleanup(pool.Stop)
	h := &GatewayHandler{usageRecordWorkerPool: pool}

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	// 占满 1 个 worker + 1 个队列位，让下一次提交必然溢出。
	require.Equal(t, service.UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		<-block
	}))
	require.Equal(t, service.UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		<-block
	}))

	var executed atomic.Bool
	h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
		executed.Store(true)
	})
	time.Sleep(50 * time.Millisecond)
	require.False(t, executed.Load(), "drop 溢出策略下任务应被丢弃，不应同步兜底执行")
}
