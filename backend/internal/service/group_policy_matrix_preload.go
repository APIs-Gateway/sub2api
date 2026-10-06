package service

import (
	"context"
	"log/slog"
	"time"
)

// W6 PR7a：启动预加载（PR4-1 审查「PR7 前必须修」）。
//
// 阶段读取不阻塞（cachedSnapshot）：缓存里没有的分组按 legacy 处理，后台加载。这对 legacy、shadow 分组没有问题，
// 但对 v2 分组是错的：进程刚启动、快照还没加载完的那几百毫秒里，v2 分组会悄悄退回渠道计费、白名单全开放。
// 所以启动时把「有配置行的分组」的快照同步加载进缓存，加载成功之后才开始处理请求；之后缓存里的快照即使过期或
// 加载失败也一直保留（旧数据优先于默认状态，见 matrixPolicy 的加载失败语义），阶段不会因为加载失败而降级。
// 没有配置行的分组本来就是 legacy，不需要预加载。

const (
	matrixPreloadAttempts   = 3
	matrixPreloadRetryDelay = time.Second
	matrixPreloadTimeout    = 30 * time.Second
)

// ConfiguredGroupLister 列出有 group_model_config 行的（未软删除的）分组。由矩阵仓库实现，是可选的读侧能力。
type ConfiguredGroupLister interface {
	ListConfiguredGroupIDs(ctx context.Context) ([]int64, error)
}

// preload 同步加载这些分组的快照，返回加载失败（落入兜底状态）的分组。失败的分组会被标记失效，下一轮重试。
func (p *matrixPolicy) preload(ctx context.Context, groupIDs []int64) (failed []int64) {
	for _, id := range groupIDs {
		if p.loadSnapshot(ctx, id).loadErr != nil {
			failed = append(failed, id)
		}
	}
	if len(failed) > 0 {
		// 兜底快照会在缓存里留 errTTL，不标记失效的话下一轮重试读到的还是它。
		p.invalidate(failed)
	}
	return failed
}

// Preload 启动时调用：先列出有配置行的分组，再同步加载它们的快照，失败的重试几次。
// 全部重试之后仍有失败时只记 Error 日志与计数（这些分组在后台刷新成功之前按 legacy 处理），不阻止启动：
// 数据库不可用时网关本来就无法鉴权与记账，这里不另设一道启动闸门。lister 为 nil 时什么也不做。
func (s *stagedPolicy) Preload(ctx context.Context, lister ConfiguredGroupLister) {
	if s == nil || s.matrix == nil || lister == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, matrixPreloadTimeout)
	defer cancel()

	var ids []int64
	for attempt := 1; attempt <= matrixPreloadAttempts; attempt++ {
		var err error
		if ids == nil {
			ids, err = lister.ListConfiguredGroupIDs(ctx)
		}
		if err == nil {
			if ids == nil {
				ids = []int64{}
			}
			ids = s.matrix.preload(ctx, ids)
			if len(ids) == 0 {
				return
			}
		} else {
			slog.Warn("pricing matrix preload: list configured groups failed", "attempt", attempt, "error", err)
		}
		if attempt < matrixPreloadAttempts {
			select {
			case <-ctx.Done():
				attempt = matrixPreloadAttempts
			case <-time.After(s.preloadRetryDelay()):
			}
		}
	}
	s.matrix.preloadFailures.Add(int64(len(ids)))
	slog.Error("pricing matrix preload incomplete: these groups stay on legacy until the background refresh succeeds",
		"group_ids", ids)
}

func (s *stagedPolicy) preloadRetryDelay() time.Duration {
	if s.retryDelay > 0 {
		return s.retryDelay
	}
	return matrixPreloadRetryDelay
}
