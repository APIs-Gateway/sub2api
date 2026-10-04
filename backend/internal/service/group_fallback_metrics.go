package service

import (
	"log/slog"
	"sync/atomic"
)

// Key 级分组回退链的进程内指标。本仓库没有接 Prometheus，约定是「原子计数 + 快照函数 + 日志」：
// 计数在进程内累加，SnapshotGroupFallbackMetrics 供测试与诊断读取，并每隔 groupFallbackMetricsLogEvery 次
// 打一条汇总日志，上线后从日志里看趋势。指标名保留 Prometheus 风格，方便以后接入时一一对应。

// 补试的结局（指标 group_fallback_saturation_probe_total{result}，审查 S6）。
const (
	// GroupSaturationProbeServed 补试在 top-K 之外找到了空位，组内服务，不回退。
	GroupSaturationProbeServed = "served"
	// GroupSaturationProbeSaturated 补试证明整组已满，WaitPlan.GroupSaturated=true，可以短等后回退。
	GroupSaturationProbeSaturated = "saturated"
	// GroupSaturationProbeTruncated 探测预算用尽被截断，没有证明整组满，按单账号等待处理。
	GroupSaturationProbeTruncated = "truncated"
	// GroupSaturationProbeError 补试自己出错（多为 Redis 抖动），按「没有证明整组满」处理。
	GroupSaturationProbeError = "error"
)

// 一次链运行的结局（指标 group_fallback_run_total{status}）。
const (
	GroupFallbackRunServedPrimary  = "served_first_hop"
	GroupFallbackRunServedFallback = "served_fallback"
	GroupFallbackRunExhausted      = "exhausted"
	GroupFallbackRunTerminal       = "terminal"
	GroupFallbackRunClientGone     = "client_gone"
	GroupFallbackRunUnresolved     = "unresolved"
)

// groupFallbackMetricsLogEvery 每累计这么多次补试就打一条汇总日志，避免日志量随流量线性增长。
const groupFallbackMetricsLogEvery = 1000

type groupFallbackMetrics struct {
	probeServed    atomic.Int64
	probeSaturated atomic.Int64
	probeTruncated atomic.Int64
	probeError     atomic.Int64

	runServedFirst    atomic.Int64
	runServedFallback atomic.Int64
	runExhausted      atomic.Int64
	runTerminal       atomic.Int64
	runClientGone     atomic.Int64
	runUnresolved     atomic.Int64

	hopSkipped           atomic.Int64
	hopBreakerSkipped    atomic.Int64
	hopFallbackNoAccount atomic.Int64
	hopFallbackBusy      atomic.Int64
	hopFallbackFailover  atomic.Int64
	breakerBypassRetry   atomic.Int64
}

var groupFallbackMetricsInstance groupFallbackMetrics

// GroupFallbackMetricsSnapshot 是回退链指标的一次读数。
type GroupFallbackMetricsSnapshot struct {
	// group_fallback_saturation_probe_total{result=served|saturated|truncated|error}
	SaturationProbeServed    int64
	SaturationProbeSaturated int64
	SaturationProbeTruncated int64
	SaturationProbeError     int64

	// group_fallback_run_total{status}
	RunServedFirstHop  int64
	RunServedFallback  int64
	RunExhausted       int64
	RunTerminal        int64
	RunClientGone      int64
	RunUnresolved      int64
	BreakerBypassRetry int64

	// group_fallback_hop_total{kind}
	HopSkipped           int64
	HopBreakerSkipped    int64
	HopFallbackNoAccount int64
	HopFallbackBusy      int64
	HopFallbackFailover  int64
}

// RecordGroupFallbackSaturationProbe 记录一次补试的结局。
func RecordGroupFallbackSaturationProbe(result string) {
	m := &groupFallbackMetricsInstance
	switch result {
	case GroupSaturationProbeServed:
		m.probeServed.Add(1)
	case GroupSaturationProbeSaturated:
		m.probeSaturated.Add(1)
	case GroupSaturationProbeTruncated:
		m.probeTruncated.Add(1)
	case GroupSaturationProbeError:
		m.probeError.Add(1)
	default:
		return
	}
	all := m.probeServed.Load() + m.probeSaturated.Load() + m.probeTruncated.Load() + m.probeError.Load()
	if all%groupFallbackMetricsLogEvery == 0 {
		slog.Info("group_fallback.saturation_probe_total",
			"served", m.probeServed.Load(),
			"saturated", m.probeSaturated.Load(),
			"truncated", m.probeTruncated.Load(),
			"error", m.probeError.Load(),
		)
	}
}

// RecordGroupFallbackRun 记录一次链运行的结局与每一跳的去向。
func RecordGroupFallbackRun(res ChainRunResult) {
	m := &groupFallbackMetricsInstance
	switch res.Status {
	case ChainRunServed:
		if res.ServedIndex > 0 {
			m.runServedFallback.Add(1)
		} else {
			m.runServedFirst.Add(1)
		}
	case ChainRunExhausted:
		m.runExhausted.Add(1)
	case ChainRunTerminal:
		m.runTerminal.Add(1)
	case ChainRunClientGone:
		m.runClientGone.Add(1)
	case ChainRunUnresolved:
		m.runUnresolved.Add(1)
	}
	if res.BreakerBypassRetried {
		m.breakerBypassRetry.Add(1)
	}
	for _, t := range res.Trace {
		switch t.SkippedBy {
		case "":
		case "attempt_skipped":
			m.hopSkipped.Add(1)
		default: // breaker_open / breaker_probe_busy
			m.hopBreakerSkipped.Add(1)
		}
		if t.Outcome != HopOutcomeFallbackWorthy {
			continue
		}
		switch t.Reason {
		case FallbackReasonNoAccount:
			m.hopFallbackNoAccount.Add(1)
		case FallbackReasonBusy:
			m.hopFallbackBusy.Add(1)
		case FallbackReasonFailoverExhausted:
			m.hopFallbackFailover.Add(1)
		}
	}
}

// SnapshotGroupFallbackMetrics 返回当前计数。
func SnapshotGroupFallbackMetrics() GroupFallbackMetricsSnapshot {
	m := &groupFallbackMetricsInstance
	return GroupFallbackMetricsSnapshot{
		SaturationProbeServed:    m.probeServed.Load(),
		SaturationProbeSaturated: m.probeSaturated.Load(),
		SaturationProbeTruncated: m.probeTruncated.Load(),
		SaturationProbeError:     m.probeError.Load(),
		RunServedFirstHop:        m.runServedFirst.Load(),
		RunServedFallback:        m.runServedFallback.Load(),
		RunExhausted:             m.runExhausted.Load(),
		RunTerminal:              m.runTerminal.Load(),
		RunClientGone:            m.runClientGone.Load(),
		RunUnresolved:            m.runUnresolved.Load(),
		BreakerBypassRetry:       m.breakerBypassRetry.Load(),
		HopSkipped:               m.hopSkipped.Load(),
		HopBreakerSkipped:        m.hopBreakerSkipped.Load(),
		HopFallbackNoAccount:     m.hopFallbackNoAccount.Load(),
		HopFallbackBusy:          m.hopFallbackBusy.Load(),
		HopFallbackFailover:      m.hopFallbackFailover.Load(),
	}
}
