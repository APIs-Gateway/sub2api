// Copyright 2023 The Go Authors. All rights reserved.
// Copyright 2026 APIs-Gateway/sub2api contributors.
// Use of this source code is governed by the BSD-style license in
// the top-level LICENSE-GO-BSD file. See BISECT-CONTRACTS.md for provenance.

package bisect_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/imroc/req/v3/internal/bisect"
)

// These are independent truth sets from the documented pattern grammar, not a
// second implementation of its parser or a copy of its mask/condition list.
func TestReqBisectContract_Matcher(t *testing.T) {
	all := func(uint64) bool { return true }
	even := func(id uint64) bool { return id%2 == 0 }
	odd := func(id uint64) bool { return id%2 == 1 }
	union := func(id uint64) bool { return id%4 == 1 || id%4 == 2 }
	minus := func(id uint64) bool { return id%4 != 1 && id%16 != 8 }
	cases := []struct {
		name, pattern string
		selected      func(uint64) bool
		disabled      bool
		quiet         bool
		verbose       bool
	}{
		{"nil", "", all, false, true, false},
		{"all", "y", all, false, false, false},
		{"none", "n", all, true, false, false},
		{"negate_all", "!y", all, true, false, false},
		{"even", "0", even, false, false, false},
		{"odd", "1", odd, false, false, false},
		{"union", "01+10", union, false, false, false},
		{"leading_plus", "+01+10", union, false, false, false},
		{"subtract", "01+10-1001", func(id uint64) bool { return union(id) && id%16 != 9 }, false, false, false},
		{"leading_minus", "-01-1000", minus, false, false, false},
		{"universe_minus", "y-01-1000", minus, false, false, false},
		{"negated_union", "!01+10", union, true, false, false},
		{"double_negation", "!!01", func(id uint64) bool { return id%4 == 1 }, false, false, false},
		{"negated_none", "!n", all, false, false, false},
		{"hex_lower", "xa", func(id uint64) bool { return id%16 == 10 }, false, false, false},
		{"hex_upper", "xA", func(id uint64) bool { return id%16 == 10 }, false, false, false},
		{"mixed_hex_binary", "xA+01", func(id uint64) bool { return id%16 == 10 || id%4 == 1 }, false, false, false},
		{"hex_subtract", "y-xF", func(id uint64) bool { return id%16 != 15 }, false, false, false},
		{"hex_full_width", "x0123456789abcdef", func(id uint64) bool { return id == 0x0123456789abcdef }, false, false, false},
		{"binary_full_width", strings.Repeat("1", 64), func(id uint64) bool { return id == ^uint64(0) }, false, false, false},
		{"quiet_all", "qy", all, false, true, false},
		{"quiet_none", "qn", all, true, true, false},
		{"verbose_all", "vy", all, false, false, true},
		{"repeated_verbose", "vvy", all, false, false, true},
		{"quiet_then_verbose", "qvy", all, false, false, true},
		{"quiet_verbose_none", "qvvn", all, true, false, true},
	}
	ids := make([]uint64, 0, 260)
	for id := uint64(0); id < 256; id++ {
		ids = append(ids, id)
	}
	ids = append(ids, 1<<63, 0x0123456789abcdef, ^uint64(0)-1, ^uint64(0))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := bisect.New(tc.pattern)
			if err != nil {
				t.Fatalf("New(%q): %v", tc.pattern, err)
			}
			if (m == nil) != (tc.pattern == "") {
				t.Fatalf("nil matcher=%v for pattern %q", m == nil, tc.pattern)
			}
			if m != nil && m.MarkerOnly() != !tc.verbose {
				t.Fatalf("MarkerOnly=%v, verbose=%v", m.MarkerOnly(), tc.verbose)
			}
			for _, id := range ids {
				selected := tc.selected(id)
				wantEnabled := selected != tc.disabled
				wantPrinted := selected && !tc.quiet
				if m.ShouldEnable(id) != wantEnabled || m.ShouldPrint(id) != wantPrinted {
					t.Fatalf("pattern=%q id=%016x: enable/print=%v/%v, want %v/%v",
						tc.pattern, id, m.ShouldEnable(id), m.ShouldPrint(id), wantEnabled, wantPrinted)
				}
			}
		})
	}
}

