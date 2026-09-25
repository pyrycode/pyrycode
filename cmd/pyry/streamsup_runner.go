package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// streamRunner adapts *streamsup.Runner to sessions.Runner. The shapes differ in
// exactly one method: sessions.Runner.State returns supervisor.State, but
// internal/streamsup must not import internal/supervisor (its package doc forbids
// it), so *streamsup.Runner declares State() streamsup.State instead. Go has no
// covariant return on interface satisfaction, so the concrete runner cannot
// satisfy the interface directly. This adapter — living in cmd/pyry, which knows
// both types — maps that one method and forwards the rest unchanged. The
// precedent for this covariant-return seam is poolResolver in main.go.
//
// The factory that constructs a streamRunner from a supervisor.Config —
// newStreamRunnerFactory below (#1109 delivered the constructor; #1098 gave it
// the turnevent sink) — the interactive_runner selection that injects it on
// sessions.Config.RunnerFactory is #1081.
//
// The embedded sessionRetentions carries the per-session holds newSessionParser mints:
// models the model-list retention added by #1840, commands the slash-command-list
// retention added by #2004, tasks the background-task-roster retention added by #2077, and
// windows the per-model context-window retention added by #2106. Every field is a pointer,
// so the adapter stays a value type and the compile-time sessions.Runner assertion below is
// unaffected.
//
// EMBEDDED rather than restated field by field, since #2106 made newSessionParser return
// the set as one value: the holds then cross the factory unsplit and cannot be bound apart
// on the way, the construction site sets one field instead of four, and a fifth hold costs
// neither this declaration nor the factory a line. sessionRetentions declares no methods,
// so nothing is promoted into this type's method set and the four accessors below reach
// their holds by ordinary field promotion.
//
// The install field is the #2446 seam: the three methods Pool.UpdateSettings
// drives go through it rather than straight to the runner, so the argv that
// update recomposes is shaped the way the construction path shapes one. A
// POINTER, unlike the embedded retentions above, because one of its fields
// moves — see settingsInstaller. A hand-built streamRunner{} leaves it nil,
// which is inert: the compile-time interface assertions in this package's tests
// are the only such values and they call nothing.
type streamRunner struct {
	r *streamsup.Runner
	sessionRetentions
	install *settingsInstaller
	// wrapUp is the #2477 reply capture chained into this runner's sink chain. A
	// POINTER, like install and for the same reason — a hand-built streamRunner{}
	// leaves it nil, which is inert: every method on a nil *wrapUpCapture refuses.
	wrapUp *wrapUpCapture
}

// BeginWrapUp arms this runner's wrap-up reply capture and returns the reply, the
// disarm the caller must run, and whether it armed (#2477).
//
// An OPTIONAL capability asserted for at the consumer, the posture
// contextUsageQuerier and the interrupt dispatch already keep: it stays OFF
// sessions.Runner because its only consumer is conversationReset in this package,
// and widening that interface would pull every test double in the tree into the
// slice for a method none of them can answer.
func (s streamRunner) BeginWrapUp() (*wrapUpReply, func(), bool) { return s.wrapUp.begin() }

// spawnArgvInstaller is the half of *streamsup.Runner that Pool.UpdateSettings
// drives — the two argv installs and the posture install that always precedes
// them. Declared as an interface rather than taking the concrete runner so the
// shaping below can be asserted against a recording double: the runner exposes
// no reader for its installed argv (setArgsLocked is its sole writer and there
// is no getter), so the alternative is spawning a child and reading its command
// line, which makes a timing-dependent integration test out of a pure argv
// transformation.
//
// Narrow on purpose. It is not a second sessions.Runner: it names the three
// methods whose inputs this file has to shape, and nothing else.
type spawnArgvInstaller interface {
	Restart(args []string)
	SetSpawnArgs(args []string)
	SetSpawnPermissionMode(mode string)
}

// settingsInstaller reapplies BOTH construction-time argv shapings to the argv
// Pool.UpdateSettings recomposes, then forwards it to the runner (#2446).
//
// # What it fixes
//
// Pool.UpdateSettings recomposes argv as Session.spawnBase + claudeSettingsArgs
// and installs it verbatim on either branch — SetSpawnArgs in band, Restart
// otherwise. Neither branch rebuilds the runner, so neither re-runs what
// newStreamRunnerFactory does to an argv on the way in:
//
//   - stripSessionIDFlags, which mapStreamsupConfig applies, because
//     streamsup.buildArgs injects the create or resume form itself per spawn.
//     Without it the id Pool.buildSession bakes into spawnBase survives, and the
//     next respawn that takes the resume form spawns "--session-id X … --resume
//     X" — which claude REFUSES outright, one stderr line and exit 1, so the
//     session crash-loops with every queued message stuck behind a child that
//     never lives. That is the 2026-09-15 incident this type exists for.
//   - withApprovalArgs, which puts the daemon's permission-approval flags on the
//     argv of every child the daemon downgrades in band. Without it such a
//     session respawns with no --permission-prompt-tool, no --mcp-config and no
//     --strict-mcp-config beside the --dangerously-skip-permissions every argv
//     carries since #2065 — the approval gate gone from a child that launches in
//     bypass. Masked until now by the crash-loop above; fixing the strip alone is
//     what makes it reachable.
//
// # The three fixed inputs, and the one that moves
//
// mcpServersPath, stdioPrompt and operatorBypass are settled before the runner
// exists and never change: the first two are newStreamRunnerFactory's own
// closure values, and the third is RunnerConfig.OperatorBypass, which the pool
// derives ONCE from the settings-free spawnBase.
//
// READING operatorBypass BACK OFF THE ARGV BEING INSTALLED WOULD BE THE WHOLE
// BUG AGAIN. claudeSettingsArgs appends the escalation flag to every
// composition, so a predicate keyed on the installed argv answers "this child
// keeps its bypass" for every session, withApprovalArgs returns args unchanged
// every time, and the approval gate silently disappears from every install. See
// operatorBypass' own doc in internal/sessions for the derivation; the rule here
// is that provenance travels in a field and is never re-derived downstream.
//
// mode is the session's STORED POSTURE and is the one value that moves, which is
// why this is a pointer type with a mutex rather than a value copied per call.
// Pool.UpdateSettings calls SetSpawnPermissionMode(merged.PermissionMode)
// unconditionally, immediately above its branch split and therefore immediately
// before either install, so the posture recorded here is the one that update
// just persisted. It is seeded from RunnerConfig.PermissionMode so an install
// arriving before any posture install still shapes with a real mode — and a
// degenerate value is fail-safe anyway, since withApprovalArgs treats an
// unrecognised or empty posture as a downgraded child and leaves the gate on.
//
// # Precondition on args
//
// args is a SETTINGS-COMPOSED argv — Session.spawnArgs output, i.e. spawnBase
// plus claudeSettingsArgs — and never an already-shaped one. The shaping is not
// idempotent for the approval set: handed its own output it would append a
// second --permission-prompt-tool / --mcp-config pair. The sole caller
// recomposes from spawnBase on every update, so nothing reaches here twice, and
// a future caller that wants to re-install the runner's current argv needs a
// different entry point rather than this one.
type settingsInstaller struct {
	target         spawnArgvInstaller
	mcpServersPath string
	stdioPrompt    bool
	operatorBypass bool

	mu   sync.Mutex
	mode string
}

