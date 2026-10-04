package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
)

// modelCatalogRepository 手写 database/sql 实现模型目录（model_catalog）的访问。
type modelCatalogRepository struct {
	db *sql.DB
}

// NewModelCatalogRepository 创建模型目录仓储。
func NewModelCatalogRepository(db *sql.DB) service.ModelCatalogRepository {
	return &modelCatalogRepository{db: db}
}

func (r *modelCatalogRepository) List(ctx context.Context, filter service.ModelCatalogFilter) ([]service.ModelCatalogEntry, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, model_key, platform, display_name, aliases, reference_model, status, note, created_by, created_at, updated_at
		 FROM model_catalog
		 WHERE ($1::text = '' OR platform = $1::text) AND ($2::text = '' OR status = $2::text)
		 ORDER BY platform, model_key`, filter.Platform, string(filter.Status))
	if err != nil {
		return nil, fmt.Errorf("query model_catalog: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []service.ModelCatalogEntry
	for rows.Next() {
		var (
			e         service.ModelCatalogEntry
			aliases   pq.StringArray
			reference sql.NullString
			status    string
			createdBy sql.NullInt64
		)
		if err := rows.Scan(&e.ID, &e.ModelKey, &e.Platform, &e.DisplayName, &aliases, &reference, &status, &e.Note,
			&createdBy, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan model_catalog: %w", err)
		}
		e.Aliases = append([]string{}, aliases...)
		e.Status = service.ModelCatalogStatus(status)
		if reference.Valid {
			v := reference.String
			e.ReferenceModel = &v
		}
		if createdBy.Valid {
			v := createdBy.Int64
			e.CreatedBy = &v
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model_catalog: %w", err)
	}
	return out, nil
}

func (r *modelCatalogRepository) Create(ctx context.Context, e *service.ModelCatalogEntry) error {
	var reference any
	if e.ReferenceModel != nil {
		reference = *e.ReferenceModel
	}
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO model_catalog (model_key, platform, display_name, aliases, reference_model, status, note, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at, updated_at`,
		e.ModelKey, e.Platform, e.DisplayName, pq.Array(e.Aliases), reference, string(e.Status), e.Note, e.CreatedBy,
	).Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return service.ErrModelCatalogExists
		}
		return fmt.Errorf("insert model_catalog: %w", err)
	}
	return nil
}