func TestReqBisectContract_InvalidPattern(t *testing.T) {
	cases := []struct {
		name, pattern, diagnostic string
	}{
		{"quiet_only", "q", "invalid pattern syntax: "},
		{"verbose_only", "v", "invalid pattern syntax: "},
		{"negation_only", "!", "invalid pattern syntax: "},
		{"repeated_verbose_only", "vv", "invalid pattern syntax: "},
		{"double_negation_only", "!!", "invalid pattern syntax: "},
		{"quiet_verbose_only", "qv", "invalid pattern syntax: "},
		{"invalid_digit", "2", "invalid pattern syntax: "},
		{"invalid_letter", "z", "invalid pattern syntax: "},
		{"hex_missing", "x", "invalid pattern syntax: "},
		{"hex_invalid", "xg", "invalid pattern syntax: "},
		{"plus_after_minus", "01-10+11", "invalid pattern syntax (+ after -): "},
		{"empty_added_term", "01++10", "invalid pattern syntax: "},
		{"empty_subtracted_term", "01--10", "invalid pattern syntax: "},
		{"trailing_operator", "01+", "invalid pattern syntax: "},
		{"y_followed_bit", "y0", "invalid pattern syntax: "},
		{"whitespace", " 01", "invalid pattern syntax: "},
		{"binary_too_long", strings.Repeat("1", 65), "pattern bits too long: "},
		{"hex_too_long", "x" + strings.Repeat("f", 17), "pattern bits too long: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := bisect.New(tc.pattern)
			if m != nil || err == nil {
				t.Fatalf("invalid pattern %q returned matcher=%v error=%v", tc.pattern, m, err)
			}
			if got, want := err.Error(), tc.diagnostic+tc.pattern; got != want {
				t.Fatalf("diagnostic=%q, want %q", got, want)
			}
		})
	}
}

func reqBisectU64(value uint64) []byte {
	var data [8]byte
	binary.LittleEndian.PutUint64(data[:], value)
	return data[:]
}

func reqBisectU32(value uint32) []byte {
	var data [4]byte
	binary.LittleEndian.PutUint32(data[:], value)
	return data[:]
}

// Standard library FNV consumes independently declared wire bytes. It does not
// call bisect.Hash or duplicate its argument switch or integer hashing loops.
func reqBisectWantID(parts ...[]byte) uint64 {
	h := fnv.New64a()
	for _, part := range parts {
		_, _ = h.Write(part)
	}
	return h.Sum64()
}

