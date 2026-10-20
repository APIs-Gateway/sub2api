package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type auditLogRepository struct {
	db *sql.DB
}

func NewAuditLogRepository(db *sql.DB) service.AuditLogRepository {
	return &auditLogRepository{db: db}
}

const auditLogInsertColumns = `created_at, actor_user_id, actor_email, actor_role, auth_method,
credential_masked, action, method, path, request_id, client_ip, user_agent,
request_body, status_code, latency_ms, extra,
actor_label, auth_kind, token_id, route, target_type, target_id, reason, "before", "after"`

// auditLogInsertColumnCount must match auditLogInsertColumns and the values
// returned by auditLogInsertValues.
const auditLogInsertColumnCount = 25

const auditLogSelectColumns = `
  l.id,
  l.created_at,
  l.actor_user_id,
  COALESCE(l.actor_email, ''),
  COALESCE(l.actor_role, ''),
  COALESCE(l.auth_method, ''),
  COALESCE(l.credential_masked, ''),
  COALESCE(l.action, ''),
  COALESCE(l.method, ''),
  COALESCE(l.path, ''),
  COALESCE(l.request_id, ''),
  COALESCE(l.client_ip, ''),
  COALESCE(l.user_agent, ''),
  COALESCE(l.request_body, ''),
  l.status_code,
  l.latency_ms,
  COALESCE(l.extra::text, '{}'),
  COALESCE(l.actor_label, ''),
  COALESCE(l.auth_kind, ''),
  l.token_id,
  COALESCE(l.route, ''),
  COALESCE(l.target_type, ''),
  COALESCE(l.target_id, ''),
  COALESCE(l.reason, ''),
  l."before"::text,
  l."after"::text`

func auditLogInsertValues(entry *service.AuditLog) ([]any, error) {
	if entry == nil {
		return nil, fmt.Errorf("nil audit log")
	}
	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	extraJSON := "{}"
	if len(entry.Extra) > 0 {
		encoded, err := json.Marshal(sanitizeAuditExtra(entry.Extra))
		if err != nil {
			return nil, fmt.Errorf("marshal audit log extra: %w", err)
		}
		extraJSON = string(encoded)
	}
	var actorUserID any
	if entry.ActorUserID != nil {
		actorUserID = *entry.ActorUserID
	}
	var tokenID any
	if entry.TokenID != nil {
		tokenID = *entry.TokenID
	}
	return []any{
		createdAt.UTC(),
		actorUserID,
		truncateAuditField(entry.ActorEmail, 255),
		truncateAuditField(entry.ActorRole, 32),
		truncateAuditField(entry.AuthMethod, 32),
		truncateAuditField(entry.CredentialMasked, 160),
		truncateAuditField(entry.Action, 128),
		truncateAuditField(entry.Method, 16),
		truncateAuditField(entry.Path, 512),
		truncateAuditField(entry.RequestID, 128),
		truncateAuditField(entry.ClientIP, 64),
		truncateAuditField(entry.UserAgent, 512),
		sanitizeAuditText(entry.RequestBody),
		entry.StatusCode,
		entry.LatencyMs,
		extraJSON,
		truncateAuditField(entry.ActorLabel, 255),
		truncateAuditField(entry.AuthKind, 32),
		tokenID,
		truncateAuditField(entry.Route, 512),
		truncateAuditField(entry.TargetType, 64),
		truncateAuditField(entry.TargetID, 128),
		truncateAuditField(entry.Reason, auditReasonMaxRunes),
		auditJSONSnapshot(entry.Before),
		auditJSONSnapshot(entry.After),
	}, nil
}

func buildAuditLogInsertQuery(entries []*service.AuditLog) (string, []any, int, error) {
	valid := make([]*service.AuditLog, 0, len(entries))
	for _, entry := range entries {
		if entry != nil {
			valid = append(valid, entry)
		}
	}
	if len(valid) == 0 {
		return "", nil, 0, nil
	}

	args := make([]any, 0, len(valid)*auditLogInsertColumnCount)
	rows := make([]string, 0, len(valid))
	for _, entry := range valid {
		values, err := auditLogInsertValues(entry)
		if err != nil {
			return "", nil, 0, err
		}
		placeholders := make([]string, len(values))
		for index := range values {
			placeholders[index] = fmt.Sprintf("$%d", len(args)+index+1)
		}
		rows = append(rows, "("+strings.Join(placeholders, ",")+")")
		args = append(args, values...)
	}
	query := "INSERT INTO audit_logs (" + auditLogInsertColumns + ") VALUES " + strings.Join(rows, ",")
	return query, args, len(valid), nil
}

