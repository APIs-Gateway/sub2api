package emailcanon

import "testing"

func TestCanonicalizeEmail_Enabled(t *testing.T) {
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(true) })

	cases := []struct{ in, want string }{
		// Gmail：去点、去 +tag、googlemail→gmail
		{"foo@gmail.com", "foo@gmail.com"},
		{"f.o.o@gmail.com", "foo@gmail.com"},
		{"foo+tag@gmail.com", "foo@gmail.com"},
		{"f.o.o+spam@gmail.com", "foo@gmail.com"},
		{"  F.O.O+Tag@Gmail.Com  ", "foo@gmail.com"}, // 大小写+空格
		{"foo@googlemail.com", "foo@gmail.com"},
		{"f.o.o+x@googlemail.com", "foo@gmail.com"},
		{"first.last@gmail.com", "firstlast@gmail.com"},
		// 非 Gmail：只 lower+trim，保留点与 +
		{"a.b+c@outlook.com", "a.b+c@outlook.com"},
		{"A.B@Example.COM", "a.b@example.com"},
		// 退化/异常：原样（lower+trim）
		{"+tag@gmail.com", "+tag@gmail.com"}, // 本地部分清空后退化
		{"...@gmail.com", "...@gmail.com"},   // 全点退化
		{"not-an-email", "not-an-email"},     // 无 @
		{"trailing@", "trailing@"},           // 无域名
		{"@gmail.com", "@gmail.com"},         // 无本地
		{"", ""},
	}
	for _, c := range cases {
		if got := CanonicalizeEmail(c.in); got != c.want {
			t.Errorf("CanonicalizeEmail(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestCanonicalizeEmail_Disabled(t *testing.T) {
	SetEnabled(false)
	t.Cleanup(func() { SetEnabled(true) })

	// 关闭时退化为 ToLower+TrimSpace，与历史行为一致（不动点/+，gmail 不归一化）
	cases := []struct{ in, want string }{
		{"  F.O.O+Tag@Gmail.Com  ", "f.o.o+tag@gmail.com"},
		{"foo@googlemail.com", "foo@googlemail.com"},
		{"a.b+c@outlook.com", "a.b+c@outlook.com"},
	}
	for _, c := range cases {
		if got := CanonicalizeEmail(c.in); got != c.want {
			t.Errorf("disabled CanonicalizeEmail(%q)=%q want %q", c.in, got, c.want)
		}
	}
	if !Enabled() {
		// SetEnabled(false) 后 Enabled() 应为 false
	} else {
		t.Fatal("Enabled() should be false after SetEnabled(false)")
	}
}

func TestCanonicalizeEmailForStorage(t *testing.T) {
	t.Cleanup(func() { SetEnabled(true) })

	SetEnabled(true)
	storageCases := []struct{ in, want string }{
		// Gmail：归一化为规范地址
		{"F.O.O+tag@Gmail.com", "foo@gmail.com"},
		{"foo@googlemail.com", "foo@gmail.com"},
		// 非 Gmail：原样落库（保留大小写/空格，去重交给查询端 LOWER(TRIM)）
		{" Legacy@Example.com ", " Legacy@Example.com "},
		{"A.B+c@outlook.com", "A.B+c@outlook.com"},
		// 退化 gmail：原样
		{"+tag@gmail.com", "+tag@gmail.com"},
	}
	for _, c := range storageCases {
		if got := CanonicalizeEmailForStorage(c.in); got != c.want {
			t.Errorf("enabled CanonicalizeEmailForStorage(%q)=%q want %q", c.in, got, c.want)
		}
	}

	// 关闭：一律原样落库（与历史行为一致）
	SetEnabled(false)
	for _, in := range []string{"F.O.O+tag@Gmail.com", " Legacy@Example.com "} {
		if got := CanonicalizeEmailForStorage(in); got != in {
			t.Errorf("disabled CanonicalizeEmailForStorage(%q)=%q want unchanged", in, got)
		}
	}
}
