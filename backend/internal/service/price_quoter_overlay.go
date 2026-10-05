package service

import (
	"context"
	"sort"
	"sync"
)

// W6 PR4b-2b-1：QuoteWith / BatchQuoteWith（设计 3.3）——在分组现状之上叠一层「假想的」单元格或分组配置再报价，
// 供价格写入的估算器（pricing_estimator.go）和管理页的「改前改后」预览用。
//
// 做法：每次调用造一个独立的 matrixPolicy，它的数据源是「真实数据源加叠加层」，再把报价器的价格解析器换成用这个策略的副本。
// 所以报价走的还是 Quote 自己的全部逻辑（取价、倍率、准入、选路），叠加层只改变「单元格与分组配置读到什么」。
//
// 叠加层只作用在 matrixPolicy 上：QuoteWith 报的是「这个分组按 v2 的单元格语义会怎么报价」，与分组今天的阶段无关
// （写入只发生在 v2 分组上，写完以后读的就是矩阵）。报价器自己原来的策略（legacy 或将来的 stagedPolicy）不受影响。

// CellOverlay 叠加层：分组的目标态单元格与目标态分组配置。零值就是「不叠加任何东西」，即分组现状。
type CellOverlay struct {
	// Cells 分组 -> 归一化后的模型名 -> 目标态单元格；值为 nil 表示这个单元格被删除。
	Cells map[int64]map[string]*MatrixCell
	// Configs 分组 -> 目标态分组配置（整个替换）。
	Configs map[int64]MatrixGroupConfig
}

// OverlayFromPlanned 由规划好的单元格写入得到叠加层：删除叠成「没有这个单元格」，创建与更新叠成目标态，noop 不叠。
func OverlayFromPlanned(planned []PlannedCellWrite) CellOverlay {
	out := CellOverlay{}
	for _, p := range planned {
		var target *MatrixCell
		switch p.Action {
		case CellWriteNoop:
			continue
		case CellWriteDelete:
		default:
			if p.After == nil {
				continue
			}
			c := *p.After
			target = &c
		}
		if out.Cells == nil {
			out.Cells = make(map[int64]map[string]*MatrixCell)
		}
		if out.Cells[p.Op.GroupID] == nil {
			out.Cells[p.Op.GroupID] = make(map[string]*MatrixCell)
		}
		out.Cells[p.Op.GroupID][normalizeChannelPricingModelName(p.Op.ModelKey)] = target
	}
	return out
}

// quoteOverlaySource 叠加层之下的真实数据源（只读），与 MatrixSnapshotSource 的方法集相同；
// 单独起一个名字是为了让 price_quoter.go 不必引用矩阵侧的类型名。*PricingMatrixRepository 满足它。
type quoteOverlaySource interface {
	GetGroupMeta(ctx context.Context, groupIDs []int64) (map[int64]DeriveGroup, error)
	LoadGroupSnapshots(ctx context.Context, groupIDs []int64) (map[int64]GroupStateSnapshot, error)
}

// SetMatrixSource 给报价器接上分组现状的只读数据源，之后才能用 QuoteWith 与 BatchQuoteWith。
// 只在装配阶段调用，不加锁；不调用或传 nil 时，QuoteWith 返回 ErrPriceQuoterUnavailable。
func (q *PriceQuoter) SetMatrixSource(src quoteOverlaySource) {
	q.matrixSource = src
}

// overlaySnapshotSource 在真实数据源读到的快照上套叠加层，并记下数据源返回的第一个错误。
// matrixPolicy 读快照失败时从不报错，而是退回「开放、没有单元格、没有额外倍率」的默认状态；
// 估算器要是照这个状态报价，写入前后两份报价会相同，结果就成了 none。所以报价之后必须检查这里记下的错误（见 failure）。
type overlaySnapshotSource struct {
	inner   quoteOverlaySource
	overlay CellOverlay

	mu  sync.Mutex
	err error
}

