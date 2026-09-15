package sessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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

// ClientIdentity is one attached client's self-reported identity: the
// device_name and client_version it put in its own hello.
//
// BOTH FIELDS ARE REMOTE-AUTHORED AND UNVALIDATED. Nothing between the wire and
// admitClient inspects them — internal/relay retains them verbatim on purpose,
// because validating there would bind a wire type to a rendering decision it does
// not own (the argument composeSystemPrompt makes for leaving #2149's
// Registry.SetSystemPrompt the single door for operator bytes). A holder of this
// type holds untrusted text until admitClient has passed it.
//
// Either field may be empty: neither client sends a product name today
// (pyrycode-desktop reports a hostname, pyrycode-mobile a device model), and a
// client is free to report nothing at all.
type ClientIdentity struct {
	Name    string
	Version string
}

// ClientIdentityResolver answers which clients are attached right now. It is the
// seam by which internal/sessions learns something only internal/relay knows,
// without importing it — TransitionObserver's shape, pointing the other way.
//
// The implementation MUST respect ctx and MUST NOT block indefinitely: the
// production one funnels a request onto the relay manager's Run goroutine and
// waits for the reply, and it is called immediately before a claude spawn.
// Returning nil is always a valid answer and is what every failure collapses to;
// there is deliberately no error return, so nothing here can fail a spawn.
type ClientIdentityResolver func(ctx context.Context) []ClientIdentity

// maxClientNameBytes and maxClientVersionBytes bound what one client may
// contribute to the composed prompt. UTF-8 BYTES, NOT RUNES, matching every
// bound in internal/protocol. An over-bound value is REFUSED, never truncated:
// a truncation would invent a value the client did not report.
//
// A LENGTH CEILING IS NOT A SAFETY PROPERTY — MaxDeviceNameBytes' warning
// transfers unchanged, and is restated rather than cross-referenced because the
// ceiling is exactly what a later reader is most likely to mistake for
// containment. 64 bytes accommodates a newline-injection payload many times
// over. What actually holds the structure is admissibleClientField's character
// set plus the placement, not these numbers.
//
// The numbers are NOT borrowed from MaxDeviceNameBytes, whose 128 is picked for a
// hand-typed pairing label rendered as a terminal row. These are picked against a
// different arithmetic: the value is a span inside one sentence that is prepended
// to EVERY turn of the session and charged in tokens each time. 64 covers a
// macOS hostname (`Juhanas-MacBook.local`, 21) and an Android Build.MODEL with
// room to spare; 32 covers a semver with a long pre-release tag. Worst case for
// the whole section is maxNamedClients × (64 + 32) plus framing — the same order
// as systemPromptText itself, which is the most this feature may cost.
const (
	maxClientNameBytes    = 64
	maxClientVersionBytes = 32
)

// maxNamedClients caps how many clients the section may name. Past it the section
// is EMPTY — the whole admitted set or none.
//
// Naming a truncated subset is the option not taken: clientSectionLead states
// that what follows is the attached clients, so a silently trimmed list would make
// that sentence false inside a system prompt. Falling back to today's text is both
// honest and the fail-closed direction. Four is past any realistic operator fleet
// (a laptop, a phone, a spare), so the whole-or-nothing rule is unreachable in
// ordinary use and is really a bound on a client that opens conns to inflate the
// prompt.
const maxNamedClients = 4

// clientIdentityTimeout bounds one resolver call.
//
// It exists for the case AC #4 names: the relay manager is wired but its Run
// goroutine is not running (not yet started, or already exited), where
// V2SessionManager.ActiveConns has no receiver for its request and waits on ctx
// alone. The bound turns that from a blocked spawn into a bounded no-identity
// answer, and it degrades a future caller that violates the never-from-Run rule
// into a stall rather than a deadlock.
//
// 250ms is picked against the site, not against a network: the resolve runs
// immediately before a claude spawn that costs hundreds of milliseconds, so the
// worst case is invisible next to work already being done, while being orders of
// magnitude above what a live Run loop needs to answer a map read.
const clientIdentityTimeout = 250 * time.Millisecond

