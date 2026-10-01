//go:build unit

package web

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsMachineDocPath(t *testing.T) {
	for _, path := range []string{"llms.txt", "llms-full.txt", "docs/codex.md", "docs/claude-code.md", "docs/a_b-1.md"} {
		assert.True(t, isMachineDocPath(path), path)
	}
	for _, path := range []string{
		"", "index.html", "docs", "docs/", "docs/.md", "docs/codex", "docs/codex.txt",
		"docs/a/b.md", "docs/../etc.md", "docs/co dex.md", "llms.txt.map", "assets/llms.txt", "docs/codex.md/",
	} {
		assert.False(t, isMachineDocPath(path), path)
	}
}

func TestParseMachineSettings(t *testing.T) {
	t.Run("reads site name, api_base_url and custom endpoints", func(t *testing.T) {
		cfg := parseMachineSettings(map[string]any{
			"site_name":        "Hiyo",
			"api_base_url":     "https://api.hiyo.test",
			"custom_endpoints": []map[string]string{{"name": "CDN", "endpoint": "https://cdn.hiyo.test", "description": "全球"}},
		})
		assert.Equal(t, "Hiyo", cfg.SiteName)
		assert.Equal(t, "https://api.hiyo.test", cfg.APIBaseURL)
		assert.Equal(t, []machineCustomEndpoint{{Name: "CDN", Endpoint: "https://cdn.hiyo.test", Description: "全球"}}, cfg.CustomEndpoints)
	})

	t.Run("accepts raw JSON like the real settings payload", func(t *testing.T) {
		cfg := parseMachineSettings(struct {
			SiteName        string          `json:"site_name"`
			CustomEndpoints json.RawMessage `json:"custom_endpoints"`
		}{SiteName: "Hiyo", CustomEndpoints: json.RawMessage(`[{"name":"a","endpoint":"https://a.test","description":""}]`)})
		assert.Equal(t, "Hiyo", cfg.SiteName)
		require.Len(t, cfg.CustomEndpoints, 1)
	})

	t.Run("a malformed endpoint list only drops the endpoints", func(t *testing.T) {
		cfg := parseMachineSettings(map[string]any{"site_name": "Hiyo", "custom_endpoints": "oops"})
		assert.Equal(t, "Hiyo", cfg.SiteName)
		assert.Empty(t, cfg.CustomEndpoints)
	})

	t.Run("nil settings give empty settings", func(t *testing.T) {
		assert.Equal(t, machineSettings{}, parseMachineSettings(nil))
	})
}

func TestMachineOrigin(t *testing.T) {
	t.Run("a TLS connection is https", func(t *testing.T) {
		assert.Equal(t, "https://docs.test", machineOrigin("docs.test", true, "", false, ""))
		assert.Equal(t, "https://docs.test", machineOrigin("docs.test", true, "http", true, ""))
	})

	t.Run("X-Forwarded-Proto counts only from a trusted proxy", func(t *testing.T) {
		assert.Equal(t, "http://docs.test", machineOrigin("docs.test", false, "http", true, ""))
		assert.Equal(t, "https://docs.test", machineOrigin("docs.test", false, "https, http", true, ""))
		assert.Equal(t, "https://docs.test", machineOrigin("docs.test", false, "ftp", true, ""), "unknown proto is ignored")
		assert.Equal(t, "https://docs.test", machineOrigin("docs.test", false, "http", false, ""), "an untrusted client cannot downgrade the links")
	})

	t.Run("without TLS or a trusted header the scheme follows api_base_url on the same host", func(t *testing.T) {
		assert.Equal(t, "http://docs.test", machineOrigin("docs.test", false, "", false, "http://DOCS.test/v1"))
		assert.Equal(t, "https://docs.test", machineOrigin("docs.test", false, "", false, "https://docs.test"))
		assert.Equal(t, "https://docs.test", machineOrigin("docs.test", false, "", false, "http://api.test"), "another host does not decide the scheme")
	})

	t.Run("otherwise local hosts and IPs are http, domains are https", func(t *testing.T) {
		for _, host := range []string{"localhost", "localhost:5173", "app.localhost", "127.0.0.1:8080", "10.0.0.2", "[::1]:8080"} {
			assert.Equal(t, "http://"+host, machineOrigin(host, false, "", false, ""), host)
		}
		assert.Equal(t, "https://hiyo.test", machineOrigin("hiyo.test", false, "", false, ""))
		assert.Equal(t, "https://hiyo.test:8443", machineOrigin("hiyo.test:8443", false, "", false, ""))
	})

	t.Run("a Host with characters that do not belong in one gives no origin", func(t *testing.T) {
		for _, host := range []string{"", "  ", "evil.test/x", "a b", "a.test\r\nX: y", "a.test?x=1", "a@b.test"} {
			assert.Equal(t, "", machineOrigin(host, false, "https", true, ""), host)
		}
	})
}

