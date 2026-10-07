package streamsup

import (
	"context"
	"io"
	"strconv"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// SetPermissionMode writes a single set_permission_mode control_request line to
// the live child's stdin, switching its permission posture WITHOUT killing it.
// #1595 measured the default switch live against claude 2.1.220 and #2041
// measured acceptEdits, dontAsk, plan and auto against 2.1.239 — each acked
// success with the next turn's init.permissionMode echoing the new mode, no
// respawn. The request_id is locally minted from the counter Interrupt and
// RequestInitialize share.
//
// mode is refused unless it is in WritePermissionMode's closed allow-list, which
// since #2066 has SIX members and the escalation among them: re-granting bypass
// arrives HERE now, not on the respawn path, because #2065 put the launch flag on
// every argv and #2060 measured claude accepting the re-escalation on such a child
// at 2.1.239. What the allow-list still refuses, by NON-MEMBERSHIP, is every mode
// claude will not parse, near misses of the escalation included. The refusal
// (ErrUnsupportedPermissionMode) is permanent and stays
// errors.Is-distinguishable from the retryable ErrNoLiveChild, which is what a
// caller gets when no child is live — Stdin() is nil then, so nothing is written
// and nothing panics.
//
// A runner wired to its concrete Parser snapshots the live child's writer and
// generation before registering the request id and requested mode, then rechecks
// that generation before writing. A call begun between children therefore cannot
// drift onto a successor. ConfirmedPermissionMode changes only after the write
// succeeds and a success response carries both that exact id and an equal echoed
// mode. The method still returns after the write rather than waiting for that
// response.
//
// Like RevokeBypass and unlike Interrupt it IS on sessions.Runner, and the
// contrast is the rule rather than an exception: Interrupt's dispatch lives in
// cmd/pyry, which can type-assert, whereas this method's consumers sit inside
// internal/sessions, with no such dispatch site. A structural assertion there
// would fail OPEN — its unmatched arm is a silent no-op, leaving the child in the
// wrong posture while the update reports success — so the interface method makes a
// runner that cannot switch a build failure instead.
//
// A SUCCESSFUL write RETARGETS the posture gate when that gate is closed (#2064), and
// this is the recovery path for a spawn-time write claude NAK'd. The gate's only other
// opener is a spawn, so without this a NAK'd child stays healthy and refuses every turn
// until the daemon restarts — and the operator's remedy would not reach it, since a
// corrective mode change is in-band-deliverable and so never restarts anything. An OPEN
// gate is left open: see retarget for why gating in-band changes would drop
// deliverSettingsInBand's own follow-on sends.
//
// The retarget is deliberately AFTER the write and skipped on error, including the
// allow-list refusal. A line that never reached the child will never be acked, so
// pointing the gate at its id would replace a pending spawn id that still might be
// acked with one that cannot be.
//
// Stdin() releases r.mu before returning, so the potentially-blocking write never
// holds it. Safe from any goroutine.
func (r *Runner) SetPermissionMode(mode string) error {
	id := r.nextControlID()
	r.mu.Lock()
	w := r.stdin
	generation := r.childGeneration
	r.mu.Unlock()

	var pending *pendingPermissionMode
	if r.parser != nil && w != nil {
		pending = r.parser.registerPermissionMode(id, mode)
		r.mu.Lock()
		current := r.stdin != nil && r.childGeneration == generation
		r.mu.Unlock()
		if !current {
			r.parser.removePermissionMode(id, pending)
			pending.resolveWrite(false)
			pending = nil
			w = nil
		}
	}

	err := WritePermissionMode(w, id, mode)
	if pending != nil {
		if err != nil {
			r.parser.removePermissionMode(id, pending)
		}
		pending.resolveWrite(err == nil)
	}
	if err != nil {
		return err
	}
	r.postureGate.retarget(id)
	return nil
}

// RevokeBypass drops the live child's bypass posture by asking for the default
// mode. It is SetPermissionMode's named revoke shorthand, kept because it has a
// production caller — Pool.deliverSettingsInBand — whose delivered bytes must not
// change while the mode-carrying method is introduced beside it. #2043 rewrites
// that caller to send an operator-chosen mode, which is the slice where this
// method loses its last caller and can go: a method is deleted alongside the
// consumer that held it, not in the slice whose contract is that the consumer is
// untouched.
//
// It cannot name anything but permissionModeDefault, so the revocation line is
// byte-identical to the one #1595 measured and #1604 has been sending since. Its
// no-live-child contract is SetPermissionMode's, unchanged.
func (r *Runner) RevokeBypass() error {
	return r.SetPermissionMode(permissionModeDefault)
}

// SetSpawnPermissionMode installs the posture EVERY LATER SPAWN asserts, without
// touching the running child (#2064). It is SetPermissionMode's per-spawn twin and
// the contrast is the point: that method changes the live child's posture and writes
// nothing durable, this one changes what the next child is told and writes nothing at
// all.
//
// It exists because Pool.UpdateSettings REBUILDS NO RUNNER — both its branches install
// a recomposed argv onto the live runner and neither reconstructs it — so a posture
// read from the construction-time Config at spawn time goes stale the moment an
// operator changes the session's settings. A crash-respawn would then assert the
// posture the session had at daemon start, which can LOOSEN one the operator has since
// tightened. That is why the pool calls this on the line above its branch split, where
// its own comment already reads "Both branches install newArgs".
//
// Two cheaper shapes do not cover it, and are recorded so they are not re-proposed:
//
//   - Piggybacking the install on SetPermissionMode, which the in-band branch already
//     calls. #2066's reason for rejecting it is gone — an escalation now IS
//     in-band-deliverable and does reach that method — but the verdict stands on the
//     branch that remains: a Model or Effort cleared to "" still takes
//     Pool.UpdateSettings' restart branch, which never calls SetPermissionMode, so a
//     piggybacked install would miss every such change and let a crash-respawn
//     re-assert a stale posture. An install on the line ABOVE the branch split is the
//     only shape that covers both.
//   - Deriving the posture from the installed argv. Exact today and dead on arrival
//     under #2065, which takes the mode out of the launch argv entirely.
//
// mode is stored VERBATIM AND UNVALIDATED, including bypassPermissions and including
// the empty string. This is a normalisation-free install of an already-canonical
// stored value, not an operator-input path: the vocabulary gate that matters runs at
// the SPAWN, where permissionModeSpawnWritable decides whether anything is written at
// all, and duplicating it here would be two copies of one defence rather than a second
// one.
//
// That predicate and not permissionModeAllowed is #2066's carve-out, and the one mode
// they disagree on is the one this paragraph installs verbatim. The writer's allow-list
// admits the escalation now, so a LIVE child can be walked back up to it; the spawn is
// still sent nothing for it, because since #2065 the launch argv already asserts it —
// there is nothing to walk a fresh child TO — and arming the posture gate on a control
// request nothing has measured as a fresh stream's first would be the bricked-session
// shape #2064 rejected.
//
// It takes restartMu alone — never mu, never a Pool lock — so the sessions layer can
// call it after releasing Pool.mu with no lock-order concern, exactly as SetSpawnArgs
// can. Safe from any goroutine; non-blocking.
func (r *Runner) SetSpawnPermissionMode(mode string) {
	r.restartMu.Lock()
	r.spawnMode = mode
	r.restartMu.Unlock()
}

// RequestInitialize writes a single initialize control_request line to the live
// child's stdin, asking it to report what the session knows about itself — the
// model list (identifiers, display names, supported reasoning-effort levels) and
// the slash-command list — without a respawn. The request_id is locally minted
// from the same counter Interrupt and RevokeBypass draw on. When no child is live
// Stdin() is nil, so RequestInitialize returns the retryable ErrNoLiveChild
// without writing and without panicking.
//
// It writes the line and stops: the control_response is NOT read here, exactly as
// RevokeBypass writes without reading its ack. The minted id is deliberately not
// returned either — the reader slice that correlates the ack is the first thing
// that needs it, and a return value with no reader is a seam with nothing on the
// far side of it.
//
// The name is RequestInitialize, not Initialize: on a type that already has New, a
// bare Initialize() reads as "initialize the runner", which is the opposite of what
// it does — it asks the CHILD to initialize.
//
// Like Interrupt and unlike RevokeBypass it is NOT on sessions.Runner, and the
// placement rule is the same one in both directions: the interface carries a
// method when its consumer sits inside internal/sessions, where a structural type
// assertion would fail open (RevokeBypass's does — Pool.UpdateSettings). This
// subtype has no consumer at all yet — the trigger lands with the publishing slice
// — so an interface method here would be a seam with nothing on the far side of
// it, and it would pull every fake runner under internal/sessions into the diff.
// Stdin() releases r.mu before returning, so the potentially-blocking write never
// holds it. Safe from any goroutine.
func (r *Runner) RequestInitialize() error {
	return WriteInitialize(r.Stdin(), r.nextControlID())
}

// RequestMCPStatus asks the live child for its current MCP server states. The
// parser's per-child policy is the sole automatic caller; direct writers remain
// available through WriteMCPStatus. The request id shares the runner-wide control
// sequence with every other subtype.
func (r *Runner) RequestMCPStatus() error {
	return WriteMCPStatus(r.Stdin(), r.nextControlID())
}

const mcpStatusQueryIDPrefix = "mcp-status-query-"

// QueryMCPStatus asks the exact eligible live child for its current MCP server
// states and waits for the response carrying that request's id. The parser claims
// that response before the shared sink; false collapses every unavailable outcome.
func (r *Runner) QueryMCPStatus(ctx context.Context) (turnevent.MCPStatus, bool) {
	if ctx.Err() != nil || r.parser == nil {
		return turnevent.MCPStatus{}, false
	}

	r.mu.Lock()
	if r.stdin == nil || r.rotating || !r.mcpStatusEligible {
		r.mu.Unlock()
		return turnevent.MCPStatus{}, false
	}
	w := r.stdin
	generation := r.childGeneration
	r.mu.Unlock()

	id := mcpStatusQueryIDPrefix + r.nextControlID()
	pending := r.parser.registerMCPStatusQuery(id)

	// Registration stays outside the runner's leaf mutex. Rechecking the binding
	// generation closes the snapshot-to-registration gap without nesting locks.
	r.mu.Lock()
	current := r.stdin != nil && !r.rotating && r.mcpStatusEligible && r.childGeneration == generation
	r.mu.Unlock()
	if !current {
		r.parser.removeMCPStatusQuery(id, pending)
		pending.resolveWrite(false)
		pending.complete(turnevent.MCPStatus{}, false)
		return turnevent.MCPStatus{}, false
	}
	if ctx.Err() != nil {
		r.parser.removeMCPStatusQuery(id, pending)
		pending.resolveWrite(false)
		pending.complete(turnevent.MCPStatus{}, false)
		return turnevent.MCPStatus{}, false
	}

	err := WriteMCPStatus(w, id)
	pending.resolveWrite(err == nil)
	if err != nil {
		r.parser.removeMCPStatusQuery(id, pending)
		pending.complete(turnevent.MCPStatus{}, false)
		return turnevent.MCPStatus{}, false
	}
	if ctx.Err() != nil {
		r.parser.removeMCPStatusQuery(id, pending)
		pending.complete(turnevent.MCPStatus{}, false)
		return turnevent.MCPStatus{}, false
	}

	select {
	case result := <-pending.result:
		if ctx.Err() != nil {
			return turnevent.MCPStatus{}, false
		}
		return result.status, result.ok
	case <-ctx.Done():
		r.parser.removeMCPStatusQuery(id, pending)
		return turnevent.MCPStatus{}, false
	}
}

// MemorySearchLaunch snapshots the exact live child's launch scope. A missing
// snapshot means that the child is between binding and evidence publication.
func (r *Runner) MemorySearchLaunch() (string, *string, string, uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stdin == nil || r.rotating || r.childWorkspace == "" {
		return "", nil, "", 0, false
	}
	return r.childWorkspace, r.childPATH, "", r.childGeneration, true
}

// mcpActuationIDPrefix namespaces the request ids of the daemon's MCP and task-stop actuations,
// and its DISJOINTNESS FROM mcpStatusQueryIDPrefix is load-bearing rather than
// cosmetic. mcpStatusQueries.claim treats ANY unregistered id carrying its own
// prefix as claimed and consumed, and claimMCPStatusQuery runs first in the parser's
// control_response arm — so an actuation minting ids under that prefix would have
// every ack swallowed before claimMCPActuation ever saw one, and the symptom is a
// caller hanging until its context expires, not a missed reading. Neither constant
// is a prefix of the other; keep it that way.
const mcpActuationIDPrefix = "mcp-actuate-"

// ReconnectMCPServer asks the exact eligible live child to reconnect one MCP server
// and reports whether that child accepted it. See actuateMCP for the contract both
// actuations share — notably that the caller's context is the only bound on the wait.
//
// Server membership and authorization belong to the caller boundary, as
// WriteMCPReconnect's own doc block states; this method adds neither. No wire surface
// reaches it: it is on the concrete runner and not on sessions.Runner, so nothing
// outside the daemon can actuate a configuration change until #2419 lands a forwarder
// together with its gate.
func (r *Runner) ReconnectMCPServer(ctx context.Context, serverName string) bool {
	return r.actuateMCP(ctx, func(w io.Writer, id string) error {
		return WriteMCPReconnect(w, id, serverName)
	})
}

// SetMCPServerEnabled enables or disables one MCP server on the exact eligible live
// child and reports whether that child accepted it. enabled is passed through to the
// write in both directions rather than one value being fixed or derived from a state
// this package does not hold; marshalMCPToggleEnvelope's pointer field is what keeps
// a false explicit on the wire.
//
// Named for the flag rather than the wire's `mcp_toggle` subtype deliberately: the
// request carries the value, so a Toggle spelling would invite a caller to expect a
// flip of whatever the current state is. Membership, authorization and the absence of
// any wire surface are as ReconnectMCPServer states.
func (r *Runner) SetMCPServerEnabled(ctx context.Context, serverName string, enabled bool) bool {
	return r.actuateMCP(ctx, func(w io.Writer, id string) error {
		return WriteMCPToggle(w, id, serverName, enabled)
	})
}

// actuateMCP writes one MCP actuation to the exact eligible live child and waits for
// the control response carrying that request's own id. It returns whether the child
// accepted it.
//
// ONE BOOL COLLAPSES REFUSAL AND UNAVAILABILITY, and that is a contract decision
// rather than lost information: #2419 publishes a single merged reject for gate,
// unknown server, no live child and failed actuation alike, and #2420's audit record
// names no reason, so a richer return would have no reader above. What the failure arm
// does buy is narrower and load-bearing — matching an id must not by itself read as
// accepted, which claimMCPActuation's subtype test is what delivers.
//
// NOTHING IS LOGGED HERE, and a diagnostic must not be added: the values in scope are
// the server name and the writer's error, which is exactly what must stay out of every
// record on this path. The writer's error is deliberately DISCARDED rather than
// returned for the same reason; the discard is the guarantee, not an oversight.
//
// THE CALLER'S CONTEXT IS THE ONLY BOUND ON THE WAIT when the child stays alive and
// simply never answers. Child death and replacement are covered — takeStdin and
// beginMCPStatusChild each fail every pending actuation — but silence is not, so a
// caller must pass a deadline rather than context.Background(). QueryMCPStatus carries
// the same contract, which is why this is stated rather than fixed with a timeout
// invented at this layer.
//
// The order below is QueryMCPStatus's and each step earns its place: snapshot the
// binding under the leaf mutex, register OUTSIDE it (r.mu is never held across a
// call-out), then re-check the generation. Registering before that re-check is what
// makes the check meaningful — a child replaced in the gap retires an entry that
// already exists, where the reverse order would leave a registration nothing can find.
func (r *Runner) actuateMCP(ctx context.Context, write func(io.Writer, string) error) bool {
	return r.actuateControl(ctx, true, write)
}

// StopTask asks the live child to stop taskID and reports acceptance, not task
// completion. Membership and authorization belong to the caller. MCP provenance
// does not gate this request. Normal outcomes preserve the child and reply; if
// cancellation interrupts a blocked write, only its captured generation is retired.
// The caller context bounds both writing and waiting; no request or error text is
// logged or returned.
func (r *Runner) StopTask(ctx context.Context, taskID string) bool {
	return r.actuateControl(ctx, false, func(w io.Writer, id string) error {
		return WriteStopTask(w, id, taskID)
	})
}

// actuateControl shares private correlation between MCP and task-stop requests.
// requireMCP retains the MCP provenance gate and synchronous write contract;
// task stops instead bound the write with the captured-generation cancellation
// callback, as QueryAppliedSettings does. Runner.mu remains a leaf lock.
func (r *Runner) actuateControl(ctx context.Context, requireMCP bool, write func(io.Writer, string) error) bool {
	if ctx.Err() != nil || r.parser == nil {
		return false
	}

	r.mu.Lock()
	if r.stdin == nil || r.rotating || (requireMCP && !r.mcpStatusEligible) {
		r.mu.Unlock()
		return false
	}
	w := r.stdin
	generation := r.childGeneration
	r.mu.Unlock()

	id := mcpActuationIDPrefix + r.nextControlID()
	pending := r.parser.registerMCPActuation(id)

	r.mu.Lock()
	current := r.stdin != nil && !r.rotating && (!requireMCP || r.mcpStatusEligible) && r.childGeneration == generation
	r.mu.Unlock()
	if !current || ctx.Err() != nil {
		r.parser.removeMCPActuation(id, pending)
		pending.resolveWrite(false)
		pending.complete(false)
		return false
	}

	var stopWriteCancel func() bool
	var writeCancelDone chan struct{}
	if !requireMCP {
		writeCancelDone = make(chan struct{})
		stopWriteCancel = context.AfterFunc(ctx, func() {
			r.retireStdinGeneration(generation)
			close(writeCancelDone)
		})
	}
	err := write(w, id)
	if stopWriteCancel != nil && !stopWriteCancel() {
		<-writeCancelDone
	}
	pending.resolveWrite(err == nil && ctx.Err() == nil)
	if err != nil || ctx.Err() != nil {
		r.parser.removeMCPActuation(id, pending)
		pending.complete(false)
		return false
	}

	select {
	case accepted := <-pending.result:
		// A parser can claim the response before the write ends, removing the
		// pending entry before child cleanup sees it. The generation check keeps
		// that early success from surviving a child boundary.
		r.mu.Lock()
		current = r.stdin != nil && !r.rotating && r.childGeneration == generation
		r.mu.Unlock()
		return accepted && current && ctx.Err() == nil
	case <-ctx.Done():
		r.parser.removeMCPActuation(id, pending)
		return false
	}
}

// RequestContextUsage asks the live child for its summary or full context
// breakdown. The detail vocabulary is checked before child lookup and ID minting;
// WriteContextUsage repeats the same boundary for direct callers. The request ID
// comes from the runner-wide control sequence, and this method does not decode the
// response. When Stdout is the runner's Parser, the id is registered before the
// write and removed again on a write failure; only that parser can consume the
// successful registration.
func (r *Runner) RequestContextUsage(detail string) error {
	if !contextUsageDetailAllowed(detail) {
		return ErrUnsupportedContextUsageDetail
	}
	id := r.nextControlID()
	var pending *pendingContextUsageRequest
	if r.parser != nil {
		pending = r.parser.registerContextUsageRequest(id)
	}
	err := WriteContextUsage(r.Stdin(), id, detail)
	if pending != nil {
		if err != nil {
			r.parser.removeContextUsageRequest(id, pending)
		}
		pending.resolve(err == nil)
	}
	return err
}

// contextUsageQueryIDPrefix namespaces the request ids of requester-private context
// readings, and its DISJOINTNESS from mcpStatusQueryIDPrefix, from mcpActuationIDPrefix
// and from the bare decimal sequence RequestContextUsage mints is load-bearing for the
// reason mcpActuationIDPrefix's own doc block gives — a collision costs a hang, not a
// miss. It carries one hazard the other two do not: BOTH context-usage paths land in the
// same control_response arm, so this prefix overlapping the bare sequence would have
// claimContextUsageQuery swallow the automatic post-turn reading the shared sink must
// still publish. Leading with a letter makes that impossible. Keep all four disjoint.
const contextUsageQueryIDPrefix = "context-usage-query-"

// QueryContextUsage asks the live child for a context breakdown at detail and waits for
// the response carrying that request's own id, so a caller answering an inbound request
// can correlate the reading to it. The parser claims that response before the shared
// sink, so a reading returned here is NOT also republished as an unsolicited frame.
// RequestContextUsage remains the fire-and-forget peer for the post-turn cadence.
//
// ONE BOOL COLLAPSES EVERY NOT-ANSWERED OUTCOME — an unsupported detail, no live child, a
// rotation in flight, a child replaced before answering, a failed write, a caller whose
// context ended, and an unusable payload alike. That is QueryMCPStatus's contract and
// actuateMCP's stated decision, and this slice wires no caller above it that could
// consume a richer one. A caller needing the vocabulary itself learns it from
// RequestContextUsage's ErrUnsupportedContextUsageDetail.
//
// THE RETURNED READING CARRIES CHILD-AUTHORED STRINGS, memory-file paths from the user's
// own filesystem among them. It is bounded but not sanitised for diagnostics, so a caller
// must not log it wholesale. Nothing on this path logs, and no diagnostic may be added
// here: the values in scope are the detail, the minted id and the writer's error.
//
// mcpStatusEligible is deliberately NOT part of the gate, unlike QueryMCPStatus's: it
// reports whether argv confines the child to the daemon's one MCP config, which has no
// bearing on whether a context reading can be asked for. A child that is live and not
// rotating is askable. rotating and childGeneration are the parts that do carry over.
//
// THE CALLER'S CONTEXT IS THE ONLY BOUND ON THE WAIT when the child stays alive and
// simply never answers — takeStdin and beginMCPStatusChild cover death and replacement,
// silence they cannot — so pass a deadline rather than context.Background().
//
// The order below is QueryMCPStatus's, for the reasons actuateMCP records: snapshot the
// binding under the leaf mutex, register OUTSIDE it, then re-check the generation.
func (r *Runner) QueryContextUsage(ctx context.Context, detail string) (turnevent.ContextUsage, bool) {
	// Vocabulary refusal precedes every other check, as RequestContextUsage and
	// WriteContextUsage do it: an unsupported value is permanent regardless of child
	// state, and refusing here is what keeps an id from being minted or a request
	// written for one.
	if !contextUsageDetailAllowed(detail) {
		return turnevent.ContextUsage{}, false
	}
	if ctx.Err() != nil || r.parser == nil {
		return turnevent.ContextUsage{}, false
	}

	r.mu.Lock()
	if r.stdin == nil || r.rotating {
		r.mu.Unlock()
		return turnevent.ContextUsage{}, false
	}
	w := r.stdin
	generation := r.childGeneration
	r.mu.Unlock()

	id := contextUsageQueryIDPrefix + r.nextControlID()
	pending := r.parser.registerContextUsageQuery(id)

	// Registration stays outside the runner's leaf mutex. Rechecking the binding
	// generation closes the snapshot-to-registration gap without nesting locks.
	r.mu.Lock()
	current := r.stdin != nil && !r.rotating && r.childGeneration == generation
	r.mu.Unlock()
	if !current || ctx.Err() != nil {
		r.parser.removeContextUsageQuery(id, pending)
		pending.resolveWrite(false)
		pending.complete(turnevent.ContextUsage{}, false)
		return turnevent.ContextUsage{}, false
	}

	err := WriteContextUsage(w, id, detail)
	pending.resolveWrite(err == nil)
	if err != nil || ctx.Err() != nil {
		r.parser.removeContextUsageQuery(id, pending)
		pending.complete(turnevent.ContextUsage{}, false)
		return turnevent.ContextUsage{}, false
	}

	select {
	case result := <-pending.result:
		if ctx.Err() != nil {
			return turnevent.ContextUsage{}, false
		}
		return result.usage, result.ok
	case <-ctx.Done():
		r.parser.removeContextUsageQuery(id, pending)
		return turnevent.ContextUsage{}, false
	}
}

// AppliedSettings is the bounded subset of Claude's get_settings reply a daemon
// caller may use. Model and Effort are child-authored display data, not trusted
// identifiers or authorization input. Effort nil preserves an explicit JSON null;
// unavailable information is reported by QueryAppliedSettings's bool instead.
//
// No effective setting, source, environment value, path or credential is decoded
// into this type. Callers must not log a value wholesale merely because it is small.
type AppliedSettings struct {
	Model  string
	Effort *string
}

// appliedSettingsQueryIDPrefix namespaces requester-private get_settings replies.
// It must remain disjoint from the other private prefixes and from the bare decimal
// sequence: each correlator consumes late unregistered ids in its own namespace.
const appliedSettingsQueryIDPrefix = "applied-settings-query-"

// QueryAppliedSettings asks the exact live child for the settings Claude applied and
// waits for the response carrying this request's locally minted id. It returns only
// bounded model and nullable effort copies; every refusal or unavailable outcome is
// the false arm.
//
// A caller deadline is mandatory. Child exit and replacement retire the query, but a
// live child can stay silent indefinitely; refusing an unbounded context prevents one
// such child from retaining a waiter forever. Cancellation is honored before and
// during the write, while waiting, and after a raced result. Ending the context during
// a blocked write retires and closes only the captured child generation, which is the
// interrupt available for an os.Pipe write and keeps a successor untouched.
//
// The ordering matches QueryContextUsage: snapshot the binding under Runner.mu,
// register outside that leaf lock, then recheck childGeneration before writing.
func (r *Runner) QueryAppliedSettings(ctx context.Context) (AppliedSettings, bool) {
	if ctx.Err() != nil || r.parser == nil {
		return AppliedSettings{}, false
	}
	if _, ok := ctx.Deadline(); !ok {
		return AppliedSettings{}, false
	}

	r.mu.Lock()
	if r.stdin == nil || r.rotating {
		r.mu.Unlock()
		return AppliedSettings{}, false
	}
	w := r.stdin
	generation := r.childGeneration
	r.mu.Unlock()

	id := appliedSettingsQueryIDPrefix + r.nextControlID()
	pending := r.parser.registerAppliedSettingsQuery(id)

	r.mu.Lock()
	current := r.stdin != nil && !r.rotating && r.childGeneration == generation
	r.mu.Unlock()
	if !current || ctx.Err() != nil {
		r.parser.removeAppliedSettingsQuery(id, pending)
		pending.resolveWrite(false)
		pending.complete(AppliedSettings{}, false)
		return AppliedSettings{}, false
	}

	writeCancelDone := make(chan struct{})
	stopWriteCancel := context.AfterFunc(ctx, func() {
		r.retireStdinGeneration(generation)
		close(writeCancelDone)
	})
	err := WriteAppliedSettings(w, id)
	if !stopWriteCancel() {
		<-writeCancelDone
	}
	pending.resolveWrite(err == nil && ctx.Err() == nil)
	if err != nil || ctx.Err() != nil {
		r.parser.removeAppliedSettingsQuery(id, pending)
		pending.complete(AppliedSettings{}, false)
		return AppliedSettings{}, false
	}

	select {
	case result := <-pending.result:
		if ctx.Err() != nil {
			return AppliedSettings{}, false
		}
		return result.settings, result.ok
	case <-ctx.Done():
		r.parser.removeAppliedSettingsQuery(id, pending)
		return AppliedSettings{}, false
	}
}

// nextControlID mints the next locally-unique control-request correlation id,
// shared by Interrupt, SetModel, SetPermissionMode, RequestInitialize,
// RequestMCPStatus, QueryMCPStatus, RequestContextUsage, QueryContextUsage and
// QueryAppliedSettings
// (RevokeBypass draws on it through SetPermissionMode,
// minting exactly one id per call, not two). The atomic counter is
// unique within the runner's lifetime — one sequence, not one per subtype, since
// request_id must be unique across all in-flight control requests on the stream
// rather than merely within one subtype.
//
// THE ACK CORRELATOR EXISTS NOW (#2064) and this counter is what it correlates on, so
// the id is no longer write-only. The parser's noteControlAck matches a
// control_response's request_id against the PostureGate's armed id; a spawn writes two
// requests off this counter and the gate opens only for its own. One sequence per
// runner is still exactly right — each runner drives exactly one child stream — and a
// SECOND counter would break the correlator rather than help it, since two sequences
// collide on their first id. That is the mistake this paragraph exists to prevent.
func (r *Runner) nextControlID() string {
	return strconv.FormatUint(r.controlSeq.Add(1), 10)
}