func (r *auditLogRepository) Insert(ctx context.Context, entry *service.AuditLog) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil audit log repository")
	}
	query, args, count, err := buildAuditLogInsertQuery([]*service.AuditLog{entry})
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("nil audit log")
	}
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *auditLogRepository) BatchInsert(ctx context.Context, entries []*service.AuditLog) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil audit log repository")
	}
	query, args, count, err := buildAuditLogInsertQuery(entries)
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return int64(count), nil
	}
	return inserted, nil
}

func buildAuditLogsWhere(filter *service.AuditLogFilter) (string, []any) {
	clauses := []string{"1=1"}
	args := make([]any, 0, 10)
	if filter == nil {
		return "WHERE " + strings.Join(clauses, " AND "), args
	}

	add := func(clause string, value any) string {
		args = append(args, value)
		return fmt.Sprintf(clause, len(args))
	}
	if filter.StartTime != nil {
		clauses = append(clauses, add("l.created_at >= $%d", filter.StartTime.UTC()))
	}
	if filter.EndTime != nil {
		clauses = append(clauses, add("l.created_at <= $%d", filter.EndTime.UTC()))
	}
	if filter.ActorUserID != nil {
		clauses = append(clauses, add("l.actor_user_id = $%d", *filter.ActorUserID))
	}
	if value := strings.TrimSpace(filter.ActorEmail); value != "" {
		clauses = append(clauses, add("l.actor_email ILIKE $%d ESCAPE '\\'", "%"+escapeLikePattern(value)+"%"))
	}
	if value := strings.TrimSpace(filter.AuthMethod); value != "" {
		clauses = append(clauses, add("l.auth_method = $%d", value))
	}
	if value := strings.TrimSpace(filter.Action); value != "" {
		clauses = append(clauses, add("l.action ILIKE $%d ESCAPE '\\'", "%"+escapeLikePattern(value)+"%"))
	}
	if value := strings.TrimSpace(filter.Method); value != "" {
		clauses = append(clauses, add("l.method = $%d", strings.ToUpper(value)))
	}
	if value := strings.TrimSpace(filter.ClientIP); value != "" {
		clauses = append(clauses, add("l.client_ip = $%d", value))
	}
	if value := strings.TrimSpace(filter.AuthKind); value != "" {
		clauses = append(clauses, add("l.auth_kind = $%d", value))
	}
	if filter.TokenID != nil {
		clauses = append(clauses, add("l.token_id = $%d", *filter.TokenID))
	}
	if value := strings.TrimSpace(filter.RoutePrefix); value != "" {
		clauses = append(clauses, add("l.route LIKE $%d ESCAPE '\\'", escapeLikePattern(value)+"%"))
	}
	if value := strings.TrimSpace(filter.TargetType); value != "" {
		clauses = append(clauses, add("l.target_type = $%d", value))
	}
	if value := strings.TrimSpace(filter.TargetID); value != "" {
		clauses = append(clauses, add("l.target_id = $%d", value))
	}
	if filter.StatusMin != nil {
		clauses = append(clauses, add("l.status_code >= $%d", *filter.StatusMin))
	}
	if filter.StatusMax != nil {
		clauses = append(clauses, add("l.status_code <= $%d", *filter.StatusMax))
	}
	if filter.Success != nil {
		if *filter.Success {
			clauses = append(clauses, "l.status_code < 400")
		} else {
			clauses = append(clauses, "l.status_code >= 400")
		}
	}
	if value := strings.TrimSpace(filter.Query); value != "" {
		pattern := "%" + escapeLikePattern(value) + "%"
		args = append(args, pattern)
		placeholder := fmt.Sprintf("$%d", len(args))
		clauses = append(clauses, "(l.path ILIKE "+placeholder+" ESCAPE '\\' OR l.action ILIKE "+placeholder+" ESCAPE '\\' OR l.actor_email ILIKE "+placeholder+" ESCAPE '\\')")
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func normalizeAuditLogFilter(filter *service.AuditLogFilter) (page, pageSize int) {
	if filter == nil {
		return 1, service.AuditLogDefaultPageSize
	}
	page = filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize = filter.PageSize
	if pageSize <= 0 {
		pageSize = service.AuditLogDefaultPageSize
	}
	if pageSize > service.AuditLogMaxPageSize {
		pageSize = service.AuditLogMaxPageSize
	}
	return page, pageSize
}

func (r *auditLogRepository) List(ctx context.Context, filter *service.AuditLogFilter) (*service.AuditLogList, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil audit log repository")
	}
	page, pageSize := normalizeAuditLogFilter(filter)
	where, args := buildAuditLogsWhere(filter)

	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs l "+where, args...).Scan(&total); err != nil {
		return nil, err
	}

	listArgs := append([]any(nil), args...)
	listArgs = append(listArgs, (page-1)*pageSize, pageSize)
	query := "SELECT " + auditLogSelectColumns + " FROM audit_logs l " + where +
		" ORDER BY l.created_at DESC, l.id DESC OFFSET $" + itoa(len(listArgs)-1) + " LIMIT $" + itoa(len(listArgs))
	rows, err := r.db.QueryContext(ctx, query, listArgs...)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	logs := make([]*service.AuditLog, 0, pageSize)
	for rows.Next() {
		entry, err := scanAuditLogRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		logs = append(logs, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &service.AuditLogList{Logs: logs, Total: total, Page: page, PageSize: pageSize}, nil
}

func scanAuditLogRow(scan func(dest ...any) error) (*service.AuditLog, error) {
	entry := &service.AuditLog{}
	var actorUserID sql.NullInt64
	var tokenID sql.NullInt64
	var extraRaw string
	var before, after sql.NullString
	if err := scan(
		&entry.ID,
		&entry.CreatedAt,
		&actorUserID,
		&entry.ActorEmail,
		&entry.ActorRole,
		&entry.AuthMethod,
		&entry.CredentialMasked,
		&entry.Action,
		&entry.Method,
		&entry.Path,
		&entry.RequestID,
		&entry.ClientIP,
		&entry.UserAgent,
		&entry.RequestBody,
		&entry.StatusCode,
		&entry.LatencyMs,
		&extraRaw,
		&entry.ActorLabel,
		&entry.AuthKind,
		&tokenID,
		&entry.Route,
		&entry.TargetType,
		&entry.TargetID,
		&entry.Reason,
		&before,
		&after,
	); err != nil {
		return nil, err
	}
	if actorUserID.Valid {
		value := actorUserID.Int64
		entry.ActorUserID = &value
	}
	if tokenID.Valid {
		value := tokenID.Int64
		entry.TokenID = &value
	}
	if before.Valid && before.String != "" {
		entry.Before = json.RawMessage(before.String)
	}
	if after.Valid && after.String != "" {
		entry.After = json.RawMessage(after.String)
	}
	if extra := strings.TrimSpace(extraRaw); extra != "" && extra != "null" && extra != "{}" {
		entry.Extra = make(map[string]any)
		if err := json.Unmarshal([]byte(extra), &entry.Extra); err != nil {
			return nil, fmt.Errorf("decode audit log extra: %w", err)
		}
	}
	return entry, nil
}

func (r *auditLogRepository) DeleteBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil audit log repository")
	}
	if batchSize <= 0 {
		batchSize = service.AuditLogDefaultDeleteBatchSize
	}
	if batchSize > service.AuditLogMaxDeleteBatchSize {
		batchSize = service.AuditLogMaxDeleteBatchSize
	}
	result, err := r.db.ExecContext(ctx, `
DELETE FROM audit_logs
WHERE id IN (
    SELECT id
    FROM audit_logs
    WHERE created_at < $1
    ORDER BY created_at ASC, id ASC
    LIMIT $2
)`, cutoff.UTC(), batchSize)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// auditReasonMaxRunes bounds the stored X-Reason.
const auditReasonMaxRunes = 2000

// truncateAuditField cuts value to maxRunes runes after making it safe for a
// PostgreSQL text column.
func truncateAuditField(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	value = sanitizeAuditText(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

// sanitizeAuditText removes what PostgreSQL text columns reject (NUL bytes and
// invalid UTF-8). One such value would otherwise fail the whole batch insert
// and lose every other audit row in it.
func sanitizeAuditText(value string) string {
	if strings.IndexByte(value, 0) >= 0 {
		value = strings.ReplaceAll(value, "\x00", "")
	}
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "\uFFFD")
	}
	return value
}

// sanitizeAuditExtra returns a copy of the extra map whose strings are safe
// for a JSONB column (which, unlike text, rejects the \u0000 escape).
func sanitizeAuditExtra(extra map[string]any) map[string]any {
	out := make(map[string]any, len(extra))
	for key, value := range extra {
		out[sanitizeAuditText(key)] = sanitizeAuditExtraValue(value)
	}
	return out
}

func sanitizeAuditExtraValue(value any) any {
	switch typed := value.(type) {
	case string:
		return sanitizeAuditText(typed)
	case map[string]any:
		return sanitizeAuditExtra(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = sanitizeAuditExtraValue(item)
		}
		return out
	default:
		return value
	}
}

// auditJSONSnapshot turns an optional JSON snapshot into a JSONB parameter:
// NULL when empty or not valid JSON (an invalid document would fail the insert).
func auditJSONSnapshot(raw json.RawMessage) any {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil
	}
	text := string(raw)
	if strings.Contains(text, "\\u0000") {
		return nil
	}
	return text
}
