package repository

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// SetContentModerationConfig serializes config writes with group deletion.
// Both operations lock live group rows before the moderation setting row, so
// a validated group cannot disappear and be written back by a concurrent save.
func (r *settingRepository) SetContentModerationConfig(ctx context.Context, value string, groupIDs []int64) error {
	client := clientFromContext(ctx, r.client)
	tx, err := client.Tx(ctx)
	if err != nil && !errors.Is(err, ent.ErrTxStarted) {
		return err
	}
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		client = tx.Client()
	}
	ids := append([]int64(nil), groupIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, id := range ids {
		if id <= 0 {
			return service.ErrGroupNotFound
		}
		if i > 0 && id == ids[i-1] {
			continue
		}
		var liveID int64
		if err := scanSingleRow(ctx, client, "SELECT id FROM groups WHERE id = $1 AND deleted_at IS NULL FOR UPDATE",
			[]any{id}, &liveID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return service.ErrGroupNotFound
			}
			return err
		}
	}
	if err := client.Setting.Create().SetKey(service.SettingKeyContentModerationConfig).
		SetValue(value).SetUpdatedAt(time.Now()).OnConflictColumns(setting.FieldKey).
		UpdateNewValues().Exec(ctx); err != nil {
		return err
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}

type settingRepository struct {
	client *ent.Client
}

func NewSettingRepository(client *ent.Client) service.SettingRepository {
	return &settingRepository{client: client}
}

func (r *settingRepository) Get(ctx context.Context, key string) (*service.Setting, error) {
	m, err := r.client.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrSettingNotFound
		}
		return nil, err
	}
	return &service.Setting{
		ID:        m.ID,
		Key:       m.Key,
		Value:     m.Value,
		UpdatedAt: m.UpdatedAt,
	}, nil
}

func (r *settingRepository) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *settingRepository) Set(ctx context.Context, key, value string) error {
	now := time.Now()
	return r.client.Setting.
		Create().
		SetKey(key).
		SetValue(value).
		SetUpdatedAt(now).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	settings, err := r.client.Setting.Query().Where(setting.KeyIn(keys...)).All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) SetMultiple(ctx context.Context, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}

	now := time.Now()
	builders := make([]*ent.SettingCreate, 0, len(settings))
	for key, value := range settings {
		builders = append(builders, r.client.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now))
	}
	return r.client.Setting.
		CreateBulk(builders...).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetAll(ctx context.Context) (map[string]string, error) {
	settings, err := r.client.Setting.Query().All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) Delete(ctx context.Context, key string) error {
	_, err := r.client.Setting.Delete().Where(setting.KeyEQ(key)).Exec(ctx)
	return err
}