func newSettingsInstaller(target spawnArgvInstaller, mcpServersPath string, stdioPrompt, operatorBypass bool, mode string) *settingsInstaller {
	return &settingsInstaller{
		target:         target,
		mcpServersPath: mcpServersPath,
		stdioPrompt:    stdioPrompt,
		operatorBypass: operatorBypass,
		mode:           mode,
	}
}

// shape applies the two construction-time shapings in the construction-time
// ORDER — strip first, then inject — so that for one session and one posture the
// installed argv is what newStreamRunnerFactory would have produced for the same
// composition. The order is load-bearing in one direction only: withApprovalArgs
// consults namesPermissionMode on what it is given, and stripSessionIDFlags
// never adds or removes a --permission-mode token, so reversing them would agree
// today — but the strip has to run on the pool's composition rather than on an
// argv already carrying injected flags, which is the order stated here.
//
// The returned slice aliases neither args nor spawnBase: stripSessionIDFlags
// always allocates, and withApprovalArgs either returns that fresh slice or a
// clone of it.
func (s *settingsInstaller) shape(args []string) []string {
	s.mu.Lock()
	mode := s.mode
	s.mu.Unlock()
	return withApprovalArgs(stripSessionIDFlags(args), s.mcpServersPath, mode, s.operatorBypass, s.stdioPrompt)
}

// setSpawnPermissionMode records the posture the shaping above reads, then
// forwards it to the runner. Recording FIRST is not an ordering requirement —
// neither half can fail and the forward makes no call back into this type — but
// it keeps the cell and the runner from disagreeing for the length of a
// non-blocking call.
//
// The mutex is a leaf: nothing under it takes another lock or does I/O, and the
// forward happens after the unlock.
func (s *settingsInstaller) setSpawnPermissionMode(mode string) {
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
	s.target.SetSpawnPermissionMode(mode)
}

func (s *settingsInstaller) restart(args []string) { s.target.Restart(s.shape(args)) }

func (s *settingsInstaller) setSpawnArgs(args []string) { s.target.SetSpawnArgs(s.shape(args)) }

func (a streamRunner) State() sessions.State { return mapStreamState(a.r.State()) }

func (a streamRunner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	return a.r.WriteUserTurn(ctx, conversationID, payload)
}

func (a streamRunner) WaitForPTY(ctx context.Context) error { return a.r.WaitForPTY(ctx) }

func (a streamRunner) Run(ctx context.Context) error { return a.r.Run(ctx) }

// Restart installs the recomposed argv WITH the kill, through the #2446 shaping
// seam rather than straight to the runner: the argv Pool.UpdateSettings hands
// down is the pool's own composition, which still carries the baked session id
// and carries no approval flags. settingsInstaller states what that costs when
// it is forwarded verbatim.
func (a streamRunner) Restart(args []string) { a.install.restart(args) }

// SetSpawnArgs forwards to (*streamsup.Runner).SetSpawnArgs (#1580), installing
// the next spawn's argv without terminating the running child. Unlike Interrupt /
// RestartFresh / BeginRotation below, it is ON the sessions.Runner interface: that
// interface has exactly one production implementation (this adapter) and five test
// doubles, all in this repo, so widening is compile-checked across the whole set —
// whereas a type assertion at a future call site would fail silently at runtime and
// fall back to Restart, which is the one outcome a swap-only caller exists to
// avoid. Dispatched from the in-band branch of Pool.UpdateSettings since #1581.
//
// It goes through the #2446 shaping seam for Restart's reason exactly: the two
// branches differ in whether they kill the live child, not in what argv they
// hand down, so a shaping applied to one and not the other would fix half an
// incident.
func (a streamRunner) SetSpawnArgs(args []string) { a.install.setSpawnArgs(args) }

// SetModel forwards the non-turning live model change to the stream supervisor.
// Validation stays at the relay boundary; this adapter neither logs nor rewrites
// the requested value.
func (a streamRunner) SetModel(model string) error { return a.r.SetModel(model) }

// SetSpawnPermissionMode forwards to (*streamsup.Runner).SetSpawnPermissionMode
// (#2064), installing the posture the runner's next and every later spawn asserts to
// its child in-band. It is ON the sessions.Runner interface for SetSpawnArgs' reason,
// with the stakes one step higher: its consumer is Pool.UpdateSettings inside
// internal/sessions, and a type assertion there whose unmatched arm silently no-ops
// would leave every respawned child asserting the posture this daemon started with —
// which can re-loosen one the operator has since tightened.
//
// The concrete method installs the mode verbatim and cannot fail, so this forward
// carries no validation of its own; the vocabulary gate runs at the spawn, where the
// runner's own allow-list decides whether anything is written at all.
//
// Since #2446 it also records the posture for the argv shaping the two installs
// below apply. That is why the seam can shape at all: the stored posture is the
// one shaping input that moves, and Pool.UpdateSettings calls this
// unconditionally above its branch split — immediately before either install —
// so the value recorded here is always the one that update just persisted.
func (a streamRunner) SetSpawnPermissionMode(mode string) { a.install.setSpawnPermissionMode(mode) }

// SetPermissionMode forwards to (*streamsup.Runner).SetPermissionMode (#2042),
// switching the live child's permission posture via a set_permission_mode control
// request rather than a respawn. It is ON the sessions.Runner interface for the
// fail-open reason stated above: its consumer is Pool.deliverSettingsInBand inside
// internal/sessions, and an assertion there whose unmatched arm silently no-ops
// would leave a child in the wrong posture while the update reports success.
//
// It is the whole posture seam since #2043: the revoke-only RevokeBypass forward
// that used to sit here went with the interface method when Pool.UpdateSettings
// started sending an operator-chosen mode. (*streamsup.Runner).RevokeBypass still
// exists with its own coverage; this adapter no longer reaches it.
//
// The concrete method refuses any mode outside its closed allow-list, so this
// forward carries no validation of its own; adding one here would be a second
// vocabulary to keep in step with the writer's.
func (a streamRunner) SetPermissionMode(mode string) error { return a.r.SetPermissionMode(mode) }

// ConfirmedPermissionMode forwards the current-child informational read added by
// #2511. It stays off sessions.Runner because its only consumer is the
// conversation-keyed settings resolver in this package.
func (a streamRunner) ConfirmedPermissionMode() (string, bool) {
	return a.r.ConfirmedPermissionMode()
}

// BeginTeardown forwards to (*streamsup.Runner).BeginTeardown (#1513), arming the
// write-refusal gate for a deliberate kill so a racing turn is refused and retried
// against the successor instead of being written into the dying child — where the
// write returns nil and msgqueue drops the queue head as committed.
//
// It is ON the sessions.Runner interface for the fail-open reason stated above, and
// here that reason IS the ticket: its consumers are Session.runActive's two eviction
// arms and Pool.UpdateSettings' restart branch, all inside internal/sessions, and an
// assertion whose unmatched arm silently no-oped would leave the teardown ungated
// while every layer reported success — the exact silent loss being closed.
//
// The concrete method cannot fail and is non-blocking, so this forward adds nothing;
// the release rule that distinguishes it from the rotation arm lives entirely in the
// runner.
func (a streamRunner) BeginTeardown() { a.r.BeginTeardown() }

