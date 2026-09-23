package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/attachments"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// maxAttachFileBytes bounds the size of one file this verb will read off the
// host and file as an attachment. Mirrors internal/attachments' own
// per-upload ceiling for the inbound leg, so a file cannot arrive by one route
// that the other would have refused.
//
// Unexported and untyped, following that ceiling's own reasons: it is RECEIVER
// POLICY, learned by being refused rather than published, so no number reaches
// docs/protocol-mobile.md (#1751's rule). Declared here rather than imported
// because attachments' constant is unexported, and exporting it to share one
// number is a cross-package change with a wire-documentation consequence this
// slice is not.
const maxAttachFileBytes = 16 << 20 // 16 MiB

// Every refusal this file produces is one of these fixed sentences. They are
// package-level vars rather than inline constructions so that "the reason is
// static" is structurally evident rather than a property a reader has to
// re-verify at each return.
//
// STATIC IS A SECURITY PROPERTY HERE, not tidiness. The reason travels to the
// wire verbatim through handleAttachFile, and a caller may log it. A host path
// must never reach a log, and neither must its leaf: docs/protocol-mobile.md
// § Attachments bans logging a filename for a privacy reason that sanitising
// does not lift, and a full path is strictly worse. So no reason below
// interpolates the requested path, its filename, the workspace, the session id
// or a wrapped filesystem error — the last being the easy mistake, since
// attachments.Store's rename failure is an *os.LinkError whose Error() prints
// its destination and therefore the sanitised filename.
//
// They stay ACTIONABLE despite carrying no content, which is what an explicit
// tool call buys over a daemon-side sweep: claude reads "outside this
// conversation's workspace", writes the file into the workspace, and calls
// again.
var (
	errAttachNoSession   = errors.New("no live session with that id")
	errAttachNoConv      = errors.New("that session is not bound to a conversation")
	errAttachNoWorkspace = errors.New("that conversation has no recorded workspace")
	errAttachUnresolved  = errors.New("that conversation's workspace cannot be resolved")
	errAttachNoFile      = errors.New("no readable file at that path")
	errAttachOutside     = errors.New("the path is outside this conversation's workspace")
	errAttachNotRegular  = errors.New("the path does not name a regular file")
	errAttachChanged     = errors.New("the file changed between being checked and being read")
	errAttachTooLarge    = errors.New("the file is too large to attach")
	errAttachStoreFailed = errors.New("storing the file failed")
)

