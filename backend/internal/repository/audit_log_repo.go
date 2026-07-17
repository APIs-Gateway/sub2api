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

type auditLogSQLDialect uint8

const (
	auditLogPostgresDialect auditLogSQLDialect = iota
	auditLogQuestionDialect
)

func auditLogSQLDialectForDB(db *sql.DB) auditLogSQLDialect {
	if db == nil || isPostgresDriver(db) {
		return auditLogPostgresDialect
	}

	// Keep the default PostgreSQL form for sqlmock and other unknown drivers;
	// the application currently uses PostgreSQL in production. The explicit
	// driver checks cover SQLite and MySQL without importing either driver.
	driverName := strings.ToLower(fmt.Sprintf("%T", db.Driver()))
	if strings.Contains(driverName, "sqlite") || strings.Contains(driverName, "mysql") {
		return auditLogQuestionDialect
	}
	return auditLogPostgresDialect
}

func auditLogPlaceholder(dialect auditLogSQLDialect, position int) string {
	if dialect == auditLogQuestionDialect {
		return "?"
	}
	return fmt.Sprintf("$%d", position)
}

func NewAuditLogRepository(db *sql.DB) service.AuditLogRepository {
	return &auditLogRepository{db: db}
}

const auditLogInsertColumns = `created_at, actor_user_id, actor_email, actor_role, auth_method,
credential_masked, action, method, path, request_id, client_ip, user_agent,
request_body, status_code, latency_ms, extra`

