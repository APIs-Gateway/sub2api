package securityaudit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// EventFilter describes the redacted fields that administrators may use to
// search prompt audit events. It intentionally has no raw prompt field.
type EventFilter struct {
	Decision   string     `json:"decision,omitempty"`
	RiskLevel  string     `json:"risk_level,omitempty"`
	Endpoint   string     `json:"endpoint,omitempty"`
	GroupID    *int64     `json:"group_id,omitempty"`
	UserID     *int64     `json:"user_id,omitempty"`
	APIKeyID   *int64     `json:"api_key_id,omitempty"`
	RequestID  string     `json:"request_id,omitempty"`
	PromptHash string     `json:"prompt_hash,omitempty"`
	Keyword    string     `json:"keyword,omitempty"`
	StartAt    *time.Time `json:"start_at,omitempty"`
	EndAt      *time.Time `json:"end_at,omitempty"`
}

type EventPage struct {
	Items    []*Event `json:"items"`
	Total    int64    `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Pages    int      `json:"pages"`
}

// EventRepository is deliberately read-only. Destructive retention operations
// are outside this child issue and must not become part of this dependency.
type EventRepository interface {
	ListEvents(ctx context.Context, filter EventFilter, page, pageSize int) (*EventPage, error)
	GetEvent(ctx context.Context, id int64) (*Event, error)
}

func (r *PostgreSQLRepository) ListEvents(ctx context.Context, filter EventFilter, page, pageSize int) (*EventPage, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("prompt audit database unavailable")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	where, args := buildEventWhere(filter, 1, r.placeholder)
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM prompt_audit_events e`+where, args...).Scan(&total); err != nil {
		return nil, err
	}

	queryArgs := append([]any(nil), args...)
	queryArgs = append(queryArgs, pageSize, (page-1)*pageSize)
	rows, err := r.db.QueryContext(ctx, `SELECT `+eventColumns("e")+` FROM prompt_audit_events e`+where+
		` ORDER BY e.created_at DESC, e.id DESC LIMIT `+r.placeholder(len(queryArgs)-1)+` OFFSET `+r.placeholder(len(queryArgs)), queryArgs...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]*Event, 0, pageSize)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	pages := 0
	if total > 0 {
		pages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}
	return &EventPage{Items: items, Total: total, Page: page, PageSize: pageSize, Pages: pages}, nil
}

func (r *PostgreSQLRepository) GetEvent(ctx context.Context, id int64) (*Event, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("prompt audit database unavailable")
	}
	event, err := scanEvent(r.db.QueryRowContext(ctx,
		`SELECT `+eventColumns("e")+` FROM prompt_audit_events e WHERE e.id=`+r.placeholder(1), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEventNotFound
	}
	return event, err
}

func canonicalEventFilter(filter EventFilter) EventFilter {
	filter.Decision = strings.TrimSpace(strings.ToLower(filter.Decision))
	filter.RiskLevel = strings.TrimSpace(strings.ToLower(filter.RiskLevel))
	filter.Endpoint = strings.TrimSpace(filter.Endpoint)
	filter.RequestID = strings.TrimSpace(filter.RequestID)
	filter.PromptHash = strings.ToLower(strings.TrimSpace(filter.PromptHash))
	filter.Keyword = strings.TrimSpace(filter.Keyword)
	if filter.StartAt != nil {
		value := filter.StartAt.UTC()
		filter.StartAt = &value
	}
	if filter.EndAt != nil {
		value := filter.EndAt.UTC()
		filter.EndAt = &value
	}
	return filter
}

func buildEventWhere(filter EventFilter, firstIndex int, placeholder func(int) string) (string, []any) {
	filter = canonicalEventFilter(filter)
	clauses := []string{" WHERE TRUE"}
	args := make([]any, 0, 12)
	add := func(clause string, value any) {
		clauses = append(clauses, fmt.Sprintf(clause, placeholder(firstIndex+len(args))))
		args = append(args, value)
	}
	if filter.Decision != "" {
		add(" AND e.decision=%s", filter.Decision)
	}
	if filter.RiskLevel != "" {
		add(" AND e.risk_level=%s", filter.RiskLevel)
	}
	if filter.Endpoint != "" {
		add(" AND e.endpoint=%s", filter.Endpoint)
	}
	if filter.GroupID != nil {
		add(" AND e.group_id=%s", *filter.GroupID)
	}
	if filter.UserID != nil {
		add(" AND e.user_id=%s", *filter.UserID)
	}
	if filter.APIKeyID != nil {
		add(" AND e.api_key_id=%s", *filter.APIKeyID)
	}
	if filter.RequestID != "" {
		add(" AND e.request_id=%s", filter.RequestID)
	}
	if filter.PromptHash != "" {
		add(" AND e.prompt_hash=%s", filter.PromptHash)
	}
	if filter.Keyword != "" {
		value := placeholder(firstIndex + len(args))
		clauses = append(clauses, ` AND (LOWER(e.request_id) LIKE LOWER(`+value+`) OR LOWER(e.prompt_hash) LIKE LOWER(`+value+`) OR LOWER(e.redacted_preview) LIKE LOWER(`+value+`)
			OR LOWER(e.username_snapshot) LIKE LOWER(`+value+`) OR LOWER(e.user_email_snapshot) LIKE LOWER(`+value+`) OR LOWER(e.api_key_name_snapshot) LIKE LOWER(`+value+`))`)
		keyword := "%" + TrimRunes(filter.Keyword, 128) + "%"
		if value == "?" {
			args = append(args, keyword, keyword, keyword, keyword, keyword, keyword)
		} else {
			args = append(args, keyword)
		}
	}
	if filter.StartAt != nil {
		add(" AND e.created_at >= %s", filter.StartAt.UTC())
	}
	if filter.EndAt != nil {
		add(" AND e.created_at <= %s", filter.EndAt.UTC())
	}
	return strings.Join(clauses, ""), args
}