// clientSectionLead opens the section naming the attached clients. It is a
// TRANSCRIPTION OF A SELF-REPORT and nothing else — "the name and version it
// reported for itself" — so systemPromptText's constraint holds here unchanged:
// it asserts nothing about what any client can render or do, because such a claim
// rots the day that client ships a change. TestClientSectionText_Pinned pins it
// against an independent copy for exactly that reason.
//
// It is also the structural half of the trust boundary. Client bytes are placed
// AFTER this lead, inside quotes, on the same line — never at the start of a
// line — which with admissibleClientField's refusal of both control characters
// and the quote delimiter is what makes "no line originates from a client" true
// by construction rather than by escaping.
const clientSectionLead = "Clients attached when this session started, each shown by the name and " +
	"version it reported for itself when it connected: "

// SetClientIdentityResolver installs the pool's client-identity resolver; a nil
// resolver (the zero value, or an explicit nil) disables client naming, which is
// the shape foreground mode, v1, and almost every test in this package run in.
//
// The value is held in an atomic rather than a plain field, which is where this
// departs from SetTransitionObserver's otherwise identical pre-Run contract. The
// reason is concrete: the install happens inside startRelayV2, and the relay
// manager's own Run goroutine is ALREADY started by then. Run creates a
// per-conn appFrameWorker on every handshake, and that worker is one of the
// goroutines that reaches Pool.Activate — so a conn completing its handshake in
// the window between mgr.Run and this call would read the field concurrently with
// this write. The window is narrow and the race is real; an atomic closes it for
// four lines and removes the need to reason about the ordering at all. Do not
// "simplify" it back to a plain field.
func (p *Pool) SetClientIdentityResolver(r ClientIdentityResolver) {
	if r == nil {
		p.clientIdentity.Store(nil)
		return
	}
	p.clientIdentity.Store(&r)
}

// attachedClients resolves the clients attached right now, or nil.
//
// It is TOTAL, the posture conversationPrompt takes for the conversations
// registry: no resolver wired, a resolver reporting nothing, a manager whose Run
// goroutine cannot answer, and an already-cancelled ctx all yield nil rather than
// an error. Nothing on this path can fail or delay a spawn beyond
// clientIdentityTimeout.
//
// Takes no pool lock, and is called from refreshSystemPrompt between that
// function's two lock acquisitions — the same off-lock window the file write
// already occupies — so no I/O and no cross-goroutine wait executes inside the
// pool's critical section, and the documented capMu → mu → lcMu order cannot
// invert.
func (p *Pool) attachedClients(ctx context.Context) []ClientIdentity {
	resolver := p.clientIdentity.Load()
	if resolver == nil || *resolver == nil {
		return nil
	}
	// A done ctx answers here rather than inside the resolver. The production
	// resolver does return nil on a cancelled ctx, but that is ITS contract and
	// this function's totality must not be borrowed from it: any resolver, including
	// a test double or a future second implementation, gets the same answer.
	if ctx.Err() != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, clientIdentityTimeout)
	defer cancel()
	return (*resolver)(ctx)
}

// admissibleClientField reports whether one reported field may appear in the
// composed prompt, returning it VERBATIM when it may.
//
// Verbatim is deliberate: a value is refused or used as sent, never repaired.
// Trimming, escaping or truncating would each put a value in claude's prompt that
// no client reported, and an escaping pass is the kind of thing that grows a
// bypass. TrimSpace appears only in the blank test, not in the returned value.
//
// The character set is mintLabelIsDisplaySafe's — no C0, no DEL, no C1 — plus a
// refusal of the double quote. The quote is what makes the refusal structural
// rather than cosmetic: clientSection renders the value INSIDE quotes, so
// refusing the delimiter is what guarantees no value can close the structure
// around itself. Invalid UTF-8 is refused too; the composed text is written to a
// file claude reads as text.
func admissibleClientField(v string, maxBytes int) (string, bool) {
	if len(v) > maxBytes || !utf8.ValidString(v) || strings.TrimSpace(v) == "" {
		return "", false
	}
	for _, r := range v {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == '"' {
			return "", false
		}
	}
	return v, true
}