// Interrupt forwards to (*streamsup.Runner).Interrupt (#1120), ending the running
// turn via a control_request line. It satisfies sessions.Runner.Interrupt, which
// the #1121 interrupt dispatch (interruptRunner in main.go) calls (#2592).
func (a streamRunner) Interrupt() error { return a.r.Interrupt() }

// RestartFresh forwards to (*streamsup.Runner).RestartFresh (#1124), rotating the
// runner's persistent id to newID so the next spawn uses --session-id <newID> (a
// fresh transcript, no fork). It satisfies sessions.Runner.RestartFresh, which the
// #1125 new_session dispatch (startFreshRunner in main.go) calls (#2592).
func (a streamRunner) RestartFresh(newID string) { a.r.RestartFresh(newID) }

// BeginRotation forwards to (*streamsup.Runner).BeginRotation (#1330), arming the
// rotation gate so no turn accepted once the rotation has begun is written into
// the outgoing child, and returning the disarm for startFreshRunner's rotate-error
// path. It satisfies sessions.Runner.BeginRotation (#2592).
func (a streamRunner) BeginRotation() func() { return a.r.BeginRotation() }

// ModelList reports the model list this session's child last named in its
// initialize reply (#1840), or ok == false when no child has reported one. It
// reads the hold the factory bound to this runner's parser, so it answers outside
// a turn, on any goroutine, with no turn in flight.
//
// It is a concrete method OFF the sessions.Runner interface. The rule is that the
// interface carries a method when a runner lacking it would fail open, silently
// dropping an operation (see the Runner doc). This one's consumer is #1837's
// publisher in cmd/pyry, which reaches it by type assertion off Session.Runner, and a
// runner without it merely reports no model list — so widening the interface would
// buy no compile-time guarantee and would drag every fake runner under
// internal/sessions and cmd/pyry into the diff.
func (a streamRunner) ModelList() (turnevent.ModelList, bool) { return a.models.ModelList() }

// SlashCommandList reports the slash-command inventory this session's child last named
// in its initialize reply (#2004), or ok == false when no child has reported one. It
// reads the hold newSessionParser bound to this runner's parser, so it answers outside
// a turn, on any goroutine, with no turn in flight — and a runner whose retention was
// never minted answers the unreported state rather than panicking, the read being
// nil-receiver-safe.
//
// It is a concrete method OFF the sessions.Runner interface, for ModelList's reason
// exactly rather than by resemblance to it. This one's consumer is #2005's resolver in
// cmd/pyry, which reaches it by type assertion off Session.Runner — so widening the
// interface would buy no compile-time guarantee and would drag every fake runner
// under internal/sessions and cmd/pyry into the diff.
func (a streamRunner) SlashCommandList() (turnevent.SlashCommandList, bool) {
	return a.commands.SlashCommandList()
}

// BackgroundTaskRoster reports the set of background tasks this session's child last
// said it was tracking (#2077), or ok == false when no child has reported a roster at
// all. It reads the hold newSessionParser bound to this runner's parser, so it answers
// outside a turn, on any goroutine, with no turn in flight — and a runner whose
// retention was never minted answers the unreported state rather than panicking, the
// read being nil-receiver-safe.
//
// The two false-y answers are NOT the same answer, and that is this variant's own
// property rather than the two forwards' above: ok == false means no roster has ever
// been reported, while ok == true with no entries means claude reported that nothing is
// alive. sessionBackgroundTaskHold.BackgroundTaskRoster's doc gives the derivation; a
// caller that collapses the two reconciles a live session as a silent one.
//
// It is a concrete method OFF the sessions.Runner interface, for ModelList's and
// SlashCommandList's reason exactly rather than by resemblance to them. This one's
// consumer is #2079's resolver in cmd/pyry, which reaches it by type assertion off
// Session.Runner — so widening the interface would buy no compile-time guarantee and
// would drag every fake runner under internal/sessions and cmd/pyry into the diff.
func (a streamRunner) BackgroundTaskRoster() (turnevent.BackgroundTaskRoster, bool) {
	return a.tasks.BackgroundTaskRoster()
}

// ModelWindows reports the per-model context windows this session's child last named in a
// `result` line, or ok == false when no child has reported a usable one (#2106). It reads
// the hold newSessionParser bound to this runner's parser, so it answers outside a turn, on
// any goroutine, with no turn in flight — and a runner whose retention was never minted
// answers the unreported state rather than panicking, the read being nil-receiver-safe.
//
// A turn that reported nothing usable does NOT erase what is retained, so what comes back is
// the newest USABLE report rather than the last turn's: sessionModelWindowHold.Sink's doc
// gives the derivation, and a caller reading this as "the window as of the last turn" would
// be wrong on any run whose most recent turn said nothing about models.
//
// Both obligations that ride the returned value — sanitization is the consumer's, and a
// model id is not an argv token — are stated on sessionModelWindowHold.ModelWindows and are
// not repeated here; this forward adds no judgement of its own.
//
// It is a concrete method OFF the sessions.Runner interface, for the reason the three
// read forwards above share rather than by resemblance to them. This one's consumer is
// #2107, in cmd/pyry, which reaches it by type assertion off Session.Runner — so widening
// the interface would buy no compile-time guarantee and would drag every fake runner
// under internal/sessions and cmd/pyry into the diff.
//
// Returning a package-private type is what that placement makes possible and costs nothing:
// every consumer is in this package. Should a later ticket need the value outside it, the
// export is a rename at one declaration rather than a redesign.
func (a streamRunner) ModelWindows() (modelWindowReport, bool) { return a.windows.ModelWindows() }

// ClaudeSessionsDir reports the directory claude writes this session's <id>.jsonl
// into — the folder this runner's own spawn probe keys on (#2423). "" means the
// derivation degraded and no folder can be named, which is the same inert state
// streamsup.Config.ClaudeSessionsDir gives the probe.
//
// IT FORWARDS THE RUNNER'S LIVE FIELD, NOT A DERIVATION, and that is the method's
// whole reason to exist. The runner holds the folder its own spawn probe keys on,
// so the usage reader (sessionTranscriptDir → snapshotUsageFor) and that probe read
// one field of one struct and cannot name different folders for one session.
// Re-deriving here from a workdir would agree today and is the shape that drifts:
// ResolveWorkdir applies canonicalCase and symlink resolution, which the confining
// validators do not, so a differently-spelled workdir would name a folder claude
// never writes — the failure streamClaudeSessionsDir's own doc enumerates.
//
// It FORWARDED A STORED COPY until #1475, when a new_session rotation gained the
// ability to move the session's spawn directory. A copy taken at construction
// would report the pre-move folder from then on, which is the #2423 reading
// ("Context: 0%") re-introduced for exactly the conversations that moved. The
// value is no longer fixed at construction, so there is now something to ask the
// runner about — but still nothing to ask a live CHILD about, so the forward stays
// a plain field read and no turn boundary is involved.
//
// It is a concrete method OFF the sessions.Runner interface, for the reason the read
// forwards above share rather than by resemblance to them. This one's consumer is
// #2423's resolver in cmd/pyry, which reaches it by type assertion off Session.Runner.
//
// SECURITY: the value derives from a directory a paired client can choose
// (resolveSpawnDir's confined, symlink-resolved output). It is a folder the daemon
// composed, never the requested text, and it must stay off every surface — its one
// consumer turns it into two integers. It must NEVER be used to build a claude
// argument: what crosses into streamsup is a probe input, not a flag
// (mapStreamsupConfig's doc states that no resolver crosses and neither side scans),
// and a device-influenced directory reaching exec.Command would be a different
// design.
func (a streamRunner) ClaudeSessionsDir() string { return a.r.ClaudeSessionsDir() }

