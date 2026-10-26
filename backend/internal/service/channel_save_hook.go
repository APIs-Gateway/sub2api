package service

import (
	"context"
	"log/slog"
)

// ChannelSaveHook 渠道创建、更新、删除成功之后的回调。
//
// W6 的派生钩子（PricingDerivationService）通过它把渠道配置同步到矩阵表。
// 回调是 best-effort：实现不得向调用方传播任何失败，ChannelService 另外兜住 panic，
// 所以钩子无论发生什么都不会让渠道保存失败。
// previousGroupIDs 是保存前渠道关联的分组（创建时为 nil），用来清理已经离开渠道的分组。
type ChannelSaveHook interface {
	AfterChannelSaved(ctx context.Context, channelID int64, previousGroupIDs []int64)
}

// SetSaveHook 设置渠道保存后的回调。必须在开始处理请求之前调用（依赖注入阶段）。
func (s *ChannelService) SetSaveHook(hook ChannelSaveHook) {
	s.saveHook = hook
}

// previousGroupIDsForSaveHook 读取保存前渠道关联的分组；没有设置钩子时不查询。
func (s *ChannelService) previousGroupIDsForSaveHook(ctx context.Context, channelID int64) []int64 {
	if s.saveHook == nil {
		return nil
	}
	ids, err := s.repo.GetGroupIDs(ctx, channelID)
	if err != nil {
		slog.Warn("failed to get previous group IDs for save hook", "channel_id", channelID, "error", err)
		return nil
	}
	return ids
}

// afterChannelSaved 调用保存钩子，并兜住钩子里的 panic。
func (s *ChannelService) afterChannelSaved(ctx context.Context, channelID int64, previousGroupIDs []int64) {
	hook := s.saveHook
	if hook == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("channel save hook panicked", "channel_id", channelID, "panic", r)
		}
	}()
	hook.AfterChannelSaved(ctx, channelID, previousGroupIDs)
}
