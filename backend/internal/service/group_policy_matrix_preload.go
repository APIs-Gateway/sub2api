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

	// 重新列出配置分组：每 60 秒一次，收到失效通知后去抖 500 毫秒再列一次。
	matrixConfiguredRelistInterval = 60 * time.Second
	matrixConfiguredRelistDebounce = 500 * time.Millisecond
)

// splitConfiguredGroups 把列表拆成全部分组 id（预加载用）与可能是 v2 的分组 id（shadow、v2 阶段）。
func splitConfiguredGroups(groups []ConfiguredGroup) (all, mayBeV2 []int64) {
	all = make([]int64, 0, len(groups))
	mayBeV2 = make([]int64, 0, len(groups))
	for _, g := range groups {
		all = append(all, g.ID)
		if g.Stage != PricingStageLegacy {
			mayBeV2 = append(mayBeV2, g.ID)
		}
	}
	return all, mayBeV2
}

// startConfiguredRefresh 记下 lister 并启动 60 秒一次的重新列出（只启动一次）。启动时列表没取到的实例也靠它补上。
func (p *matrixPolicy) startConfiguredRefresh(lister ConfiguredGroupLister) {
	p.relistOnce.Do(func() {
		p.relistMu.Lock()
		p.lister = lister
		p.relistMu.Unlock()
		interval := p.relistInterval
		if interval <= 0 {
			interval = matrixConfiguredRelistInterval
		}
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for range ticker.C {
				p.relistConfigured()
			}
		}()
	})
}

// relistConfigured 重新列一次配置分组，只往「可能是 v2」集合里加 shadow、v2 的分组，不移除。失败只记 Warn。
func (p *matrixPolicy) relistConfigured() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("pricing matrix relist configured groups panicked", "panic", r)
		}
	}()
	p.relistMu.Lock()
	lister := p.lister
	p.relistMu.Unlock()
	if lister == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), matrixSnapshotDBTimeout)
	defer cancel()
	groups, err := lister.ListConfiguredGroups(ctx)
	if err != nil {
		slog.Warn("pricing matrix: relist configured groups failed", "error", err)
		return
	}
	_, mayBeV2 := splitConfiguredGroups(groups)
	p.setConfiguredList(mayBeV2)
}

// kickRelist 收到失效通知时调用：去抖之后重新列一次。等待期间的后续通知并入同一次。
func (p *matrixPolicy) kickRelist() {
	p.relistMu.Lock()
	has := p.lister != nil
	p.relistMu.Unlock()
	if !has || !p.relistPending.CompareAndSwap(false, true) {
		return
	}
	delay := p.relistDebounce
	if delay <= 0 {
		delay = matrixConfiguredRelistDebounce
	}
	go func() {
		time.Sleep(delay)
		p.relistPending.Store(false)
		p.relistConfigured()
	}()
}

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
// 全部重试之后仍有失败时只记 Error 日志与计数（legacy 分组按 legacy 处理；shadow、v2 分组在快照加载成功之前会被拒绝），不阻止启动：
// 数据库不可用时网关本来就无法鉴权与记账，这里不另设一道启动闸门。lister 为 nil 时什么也不做。
func (s *stagedPolicy) Preload(ctx context.Context, lister ConfiguredGroupLister) {
	if s == nil || s.matrix == nil || lister == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, matrixPreloadTimeout)
	defer cancel()
	s.matrix.startConfiguredRefresh(lister)

	var ids []int64
	listed := false
	for attempt := 1; attempt <= matrixPreloadAttempts; attempt++ {
		var err error
		if !listed {
			var groups []ConfiguredGroup
			groups, err = lister.ListConfiguredGroups(ctx)
			if err == nil {
				var mayBeV2 []int64
				ids, mayBeV2 = splitConfiguredGroups(groups)
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
		// 三次都没能列出有配置行的分组：不知道哪些是 v2，计一次失败，快照不可用时所有分组都按「可能是 v2」处理，
		// 直到 60 秒一次的重新列出成功为止。
		s.matrix.preloadFailures.Add(1)
		slog.Error("pricing matrix preload failed: could not list the configured groups, until the periodic relist succeeds a group whose snapshot cannot be loaded will be rejected")
		return
	}
	s.matrix.preloadFailures.Add(int64(len(ids)))
	slog.Error("pricing matrix preload incomplete: these groups have no snapshot yet (requests of shadow and v2 groups among them are rejected until one loads, legacy groups are served as legacy)",
		"group_ids", ids)
}

func (s *stagedPolicy) preloadRetryDelay() time.Duration {
	if s.retryDelay > 0 {
		return s.retryDelay
	}
	return matrixPreloadRetryDelay
}