const auditLogInsertColumnsWithID = `id, ` + auditLogInsertColumns

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
  COALESCE(l.extra, '{}')`

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

func auditLogInsertValuesWithID(id int64, entry *service.AuditLog) ([]any, error) {
	values, err := auditLogInsertValues(entry)
	if err != nil {
		return nil, err
	}
	return append([]any{id}, values...), nil
}

func nonNilAuditLogEntries(entries []*service.AuditLog) []*service.AuditLog {
	valid := make([]*service.AuditLog, 0, len(entries))
	for _, entry := range entries {
		if entry != nil {
			valid = append(valid, entry)
		}
	}
	return valid
}

func buildAuditLogInsertQuery(entries []*service.AuditLog) (string, []any, int, error) {
	valid := nonNilAuditLogEntries(entries)
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

func buildAuditLogInsertQueryWithIDs(entries []*service.AuditLog, ids []int64, dialect auditLogSQLDialect) (string, []any, int, error) {
	valid := nonNilAuditLogEntries(entries)
	if len(valid) == 0 {
		return "", nil, 0, nil
	}
	if len(ids) != len(valid) {
		return "", nil, 0, fmt.Errorf("audit log ID count %d does not match entry count %d", len(ids), len(valid))
	}

	args := make([]any, 0, len(valid)*17)
	rows := make([]string, 0, len(valid))
	for index, entry := range valid {
		values, err := auditLogInsertValuesWithID(ids[index], entry)
		if err != nil {
			return "", nil, 0, err
		}
		placeholders := make([]string, len(values))
		for valueIndex := range values {
			placeholders[valueIndex] = auditLogPlaceholder(dialect, len(args)+valueIndex+1)
		}
		rows = append(rows, "("+strings.Join(placeholders, ",")+")")
		args = append(args, values...)
	}
	return "INSERT INTO audit_logs (" + auditLogInsertColumnsWithID + ") VALUES " + strings.Join(rows, ","), args, len(valid), nil
}

func (r *auditLogRepository) Insert(ctx context.Context, entry *service.AuditLog) error {
	if entry == nil {
		return fmt.Errorf("nil audit log")
	}
	_, err := r.insertEntries(ctx, []*service.AuditLog{entry})
	return err
}

func (r *auditLogRepository) BatchInsert(ctx context.Context, entries []*service.AuditLog) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil audit log repository")
	}
	return r.insertEntries(ctx, entries)
}

type auditLogSQLQueryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func allocateAuditLogIDs(ctx context.Context, queryer auditLogSQLQueryer, count int, dialect auditLogSQLDialect) ([]int64, error) {
	if count <= 0 {
		return nil, nil
	}
	placeholder := auditLogPlaceholder(dialect, 1)
	if _, err := queryer.ExecContext(ctx,
		"UPDATE audit_log_id_sequence SET id = id + "+placeholder,
		count,
	); err != nil {
		return nil, fmt.Errorf("allocate audit log IDs: %w", err)
	}

	var lastID int64
	if err := queryer.QueryRowContext(ctx, "SELECT id FROM audit_log_id_sequence").Scan(&lastID); err != nil {
		return nil, fmt.Errorf("read audit log ID sequence: %w", err)
	}
	firstID := lastID - int64(count) + 1
	ids := make([]int64, count)
	for index := range ids {
		ids[index] = firstID + int64(index)
	}
	return ids, nil
}

func (r *auditLogRepository) insertEntries(ctx context.Context, entries []*service.AuditLog) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil audit log repository")
	}
	valid := nonNilAuditLogEntries(entries)
	if len(valid) == 0 {
		return 0, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	dialect := auditLogSQLDialectForDB(r.db)
	ids, err := allocateAuditLogIDs(ctx, tx, len(valid), dialect)
	if err != nil {
		return 0, err
	}
	query, args, count, err := buildAuditLogInsertQueryWithIDs(valid, ids, dialect)
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return int64(count), nil
	}
	return inserted, nil
}

func buildAuditLogsWhere(filter *service.AuditLogFilter) (string, []any) {
	return buildAuditLogsWhereForDialect(filter, auditLogPostgresDialect)
}

func buildAuditLogsWhereForDialect(filter *service.AuditLogFilter, dialect auditLogSQLDialect) (string, []any) {
	clauses := []string{"1=1"}
	args := make([]any, 0, 10)
	if filter == nil {
		return "WHERE " + strings.Join(clauses, " AND "), args
	}

	add := func(clause string, value any) string {
		args = append(args, value)
		return fmt.Sprintf(clause, auditLogPlaceholder(dialect, len(args)))
	}
	if filter.StartTime != nil {
		clauses = append(clauses, add("l.created_at >= %s", filter.StartTime.UTC()))
	}
	if filter.EndTime != nil {
		clauses = append(clauses, add("l.created_at <= %s", filter.EndTime.UTC()))
	}
	if filter.ActorUserID != nil {
		clauses = append(clauses, add("l.actor_user_id = %s", *filter.ActorUserID))
	}
	if value := strings.TrimSpace(filter.ActorEmail); value != "" {
		pattern := "%" + escapeLikePattern(value) + "%"
		if dialect == auditLogQuestionDialect {
			clauses = append(clauses, add("LOWER(l.actor_email) LIKE LOWER(%s) ESCAPE '\\'", strings.ToLower(pattern)))
		} else {
			clauses = append(clauses, add("l.actor_email ILIKE %s ESCAPE '\\'", pattern))
		}
	}
	if value := strings.TrimSpace(filter.AuthMethod); value != "" {
		clauses = append(clauses, add("l.auth_method = %s", value))
	}
	if value := strings.TrimSpace(filter.Action); value != "" {
		pattern := "%" + escapeLikePattern(value) + "%"
		if dialect == auditLogQuestionDialect {
			clauses = append(clauses, add("LOWER(l.action) LIKE LOWER(%s) ESCAPE '\\'", strings.ToLower(pattern)))
		} else {
			clauses = append(clauses, add("l.action ILIKE %s ESCAPE '\\'", pattern))
		}
	}
	if value := strings.TrimSpace(filter.Method); value != "" {
		clauses = append(clauses, add("l.method = %s", strings.ToUpper(value)))
	}
	if value := strings.TrimSpace(filter.ClientIP); value != "" {
		clauses = append(clauses, add("l.client_ip = %s", value))
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
		if dialect == auditLogQuestionDialect {
			args = append(args, strings.ToLower(pattern), strings.ToLower(pattern), strings.ToLower(pattern))
			pathPlaceholder := auditLogPlaceholder(dialect, len(args)-2)
			actionPlaceholder := auditLogPlaceholder(dialect, len(args)-1)
			emailPlaceholder := auditLogPlaceholder(dialect, len(args))
			clauses = append(clauses, "(LOWER(l.path) LIKE LOWER("+pathPlaceholder+") ESCAPE '\\' OR LOWER(l.action) LIKE LOWER("+actionPlaceholder+") ESCAPE '\\' OR LOWER(l.actor_email) LIKE LOWER("+emailPlaceholder+") ESCAPE '\\')")
		} else {
			args = append(args, pattern)
			placeholder := auditLogPlaceholder(dialect, len(args))
			clauses = append(clauses, "(l.path ILIKE "+placeholder+" ESCAPE '\\' OR l.action ILIKE "+placeholder+" ESCAPE '\\' OR l.actor_email ILIKE "+placeholder+" ESCAPE '\\')")
		}
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
	dialect := auditLogSQLDialectForDB(r.db)
	where, args := buildAuditLogsWhereForDialect(filter, dialect)

	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs l "+where, args...).Scan(&total); err != nil {
		return nil, err
	}

	listArgs := append([]any(nil), args...)
	var query string
	if dialect == auditLogQuestionDialect {
		listArgs = append(listArgs, pageSize, (page-1)*pageSize)
		limitPlaceholder := auditLogPlaceholder(dialect, len(listArgs)-1)
		offsetPlaceholder := auditLogPlaceholder(dialect, len(listArgs))
		query = "SELECT " + auditLogSelectColumns + " FROM audit_logs l " + where +
			" ORDER BY l.created_at DESC, l.id DESC LIMIT " + limitPlaceholder + " OFFSET " + offsetPlaceholder
	} else {
		listArgs = append(listArgs, (page-1)*pageSize, pageSize)
		offsetPlaceholder := auditLogPlaceholder(dialect, len(listArgs)-1)
		limitPlaceholder := auditLogPlaceholder(dialect, len(listArgs))
		query = "SELECT " + auditLogSelectColumns + " FROM audit_logs l " + where +
			" ORDER BY l.created_at DESC, l.id DESC OFFSET " + offsetPlaceholder + " LIMIT " + limitPlaceholder
	}
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
	var extraRaw sql.NullString
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
	if extra := strings.TrimSpace(extraRaw.String); extraRaw.Valid && extra != "" && extra != "null" && extra != "{}" {
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
	dialect := auditLogSQLDialectForDB(r.db)
	firstPlaceholder := auditLogPlaceholder(dialect, 1)
	secondPlaceholder := auditLogPlaceholder(dialect, 2)
	query := fmt.Sprintf(`
DELETE FROM audit_logs
WHERE id IN (
    SELECT id
    FROM audit_logs
    WHERE created_at < %s
    ORDER BY created_at ASC, id ASC
	LIMIT %s
)`, firstPlaceholder, secondPlaceholder)
	result, err := r.db.ExecContext(ctx, query, cutoff.UTC(), batchSize)
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
