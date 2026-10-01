//go:build embed || unit

package web

import (
	"encoding/json"
	"net/url"
	"strings"
)

// 给 AI 读的文档文件（/llms.txt、/llms-full.txt、/docs/<章节 id>.md）。
//
// 前端构建时把它们作为「带占位符的模板」写进 dist（frontend/vite-plugins/docsMachineFiles.ts，
// 内容由 frontend/src/views/docs/docsMachine.ts 按 sections.ts 生成），静态文件里写不死域名。
// 这里在响应时按公开设置替换占位符。替换规则要和 docsMachine.ts 的 fillMachineText 保持一致，
// 两边改占位符或备用地址段落时一起改。
//
// 占位符：{{base}} {{v1}} {{site}} {{origin}} {{llms}} {{provider}} {{providerName}}，
// 以及 {{endpoints}}（备用地址段落）和 {{q}}（?endpoint=… 查询串）。{{model}} 在构建时已替换。

const (
	// machineEndpointParam 是选用备用地址的查询参数，值必须是站点设置里某个地址的 API 根地址。
	machineEndpointParam = "endpoint"
	// machineDefaultSiteName 是站点名为空时的名字，和前端一致。
	machineDefaultSiteName = "Sub2API"
)

type machineCustomEndpoint struct {
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Description string `json:"description"`
}

// machineSettings 是模板替换用到的公开设置。
type machineSettings struct {
	SiteName        string
	APIBaseURL      string
	CustomEndpoints []machineCustomEndpoint
}

// machineEndpoint 是一个可用的接入地址。第一个永远是默认地址（api_base_url）。
type machineEndpoint struct {
	Name        string
	Description string
	Base        string // API 根地址，不带 /v1
	V1          string // 带 /v1
	IsDefault   bool
}

