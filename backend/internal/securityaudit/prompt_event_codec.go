package securityaudit

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

func eventColumns(alias string) string {
	return fmt.Sprintf(`%[1]s.id,%[1]s.job_id,%[1]s.request_id,%[1]s.user_id,%[1]s.username_snapshot,
		%[1]s.user_email_snapshot,%[1]s.api_key_id,%[1]s.api_key_name_snapshot,%[1]s.group_id,%[1]s.group_name,
		%[1]s.provider,%[1]s.endpoint,%[1]s.protocol,%[1]s.model,%[1]s.prompt_hash,%[1]s.redacted_preview,
		%[1]s.decision,%[1]s.risk_level,%[1]s.action,%[1]s.categories,%[1]s.matched_scanners,
		%[1]s.scanner_scores,%[1]s.scanner_evidence,%[1]s.scanner_backend,%[1]s.scanner_version,
		%[1]s.guard_endpoint_id,%[1]s.policy_id,%[1]s.policy_version,%[1]s.config_version,
		%[1]s.chunk_total,%[1]s.latency_ms,%[1]s.created_at`, alias)
}

func scanEvent(row rowScanner) (*Event, error) {
	event := &Event{}
	var userID, apiKeyID, groupID sql.NullInt64
	var categories, matched, scores, evidence []byte
	err := row.Scan(&event.ID, &event.JobID, &event.Snapshot.RequestID, &userID,
		&event.Snapshot.UsernameSnapshot, &event.Snapshot.UserEmailSnapshot, &apiKeyID,
		&event.Snapshot.APIKeyNameSnapshot, &groupID, &event.Snapshot.GroupName,
		&event.Snapshot.Provider, &event.Snapshot.Endpoint, &event.Snapshot.Protocol, &event.Snapshot.Model,
		&event.Snapshot.PromptHash, &event.Snapshot.RedactedPreview, &event.Decision, &event.RiskLevel,
		&event.Action, &categories, &matched, &scores, &evidence, &event.ScannerBackend,
		&event.ScannerVersion, &event.GuardEndpointID, &event.PolicyID, &event.PolicyVersion,
		&event.ConfigVersion, &event.ChunkTotal, &event.LatencyMS, &event.CreatedAt)
	if err != nil {
		return nil, err
	}
	event.Snapshot.UserID = nullableInt64Value(userID)
	event.Snapshot.APIKeyID = nullableInt64Value(apiKeyID)
	event.Snapshot.GroupID = nullableInt64Ptr(groupID)
	_ = json.Unmarshal(categories, &event.Categories)
	_ = json.Unmarshal(matched, &event.MatchedScanners)
	_ = json.Unmarshal(scores, &event.ScannerScores)
	_ = json.Unmarshal(evidence, &event.ScannerEvidence)
	result := NormalizedResult{Decision: event.Decision, RiskLevel: event.RiskLevel, Action: event.Action,
		Categories: event.Categories, MatchedScanners: event.MatchedScanners, ScannerScores: event.ScannerScores,
		ScannerEvidence: event.ScannerEvidence}
	event.IssueSummaries = BuildIssueSummaries(result)
	return event, nil
}