func TestReqBisectContract_HashEncoding(t *testing.T) {
	cases := []struct {
		name  string
		args  []any
		bytes [][]byte
	}{
		{"empty", nil, nil},
		{"empty_string", []any{""}, nil},
		{"empty_slices", []any{[]string(nil), []byte(nil), []int(nil), []uint(nil), []int32(nil), []uint32(nil), []int64(nil), []uint64(nil), []uintptr(nil)}, nil},
		{"unicode_string", []any{"网关/λ"}, [][]byte{[]byte("网关/λ")}},
		{"byte", []any{byte(0xff)}, [][]byte{{0xff}}},
		{"signed_int", []any{int(-2)}, [][]byte{reqBisectU64(^uint64(1))}},
		{"unsigned_int", []any{uint(0x01020304)}, [][]byte{reqBisectU64(0x01020304)}},
		{"signed_int32", []any{int32(-7)}, [][]byte{reqBisectU32(^uint32(6))}},
		{"unsigned_int32", []any{uint32(0x11223344)}, [][]byte{reqBisectU32(0x11223344)}},
		{"signed_int64", []any{int64(-2)}, [][]byte{reqBisectU64(^uint64(1))}},
		{"unsigned_int64", []any{uint64(0x0123456789abcdef)}, [][]byte{reqBisectU64(0x0123456789abcdef)}},
		{"uintptr", []any{uintptr(0x11223344)}, [][]byte{reqBisectU64(0x11223344)}},
		{"strings_slice", []any{[]string{"HTTP", "", "/2"}}, [][]byte{[]byte("HTTP/2")}},
		{"bytes_slice", []any{[]byte{0, 0xff, 1}}, [][]byte{{0, 0xff, 1}}},
		{"int_slice", []any{[]int{-2, 7}}, [][]byte{reqBisectU64(^uint64(1)), reqBisectU64(7)}},
		{"uint_slice", []any{[]uint{0, 0x01020304}}, [][]byte{reqBisectU64(0), reqBisectU64(0x01020304)}},
		{"int32_slice", []any{[]int32{-7, 3}}, [][]byte{reqBisectU32(^uint32(6)), reqBisectU32(3)}},
		{"uint32_slice", []any{[]uint32{0, 0xffffffff}}, [][]byte{reqBisectU32(0), reqBisectU32(0xffffffff)}},
		{"int64_slice", []any{[]int64{-2, 7}}, [][]byte{reqBisectU64(^uint64(1)), reqBisectU64(7)}},
		{"uint64_slice", []any{[]uint64{0, ^uint64(0)}}, [][]byte{reqBisectU64(0), reqBisectU64(^uint64(0))}},
		{"uintptr_slice", []any{[]uintptr{0, 0x11223344}}, [][]byte{reqBisectU64(0), reqBisectU64(0x11223344)}},
		{"mixed", []any{"HTTP", byte('2'), uint32(0x01020304), int64(-1)}, [][]byte{[]byte("HTTP2"), {4, 3, 2, 1}, {0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := bisect.Hash(tc.args...), reqBisectWantID(tc.bytes...); got != want {
				t.Fatalf("Hash(%v)=%016x, independent FNV wire oracle=%016x", tc.args, got, want)
			}
		})
	}
}

func TestReqBisectContract_HashUnsupported(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"bool", true},
		{"float", 1.5},
		{"struct", struct{}{}},
		{"slice", []uint16{1}},
		{"nil", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if got := recover(); got != "bisect.Hash: unexpected argument type" {
					t.Fatalf("unsupported %T panic=%v", tc.value, got)
				}
			}()
			bisect.Hash(tc.value)
		})
	}
}

func TestReqBisectContract_MarkerFormat(t *testing.T) {
	cases := []struct {
		name string
		id   uint64
	}{
		{"zero", 0},
		{"one", 1},
		{"mixed", 0x0123456789abcdef},
		{"max", ^uint64(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := fmt.Sprintf("[bisect-match 0x%016x]", tc.id)
			if got := bisect.Marker(tc.id); got != want {
				t.Fatalf("Marker=%q, want %q", got, want)
			}
			dst := []byte("caller:")
			if got := string(bisect.AppendMarker(dst, tc.id)); got != "caller:"+want || string(dst) != "caller:" {
				t.Fatalf("AppendMarker=%q, original prefix=%q", got, dst)
			}
			var out bytes.Buffer
			if err := bisect.PrintMarker(&out, tc.id); err != nil || out.String() != want+"\n" {
				t.Fatalf("PrintMarker=%q error=%v, want %q", out.String(), err, want+"\n")
			}
		})
	}
}

type reqBisectErrorWriter struct {
	err   error
	calls int
	bytes []byte
}

func (w *reqBisectErrorWriter) Write(data []byte) (int, error) {
	w.calls++
	w.bytes = append(w.bytes, data...)
	return 0, w.err
}

func TestReqBisectContract_MarkerWriterError(t *testing.T) {
	sentinel := errors.New("caller-owned diagnostic writer failure")
	w := &reqBisectErrorWriter{err: sentinel}
	if err := bisect.PrintMarker(w, 1); !errors.Is(err, sentinel) {
		t.Fatalf("PrintMarker error=%v, want original writer failure", err)
	}
	if w.calls != 1 || string(w.bytes) != "[bisect-match 0x0000000000000001]\n" {
		t.Fatalf("writer calls=%d bytes=%q", w.calls, w.bytes)
	}
}

