package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// opsIngressRejectUpsertChunkSize bounds the number of rows per multi-row INSERT so a
// single flush from the in-process aggregator cannot build an unbounded SQL statement.
const opsIngressRejectUpsertChunkSize = 500

func opsIngressRejectPlaceholder(dialect migrationDatabaseDialect, position int) string {
	return migrationPlaceholder(dialect, position)
}

func opsIngressRejectUpsertQuery(dialect migrationDatabaseDialect, rowCount int) string {
	var query strings.Builder
	_, _ = query.WriteString(`INSERT INTO ops_ingress_reject_aggregates
  (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id, request_count, first_seen, last_seen)
VALUES `)
	for row := 0; row < rowCount; row++ {
		if row > 0 {
			_ = query.WriteByte(',')
		}
		_ = query.WriteByte('(')
		for column := 1; column <= 10; column++ {
			if column > 1 {
				_ = query.WriteByte(',')
			}
			_, _ = query.WriteString(opsIngressRejectPlaceholder(dialect, row*10+column))
		}
		_ = query.WriteByte(')')
	}

	switch dialect {
	case migrationDatabaseMySQL:
		_, _ = query.WriteString(`
ON DUPLICATE KEY UPDATE request_count = request_count + VALUES(request_count),
                        first_seen = LEAST(first_seen, VALUES(first_seen)),
                        last_seen = GREATEST(last_seen, VALUES(last_seen)),
                        updated_at = CURRENT_TIMESTAMP`)
	case migrationDatabaseSQLite:
		_, _ = query.WriteString(`
ON CONFLICT (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id)
DO UPDATE SET request_count = ops_ingress_reject_aggregates.request_count + excluded.request_count,
              first_seen = MIN(ops_ingress_reject_aggregates.first_seen, excluded.first_seen),
              last_seen = MAX(ops_ingress_reject_aggregates.last_seen, excluded.last_seen),
              updated_at = CURRENT_TIMESTAMP`)
	default:
		_, _ = query.WriteString(`
ON CONFLICT (bucket_start, reject_reason, route_family, protocol, client_ip, user_id, api_key_id)
DO UPDATE SET request_count = ops_ingress_reject_aggregates.request_count + EXCLUDED.request_count,
              first_seen = LEAST(ops_ingress_reject_aggregates.first_seen, EXCLUDED.first_seen),
              last_seen = GREATEST(ops_ingress_reject_aggregates.last_seen, EXCLUDED.last_seen),
              updated_at = NOW()`)
	}
	return query.String()
}

// BatchUpsertIngressRejects persists pre-aggregated ingress-reject counters produced by
// service.OpsIngressRejectAggregator. Items arrive already bucketed and bounded
// (reason/route_family/protocol/client_ip are all low-cardinality dimensions and
// client_ip is already masked to a network prefix by the aggregator), so this layer
// never receives or stores raw request bodies, headers, credentials, or per-device IP
// addresses. Matching rows (same dimensions in the same minute bucket) are merged by
// summing request_count and widening the first_seen/last_seen window.
func (r *opsRepository) BatchUpsertIngressRejects(ctx context.Context, items []*service.OpsIngressRejectAggregate) error {
	if r == nil || r.db == nil || len(items) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	dialect := migrationDatabaseDialectForDB(r.db)

	for start := 0; start < len(items); start += opsIngressRejectUpsertChunkSize {
		end := start + opsIngressRejectUpsertChunkSize
		if end > len(items) {
			end = len(items)
		}
		valid := make([]*service.OpsIngressRejectAggregate, 0, end-start)
		for _, item := range items[start:end] {
			if item != nil && item.RequestCount > 0 {
				valid = append(valid, item)
			}
		}
		if len(valid) == 0 {
			continue
		}

		args := make([]any, 0, len(valid)*10)
		for _, item := range valid {
			var userID, apiKeyID int64
			if item.UserID != nil {
				userID = *item.UserID
			}
			if item.APIKeyID != nil {
				apiKeyID = *item.APIKeyID
			}
			args = append(args, item.BucketStart.UTC(), item.RejectReason, item.RouteFamily, item.Protocol,
				item.ClientIP, userID, apiKeyID, item.RequestCount, item.FirstSeen.UTC(), item.LastSeen.UTC())
		}
		if _, err := tx.ExecContext(ctx, opsIngressRejectUpsertQuery(dialect, len(valid)), args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListIngressRejects returns paginated, already-aggregated ingress-reject counters.
// Callers (see internal/handler/admin/ops_ingress_reject_handler.go) never receive raw
// request bodies, headers, or credentials through this path.
func (r *opsRepository) ListIngressRejects(ctx context.Context, filter *service.OpsIngressRejectFilter) (*service.OpsIngressRejectList, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		filter = &service.OpsIngressRejectFilter{}
	}
	dialect := migrationDatabaseDialectForDB(r.db)
	page, pageSize := filter.Page, filter.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	clauses := []string{"1=1"}
	args := make([]any, 0)
	add := func(expr string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(expr, opsIngressRejectPlaceholder(dialect, len(args))))
	}
	if filter.StartTime != nil {
		add("bucket_start >= %s", filter.StartTime.UTC())
	}
	if filter.EndTime != nil {
		add("bucket_start < %s", filter.EndTime.UTC())
	}
	if value := strings.TrimSpace(filter.RejectReason); value != "" {
		add("reject_reason = %s", value)
	}
	if value := strings.TrimSpace(filter.RouteFamily); value != "" {
		add("route_family = %s", value)
	}
	if value := strings.TrimSpace(filter.Protocol); value != "" {
		add("protocol = %s", value)
	}
	if value := strings.TrimSpace(filter.ClientIP); value != "" {
		add("client_ip = %s", value)
	}
	if filter.UserID != nil {
		add("user_id = %s", *filter.UserID)
	}
	if filter.APIKeyID != nil {
		add("api_key_id = %s", *filter.APIKeyID)
	}
	where := "WHERE " + strings.Join(clauses, " AND ")

	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ops_ingress_reject_aggregates "+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	query := fmt.Sprintf(`SELECT id,bucket_start,reject_reason,route_family,protocol,client_ip,user_id,api_key_id,request_count,first_seen,last_seen
FROM ops_ingress_reject_aggregates %s ORDER BY bucket_start DESC,id DESC LIMIT %s OFFSET %s`,
		where, opsIngressRejectPlaceholder(dialect, len(args)-1), opsIngressRejectPlaceholder(dialect, len(args)))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := &service.OpsIngressRejectList{
		Items: make([]*service.OpsIngressRejectAggregate, 0, pageSize), Total: total, Page: page, PageSize: pageSize,
	}
	for rows.Next() {
		item := &service.OpsIngressRejectAggregate{}
		var userID, apiKeyID int64
		if err := rows.Scan(&item.ID, &item.BucketStart, &item.RejectReason, &item.RouteFamily, &item.Protocol,
			&item.ClientIP, &userID, &apiKeyID, &item.RequestCount, &item.FirstSeen, &item.LastSeen); err != nil {
			return nil, err
		}
		if userID > 0 {
			item.UserID = &userID
		}
		if apiKeyID > 0 {
			item.APIKeyID = &apiKeyID
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
