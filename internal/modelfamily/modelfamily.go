// Package modelfamily maps a Claude model id to the family alias a pyry session
// follows, and reduces claude's published model list to one row per family.
//
// The operator's rule (2026-09-15, 2026-09-24) is that a pyry session follows the
// latest model of its family, and that the model menu shows one row per family. A
// pinned id such as claude-opus-4-7 or claude-opus-5 holds a session on a model
// claude has since superseded, so every Claude model the daemon hands to claude,
// stores or offers goes through this package first.
//
// Claude Code does not publish family names only. Since about 2.1.289 its list
// carries the family rows (default, opus, fable, sonnet, haiku) followed by pinned
// rows (claude-sonnet-5, claude-opus-5, claude-fable-5, claude-opus-4-8,
// claude-opus-4-7, and more). Reduce removes those pinned rows; Alias resolves any
// pinned id that still arrives, from a client, a saved setting or Claude Code's own
// settings file, to its family.
//
// Only Claude ids are touched. A Codex model ("gpt-5.6-terra", or its family
// "terra") never has the claude- prefix, so both functions return it unchanged.
package modelfamily

import "strings"

// Alias returns the family alias for an exact Claude model id, carrying any
// trailing variant group through unchanged: "claude-fable-5-1[1m]" becomes
// "fable[1m]", "claude-opus-5" becomes "opus". Everything else is returned byte
// for byte as it arrived, including a bare alias ("opus", "opus[1m]"), "default"
// and "".
//
// The rule, deliberately narrow (#2447): after any trailing variant group is split
// off, the base must divide on "-" into "claude", a family segment of letters, and
// at least one further segment, with every remaining segment all digits. That
// covers an id with no minor version (claude-opus-5) and a dated one
// (claude-haiku-4-5-20251001). It refuses the legacy generation-first spelling
// (claude-3-5-sonnet-20241022), whose family sits in no fixed position, and a value
// with no version segment at all (claude-fable).
//
// SECURITY. The output reaches an argv token and a live child's stdin, so it must
// not weaken what internal/relay's validModel guarantees for those sinks, and it
// must do so WITHOUT assuming validModel ran: a saved setting, the operator's own
// --model and Claude Code's settings file all reach here unvalidated. Both
// properties are structural:
//
//   - Every output byte is an input byte. The rewrite returns a substring of the
//     base joined to a suffix of the input, and the identity arm returns the input.
//   - A REWRITTEN value is strictly narrower than what validModel accepts: the
//     emitted base is non-empty and all ASCII letters, so it can never start with
//     "-" and pose as a claude flag, and the group, when present, is balanced,
//     non-empty, unnested and drawn from [A-Za-z0-9._-].
//
// Length never grows. A non-empty input always yields a non-empty output.
// TestAlias_RewrittenOutputIsNarrowerThanItsInput walks all 256 byte values
// through three positions rather than leaving those claims as prose.
func Alias(model string) string {
	family, ok := parsePinned(model)
	if !ok {
		return model
	}
	return family
}