// admitClient is THE trust boundary: the one place a remote-authored
// ClientIdentity becomes text the daemon is willing to compose into a system
// prompt. Everything upstream of it — the relay's retention, ActiveConn, the
// cmd/pyry closure — carries the bytes and judges nothing.
//
// The two fields are judged independently, and the NAME is what gates the client:
// an inadmissible name drops the whole identity, because a version alone names
// nobody, while an inadmissible version drops only itself and leaves the client
// named. A refusal is silent — no log line, at any level, so a hostile name has
// no line to appear in, which is the package's existing rule for prompt bytes.
func admitClient(c ClientIdentity) (ClientIdentity, bool) {
	name, ok := admissibleClientField(c.Name, maxClientNameBytes)
	if !ok {
		return ClientIdentity{}, false
	}
	version, _ := admissibleClientField(c.Version, maxClientVersionBytes)
	return ClientIdentity{Name: name, Version: version}, true
}

// admittedClients returns the identities that may appear in a composed prompt —
// admitClient's survivors, SORTED and DEDUPLICATED — or nil when there is no
// section to render: no admissible identity, or more than maxNamedClients of them.
//
// Neither the sort nor the dedup is cosmetic. V2SessionManager.ActiveConns returns
// Go's randomized map-iteration order, so an unsorted set would rewrite the prompt
// file with different bytes on every refresh of an unchanged conn set. Dedup
// collapses one client holding two conns — a reconnect whose previous conn is not
// yet reaped — into the one client it is.
//
// Over-cap collapsing to nil rather than to a subset is maxNamedClients' rule, and
// the whole-or-nothing reading belongs here because this is where the count is known.
//
// It is split out of clientSection so #2436 can RETAIN a set rather than only render
// one: Session.promptClients holds this function's output, which is what keeps
// unadmitted remote-authored bytes off a long-lived struct and bounds what one client
// can park there. It is IDEMPOTENT for that reason — re-running it over its own output
// is the identity, since every predicate already holds and the set is already sorted
// and deduplicated — so a rotation composing from a carried set reproduces the section
// the earlier compose wrote, byte for byte.
func admittedClients(clients []ClientIdentity) []ClientIdentity {
	named := make([]ClientIdentity, 0, len(clients))
	for _, c := range clients {
		if admitted, ok := admitClient(c); ok {
			named = append(named, admitted)
		}
	}
	slices.SortFunc(named, func(a, b ClientIdentity) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Version, b.Version)
	})
	named = slices.Compact(named)
	if len(named) == 0 || len(named) > maxNamedClients {
		return nil
	}
	return named
}

// clientSection renders the section naming clients, or "" when admittedClients
// finds nothing to render.
//
// It admits its own input rather than trusting the caller to have done it, which is
// what keeps composeSystemPromptFor total over hostile values for every caller —
// including #2436's, which passes an already-admitted set through the idempotent path.
//
// The returned section ends in "\n", exactly as systemPromptText does, so both
// joins in composeSystemPromptFor use the same blank-line separator convention.
func clientSection(clients []ClientIdentity) string {
	named := admittedClients(clients)
	if len(named) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(clientSectionLead)
	for i, c := range named {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(`"` + c.Name + `"`)
		if c.Version != "" {
			b.WriteString(` (version "` + c.Version + `")`)
		}
	}
	b.WriteString(".\n")
	return b.String()
}

