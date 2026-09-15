package sessions

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// ErrAttachUnavailable is returned by Session.Attach when the session has no
// bridge (foreground mode). The control plane maps this back to the existing
// "daemon may be in foreground mode" wire string for byte-identical client
// output.
var ErrAttachUnavailable = errors.New("sessions: attach unavailable (no bridge)")

// lifecycleState is the per-session two-state machine introduced in 1.2c-A:
// active (claude is, or should be, running) and evicted (no claude process;
// JSONL on disk is frozen and can be reattached on demand).
type lifecycleState uint8

const (
	stateActive  lifecycleState = iota // claude is (or should be) running
	stateEvicted                       // claude exited; JSONL is on disk
)

// String returns the on-disk encoding for a lifecycleState. Used by
// Pool.saveLocked when serializing the registry.
func (s lifecycleState) String() string {
	switch s {
	case stateEvicted:
		return "evicted"
	default:
		return "active"
	}
}

// parseLifecycleState maps the on-disk string back to its in-memory enum.
// Empty input or any unrecognised value defaults to stateActive — old pyry
// binaries write no lifecycle_state field, and unknown future values are
// treated as the conservative "session is live" default.
func parseLifecycleState(s string) lifecycleState {
	if s == "evicted" {
		return stateEvicted
	}
	return stateActive
}

// closedChan returns a chan that is already closed. Used as the initial
// activeCh for sessions that warm-start in stateActive (most of the time);
// Activate's wait on the channel returns immediately.
func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// SessionSettings is the per-session model / reasoning-effort / permission-posture
// set persisted in the registry (#833, #2043) and applied to the claude spawn argv.
// The zero value inherits the daemon template for Model/Effort and enforces
// permissions (YOLO off, mode default) — the fail-safe default.
//
// Set initially in Pool.New (bootstrap) or Pool.buildSession (minted) and
// mutated post-construction by Pool.UpdateSettings (#840) under Pool.mu (write);
// read under Pool.mu — the same discipline Pool.Rename uses for label.
//
// PermissionMode and YOLO express ONE posture through two fields, and a Pool-held
// Session always satisfies YOLO == (PermissionMode == permissionModeBypass):
// every writer either rejects (Pool.UpdateSettings, on an unknown mode or a
// contradictory pair) or normalises (both construction sites and the registry
// read, via canonicalPermissionMode). YOLO stays the authoritative half for the
// escalation — claudeSettingsArgs suppresses the mode pair on it and never derives
// a posture from the mode string — so the fail-safe is enforced in exactly one
// place even if a hand-built Session literal in a test breaks the invariant.
//
// Since #2065 the pair no longer decides what the child LAUNCHES as — every child
// launches in bypass — only what it is walked back to in-band. PermissionMode is
// therefore the value that actually decides the running posture, and the safety
// moved from "the flag is absent from the argv" to "the write was confirmed",
// which is the fail-closed turn gate in internal/streamsup.
//
// yolo is not redundant and is not being phased out here: the mobile client
// speaks it, and #1687 settles what the two mean together on the wire.
type SessionSettings struct {
	Model  string
	Effort string
	YOLO   bool

	// PermissionMode is the posture claude runs the session under: one of the
	// five non-escalating modes (permissionModeInBand) or permissionModeBypass.
	// Membership in the whole storable set is permissionModeKnown, and since
	// #2066 that set is also the set the daemon DELIVERS on a held-open stream:
	// all six are granted the same way, as a set_permission_mode control
	// request. The escalation used to be the exception — only a relaunch under a
	// recomposed argv could grant it (#1595) — so a reader of this field no
	// longer has to model its two halves differently. Empty is the zero value's
	// "never chosen", read as the default posture and normalised to
	// permissionModeDefault at every construction site.
	PermissionMode string
}

// permissionModeDefault is claude's own default posture and the mode a bypass
// revocation lands in. It is a NAMEABLE mode, unlike an empty Model or Effort,
// and since #2065 claudeSettingsArgs NAMES it: silence used to be the equivalent
// of an empty Model, but beside an unconditional --dangerously-skip-permissions
// an unnamed posture reads as the escalation rather than as the default.
//
// permissionModeBypass is the escalation. It is stored and it reaches the spawn
// argv ONLY as --dangerously-skip-permissions (claudeSettingsArgs), never as a
// --permission-mode value, so the YOLO fail-safe keeps exactly one spelling. That
// property survives #2065 making the flag unconditional: the flag no longer says
// which posture the session RUNS in, but it is still the only spelling of the
// escalation the daemon composes, and no mode string can produce it.
const (
	permissionModeDefault = "default"
	permissionModeBypass  = "bypassPermissions"
)

// PermissionModeBypass is the escalation's one spelling, exported for cmd/pyry's
// withApprovalArgs — the only consumer outside this package that has to tell a
// session which STAYS in bypass from one the daemon walks back in-band (#2065).
// It is an alias of permissionModeBypass rather than a second literal, so the
// vocabulary still lives in exactly one place.
//
// Nothing here decides authorisation from it. It answers "will this child keep
// the posture it launched with?", and cmd/pyry pairs it with the provenance bit
// (RunnerConfig.OperatorBypass) because the stored posture alone cannot answer
// that — the operator's pass-through claude args put the escalation on an argv
// without ever touching SessionSettings.
const PermissionModeBypass = permissionModeBypass

// bypassPermissionsArg is claude's argv spelling of the escalation and the only
// spelling of it — the same constant internal/streamsup names bypassPermissionsFlag
// for its own reader. Hoisted out of claudeSettingsArgs when #2065 gave the flag a
// second reader in this package (operatorBypass), so the two cannot drift.
const bypassPermissionsArg = "--dangerously-skip-permissions"