func (s *overlaySnapshotSource) record(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

// failure 返回读快照时遇到的第一个错误；没有出错返回 nil。
func (s *overlaySnapshotSource) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

var _ MatrixSnapshotSource = (*overlaySnapshotSource)(nil)

func (s *overlaySnapshotSource) GetGroupMeta(ctx context.Context, groupIDs []int64) (map[int64]DeriveGroup, error) {
	metas, err := s.inner.GetGroupMeta(ctx, groupIDs)
	if err != nil {
		s.record(err)
	}
	return metas, err
}

func (s *overlaySnapshotSource) LoadGroupSnapshots(ctx context.Context, groupIDs []int64) (map[int64]GroupStateSnapshot, error) {
	snaps, err := s.inner.LoadGroupSnapshots(ctx, groupIDs)
	if err != nil {
		s.record(err)
		return nil, err
	}
	out := make(map[int64]GroupStateSnapshot, len(snaps))
	for id, snap := range snaps {
		out[id] = s.overlay.applyTo(id, snap)
	}
	return out, nil
}

// applyTo 把叠加层套到一个分组的快照上，返回新的快照，不改入参。
// 分组没有配置行时不凭空造一行：写入器本来就拒绝这样的分组，叠加层也不替它补。
func (o CellOverlay) applyTo(groupID int64, snap GroupStateSnapshot) GroupStateSnapshot {
	cfg, hasCfg := o.Configs[groupID]
	cells := o.Cells[groupID]
	if !hasCfg && len(cells) == 0 {
		return snap
	}
	out := snap
	if hasCfg && snap.Config != nil {
		c := *snap.Config
		c.MatrixGroupConfig = cfg
		out.Config = &c
	}
	if len(cells) > 0 {
		out.Cells = overlayCells(groupID, snap.Cells, cells)
	}
	return out
}

func overlayCells(groupID int64, existing []StoredMatrixCell, overlay map[string]*MatrixCell) []StoredMatrixCell {
	out := make([]StoredMatrixCell, 0, len(existing)+len(overlay))
	applied := make(map[string]struct{}, len(overlay))
	for _, c := range existing {
		if !c.IsPattern {
			key := normalizeChannelPricingModelName(c.ModelKey)
			if target, ok := overlay[key]; ok {
				applied[key] = struct{}{}
				if target == nil {
					continue
				}
				c.MatrixCell = *target
				c.IsPattern = false
			}
		}
		out = append(out, c)
	}
	keys := make([]string, 0, len(overlay))
	for key, target := range overlay {
		if _, done := applied[key]; !done && target != nil {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, StoredMatrixCell{GroupID: groupID, MatrixCell: *overlay[key]})
	}
	return out
}

// withOverlay 返回一个报价器副本：价格解析器的分组策略换成「真实数据源加叠加层」的 matrixPolicy。
// 副本与原报价器共用计费服务、分组读取、倍率与目录，所以报价逻辑完全相同。
// 返回的数据源要在报价之后调用 failure：读快照出过错，这次报价就不可信。
func (q *PriceQuoter) withOverlay(overlay CellOverlay) (*PriceQuoter, *overlaySnapshotSource, error) {
	if q == nil || q.resolver == nil || q.billing == nil || q.groups == nil || q.matrixSource == nil {
		return nil, nil, ErrPriceQuoterUnavailable
	}
	src := &overlaySnapshotSource{inner: q.matrixSource, overlay: overlay}
	resolver := *q.resolver
	resolver.policyOverride = NewMatrixGroupPolicy(src, nil)
	scoped := *q
	scoped.resolver = &resolver
	return &scoped, src, nil
}

// QuoteWith 在分组现状之上叠加 overlay 再报价。overlay 为零值时报的就是分组按矩阵语义的现状。
// 读分组现状失败时返回错误（不会退回「没有单元格」的默认状态报价）。
func (q *PriceQuoter) QuoteWith(ctx context.Context, req QuoteRequest, overlay CellOverlay) (*Quote, error) {
	scoped, src, err := q.withOverlay(overlay)
	if err != nil {
		return nil, err
	}
	quote, err := scoped.Quote(ctx, req)
	if ferr := src.failure(); ferr != nil {
		return nil, ferr
	}
	return quote, err
}

// BatchQuoteWith 是 BatchQuote 的叠加版：同一批请求共用一份叠加层，分组与快照各只读一次。
// 结果与 reqs 一一对应，单个请求失败时该位置为 nil；报价器不可用或读分组现状失败时整体返回错误。
func (q *PriceQuoter) BatchQuoteWith(ctx context.Context, reqs []QuoteRequest, overlay CellOverlay) ([]*Quote, error) {
	scoped, src, err := q.withOverlay(overlay)
	if err != nil {
		return nil, err
	}
	quotes := scoped.batchQuote(ctx, reqs)
	if ferr := src.failure(); ferr != nil {
		return nil, ferr
	}
	return quotes, nil
}
