package repository

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestBuildOpsErrorLogsWhere_QueryUsesQualifiedColumns(t *testing.T) {
	filter := &service.OpsErrorLogFilter{
		Query: "ACCESS_DENIED",
	}

	where, args := buildOpsErrorLogsWhere(filter)
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 1 {
		t.Fatalf("args len = %d, want 1", len(args))
	}
	if !strings.Contains(where, "e.request_id ILIKE $") {
		t.Fatalf("where should include qualified request_id condition: %s", where)
	}
	if !strings.Contains(where, "e.client_request_id ILIKE $") {
		t.Fatalf("where should include qualified client_request_id condition: %s", where)
	}
	if !strings.Contains(where, "e.error_message ILIKE $") {
		t.Fatalf("where should include qualified error_message condition: %s", where)
	}
}

func TestBuildOpsErrorLogsWhere_UserQueryUsesExistsSubquery(t *testing.T) {
	filter := &service.OpsErrorLogFilter{
		UserQuery: "admin@",
	}

	where, args := buildOpsErrorLogsWhere(filter)
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 1 {
		t.Fatalf("args len = %d, want 1", len(args))
	}
	if !strings.Contains(where, "EXISTS (SELECT 1 FROM users u WHERE u.id = e.user_id AND u.email ILIKE $") {
		t.Fatalf("where should include EXISTS user email condition: %s", where)
	}
}

// TestBuildOpsErrorLogsWhere_ViewFilter 覆盖 issue #16 Part A 的列表视图口径:
// errors 视图须与 SLA 同义(排除 error_owner='client');新增 client / 保留 excluded / all;
// 并验证「view=errors + 显式 owner 过滤」不再叠加 client 排除(否则 owner<>'client' AND owner=$N 恒空)。
func TestBuildOpsErrorLogsWhere_ViewFilter(t *testing.T) {
	const (
		biz0      = "COALESCE(e.is_business_limited,false) = false"
		biz1      = "COALESCE(e.is_business_limited,false) = true"
		ownerNotC = "LOWER(COALESCE(e.error_owner,'')) <> 'client'"
		ownerEqC  = "LOWER(COALESCE(e.error_owner,'')) = 'client'"
	)
	tests := []struct {
		name           string
		view           string
		owner          string
		mustContain    []string
		mustNotContain []string
	}{
		{
			name:           "errors 默认排除 client(与 SLA 同义)",
			view:           "errors",
			mustContain:    []string{biz0, ownerNotC},
			mustNotContain: []string{biz1, ownerEqC},
		},
		{
			name:           "空视图等同 errors",
			view:           "",
			mustContain:    []string{biz0, ownerNotC},
			mustNotContain: nil,
		},
		{
			name:           "未知视图回落 errors",
			view:           "garbage",
			mustContain:    []string{biz0, ownerNotC},
			mustNotContain: nil,
		},
		{
			name:           "client 视图仅看客户端归因",
			view:           "client",
			mustContain:    []string{ownerEqC},
			mustNotContain: []string{biz0, biz1, ownerNotC},
		},
		{
			name:           "excluded 视图仅看业务限制",
			view:           "excluded",
			mustContain:    []string{biz1},
			mustNotContain: []string{ownerNotC, ownerEqC},
		},
		{
			name:           "all 视图不加业务限制/owner 视图约束",
			view:           "all",
			mustNotContain: []string{biz0, biz1, ownerNotC},
		},
		{
			// footgun:显式 owner 过滤时,errors 视图不得再叠加 <> 'client',否则与 owner=$N 矛盾恒空。
			name:           "errors + 显式 owner 过滤抑制 client 排除",
			view:           "errors",
			owner:          "client",
			mustContain:    []string{biz0, "LOWER(COALESCE(e.error_owner,'')) = $"},
			mustNotContain: []string{ownerNotC},
		},
		{
			name:           "errors + owner=provider 仍正常过滤",
			view:           "errors",
			owner:          "provider",
			mustContain:    []string{biz0, "LOWER(COALESCE(e.error_owner,'')) = $"},
			mustNotContain: []string{ownerNotC},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, _ := buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{
				View:  tt.view,
				Owner: tt.owner,
			})
			for _, frag := range tt.mustContain {
				if !strings.Contains(where, frag) {
					t.Errorf("where 应包含 %q\n实际: %s", frag, where)
				}
			}
			for _, frag := range tt.mustNotContain {
				if strings.Contains(where, frag) {
					t.Errorf("where 不应包含 %q\n实际: %s", frag, where)
				}
			}
		})
	}
}
