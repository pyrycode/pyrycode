package attachments

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// ErrInvalidID reports an identifier that is not of canonical shape, whether it
// is the conversation id or the attachment id. Every refusal wraps it naming
// which of the two failed and its value, and callers distinguish it with
// errors.Is rather than by comparing error strings.
//
// ONE sentinel covers both ids on purpose, where ErrUploadTooLarge is a
// deliberate second sentinel beside ErrInvalidDeclaration. That split exists
// because the client's repair differs — re-chunk versus shrink the file. Here
// it does not: neither refusal is retryable and neither is repairable by the
// client, since a non-canonical conversation id is a daemon bug and a
// non-canonical attachment id is a client bug. A caller that wants to know
// which id was bad reads the wrapped message; a caller that wants to branch has
// nothing to branch to.
//
// It is deliberately distinguishable from ErrNotContained, its neighbour below:
// a refusal here means no path was ever built, so nothing on the filesystem was
// touched, while a containment refusal means both ids were canonical and the
// destination they name resolves somewhere else.
var ErrInvalidID = errors.New("attachments: identifier is not of canonical shape")

// ErrNotContained reports a destination whose symlink-resolved path is not the
// path this (conversation id, attachment id) pair maps to beneath the resolved
// instance directory — whether it lands outside the instance directory
// entirely, or inside it under another conversation. Refusals wrap it with the
// expected and the resolved path, and callers distinguish it with errors.Is.
//
// The host paths in that message are for the operator's log and are safe here
// only because they never reach the wire: docs/protocol-mobile.md § Attachments
// makes attachment.storage_failed a STATIC message carrying neither the host
// path nor the underlying filesystem error, and #1744's mapping is what
// enforces that. This package emits no wire code and makes no log call.
var ErrNotContained = errors.New("attachments: attachment directory resolves outside the destination it was built for")