// fileAttacher builds the dependency control.SetFileAttacher installs: given
// the CALLING session's id and a path claude named, it files that file under
// that session's conversation and returns the minted attachment id.
//
// The destination is derived from the session and nothing else. It is not read
// from the request — a caller naming its own destination could file into a
// conversation it has no part in — and it is not read from the follow-active
// cursor, which is stamped at enqueue by the session router and therefore names
// whichever chat the operator last messaged, so a file produced by a BACKGROUND
// conversation's claude would land in the wrong thread. #2143 settled the same
// rule for uploads: the destination travels per transfer, not through the
// cursor.
//
// live is a one-line adapter over sessions.Pool.Lookup at the call site rather
// than the pool itself. It exists for a testing need, not preemptively:
// proving the two-live-sessions destination rule against a real *sessions.Pool
// would mean spawning claude children. Keeping it a func also stops
// *sessions.Session leaking into a seam that has no use for one — liveness is
// the entire question being asked.
//
// announce tells paired clients the file now exists (#2166), so a file claude
// produced reaches a client as something rather than as nothing at all. A bare
// func for the reason live is one, and for the reason settingsUpdaterAdapter
// is: the value crossing out of the relay leg stays a
// primitive-shaped closure, so this seam takes on no relay type.
//
// IT MAY BE NIL, and the nil is not defensive padding — it is the
// SetApprovalSurfacer(nil) shape rather than the always-installed approval
// registry's. The hook is absent exactly when the relay leg is: startRelay
// returns before any manager exists when no URL is configured, so that daemon
// has nobody to announce to. A nil hook stores the file and answers with the
// minted id; it never turns a successful store into a refusal.
//
// log may be nil (tests, and any caller that has no logger yet); nothing here
// requires one.
func fileAttacher(
	convReg *conversations.Registry,
	live func(sessions.SessionID) error,
	instanceDir string,
	announce func(conversationID, attachmentID, filename string),
	log *slog.Logger,
) func(sessionID, path string) (string, error) {
	return func(sessionID, path string) (string, error) {
		// The empty id is refused rather than defaulted, and this is the guard
		// that matters even though control.handleAttachFile has one too. Both
		// seams below treat "" as a wildcard: sessions.Pool.Lookup("") resolves
		// to the BOOTSTRAP session with a nil error, and a scan keyed on
		// CurrentSessionID matches an UNBOUND conversation, whose binding is
		// exactly the empty string. Either would file claude's bytes under a
		// conversation that never asked for them.
		if sessionID == "" {
			return "", errAttachNoSession
		}
		if err := live(sessions.SessionID(sessionID)); err != nil {
			return "", errAttachNoSession
		}
		conv, ok := conversationForCurrentSession(convReg, sessionID)
		if !ok {
			return "", errAttachNoConv
		}
		// Read at call time, not captured at wiring time: change_workspace
		// updates this row, and the confinement root must be the workspace the
		// conversation records NOW.
		if conv.Cwd == "" {
			return "", errAttachNoWorkspace
		}

		resolved, checked, err := confineFile(conv.Cwd, path)
		if err != nil {
			return "", err
		}
		data, err := readChecked(resolved, checked, maxAttachFileBytes)
		if err != nil {
			return "", err
		}

		// Minted here, never taken from the request: a caller-chosen id would
		// let it address — and overwrite — storage it did not create. crypto/rand
		// UUIDv4, lowercase, which is the shape attachments validates against
		// and the shape that keeps the id-to-directory mapping injective on a
		// case-insensitive filesystem.
		id, err := conversations.NewID()
		if err != nil {
			return "", errAttachStoreFailed
		}
		dir, err := attachments.EnsureDir(instanceDir, conv.ID, string(id))
		if err != nil {
			// Deliberately dropped rather than wrapped: EnsureDir's message
			// names the instance directory, a host path.
			return "", errAttachStoreFailed
		}
		// Store sanitises the claude-authored name into one path component
		// itself, so the leaf can never spell a directory. Its error is dropped
		// for a sharper reason than EnsureDir's: the rename path returns an
		// *os.LinkError whose Error() prints the destination, filename included.
		//
		// The RETURNED PATH is kept rather than discarded, and that is what makes
		// the announcement below name the file that was actually written. Its
		// leaf is Store's own SanitizeFilename output, which is also what the
		// retrieval leg publishes for this id — one string, not two derivations
		// of one. filepath.Base recovers it exactly: the sanitiser's allowlist
		// rewrites every separator, so Join cleans nothing away.
		stored, err := attachments.Store(dir, filepath.Base(resolved), data)
		if err != nil {
			return "", errAttachStoreFailed
		}

		// The only log line on any path, and the only two values in this whole
		// exchange that are safe to log: one daemon-minted id and one
		// shape-checked id. No path, no filename, no size. Refusals are not
		// logged at all — the reason goes back to the caller, which is the only
		// party that needs it, and that is also what keeps ten refusal branches
		// from becoming ten log calls.
		if log != nil {
			log.Info("control: filed a host file as an attachment",
				"conversation_id", string(conv.ID), "attachment_id", string(id))
		}

		// Tell paired clients the file exists (#2166). LAST, and only on the
		// success path: every refusal above has already returned, so a refused
		// store announces nothing. Its result is deliberately not consulted — a
		// failed push must not turn a successful store into a refusal, because
		// the bytes are on disk and the id is real whether or not anyone heard.
		if announce != nil {
			announce(string(conv.ID), string(id), filepath.Base(stored))
		}
		return string(id), nil
	}
}

// conversationForCurrentSession returns the conversation currently bound to
// sid, and whether one was found.
//
// CurrentSessionID only. conversationForSession, the neighbouring scan, also
// matches the append-only SessionHistory because it answers "which conversation
// owned this session, ever"; this one answers "where do this session's bytes
// belong now", and a retired session id must not still be able to file into the
// conversation that moved on from it.
//
// Duplicated here rather than added to the registry as a by-session-id read:
// PROJECT-MEMORY's "Resist over-DRY on duplicated registry primitives", the
// same reason conversationForSession records for its own copy. This one needs
// the ROW, for Cwd, where that one needs only the id.
//
// The empty-sid guard is not defensive tidying — an unbound conversation
// carries CurrentSessionID == "", so without it a stray empty lookup matches
// the first unbound row. fileAttacher guards it too; this one is what protects
// the scan from any future caller.
//
// Race-safe against a concurrent RebindSession for List's documented reason:
// it copies the slice header under the registry mutex, and a concurrent append
// writes at or above len, never into an index the captured read touches.
func conversationForCurrentSession(convReg *conversations.Registry, sid string) (conversations.Conversation, bool) {
	if sid == "" {
		return conversations.Conversation{}, false
	}
	for _, c := range convReg.List() {
		if c.CurrentSessionID == sid {
			return c, true
		}
	}
	return conversations.Conversation{}, false
}

