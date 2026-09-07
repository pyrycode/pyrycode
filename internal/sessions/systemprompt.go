package sessions

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// systemPromptText is the text every interactive claude is spawned with, via
// --append-system-prompt-file. APPEND, never replace: the replacing form would
// discard claude's own system prompt, which nobody here intends to discard.
//
// It exists because an assistant reasons about the surface its words land on —
// whether output renders as markdown, whether the operator can interrupt, whether
// there is a terminal at all — and told nothing, it assumes the surface is the
// machine it executes on. Those are different machines. Observed 2026-09-04: a
// markdown rendering fault in the operator's client was diagnosed as Claude
// Code's own renderer, a library "not ours to switch", and a terminal-width
// problem, all of it about the wrong process (#2093).
//
// EVERY SENTENCE IS AN ARCHITECTURE FACT, and that is a constraint on what may be
// added here, not an observation about what is. The text asserts nothing about
// what any client can render or do, because such a claim rots: "this client
// cannot render tables" is false the day the client ships the plugin that renders
// them, and nothing would ever bring the two back into line. Two live instances
// of exactly that cost real time on 2026-09-04. What is stated here is true of
// every client that will ever connect.
//
// TestSystemPromptText_Pinned pins these bytes against an independent
// transcription, which is what makes a future capability claim a visible,
// deliberate diff rather than drift.
//
// The client's own name and version are deliberately absent — the handshake's
// device_name carries a hostname rather than a product name, and the daemon does
// not retain the field at all. That is #2148's, and it is why this ticket ships
// the half that cannot rot.
const systemPromptText = "You are running as a supervised child of the pyry daemon. " +
	"Your replies are not displayed in a terminal: they leave this process as a " +
	"structured stream and are rendered for the operator by a separate client " +
	"application, which may be running on a different machine than the one your " +
	"tools execute on. More than one client can be attached to a session, and " +
	"which one is attached can change while the session runs.\n"

// composeSystemPrompt returns the text a session is spawned with: the constant
// above, plus the conversation's operator-set prompt when it has bytes.
//
// The operator's bytes are APPENDED after a blank-line separator (the constant
// already ends in a newline) and are otherwise verbatim — untrimmed, unescaped,
// unbounded here. #2149's Registry.SetSystemPrompt is the single validating door
// (byte bound + UTF-8 validity); re-validating at this end would put a second
// opinion in a second place.
//
// operator == "" returns the constant BYTE FOR BYTE, with no separator and no
// trailing blank line. That is the contract for both of #2149's no-bytes states —
// an absent prompt and an explicitly-empty one — which the resolver flattens to
// the same empty string, so this function has one predicate rather than three.
//
// There is deliberately no branch that returns the operator's text alone.
// Replacing claude's own system prompt is what --append-system-prompt-file
// exists not to do, and a composition that dropped the constant would satisfy
// every argv assertion in this package while silently discarding what #2093 was
// built to say (#2150).
func composeSystemPrompt(operator string) string {
	if operator == "" {
		return systemPromptText
	}
	return systemPromptText + "\n" + operator
}

// sessionPromptsDir is the per-session prompt directory's name under the daemon
// data dir. A sibling of session-settings/ rather than a tenant of it: that
// directory's *.json contents are documented to count sessions exactly.
const sessionPromptsDir = "session-prompts"

// systemPromptPathFor resolves the absolute path of a system-prompt file, or ""
// when persistence is disabled. Three cases:
//
//   - registryPath == "" — the test-only mode Pool.dataDir reports as "". There
//     is no data dir to write into, so "" is returned and the caller's write
//     mints a random name in os.TempDir. Most of this package's tests build a
//     pool with no RegistryPath.
//   - id == "" — the daemon-scoped bootstrap file, <dataDir>/system-prompt.txt,
//     where dataDir is the absolutised parent of registryPath. The bootstrap is
//     constructed in Pool.New before any conversation exists and can never become
//     a conversation's bound session, so it has no per-conversation text to carry
//     and keeps #2093's fixed name.
//   - id != "" — <dataDir>/session-prompts/<id>.txt, one file per session,
//     because since #2150 the text is NOT identical for every session: it carries
//     the bound conversation's operator prompt. That is what buys the file a
//     per-session lifecycle (Pool.Remove now removes it) where #2093's was
//     written once and removed once.
//
// The directory is created here at 0700 because on a cold start nothing has
// created the data dir yet — saveRegistryLocked has not necessarily run when
// Pool.New writes the bootstrap file.
//
// id is gated on ValidID, and for writeMCPSettings' reason: since it names a
// file, an id carrying a separator or a ".." segment would place the write
// outside the data dir. A warm-start id is decoded straight out of the registry
// with no shape check on that path, so a malformed one is a hard error here,
// matching loadRegistry's "a malformed file is a hard error" posture rather than
// silently falling back to a temp file.
func systemPromptPathFor(registryPath string, id SessionID) (string, error) {
	if registryPath == "" {
		return "", nil
	}
	dataDir, err := filepath.Abs(filepath.Dir(registryPath))
	if err != nil {
		return "", fmt.Errorf("sessions: resolve system prompt dir: %w", err)
	}
	dir := dataDir
	name := "system-prompt.txt"
	if id != "" {
		if !ValidID(string(id)) {
			return "", fmt.Errorf("sessions: system prompt file for session %q: not a canonical session id", id)
		}
		dir = filepath.Join(dataDir, sessionPromptsDir)
		name = string(id) + ".txt"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("sessions: mkdir system prompt dir: %w", err)
	}
	return filepath.Join(dir, name), nil
}

