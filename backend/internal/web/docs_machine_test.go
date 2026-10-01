//go:build unit

package web

import (
	"encoding/json"
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
	assert.Equal(t, "https://docs.test", machineOrigin("https", false, "docs.test"))
	assert.Equal(t, "https://docs.test", machineOrigin("", true, "docs.test"))
	assert.Equal(t, "http://localhost:5173", machineOrigin("", false, "localhost:5173"))
	assert.Equal(t, "https://docs.test", machineOrigin("https, http", false, "docs.test"))
	assert.Equal(t, "http://docs.test", machineOrigin("ftp", false, "docs.test"), "unknown proto is ignored")
	assert.Equal(t, "https://[::1]:8080", machineOrigin("https", false, "[::1]:8080"))
	for _, host := range []string{"", "  ", "evil.test/x", "a b", "a.test\r\nX: y", "a.test?x=1", "a@b.test"} {
		assert.Equal(t, "", machineOrigin("https", false, host), host)
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
	assert.Equal(t, "my_site2", machineCodexProviderID("My Site-2!"))
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
}

func TestRenderMachineDoc(t *testing.T) {
	cfg := machineSettings{
		SiteName:   "Hiyo",
		APIBaseURL: "https://api.first.test",
		CustomEndpoints: []machineCustomEndpoint{
			{Name: "CDN 加速域名", Endpoint: "https://cdn.second.test", Description: "全球支持"},
		},
	}
	const origin = "https://hiyo.test"

	t.Run("fills every placeholder with the default address", func(t *testing.T) {
		template := "{{site}}|{{base}}|{{v1}}|{{origin}}|{{llms}}|{{provider}}|{{providerName}}|{{q}}|"
		assert.Equal(t,
			"Hiyo|https://api.first.test|https://api.first.test/v1|https://hiyo.test|https://hiyo.test/llms.txt|hiyo|Hiyo||",
			renderMachineDoc(template, cfg, origin, ""))
	})

	t.Run("a requested custom endpoint replaces the address and is carried in links", func(t *testing.T) {
		got := renderMachineDoc("{{v1}} {{origin}}/docs/codex.md{{q}}", cfg, origin, "https://cdn.second.test")
		assert.Equal(t, "https://cdn.second.test/v1 https://hiyo.test/docs/codex.md?endpoint=https://cdn.second.test", got)
	})

	t.Run("an endpoint that is not in the settings is ignored", func(t *testing.T) {
		for _, requested := range []string{"https://evil.test", "https://api.first.test", "cdn.second.test", "https://cdn.second.test/v1"} {
			got := renderMachineDoc("{{base}}{{q}}", cfg, origin, requested)
			assert.Equal(t, "https://api.first.test", got, requested)
		}
	})

	t.Run("lists the custom endpoints once in the endpoints section", func(t *testing.T) {
		got := renderMachineDoc("A\n\n{{endpoints}}B", cfg, origin, "")
		assert.Equal(t, "A\n\n## 备用地址\n\n管理员还提供了下面的备用地址，访问慢时可以换用，密钥通用。用户选用备用地址时，把文档里的接入地址换成它（OpenAI 兼容客户端再加 /v1）：\n\n- CDN 加速域名：`https://cdn.second.test`，全球支持\n\nB", got)
	})

	t.Run("the endpoints section is empty without custom endpoints", func(t *testing.T) {
		got := renderMachineDoc("A\n\n{{endpoints}}B", machineSettings{SiteName: "Hiyo"}, origin, "")
		assert.Equal(t, "A\n\nB", got)
	})

	t.Run("falls back to the request origin and the default site name", func(t *testing.T) {
		got := renderMachineDoc("{{site}} {{base}} {{provider}}", machineSettings{}, origin, "")
		assert.Equal(t, "Sub2API https://hiyo.test sub2api", got)
	})

	t.Run("a site name with line breaks and quotes cannot break the Codex config", func(t *testing.T) {
		got := renderMachineDoc(`name = "{{providerName}}" [model_providers.{{provider}}]`, machineSettings{SiteName: "A\"B\nC"}, origin, "")
		assert.Equal(t, `name = "A\"B C" [model_providers.abc]`, got)
	})

	t.Run("substituted values are not scanned again", func(t *testing.T) {
		got := renderMachineDoc("{{site}}", machineSettings{SiteName: "{{base}}"}, origin, "")
		assert.Equal(t, "{{base}}", got)
	})

	t.Run("leaves unknown placeholders alone", func(t *testing.T) {
		assert.Equal(t, "{{nope}}", renderMachineDoc("{{nope}}", cfg, origin, ""))
	})
}
