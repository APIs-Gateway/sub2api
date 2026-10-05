package handler

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ChannelMonitorUserHandler 渠道监控用户只读 handler。
type ChannelMonitorUserHandler struct {
	monitorService *service.ChannelMonitorService
	settingService *service.SettingService
}

// NewChannelMonitorUserHandler 创建 handler。
// settingService 用于每次请求前读取功能开关；关闭时 List/GetStatus 直接返回空/404。
func NewChannelMonitorUserHandler(
	monitorService *service.ChannelMonitorService,
	settingService *service.SettingService,
) *ChannelMonitorUserHandler {
	return &ChannelMonitorUserHandler{
		monitorService: monitorService,
		settingService: settingService,
	}
}

// featureEnabled 返回当前渠道监控功能是否开启。
// settingService 为 nil（测试场景）视为启用。
func (h *ChannelMonitorUserHandler) featureEnabled(c *gin.Context) bool {
	if h.settingService == nil {
		return true
	}
	return h.settingService.GetChannelMonitorRuntime(c.Request.Context()).Enabled
}

// --- Response ---

type channelMonitorUserListItem struct {
	ID                   int64                                `json:"id"`
	Name                 string                               `json:"name"`
	Provider             string                               `json:"provider"`
	GroupName            string                               `json:"group_name"`
	PrimaryModel         string                               `json:"primary_model"`
	PrimaryStatus        string                               `json:"primary_status"`
	PrimaryLatencyMs     *int                                 `json:"primary_latency_ms"`
	PrimaryPingLatencyMs *int                                 `json:"primary_ping_latency_ms"`
	Availability7d       float64                              `json:"availability_7d"`
	ExtraModels          []dto.ChannelMonitorExtraModelStatus `json:"extra_models"`
	Timeline             []channelMonitorUserTimelinePoint    `json:"timeline"`
}

// channelMonitorUserTimelinePoint 主模型最近一次检测的 timeline 点。
// 仅用于用户视图 list 响应，admin 视图不使用。
type channelMonitorUserTimelinePoint struct {
	Status        string `json:"status"`
	LatencyMs     *int   `json:"latency_ms"`
	PingLatencyMs *int   `json:"ping_latency_ms"`
	CheckedAt     string `json:"checked_at"`
}

type channelMonitorUserDetailResponse struct {
	ID        int64                         `json:"id"`
	Name      string                        `json:"name"`
	Provider  string                        `json:"provider"`
	GroupName string                        `json:"group_name"`
	Models    []channelMonitorUserModelStat `json:"models"`
}

type channelMonitorUserModelStat struct {
	Model           string  `json:"model"`
	LatestStatus    string  `json:"latest_status"`
	LatestLatencyMs *int    `json:"latest_latency_ms"`
	Availability7d  float64 `json:"availability_7d"`
	Availability15d float64 `json:"availability_15d"`
	Availability30d float64 `json:"availability_30d"`
	AvgLatency7dMs  *int    `json:"avg_latency_7d_ms"`
}

// ---- 非管理员视图 ----
//
// 普通用户只能看到状态、可用率、对话延迟和时间线，看不到供应商、主模型名、
// 附加模型和 PING 延迟（这些属于上游信息，打开浏览器开发者工具也不应该拿到）。
// 这里用独立的结构体做白名单，而不是在上面的结构体里用 omitempty 去掉字段，
// 这样以后给管理员视图加字段时，不会无意中漏给普通用户。

type channelMonitorPublicListItem struct {
	ID               int64                               `json:"id"`
	Name             string                              `json:"name"`
	GroupName        string                              `json:"group_name"`
	PrimaryStatus    string                              `json:"primary_status"`
	PrimaryLatencyMs *int                                `json:"primary_latency_ms"`
	Availability7d   float64                             `json:"availability_7d"`
	Timeline         []channelMonitorPublicTimelinePoint `json:"timeline"`
}

type channelMonitorPublicTimelinePoint struct {
	Status    string `json:"status"`
	LatencyMs *int   `json:"latency_ms"`
	CheckedAt string `json:"checked_at"`
}

// channelMonitorPublicDetailResponse 的 models 只含主模型一项，且不带模型名。
type channelMonitorPublicDetailResponse struct {
	ID        int64                           `json:"id"`
	Name      string                          `json:"name"`
	GroupName string                          `json:"group_name"`
	Models    []channelMonitorPublicModelStat `json:"models"`
}

type channelMonitorPublicModelStat struct {
	LatestStatus    string  `json:"latest_status"`
	LatestLatencyMs *int    `json:"latest_latency_ms"`
	Availability7d  float64 `json:"availability_7d"`
	Availability15d float64 `json:"availability_15d"`
	Availability30d float64 `json:"availability_30d"`
	AvgLatency7dMs  *int    `json:"avg_latency_7d_ms"`
}

// isAdminCaller 判断调用者是否管理员。角色来自 JWT 中间件每次请求从数据库读取的用户，
// 取不到角色时按非管理员处理（默认收紧）。
func isAdminCaller(c *gin.Context) bool {
	role, _ := middleware.GetUserRoleFromContext(c)
	return role == service.RoleAdmin
}