func TestReqBisectContract_CutMarker(t *testing.T) {
	maxBinary := "[bisect-match " + strings.Repeat("1", 64) + "]"
	overflowBinary := "[bisect-match " + strings.Repeat("1", 65) + "]"
	overflowHex := "[bisect-match 0x" + strings.Repeat("f", 17) + "]"
	cases := []struct {
		name, line, short string
		id                uint64
		ok                bool
	}{
		{"plain", "plain HTTP debug text", "plain HTTP debug text", 0, false},
		{"prefix_fragment", "[bisect-match ", "[bisect-match ", 0, false},
		{"missing_close", "header [bisect-match 001", "header [bisect-match 001", 0, false},
		{"empty_binary", "[bisect-match ]", "[bisect-match ]", 0, false},
		{"binary_zero", "[bisect-match 0]", "", 0, true},
		{"binary_one", "[bisect-match 1]", "", 1, true},
		{"binary_mixed", "abc[bisect-match 101]def", "abcdef", 5, true},
		{"binary_upper_limit", maxBinary, "", ^uint64(0), true},
		{"binary_overflow", overflowBinary, overflowBinary, 0, false},
		{"invalid_binary_digit", "[bisect-match 102]", "[bisect-match 102]", 0, false},
		{"hex_zero", "x [bisect-match 0x0] y", "x y", 0, true},
		{"hex_mixed", "[bisect-match 0x1a2b]", "", 0x1a2b, true},
		{"hex_uppercase", "[bisect-match 0xDeAd]", "", 0xdead, true},
		{"hex_max", "[bisect-match 0xffffffffffffffff]", "", ^uint64(0), true},
		{"hex_overflow", overflowHex, overflowHex, 0, false},
		{"space_before", "before [bisect-match 1]", "before", 1, true},
		{"space_after", "[bisect-match 1] after", "after", 1, true},
		{"spacing_both", "left  [bisect-match 1]  right", "left   right", 1, true},
		{"first_of_two", "a [bisect-match 1] b [bisect-match 10]", "a b [bisect-match 10]", 1, true},
		{"unicode_surround", "左[bisect-match 101]右", "左右", 5, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			short, id, ok := bisect.CutMarker(tc.line)
			if short != tc.short || id != tc.id || ok != tc.ok {
				t.Fatalf("CutMarker(%q)=(%q,%x,%v), want (%q,%x,%v)",
					tc.line, short, id, ok, tc.short, tc.id, tc.ok)
			}
		})
	}
}

func TestReqBisectContract_FileLine(t *testing.T) {
	cases := []struct {
		name, pattern string
		line          int
		enabled       bool
		report        string
	}{
		{"nil", "", 0, true, ""},
		{"marker_zero", "y", 0, true, "marker"},
		{"verbose_positive", "vy", 37, true, "file"},
		{"verbose_negative", "vy", -37, true, "file"},
		{"verbose_zero", "vy", 0, true, "file"},
		{"quiet", "qy", 37, true, ""},
		{"excluded", "-y", 37, false, ""},
		{"disabled_all", "n", 37, false, "marker"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := bisect.New(tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			const file = "网关/transfer.go"
			var output bytes.Buffer
			if got := m.FileLine(&output, file, tc.line); got != tc.enabled {
				t.Fatalf("FileLine enabled=%v, want %v", got, tc.enabled)
			}
			want := ""
			if tc.report != "" {
				id := reqBisectWantID([]byte(file), reqBisectU64(uint64(int64(tc.line))))
				want = fmt.Sprintf("[bisect-match 0x%016x]", id)
				if tc.report == "file" {
					want += fmt.Sprintf("%s:%d", file, tc.line)
				}
				want += "\n"
			}
			if output.String() != want {
				t.Fatalf("FileLine report=%q, want %q", output.String(), want)
			}
		})
	}
}

type reqBisectConcurrentWriter struct {
	mu  sync.Mutex
	out bytes.Buffer
}

func (w *reqBisectConcurrentWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(data)
}