// operatorBypass reports whether base — a SETTINGS-FREE spawn argv, i.e. a
// Session.spawnBase — already carries the escalation. It is the provenance signal
// #2065 turns both of the daemon's permission fail-safes on, and it exists because
// after that ticket the ASSEMBLED argv can no longer answer the question:
// claudeSettingsArgs appends the flag to every composition, so a reader keyed on
// the assembled argv answers "yes" for every session and both fail-safes invert
// into their permissive arms, silently.
//
// True means the operator handed this daemon a bypass — the shape
// `pyry install-service -- --dangerously-skip-permissions` documents — which never
// touches SessionSettings and which the daemon therefore may NOT walk back: doing
// so revokes, in-band and unanswerable, an escalation the machine's owner asked for
// outright. False means every escalation on this session's argv is one this package
// composed from the stored posture, so the stored posture decides what the child
// ends up in.
//
// It reads base and NEVER the assembled argv. A caller that hands it
// Session.spawnArgs' output gets true unconditionally, which is the one way to
// misuse it and is why the parameter is named for what it must be.
func operatorBypass(base []string) bool {
	return slices.Contains(base, bypassPermissionsArg)
}

// ErrUnsupportedPermissionMode is returned by Pool.UpdateSettings when the update
// names a permission mode this daemon does not recognise. Nothing is persisted and
// no live child is touched, so an unrecognised value can never reach a spawn argv
// or the child.
//
// Bare: it does NOT echo the rejected mode. A caller may therefore log it
// verbatim — #833 keeps settings values out of the daemon log and a permission
// mode is a settings value. It is the in-package twin of the seam's own
// streamsup.ErrUnsupportedPermissionMode, which this package must not import
// (that would invert the Runner seam).
var ErrUnsupportedPermissionMode = errors.New("sessions: unsupported permission mode")

// ErrPermissionModeConflict is returned by Pool.UpdateSettings when one frame
// carries a permission mode and a YOLO bit that contradict each other — a
// bypassPermissions mode with yolo:false, or any other mode with yolo:true.
// Nothing is persisted.
//
// Rejection rather than a precedence rule, because neither precedence is
// fail-safe in both directions: letting the mode win downgrades an escalation
// silently, letting YOLO win GRANTS one from a frame that said false. A
// contradictory update has no correct reading. Bare, for
// ErrUnsupportedPermissionMode's reason.
var ErrPermissionModeConflict = errors.New("sessions: permission mode contradicts yolo")

// permissionModeInBand reports whether mode is one of the five NON-ESCALATING
// postures claude accepts on a stream it is already reading — #2041 measured all
// five live at 2.1.239, extending #1595's default.
//
// IT IS NO LONGER THE ROUTING PREDICATE, and reading it as one is the mistake this
// paragraph exists to prevent. #2066 made the escalation in-band-deliverable too
// (#2065 put the launch flag on every argv; #2060 measured claude accepting the
// re-escalation on such a child), so "deliverable in band" is now the SIX-member
// question permissionModeKnown answers, and inBandDeliverable calls that instead.
// This predicate deliberately stayed at five, because its other three callers ask a
// different question and every one of them breaks silently if the escalation joins:
//
//   - canonicalPermissionMode would canonicalise (bypassPermissions, yolo:false) to
//     the escalation instead of degrading it to default, so a hand-edited registry
//     entry would escalate itself on warm start — reversing settingsFromEntry's
//     guarantee that the tolerance can only ever move AWAY from the escalation.
//   - claudeSettingsArgs would start emitting --permission-mode bypassPermissions,
//     giving the escalation a second spelling on the argv; non-membership here is
//     the named mechanism that function's doc relies on.
//   - permissionModeForDisk would write the escalation into the permission_mode key
//     beside the yolo key that already carries it.
//
// None of those is a build error, so the containment is this predicate's membership
// and nothing else. Widen the CALL SITE that needs six, never this list.
//
// A switch, deliberately, and NOT a package-level slice or map: the latter is
// mutable package state holding a security vocabulary, which anything in this
// package — a test included — could append the escalation onto. Control flow
// cannot be appended to. Same argument the writer's own allow-list records.
//
// THIS IS A VOCABULARY CHECK, NOT AN AUTHORISATION CHECK. It answers "will claude
// parse this mode?" and nothing else. Three of its members (acceptEdits, auto,
// dontAsk) genuinely LOOSEN a child launched behind the daemon's approval flags
// (cmd/pyry's withApprovalArgs), and dontAsk means the permission-prompt tool
// never fires at all. Who may ask for that is #1687's decision; this slice's only
// callers are in-package.
func permissionModeInBand(mode string) bool {
	switch mode {
	case permissionModeDefault, "acceptEdits", "plan", "auto", "dontAsk":
		return true
	}
	return false
}

// permissionModeKnown reports whether mode is one this daemon can STORE: the
// non-escalating five plus the escalation. It is the gate Pool.UpdateSettings
// applies to operator input, and it is what keeps an unrecognised value out of the
// registry — and therefore out of every argv composed from it later.
//
// Since #2066 it is ALSO the routing predicate inBandDeliverable reads, because
// every posture this daemon can store is now one it can deliver in band: the
// escalation used to be the exception, stored but granted only by relaunching, and
// that ticket removed the exception. Two questions, one answer, and no second
// predicate minted for a set that already has a name — a duplicate would be two
// copies of one membership rule rather than a second defence.
//
// They would diverge again if claude ever accepted a mode on the launch argv that
// it refused on a held-open stream. That mode gets its own predicate when it
// arrives; until then, do not pre-split this one.
func permissionModeKnown(mode string) bool {
	return mode == permissionModeBypass || permissionModeInBand(mode)
}