// Reduce returns rows with Claude's list reduced to one row per family, in
// claude's own order. value reads a row's value, the argument passed to claude.
//
//   - A pinned row (one Alias rewrites) is dropped when a row whose value is its
//     family is also present: claude-opus-4-7 goes when opus is published.
//   - When a family has only pinned rows, the newest one is kept. Newest compares
//     the version segments numerically, and a longer version beats its own prefix,
//     so claude-opus-5-5 is newer than claude-opus-5, which is newer than
//     claude-opus-4-8.
//   - Every other row is kept: default, the family rows, and any value whose shape
//     Alias does not touch.
//
// A family key includes the variant group, so claude-opus-5-5[1m] is dropped by an
// opus[1m] row and not by an opus row.
//
// Reduce never empties a non-empty list, since every family keeps one row. It does
// not modify rows and returns a fresh slice.
func Reduce[T any](rows []T, value func(T) string) []T {
	present := make(map[string]bool, len(rows))
	for _, r := range rows {
		present[value(r)] = true
	}
	// newest maps a family with no family row to the index of its newest pinned row.
	newest := make(map[string]int)
	for i, r := range rows {
		v := value(r)
		family, ok := parsePinned(v)
		if !ok || present[family] {
			continue
		}
		if j, seen := newest[family]; !seen || newerVersion(v, value(rows[j])) {
			newest[family] = i
		}
	}
	out := make([]T, 0, len(rows))
	for i, r := range rows {
		if family, ok := parsePinned(value(r)); ok && (present[family] || newest[family] != i) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// parsePinned reports the family alias a pinned Claude id resolves to, and whether
// model is one. It is Alias's rule with the "is it pinned" answer kept.
func parsePinned(model string) (string, bool) {
	base, group := splitVariantGroup(model)
	parts, ok := pinnedParts(base)
	if !ok {
		return "", false
	}
	return parts[1] + group, true
}

// pinnedParts splits a pinned id's base into "claude", the family and the version
// segments, or reports that base is not one.
func pinnedParts(base string) ([]string, bool) {
	if base == "" {
		return nil, false
	}
	parts := strings.Split(base, "-")
	if len(parts) < 3 || parts[0] != "claude" || !modelLetters(parts[1]) {
		return nil, false
	}
	for _, segment := range parts[2:] {
		if !modelDigits(segment) {
			return nil, false
		}
	}
	return parts, true
}

// newerVersion reports whether pinned id a carries a newer version than pinned id
// b of the same family. Segments compare as numbers, without overflow, so a date
// segment is compared exactly. When one version is a prefix of the other, the
// longer one is newer: 5.5 is newer than 5. Equal versions are not newer, so the
// first of two duplicates is kept.
func newerVersion(a, b string) bool {
	baseA, _ := splitVariantGroup(a)
	baseB, _ := splitVariantGroup(b)
	pa, _ := pinnedParts(baseA)
	pb, _ := pinnedParts(baseB)
	va, vb := pa[2:], pb[2:]
	for i := 0; i < len(va) && i < len(vb); i++ {
		if c := compareDigits(va[i], vb[i]); c != 0 {
			return c > 0
		}
	}
	return len(va) > len(vb)
}

// compareDigits compares two all-digit strings as numbers.
func compareDigits(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) > len(b) {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}

// splitVariantGroup divides m into its base and its trailing variant group,
// returning the group WITH its brackets so a caller reassembles by concatenation.
// A value carrying no group returns (m, ""); one whose brackets are not a
// well-formed trailing group returns ("", ""), which reads as "not a shape this
// package touches".
//
// Three conditions on the FIRST "[", which between them rule out a nested group, a
// second group and a suffix after the group: the value's LAST byte closes the
// group, the interior is non-empty, and every interior byte is in [A-Za-z0-9._-],
// which excludes both brackets.
//
// This MIRRORS internal/contextwindow's variantBase and internal/relay's
// validModel, and deliberately shares neither: they guard different things, and
// sharing the rule would make relaxing one package's a silent change to the
// others' threat.
func splitVariantGroup(m string) (base, group string) {
	i := strings.IndexByte(m, '[')
	if i < 0 {
		return m, ""
	}
	if m[len(m)-1] != ']' {
		return "", ""
	}
	inner := m[i+1 : len(m)-1]
	if inner == "" {
		return "", ""
	}
	for j := 0; j < len(inner); j++ {
		if !modelGroupByte(inner[j]) {
			return "", ""
		}
	}
	if i == 0 {
		return "", "" // a leading group has no base to carry it
	}
	return m[:i], m[i:]
}

// modelLetters reports whether s is non-empty and entirely ASCII letters, the
// class demanded of a family segment.
func modelLetters(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

// modelDigits reports whether s is non-empty and entirely ASCII digits, the class
// demanded of every segment after the family.
func modelDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// modelGroupByte reports whether c is in [A-Za-z0-9._-], the closed byte class of
// a variant group's interior, written down in one place.
func modelGroupByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-'
}