// SetSpawnWorkDir installs the directory this session's NEXT spawn chdirs into —
// the seam a new_session rotation uses to bring the successor up in the workspace
// the operator recorded on the conversation (#1475). It restarts nothing; the
// caller pairs it with RestartFresh.
//
// THE DERIVATION LIVES HERE, not in streamsup, because mapStreamsupConfig already
// does it here with this same helper: one call site for the construction-time
// value and one for the swap, both streamClaudeSessionsDir, so the two can never
// name different folders for one workdir. streamsup cannot do it itself —
// sessions.DefaultClaudeSessionsDir is the sessions package's, and the mapper
// exists precisely so streamsup imports neither it nor this one.
//
// The runner applies ResolveWorkdir to workDir under its own contract, and
// streamClaudeSessionsDir applies it to the same input on the way to the projects
// folder, so the pair the runner installs is derived from one resolution of one
// path. A degraded derivation answers "" — the inert probe state — rather than a
// wrong folder, which is streamClaudeSessionsDir's documented contract.
//
// It is a concrete method OFF the sessions.Runner interface: its consumer is
// installSpawnDir in this package, reaching it by type assertion off
// Session.Runner, and a runner without it still rotates, just in place.
//
// SECURITY: workDir is resolveSpawnDir's confined, symlink-resolved output and
// nothing else — the caller re-runs that validator on the recorded value at
// rotation time rather than trusting it, because a path confined when it was
// stored can be re-pointed before it is spawned in. A refusal never reaches here;
// it leaves the runner in the directory it already had.
func (a streamRunner) SetSpawnWorkDir(workDir string) error {
	return a.r.SetSpawnWorkDir(workDir, streamClaudeSessionsDir(workDir))
}

// QueryMCPStatus asks this runner's exact live child for a requester-private
// status reading. It stays off sessions.Runner because its only consumer is the
// conversation resolver in this package.
func (a streamRunner) QueryMCPStatus(ctx context.Context) (turnevent.MCPStatus, bool) {
	return a.r.QueryMCPStatus(ctx)
}

// QueryContextUsage asks this runner's exact live child for a requester-private
// context-window reading at detail. It stays off sessions.Runner for QueryMCPStatus's
// reason above: its only consumer is contextUsageResolver in this package.
//
// THE DETAIL IS THE CALLER'S AND IS NOT CHECKED HERE. streamsup's own allow-list is
// its sole validator and refuses an unsupported value before minting a request id or
// writing anything, so a second check here would be a second place that vocabulary is
// decided. The one production caller passes a compile-time constant.
func (a streamRunner) QueryContextUsage(ctx context.Context, detail string) (turnevent.ContextUsage, bool) {
	return a.r.QueryContextUsage(ctx, detail)
}

// QueryAppliedSettings asks this runner's exact live child for the bounded model
// and nullable effort Claude applied. It stays off sessions.Runner because #2505
// adds no general session-lifecycle consumer; the client projection belongs to
// #2507 and will assert this optional concrete capability.
func (a streamRunner) QueryAppliedSettings(ctx context.Context) (streamsup.AppliedSettings, bool) {
	return a.r.QueryAppliedSettings(ctx)
}

// ReconnectMCPServer and SetMCPServerEnabled forward the two MCP actuations #2418
// landed on the concrete runner. They are the WRITE half of the pair whose read half is
// QueryMCPStatus above and stay off sessions.Runner for its reason: their only consumer
// is mcpActuatorV2 in this package, and widening that interface would pull every test
// double into the slice.
//
// NEITHER FORWARD ADDS A CHECK, and the absence is the design rather than an omission.
// Membership of the named server and the asking device's authorization are settled
// ABOVE this adapter, in the one place that can audit them; the concrete methods say
// the same thing from below. A check here would be a third opinion on a decision that
// already has exactly one owner.
func (a streamRunner) ReconnectMCPServer(ctx context.Context, serverName string) bool {
	return a.r.ReconnectMCPServer(ctx, serverName)
}

func (a streamRunner) SetMCPServerEnabled(ctx context.Context, serverName string, enabled bool) bool {
	return a.r.SetMCPServerEnabled(ctx, serverName, enabled)
}

// mapStreamState maps streamsup's native lifecycle snapshot to supervisor.State.
// The two types mirror each other field-for-field; Phase maps by a plain string
// conversion because the phase values are identical across the two packages.
func mapStreamState(s streamsup.State) sessions.State {
	return sessions.State{
		Phase:        sessions.Phase(string(s.Phase)),
		ChildPID:     s.ChildPID,
		StartedAt:    s.StartedAt,
		RestartCount: s.RestartCount,
		LastUptime:   s.LastUptime,
		NextBackoff:  s.NextBackoff,
	}
}

