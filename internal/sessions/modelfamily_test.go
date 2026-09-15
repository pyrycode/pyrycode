package sessions

import "testing"

// TestFamilyAlias is #2447's AC 1: the exact list of values the rule rewrites and
// the exact list it must leave alone, in one table, because the rule is only
// meaningful as a pair of lists. Every rewritten row is a value claude has been
// observed to publish as a row value; every unchanged row is a shape that would
// have been rewritten by a looser rule, so the table doubles as the record of
// which loosenings were rejected.
func TestFamilyAlias(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Rewritten. The bracket group is carried through untouched rather than
		// special-cased on "1m": it is the only variant seen so far, and a rule
		// keyed on its text would silently drop the next one.
		{"dated fable variant", "claude-fable-5-1[1m]", "fable[1m]"},
		{"older fable variant", "claude-fable-5[1m]", "fable[1m]"},
		{"opus exact id", "claude-opus-5", "opus"},
		{"haiku exact id", "claude-haiku-4-5", "haiku"},
		{"haiku exact id with a date segment", "claude-haiku-4-5-20251001", "haiku"},
		{"sonnet exact id", "claude-sonnet-5", "sonnet"},

		// Unchanged. A bare alias already follows its family, so rewriting it
		// would be a no-op at best; the rest are shapes the rule must not touch.
		{"bare alias", "sonnet", "sonnet"},
		{"bare alias with a variant group", "opus[1m]", "opus[1m]"},
		{"the default sentinel", "default", "default"},
		{"empty", "", ""},
		// The legacy id puts the version before the family, so the segment after
		// "claude" is not letters. Rewriting it would need a rule that searches
		// for the family rather than reading it from a fixed position, and such a
		// rule would have to know the family names.
		{"legacy generation-first id", "claude-3-5-sonnet-20241022", "claude-3-5-sonnet-20241022"},
		{"empty family segment", "claude--5", "claude--5"},
		// An unclosed group is not a group. Splitting on the "[" anyway would emit
		// a base whose trailing bytes were silently discarded.
		{"unclosed group", "claude-fable-5-1[1m", "claude-fable-5-1[1m"},
		{"a group with no base", "[1m]", "[1m]"},
		{"no version segment", "claude-fable", "claude-fable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := familyAlias(tc.in)
			if got != tc.want {
				t.Errorf("familyAlias(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Per row, the property claudeSettingsArgs depends on and cannot
			// state for itself: it guards on a non-empty stored Model and then
			// appends this output as the element AFTER --model, so an empty
			// return would compose an empty model value onto the argv. Two arms
			// make that unreachable today; this is what reddens if a third is
			// ever added beside them.
			if tc.in != "" && got == "" {
				t.Errorf("familyAlias(%q) returned empty; --model would carry an empty value", tc.in)
			}
		})
	}
}

// TestFamilyAlias_RewrittenOutputIsNarrowerThanItsInput is the machine-checked
// form of #2447's security argument, in the shape of internal/relay's
// TestValidModel_ByteSetIsClosed: it walks all 256 byte values through three
// positions of a rewritable template and asserts that whatever comes back is
// either byte-identical to what went in, or matches the strict shape
// [A-Za-z]+("[" [A-Za-z0-9._-]+ "]")?.
//
// That second arm is what keeps internal/relay's validModel's two sink
// properties intact WITHOUT depending on validModel having run: not every path
// into this function passes it (settingsFromEntry copies the on-disk value with
// no shape check, and the operator's own --model in the bootstrap template is
// never validated), so "the input was already safe" would be a false premise.
// A rewritten value's first byte being a letter is the whole of the bar that
// stops a value posing as a claude flag on the argv sink.
func TestFamilyAlias_RewrittenOutputIsNarrowerThanItsInput(t *testing.T) {
	t.Parallel()
	// One injection site per structural position the rule reads: the family
	// segment, a version segment, and the variant group's interior. %c is a byte
	// here, not a rune — every template is built from a single byte value.
	templates := []struct {
		name  string
		build func(b byte) string
	}{
		{"family segment", func(b byte) string { return "claude-fab" + string(b) + "le-5" }},
		{"version segment", func(b byte) string { return "claude-fable-5" + string(b) + "1" }},
		{"group interior", func(b byte) string { return "claude-fable-5[1" + string(b) + "m]" }},
	}
	for _, tpl := range templates {
		t.Run(tpl.name, func(t *testing.T) {
			t.Parallel()
			for i := 0; i < 256; i++ {
				in := tpl.build(byte(i))
				got := familyAlias(in)
				if got == in {
					continue // the identity arm passes today's behaviour through
				}
				if !strictAliasShape(got) {
					t.Errorf("familyAlias(%q) = %q, which is neither its input nor a "+
						"letters-plus-optional-group value; byte %#x escaped the rewrite arm", in, got, i)
				}
				if len(got) > len(in) {
					t.Errorf("familyAlias(%q) = %q, longer than its input; the upstream 64-byte bound no longer holds", in, got)
				}
			}
		})
	}
}

// strictAliasShape reports whether s matches [A-Za-z]+("[" [A-Za-z0-9._-]+ "]")?
// — the only shape familyAlias may emit when it rewrites. Written out here rather
// than reusing the production helpers so the test states the property
// independently instead of asking the code whether it agrees with itself.
func strictAliasShape(s string) bool {
	base := s
	if i := indexByteInString(s, '['); i >= 0 {
		if s[len(s)-1] != ']' {
			return false
		}
		inner := s[i+1 : len(s)-1]
		if inner == "" {
			return false
		}
		for j := 0; j < len(inner); j++ {
			c := inner[j]
			letter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
			digit := c >= '0' && c <= '9'
			if !letter && !digit && c != '.' && c != '_' && c != '-' {
				return false
			}
		}
		base = s[:i]
	}
	if base == "" {
		return false
	}
	for i := 0; i < len(base); i++ {
		if c := base[i]; !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

func indexByteInString(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