// canonicalPermissionMode returns the posture the (mode, yolo) pair actually
// describes, establishing SessionSettings' invariant. Total, and it never
// escalates: an unrecognised or empty mode, and a bypassPermissions mode paired
// with yolo:false, all land on the default posture.
//
// It reads permissionModeInBand, the FIVE non-escalating modes, and #2066 left it
// on that predicate on purpose while widening the routing one: the tolerance here
// must keep moving away from the escalation, so an unrecognised value and a
// hand-written bypassPermissions beside yolo:false both still land on default.
//
// It NORMALISES TRUSTED INPUT and is not a validation path — its callers are the
// two construction sites and the registry read, never operator input, which
// Pool.UpdateSettings rejects loudly instead. The tolerant arm follows
// parseLifecycleState's precedent in this file: an unrecognised value on disk
// degrades to the conservative default rather than bricking a daemon over a
// hand-edit that has a safe reading.
func canonicalPermissionMode(mode string, yolo bool) string {
	if yolo {
		return permissionModeBypass
	}
	if permissionModeInBand(mode) {
		return mode
	}
	return permissionModeDefault
}

// canonicalSettings returns s with its permission posture normalised. Applied at
// both construction sites so a Pool-held Session never carries the empty mode,
// and so the unset posture Pool.mintSettings and Pool.revivedSettings both leave
// behind becomes the default one rather than an unspelled one.
func canonicalSettings(s SessionSettings) SessionSettings {
	s.PermissionMode = canonicalPermissionMode(s.PermissionMode, s.YOLO)
	return s
}

// SettingsUpdate is a partial change to a session's SessionSettings. A nil
// field means "leave the stored value untouched"; a non-nil field sets that
// value — including "" for Model/Effort and false for YOLO, which are thereby
// distinguishable from omitted. It is the presence contract shared with the v2
// settings verb (#841), which decodes the wire payload into it.
//
// YOLO is a *bool for the fail-safe: an omitted (nil) YOLO can never enable
// bypass — only an explicit non-nil *true turns --dangerously-skip-permissions
// on, and an explicit *false turns it off.
type SettingsUpdate struct {
	Model  *string
	Effort *string
	YOLO   *bool

	// PermissionMode names the posture to switch to (#2043). Unlike Model and
	// Effort, "" is NOT a meaningful value here — the default posture is a
	// nameable mode, so an explicit empty string has no reading and is rejected
	// with ErrUnsupportedPermissionMode like any other unrecognised value.
	//
	// It and YOLO express one posture, so an update naming EITHER is an update
	// to the posture; naming both with values that contradict each other is
	// ErrPermissionModeConflict. See Pool.UpdateSettings for the derivation.
	PermissionMode *string
}

// claudeSettingsArgs returns the extra claude flags implied by s, in a
// deterministic order (model, effort, posture) for testability. Empty Model or
// Effort emits no flag (inherit the template).
//
// THE --model VALUE IS THE FAMILY ALIAS, NOT THE STORED ONE (#2447), and the two
// differ by design. Not every row claude publishes is an alias — Fable and the
// "Haiku 4.5" row are published as exact ids — so composing the stored value
// verbatim pinned a session to a model claude had since superseded. familyAlias
// rewrites an exact id to its family here and at Pool.deliverSettingsInBand, and
// nowhere else: the stored value is left as picked so the model menu keeps
// matching its row by exact equality and cmd/pyry's validateModelVocabulary keeps
// finding it in the published list. Read familyAlias for why its output cannot
// weaken what internal/relay's validModel buys this argv sink.
//
// THE ESCALATION FLAG IS UNCONDITIONAL (#2065). Every child the daemon spawns —
// bootstrap, minted and revived — launches with --dangerously-skip-permissions,
// and the posture it actually RUNS in is decided by the in-band write
// streamsup's spawnAndWait issues before any user turn can reach it. The launch
// argv stopped being the posture's authority, which is the whole ticket: claude
// gates a re-escalation on the launch argv and refuses it in-band, so a posture
// change in that direction needed a respawn while the flag was conditional.
//
// The posture slot is therefore no longer mutually exclusive — the two flags sit
// side by side — but the property the exclusion existed for is unchanged and is
// restated here rather than dropped:
//
//   - The escalation keeps EXACTLY ONE SPELLING. This function never emits
//     --permission-mode bypassPermissions, because permissionModeInBand gates the
//     value and the escalation is not a member. That non-membership is a live
//     invariant rather than a historical accident: #2066 widened the routing
//     predicate to six members and deliberately left permissionModeInBand at five
//     for this clause and its two siblings, so a later slice that "tidies" the two
//     predicates into one hands the escalation a second argv spelling. No mode
//     string can compose the flag either: it is appended unconditionally, from
//     nothing.
//   - The YOLO bit stays the authoritative half of the posture. YOLO==true emits
//     the flag alone, so a hand-built literal whose mode contradicts its bit
//     cannot make the daemon assert a mode at a session stored as escalated.
//   - A non-escalated posture appends --permission-mode <mode>, INCLUDING
//     permissionModeDefault. That mode used to append nothing, on the reasoning
//     that default IS claude's own default so silence was the equivalent of an
//     empty Model. Silence beside an unconditional bypass flag no longer reads as
//     default, so the reason is gone with the exclusion. The assembled argv is not
//     new: every non-bypass stream spawn already ended in --permission-mode
//     default, injected by cmd/pyry's permissionArgs, whose own copy
//     withApprovalArgs then drops (#2043).
//
// A mode outside the in-band set — "" from a hand-built zero literal, or an
// unrecognised string — appends NO pair, so the argv never claims a posture the
// daemon will not go on to write. A Pool-held Session cannot reach that state:
// canonicalSettings runs at both construction sites and the registry read. Do not
// "harden" this by normalising "" to default here; that would put a posture on the
// argv that nothing writes in-band, and the flag wins at launch, so the argv would
// assert a safety the child does not have.
func claudeSettingsArgs(s SessionSettings) []string {
	var args []string
	if s.Model != "" {
		args = append(args, "--model", familyAlias(s.Model))
	}
	if s.Effort != "" {
		args = append(args, "--effort", s.Effort)
	}
	args = append(args, bypassPermissionsArg)
	if !s.YOLO && permissionModeInBand(s.PermissionMode) {
		args = append(args, "--permission-mode", s.PermissionMode)
	}
	return args
}

