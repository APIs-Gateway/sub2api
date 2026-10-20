package service

import (
	"context"
	"encoding/json"
)

// PricingDisplaySettings 控制用户「价格与计费」页(可用渠道)展示哪些分组 / 模型。
// 两个列表都为空表示「全部展示」(向后兼容,未配置即维持原行为)。
type PricingDisplaySettings struct {
	GroupIDs []int64  `json:"group_ids"`
	Models   []string `json:"models"`
}

// GetPricingDisplaySettings 读取价格页展示设置。读取或解析失败时返回空(=全部展示)。
func (s *SettingService) GetPricingDisplaySettings(ctx context.Context) PricingDisplaySettings {
	return PricingDisplaySettings{
		GroupIDs: s.pricingVisibleGroupIDs(ctx),
		Models:   s.pricingVisibleModels(ctx),
	}
}

func (s *SettingService) pricingVisibleGroupIDs(ctx context.Context) []int64 {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyPricingVisibleGroupIDs)
	if err != nil || raw == "" {
		return nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}

func (s *SettingService) pricingVisibleModels(ctx context.Context) []string {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyPricingVisibleModels)
	if err != nil || raw == "" {
		return nil
	}
	var models []string
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return nil
	}
	return models
}

// SetPricingDisplaySettings 保存价格页展示设置(nil 列表落库为空数组)。
func (s *SettingService) SetPricingDisplaySettings(ctx context.Context, in PricingDisplaySettings) error {
	gids := in.GroupIDs
	if gids == nil {
		gids = []int64{}
	}
	models := in.Models
	if models == nil {
		models = []string{}
	}
	gidsJSON, err := json.Marshal(gids)
	if err != nil {
		return err
	}
	modelsJSON, err := json.Marshal(models)
	if err != nil {
		return err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyPricingVisibleGroupIDs, string(gidsJSON)); err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, SettingKeyPricingVisibleModels, string(modelsJSON))
}