func TestMachineTrustedProxies(t *testing.T) {
	nets := parseMachineTrustedProxies([]string{"10.0.0.0/8", " 192.168.1.5 ", "::1", "", "not-an-ip"})
	require.Len(t, nets, 3)
	for _, ip := range []string{"10.1.2.3", "192.168.1.5", "::1"} {
		assert.True(t, machineProxyTrusted(nets, ip), ip)
	}
	for _, ip := range []string{"8.8.8.8", "192.168.1.6", "", "garbage"} {
		assert.False(t, machineProxyTrusted(nets, ip), ip)
	}
	assert.False(t, machineProxyTrusted(nil, "10.1.2.3"), "no trusted proxies configured trusts nobody")
	assert.Equal(t, []*net.IPNet(nil), parseMachineTrustedProxies(nil))
}

func TestCanonicalizeEndpointURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://x.com:443":          "https://x.com/",
		"http://x.com:80/v1":         "http://x.com/v1",
		"http://x.com:8080":          "http://x.com:8080/",
		"HTTPS://X.COM/A/../b":       "https://x.com/b",
		"https://x.com/a/./b/.":      "https://x.com/a/b/",
		"https://x.com/a/%2e%2e/b":   "https://x.com/b",
		"https://例え.test":            "https://xn--r8jz45g.test/",
		"https://x.com?a=1":          "https://x.com/?a=1",
		"http:///x.com/y":            "http://x.com/y",
		`https://good.com\@evil.com`: "https://good.com/@evil.com",
		"  https://x.com/\t\n  ":     "https://x.com/",
		"https://[::1]:8080/v1":      "https://[::1]:8080/v1",
		"https://u:p@x.com/":         "https://u:p@x.com/",
		"https://x.com/#frag":        "https://x.com/#frag",
	} {
		got, ok := canonicalizeEndpointURL(raw)
		assert.True(t, ok, raw)
		assert.Equal(t, want, got.String, raw)
	}
	for _, raw := range []string{
		"", "x.com", "ftp://x.com", "javascript:alert(1)", "//x.com", "https://", "https:///", "https://x.com:99999",
		"https://x.com:abc", "https://a b.test", "https://x.com/\x01", "https://a.test ;touch",
	} {
		_, ok := canonicalizeEndpointURL(raw)
		assert.False(t, ok, raw)
	}
}

func TestNormalizeAPIBase(t *testing.T) {
	assert.Equal(t, "https://a.test", normalizeAPIBase("https://a.test/", ""))
	assert.Equal(t, "https://a.test", normalizeAPIBase("https://a.test/v1", ""))
	assert.Equal(t, "https://a.test", normalizeAPIBase(" https://a.test/v1/ ", ""))
	assert.Equal(t, "https://site.test", normalizeAPIBase("", "https://site.test"))
	assert.Equal(t, "https://site.test", normalizeAPIBase("  ", "https://site.test/"))
}