// Interactive stream runner factory wiring returns a sessions.RunnerFactory (func(supervisor.Config)
// (sessions.Runner, error)) that constructs a stream-json runner AND installs the
// #1088 turnevent Parser as its Config.Stdout, with the Parser's sink bound to
// sink for the runner's session (#1098). The factory captures the
// daemon-singleton fan-in sink once; each per-session invocation mints a
// streamSessionTag seeded with cfg.SessionID and binds a fresh Parser to
// sink.sinkForTag(tag.ID), so every runner's turnevents fan into the one drain
// tagged by their pool session id. The install lives HERE, not in
// mapStreamsupConfig, so the mapper stays pure (its Stdout == nil assertion is
// untouched) — the Parser is a runtime object, one layer up.
//
// #1133 makes that tag LIVE rather than frozen, and the tag is minted FIRST — above
// newSessionParser, which is itself above streamsup.New — because both halves have
// to read the one object: the Parser is built before the runner exists, and the
// runner is what moves the tag. Before it, the session id was captured directly by
// the two lane closures, so a stream-mode new_session left every later event tagged
// with an id the pool had rebound away from and the drain dropped all of them until
// the daemon restarted. tag.Rotate goes onto Config.OnSessionRotate below, so the
// half that MOVES the tag and the halves that READ it are the same object by
// construction and cannot be bound apart — #1840's argument for minting a parser
// and its holds in one call, and #2064's for the posture gate, applied a third
// time.
//
// #1210 installs the SECOND lane onto the same fan-in: the runner's child-exit
// callback, which closes the turn of a conversation whose child died mid-turn (no
// result line for the abandoned turn, and no pool transition — the two feeds that
// are structurally silent on that path). The two installs bind from the ONE tag and
// sit adjacent on purpose: identical session tags on both lanes are what let the
// drain's exit arm clear exactly the conversation whose events it is ordered
// behind, and reading them as a pair is the evidence, not a derivation. exitForTag
// is INSTALLED rather than re-derived —
// it owns the non-blocking send and the Warn drop diagnostic the seam's
// must-not-block / must-not-panic contract requires, so a hand-rolled func() here
// would duplicate that contract instead of consuming it.
//
// #1840 puts the per-session model-list retention INSIDE that install: the Parser
// and its sessionModelHold come from one newSessionParser call, so the parser's
// sink is the hold's decorator and the hold reaches the returned adapter on the
// next line. The halves are minted together precisely so they cannot be bound
// to different holds, and each decorator stores BEFORE forwarding to sinkFor —
// which is what keeps the retention upstream of the droppable send that can
// otherwise discard the one initialize reply a child ever sends. #2004 adds the
// slash-command retention as a SECOND decorator on that same chain, on the same
// argument and from the same call, #2077 the background-task roster as a THIRD, and
// #2106 the per-model context windows as a FOURTH. The last two are the ones whose
// producers fire repeatedly rather than once per child, so what the retention buys there
// is an answer BETWEEN reports rather than a value that would otherwise be lost forever.
// Since #2106 the four cross this seam as one sessionRetentions value, which the adapter
// embeds — so they cannot be bound apart on the way and a fifth costs this site no line.
//
// #1109 constructed the runner via streamsup.New (the first caller tree-wide) and
// deliberately left Config.Stdout nil for this ticket to fill. It is the arm the
// #1081 interactive_runner selection assigns to sessions.Config.RunnerFactory;
// this ticket delivers the factory + the drain it feeds, not the production wiring.
//
// There is NO silent PTY fallback: a streamsup.New error (empty SessionID —
// impossible at the pool sites per #1108; missing binary via exec.LookPath;
// absent WorkDir via agentrun.ResolveWorkdir) is wrapped and returned with a nil
// runner. Substituting supervisor.New here would silently diverge the primary
// interactive session (which `pyry attach` drives) from the operator's stated
// stream-json intent. The error surfaces through the pool's existing
// "sessions: … supervisor: %w" wraps at both construction sites.
//
// mcpServersPath is the daemon-global --mcp-config file written once at startup
// (runSupervisor, gated on stream-json). withApprovalArgs injects the #1106
// permission-approval flags onto every non-yolo spawn's args (#1168) — this is
// the sole stream-path-specific spawn-construction site and covers both the
// bootstrap runner and per-conversation runners, so a per-conversation stream
// session cannot silently bypass the approval gate. On the "" / "pty" path the
// factory is never built, so mcpServersPath is "" and unused there. The live
// wire is exercised end-to-end by TestInteractiveStreamModalResolution (#1154).

// streamApprovalConfig carries the optional stdio transport's daemon-singleton
// dependencies into each per-session handler. A zero value preserves the MCP path.
type streamApprovalConfig struct {
	stdio    bool
	registry *permbridge.Registry
	timeout  time.Duration
	surface  *approvalSurfaceReport
}

// approvalSurfaceReport bridges the composition-order gap between runner creation
// and relay construction. set runs before pool.Run; every reader starts from a
// goroutine created by pool.Run, so goroutine publication makes the single write
// visible without a mutex.
type approvalSurfaceReport struct {
	show func(permbridge.Request) func()
}

func (r *approvalSurfaceReport) set(show func(permbridge.Request) func()) {
	if r != nil {
		r.show = show
	}
}

func (r *approvalSurfaceReport) surface(req permbridge.Request) func() {
	if r == nil || r.show == nil {
		return func() {}
	}
	return r.show(req)
}

const (
	reasonStdioInvalidRequest = "permission request was invalid"
	reasonStdioDuplicate      = "permission request was already pending"
	reasonStdioChildExit      = "permission request ended with its Claude process"
)

// stdioPermissionHandler adapts one runner's can_use_tool requests onto the
// daemon-wide approval registry. live contains only requests originating from the
// runner that owns this handler, allowing child exit to fail them closed without
// disturbing another session's approvals.
type stdioPermissionHandler struct {
	registry *permbridge.Registry
	timeout  time.Duration
	surface  *approvalSurfaceReport

	mu   sync.Mutex
	live map[string]*permbridge.Pending
}

func newStdioPermissionHandler(registry *permbridge.Registry, timeout time.Duration, surface *approvalSurfaceReport) *stdioPermissionHandler {
	return &stdioPermissionHandler{
		registry: registry,
		timeout:  timeout,
		surface:  surface,
		live:     make(map[string]*permbridge.Pending),
	}
}

// handle is called synchronously by Parser on the child's stdout-forwarding
// goroutine. Registration is bounded local work; surfacing, waiting, and response
// I/O run asynchronously so later stdout can always drain.
func (h *stdioPermissionHandler) handle(req streamsup.CanUseToolRequest, origin io.Writer) {
	if req.RequestID == "" || req.ToolUseID == "" || h.registry == nil || origin == nil {
		go func() {
			_ = streamsup.WriteCanUseToolDeny(origin, req.RequestID, reasonStdioInvalidRequest, false)
		}()
		return
	}

	parked := permbridge.Request{
		ToolName:                req.ToolName,
		Input:                   req.Input,
		ToolUseID:               req.ToolUseID,
		DecisionReason:          req.DecisionReason,
		DecisionReasonType:      req.DecisionReasonType,
		BlockedPath:             req.BlockedPath,
		Description:             req.Description,
		DefaultToNo:             req.DefaultToNo,
		RequiresUserInteraction: req.RequiresUserInteraction,
		AlwaysAllow:             permbridge.ParseAlwaysAllow(req.PermissionSuggestions, req.SuppressAlwaysAllowRule),
	}
	pending, err := h.registry.Register(req.ToolUseID, parked, h.timeout)
	if err != nil {
		go func() {
			_ = streamsup.WriteCanUseToolDeny(origin, req.RequestID, reasonStdioDuplicate, false)
		}()
		return
	}

	h.mu.Lock()
	h.live[req.ToolUseID] = pending
	h.mu.Unlock()

	go h.await(req.RequestID, parked, pending, origin)
}

func (h *stdioPermissionHandler) await(requestID string, req permbridge.Request, pending *permbridge.Pending, origin io.Writer) {
	retire := h.surface.surface(req)
	verdict := pending.Await()

	h.mu.Lock()
	if h.live[req.ToolUseID] == pending {
		delete(h.live, req.ToolUseID)
	}
	h.mu.Unlock()

	switch verdict.Behavior {
	case permbridge.BehaviorAllow:
		_ = streamsup.WriteCanUseToolAllow(origin, requestID, verdict.UpdatedInput, verdict.UpdatedPermissions)
	default:
		_ = streamsup.WriteCanUseToolDeny(origin, requestID, verdict.Message, false)
	}
	retire()
}

// childExited resolves only this runner's outstanding asks. The registry one-shot
// arbitrates an answer or timeout racing the exit; await remains the sole writer.
func (h *stdioPermissionHandler) childExited() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.live))
	for id := range h.live {
		ids = append(ids, id)
	}
	h.mu.Unlock()

	for _, id := range ids {
		h.registry.Resolve(id, permbridge.Deny(reasonStdioChildExit))
	}
}