// sessionPromptsDirFor returns the per-session prompt directory under the data
// dir holding registryPath, or "" when persistence is disabled (nothing to
// purge, because nothing was written there).
//
// It exists for the two directory-wide removals that make AC #3's "never
// outlives the daemon" true: Pool.Run's shutdown defer, and Pool.New's purge of
// what a SIGKILL left behind — no defer survives a kill, and these files carry
// operator text into a data dir nothing reaps. The purge is sound at New because
// no session has been materialised at that point.
func sessionPromptsDirFor(registryPath string) string {
	if registryPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(registryPath), sessionPromptsDir)
}

// writeSystemPrompt writes text as the appended system-prompt file for id and
// returns its absolute path — systemPromptPathFor's derivation followed by
// writeSystemPromptFile's write. It is the entry point every CONSTRUCTION site
// uses; a refresh of an already-built session writes writeSystemPromptFile
// directly, against the path frozen into that session's spawnBase.
//
// text is a PARAMETER rather than a read of systemPromptText, and that is the
// join seam #2093 left. #2150 is the ticket that took it: its caller composes
// through composeSystemPrompt, and this function is unchanged in what it does
// with the bytes. #2148 (the client's name and version) inherits the same seam.
//
// The caller — not this helper — removes the file: at session teardown
// (Pool.Remove), at daemon shutdown (Pool.Run's defer), and on every error
// return between the write and its own success.
func writeSystemPrompt(registryPath string, id SessionID, text string) (string, error) {
	final, err := systemPromptPathFor(registryPath, id)
	if err != nil {
		return "", err
	}
	return writeSystemPromptFile(final, text)
}

// writeSystemPromptFile writes text to final, atomically, and returns the path
// written. final == "" means "mint a random name in os.TempDir" — the
// persistence-disabled branch.
//
// It takes a resolved path rather than deriving one, because a session's prompt
// file must stay reachable at the path baked into its spawnBase. A `/clear`
// rotation re-keys a session IN PLACE (Pool.rekeyLocked), so re-deriving from
// the session's CURRENT id after a rotation would write a file no argv names —
// and every later spawn would carry the pre-rotation text.
//
// The write is atomic — scratch file in the target directory, fsync, rename —
// and the mode is os.CreateTemp's 0600, preserved by the rename. Same recipe and
// same reasons as writeMCPSettings: a rename hands a live child either the
// complete old file or the complete new one rather than a truncated prefix, and
// it replaces a symlink at the destination instead of writing through it. Since
// #2150 the 0600 matters for confidentiality as well as integrity — the payload
// is no longer only a public constant, it carries the operator's own text — and
// anyone who could write this file would control text pyry hands claude as a
// system prompt.
func writeSystemPromptFile(final, text string) (string, error) {
	// dir == "" selects os.CreateTemp's own os.TempDir contract; a resolved final
	// puts the scratch file beside it so the rename is same-filesystem.
	var dir string
	if final != "" {
		dir = filepath.Dir(final)
	}

	pattern := "pyry-system-prompt-*.txt"
	if final != "" {
		// Dotted .tmp suffix so a scratch file left by a SIGKILL inside the write
		// window is never mistaken for the prompt file itself. Mirrors
		// writeMCPSettings' ".settings-*.json.tmp".
		pattern = ".system-prompt-*.txt.tmp"
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("sessions: create system prompt tempfile: %w", err)
	}
	tmpName := f.Name()

	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: write system prompt: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: fsync system prompt: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: close system prompt: %w", err)
	}
	if final == "" {
		return tmpName, nil
	}
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: rename system prompt: %w", err)
	}
	return final, nil
}

