package service

import (
	"context"
	"log/slog"
	"sync"
)

// BatchQuote 对一批请求逐个报价，结果与 reqs 一一对应；单个请求失败（缺少模型、分组不存在等）时该位置为 nil。
//
// 与逐个调用 Quote 的区别只有一处：同一批里相同的分组只读一次（价格页是「用户 × 可见分组 × 模型」的笛卡尔积，
// 同一个分组会出现几百次）。取价、倍率、准入全部还是 Quote 自己的逻辑，所以每个结果与单独调用 Quote 逐位相同。
// 返回的 Quote 可以继续调用 Cost。
func (q *PriceQuoter) BatchQuote(ctx context.Context, reqs []QuoteRequest) []*Quote {
	return q.batchQuote(ctx, reqs)
}

// batchQuote 是 BatchQuote 的实现；BatchQuoteWith 在带单元格叠加的报价器上复用它。
func (q *PriceQuoter) batchQuote(ctx context.Context, reqs []QuoteRequest) []*Quote {
	out := make([]*Quote, len(reqs))
	if q == nil || q.resolver == nil || q.billing == nil || q.groups == nil {
		return out
	}
	scoped := *q
	scoped.groups = &batchGroupReader{inner: q.groups, cache: make(map[int64]batchGroupEntry)}
	failed := 0
	for i := range reqs {
		quote, err := scoped.Quote(ctx, reqs[i])
		if err != nil {
			failed++
			continue
		}
		out[i] = quote
	}
	if failed > 0 {
		slog.Warn("price quote batch: some requests could not be quoted", "failed", failed, "total", len(reqs))
	}
	return out
}

// GroupStage 返回分组当前的价格体系阶段，没有分组策略时按 legacy 处理。
// 价格页据此决定一个分组的模型清单从哪里来（legacy、shadow 读渠道，v2 读模型目录）；价格本身仍由 Quote 按阶段取。
func (q *PriceQuoter) GroupStage(ctx context.Context, groupID int64) PricingStage {
	if q == nil || q.resolver == nil {
		return PricingStageLegacy
	}
	policy := q.resolver.groupPolicy()
	if policy == nil {
		return PricingStageLegacy
	}
	return policy.Stage(ctx, groupID)
}

// batchGroupReader 在一批报价内缓存分组读取结果（含「不存在」）。Quote 只读取返回值，
// 但分组指针可能被调用方共用，所以每次返回一份浅拷贝。
type batchGroupReader struct {
	inner priceQuoteGroupReader
	mu    sync.Mutex
	cache map[int64]batchGroupEntry
}

type batchGroupEntry struct {
	group *Group
	err   error
}

func (r *batchGroupReader) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	r.mu.Lock()
	entry, ok := r.cache[id]
	r.mu.Unlock()
	if !ok {
		group, err := r.inner.GetByIDLite(ctx, id)
		entry = batchGroupEntry{group: group, err: err}
		r.mu.Lock()
		r.cache[id] = entry
		r.mu.Unlock()
	}
	if entry.err != nil || entry.group == nil {
		return nil, entry.err
	}
	cp := *entry.group
	return &cp, nil
}
