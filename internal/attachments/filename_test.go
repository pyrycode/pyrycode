package attachments

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// assertComponent asserts the postconditions SanitizeFilename's doc comment
// states, all of which are stated on the RETURNED value and therefore hold for
// every input rather than only for the row aimed at them. Asserting them in the
// table loop makes every row a fixture for all of them at once.
//
// The length check is len, not utf8.RuneCountInString: the published bound
// protocol.MaxAttachmentFilenameBytes counts bytes, and a rune-count assertion
// passes on a multi-byte fixture while the byte bound is blown.
func assertComponent(t *testing.T, in, got string) {
	t.Helper()

	if got == "" {
		t.Errorf("SanitizeFilename(%q) = %q, want a non-empty component", in, got)
	}
	if len(got) > protocol.MaxAttachmentFilenameBytes {
		t.Errorf("SanitizeFilename(%q) is %d bytes, want at most %d", in, len(got), protocol.MaxAttachmentFilenameBytes)
	}
	if !utf8.ValidString(got) {
		t.Errorf("SanitizeFilename(%q) = %q, want valid UTF-8", in, got)
	}
	if strings.Contains(got, "/") {
		t.Errorf("SanitizeFilename(%q) = %q, want no path separator", in, got)
	}
	for _, r := range got {
		if r < 0x20 || r == 0x7f {
			t.Errorf("SanitizeFilename(%q) = %q, want no control character (found %q)", in, got, r)
			break
		}
	}
	if strings.HasPrefix(got, ".") {
		t.Errorf("SanitizeFilename(%q) = %q, want a result that does not begin with %q", in, got, ".")
	}
	if got == "." || got == ".." {
		t.Errorf("SanitizeFilename(%q) = %q, want a name rather than a path element", in, got)
	}
}

func TestSanitizeFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		// Separators and parent-directory references. "." and ".." gain a
		// LEADING underscore, where cmd/pyry's sanitizeName gives them a
		// trailing one ("._", ".._"): a suffix leaves the result beginning
		// with a dot, so the stored file would still be hidden.
		{"../etc/passwd", "_.._etc_passwd"},
		{"a/../b", "a_.._b"},
		{"..", "_.."},
		{".", "_."},
		{`a\b`, "a_b"},

		// Control characters. The NUL row is the classic filename attack
		// (report.txt\x00.exe), not merely one control character among
		// several; the allowlist takes it with everything else outside it.
		{"a\x00b", "a_b"},
		{"a\nb", "a_b"},
		{"a\rb", "a_b"},
		{"a\r\nb", "a__b"},

		// Hidden files. The prefix rule is positional, so a non-leading dot
		// survives untouched.
		{".bashrc", "_.bashrc"},
		{".ssh", "_.ssh"},
		{"a.b.c", "a.b.c"},

		// The fixed fallback: the empty input, and every input where nothing
		// survived the allowlist. "///" is the row that makes the second arm
		// live — an emptiness check derived from the built result answers
		// "___" here, which is not "the same one every time". "報告書" is the
		// legitimate name that shows the strong reading is the useful one.
		{"", fallbackFilename},
		{"///", fallbackFilename},
		{"報告書", fallbackFilename},
		{"\x00\n", fallbackFilename},

		// The byte bound, measured on the OUTPUT.
		{strings.Repeat("a", 300), strings.Repeat("a", protocol.MaxAttachmentFilenameBytes)},
		// 400 runes and 600 input bytes mapping to 400 output bytes, so a
		// count confused between runes and bytes lands somewhere other than
		// 255.
		{strings.Repeat("aé", 200), strings.Repeat("a_", 127) + "a"},
		// The off-by-one pin: exactly at the bound, returned unchanged.
		{strings.Repeat("a", protocol.MaxAttachmentFilenameBytes), strings.Repeat("a", protocol.MaxAttachmentFilenameBytes)},
		// The ordering pin. This 255-byte name grows to 256 when the leading
		// dot is prefixed, so it must be truncated AFTER the prefix; an
		// implementation that truncates first returns 256 bytes.
		{"." + strings.Repeat("a", 254), "_." + strings.Repeat("a", 253)},

		// Already one safe component within the bound: byte-identical back.
		{"report-2026.pdf", "report-2026.pdf"},
		{"a_b-c.tar.gz", "a_b-c.tar.gz"},
		{"README", "README"},
		{"2026", "2026"},
		// These two pin that "nothing survived" is decided from the INPUT: an
		// underscore is on the allowlist, so it survives, and their output is
		// indistinguishable from the output of a name whose every character
		// was replaced. A result-derived check answers fallbackFilename here.
		{"___", "___"},
		{"_", "_"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got := SanitizeFilename(tt.in)
			if got != tt.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.in, got, tt.want)
			}
			assertComponent(t, tt.in, got)
		})
	}
}

// TestSanitizeFilename_FallbackIsAFixpoint pins the fallback constant's own
// safety: any future edit giving it a leading dot, a separator or an over-long
// value turns this red. It is also why the fallback may be returned directly
// rather than routed through the remaining steps.
func TestSanitizeFilename_FallbackIsAFixpoint(t *testing.T) {
	t.Parallel()

	if got := SanitizeFilename(fallbackFilename); got != fallbackFilename {
		t.Errorf("SanitizeFilename(%q) = %q, want the fallback unchanged", fallbackFilename, got)
	}
	assertComponent(t, fallbackFilename, fallbackFilename)
}

// TestTruncateToBytes exercises the helper directly, on genuinely multi-byte
// input. SanitizeFilename's own output is ASCII by the time truncation runs, so
// no test driven through the exported surface can tell a rune-safe cut from a
// plain s[:maxBytes] — this is the test that makes "no multi-byte rune is
// split" a real claim rather than a vacuous one.
func TestTruncateToBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		in       string
		maxBytes int
		want     string
	}{
		{"under the bound", "abc", 10, "abc"},
		{"exactly at the bound", "abc", 3, "abc"},
		{"one byte over", "abcd", 3, "abc"},
		// This pair is the sole red for a plain s[:maxBytes]: cutting at 2
		// lands inside é, so the rune goes; cutting at 3 lands exactly on its
		// far boundary, so it stays.
		{"cut inside a two-byte rune", "héllo", 2, "h"},
		{"cut on a two-byte rune's boundary", "héllo", 3, "hé"},
		{"two boundaries to back over", "日本語", 4, "日"},
		// The documented degenerate case: the bound is smaller than the first
		// rune. SanitizeFilename never reaches it, since it passes 255.
		{"bound below the first rune", "é", 1, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateToBytes(tt.in, tt.maxBytes)
			if got != tt.want {
				t.Errorf("truncateToBytes(%q, %d) = %q, want %q", tt.in, tt.maxBytes, got, tt.want)
			}
			if len(got) > tt.maxBytes {
				t.Errorf("truncateToBytes(%q, %d) is %d bytes, want at most %d", tt.in, tt.maxBytes, len(got), tt.maxBytes)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncateToBytes(%q, %d) = %q, want valid UTF-8", tt.in, tt.maxBytes, got)
			}
		})
	}
}