// spawnArgs composes the full claude spawn argv for the given settings: the
// settings-free base (spawnBase) plus claudeSettingsArgs(settings). It is the
// only argv-recompose path outside session construction — the live-apply in
// Pool.UpdateSettings, whether it installs the result with the kill (#842's
// Restart) or without one (#1581's in-band branch) — and, like construction,
// routes the settings
// suffix through claudeSettingsArgs, so the YOLO fail-safe is enforced in
// exactly one place. Returns a fresh slice that aliases neither spawnBase nor
// the caller's state; a zero-value settings appends nothing (byte-identical to
// the base).
func (s *Session) spawnArgs(settings SessionSettings) []string {
	return append(slices.Clone(s.spawnBase), claudeSettingsArgs(settings)...)
}

// Session is one supervised claude instance plus the bridge that mediates its
// I/O in service mode. As of 1.2c-A each Session owns a lifecycle goroutine
// (the body of Run) that drives the active↔evicted state machine.
type Session struct {
	// id is the session's stable identifier. Guarded by lcMu: written by
	// Pool.RotateID under BOTH Pool.mu (W) and lcMu on a /clear rotation (#866);
	// read off the lifecycle goroutine via currentID(), or directly by
	// Pool.mu-holders (List, ResolveID, Snapshot, saveLocked, Activate).
	id  SessionID
	sup Runner
	log *slog.Logger

	// Persisted metadata. createdAt and bootstrap are immutable post-New.
	// label is immutable from the lifecycle goroutine's perspective but may
	// be mutated by Pool.Rename under Pool.mu (write); other readers hold
	// Pool.mu (RLock or Lock). lastActiveAt is bumped under lcMu on every
	// state transition.
	label     string
	createdAt time.Time
	bootstrap bool

	// settings holds the per-session model / effort / YOLO applied to the
	// claude spawn argv (#833). Set in Pool.New (bootstrap) or
	// Pool.buildSession (minted) and mutated by Pool.UpdateSettings (#840)
	// under Pool.mu (write); read under Pool.mu by saveLocked (same discipline
	// as label, NOT under lcMu).
	settings SessionSettings

	// spawnBase is the settings-free claude spawn argv: the template args plus
	// any construction-time resume suffix (--session-id <id> for a minted
	// session), but WITHOUT the claudeSettingsArgs suffix. spawnArgs recomposes
	// the full argv from this base plus the live settings on every settings-change
	// live-apply (#842's restart, #1581's in-band install). Immutable
	// post-construction, so it is read
	// without a lock. It never contains a YOLO-derived flag, so no persisted-false
	// state can recompose into an escalated child through the STORED posture.
	//
	// It CAN contain the escalation flag from the other direction, and since #2065
	// that is the load-bearing fact about this field rather than a footnote: the
	// operator's bootstrap pass-through claude args land here verbatim, so a base
	// carrying --dangerously-skip-permissions is a bypass the machine's owner asked
	// for outside SessionSettings entirely. That is the ONE signal separating a
	// bypass the daemon composed (and may walk back) from one it was handed (and may
	// not) — claudeSettingsArgs now appends the flag to every argv, so the assembled
	// argv cannot answer it. operatorBypass reads this field for exactly that, and
	// both RunnerConfig construction sites carry the answer across the runner seam.
	spawnBase []string

	// settingsPath is the absolute path to the per-session --settings file
	// carrying {"enableAllProjectMcpServers":true}, which pre-approves the
	// project's MCP servers so claude's startup enablement modal never wedges the
	// PTY readiness check (#943). It is a member of spawnBase, so it survives
	// every recompose — a backoff restart and the #842 live settings-restart both
	// re-exec with the same path. Removed at session teardown: Pool.Remove for a
	// minted session, Pool.Run shutdown for the bootstrap. Immutable
	// post-construction, so it is read without a lock (same discipline as
	// spawnBase). Empty only for a test-constructed Session that hand-builds a
	// literal and never spawns.
	settingsPath string

	// systemPromptPath is the absolute path to this session's appended
	// system-prompt file — #2093's constant plus, since #2150, the bound
	// conversation's operator-set prompt. Like settingsPath it is a member of
	// spawnBase, so it survives every recompose (a backoff restart, the #842 live
	// settings-restart) and is immutable post-construction, read without a lock.
	//
	// Its BYTES are not immutable: Pool.refreshSystemPrompt rewrites this file
	// before every spawn that Pool.Activate drives, which is what carries a prompt
	// set after the session was minted (#2085's create-then-configure-then-talk
	// flow) into the child's argv without touching the frozen argv itself.
	// Rewriting this path verbatim rather than re-deriving it from the session id
	// is load-bearing: a /clear rotation re-keys the session in place, so after one
	// this path still carries the pre-rotation id.
	//
	// Removed at session teardown (Pool.Remove), at daemon shutdown (Pool.Run
	// removes the whole session-prompts directory), and on every error return
	// between the write and a successful build. Empty on the bootstrap session,
	// whose file is daemon-scoped and lives on the Pool as systemPromptPath, and
	// on any test-constructed Session that hand-builds a literal and never spawns.
	systemPromptPath string

	// systemPrompt is the OPERATOR half of what this session was last composed
	// with: the conversation's stored prompt at construction, refreshed by
	// Pool.refreshSystemPrompt before each spawn. Empty means the session spawned
	// with #2093's constant and nothing more, which covers both of #2149's
	// no-bytes states.
	//
	// It is the operator half rather than the composed whole so a reader can
	// compare it against the conversation's stored value without stripping a
	// constant it does not own — Pool.SystemPromptFor is that reader's accessor
	// and #2152 is the slice that reports the difference.
	//
	// Written under Pool.mu (write) by the refresh and set lock-free at
	// construction, before the session is registered; read under Pool.mu (RLock).
	// The same discipline settings follows, and deliberately NOT lcMu.
	systemPrompt string

	// promptClients is the ADMITTED client set the session's appended prompt was
	// last composed with (#2148's section), carried so a `new_session` rotation can
	// recompose without resolving (#2436). The rotation dispatch runs on the relay
	// manager's own Run goroutine, which is the goroutine Pool.attachedClients needs
	// an answer from, so a resolve there cannot be answered — refreshSystemPrompt's
	// never-from-Run rule. Carrying the previous resolve forward keeps
	// clientSectionLead true: it transcribes the clients attached when the session
	// started, in the past tense, and is never restated mid-session.
	//
	// It holds only values that have already crossed admitClient — the output of
	// admittedClients, nil whenever nothing would render. Retaining the resolver's
	// raw answer instead would park unadmitted remote-authored bytes on a long-lived
	// struct, and would bound the retention by however many conns a client holds
	// rather than by maxNamedClients × (maxClientNameBytes + maxClientVersionBytes).
	//
	// Written under Pool.mu (write) and read under Pool.mu (RLock) — systemPrompt's
	// discipline exactly, and deliberately NOT lcMu. The slice is IMMUTABLE once
	// stored: a later compose must REPLACE it and MUST NOT append into the backing
	// array, which is what lets refreshSystemPromptForRotation read the header under
	// a short RLock and compose off the lock.
	promptClients []ClientIdentity

	// pool is the back-pointer used to persist registry changes after a
	// state transition. Set once, in Pool.New.
	pool *Pool

	// idleTimeout is the eviction window. 0 disables eviction entirely
	// (test default and operator escape hatch).
	idleTimeout time.Duration

	// removedCh is closed exactly once by Pool.Remove, after the registry
	// remove commits. A closed removedCh tells the lifecycle goroutine to
	// exit its Run loop cleanly (return nil) instead of re-parking in
	// runEvicted. Write-once: allocated at construction, never reallocated
	// (unlike activeCh/evictedCh, which swap under lcMu). Readers select/read
	// it WITHOUT lcMu — it is therefore deliberately outside the lcMu-guarded
	// block below. A nil channel is a valid "never removed" state (a nil
	// channel is a never-ready select case), used by any test-constructed
	// Session that hand-builds a literal.
	removedCh chan struct{}

	// Lifecycle state, attach bookkeeping, and Activate/Evict signalling.
	// lcMu protects all fields below it.
	lcMu         sync.Mutex
	lcState      lifecycleState
	attached     int           // number of currently-bound bridge clients
	activeCh     chan struct{} // closed when stateActive; replaced when stateEvicted
	evictedCh    chan struct{} // closed when stateEvicted; replaced when stateActive
	activateCh   chan struct{} // buffered(1); Activate sends, runEvicted reads
	evictCh      chan struct{} // buffered(1); Evict sends, runActive reads
	lastActiveAt time.Time
}

