package main

import (
	"strings"
)

// pyryFlagBools are pyry-specific boolean flags. Recognised by their exact
// name (with or without a leading -- and with or without =value).
var pyryFlagBools = map[string]bool{
	"pyry-resume":      true,
	"pyry-verbose":     true,
	"pyry-auto-update": true,
}

// pyryFlagValues are pyry-specific flags that take a value. The value can
// be glued (`-pyry-claude=/path`) or in the next arg (`-pyry-claude /path`).
var pyryFlagValues = map[string]bool{
	"pyry-claude":              true,
	"pyry-codex":               true,
	"pyry-workdir":             true,
	"pyry-socket":              true,
	"pyry-name":                true,
	"pyry-idle-timeout":        true,
	"pyry-active-cap":          true,
	"pyry-conv-sweep-interval": true,
	"pyry-wrapup-deadline":     true,
	"pyry-relay":               true,
	"pyry-read-folder":         true,
	claudeAccountFlagName:      true,
	claudeAccountOpCLIFlagName: true,
}

// splitArgs walks args left-to-right and partitions them into pyry's own
// flags and the rest (forwarded to claude). The split rules are:
//
//   - "--" is an explicit separator: everything before it is pyry's, every-
//     thing after is claude's.
//   - Args matching a known pyry-* flag pattern (with or without a value) are
//     pyry's. Boolean flags consume only themselves; value flags also consume
//     the next arg if no `=value` was glued on.
//   - The first arg that isn't a recognised pyry flag (and isn't "--") tips
//     into claude territory: it and everything after go to claude.
//
// This means pyry-* flags must come BEFORE any claude arguments — same
// convention as `sudo`, `time`, `xargs`. Use "--" if you need to mix.
func splitArgs(args []string) (pyryArgs, claudeArgs []string) {
	i := 0
	for i < len(args) {
		a := args[i]

		if a == "--" {
			claudeArgs = append(claudeArgs, args[i+1:]...)
			return
		}

		name, _, hasVal := parseFlagSyntax(a)

		if pyryFlagBools[name] {
			pyryArgs = append(pyryArgs, a)
			i++
			continue
		}
		if pyryFlagValues[name] {
			pyryArgs = append(pyryArgs, a)
			if !hasVal && i+1 < len(args) {
				pyryArgs = append(pyryArgs, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}

		// Not a pyry flag — everything from here goes to claude.
		claudeArgs = append(claudeArgs, args[i:]...)
		return
	}
	return
}

// clientPyryValueFlags lists the -pyry-* flags every control client accepts.
// Both take a value (string). Walk-based extraction needs this map so it can
// decide whether to consume the next token as the value (for the
// space-separated form: `-pyry-name elli`).
var clientPyryValueFlags = map[string]bool{
	"pyry-name":   true,
	"pyry-socket": true,
}

// splitClientFlags peels recognised -pyry-name / -pyry-socket tokens off the
// front of args and returns them as pyryArgs, leaving everything else in
// rest verbatim. Stops at the first non-pyry-* token: subsequent -pyry-*
// tokens are not extracted. Mirrors splitArgs's shape; differs only in the
// recognised flag set.
//
// Both `-pyry-name=elli` and `-pyry-name elli` forms are supported, as are
// the `-` and `--` dash prefixes (parseFlagSyntax normalises both).
//
// `--` is treated as a verb-side token: it and everything after go into
// rest. The verb's own FlagSet is the one that should interpret `--`.
func splitClientFlags(args []string) (pyryArgs, rest []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			return
		}
		name, _, hasVal := parseFlagSyntax(a)
		if !clientPyryValueFlags[name] {
			rest = append(rest, args[i:]...)
			return
		}
		pyryArgs = append(pyryArgs, a)
		if !hasVal && i+1 < len(args) {
			pyryArgs = append(pyryArgs, args[i+1])
			i += 2
			continue
		}
		i++
	}
	return
}

// parseFlagSyntax extracts the flag name from a "-foo", "--foo", "-foo=bar",
// or "--foo=bar" arg. Returns (name, value, hasValue). For non-flag args
// (e.g. "summarize this") returns ("", "", false).
func parseFlagSyntax(a string) (name, value string, hasValue bool) {
	if !strings.HasPrefix(a, "-") {
		return "", "", false
	}
	a = strings.TrimLeft(a, "-")
	if eq := strings.IndexByte(a, '='); eq >= 0 {
		return a[:eq], a[eq+1:], true
	}
	return a, "", false
}
