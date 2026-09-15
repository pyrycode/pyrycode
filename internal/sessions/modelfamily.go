package sessions

import "strings"

// familyAlias returns the family alias for an exact Anthropic model id, carrying
// any trailing variant group through unchanged — "claude-fable-5-1[1m]" becomes
// "fable[1m]" — and returns everything else byte for byte as it arrived (#2447).
//
// It exists because a session's stored model is the row value the client picked
// from claude's published list, held verbatim, and not every row is an alias:
// sonnet, opus and haiku are published bare, while Fable and the "Haiku 4.5" row
// are published as exact ids. Passing a stored exact id straight through pins the
// session to a model claude has since superseded, so an operator who picked a
// family once would have to reopen the model menu after every claude update. The
// operator's decision is that a pyry session follows the latest model of its
// family.
//
// THE DAEMON'S RULE THAT MODEL VALUES ARE OPAQUE STILL HOLDS, and this is not the
// exception that starts eroding it. Model values are never parsed for matching,
// indexing or keying anywhere in this daemon — the menu highlights by exact
// equality, internal/relay's validModel is a shape check rather than a
// vocabulary, and cmd/pyry's validateModelVocabulary compares against the
// published list. This function parses in exactly ONE place and only to produce
// the string handed OUTBOUND to claude, at the two sinks named below. Do not
// generalise it into a matcher, a family key or a normaliser: the stored value
// stays as picked precisely so those three consumers keep working.
//
// Two callers, and they are the complete set:
//
//   - claudeSettingsArgs, for the --model element of a spawn argv.
//   - Pool.deliverSettingsInBand, for the model a live set_model control request
//     names.
//
// They cannot disagree. On the in-band path Pool.UpdateSettings has already
// assigned the frame's model into the merged settings both sites read from, so
// the live child and the argv installed for the next spawn always name the same
// model.
//
// The rule, deliberately narrow: after any trailing variant group is split off,
// the base must divide on "-" into "claude", a family segment of letters, and at
// least one further segment, with every remaining segment all-digits. Everything
// else is returned unchanged. That refuses the legacy generation-first spelling
// (claude-3-5-sonnet-20241022), whose family sits in no fixed position, and it
// refuses a value with no version segment at all (claude-fable), which is already
// as close to an alias as this rule could make it.
//
// SECURITY. The output reaches an argv token and a live child's stdin, so it must
// not weaken either property internal/relay's validModel rests those sinks on —
// and it must do so WITHOUT assuming validModel ran, because it did not on every
// path here: settingsFromEntry copies the on-disk model with no shape check, and
// the operator's own --model in the bootstrap template is never validated. Both
// properties are structural:
//
//   - Every output byte is an input byte. The rewrite returns a substring of the
//     base joined to a suffix of the input, so no shell metacharacter,
//     whitespace, control byte or byte >= 0x80 can appear that the input did not
//     already carry, and the identity arm returns the input itself.
//   - A REWRITTEN value is strictly narrower than what validModel accepts: the
//     emitted base is non-empty and all ASCII letters, so its first byte can
//     never be "-" and cannot pose as a claude flag, and the group — when present
//     — is balanced, non-empty, unnested and drawn from the closed class, because
//     those are the conditions that had to hold for it to be split off at all.
//
// Length never grows, so an upstream bound on the input still bounds the output.
// TestFamilyAlias_RewrittenOutputIsNarrowerThanItsInput walks all 256 byte values
// through three positions rather than leaving those two claims as prose.
//
// A non-empty input always yields a non-empty output, which claudeSettingsArgs
// depends on and cannot state for itself: it guards on a non-empty stored model
// and appends this result as the element after --model.
func familyAlias(model string) string {
	base, group := splitVariantGroup(model)
	if base == "" {
		return model
	}
	parts := strings.Split(base, "-")
	if len(parts) < 3 || parts[0] != "claude" {
		return model
	}
	if !modelLetters(parts[1]) {
		return model
	}
	for _, segment := range parts[2:] {
		if !modelDigits(segment) {
			return model
		}
	}
	return parts[1] + group
}

// splitVariantGroup divides m into its base and its trailing variant group,
// returning the group WITH its brackets so a caller reassembles by concatenation
// and never rebuilds the punctuation. A value carrying no group returns (m, "");
// one whose brackets are not a well-formed trailing group returns ("", ""), which
// familyAlias reads as "not a shape this rule touches".
//
// Three conditions on the FIRST "[", and between them they rule out a nested
// group, a second group and a suffix trailing the group without any of the three
// needing a check of its own: the value's LAST byte closes the group, the
// interior is non-empty, and every interior byte is in [A-Za-z0-9._-] — which
// excludes both brackets, so nothing inside a group can open or close another.
//
// This MIRRORS internal/contextwindow's variantBase and internal/relay's
// validModel, and deliberately shares neither. Those two guard different things —
// one decides whether two claude-authored strings name one model, the other
// admits a phone-supplied value — and internal/sessions may import neither
// anyway. Sharing the rule would make relaxing any one package's a silent change
// to the others' threat.
func splitVariantGroup(m string) (base, group string) {
	i := strings.IndexByte(m, '[')
	if i < 0 {
		return m, "" // no group: the whole value is the base
	}
	if m[len(m)-1] != ']' {
		return "", "" // the group is not the value's final element
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

// modelLetters reports whether s is non-empty and entirely ASCII letters — the
// class familyAlias demands of a family segment, and the whole of what makes a
// rewritten value's first byte alphanumeric. Non-empty is part of the class, not
// a separate check: an empty segment (claude--5) has no family to name.
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

// modelDigits reports whether s is non-empty and entirely ASCII digits — the
// class familyAlias demands of every segment after the family. Requiring it of
// ALL of them, rather than of the first, is what keeps the rule from rewriting a
// value whose tail carries something other than a version or a date.
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

// modelGroupByte reports whether c is in [A-Za-z0-9._-] — splitVariantGroup's
// closed byte class for a variant group's interior, and the ONE place it is
// written down. Naming it separately keeps "a group admits nothing a model id
// would not" a property of the code rather than of two lists kept in step by
// hand, which is the reason internal/relay factors out modelWordByte and
// internal/contextwindow variantWordByte.
func modelGroupByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-'
}