// newStreamRunnerFactory binds the shared event sink, the daemon-wide model
// vocabulary store and the optional stdio approval transport to each streamsup
// runner it constructs.
//
// vocab may be nil — a daemon that persists no vocabulary (every test factory, and
// any host built without a state directory) then gets a runner whose chain is
// byte-identical to the pre-#2450 one, because the decorator that would feed the
// store is simply not inserted.
func newStreamRunnerFactory(sink *streamTurnSink, mcpServersPath string, vocab *modelVocabularyStore, approval streamApprovalConfig) sessions.RunnerFactory {
	return func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
		scfg := mapStreamsupConfig(cfg)
		// The posture and its provenance are BOTH read off cfg, never off scfg.Args:
		// since #2065 every argv carries the escalation flag, so the assembled argv
		// cannot say which children the daemon downgrades and which keep the bypass
		// they launch with. See withApprovalArgs' doc for the derivation.
		scfg.Args = withApprovalArgs(scfg.Args, mcpServersPath, cfg.PermissionMode, cfg.OperatorBypass, approval.stdio)
		// The same path that withApprovalArgs may put on the child argv is the
		// provenance anchor for automatic MCP status. The runner compares it with
		// each completed spawn argv, so bypass children whose args stayed unchanged
		// remain ineligible.
		scfg.MCPStatusConfigPath = mcpServersPath
		tag := newStreamSessionTag(cfg.SessionID)
		// #2135 chains the announced-reset follower between the retention holds and
		// the fan-in send, rather than inside newSessionParser: it retains nothing,
		// and it needs the two per-runner objects on this line — the live tag and the
		// pool callback — which that function has no business knowing about. The
		// placement it DOES need is being on the parser's side of the channel, which
		// the chain's own doc states is what the retentions get from, and only from,
		// sitting here.
		follow := newSessionResetFollower(tag, cfg.AdoptAnnouncedReset, sink.sinkForTag(tag.ID), cfg.Logger)
		contextUsage := newTurnEndContextUsageRequester(follow.Sink, cfg.Logger)
		// #2450 chains the vocabulary persister at the head of the same run of
		// non-retaining decorators, for the reason the two above it sit here: it needs
		// a per-DAEMON object newSessionParser has no business knowing about, and
		// threading a persist parameter through the hold constructors would touch
		// every one of their call sites for no behaviour gained. WHERE in the chain it
		// sits is immaterial — what puts a link upstream of the droppable fan-in send
		// is sitting on the PARSER'S SIDE of the channel, which the chain's own doc
		// states — but being upstream is not: turnMarkFor answers turnMarkNone for
		// ModelList, one initialize exchange per child produces exactly one of these,
		// and no later event replaces it, so a persist point below the send could lose
		// the list for the whole life of the child.
		// #2477 chains the wrap-up reply capture into the same run of non-retaining
		// decorators, and for the placement reason the two below it were chained here
		// rather than being folded into newSessionParser: what puts a link upstream of
		// the droppable fan-in send is sitting on the PARSER'S SIDE of the channel.
		// Here that is not merely sufficient but necessary, twice over — turnMarkFor
		// answers turnMarkOpen for TextChunk, so assistant text is droppable class and
		// sinkForTag refuses it at droppableCap; and the drain's active-session gate
		// drops every event of a BACKGROUND conversation, which is most of the ones a
		// reset names. A capture below either writes a silently truncated note. See
		// wrapUpCapture's own doc block.
		wrapUp := newWrapUpCapture(contextUsage.Sink)
		parserSink := wrapUp.Sink
		if vocab != nil {
			parserSink = vocab.sinkFor(parserSink)
		}
		parser, held := newSessionParser(parserSink, cfg.Logger)
		scfg.Stdout = parser
		onChildExit := sink.exitForTag(tag.ID)
		var r *streamsup.Runner
		if approval.stdio {
			handler := newStdioPermissionHandler(approval.registry, approval.timeout, approval.surface)
			parser.SetCanUseToolHandler(func(req streamsup.CanUseToolRequest) {
				handler.handle(req, r.Stdin())
			})
			scfg.OnChildExit = func() {
				handler.childExited()
				onChildExit()
			}
		} else {
			scfg.OnChildExit = onChildExit
		}
		scfg.OnSessionRotate = tag.Rotate
		// #2064's posture gate is bound HERE rather than in mapStreamsupConfig, on the
		// same dividing line Stdout sits on: it is a runtime object, not a plain value
		// derived from a RunnerConfig field. Taken from the parser that was just built
		// — the parser mints it, so the half that RELEASES the gate and the half that
		// ARMS it are the same object by construction and cannot be bound apart. That
		// is #1840's argument for minting the parser and its holds in one call, applied
		// to a seam whose two ends sit in different packages.
		scfg.PostureGate = parser.PostureGate()
		var err error
		r, err = streamsup.New(scfg)
		if err != nil {
			return nil, fmt.Errorf("cmd/pyry: stream runner: %w", err)
		}
		// #2136's runner half, closed here rather than above because the runner does not
		// exist up there: the follower's Sink is what the parser writes into, and that
		// parser is this runner's Stdout. This line is still inside the factory's own
		// single-goroutine window and above anything that starts Run, so the assignment
		// is published to the stdout forwarder by the same goroutine-creation edge the
		// three Config seams on the lines above rely on. The concrete *streamsup.Runner
		// is reached directly: sessions.Runner carries a method only when its consumer
		// sits inside internal/sessions, and this consumer sits here.
		follow.adoptRunner = r.AdoptSessionID
		contextUsage.request = r.RequestContextUsage
		// The probe's OWN folder is NOT copied onto the adapter. It travels in
		// scfg.ClaudeSessionsDir, which streamsup.New seeds the runner's live field
		// from, and the usage reader forwards to that field (#2423, made live at
		// #1475 so a rotation that moves the workdir moves the reading with it). One
		// field on one struct, so the reader and the probe cannot name different
		// folders for one session; see ClaudeSessionsDir's doc for what a second
		// derivation would cost.
		//
		// #2446's install seam is built from the SAME four values withApprovalArgs
		// was called with at the top of this function — each named once, none
		// re-derived — so the construction path and the settings-update install
		// path cannot be handed different shaping inputs. It sits below
		// streamsup.New because the runner is what it forwards to.
		install := newSettingsInstaller(r, mcpServersPath, approval.stdio, cfg.OperatorBypass, cfg.PermissionMode)
		return streamRunner{r: r, sessionRetentions: held, install: install, wrapUp: wrapUp}, nil
	}
}

