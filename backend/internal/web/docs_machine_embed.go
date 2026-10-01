//go:build embed

package web

import (
	"context"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// machineSettingsTTL 是公开设置在内存里缓存的时间。设置改动时 InvalidateCache 会立即清掉。
const machineSettingsTTL = time.Minute

// shouldServeMachineDoc 判断这次请求是否由 serveMachineDoc 处理：GET/HEAD，
// 路径是机器文件，且构建产物里确实有这个文件。
func (s *FrontendServer) shouldServeMachineDoc(c *gin.Context, cleanPath string) bool {
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		return false
	}
	return isMachineDocPath(cleanPath) && s.fileExists(cleanPath)
}

// serveMachineDoc 读出构建产物里的模板，按公开设置替换占位符后返回。
func (s *FrontendServer) serveMachineDoc(c *gin.Context, cleanPath string) {
	template, err := fs.ReadFile(s.distFS, cleanPath)
	if err != nil {
		c.Status(http.StatusNotFound)
		c.Abort()
		return
	}

	cfg := s.machineDocSettings(c.Request.Context())
	origin := machineOrigin(c.GetHeader("X-Forwarded-Proto"), c.Request.TLS != nil, c.Request.Host)
	if origin == "" {
		origin = normalizeAPIBase(cfg.APIBaseURL, "")
	}
	body := renderMachineDoc(string(template), cfg, origin, c.Query(machineEndpointParam))

	contentType := "text/plain; charset=utf-8"
	if strings.HasSuffix(cleanPath, ".md") {
		contentType = "text/markdown; charset=utf-8"
	}
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, contentType, []byte(body))
	c.Abort()
}

// machineDocSettings 读公开设置，带一分钟缓存。读取失败时返回空设置，
// 文件照样能出：地址退回当前站点，站点名退回默认名。失败不缓存。
func (s *FrontendServer) machineDocSettings(ctx context.Context) machineSettings {
	s.machineMu.Lock()
	defer s.machineMu.Unlock()

	if s.machineCfg != nil && time.Since(s.machineAt) < machineSettingsTTL {
		return *s.machineCfg
	}
	if s.settings == nil {
		return machineSettings{}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := s.settings.GetPublicSettingsForInjection(ctx)
	if err != nil {
		return machineSettings{}
	}
	cfg := parseMachineSettings(raw)
	s.machineCfg = &cfg
	s.machineAt = time.Now()
	return cfg
}
