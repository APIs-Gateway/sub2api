package securityaudit

import (
	"context"
	"log/slog"
	"strings"
)

const (
	EventConfigUpdated        = "prompt_audit.config_updated"
	EventConfigLoaded         = "prompt_guard.config_loaded"
	EventConfigReloadDegraded = "prompt_guard.config_reload_degraded"
	EventProbeStarted         = "prompt_audit.endpoint_probe_started"
	EventProbeFinished        = "prompt_audit.endpoint_probe_finished"
	EventProbeFailed          = "prompt_audit.endpoint_probe_failed"
	EventJobEnqueued          = "prompt_audit.job_enqueued"
	EventEnqueueSkipped       = "prompt_audit.enqueue_skipped"
	EventEnqueueDropped       = "prompt_audit.enqueue_dropped"
	EventAuditStarted         = "prompt_audit.started"
	EventProcessingReclaimed  = "prompt_audit.processing_reclaimed"
	EventProcessed            = "prompt_audit.processed"
	EventProcessFailed        = "prompt_audit.process_failed"
	EventFindingRecorded      = "prompt_audit.finding_recorded"
	EventChunkStarted         = "prompt_audit.scan_chunk_started"
	EventChunkCompleted       = "prompt_audit.scan_chunk_completed"
	EventChunkFailed          = "prompt_audit.scan_chunk_failed"
	EventChunksAggregated     = "prompt_audit.scan_chunks_aggregated"
	EventEvaluationStarted    = "prompt_guard.evaluation_started"
	EventGuardAllowed         = "prompt_guard.allowed"
	EventGuardBlocked         = "prompt_guard.blocked"
	EventGuardFailed          = "prompt_guard.failed"
	EventResultRecordFailed   = "prompt_guard.result_record_failed"
	EventEventDeleted         = "prompt_audit.event_deleted"
	EventEventsDeleted        = "prompt_audit.events_deleted"
	EventDeletePreviewed      = "prompt_audit.events_delete_previewed"
	EventEventsFilterDeleted  = "prompt_audit.events_filter_deleted"
)

var knownLogEvents = map[string]struct{}{
	EventConfigUpdated: {}, EventConfigLoaded: {}, EventConfigReloadDegraded: {},
	EventProbeStarted: {}, EventProbeFinished: {}, EventProbeFailed: {},
	EventJobEnqueued: {}, EventEnqueueSkipped: {}, EventEnqueueDropped: {},
	EventAuditStarted: {}, EventProcessingReclaimed: {}, EventProcessed: {}, EventProcessFailed: {}, EventFindingRecorded: {},
	EventChunkStarted: {}, EventChunkCompleted: {}, EventChunkFailed: {}, EventChunksAggregated: {},
	EventEvaluationStarted: {}, EventGuardAllowed: {}, EventGuardBlocked: {}, EventGuardFailed: {}, EventResultRecordFailed: {},
	EventEventDeleted: {}, EventEventsDeleted: {}, EventDeletePreviewed: {}, EventEventsFilterDeleted: {},
}

var allowedLogFields = map[string]struct{}{
	"request_id": {}, "user_id": {}, "api_key_id": {}, "group_id": {}, "provider": {},
	"protocol": {}, "endpoint": {}, "model": {}, "job_id": {}, "event_id": {},
	"config_version": {}, "guard_endpoint_id": {}, "decision": {}, "risk_level": {},
	"action": {}, "chunk_index": {}, "chunk_total": {}, "chunk_chars": {}, "input_chars": {},
	"input_limit": {}, "latency_ms": {}, "status": {}, "error_code": {}, "error_kind": {},
	"queue_length": {}, "queue_capacity": {}, "stage": {}, "upstream_dispatched": {},
	"billing_preconsumed": {}, "worker_id": {}, "reclaimed_total": {}, "attempts": {},
	"max_attempts": {}, "claim_version": {}, "http_status": {}, "retryable": {},
}

func LogInfo(event string, fields map[string]any) {
	if _, ok := knownLogEvents[event]; !ok {
		return
	}
	slog.LogAttrs(context.Background(), slog.LevelInfo, event, safeAttrs(fields)...)
}
func LogWarn(event string, fields map[string]any) {
	if _, ok := knownLogEvents[event]; !ok {
		return
	}
	slog.LogAttrs(context.Background(), slog.LevelWarn, event, safeAttrs(fields)...)
}
func LogError(event string, fields map[string]any) {
	if _, ok := knownLogEvents[event]; !ok {
		return
	}
	slog.LogAttrs(context.Background(), slog.LevelError, event, safeAttrs(fields)...)
}

func safeAttrs(fields map[string]any) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(fields))
	for key, value := range fields {
		key = strings.TrimSpace(key)
		if _, allowed := allowedLogFields[key]; !allowed {
			continue
		}
		if text, ok := value.(string); ok {
			if key == "error_kind" || key == "error_code" {
				value = stableErrorCode(text)
			} else {
				value = TrimRunes(strings.TrimSpace(text), 256)
			}
		}
		attrs = append(attrs, slog.Any(key, value))
	}
	return attrs
}

func mergeLogFields(base map[string]any, extra map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range extra {
		result[key] = value
	}
	return result
}

func requestLogFields(req Request) map[string]any {
	return map[string]any{
		"request_id": req.RequestID, "user_id": req.UserID, "api_key_id": req.APIKeyID,
		"group_id": pointerLogID(req.GroupID), "provider": req.Provider, "protocol": req.Protocol,
		"endpoint": req.Endpoint, "model": req.Model, "stage": req.Stage,
	}
}

func pointerLogID(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