// parseMachineSettings 从公开设置里取出站点名、api_base_url 和自定义端点。
// 任何一项解析失败都只丢掉那一项，不影响其他项。
func parseMachineSettings(settings any) machineSettings {
	raw, err := json.Marshal(settings)
	if err != nil {
		return machineSettings{}
	}
	var head struct {
		SiteName        string          `json:"site_name"`
		APIBaseURL      string          `json:"api_base_url"`
		CustomEndpoints json.RawMessage `json:"custom_endpoints"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return machineSettings{}
	}
	cfg := machineSettings{SiteName: head.SiteName, APIBaseURL: head.APIBaseURL}
	if len(head.CustomEndpoints) > 0 {
		var items []machineCustomEndpoint
		if err := json.Unmarshal(head.CustomEndpoints, &items); err == nil {
			cfg.CustomEndpoints = items
		}
	}
	return cfg
}

// isMachineDocPath 判断（不带开头斜杠的）路径是不是需要替换占位符的机器文件。
func isMachineDocPath(cleanPath string) bool {
	if cleanPath == "llms.txt" || cleanPath == "llms-full.txt" {
		return true
	}
	name, ok := strings.CutPrefix(cleanPath, "docs/")
	if !ok {
		return false
	}
	id, ok := strings.CutSuffix(name, ".md")
	if !ok || id == "" {
		return false
	}
	for _, r := range id {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		if !isLetter && !isDigit && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// machineOrigin 由请求推出站点来源（如 https://example.com）。
// 只用 Host，不用 X-Forwarded-Host：响应可能被中间的缓存存下来，不能让请求头改写别人看到的域名。
// Host 里有不该出现的字符时返回空串，调用方改用 api_base_url。
func machineOrigin(forwardedProto string, hasTLS bool, host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	for _, r := range host {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		if !isLetter && !isDigit && !strings.ContainsRune(".-:[]", r) {
			return ""
		}
	}
	scheme := "http"
	if hasTLS {
		scheme = "https"
	}
	first, _, _ := strings.Cut(forwardedProto, ",")
	if proto := strings.ToLower(strings.TrimSpace(first)); proto == "http" || proto == "https" {
		scheme = proto
	}
	return scheme + "://" + host
}

// normalizeAPIBase 去掉结尾的斜杠和 /v1，留空时用 fallback。和前端 resolveApiBases 一致。
func normalizeAPIBase(raw, fallback string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = fallback
	}
	value = strings.TrimRight(value, "/")
	return strings.TrimSuffix(value, "/v1")
}

// sanitizeEndpointURL 只接受 http(s) 绝对地址，其余返回空串。
func sanitizeEndpointURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String()
}

// resolveMachineEndpoints 返回默认地址加上管理员配置的备用地址。
// 不是 http(s) 地址的、和前面重复的备用地址会被丢掉。规则和前端 resolveEndpointOptions 一致。
func resolveMachineEndpoints(cfg machineSettings, origin string) []machineEndpoint {
	base := normalizeAPIBase(cfg.APIBaseURL, origin)
	options := []machineEndpoint{{Base: base, V1: base + "/v1", IsDefault: true}}
	seen := map[string]bool{base: true}
	for _, item := range cfg.CustomEndpoints {
		endpointURL := sanitizeEndpointURL(item.Endpoint)
		if endpointURL == "" {
			continue
		}
		endpointBase := normalizeAPIBase(endpointURL, origin)
		if seen[endpointBase] {
			continue
		}
		seen[endpointBase] = true
		name := strings.TrimSpace(item.Name)
		if name == "" {
			if parsed, err := url.Parse(endpointURL); err == nil {
				name = parsed.Host
			}
		}
		options = append(options, machineEndpoint{
			Name:        name,
			Description: strings.TrimSpace(item.Description),
			Base:        endpointBase,
			V1:          endpointBase + "/v1",
		})
	}
	return options
}

// machineOneLine 把任意空白（含换行）压成单个空格并去掉首尾空白。
func machineOneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// machineEndpointsSection 是 llms.txt 里的「备用地址」段落。没有备用地址时是空串。
func machineEndpointsSection(options []machineEndpoint) string {
	var items []string
	for _, option := range options {
		if option.IsDefault {
			continue
		}
		line := "- " + machineOneLine(option.Name) + "：`" + option.Base + "`"
		if desc := machineOneLine(option.Description); desc != "" {
			line += "，" + desc
		}
		items = append(items, line)
	}
	if len(items) == 0 {
		return ""
	}
	return "## 备用地址\n\n" +
		"管理员还提供了下面的备用地址，访问慢时可以换用，密钥通用。用户选用备用地址时，把文档里的接入地址换成它（OpenAI 兼容客户端再加 /v1）：\n\n" +
		strings.Join(items, "\n") + "\n\n"
}

// machineCodexProviderID 由站点名生成 Codex 的 provider id：只含小写字母、数字、下划线，
// 不与内置 provider 重名。和前端 codexProviderId 一致。
func machineCodexProviderID(siteName string) string {
	var id strings.Builder
	for _, r := range strings.ToLower(siteName) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			id.WriteRune(r)
		}
	}
	result := id.String()
	if result == "" {
		result = "sub2api"
	}
	switch result {
	case "openai", "ollama", "lmstudio":
		return result + "_site"
	}
	return result
}

// machineCodexProviderName 是写进 TOML 双引号字符串的站点名：控制字符换成空格，转义反斜杠和双引号。
// 和前端 codexProviderName 一致。
func machineCodexProviderName(siteName string) string {
	var cleaned strings.Builder
	inControl := false
	for _, r := range siteName {
		if r <= 0x1f || r == 0x7f {
			if !inControl {
				cleaned.WriteRune(' ')
			}
			inControl = true
			continue
		}
		inControl = false
		cleaned.WriteRune(r)
	}
	name := strings.TrimSpace(cleaned.String())
	if name == "" {
		name = "sub2api"
	}
	name = strings.ReplaceAll(name, `\`, `\\`)
	return strings.ReplaceAll(name, `"`, `\"`)
}

// machineEndpointQuery 生成 ?endpoint=… 查询串。冒号和斜杠不转义，链接读起来像地址；
// 和前端 endpointQuery 同规则。
func machineEndpointQuery(base string) string {
	escaped := url.QueryEscape(base)
	escaped = strings.ReplaceAll(escaped, "%3A", ":")
	escaped = strings.ReplaceAll(escaped, "%2F", "/")
	return "?" + machineEndpointParam + "=" + escaped
}

// renderMachineDoc 替换模板里的占位符。requestedEndpoint 是请求里 ?endpoint= 的值，
// 只有它正好是站点设置里某个备用地址时才生效，其他情况一律用默认地址。
func renderMachineDoc(template string, cfg machineSettings, origin, requestedEndpoint string) string {
	options := resolveMachineEndpoints(cfg, origin)
	chosen := options[0]
	query := ""
	if requestedEndpoint != "" {
		for _, option := range options[1:] {
			if option.Base == requestedEndpoint {
				chosen = option
				query = machineEndpointQuery(option.Base)
				break
			}
		}
	}
	site := machineOneLine(cfg.SiteName)
	if site == "" {
		site = machineDefaultSiteName
	}

	text := strings.ReplaceAll(template, "{{endpoints}}", machineEndpointsSection(options))
	text = strings.ReplaceAll(text, "{{q}}", query)
	return strings.NewReplacer(
		"{{base}}", chosen.Base,
		"{{v1}}", chosen.V1,
		"{{site}}", site,
		"{{llms}}", origin+"/llms.txt",
		"{{origin}}", origin,
		"{{provider}}", machineCodexProviderID(site),
		"{{providerName}}", machineCodexProviderName(site),
	).Replace(text)
}