// ID returns the session's stable identifier.
func (s *Session) ID() SessionID { return s.currentID() }

// currentID returns s.id under s.lcMu. sess.id is written by Pool.RotateID
// under both Pool.mu (W) and lcMu (#866); a read is race-clean while holding
// either. Lifecycle-goroutine readers (those NOT holding Pool.mu) MUST route
// id reads through this helper; Pool.mu-holders read s.id directly.
func (s *Session) currentID() SessionID {
	s.lcMu.Lock()
	defer s.lcMu.Unlock()
	return s.id
}

// State returns a snapshot of the supervisor's runtime state. Pure delegation
// to (*supervisor.Supervisor).State. Note: in stateEvicted, the supervisor's
// phase is PhaseStopped — that is faithful, since the supervisor really
// isn't running.
func (s *Session) State() State { return s.sup.State() }

// WriteUserTurn delegates to the underlying supervisor. Consumed by the
// send_message handler via the handlers.TurnWriter interface. ctx bounds the
// supervisor's ready-gate + commit-confirm delivery; the handler passes a
// timeout-bounded ctx so a busy/wedged claude surfaces as a loud failure
// rather than hanging the per-conn goroutine.
func (s *Session) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	return s.sup.WriteUserTurn(ctx, conversationID, payload)
}

// Runner exposes the underlying runner as the Runner interface, so a consumer can
// reach runner-type-specific methods (SendEsc / Interrupt) that are deliberately
// NOT on the narrow Runner interface (#1077). Unlike Supervisor(), which returns
// the concrete *supervisor.Supervisor (nil for a stream-json runner), Runner()
// returns whatever backs sess.sup — total for both runner types. Consumed by
// cmd/pyry's #1121 interrupt routing, which type-switches the returned runner to
// its concrete interrupt method. No lock: sup is set once at construction, the
// same discipline as Supervisor().
func (s *Session) Runner() Runner { return s.sup }

// Bridge exposes the underlying I/O bridge, or nil in foreground mode.
// Consumed by the assistant-turn bridge in cmd/pyry to register an output
// observer on the PTY-drain path.

// LifecycleState returns a snapshot of the current lifecycle state. Used by
// tests and (eventually) status payloads. Safe from any goroutine.
func (s *Session) LifecycleState() lifecycleState {
	s.lcMu.Lock()
	defer s.lcMu.Unlock()
	return s.lcState
}

