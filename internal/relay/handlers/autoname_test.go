package handlers

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestDeriveConversationName covers AC 1 in full: the normalisation, the
// within-bound identity, both cut shapes, and the rune-not-byte counting.
//
// Every want is written as a literal rather than computed from maxAutoNameRunes,
// so a build that changed the bound would redden here instead of agreeing with
// itself. The rune counts in the comments are the arithmetic each row turns on.
func TestDeriveConversationName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		text   string
		want   string
		wantOK bool
	}{
		{
			// The attachment-only message: a person attaches a file and says
			// nothing. Nothing is written and nothing is pushed.
			name: "empty yields no name",
			text: "",
		},
		{
			name: "whitespace only yields no name",
			text: "   ",
		},
		{
			// Tabs and newlines are whitespace to unicode.IsSpace too, so this
			// normalises to empty exactly as a run of spaces does.
			name: "tabs and newlines only yield no name",
			text: "\t\n \r\n\t",
		},
		{
			name:   "short text is the title verbatim",
			text:   "fix the login bug",
			want:   "fix the login bug",
			wantOK: true,
		},
		{
			name:   "leading and trailing whitespace is trimmed",
			text:   "  \n fix the login bug \t\n ",
			want:   "fix the login bug",
			wantOK: true,
		},
		{
			// The AC's "collapse every run of whitespace, newlines included, to
			// one space" row. A build that only trimmed the ends, or only
			// collapsed spaces, produces a multi-line or double-spaced title.
			name:   "embedded newlines and runs collapse to single spaces",
			text:   "fix the\n\nlogin    bug\ttoday",
			want:   "fix the login bug today",
			wantOK: true,
		},
		{
			// Exactly at the bound: nothing was dropped, so NO ellipsis. This is
			// the row that catches an off-by-one which truncates at the boundary.
			// 40 runes: 4 x "0123456789" with no separators.
			name:   "exactly forty runes is verbatim with no ellipsis",
			text:   "0123456789012345678901234567890123456789",
			want:   "0123456789012345678901234567890123456789",
			wantOK: true,
		},
		{
			// One rune past the bound, cut at a word boundary. "aaaa bbbb cccc
			// dddd eeee ffff gggg hhhh" is 39 runes; adding " iiii" makes 44, so
			// the last word is dropped and the retained text is the 39-rune
			// prefix — under the bound, because whole words are taken.
			name:   "over the bound cuts at a word boundary and appends the ellipsis",
			text:   "aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii",
			want:   "aaaa bbbb cccc dddd eeee ffff gggg hhhh…",
			wantOK: true,
		},
		{
			// One word longer than the bound: cut the WORD at 40 runes. 45 'a's
			// in, 40 'a's plus the ellipsis out.
			name:   "a single over-long word is cut mid-word at the bound",
			text:   strings.Repeat("a", 45),
			want:   strings.Repeat("a", 40) + "…",
			wantOK: true,
		},
		{
			// A first word of EXACTLY the bound followed by more. Whole-word
			// taking keeps that word alone (adding the space plus "tail" would
			// exceed), and something was dropped, so the ellipsis appears. A
			// build that only special-cased "first word > bound" leaves this row
			// un-truncated at 45 runes.
			name:   "a bound-length first word keeps the word and drops the rest",
			text:   strings.Repeat("a", 40) + " tail",
			want:   strings.Repeat("a", 40) + "…",
			wantOK: true,
		},
		{
			// 40 CJK runes = 120 bytes. A byte-counting build truncates this to
			// ~13 characters; a byte-SLICING build splits a rune and emits
			// U+FFFD.
			name:   "forty multi-byte runes survive whole",
			text:   strings.Repeat("日", 40),
			want:   strings.Repeat("日", 40),
			wantOK: true,
		},
		{
			// 41 CJK runes as one word: cut at 40 runes, not 40 bytes.
			name:   "an over-long multi-byte word is cut on rune boundaries",
			text:   strings.Repeat("日", 41),
			want:   strings.Repeat("日", 40) + "…",
			wantOK: true,
		},
		{
			// An emoji outside the BMP (4 bytes in UTF-8, 1 rune here). The
			// title keeps it whole.
			name:   "an astral-plane rune counts as one and is not split",
			text:   "ship it 🚀",
			want:   "ship it 🚀",
			wantOK: true,
		},
		{
			// A slash command names the chat after the command. Accepted, no
			// special case — the operator can see what the chat is.
			name:   "a slash command is a title like any other",
			text:   "/compact",
			want:   "/compact",
			wantOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := deriveConversationName(tc.text)
			if ok != tc.wantOK {
				t.Fatalf("deriveConversationName(%q) ok = %v, want %v", tc.text, ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("deriveConversationName(%q) = %q, want %q", tc.text, got, tc.want)
			}
			if !ok && got != "" {
				t.Errorf("a refused derivation returned %q, want the empty string", got)
			}
			// The bound is on the RETAINED text: a truncated title is the bound
			// plus the one-rune ellipsis, and an untruncated one is at most the
			// bound. Asserted on every row so no case can quietly exceed it.
			if n := utf8.RuneCountInString(strings.TrimSuffix(got, "…")); n > 40 {
				t.Errorf("retained text is %d runes, want at most 40", n)
			}
		})
	}
}