// withApprovalArgs injects claude's permission-approval flags onto a stream
// spawn's args unless the spawn's child will actually RUN in bypass (#1168, and
// #2065 for how that question is now answered). It is the interactive-stream twin
// of agent_run.go's non-yolo permissionArgs wiring — the first live consumer of
// permissionArgs on the interactive path.
//
// # Why the flag's presence stopped being the signal
//
// Until #2065 yolo was expressed to claude as exactly one flag,
// --dangerously-skip-permissions, both entry points funnelled through it — the
// operator's bootstrap pass-through claude args (main.go) and
// internal/sessions.claudeSettingsArgs, which appended it for a set YOLO bit — and
// so its presence in the spawn's args WAS the single deterministic yolo signal.
//
// #2065 makes claudeSettingsArgs append it to EVERY argv, because the posture is
// now decided by an in-band write rather than by the launch argv. A predicate
// reading the flag therefore answers "yolo" for every session, the approval set
// stops being injected anywhere, and the daemon's approval gate quietly
// disappears for every non-bypass session. So the question moved: not "is the flag
// there" but "will this child KEEP the bypass it launches with", which is true in
// exactly two cases, both of them decided before this function runs:
//
//   - storedMode is the escalation — internal/streamsup's SPAWN path writes no
//     posture for it, so nothing walks the child back. Still by non-membership,
//     but since #2066 the predicate is permissionModeSpawnWritable rather than the
//     writer's allow-list, which admits the escalation now so that a LIVE child can
//     be escalated in band. The verdict on this row is unchanged; only the
//     mechanism behind it moved, and it is worth naming precisely because this
//     function decides whether the daemon's approval gate reaches the argv.
//   - operatorBypass — the escalation came from the operator's pass-through claude
//     args, which never touch SessionSettings; the daemon may not revoke a bypass
//     it did not grant, so again nothing walks the child back. This is
//     sessions.RunnerConfig.OperatorBypass, derived there from the settings-free
//     spawnBase, and it is the same provenance bit streamsup's spawnAndWait reads.
//
// Everything else is a child the daemon downgrades in-band before its first turn,
// and it needs the approval gate on its argv exactly as it did before this ticket.
// So:
//
//   - staying in bypass → return args UNCHANGED. The flag is already in args;
//     injecting nothing keeps this byte-identical to pre-#2065 behaviour on both
//     of those rows and avoids a duplicate --dangerously-skip-permissions. Do NOT
//     call permissionArgs(true, …) here — that would re-emit the flag already
//     present.
//   - otherwise → append permissionArgs(false, mcpServersPath): the
//     --permission-prompt-tool / --mcp-config / --strict-mcp-config /
//     --permission-mode default set that routes every non-allowlisted tool use
//     through the daemon approval registry.
//
// # The mode-pair drop, now the common case
//
// Since #2043 the spawn's own args can already name a permission mode: a session
// storing one composes --permission-mode <mode> through claudeSettingsArgs, and
// this function runs at runner CONSTRUCTION on top of that — the path a daemon
// restart takes to rebuild a session out of the registry. Injecting the set
// unmodified would spawn it as "--permission-mode plan … --permission-mode
// default", so the injected set drops its own mode pair in that case. #2065 makes
// every non-escalated posture name itself, so this arm is now the common case
// rather than the exception; the reasoning is unchanged, only its frequency.
//
// It drops ONLY that pair. Returning args unchanged instead — the shape the
// staying-in-bypass arm uses — would spawn every mode-carrying session with no
// permission-prompt tool and no mcp-config, i.e. with the daemon's approval gate
// entirely absent, reachable from a stored setting. That arm used to be justified
// by "a bypass child has no approval gate to lose", and THAT SENTENCE STOPS
// HOLDING at #2065: every child now launches in bypass, so the sentence is only
// true of the children this function still returns unchanged — the ones nothing
// downgrades. For every other child the gate is very much there to lose.
//
// # Polarity, deliberately not symmetric with streamsup's predicate
//
// An unrecognised or empty storedMode falls THROUGH to injection here, where the
// same value makes spawnAndWait write nothing. Each is the fail-safe direction for
// its own consumer: an unexpected value here leaves the approval gate present, and
// there it asserts no posture and so leaves every construction site outside the
// interactive daemon byte-identical. sessions guarantees a canonical mode at this
// seam either way (TestRunnerConfigPermissionModeIsAlwaysKnown).
//
// The append runs on a clone so the caller's args (scfg.Args, freshly owned by
// mapStreamsupConfig) is never aliased or mutated.
func withApprovalArgs(args []string, mcpServersPath, storedMode string, operatorBypass, stdioPermissionPrompt bool) []string {
	if storedMode == sessions.PermissionModeBypass || operatorBypass {
		return args
	}
	extra := permissionArgs(false, mcpServersPath)
	if stdioPermissionPrompt {
		extra[1] = "stdio"
	}
	if namesPermissionMode(args) {
		extra = dropPermissionMode(extra)
	}
	return append(slices.Clone(args), extra...)
}

// namesPermissionMode reports whether args already carry a --permission-mode
// flag, in either the two-token or the joined form. Both are checked for
// stripSessionIDFlags' reason: the composed argv only ever uses the two-token
// form, but the operator's bootstrap pass-through claude args reach this function
// too and can spell a flag either way.
func namesPermissionMode(args []string) bool {
	for _, a := range args {
		if a == "--permission-mode" || strings.HasPrefix(a, "--permission-mode=") {
			return true
		}
	}
	return false
}

