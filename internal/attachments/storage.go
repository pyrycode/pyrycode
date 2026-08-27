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

// ErrWriteFailed reports that an attachment's verified bytes could not be
// written into the directory they were destined for. Every refusal wraps it
// alongside the underlying filesystem error, so errors.Is reaches this sentinel
// for #1744's wire mapping and the fs error for the operator.
//
// It is deliberately NOT named ErrStorageFailed. All three of this file's
// sentinels map to attachment.storage_failed at #1744, so a Go name matching the
// wire code would falsely suggest this one is THE storage-failure sentinel. It
// names what failed — the write — not the code it becomes.
//
// Like ErrInvalidID and ErrNotContained, and unlike the six latching sentinels
// in accumulator.go, it carries no discard semantics: there is no accumulator
// state here to latch or drop.
var ErrWriteFailed = errors.New("attachments: attachment file could not be written")

// Store writes verified attachment bytes into dir under a name derived from the
// client-supplied filename, atomically, and returns the path it wrote. The
// bytes are expected to have already matched their declared size and digest —
// that comparison is Accumulator.Assemble's and is not repeated here.
//
// PRECONDITION: dir must be a path EnsureDir returned. Store TRUSTS it and
// resolves nothing — every containment guarantee already happened above
// EnsureDir, and re-deriving the path here would fork the check EnsureDir exists
// to own. It would also break the same-filename property below, since the
// per-attachment-id component of that directory is the entire reason two
// attachments in one conversation can carry one client filename.
//
// The returned path is dir joined with SanitizeFilename's single-component form
// of filename — which is NOT unique across conversations or attachments, since
// distinct client names collide and every unusable name answers one fallback.
// It is safe as a leaf beneath dir precisely because dir is keyed by attachment
// id; nothing may name a stored file by that component alone.
//
// Idempotent: a second call with the same dir and filename overwrites with no
// error, so a phone that drops mid-upload and re-sends is not a storage failure.
// There is deliberately no O_EXCL and no existence pre-check — either would turn
// an ordinary reconnect into attachment.storage_failed at #1744, which the wire
// contract marks retryable and which the client would then hot-loop against.
//
// Safe for concurrent use on distinct dir values, which is the only way #1744
// can reach it. Two concurrent calls sharing a dir and a filename are
// last-writer-wins, atomically: each writes its own uniquely named temp file and
// the renames serialise in the kernel, so a reader sees one complete file or the
// other, never a mixture.
//
// On any failure it returns ("", err) and leaves no temporary file behind.
//
// LOGGING OBLIGATION on the consumer. The error text names dir, never the
// sanitised filename — except on the rename path, where os.Rename returns an
// *os.LinkError whose Error() prints its destination and therefore the sanitised
// component. docs/protocol-mobile.md § Attachments bans logging a filename for a
// privacy reason that sanitising does not lift, so #1744 must map this to
// attachment.storage_failed's STATIC wire message and, if it logs, log the
// sentinel and the ids rather than the error text. This is ErrNotContained's
// "safe only because they never reach the wire" note plus that one clause.
func Store(dir, filename string, data []byte) (string, error) {
	name := SanitizeFilename(filename)
	path := filepath.Join(dir, name)

	// keys.writeStaticKey's recipe, step for step; conversations and devices
	// hand-roll the same one. Copied rather than extracted: ten packages carry
	// it today and none imports another's, so factoring it out is a
	// cross-package refactor this slice is not.
	//
	// The temp file goes in dir ITSELF, which is what makes the rename below
	// intra-filesystem and therefore atomic. Its dot-prefixed pattern cannot
	// collide with a stored attachment: SanitizeFilename never returns a
	// component beginning with '.', so ".attachment-*.tmp" is unreachable as an
	// attachment's name — which is what makes "the directory holds exactly this
	// one file" a clean assertion rather than a fragile one.
	//
	// The format string names dir and NOTHING else on every path below. path and
	// name embed the sanitised client filename, and sanitising removes the
	// log-injection half of the hazard but not the independent privacy reason
	// docs/protocol-mobile.md § Attachments bans logging a filename for. dir is
	// built entirely from two canonical-shape-checked ids and carries no client
	// text. Same discipline ErrDigestMismatch follows carrying the COMPUTED
	// digest and never the declared one.
	f, err := os.CreateTemp(dir, ".attachment-*.tmp")
	if err != nil {
		return "", fmt.Errorf("%w: create temp in %q: %w", ErrWriteFailed, dir, err)
	}
	tmp := f.Name()
	// What makes "no temporary file is left behind" hold on every failure path.
	// After a successful rename it fails ENOENT and that error is deliberately
	// discarded.
	defer func() { _ = os.Remove(tmp) }()

	// Belt-and-suspenders, and measured as such: os.CreateTemp already opens at
	// 0600 and umask can only clear bits, so dropping this line reddens nothing.
	// It stays because it is the house recipe and because it makes the mode
	// explicit rather than inherited from a default invisible at the call site.
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("%w: chmod temp in %q: %w", ErrWriteFailed, dir, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("%w: write temp in %q: %w", ErrWriteFailed, dir, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("%w: fsync temp in %q: %w", ErrWriteFailed, dir, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("%w: close temp in %q: %w", ErrWriteFailed, dir, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("%w: rename into %q: %w", ErrWriteFailed, dir, err)
	}
	return path, nil
}

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
