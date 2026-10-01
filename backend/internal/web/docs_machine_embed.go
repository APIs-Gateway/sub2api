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

// shouldServeMachineDoc 判断这次请求是否由 serveMachineDoc 处理：路径是机器文件，
// 且构建产物里确实有这个文件。方法不对的请求也交给它，由它回 405，免得把带占位符的模板原样发出去。
func (s *FrontendServer) shouldServeMachineDoc(cleanPath string) bool {
	return isMachineDocPath(cleanPath) && s.fileExists(cleanPath)
}

// SetTrustedProxies 设置可信代理（server.trusted_proxies）。只有来自这些地址的请求，
// 它带的 X-Forwarded-Proto 才会被用来决定机器文件里链接的协议。启动时调用一次。
func (s *FrontendServer) SetTrustedProxies(entries []string) {
	if s != nil {
		s.trustedProxies = parseMachineTrustedProxies(entries)
	}
}

// serveMachineDoc 读出构建产物里的模板，按公开设置替换占位符后返回。
func (s *FrontendServer) serveMachineDoc(c *gin.Context, cleanPath string) {
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Header("Allow", "GET, HEAD")
		c.Status(http.StatusMethodNotAllowed)
		c.Abort()
		return
	}
	template, err := fs.ReadFile(s.distFS, cleanPath)
	if err != nil {
		c.Status(http.StatusNotFound)
		c.Abort()
		return
	}

	cfg := s.machineDocSettings(c.Request.Context())
	trustProto := machineProxyTrusted(s.trustedProxies, c.RemoteIP())
	origin := machineOrigin(c.Request.Host, c.Request.TLS != nil, c.GetHeader("X-Forwarded-Proto"), trustProto, cfg.APIBaseURL)
	if origin == "" {
		origin = normalizeAPIBase(sanitizeEndpointURL(cfg.APIBaseURL), "")
	}
	body := renderMachineDoc(string(template), cfg, origin, c.Query(machineEndpointParam))

	contentType := "text/plain; charset=utf-8"
	if strings.HasSuffix(cleanPath, ".md") {
		contentType = "text/markdown; charset=utf-8"
	}
	// 内容随 Host 变，响应不该被共享缓存存下来再发给别的访客。
	c.Header("Cache-Control", "private, no-store")
	c.Header("Vary", "Host, X-Forwarded-Proto")
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
