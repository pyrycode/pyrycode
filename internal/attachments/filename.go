package attachments

import (
	"strings"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// fallbackFilename is the component returned when nothing in the input survived
// the allowlist. Fixed rather than derived or randomised, so the answer is the
// same every time and a test can pin it; the price is that it is the most
// collision-prone component this function produces, which is part of why the
// caller must not name a stored file by the component alone.
const fallbackFilename = "attachment"

// SanitizeFilename turns a client-supplied filename into one safe path
// component. It is TOTAL — every string has a safe answer, so there is nothing
// to refuse and no error to return — and PURE: it touches no filesystem,
// constructs no path, holds no state and takes no lock, so it is safe to call
// concurrently from any goroutine. That is worth saying next to Accumulator,
// which carries the opposite constraint and is documented as fed serially by
// one session's appFrameWorker goroutine.
//
// The returned value is exactly one path component: never empty, never "." or
// "..", never containing '/', never containing a control character, never
// beginning with '.', valid UTF-8, and at most
// protocol.MaxAttachmentFilenameBytes BYTES measured by len — bytes, not
// characters. Every rune outside the allowlist a-z A-Z 0-9 _ . - becomes '_',
// which is cmd/pyry's sanitizeName allowlist byte for byte; the shape is that
// function's, which cannot be imported (unexported, package main).
//
// What it does NOT return is a UNIQUE name, and not an identifier. Distinct
// client names collide: "a/b" and "a_b" both answer "a_b", every unusable name
// answers fallbackFilename, and a case-insensitive host (APFS by default) folds
// "Report.pdf" into "report.pdf". A caller that names a stored file by this
// component alone lets one upload silently overwrite another — storage keys by
// the canonical-shape-checked attachment_id and treats this as display material
// at most.
//
// Do NOT call this on AttachmentChunkPayload.AttachmentID. Sanitising an id
// changes which attachment is addressed and a mangled id passes silently where
// a check would reject; the id's contract is a canonical-shape check on
// conversations.ValidID's precedent, per AttachmentChunkPayload's SECURITY
// block.
//
// This is also not the enforcement point for the wire bound. An over-long
// filename is truncated here, never refused — protocol's attachment bounds are
// producer-side contracts with no validator, and refusing the frame outright,
// if the daemon ever wants to, belongs to the admission pass.
//
// The never-log rule survives this function. Sanitising removes the
// LOG-INJECTION half of the hazard — the output can forge no log line — but
// docs/protocol-mobile.md § Attachments bans logging a filename for a second,
// independent reason: a filename is often private in itself. The output is no
// more loggable than the input.
func SanitizeFilename(name string) string {
	var b strings.Builder
	kept := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			b.WriteRune(r)
			kept = true
		default:
			b.WriteRune('_')
		}
	}

	// Derived from the INPUT rather than from the built result, which is the
	// one place this deliberately departs from sanitizeName's
	// check-the-built-result rule. The output cannot support the distinction:
	// "___" is what both "///" (nothing survived) and the literal client name
	// "___" (everything survived) build, and the second has to come back
	// byte-identical. The flag is stated relative to the allowlist — "some rune
	// passed" — not to any particular allowlist, so widening the allowlist
	// later does not drift its meaning.
	//
	// The empty input needs no arm of its own: it writes no rune, so its flag
	// is false for the same reason "///"'s is.
	if !kept {
		return fallbackFilename
	}

	// Checked on the built result, per sanitizeName's rule, so the
	// postcondition keeps holding if the allowlist above ever changes. The
	// underscore goes in FRONT: a trailing one, which is what sanitizeName
	// gives "." and "..", leaves the result beginning with a dot and therefore
	// hidden. A prefix discharges both hazards with one rule, since a string
	// that does not begin with '.' is neither "." nor "..".
	out := b.String()
	if strings.HasPrefix(out, ".") {
		out = "_" + out
	}

	// Last, and the earlier steps survive it: cutting a suffix introduces no
	// separator and no control character, changes no first byte, and cannot
	// empty a string this long. The order matters the other way round too — a
	// 255-byte name beginning with '.' is 256 bytes after the prefix above, so
	// truncating first would return an over-long component.
	return truncateToBytes(out, protocol.MaxAttachmentFilenameBytes)
}

// truncateToBytes returns s cut to at most maxBytes bytes, dropping whole runes
// so the result is still valid UTF-8. maxBytes must not be negative. The result
// may be "" when maxBytes is smaller than s's first rune; SanitizeFilename
// never reaches that, since it passes protocol.MaxAttachmentFilenameBytes.
//
// Each turn is O(1) and drops at least one byte — DecodeLastRuneInString
// reports size 1 for an invalid trailing byte, so the loop always makes
// progress — which keeps the whole call linear in len(s). A condition that
// recomputed utf8.RuneCountInString each turn would be quadratic, and nothing
// bounds this function's input: a filename rides every chunk of a transfer and
// the wire bound has no validator.
func truncateToBytes(s string, maxBytes int) string {
	for len(s) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