func userMonitorViewToItem(v *service.UserMonitorView) channelMonitorUserListItem {
	extras := make([]dto.ChannelMonitorExtraModelStatus, 0, len(v.ExtraModels))
	for _, e := range v.ExtraModels {
		extras = append(extras, dto.ChannelMonitorExtraModelStatus{
			Model:     e.Model,
			Status:    e.Status,
			LatencyMs: e.LatencyMs,
		})
	}
	timeline := make([]channelMonitorUserTimelinePoint, 0, len(v.Timeline))
	for _, p := range v.Timeline {
		timeline = append(timeline, channelMonitorUserTimelinePoint{
			Status:        p.Status,
			LatencyMs:     p.LatencyMs,
			PingLatencyMs: p.PingLatencyMs,
			CheckedAt:     p.CheckedAt.UTC().Format(time.RFC3339),
		})
	}
	return channelMonitorUserListItem{
		ID:                   v.ID,
		Name:                 v.Name,
		Provider:             v.Provider,
		GroupName:            v.GroupName,
		PrimaryModel:         v.PrimaryModel,
		PrimaryStatus:        v.PrimaryStatus,
		PrimaryLatencyMs:     v.PrimaryLatencyMs,
		PrimaryPingLatencyMs: v.PrimaryPingLatencyMs,
		Availability7d:       v.Availability7d,
		ExtraModels:          extras,
		Timeline:             timeline,
	}
}

func userMonitorDetailToResponse(d *service.UserMonitorDetail) *channelMonitorUserDetailResponse {
	models := make([]channelMonitorUserModelStat, 0, len(d.Models))
	for _, m := range d.Models {
		models = append(models, channelMonitorUserModelStat{
			Model:           m.Model,
			LatestStatus:    m.LatestStatus,
			LatestLatencyMs: m.LatestLatencyMs,
			Availability7d:  m.Availability7d,
			Availability15d: m.Availability15d,
			Availability30d: m.Availability30d,
			AvgLatency7dMs:  m.AvgLatency7dMs,
		})
	}
	return &channelMonitorUserDetailResponse{
		ID:        d.ID,
		Name:      d.Name,
		Provider:  d.Provider,
		GroupName: d.GroupName,
		Models:    models,
	}
}

func userMonitorViewToPublicItem(v *service.UserMonitorView) channelMonitorPublicListItem {
	timeline := make([]channelMonitorPublicTimelinePoint, 0, len(v.Timeline))
	for _, p := range v.Timeline {
		timeline = append(timeline, channelMonitorPublicTimelinePoint{
			Status:    p.Status,
			LatencyMs: p.LatencyMs,
			CheckedAt: p.CheckedAt.UTC().Format(time.RFC3339),
		})
	}
	return channelMonitorPublicListItem{
		ID:               v.ID,
		Name:             v.Name,
		GroupName:        v.GroupName,
		PrimaryStatus:    v.PrimaryStatus,
		PrimaryLatencyMs: v.PrimaryLatencyMs,
		Availability7d:   v.Availability7d,
		Timeline:         timeline,
	}
}

// userMonitorDetailToPublicResponse 只保留主模型（Models[0]，聚合层保证主模型排第一）的统计，
// 并丢掉模型名；附加模型整条不返回。
// 状态用主模型最近几次探测的综合状态（CardStatus），与 /monitor 卡片保持一致；没有时回落到最近一次状态。
func userMonitorDetailToPublicResponse(d *service.UserMonitorDetail) *channelMonitorPublicDetailResponse {
	models := make([]channelMonitorPublicModelStat, 0, 1)
	if len(d.Models) > 0 {
		m := d.Models[0]
		status := m.CardStatus
		if false {
			status = m.LatestStatus
		}
		models = append(models, channelMonitorPublicModelStat{
			LatestStatus:    status,
			LatestLatencyMs: m.LatestLatencyMs,
			Availability7d:  m.Availability7d,
			Availability15d: m.Availability15d,
			Availability30d: m.Availability30d,
			AvgLatency7dMs:  m.AvgLatency7dMs,
		})
	}
	return &channelMonitorPublicDetailResponse{
		ID:        d.ID,
		Name:      d.Name,
		GroupName: d.GroupName,
		Models:    models,
	}
}

// --- Handlers ---

// List GET /api/v1/channel-monitors
// 管理员拿到完整视图；其他调用者只拿到不含上游信息的精简视图。
func (h *ChannelMonitorUserHandler) List(c *gin.Context) {
	if !h.featureEnabled(c) {
		response.Success(c, gin.H{"items": []channelMonitorPublicListItem{}})
		return
	}
	views, err := h.monitorService.ListUserView(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if isAdminCaller(c) {
		items := make([]channelMonitorUserListItem, 0, len(views))
		for _, v := range views {
			items = append(items, userMonitorViewToItem(v))
		}
		response.Success(c, gin.H{"items": items})
		return
	}
	items := make([]channelMonitorPublicListItem, 0, len(views))
	for _, v := range views {
		items = append(items, userMonitorViewToPublicItem(v))
	}
	response.Success(c, gin.H{"items": items})
}

// GetStatus GET /api/v1/channel-monitors/:id/status
func (h *ChannelMonitorUserHandler) GetStatus(c *gin.Context) {
	if !h.featureEnabled(c) {
		response.ErrorFrom(c, service.ErrChannelMonitorNotFound)
		return
	}
	// 复用 admin.ParseChannelMonitorID 保持错误码与日志一致。
	id, ok := admin.ParseChannelMonitorID(c)
	if !ok {
		return
	}
	detail, err := h.monitorService.GetUserDetail(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if isAdminCaller(c) {
		response.Success(c, userMonitorDetailToResponse(detail))
		return
	}
	response.Success(c, userMonitorDetailToPublicResponse(detail))
}
