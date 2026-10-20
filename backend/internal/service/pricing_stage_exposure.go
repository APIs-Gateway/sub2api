package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6：shadow 切 v2 时的无价与开放范围检查（#1661 审查 B1）。
//
// 派生写入豁免保存时校验（设计 4.2、S-6），所以 shadow 阶段的白名单分组里可以带着三类问题单元格：open 的通配符、
// 价格全空的 per_request / image 空 custom（0 元）、没有官方价的 inherit。矩阵对 shadow 分组不生效，这些行在那之前
// 不影响任何请求；一旦分组进入 v2 它们就按目标态生效（0 元放行），而且之后每一次快照批准都会因它们失败。
// 设计第 713 行要求切换前把这些行列出来由管理员决定「补价还是接受」，这里把它做成切换的硬检查：
//
//   - 预览：对「按渠道当前配置派生之后」的目标态评估，不写库；有问题就作为闸门失败项列出，不登记凭证；
//   - 提交：在写事务里、冻结派生行并改完阶段之后，读事务自己的状态再检查一遍，有问题整体回滚（凭证随之不消耗）。
//
// 「接受」的出口保持原样：精确的无价或 0 元单元格加进已知免费名单就放行；通配符放行的范围没有办法逐个模型验证，
// 只能在渠道上去掉。

// StageExposureChecker 阶段切换用的暴露检查：保存时校验（ExposureGuard，按分组 id 读，不看阶段）加开放时预检的 v2 目标态评估。
type StageExposureChecker struct {
	guard    *ExposureGuard
	precheck *OpenPrechecker
}

// NewStageExposureChecker 创建检查器；reader、validator 与 prices 和价格写入路径共用同一份。
func NewStageExposureChecker(reader ExposureReader, validator *ExposureValidator, prices OfficialPriceStateSource) *StageExposureChecker {
	return &StageExposureChecker{
		guard:    NewExposureGuard(reader, validator),
		precheck: NewOpenPrechecker(nil, validator, prices),
	}
}

// CheckInTx 提交用：在写事务里，分组已经追平派生行、冻结并改成 v2 之后调用。snap 是同一个事务读出的分组快照。
// 返回白名单分组里会出现的问题；没有问题返回空。
//
// 两层检查都在 tx 里读：
//   - ExposureGuard.CheckGroups 按分组 id 读准入模式与 open 单元格，不依赖阶段，和所有写入路径的保存时校验同一口径；
//   - OpenPrechecker.EvaluateAsV2 在快照上评估，补上映射目标（精确映射命中 open 单元格时目标要官方有价且非零）。
//
// 不能用 PrecheckGroups：它走连接池读已提交状态，看到的仍是 shadow，也看不到事务里刚做的冻结与改阶段。
func (c *StageExposureChecker) CheckInTx(ctx context.Context, tx MatrixExecutor, groupID int64, snap GroupStateSnapshot) ([]OpenPrecheckIssue, error) {
	if c == nil || c.guard == nil || c.precheck == nil {
		return nil, infraerrors.InternalServer(ReasonExposureGuardMissing, "exposure validation is not configured")
	}
	guardErr := c.guard.CheckGroups(ctx, tx, []int64{groupID})
	if guardErr != nil && infraerrors.Reason(guardErr) != ReasonExposureUnpriced {
		return nil, guardErr // 读库失败等：失败关闭，交回调用方
	}
	rep, err := c.precheck.EvaluateAsV2(ctx, groupID, snap)
	if err != nil {
		return nil, err
	}
	issues := rep.Blocking
	if guardErr != nil && len(issues) == 0 {
		// 两层读到的状态不一致（不应发生）：以保存时校验为准，失败关闭。
		issues = []OpenPrecheckIssue{{GroupID: groupID, ModelKey: "*", Reason: string(ExposureUnpriced)}}
	}
	return issues, nil
}