// dropPermissionMode returns args without its --permission-mode flag and value.
// It scans rather than slicing a known offset, so permissionArgs' ordering is not
// load-bearing here — a reordering there stays a cosmetic change instead of
// silently dropping the wrong flag. Never mutates args.
func dropPermissionMode(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--permission-mode" {
			i++ // skip its value; a dangling flag just drops the lone token
			continue
		}
		if strings.HasPrefix(a, "--permission-mode=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// mapStreamsupConfig maps a supervisor.Config to the streamsup.Config that
// streamsup.New consumes. It mutates nothing, constructs no runtime object, and
// returns a struct every field of which is inspectable, so the no-double-inject
// invariant is asserted directly (see streamsup_runner_test.go). It is no longer
// syscall-free, though: #1631's ClaudeSessionsDir is derived from WorkDir by
// streamClaudeSessionsDir, which stats the path and reads $HOME. Those reads are
// read-only and degrade to "" rather than failing, so the mapper still has no
// error return and no ordering constraint.
//
// The PTY-only fields on supervisor.Config are deliberately NOT mapped —
// streamsup owns its own id-flag inversion (buildArgs), has no PTY bridge, no
// transcript binding, and no .cast recorder: ResumeLast, ResolveSessionID,
// Bridge, ValidateConversation, ResolveTranscript, helperEnv.
// ResolveSessionID stays dropped even now that a sessions DIRECTORY crosses this
// seam: the directory is all that crosses, and streamsup's own per-spawn by-id
// existence probe (useCreateForm) decides the flag from it. No resolver callback
// crosses the seam in either direction, and neither side SCANS the directory —
// the probe is by-id only (#839 deleted --continue and adopt-by-mtime). Stdout
// stays nil HERE — the turnevent Parser that plugs into Stdout is a runtime
// object installed one layer up in newStreamRunnerFactory (#1098), which is the
// line the new field does not cross: it is a plain string derived from one
// RunnerConfig field, not a live object. Stderr and Env have no supervisor.Config
// analogue and stay nil — the session identity #2169 puts on the claude child is a
// per-SPAWN value, so what crosses here is SessionIDEnvVar, the variable's name,
// and streamsup composes the binding at the spawn that knows the live id (below).
func mapStreamsupConfig(cfg sessions.RunnerConfig) streamsup.Config {
	return streamsup.Config{
		ClaudeBin: cfg.ClaudeBin,
		WorkDir:   cfg.WorkDir,
		// #1108 guarantees SessionID is non-empty at both pool sites; streamsup.New
		// requires it (streamsup owns re-injecting the id flag from this value).
		SessionID:         cfg.SessionID,
		ClaudeSessionsDir: streamClaudeSessionsDir(cfg.WorkDir),
		Args:              stripSessionIDFlags(cfg.ClaudeArgs),
		Logger:            cfg.Logger,
		BackoffInitial:    cfg.BackoffInitial,
		BackoffMax:        cfg.BackoffMax,
		BackoffReset:      cfg.BackoffReset,
		// The interactive daemon asks every child, once, what the session knows
		// about itself (#1839). Set HERE rather than in newStreamRunnerFactory
		// because it is a plain bool constant, not a runtime object — the same
		// dividing line ClaudeSessionsDir sits on — and setting it in the pure
		// mapper makes the policy directly assertable with no new scaffolding.
		RequestInitializeOnSpawn: true,
		// The session's stored posture, asserted to every child the runner spawns
		// (#2064). Set HERE for RequestInitializeOnSpawn's reason verbatim: it is a
		// plain string read off one RunnerConfig field, not a runtime object, so it
		// belongs on ClaudeSessionsDir's side of the mapper's dividing line and the
		// policy stays directly assertable with no scaffolding. Its partner
		// PostureGate is the runtime half and is installed one layer up.
		SpawnPermissionMode: cfg.PermissionMode,
		// The provenance of the escalation on this session's argv (#2065), mapped
		// here for SpawnPermissionMode's reason verbatim — a plain bool read off one
		// RunnerConfig field. It is what lets the runner tell a bypass the daemon
		// composed, and may walk back, from one the operator handed it.
		OperatorBypass: cfg.OperatorBypass,
		// The variable the claude child's environment binds to the calling session's
		// identity (#2169). streamsup composes NAME=<live id> per spawn and appends it
		// to os.Environ(), so the pyry_files MCP server claude FORKS inherits it and
		// forwards it as the attachment.file destination. It cannot ride the mcp-config
		// argv instead: that document is daemon-global and byte-identical for every
		// session (see mcpServersConfig).
		//
		// The NAME crosses this seam and not the composed value, which is the rework
		// #2169 took after review. The value has to be the LIVE id, and cfg.SessionID
		// is the construction-time seed — Pool.New's own comment on the field says it
		// does not mirror a /clear rotation — so composing NAME=<cfg.SessionID> here
		// named the retired session on every child spawned after a new_session
		// rotation, and the daemon refuses a retired id. streamsup.Config.SessionID
		// still crosses (streamsup seeds its live id from it and re-injects the id
		// flag); only the environment's value is deferred to the spawn that knows it.
		//
		// Set HERE for SpawnPermissionMode's reason verbatim — a plain constant, not a
		// runtime object. The per-session identity itself is asserted one layer down,
		// where it is composed: internal/streamsup → TestRunner_BeginSpawn_EnvCarries-
		// OwnLiveSessionID and its rotation sibling.
		SessionIDEnvVar: envSessionID,
	}
}

// streamClaudeSessionsDir derives the directory claude writes <uuid>.jsonl into
// for a runner whose WorkDir is workdir — the value streamsup.Config's
// ClaudeSessionsDir field takes, where "" means "run no probe" and every spawn's
// id flag falls back to the Run loop's firstRun latch (#1631 arms what #1630 left
// inert). It is derived per runner rather than once per pool because a
// per-conversation runner's WorkDir is the phone's confined spawn dir when one
// was requested (Pool.buildSession), so it legitimately differs from the
// bootstrap workdir.
//
// The composition is pinned to the transform streamsup.New itself applies: it
// sets the child's cmd.Dir to agentrun.ResolveWorkdir(WorkDir), and claude
// encodes its own resolved cwd into the projects folder name, so the probe must
// key on the SAME resolution. #1655 measured the empirical directory — located by
// finding the transcript claude actually wrote — against
// sessions.DefaultClaudeSessionsDir of a cwd resolved that way, and found them
// equal.
//
// Do NOT substitute resolveClaudeSessionsDir or a bare DefaultClaudeSessionsDir.
// Neither applies canonicalCase, which ResolveWorkdir does and claude does, and
// neither confineWorkdirToHome (bootstrap) nor resolveSpawnDir (phone)
// canonicalises case — so on a case-insensitive filesystem a wrong-cased workdir
// would yield a directory claude never writes. That reads "absent" for a session
// whose transcript exists, spawns --session-id against a live transcript, which
// claude refuses (ADR 032), and — because a supplied directory makes
// useCreateForm decide OUTRIGHT rather than latch — loops permanently instead of
// wasting one spawn.
//
// Every "" arm is pre-#1631 behaviour rather than a new failure mode. An empty
// workdir is unreachable at both pool sites (both carry a confined realpath) but
// must not fall through to the process cwd the way resolveClaudeSessionsDir
// deliberately does; a workdir ResolveWorkdir rejects never produced a runner at
// all, since streamsup.New calls the same function on the same value and returns
// an error; and an unresolvable $HOME degrades inside DefaultClaudeSessionsDir.
func streamClaudeSessionsDir(workdir string) string {
	if workdir == "" {
		return ""
	}
	resolved, err := agentrun.ResolveWorkdir(workdir)
	if err != nil {
		return ""
	}
	return sessions.DefaultClaudeSessionsDir(resolved)
}

// stripSessionIDFlags returns a new slice with every --session-id/--resume flag
// (and its value) removed. It never mutates args — the caller's ClaudeArgs is
// aliased into the pool's spawnBase — so it always allocates a fresh backing
// array.
//
// streamsup.buildArgs re-injects --session-id <id> (first spawn) / --resume <id>
// (respawn) from Config.SessionID itself. Leaving the pool's baked
// "--session-id <id>" (Pool.buildSession) in Args would double-inject
// (--session-id X … --session-id X, or the contradictory --session-id X …
// --resume X on respawn). The strip is required at the per-session site and a
// harmless no-op at the bootstrap site (whose ClaudeArgs carry no id flag) —
// applied uniformly, no site-specific branch.
func stripSessionIDFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		// Two-token form: --session-id <value> / --resume <value>. Drop the flag
		// and skip its value. A dangling flag (no following token) just drops the
		// lone flag — i++ past the end ends the loop without an index.
		if a == "--session-id" || a == "--resume" {
			i++
			continue
		}
		// Joined form: --session-id=<value> / --resume=<value>. One token to drop.
		if strings.HasPrefix(a, "--session-id=") || strings.HasPrefix(a, "--resume=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Assert at compile time that streamRunner satisfies sessions.Runner (AC1). The
// build fails if a method is missing or mis-typed. This is the ONLY conformance
// assertion for the interface, not a mirror of one in internal/sessions: that
// package declares no assertion of its own because the sole production
// implementation lives here.
var _ sessions.Runner = streamRunner{}
var _ confirmedPermissionModeReader = streamRunner{}