func TestResolveMachineEndpoints(t *testing.T) {
	t.Run("default first, custom normalized, junk and repeats dropped", func(t *testing.T) {
		options := resolveMachineEndpoints(machineSettings{
			APIBaseURL: "https://api.first.test/v1/",
			CustomEndpoints: []machineCustomEndpoint{
				{Name: " CDN 加速域名 ", Endpoint: "https://Fast.Second.test/v1/", Description: " 全球支持 "},
				{Name: "重复", Endpoint: "https://api.first.test"},
				{Name: "不是地址", Endpoint: "javascript:alert(1)"},
				{Name: "ftp", Endpoint: "ftp://x.test"},
				{Name: "空", Endpoint: "  "},
				{Name: " ", Endpoint: "https://one.second.test"},
				{Name: "又重复", Endpoint: "https://one.second.test/v1"},
			},
		}, "https://site.test")

		assert.Equal(t, []machineEndpoint{
			{Base: "https://api.first.test", V1: "https://api.first.test/v1", IsDefault: true},
			{Name: "CDN 加速域名", Description: "全球支持", Base: "https://fast.second.test", V1: "https://fast.second.test/v1"},
			{Name: "one.second.test", Base: "https://one.second.test", V1: "https://one.second.test/v1"},
		}, options)
	})

	t.Run("an empty api_base_url falls back to the request origin", func(t *testing.T) {
		options := resolveMachineEndpoints(machineSettings{}, "https://site.test")
		assert.Equal(t, []machineEndpoint{{Base: "https://site.test", V1: "https://site.test/v1", IsDefault: true}}, options)
	})
}

func TestMachineCodexProvider(t *testing.T) {
	assert.Equal(t, "hiyo", machineCodexProviderID("Hiyo"))
	// 只保留 [a-z0-9_]，其余字符直接丢掉，不换成下划线。规则和前端 docsRender.ts 的 codexProviderId 一致，
	// 同一站点名在文档页和 llms.txt 里给出的 Codex provider id 必须相同（共享用例里也有）。
	assert.Equal(t, "mysite2", machineCodexProviderID("My Site-2!"))
	assert.Equal(t, "my_site2", machineCodexProviderID("my_site-2"))
	assert.Equal(t, "sub2api", machineCodexProviderID("站点"))
	for _, reserved := range []string{"openai", "OpenAI", "ollama", "lmstudio"} {
		assert.Equal(t, strings.ToLower(reserved)+"_site", machineCodexProviderID(reserved))
	}
	assert.Equal(t, `A \"B\" \\ C D`, machineCodexProviderName("A \"B\" \\ C\nD"))
	assert.Equal(t, "sub2api", machineCodexProviderName(" \n "))
}

func TestMachineEndpointQuery(t *testing.T) {
	assert.Equal(t, "?endpoint=https://cdn.second.test", machineEndpointQuery("https://cdn.second.test"))
	assert.Equal(t, "?endpoint=http://h.test:8080/a%3Fb%26c", machineEndpointQuery("http://h.test:8080/a?b&c"))
	// encodeURIComponent 不转义 ! * ' ( )，前端生成的链接里它们是原样的。
	assert.Equal(t, "?endpoint=https://h.test/a(b)!*'", machineEndpointQuery("https://h.test/a(b)!*'"))
}

// machineRenderCase 是前后端共用的占位符替换用例，前端 docsMachine.spec.ts 读同一份文件。
type machineRenderCase struct {
	Name     string `json:"name"`
	Settings struct {
		SiteName        string                  `json:"site_name"`
		APIBaseURL      string                  `json:"api_base_url"`
		CustomEndpoints []machineCustomEndpoint `json:"custom_endpoints"`
	} `json:"settings"`
	Origin    string `json:"origin"`
	Requested string `json:"requested"`
	Template  string `json:"template"`
	Expected  string `json:"expected"`
}

func TestRenderMachineDocSharedCases(t *testing.T) {
	path := filepath.Join("..", "..", "..", "frontend", "src", "views", "docs", "__tests__", "fixtures", "machine-render-cases.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var file struct {
		Cases []machineRenderCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &file))
	require.Greater(t, len(file.Cases), 20)

	for _, item := range file.Cases {
		t.Run(item.Name, func(t *testing.T) {
			cfg := machineSettings{
				SiteName:        item.Settings.SiteName,
				APIBaseURL:      item.Settings.APIBaseURL,
				CustomEndpoints: item.Settings.CustomEndpoints,
			}
			assert.Equal(t, item.Expected, renderMachineDoc(item.Template, cfg, item.Origin, item.Requested))
		})
	}
}
