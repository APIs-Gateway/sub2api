package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
request_body, status_code, latency_ms, extra`

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
  COALESCE(l.extra::text, '{}')`

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
		encoded, err := json.Marshal(entry.Extra)
		if err != nil {
			return nil, fmt.Errorf("marshal audit log extra: %w", err)
		}
		extraJSON = string(encoded)
	}
	var actorUserID any
	if entry.ActorUserID != nil {
		actorUserID = *entry.ActorUserID
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
		entry.RequestBody,
		entry.StatusCode,
		entry.LatencyMs,
		extraJSON,
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

	args := make([]any, 0, len(valid)*16)
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
	defer rows.Close()

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
	var extraRaw string
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
	); err != nil {
		return nil, err
	}
	if actorUserID.Valid {
		value := actorUserID.Int64
		entry.ActorUserID = &value
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

func truncateAuditField(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}