// conversationPrompt returns the operator-set system prompt of the conversation
// label names, or "" when there are no bytes to append.
//
// It is TOTAL: a nil conversations registry (Config.ConversationsRegistry's own
// doc calls nil the test default, and most of this package's tests build a pool
// that way), an empty label, a label naming no conversation, and #2149's absent
// (nil) prompt all return "" rather than an error. rebindConversation is the
// precedent for the nil-registry no-op, and "a label naming no conversation is
// not an error" is what keeps the bootstrap and any non-conversation session on
// the ordinary path instead of a special case.
//
// It also flattens #2149's tri-state: the absent state and the explicitly-empty
// state both return "", so the spawn site carries ONE predicate — has bytes to
// append — and the tri-state stays in the registry where #2149 put it.
//
// label is the conversation id at both production sites: create_conversation
// passes it into Pool.Mint through sessionMinter.Create, and sessionRouter's
// revive passes it into Pool.Revive.
//
// Concurrency: Registry.Get takes the registry's own mutex and returns a shallow
// copy sharing the stored *string. SetSystemPrompt REPLACES that pointer rather
// than mutating a pointee, so the deref below cannot race a concurrent set.
func (p *Pool) conversationPrompt(label string) string {
	if p.convReg == nil || label == "" {
		return ""
	}
	conv, ok := p.convReg.Get(conversations.ConversationID(label))
	if !ok || conv.SystemPrompt == nil {
		return ""
	}
	return *conv.SystemPrompt
}

// refreshSystemPrompt re-composes sess's appended system prompt from the
// registry and rewrites the file its spawnBase already names. Called from
// Pool.Activate, the pool-owned funnel every first spawn and every re-activate
// passes through.
//
// It exists because spawnBase is immutable after construction while the prompt
// is not. Since #2085 a conversation's session is MINTED at create and its child
// comes up on the first message, so the operator's normal flow — create the
// channel, set its prompt, talk — sets the prompt after buildSession has already
// frozen the argv. Composing only at build time would satisfy every other clause
// of AC #1 and ship dead for the flow operators actually use, which is the shape
// Conversation.Cwd is stuck in: its doc claims a change takes effect on the next
// fresh spawn, and no production path reads it.
//
// The write targets sess.systemPromptPath VERBATIM rather than re-deriving from
// sess.id: a `/clear` rotation re-keys a session in place (Pool.rekeyLocked), so
// after one the path in spawnBase still carries the pre-rotation id.
//
// An already-active session is skipped. That is Juhana's ruling in code —
// setting a prompt does not restart a running session, it takes effect at the
// next session start — and it keeps a disk write off Activate's LRU-touch hot
// path.
//
// A write failure is logged and swallowed, deliberately. buildSession already
// wrote this file and the write is a rename, so a failed refresh leaves the
// previous COMPLETE composition in place — never a missing or truncated one.
// Failing an operator's message on a transient disk error, when the fallback is
// one-revision-stale prompt bytes, is the worse trade. The log carries the
// error, whose paths are already public (the argv record names this file), and
// no fragment of the prompt.
//
// Concurrency: sess.label is read under p.mu (RLock) and sess.systemPrompt is
// written under p.mu (write) — the discipline Session.settings documents. The
// file write runs between the two, off the lock, so no I/O executes inside the
// pool's critical section. Both acquisitions are released before Pool.Activate
// takes p.capMu, so the documented capMu → mu → lcMu order is not inverted.
func (p *Pool) refreshSystemPrompt(sess *Session) {
	if sess.systemPromptPath == "" {
		// The bootstrap (its file is daemon-scoped and lives on the Pool) and any
		// test-constructed Session literal that never spawns.
		return
	}
	if sess.LifecycleState() == stateActive {
		return
	}

	p.mu.RLock()
	label := sess.label
	p.mu.RUnlock()

	operator := p.conversationPrompt(label)
	if _, err := writeSystemPromptFile(sess.systemPromptPath, composeSystemPrompt(operator)); err != nil {
		p.log.Warn("refresh appended system prompt", "error", err)
		return
	}

	p.mu.Lock()
	sess.systemPrompt = operator
	p.mu.Unlock()
}

// SystemPromptFor returns the operator-set system prompt bytes the named session
// was actually spawned with — what a later slice compares against the stored
// value to tell an operator that a running session predates their edit (#2152).
//
// It reports the OPERATOR half, not the composed text: composing is this
// package's business, and returning the whole would make the caller strip a
// constant it does not own in order to compare. "" means the session was spawned
// with no appended operator text, which is both of #2149's no-bytes states.
//
// Deliberately in-process and unexported to the wire: #2150 adds no control-plane
// verb and no frame. #2152 is the ticket that puts this on the wire.
//
// Concurrency: MUST be called with p.mu unheld — one RLock acquisition, the same
// shape and the same non-reentrancy hazard SettingsFor documents.
func (p *Pool) SystemPromptFor(id SessionID) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	sess, ok := p.sessions[id]
	if !ok {
		return "", ErrSessionNotFound
	}
	return sess.systemPrompt, nil
}
