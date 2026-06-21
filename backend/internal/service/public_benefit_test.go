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
