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

// ConfiguredGroup 是一个有 group_model_config 行的分组及其阶段。
type ConfiguredGroup struct {
	ID    int64
	Stage PricingStage
}

// ConfiguredGroupLister 列出有 group_model_config 行的（未软删除的）分组及其阶段。由矩阵仓库实现，是可选的读侧能力。
type ConfiguredGroupLister interface {
	ListConfiguredGroups(ctx context.Context) ([]ConfiguredGroup, error)
}

// preload 同步加载这些分组的快照，返回加载失败（落入兜底状态）的分组。失败的分组会被标记失效，下一轮重试。
func (p *matrixPolicy) preload(ctx context.Context, groupIDs []int64) (failed []int64) {
	for i, id := range groupIDs {
		if ctx.Err() != nil {
			// 超时或被取消：剩下的分组没有加载，算失败（loadSnapshot 自己带 10 秒超时，不受这里的 deadline 管）。
			failed = append(failed, groupIDs[i:]...)
			break
		}
		// 加载期间收到失效通知时，快照没有存进缓存（代数变了）却也没有 loadErr：只看 loadErr 会把它算成功，
		// 所以还要确认缓存里确实有一份新鲜的好快照（W6 PR7a 审查 1(c)）。
		if p.loadSnapshot(ctx, id).loadErr != nil || !p.cachedReady(id) {
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
	listed := false
	for attempt := 1; attempt <= matrixPreloadAttempts; attempt++ {
		var err error
		if !listed {
			var groups []ConfiguredGroup
			groups, err = lister.ListConfiguredGroups(ctx)
			if err == nil {
				ids = make([]int64, 0, len(groups))
				mayBeV2 := make([]int64, 0, len(groups))
				for _, g := range groups {
					ids = append(ids, g.ID)
					if g.Stage != PricingStageLegacy {
						mayBeV2 = append(mayBeV2, g.ID)
					}
				}
				listed = true
				// 记下启动时处于 shadow、v2 的分组：之后快照加载不出来时，只有它们（可能是 v2）会被拒绝。
				// legacy 分组（有配置行也一样）的计费不读矩阵，加载失败照旧按 legacy 放行，与 7a、main 一致。
				// 预加载仍然覆盖全部有配置行的分组（ids）。
				s.matrix.setConfiguredList(mayBeV2)
			}
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
	if !listed {
		// 三次都没能列出有配置行的分组：不知道哪些是 v2，计一次失败，快照不可用时所有分组都按「可能是 v2」处理。
		s.matrix.preloadFailures.Add(1)
		slog.Error("pricing matrix preload failed: could not list the configured groups, a group whose snapshot cannot be loaded will be rejected")
		return
	}
	s.matrix.preloadFailures.Add(int64(len(ids)))
	slog.Error("pricing matrix preload incomplete: requests of these groups are rejected until a snapshot loads",
		"group_ids", ids)
}

func (s *stagedPolicy) preloadRetryDelay() time.Duration {
	if s.retryDelay > 0 {
		return s.retryDelay
	}
	return matrixPreloadRetryDelay
}
