//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 以下取值只出现在“上游信息”字段里；非管理员响应的原始 JSON 里不能出现它们。
const (
	testMonitorProvider   = "anthropic"
	testMonitorPrimary    = "claude-opus-secret-model"
	testMonitorExtraModel = "claude-haiku-extra-model"
)

// monitorRepoStub 只实现用户视图聚合用到的几个方法，其余方法嵌入 nil 接口，被调用即 panic。
type monitorRepoStub struct {
	service.ChannelMonitorRepository
}

func (monitorRepoStub) ListEnabled(context.Context) ([]*service.ChannelMonitor, error) {
	return []*service.ChannelMonitor{monitorFixture()}, nil
}

func (monitorRepoStub) GetByID(context.Context, int64) (*service.ChannelMonitor, error) {
	return monitorFixture(), nil
}

func (monitorRepoStub) ListLatestForMonitorIDs(context.Context, []int64) (map[int64][]*service.ChannelMonitorLatest, error) {
	return map[int64][]*service.ChannelMonitorLatest{7: latestFixture()}, nil
}

func (monitorRepoStub) ListLatestPerModel(context.Context, int64) ([]*service.ChannelMonitorLatest, error) {
	return latestFixture(), nil
}

func (monitorRepoStub) ComputeAvailabilityForMonitors(context.Context, []int64, int) (map[int64][]*service.ChannelMonitorAvailability, error) {
	return map[int64][]*service.ChannelMonitorAvailability{7: availabilityFixture(7)}, nil
}

func (monitorRepoStub) ComputeAvailability(_ context.Context, _ int64, windowDays int) ([]*service.ChannelMonitorAvailability, error) {
	return availabilityFixture(windowDays), nil
}

func (monitorRepoStub) ListRecentHistoryForMonitors(context.Context, []int64, map[int64]string, int) (map[int64][]*service.ChannelMonitorHistoryEntry, error) {
	return map[int64][]*service.ChannelMonitorHistoryEntry{7: {
		{Model: testMonitorPrimary, Status: "operational", LatencyMs: intPtr(820), PingLatencyMs: intPtr(37), CheckedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)},
		{Model: testMonitorPrimary, Status: "degraded", LatencyMs: intPtr(2100), PingLatencyMs: intPtr(41), CheckedAt: time.Date(2026, 10, 4, 11, 59, 0, 0, time.UTC)},
	}}, nil
}

func intPtr(v int) *int { return &v }

func monitorFixture() *service.ChannelMonitor {
	return &service.ChannelMonitor{
		ID:           7,
		Name:         "GPT 通道",
		Provider:     testMonitorProvider,
		PrimaryModel: testMonitorPrimary,
		ExtraModels:  []string{testMonitorExtraModel},
		GroupName:    "GPT",
		Enabled:      true,
	}
}

func latestFixture() []*service.ChannelMonitorLatest {
	return []*service.ChannelMonitorLatest{
		{Model: testMonitorPrimary, Status: "operational", LatencyMs: intPtr(820), PingLatencyMs: intPtr(37)},
		{Model: testMonitorExtraModel, Status: "degraded", LatencyMs: intPtr(1500), PingLatencyMs: intPtr(40)},
	}
}

func availabilityFixture(windowDays int) []*service.ChannelMonitorAvailability {
	return []*service.ChannelMonitorAvailability{
		{Model: testMonitorPrimary, WindowDays: windowDays, AvailabilityPct: 99.5, AvgLatencyMs: intPtr(900)},
		{Model: testMonitorExtraModel, WindowDays: windowDays, AvailabilityPct: 88.8, AvgLatencyMs: intPtr(1700)},
	}
}

