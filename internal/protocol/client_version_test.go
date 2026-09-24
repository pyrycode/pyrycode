package protocol

import (
	"strings"
	"testing"
)

// TestParseClientVersion pins the grammar docs/protocol-mobile.md § hello
// (v2-specific note) gives client_version: <app>/<MAJOR>.<MINOR>.<PATCH>, at
// most 32 bytes (#2576, #2578).
func TestParseClientVersion(t *testing.T) {
	t.Parallel()

	// 32 bytes exactly: 15 + 1 + 16.
	at32 := "pyrycode-mobile/" + "1234567890.123.0"
	if len(at32) != maxClientVersionBytes {
		t.Fatalf("fixture at32 is %d bytes, want %d", len(at32), maxClientVersionBytes)
	}
	over32 := "pyrycode-mobile/" + "1234567890.1234.0"

	valid := []struct {
		in      string
		wantApp string
		want    Version
	}{
		{"pyrycode-mobile/1.4.0", AppMobile, Version{1, 4, 0}},
		{"pyrycode-desktop/0.3.0", AppDesktop, Version{0, 3, 0}},
		{"pyrycode-android/0.0.0", "pyrycode-android", Version{0, 0, 0}},
		{"a/10.20.30", "a", Version{10, 20, 30}},
		{"a1-b/1.0.0", "a1-b", Version{1, 0, 0}},
		{at32, AppMobile, Version{1234567890, 123, 0}},
		{"a/18446744073709551615.0.0", "a", Version{18446744073709551615, 0, 0}},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.in, func(t *testing.T) {
			app, v, ok := ParseClientVersion(tc.in)
			if !ok {
				t.Fatalf("ParseClientVersion(%q) ok = false, want true", tc.in)
			}
			if app != tc.wantApp || v != tc.want {
				t.Errorf("ParseClientVersion(%q) = (%q, %+v), want (%q, %+v)", tc.in, app, v, tc.wantApp, tc.want)
			}
		})
	}

	unparsable := []struct{ name, in string }{
		{"empty", ""},
		{"legacy two-part", "1.0"},
		{"legacy three-part no app", "0.1.0"},
		{"space instead of slash", "pyrycode-mobile 0.1.0"},
		{"over 32 bytes", over32},
		{"leading zero major", "pyrycode-mobile/01.4.0"},
		{"leading zero patch", "pyrycode-mobile/1.4.00"},
		{"pre-release suffix", "pyrycode-mobile/1.4.0-beta"},
		{"build suffix", "pyrycode-mobile/1.4.0+42"},
		{"v prefix", "pyrycode-mobile/v1.4.0"},
		{"two slashes", "pyrycode-mobile/1.4.0/1"},
		{"slash in app", "pyrycode/mobile/1.4.0"},
		{"empty app", "/1.4.0"},
		{"uppercase app", "Pyrycode-mobile/1.4.0"},
		{"app starts with digit", "1app/1.4.0"},
		{"app starts with dash", "-app/1.4.0"},
		{"app with underscore", "pyrycode_mobile/1.4.0"},
		{"empty version", "pyrycode-mobile/"},
		{"two components", "pyrycode-mobile/1.4"},
		{"four components", "pyrycode-mobile/1.4.0.0"},
		{"empty component", "pyrycode-mobile/1..0"},
		{"trailing dot", "pyrycode-mobile/1.4."},
		{"leading dot", "pyrycode-mobile/.1.4"},
		{"space in version", "pyrycode-mobile/1.4. 0"},
		{"uint64 overflow", "a/18446744073709551616.0.0"},
		{"long digit run overflow", "a/99999999999999999999999.0.0"},
		{"non-ascii", "pyrycode-mobilé/1.4.0"},
		{"quote", `a/1.4.0"`},
	}
	for _, tc := range unparsable {
		t.Run("unparsable/"+tc.name, func(t *testing.T) {
			if app, v, ok := ParseClientVersion(tc.in); ok {
				t.Errorf("ParseClientVersion(%q) = (%q, %+v, true), want unparsable", tc.in, app, v)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in     string
		want   Version
		wantOK bool
	}{
		{"1.4.0", Version{1, 4, 0}, true},
		{"0.0.0", Version{0, 0, 0}, true},
		{"", Version{}, false},
		{"1.0", Version{}, false},
		{"01.0.0", Version{}, false},
		{"1.0.0-rc", Version{}, false},
		{"pyrycode-mobile/1.0.0", Version{}, false},
		{strings.Repeat("1", 31) + ".0.0", Version{}, false},
	}
	for _, tc := range cases {
		got, ok := ParseVersion(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("ParseVersion(%q) = (%+v, %v), want (%+v, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestVersion_CompareAndString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		a, b Version
		want int
	}{
		{Version{1, 4, 0}, Version{1, 4, 0}, 0},
		{Version{1, 3, 9}, Version{1, 4, 0}, -1},
		{Version{2, 0, 0}, Version{1, 99, 99}, 1},
		{Version{1, 4, 1}, Version{1, 4, 0}, 1},
		{Version{0, 10, 0}, Version{0, 9, 0}, 1},
		{Version{1, 4, 0}, Version{1, 4, 2}, -1},
	}
	for _, tc := range cases {
		if got := tc.a.Compare(tc.b); got != tc.want {
			t.Errorf("%+v.Compare(%+v) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}

	if got := (Version{1, 4, 0}).String(); got != "1.4.0" {
		t.Errorf("String() = %q, want %q", got, "1.4.0")
	}
}