// composeSystemPromptFor is composeSystemPrompt plus the section naming the
// clients attached as the prompt is composed (#2148). Order is constant, then
// clients, then the operator's bytes.
//
// WITH NO SECTION TO ADD IT DELEGATES to composeSystemPrompt rather than
// reconstructing its result. That is what makes AC #2's "byte-for-byte what it is
// today" a structural property instead of a second branch that happens to agree
// today and drifts tomorrow, and it is why TestComposeSystemPrompt and
// TestSystemPromptText_Pinned needed no edit for this ticket.
//
// composeSystemPrompt keeps its own signature and its own body for the same
// reason. buildSession stays on it too: the construction-time write is overwritten
// by refreshSystemPrompt before any child comes up, so composing client names
// there would produce bytes nothing ever reads.
func composeSystemPromptFor(operator string, clients []ClientIdentity) string {
	section := clientSection(clients)
	if section == "" {
		return composeSystemPrompt(operator)
	}
	out := systemPromptText + "\n" + section
	if operator != "" {
		out += "\n" + operator
	}
	return out
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

// writeComposedPrompt composes sess's appended system prompt from the conversations
// registry plus clients, writes it to the path sess's spawnBase already names, and
// records what the session was composed with.
//
// It is the step the two refresh funnels share — refreshSystemPrompt for a spawn
// Pool.Activate drives, refreshSystemPromptForRotation for a `new_session` rotation.
// What differs between them is where clients comes from and which guards precede the
// call; everything from the registry read down is identical and lives here.
//
// The write targets sess.systemPromptPath VERBATIM rather than re-deriving from
// sess.id: every re-key moves a session in place (Pool.rekeyLocked), so after one the
// path in spawnBase still carries the pre-rotation id.
//
// Only what would render is retained — see Session.promptClients, whose doc carries
// the reason a raw resolver answer must not be stored.
//
// A write failure is logged and swallowed, deliberately. buildSession already
// wrote this file and the write is a rename, so a failed compose leaves the
// previous COMPLETE composition in place — never a missing or truncated one.
// Failing an operator's message, or their rotation, on a transient disk error when
// the fallback is one-revision-stale prompt bytes, is the worse trade. The log
// carries the error, whose paths are already public (the argv record names this
// file), and no fragment of the prompt and no client name: a refusal is silent, so a
// hostile name has no line to appear in. The composed-with fields are left untouched
// on that path, so they keep describing what the file actually holds.
//
// Concurrency: sess.label is read under p.mu (RLock) and the composed-with fields are
// written under p.mu (write) — the discipline Session.settings documents. The file
// write runs between the two, off the lock, so no I/O executes inside the pool's
// critical section. MUST be called with p.mu unheld.
func (p *Pool) writeComposedPrompt(sess *Session, clients []ClientIdentity) {
	p.mu.RLock()
	label := sess.label
	p.mu.RUnlock()

	operator := p.conversationPrompt(label)
	named := admittedClients(clients)
	if _, err := writeSystemPromptFile(sess.systemPromptPath, composeSystemPromptFor(operator, named)); err != nil {
		p.log.Warn("compose appended system prompt", "error", err)
		return
	}

	p.mu.Lock()
	sess.systemPrompt = operator
	sess.promptClients = named
	p.mu.Unlock()
}

// refreshSystemPromptForRotation recomposes sess's appended system prompt for a
// rotation that is about to discard its child, so the successor comes up on the
// prompt the operator has stored rather than on the one composed before they saved
// it (#2436).
//
// It is refreshSystemPrompt's twin and departs from it on exactly two axes, both of
// which are the whole of this ticket:
//
//   - NO stateActive GUARD. That guard keeps a disk write off Activate's LRU-touch hot
//     path and honours "setting a prompt does not restart a running session"; neither
//     is in tension with a rotation that is already replacing the child. It also has
//     to be absent rather than merely unreached: nothing in a rotation leaves
//     stateActive — only Session.Evict does — so a rotated session routed through
//     Activate's funnel would keep the stale composition for every later turn as well,
//     not just for the one child.
//   - NO CLIENT-IDENTITY RESOLVE. The whole new_session dispatch, from handleNewSession
//     down through Pool.RotateForNewSession, runs on V2SessionManager's single Run
//     dispatch goroutine — the goroutine Pool.attachedClients funnels its request onto
//     and then waits for a reply from. Called from Run it cannot be answered: it would
//     stall that goroutine for clientIdentityTimeout and then return nil, silently
//     dropping the section from every rotated session's prompt. That is the
//     never-from-Run rule clientIdentityTimeout's doc names, and with rate limiting
//     deferred (docs/protocol-mobile.md § Security model, threat 7) the stall is
//     reachable once per remotely-sent new_session frame. The session's carried
//     promptClients is composed instead, which clientSectionLead's past tense is what
//     licenses: it transcribes the clients attached when the session started and is
//     never restated mid-session.
//
// MUST be called with p.mu unheld, and MUST complete before the respawn is triggered:
// startFreshRunner feeds the rotation's new id to (*streamsup.Runner).RestartFresh,
// which cancels the live child at once, so a write landing after that is a race
// against the successor's spawn.
func (p *Pool) refreshSystemPromptForRotation(sess *Session) {
	if sess.systemPromptPath == "" {
		// The bootstrap — its file is daemon-scoped, lives on the Pool, and is read by
		// every session, so one conversation's operator text must never reach it — and
		// any test-constructed Session literal that never spawns.
		return
	}

	p.mu.RLock()
	clients := sess.promptClients
	p.mu.RUnlock()

	p.writeComposedPrompt(sess, clients)
}

// refreshSystemPrompt re-composes sess's appended system prompt from the
// registry and rewrites the file its spawnBase already names. Called from
// Pool.Activate, the funnel every first spawn and every re-activate passes through;
// since #2436 it is one of two, refreshSystemPromptForRotation being the other.
//
// It exists because spawnBase is immutable after construction while the prompt
// is not. Since #2085 a conversation's session is MINTED at create and its child
// comes up on the first message, so the operator's normal flow — create the
// channel, set its prompt, talk — sets the prompt after buildSession has already
// frozen the argv. Composing only at build time would satisfy every other clause
// of AC #1 and ship dead for the flow operators actually use — the shape
// Conversation.Cwd was stuck in until #1475, which gave the new_session rotation
// the re-read that makes its doc's "takes effect on the next fresh spawn" claim
// true. Two production paths read it now, and they are the two fresh spawns: that
// rotation, and the post-restart revive (#1487). An idle-evict re-activation is a
// resume rather than a fresh spawn and keeps the directory its runner already has.
//
// The composition itself, including the verbatim write and the swallowed write
// error, is writeComposedPrompt's; this function owns the two guards above it and
// the resolve.
//
// An already-active session is skipped. That is Juhana's ruling in code —
// setting a prompt does not restart a running session, it takes effect at the
// next session start — and it keeps a disk write off Activate's LRU-touch hot
// path. Since #2436 that guard is also why the `new_session` rotation does NOT come
// through here: the rotation is a next session start, and it is replacing the child
// anyway. refreshSystemPromptForRotation's doc has both halves of the difference.
//
// Concurrency: the resolve happens AFTER both early returns above, which is
// load-bearing: an already-active session skips it, so no cross-goroutine wait is
// ever paid on Activate's LRU-touch hot path. Every lock acquisition below is
// released before Pool.Activate takes p.capMu, so the documented capMu → mu → lcMu
// order is not inverted. Since #2148 the composition also names the clients attached
// at this moment, resolved through attachedClients. The snapshot's staleness is
// benign — clientSectionLead is written in the past tense as a transcription, so a
// client detaching between the resolve and the write makes the sentence no less true.
func (p *Pool) refreshSystemPrompt(ctx context.Context, sess *Session) {
	if sess.systemPromptPath == "" {
		// The bootstrap (its file is daemon-scoped and lives on the Pool) and any
		// test-constructed Session literal that never spawns.
		return
	}
	if sess.LifecycleState() == stateActive {
		return
	}

	p.writeComposedPrompt(sess, p.attachedClients(ctx))
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