// EnsureDir resolves and creates the directory one attachment of one
// conversation is filed under, returning its symlink-resolved absolute path.
// Writing bytes into it is #1782's; dispatching to it and mapping the two
// sentinels above to wire codes is #1744's.
//
// The layout beneath instanceDir is conversations/<conversation-id>/
// attachments/<attachment-id>, every level created 0o700, matching Registry.Save
// in internal/conversations and in internal/devices. A conversations DIRECTORY
// and the existing conversations.json FILE coexist under one parent without
// colliding. The per-attachment-id component is what lets two attachments in one
// conversation carry the same client-supplied filename.
//
// attachmentID is CLIENT-CHOSEN on the upload leg and conversationID is
// daemon-side, but both become path components here so both are validated here.
// protocol.MaxAttachmentIDBytes is not the defence and does not need to be
// consulted: conversations.ValidID admits 36 characters of lowercase hex and
// dashes at fixed offsets and nothing else, so a passing id contains no '/', no
// '.', no NUL and no "..", cannot spell any path component other than itself,
// and fits the ceiling structurally. Its lowercase-only alphabet is load-bearing
// beyond traversal: it is what keeps the id-to-directory mapping injective on a
// case-insensitive filesystem, which APFS is by default. A replacement shape
// (#1744 publishes the client-visible contract) must keep that property, or two
// distinct ids resolve to one directory on macOS and #1782's writes
// cross-contaminate without any symlink involved.
//
// conversationID is typed and attachmentID is not, deliberately. Both are
// canonical UUIDv4 strings, so a caller that swaps them produces a VALID but
// wrong path that no validator can catch; typing one of the two makes the swap a
// compile error at every call site. There is no AttachmentID type to use for the
// other — protocol.AttachmentChunkPayload's field is a plain string, and minting
// a type for it is #1744's contract to make.
//
// Idempotent: a second call for a pair whose directory already exists returns
// the same path and no error, so a re-delivered upload is not a storage failure.
// Safe for concurrent use by construction — no state, no lock, and MkdirAll
// races benignly against itself.
//
// The path returned is the EvalSymlinks output rather than the textually built
// one. They are provably equal by the time it returns, and returning the
// resolved one is the honest expression of what the caller is promised.
func EnsureDir(instanceDir string, conversationID conversations.ConversationID, attachmentID string) (string, error) {
	// Both ids are validated BEFORE the filesystem is touched, which is what
	// makes "the instance directory is left exactly as it was" true in its
	// strongest form: on an id refusal not even the anchor is created.
	if !conversations.ValidID(string(conversationID)) {
		return "", fmt.Errorf("%w: conversation id %q", ErrInvalidID, string(conversationID))
	}
	if !conversations.ValidID(attachmentID) {
		return "", fmt.Errorf("%w: attachment id %q", ErrInvalidID, attachmentID)
	}

	abs, err := filepath.Abs(instanceDir)
	if err != nil {
		return "", fmt.Errorf("resolve instance directory %q: %w", instanceDir, err)
	}
	// An absent instance directory is created rather than refused: every other
	// registry under it creates it lazily on first write, so attachment storage
	// must not depend on whether some other subsystem happened to persist
	// first. It is created before it is resolved because EvalSymlinks fails on a
	// path that does not exist, and the anchor's resolution is what the
	// containment check compares against.
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("create instance directory %q: %w", abs, err)
	}

	// The anchor, and the only path resolved from the outside in. Anchoring on
	// the resolved CONVERSATION directory instead would defeat the whole check:
	// a conversation directory symlinked out of the instance directory resolves
	// first, and everything beneath it is then trivially contained in it.
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve instance directory %q: %w", abs, err)
	}

	// Built TEXTUALLY beneath the resolved root; nothing under root is resolved
	// to build it. This is the value the resolved destination must equal.
	want := filepath.Join(root, "conversations", string(conversationID), "attachments", attachmentID)

	// Split want into the longest leading ancestor that exists on disk and the
	// not-yet-existing suffix, exactly as confineWorkdirToHomeCreating does. The
	// probe is os.Lstat, NOT os.Stat, so a symlink counts as existing and is
	// resolved below rather than stepped over.
	//
	// Measured, not assumed: swapping in os.Stat reddens nothing in this
	// package's suite, because it is not solely load-bearing HERE the way it is
	// in the precedent. Stat reports the TARGET's existence, which differs from
	// Lstat only for a DANGLING link — for a link whose target exists both stop
	// the walk at the same ancestor, and the equality check below refuses either
	// way. On a dangling one Stat steps over the link, so check #1 passes
	// vacuously, and it is MkdirAll's own EEXIST on the symlink that refuses
	// instead — the same wrapped-OS-error class the Lstat build answers by
	// failing to resolve it. Lstat stays because that refusal should come from
	// the resolution step rather than incidentally from the creation step, and
	// because the precedent's weaker filepath.Rel containment test does depend
	// on it: anything that ever relaxes the equality below makes this load-
	// bearing again.
	existing := want
	var rest string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break // reached the filesystem root; guard against looping
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}

	existingReal, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve attachment directory %q: %w", existing, err)
	}
	candidate := existingReal
	if rest != "" {
		candidate = filepath.Join(existingReal, rest)
	}

	// Containment check #1, BEFORE anything is created — which is the whole
	// point of the ordering, and the difference between "refused" and "refused
	// before creating anything beneath the offending symlink".
	//
	// The comparison is equality against want, not a filepath.Rel-style "is it
	// under root" test like withinDir's. Equality is strictly stronger — a path
	// equal to one built beneath root is trivially beneath root — and it is what
	// refuses a conversation directory symlinked at a SIBLING conversation,
	// which stays inside the instance directory and passes any containment test.
	// Both sides are clean absolute paths (Abs, EvalSymlinks and Join all
	// clean), so == is well defined. That is also why agentrun.ResolveWorkdir is
	// not a substitute: it case-canonicalises, which this comparison must not,
	// and it requires the path to already exist.
	if candidate != want {
		return "", fmt.Errorf("%w: %q resolves to %q", ErrNotContained, want, candidate)
	}

	// Unconditional, where the precedent guards its MkdirAll with `rest != ""`.
	// MkdirAll on an existing directory is a no-op returning nil, so the guard
	// buys nothing; dropping it means a want that exists as a FILE is refused by
	// MkdirAll's own error rather than sliding through.
	if err := os.MkdirAll(want, 0o700); err != nil {
		return "", fmt.Errorf("create attachment directory %q: %w", want, err)
	}

	// Containment check #2 (post-creation): the path now exists, so EvalSymlinks
	// canonicalises it fully. It catches a destination that became escaping
	// during creation and yields the path to return. The residual window between
	// the two checks is the same one confineWorkdirToHomeCreating accepts, and
	// is bounded the same way: exploiting it needs write access to the daemon's
	// own 0o700 state directory, and anyone holding that can already rewrite
	// devices.json — strictly worse than an empty misplaced directory.
	final, err := filepath.EvalSymlinks(want)
	if err != nil {
		return "", fmt.Errorf("resolve attachment directory %q: %w", want, err)
	}
	if final != want {
		return "", fmt.Errorf("%w: %q resolves to %q", ErrNotContained, want, final)
	}
	return final, nil
}