// callMonitorHandler 以指定角色调用 List（id 为空）或 GetStatus，返回响应里的 data 与原始 body。
// role 为空串表示上下文里没有角色（模拟异常链路）。
func callMonitorHandler(t *testing.T, role string, id string) (map[string]any, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewChannelMonitorUserHandler(service.NewChannelMonitorService(monitorRepoStub{}, nil), nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/channel-monitors", nil)
	if role != "" {
		c.Set(string(middleware.ContextKeyUserRole), role)
	}
	if id == "" {
		h.List(c)
	} else {
		c.Params = gin.Params{{Key: "id", Value: id}}
		h.GetStatus(c)
	}
	require.Equal(t, http.StatusOK, w.Code)

	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	return envelope.Data, w.Body.String()
}

func firstListItem(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	items, ok := data["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	return items[0].(map[string]any)
}

// 不应出现在非管理员响应里的 JSON 键和值。
var monitorUpstreamKeys = []string{
	"provider", "primary_model", "primary_ping_latency_ms", "ping_latency_ms", "extra_models",
}

func requireNoUpstreamInfo(t *testing.T, raw string) {
	t.Helper()
	for _, key := range monitorUpstreamKeys {
		require.NotContains(t, raw, `"`+key+`"`, "response leaks key %q", key)
	}
	for _, value := range []string{testMonitorProvider, testMonitorPrimary, testMonitorExtraModel} {
		require.False(t, strings.Contains(raw, value), "response leaks value %q", value)
	}
}

func TestChannelMonitorUserList_NonAdminHidesUpstreamInfo(t *testing.T) {
	for _, role := range []string{"user", ""} { // 空串 = 没有角色，必须按非管理员处理
		t.Run("role="+role, func(t *testing.T) {
			data, raw := callMonitorHandler(t, role, "")
			requireNoUpstreamInfo(t, raw)

			item := firstListItem(t, data)
			require.EqualValues(t, 7, item["id"])
			require.Equal(t, "GPT 通道", item["name"])
			require.Equal(t, "GPT", item["group_name"])
			require.Equal(t, "operational", item["primary_status"])
			require.EqualValues(t, 820, item["primary_latency_ms"])
			require.EqualValues(t, 99.5, item["availability_7d"])

			timeline := item["timeline"].([]any)
			require.Len(t, timeline, 2)
			point := timeline[0].(map[string]any)
			require.Equal(t, "operational", point["status"])
			require.EqualValues(t, 820, point["latency_ms"])
			require.Equal(t, "2026-10-04T12:00:00Z", point["checked_at"])
			require.Len(t, point, 3, "timeline point should only carry status, latency_ms, checked_at")
		})
	}
}

func TestChannelMonitorUserList_AdminKeepsFullView(t *testing.T) {
	data, _ := callMonitorHandler(t, service.RoleAdmin, "")
	item := firstListItem(t, data)

	require.Equal(t, testMonitorProvider, item["provider"])
	require.Equal(t, testMonitorPrimary, item["primary_model"])
	require.EqualValues(t, 37, item["primary_ping_latency_ms"])
	extras := item["extra_models"].([]any)
	require.Len(t, extras, 1)
	require.Equal(t, testMonitorExtraModel, extras[0].(map[string]any)["model"])
	point := item["timeline"].([]any)[0].(map[string]any)
	require.EqualValues(t, 37, point["ping_latency_ms"])
}

func TestChannelMonitorUserGetStatus_NonAdminOnlyPrimaryWithoutModelName(t *testing.T) {
	for _, role := range []string{"user", ""} {
		t.Run("role="+role, func(t *testing.T) {
			data, raw := callMonitorHandler(t, role, "7")
			requireNoUpstreamInfo(t, raw)
			require.NotContains(t, raw, `"model"`)

			require.Equal(t, "GPT 通道", data["name"])
			require.Equal(t, "GPT", data["group_name"])
			models := data["models"].([]any)
			require.Len(t, models, 1, "extra models must not be returned to non-admin callers")
			stat := models[0].(map[string]any)
			require.Equal(t, "operational", stat["latest_status"])
			require.EqualValues(t, 820, stat["latest_latency_ms"])
			require.EqualValues(t, 99.5, stat["availability_7d"])
			require.EqualValues(t, 99.5, stat["availability_15d"])
			require.EqualValues(t, 99.5, stat["availability_30d"])
			require.EqualValues(t, 900, stat["avg_latency_7d_ms"])
		})
	}
}

func TestChannelMonitorUserGetStatus_AdminKeepsAllModels(t *testing.T) {
	data, _ := callMonitorHandler(t, service.RoleAdmin, "7")

	require.Equal(t, testMonitorProvider, data["provider"])
	models := data["models"].([]any)
	require.Len(t, models, 2)
	require.Equal(t, testMonitorPrimary, models[0].(map[string]any)["model"])
	require.Equal(t, testMonitorExtraModel, models[1].(map[string]any)["model"])
}

func TestUserMonitorDetailToPublicResponse_EmptyModels(t *testing.T) {
	out := userMonitorDetailToPublicResponse(&service.UserMonitorDetail{ID: 1, Name: "n"})
	require.NotNil(t, out.Models)
	require.Empty(t, out.Models)
}