// CheckTarget 预览用：评估一份假想的目标态快照（只读，不碰数据库）。
func (c *StageExposureChecker) CheckTarget(ctx context.Context, groupID int64, target GroupStateSnapshot) ([]OpenPrecheckIssue, error) {
	if c == nil || c.precheck == nil {
		return nil, infraerrors.InternalServer(ReasonExposureGuardMissing, "exposure validation is not configured")
	}
	rep, err := c.precheck.EvaluateAsV2(ctx, groupID, target)
	if err != nil {
		return nil, err
	}
	return rep.Blocking, nil
}

// applyPlanToSnapshot 在内存里把派生的落库计划应用到快照上，得到「追平派生行之后」的目标态（预览用，不写库）。
// 计划被跳过（分组已经是 v2）时原样返回。
func applyPlanToSnapshot(snap GroupStateSnapshot, plan GroupApplyPlan) GroupStateSnapshot {
	if plan.Skipped {
		return snap
	}
	out := GroupStateSnapshot{Rules: snap.Rules}
	switch {
	case snap.Config != nil:
		cfg := *snap.Config
		if plan.ConfigWrite != nil {
			cfg.MatrixGroupConfig = *plan.ConfigWrite
		}
		out.Config = &cfg
	case plan.ConfigWrite != nil:
		out.Config = &StoredGroupConfig{GroupID: plan.GroupID, MatrixGroupConfig: *plan.ConfigWrite, PricingStage: PricingStageLegacy}
	}

	deleted := make(map[int64]struct{}, len(plan.CellDeletes))
	for _, id := range plan.CellDeletes {
		deleted[id] = struct{}{}
	}
	updated := make(map[int64]StoredMatrixCell, len(plan.CellUpdates))
	for _, c := range plan.CellUpdates {
		updated[c.ID] = c
	}
	out.Cells = make([]StoredMatrixCell, 0, len(snap.Cells)+len(plan.CellInserts))
	for _, c := range snap.Cells {
		if _, gone := deleted[c.ID]; gone {
			continue
		}
		if u, ok := updated[c.ID]; ok {
			out.Cells = append(out.Cells, u)
			continue
		}
		out.Cells = append(out.Cells, c)
	}
	for _, c := range plan.CellInserts {
		out.Cells = append(out.Cells, StoredMatrixCell{GroupID: plan.GroupID, MatrixCell: c})
	}
	return out
}

// maxStageExposureIssuesInMessage 闸门失败信息里最多列出的模型数（完整清单在 metadata.issues，最多 20 项）。
const maxStageExposureIssuesInMessage = 10

// stageExposureMessage 给管理员看的说明：列出模型与问题类型，并说清楚两种出口。
func stageExposureMessage(issues []OpenPrecheckIssue) string {
	items := make([]string, 0, len(issues))
	for i, is := range issues {
		if i == maxStageExposureIssuesInMessage {
			items = append(items, fmt.Sprintf("and %d more", len(issues)-i))
			break
		}
		item := is.ModelKey + " (" + is.Reason
		if is.Target != "" {
			item += " -> " + is.Target
		}
		items = append(items, item+")")
	}
	return fmt.Sprintf("the allowlist group would expose %d model(s) without a usable price after moving to v2: %s. "+
		"Set a price for them, or add the exact models to the known-free list; an open wildcard can only be removed on the channel",
		len(issues), strings.Join(items, ", "))
}

// stageExposureFailure 预览里的闸门失败项。
func stageExposureFailure(issues []OpenPrecheckIssue) StageGateFailure {
	return StageGateFailure{Code: ReasonPricingGateExposureBlocked, Message: stageExposureMessage(issues)}
}

// StageExposureError 提交时的 409：reason 是 PRICING_GATE_EXPOSURE_BLOCKED，metadata 与其他闸门错误同形
// （group_id、failures），再加 count 与 issues（分组:模型:原因，最多 20 项）。
func StageExposureError(groupID int64, issues []OpenPrecheckIssue) error {
	return infraerrors.Conflict(ReasonPricingGateExposureBlocked, stageExposureMessage(issues)).WithMetadata(map[string]string{
		"group_id": strconv.FormatInt(groupID, 10),
		"failures": ReasonPricingGateExposureBlocked,
		"count":    strconv.Itoa(len(issues)),
		"issues":   openIssueList(issues),
	})
}