func (w *reqBisectConcurrentWriter) snapshot() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.String()
}

// Keep the intended call context stable across repeated invocations without
// replacing runtime.Callers, exposing private state, or assuming an absolute PC.
//
//go:noinline
func reqBisectCallStack(m *bisect.Matcher, w bisect.Writer) bool {
	return m.Stack(w)
}

func reqBisectStackMarkers(t *testing.T, output string, verbose bool) (int, int) {
	t.Helper()
	const prefix = "[bisect-match 0x"
	tails := make(map[uint64]int)
	allIDs := make(map[uint64]bool)
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		end := len(prefix) + 16
		if !strings.HasPrefix(line, prefix) || len(line) <= end || line[end] != ']' {
			t.Fatalf("malformed actual stack report line %q", line)
		}
		id, err := strconv.ParseUint(line[len(prefix):end], 16, 64)
		if err != nil || line[:end+1] != fmt.Sprintf("[bisect-match 0x%016x]", id) {
			t.Fatalf("invalid marker in actual stack report %q: %v", line, err)
		}
		allIDs[id] = true
		rest := line[end+1:]
		if !verbose && rest != "" {
			t.Fatalf("marker-only report disclosed extra text %q", rest)
		}
		if rest == "" {
			tails[id]++
		}
	}
	total := 0
	for _, count := range tails {
		total += count
	}
	return total, len(allIDs)
}

func reqBisectConcurrentStack(t *testing.T, m *bisect.Matcher, w bisect.Writer) {
	t.Helper()
	const workers = 16
	var group sync.WaitGroup
	start := make(chan struct{})
	failed := make(chan bool, workers)
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for iteration := 0; iteration < 16; iteration++ {
				if !reqBisectCallStack(m, w) {
					failed <- true
					return
				}
			}
		}()
	}
	close(start)
	group.Wait()
	close(failed)
	for range failed {
		t.Error("all-selected concurrent Stack unexpectedly disabled its caller")
	}
}

func TestReqBisectContract_StackReports(t *testing.T) {
	cases := []struct {
		name, pattern string
		enabled       bool
		verbose       bool
		concurrent    bool
		printed       bool
	}{
		{"nil", "", true, false, false, false},
		{"quiet", "qy", true, false, false, false},
		{"excluded", "-y", false, false, false, false},
		{"marker_dedup_same_site", "y", true, false, false, true},
		{"verbose_dedup_same_site", "vy", true, true, false, true},
		{"disabled_marker", "n", false, false, false, true},
		{"concurrent_verbose", "vy", true, true, true, true},
		{"concurrent_marker_lossy", "y", true, false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := bisect.New(tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			var output reqBisectConcurrentWriter
			if tc.concurrent {
				reqBisectConcurrentStack(t, m, &output)
			} else {
				for iteration := 0; iteration < 3; iteration++ {
					if got := reqBisectCallStack(m, &output); got != tc.enabled {
						t.Fatalf("Stack enabled=%v, want %v", got, tc.enabled)
					}
				}
			}
			text := output.snapshot()
			if !tc.printed {
				if text != "" {
					t.Fatalf("non-reporting Stack wrote %q", text)
				}
				return
			}
			reports, ids := reqBisectStackMarkers(t, text, tc.verbose)
			if reports < 1 {
				t.Fatalf("actual reports=%d IDs=%d, want at least one valid report", reports, ids)
			}
			if tc.name != "concurrent_marker_lossy" && (reports != 1 || ids != 1) {
				t.Fatalf("same-context report was not deduplicated: reports=%d IDs=%d", reports, ids)
			}
			if tc.verbose && !strings.Contains(text, "TestReqBisectContract_StackReports") &&
				!strings.Contains(text, "reqBisectConcurrentStack") {
				t.Fatalf("verbose report lacks actual caller function: %q", text)
			}
			// No exact-once assertion for concurrent_marker_lossy: the API's
			// approximate lock-free cache explicitly permits duplicates. Any
			// actual -race failure still fails remote CI and is not waived.
		})
	}
}
