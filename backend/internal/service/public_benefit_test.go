package service

import (
	"reflect"
	"testing"
)

func TestParsePublicBenefitKeyNames(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"hvoy,hovy", []string{"hvoy", "hovy"}},
		{" hvoy , HOVY ,hvoy,", []string{"hvoy", "hovy"}}, // trim + 小写 + 去重 + 去空
		{"", []string{}},
		{",,", []string{}},
		{"Foo", []string{"foo"}},
	}
	for _, c := range cases {
		got := parsePublicBenefitKeyNames(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parsePublicBenefitKeyNames(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPublicBenefitMatchesKeyName(t *testing.T) {
	cfg := PublicBenefitConfig{KeyNames: parsePublicBenefitKeyNames(PublicBenefitKeyNamesDefault)}
	match := []string{"hvoy", "hovy", "HVOY", " hvoy ", "Hovy"}
	for _, n := range match {
		if !cfg.matchesKeyName(n) {
			t.Errorf("expected %q to match public-benefit names", n)
		}
	}
	noMatch := []string{"", "hvoyx", "abc", "hvo", "default", "my-key"}
	for _, n := range noMatch {
		if cfg.matchesKeyName(n) {
			t.Errorf("expected %q NOT to match public-benefit names", n)
		}
	}
}

func TestParsePublicBenefitIPWhitelist(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// IPv6 规范化 + 去重（压缩/展开/大小写视为同一 IP）
		{"240d:c000:f06f:ab00:236:8350:42d2:0", []string{"240d:c000:f06f:ab00:236:8350:42d2:0"}},
		{"1.2.3.4, 1.2.3.4 ,5.6.7.8", []string{"1.2.3.4", "5.6.7.8"}},
		{"2001:DB8::1, 2001:db8:0:0:0:0:0:1", []string{"2001:db8::1"}}, // 同一 IPv6 不同写法→去重
		{"", []string{}},
		{" , ,", []string{}},
		{"not-an-ip", []string{"not-an-ip"}}, // 非法 IP 原样保留，回退精确匹配
	}
	for _, c := range cases {
		got := parsePublicBenefitIPWhitelist(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parsePublicBenefitIPWhitelist(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPublicBenefitIPWhitelisted(t *testing.T) {
	cfg := PublicBenefitConfig{IPWhitelist: parsePublicBenefitIPWhitelist("240d:c000:f06f:ab00:236:8350:42d2:0, 1.2.3.4")}
	// 命中（含 IPv6 不同表示）
	for _, ip := range []string{"240d:c000:f06f:ab00:236:8350:42d2:0", "240d:c000:f06f:ab00:0236:8350:42d2:0000", "1.2.3.4"} {
		if !cfg.ipWhitelisted(ip) {
			t.Errorf("expected %q to be whitelisted", ip)
		}
	}
	// 未命中
	for _, ip := range []string{"", "1.2.3.5", "240d:c000:f06f:ab00:236:8350:42d2:1"} {
		if cfg.ipWhitelisted(ip) {
			t.Errorf("expected %q NOT to be whitelisted", ip)
		}
	}
	// 空白名单永不命中
	if (PublicBenefitConfig{}).ipWhitelisted("1.2.3.4") {
		t.Error("empty whitelist must not match")
	}
}

func TestPublicBenefitConfigDefaults(t *testing.T) {
	// 默认应为：开启、$10、含 hvoy/hovy、文案非空。
	if !PublicBenefitIPCapEnabledDefault {
		t.Error("default should be enabled")
	}
	if PublicBenefitIPDailyCapUSDDefault != 10.0 {
		t.Errorf("default cap = %v, want 10", PublicBenefitIPDailyCapUSDDefault)
	}
	names := parsePublicBenefitKeyNames(PublicBenefitKeyNamesDefault)
	if len(names) != 2 {
		t.Errorf("default names = %v, want hvoy+hovy", names)
	}
	if PublicBenefitIPCapMessageDefault == "" {
		t.Error("default message must be non-empty")
	}
}