// Activate moves the session into stateActive if it is currently evicted,
// blocking until the lifecycle goroutine has started the supervisor AND the
// post-transition registry persist has completed AND the supervisor has
// bound its PTY (or ctx is cancelled). Safe from any goroutine; idempotent
// under concurrent calls.
//
// No early-return for "already active" — callers always wait on activeCh.
// When the session is fully active and persisted, activeCh is already closed
// and the receive returns immediately. When a transition is in flight (state
// flipped, persist still running), the receive correctly blocks until the
// persist completes and transitionTo closes activeCh.
//
// PTY-readiness wait: after the state flip, runOnce takes a brief window
// (~hundreds of ms) to allocate the PTY master and call setPTY. Activate
// waits past that window via supervisor.WaitForPTY so callers that follow
// Activate with WriteUserTurn/Resize observe a live PTY rather than the
// silent-drop-on-nil branch. The relay-routed send_message path depends
// on this guarantee (#396).
//
// Cancellation, in two cases that differ (#1805):
//
//   - A ctx already cancelled or expired at the call returns ctx.Err()
//     immediately, before anything else happens: no signal on activateCh, no
//     supervisor start, no child. That guarantee is hard.
//   - A cancellation arriving while the call is waiting also returns
//     ctx.Err(), except that a transition completing at the same instant may
//     win the race and return nil — both select arms are then ready. Callers
//     that need certainty must not rely on this half.
func (s *Session) Activate(ctx context.Context) error {
	// Fail fast rather than fall into the select below, where a closed
	// activeCh makes both arms ready and Go's uniform pick returns nil to a
	// cancelled caller roughly half the time. Above the lcMu block on
	// purpose: below it, the activateCh send would already have driven a
	// re-activation for a caller that had given up.
	if err := ctx.Err(); err != nil {
		return err
	}

	s.lcMu.Lock()
	ch := s.activeCh
	if s.lcState != stateActive {
		// Buffered(1) — concurrent Activates collapse to one signal; the
		// lifecycle goroutine drains it once when leaving runEvicted, then
		// the shared activeCh wakeup picks up any extra waiters.
		select {
		case s.activateCh <- struct{}{}:
		default:
		}
	}
	s.lcMu.Unlock()

	select {
	case <-ch:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.sup.WaitForPTY(ctx)
}

// Evict moves the session into stateEvicted if it is currently active,
// blocking until the lifecycle goroutine has stopped the supervisor (or ctx
// is cancelled). No-op when the session is already evicted. Safe from any
// goroutine; idempotent under concurrent calls.
//
// Used by the cap-policy spawn path (Phase 1.2c-B): when activating one more
// session would exceed Pool.activeCap, the LRU peer is evicted via this
// primitive before the new spawn proceeds. Force-eviction — unlike the idle
// timer, it does not defer for attached>0. The cap is a hard limit; an
// attached caller will see EOF on its bridge.
func (s *Session) Evict(ctx context.Context) error {
	s.lcMu.Lock()
	ch := s.evictedCh
	if s.lcState != stateEvicted {
		select {
		case s.evictCh <- struct{}{}:
		default:
		}
	}
	s.lcMu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// touchLastActive bumps lastActiveAt to time.Now().UTC() under lcMu. Called
// by the cap-policy spawn path on an Activate against an already-active
// session so LRU ordering reflects the most recent touch. Not persisted —
// the registry's lastActiveAt is only flushed on state transitions.
func (s *Session) touchLastActive() {
	s.lcMu.Lock()
	s.lastActiveAt = time.Now().UTC()
	s.lcMu.Unlock()
}

// Run blocks until ctx is cancelled, driving the session's active↔evicted
// state machine. The body alternates between runActive (supervisor running,
// idle timer armed) and runEvicted (no supervisor; waiting for an Activate).
// A registry write happens after each transition.
func (s *Session) Run(ctx context.Context) error {
	// On permanent termination — outer ctx cancel (pool shutdown) or Pool.Remove
	// — release any attach input pump parked on the bridge's buffered send so
	// the daemon exits promptly instead of hanging until SIGKILL (#863). This is
	for {
		switch s.snapshotState() {
		case stateActive:
			// runActive fully commits the eviction before it returns: on a real
			// eviction reason beginEvict FIRES the transition signal and THEN flips
			// the session to stateEvicted, both BEFORE the child is torn down — so a
			// send_message delivery racing the teardown window drives a real respawn
			// instead of no-oping against the dying child (#1186), and the signal is
			// ordered ahead of stateEvicted becoming observable (#1186 rework: the
			// two-phase split must not decouple the state flip from the signal).
			// endEvict then persists the registry and closes evictedCh after the
			// child stops. Nothing is left for the loop but to propagate a terminal
			// error.
			if err := s.runActive(ctx); err != nil {
				return err
			}
		case stateEvicted:
			if err := s.runEvicted(ctx); err != nil {
				return err
			}
			if s.isRemoved() {
				// Pool.Remove signalled removal: exit the lifecycle goroutine
				// with nil so the pool's shared errgroup does NOT cancel gctx
				// and tear down every other session plus the relay leg. Only a
				// genuine ctx.Done() (pool shutdown) returns ctx.Err() above.
				return nil
			}
			if err := s.transitionTo(stateActive); err != nil {
				// Non-fatal, same reasoning as the evict transition above:
				// memory is already authoritative, the next transition
				// re-persists. Do not tear down the daemon over a persist I/O
				// error on a routine re-activation.
				s.log.Warn("session: registry persist failed on activate; keeping in-memory state, retrying next transition",
					"event", "session.persist_failed",
					"transition", "active",
					"err", err)
			}
		}
	}
}

// snapshotState returns the current lifecycle state under lcMu.
func (s *Session) snapshotState() lifecycleState {
	s.lcMu.Lock()
	defer s.lcMu.Unlock()
	return s.lcState
}

// isRemoved reports whether Pool.Remove has closed removedCh. Non-blocking; no
// lcMu (removedCh is write-once and never reopens, so the read is race-free and
// deterministic). A nil removedCh — a never-removed test literal — is a
// never-ready channel, so the default arm returns false.
func (s *Session) isRemoved() bool {
	select {
	case <-s.removedCh:
		return true
	default:
		return false
	}
}

// runActive supervises the session while it is active: spawns the supervisor
// on an inner ctx, arms the idle timer, and returns when one of:
//   - outer ctx cancels → returns ctx.Err() (terminal; outer Run propagates)
//   - supervisor exits spontaneously → beginEvict("") (silent), returns nil (loop
//     will evict; the wire has no "crashed" reason, so nothing signals)
//   - idle timer fires AND attached==0 → beginEvict(ReasonEviction), returns nil
//   - cap-policy evict signal → beginEvict(ReasonEviction), returns nil
//
// On every eviction path runActive both fires the transition signal and commits
// stateEvicted itself, via beginEvict, BEFORE tearing the child down. Threading
// the reason into beginEvict orders the signal ahead of stateEvicted becoming
// observable, so a consumer that reads LifecycleState()==stateEvicted is
// guaranteed the eviction signal has already fired (#1186 rework). While
// attached>0, idle eviction is deferred (poll-with-grace: re-arm on fire —
// eviction may overshoot the configured timeout by up to one window). A zero
// idleTimeout disables the timer entirely.
func (s *Session) runActive(ctx context.Context) error {
	subCtx, cancelSup := context.WithCancel(ctx)
	defer cancelSup()

	runErr := make(chan error, 1)
	go func() { runErr <- s.sup.Run(subCtx) }()
	drainSup := func() { <-runErr }

	// nil channel never selects — used as the timer placeholder when
	// idleTimeout is zero (eviction disabled).
	var timerCh <-chan time.Time
	var timer *time.Timer
	if s.idleTimeout > 0 {
		timer = time.NewTimer(s.idleTimeout)
		defer timer.Stop()
		timerCh = timer.C
	}

	for {
		select {
		case <-ctx.Done():
			cancelSup()
			drainSup()
			return ctx.Err()
		case <-runErr:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Supervisor exited on its own. Today this is largely
			// defensive — supervisor.Run only returns on ctx cancel —
			// but treating it as an evict trigger keeps the lifecycle
			// loop consistent if that contract ever loosens. No reason
			// is surfaced: the wire has no "crashed" transition, so Run
			// fires no signal on this path.
			//
			// The child is already gone (runErr fired), so there is nothing to
			// tear down: commit the eviction and close it out back-to-back so
			// the session still lands in stateEvicted and persists (#1186). The
			// empty reason fires no signal — the wire has no "crashed" transition.
			s.beginEvict("")
			s.endEvict()
			return nil
		case <-timerCh:
			s.lcMu.Lock()
			attached := s.attached
			s.lcMu.Unlock()
			if attached > 0 {
				timer.Reset(s.idleTimeout)
				continue
			}
			// SIGKILL-cause record: pairs with the supervisor-level
			// "claude exited" line that follows. Operators reading logs
			// after an idle eviction see this WARN first and don't have
			// to correlate the generic "signal: killed" exit with the
			// configured idle window. #396 added this signal so a
			// supervision-incomplete state has an explicit log line.
			s.log.Warn("session: idle eviction firing",
				"event", "session.idle_eviction",
				"session_id", string(s.currentID()),
				"idle_timeout", s.idleTimeout,
				"bootstrap", s.bootstrap)
			// Refuse writes BEFORE anything publishes this teardown (#1513). #1186's
			// non-active flip below covers a delivery that has not yet been dispatched;
			// it cannot help one already past the seam, whose bytes would land in the
			// dying child's pipe, return nil, and be dropped from the queue as
			// committed. The arm is above beginEvict rather than merely above
			// cancelSup because beginEvict FIRES the eviction signal, and #1330's
			// placement rule is to arm before the client is told anything. It takes one
			// leaf mutex on the runner and makes no call-out, so it delays neither the
			// signal nor the kill.
			s.sup.BeginTeardown()
			// Fire the eviction signal and commit stateEvicted BEFORE tearing down
			// the child so a delivery racing this teardown window sees a non-active
			// session and drives a real respawn instead of no-oping against the
			// dying child (#1186). beginEvict signals ahead of the flip; endEvict
			// persists and closes evictedCh only after the child stops.
			s.beginEvict(ReasonEviction)
			cancelSup()
			drainSup()
			s.endEvict()
			return nil
		case <-s.evictCh:
			// Same arm as the idle path above, and the one that matters most in
			// ordinary use (#1513): a cap eviction is uncorrelated with the victim's
			// own deliveries, so multi-conversation traffic reaches this window without
			// any contrived interleaving.
			s.sup.BeginTeardown()
			// Cap-policy eviction: forced, regardless of attached count. Same
			// two-phase commit as the idle path (#1186): signal, then flip to
			// evicted before teardown so a racing Activate is never lost, close out
			// after.
			s.beginEvict(ReasonEviction)
			cancelSup()
			drainSup()
			s.endEvict()
			return nil
		}
	}
}

// runEvicted blocks until either ctx is cancelled (terminal, returns ctx.Err)
// or an Activate call signals on activateCh (returns nil; loop transitions
// back to active). No supervisor is running while we sit here.
func (s *Session) runEvicted(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.removedCh:
		// Pool.Remove closed removedCh. Return nil; Run's isRemoved() check
		// turns this into a clean exit (no errgroup error). A concurrent
		// activateCh signal loses: Run re-checks isRemoved() before
		// re-activating, so a removed session can never resurrect.
		return nil
	case <-s.activateCh:
		return nil
	}
}

// transitionTo flips lcState to newState, bumps lastActiveAt, allocates the
// fresh wake channel for the *opposite* direction, persists the registry,
// then closes the wake channel for the current direction. The persist runs
// between the state flip and the wake so that any Activate/Evict waiter that
// observes the wake also sees a registry on disk consistent with newState.
//
// Lock order: lcMu is released before calling pool.persist so saveLocked's
// per-session lcMu re-acquire doesn't deadlock against us. lcMu is then
// re-acquired (after persist returns and releases Pool.mu) only to serialise
// the close against any concurrent Activate/Evict capturing the channel
// reference. The two lcMu acquisitions are sequential, not nested.
//
// On persist failure the wake channel still closes — a permanently-stuck
// waiter is a worse failure mode than a waiter that wakes to stale disk.
// The persist error propagates up Run, which treats it as fatal.
func (s *Session) transitionTo(newState lifecycleState) error {
	s.lcMu.Lock()
	s.lcState = newState
	s.lastActiveAt = time.Now().UTC()
	switch newState {
	case stateActive:
		// Fresh open channel for the next Evict to wait on. The current
		// direction's activeCh is left open here; closed below after persist.
		s.evictedCh = make(chan struct{})
	case stateEvicted:
		// Fresh open channel for the next Activate to wait on. The current
		// direction's evictedCh is left open here; closed below after persist.
		s.activeCh = make(chan struct{})
	}
	s.lcMu.Unlock()

	var persistErr error
	if s.pool != nil {
		persistErr = s.pool.persist()
	}

	// Wake waiters under lcMu to order the close against any concurrent
	// Activate/Evict capturing the channel reference. Single-shot per
	// direction: transitionTo is called only by the lifecycle goroutine, and
	// Run alternates directions, so each close fires exactly once per fresh
	// channel.
	s.lcMu.Lock()
	switch newState {
	case stateActive:
		close(s.activeCh)
	case stateEvicted:
		close(s.evictedCh)
	}
	s.lcMu.Unlock()

	return persistErr
}

// beginEvict commits the session to stateEvicted at the instant eviction is
// decided — BEFORE the child is torn down. It first fires the pool's transition
// observer (for a real eviction reason), THEN under lcMu flips lcState, stamps
// lastActiveAt, and swaps activeCh for a fresh open channel. A delivery that now
// races the teardown window (idle-timer fire, cap evict, or spontaneous exit)
// observes a non-active session: Activate signals activateCh and blocks on the
// fresh activeCh until a real respawn, instead of the pre-#1186 no-op against the
// dying child (the "acked but silently dropped" send). It deliberately does NOT
// close evictedCh and does NOT persist — endEvict does both, after the child has
// actually stopped, so the cap-policy Evict "blocks until the supervisor has
// stopped" contract still holds (closing evictedCh here would release Evict
// before the child was reaped → a transient active-cap overshoot).
//
// The signal fires BEFORE the flip so it is ordered ahead of stateEvicted
// becoming externally observable: a consumer that reads
// LifecycleState()==stateEvicted is then guaranteed the eviction transition has
// already fired (#1186 rework — the two-phase split moved the flip ahead of
// teardown but must NOT decouple it from the signal, which previously fired
// adjacent to the flip in Run's outer loop). notifyTransition is a leaf, off-lock
// callback (docs/lessons.md "Lock order with callback into the host"), so it MUST
// run outside lcMu — firing it before Lock keeps the observer contract while still
// ordering it ahead of the flip. reason == "" (the spontaneous-exit path) fires
// nothing: the wire has no "crashed" transition. s.pool != nil mirrors endEvict's
// guard for test-constructed sessions with no pool.
//
// It splits the old transitionTo(stateEvicted): beginEvict is transitionTo's
// pre-persist half (signal + flip + reset the opposite channel), run before
// teardown; endEvict is the persist + close-wake half, run after. transitionTo is
// retained unchanged for the stateActive (reactivation) direction.
func (s *Session) beginEvict(reason TransitionReason) {
	if reason != "" && s.pool != nil {
		s.pool.notifyTransition(SessionTransition{
			PreviousID: s.currentID(),
			Reason:     reason,
			OccurredAt: time.Now().UTC(),
		})
	}
	s.lcMu.Lock()
	s.lcState = stateEvicted
	s.lastActiveAt = time.Now().UTC()
	s.activeCh = make(chan struct{})
	s.lcMu.Unlock()
}

// endEvict finishes the eviction begun by beginEvict, after cancelSup/drainSup
// have stopped the child: it persists the registry (now consistent with
// stateEvicted) and then closes evictedCh to release any cap-policy Evict waiter.
// Persisting before the close preserves transitionTo's invariant that a waiter
// observing the wake also sees a registry on disk consistent with the new state.
// A registry-persist failure is NON-FATAL — memory is authoritative and the next
// transition re-persists the whole registry (self-healing) — so it is logged, not
// returned, exactly as the pre-#1186 transitionTo(stateEvicted) path did; the
// alternative would tear down every live session over one disk hiccup during a
// routine eviction.
func (s *Session) endEvict() {
	var persistErr error
	if s.pool != nil {
		persistErr = s.pool.persist()
	}

	s.lcMu.Lock()
	close(s.evictedCh)
	s.lcMu.Unlock()

	if persistErr != nil {
		s.log.Warn("session: registry persist failed on evict; keeping in-memory state, retrying next transition",
			"event", "session.persist_failed",
			"transition", "evicted",
			"err", persistErr)
	}
}