// confineFile resolves path against root and confines it there, returning the
// symlink-free absolute path and the FileInfo that passed the check. The
// FileInfo is returned rather than discarded because it is half of readChecked's
// guarantee: the descriptor that gets opened is compared against THIS stat.
//
// Splitting the check from the read is what makes the check-then-use window
// testable: a test can perform a real swap between the two calls instead of
// racing for one.
//
// The recipe, and why each step is the one chosen:
//
//   - Both root and target go through agentrun.ResolveWorkdir — Abs,
//     EvalSymlinks, then on-disk case folding. The case fold is not cosmetic:
//     APFS is case-insensitive by default, so two differently-cased spellings
//     of one directory compare unequal textually while naming the same place.
//     Canonicalising only one side would be unsound in both directions.
//   - A relative path joins against the canonical ROOT, never the daemon's
//     process directory — which is what a bare filepath.Abs would use, and is
//     an escape by default rather than by attack. filepath.Join cleans ".."
//     while doing so, and that hides nothing: the boundary test runs after it.
//   - Containment is withinDir, the filepath.Rel test, never a prefix compare —
//     the latter reads /home/userfoo as inside /home/user (#118/#221).
//   - resolveSpawnDir is deliberately NOT reused despite validating a workspace:
//     it is a spawn-site validator with side effects a read path must not have,
//     creating missing directories and writing ~/.claude.json.
//   - The regular-file check runs BEFORE any open, and the ordering is
//     load-bearing rather than stylistic: opening a FIFO blocks until a writer
//     appears, and no deadline on the control conn interrupts a blocking open.
//
// Every refusal is one of the static sentences above; no filesystem error is
// wrapped, since agentrun.ResolveWorkdir's names the path it failed on.
func confineFile(root, path string) (string, os.FileInfo, error) {
	canonicalRoot, err := agentrun.ResolveWorkdir(root)
	if err != nil {
		return "", nil, errAttachUnresolved
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(canonicalRoot, path)
	}
	resolved, err := agentrun.ResolveWorkdir(path)
	if err != nil {
		return "", nil, errAttachNoFile
	}
	if !withinDir(canonicalRoot, resolved) {
		return "", nil, errAttachOutside
	}
	checked, err := os.Stat(resolved)
	if err != nil {
		return "", nil, errAttachNoFile
	}
	if !checked.Mode().IsRegular() {
		return "", nil, errAttachNotRegular
	}
	return resolved, checked, nil
}

// readChecked opens resolved and returns its bytes, but only once it has
// confirmed that the file it opened is the file confineFile checked.
//
// THE FILE THAT IS READ IS THE FILE THAT WAS CHECKED. os.SameFile compares the
// checked FileInfo against the descriptor's own fstat, so the check cannot be
// won by swapping the path between confineFile and here. It closes strictly
// more than a final-component guard would: if ANY component of the resolved
// chain is replaced after the check — the leaf, or a parent turned into a
// symlink — the opened inode differs and the read is refused. The only way to
// pass is to land on the inode that passed, which is the file that passed.
//
// Two open flags back that up with different fabric, both subordinate to
// SameFile rather than replacing it:
//
//   - O_NOFOLLOW makes a leaf swapped to a symlink a KERNEL-side refusal rather
//     than a userspace comparison. It cannot cause a false refusal: resolved is
//     EvalSymlinks' output and is symlink-free by construction, so a path the
//     caller reached through a symlink still opens.
//   - O_NONBLOCK closes a hang rather than a leak. confineFile already refuses
//     a FIFO, but a swap after that check would reach open(2) on one, which
//     blocks until a writer appears — wedging the handler goroutine with no
//     deadline able to interrupt it. With the flag the open returns at once and
//     the regular-file check below refuses it. A no-op on a regular file.
//
// The bound is applied twice, and takes maxBytes as a parameter so both rungs
// are exercisable without materialising 16 MiB: from the descriptor's own
// fstat BEFORE any byte is read, then again on what was read, so a file that
// grows between the two is refused rather than truncated silently.
func readChecked(resolved string, checked os.FileInfo, maxBytes int64) ([]byte, error) {
	f, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errAttachNoFile
	}
	defer func() { _ = f.Close() }()

	opened, err := f.Stat()
	if err != nil {
		return nil, errAttachNoFile
	}
	if !os.SameFile(checked, opened) {
		return nil, errAttachChanged
	}
	if !opened.Mode().IsRegular() {
		return nil, errAttachNotRegular
	}
	if opened.Size() > maxBytes {
		return nil, errAttachTooLarge
	}

	// LimitReader at the bound plus one: reading exactly maxBytes cannot tell a
	// file at the bound from one that grew past it.
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, errAttachNoFile
	}
	if int64(len(data)) > maxBytes {
		return nil, errAttachTooLarge
	}
	return data, nil
}
