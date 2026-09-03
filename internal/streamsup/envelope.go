package streamsup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/pyrycode/pyrycode/internal/turncommit"
)

// ErrNoLiveChild is returned by WriteTurn when the child's stdin handle is nil —
// no claude child is currently live (before the first spawn, between spawns, or
// mid-restart). Runner.Stdin returns an untyped nil in exactly those windows,
// which WriteTurn's w == nil check maps to this sentinel. The wiring slice maps
// it to a retryable "no live child" outcome.
var ErrNoLiveChild = errors.New("streamsup: no live child")

// ErrUnsupportedPermissionMode is returned by WritePermissionMode when the caller
// names a mode outside permissionModeAllowed's closed set. It is a permanent
// refusal, unlike the retryable ErrNoLiveChild, and the two must stay
// errors.Is-distinguishable: a caller that mistook this for "no live child" would
// retry a request that can never succeed.
//
// It is returned BARE, never wrapped with the rejected value. Its text is a
// constant precisely so it cannot echo the mode: Pool.deliverSettingsInBand logs
// this error verbatim, and #833 keeps settings values out of the daemon log. A
// mode is a settings value, so the obvious fmt.Errorf("… %q", mode) would leak an
// operator's value into the log through the error return. The refusal tells an
// operator THAT a mode was refused; which one is recoverable from the request they
// sent, not from pyry's log.
var ErrUnsupportedPermissionMode = errors.New("streamsup: unsupported permission mode")

// userTurn is the stream-json envelope written to claude's stdin. The shape
// mirrors streamrunner's verbatim (the 2026-05-14 probe):
//
//	{"type":"user","message":{"role":"user","content":[{"type":"text","text":"…"}]}}
//
// The one divergence from streamrunner is at the call site, not the shape:
// streamrunner writes ONE envelope then closes stdin; here stdin stays open for
// the next turn (WriteTurn takes an io.Writer, which cannot close it).
type userTurn struct {
	Type    string          `json:"type"`
	Message userTurnMessage `json:"message"`
}

type userTurnMessage struct {
	Role    string                `json:"role"`
	Content []userTurnContentText `json:"content"`
}

type userTurnContentText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// marshalTurnEnvelope returns the single newline-terminated stream-json line for
// prompt. The prompt is carried as a JSON string value (content[].text) and
// json.Marshal-escaped, so every metacharacter — critically every newline —
// becomes an escape sequence. The marshalled envelope is therefore a single
// physical line and the appended '\n' is the only raw newline: an untrusted
// prompt cannot introduce a second stream-json line, so it cannot forge a
// `result` (fake turn-end), a `control_request` (interrupt), or a permission
// approval on claude's stdin. This is enforced by construction (structured
// encoding, never string concatenation).
func marshalTurnEnvelope(prompt []byte) ([]byte, error) {
	env := userTurn{
		Type: "user",
		Message: userTurnMessage{
			Role: "user",
			Content: []userTurnContentText{{
				Type: "text",
				Text: string(prompt),
			}},
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// controlRequest is a stream-json control line written to claude's held-open
// stdin. It is marshalled structured (never string-concatenated) so it is
// exactly one physical line — the same injection-resistance invariant
// marshalTurnEnvelope holds. The interrupt control line carries no free-text
// field, so it has no injection surface of its own; the structured-encoding
// discipline is kept for symmetry and future control subtypes.
type controlRequest struct {
	Type      string              `json:"type"`       // "control_request"
	RequestID string              `json:"request_id"` // locally-minted correlation id
	Request   controlRequestInner `json:"request"`
}

// controlRequestInner carries the subtype and its subtype-specific fields. Field
// order is the wire order (encoding/json marshals in declaration order), so
// Subtype MUST stay first: the set_permission_mode line is pinned byte for byte
// against the one #1595 measured live, subtype before mode.
//
// The omitempty on Mode is load-bearing rather than cosmetic. Mode belongs to
// set_permission_mode only; without the tag every interrupt and every initialize
// line would grow a "mode":"" field it has no business carrying, and
// marshalInterruptEnvelope's and marshalInitializeEnvelope's output would stop
// matching the lines claude has been sent since #1120 and measured in #1763.
// TestMarshalInterruptEnvelope's and TestMarshalInitializeEnvelope's byte-exact
// wants are what hold this.
type controlRequestInner struct {
	Subtype string `json:"subtype"`        // "interrupt" | "set_permission_mode" | "initialize"
	Mode    string `json:"mode,omitempty"` // set_permission_mode only
}

// marshalInterruptEnvelope returns the single newline-terminated interrupt
// control line for requestID. The subtype/type are fixed literals, so the only
// caller-influenced field is the locally-minted request_id; the appended '\n' is
// the sole raw newline, making the envelope one physical line by construction.
func marshalInterruptEnvelope(requestID string) ([]byte, error) {
	env := controlRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   controlRequestInner{Subtype: "interrupt"},
	}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// WriteInterrupt writes one interrupt control_request line onto w, the child's
// held-open stdin (from Runner.Stdin). It mirrors WriteTurn minus the turncommit
// gate: an interrupt is not a queued turn, so there is nothing to claim or drop.
//
// A nil w means no live child: WriteInterrupt returns ErrNoLiveChild and writes
// nothing (checked first, so no panic and no partial write — AC2). A marshal
// failure (not reachable with fixed literals, defensive) and a write failure
// (e.g. EPIPE when the pipe closed mid-teardown) are returned wrapped; it never
// closes w — the io.Writer type structurally forbids a half-close/EOF forgery.
func WriteInterrupt(w io.Writer, requestID string) error {
	if w == nil {
		return ErrNoLiveChild
	}
	env, err := marshalInterruptEnvelope(requestID)
	if err != nil {
		return fmt.Errorf("streamsup: marshal interrupt: %w", err)
	}
	if _, err := w.Write(env); err != nil {
		return fmt.Errorf("streamsup: write interrupt: %w", err)
	}
	return nil
}

// permissionModeDefault is the posture a bypass revocation asks for, and the mode
// (*Runner).RevokeBypass names. It is the one member of the allow-list below with
// a production caller.
const permissionModeDefault = "default"

// permissionModeBypass is claude's escalating permission mode, and the only
// spelling of it — the mode-string counterpart to bypassPermissionsFlag above,
// which is the argv spelling. This is a MODE NAME and not a flag; the two are
// different strings for the same posture and neither is derived from the other.
//
// #1603 emptied this package's production source of this literal so the escalation
// stayed structurally absent from the write path, and #2066 puts it back
// deliberately, because that ticket's whole point is that the writer now admits the
// escalation. What replaces the absence is that the literal has exactly ONE
// spelling here with two readers — permissionModeAllowed, which admits it, and
// permissionModeSpawnWritable, which subtracts it back out for the spawn path — so
// a reader looking for "where does this package name the escalation?" finds one
// answer rather than a scattering of quoted strings.
const permissionModeBypass = "bypassPermissions"

// permissionModeAllowed reports whether mode is one claude's set_permission_mode
// accepts. It is a CLOSED allow-list and membership is the whole gate.
//
// It has SIX members since #2066, and the escalation is one of them. Until then it
// had five and refused the escalation by NON-MEMBERSHIP — #1603's carve-out, kept
// because claude gated the escalation on the launch argv and refused the control
// request in words on a child launched without --dangerously-skip-permissions
// (#1595). #2065 removed that gate by launching every child with the flag, and
// #2060 measured claude accepting the re-escalation on such a child at 2.1.239
// (internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_reescalate.json),
// so the mode is now one claude will parse and refusing it here would only mean
// the daemon's own routing sent a line this writer swallowed.
//
// The member is added rather than the gate opened, and the distinction is the
// whole safety argument. Every unanticipated spelling — a wrong case, a trailing
// space, a mode claude adds in a later version — is still refused, because the
// refusal is still by non-membership. A deny-list would fail open on each of them.
//
// The set is a switch rather than a package-level slice or map on purpose. A
// `var permissionModes = []string{…}` reads more like a list, but it is mutable
// package state holding a security allow-list: anything in this package, a test
// included, could append onto it and widen the vocabulary globally. Control flow
// cannot be appended to, and that argument gained rather than lost force when the
// list came to include the escalation.
//
// THIS IS A VOCABULARY CHECK, NOT AN AUTHORISATION CHECK, and after #2066 that
// sentence carries the ticket's whole risk. It answers "is this a permission mode
// claude will parse?" and nothing else. It does not answer "may this caller change
// this session's posture?" — that decision belongs to whoever accepts the mode from
// a wire frame, and for the escalation it is made in exactly two places: internal/
// relay's validPermissionMode, which refuses bypassPermissions as a mode string so
// the wire keeps exactly one spelling of the escalation (the YOLO bit), and
// sessions.Pool.UpdateSettings, which gates every stored posture. This writer is no
// longer a third stop and must not be mistaken for one.
//
// Membership also bounds the emitted line's LENGTH, which is load-bearing and easy
// to lose in a refactor. The longest member is bypassPermissions since #2066 — six
// bytes longer than the acceptEdits that held the title before it — so the envelope
// is 115 bytes with its newline, still far under PIPE_BUF, which is what keeps one
// write(2) atomic so a control line cannot interleave with a concurrent WriteTurn
// on the same fd. An unbounded caller-supplied mode could push the line past
// PIPE_BUF and tear it against a turn. A future widening of the vocabulary inherits
// that constraint, and it is measured rather than restated:
// TestMarshalPermissionModeEnvelope_LengthStaysUnderPipeBuf re-derives the longest
// member and its byte count against the POSIX PIPE_BUF floor of 512.
func permissionModeAllowed(mode string) bool {
	switch mode {
	case permissionModeDefault, "acceptEdits", "plan", "auto", "dontAsk", permissionModeBypass:
		return true
	}
	return false
}

// permissionModeSpawnWritable reports whether mode is one a FRESH SPAWN may be sent
// as its posture write. It is the allow-list minus the escalation, and it exists
// because permissionModeAllowed has two production readers that #2066 had to
// separate: WritePermissionMode, which widened, and spawnAndWait's postureID
// decision, which must not.
//
// SUBTRACTION, not a second switch. A seventh mode added to the allow-list becomes
// spawn-writable automatically, which is the right default, and the two lists
// cannot drift into disagreeing about what claude parses. The one carve-out is
// spelled once, here.
//
// Why the escalation is excluded is not caution. Since #2065 the launch argv
// asserts the escalation on every child, so a bypass session's fresh child is
// ALREADY in the posture its stored settings ask for: there is nothing to walk it
// back to and the write would be pure redundancy. It would also be the FIRST
// control request on a fresh stream, a shape nothing has measured — #2060 captured
// the escalation on an established stream after two turns — and spawnAndWait arms
// the posture gate CLOSED for any spawn that writes. A gate no write can ever
// release is a bricked session, so an unmeasured spawn-time escalation would risk
// refusing turns from a session's very first one. Excluding it keeps arm("") and
// keeps a bypass child's turns flowing with no ack dependence.
//
// The escalation still reaches a LIVE child, through SetPermissionMode. This
// predicate scopes the widening to the routing path; it does not undo it.
func permissionModeSpawnWritable(mode string) bool {
	return mode != permissionModeBypass && permissionModeAllowed(mode)
}

// marshalPermissionModeEnvelope returns the single newline-terminated
// set_permission_mode control line asking a running child to switch to mode.
// #1595 measured the default line live against claude 2.1.220 (success ack, next
// init reporting permissionMode default, matching behaviour on the following turn,
// no respawn) and #2041 measured acceptEdits, dontAsk, plan and auto the same way
// against 2.1.239.
//
// Like its three siblings in this file it is a PURE ENCODER with no gate of its
// own: the policy lives in the Write* half, where the nil-writer refusal already
// lives. That placement is deliberate and it is this writer's one residual — the
// encoder will mint a line for any mode string it is handed, escalation included.
// It has exactly ONE caller, WritePermissionMode, which holds the allow-list gate;
// a SECOND caller is the moment that gate has to move down into this function.
// Duplicating the check in both halves today would be two copies of one defence
// rather than a second one.
//
// mode and the locally-minted request_id are the only caller-influenced fields and
// both are marshalled as JSON string values, so json.Marshal escapes every
// metacharacter: neither can open a second physical line or rewrite the other's
// field. The appended '\n' is the sole raw newline, making the envelope one
// physical line by construction (structured encoding, never concatenation).
func marshalPermissionModeEnvelope(requestID, mode string) ([]byte, error) {
	env := controlRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request: controlRequestInner{
			Subtype: "set_permission_mode",
			Mode:    mode,
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// WritePermissionMode writes one set_permission_mode control_request line onto w,
// the child's held-open stdin (from Runner.Stdin), switching the live child's
// permission posture without killing it. It mirrors WriteInterrupt: refuse,
// marshal, one Write, never close w — the io.Writer type structurally forbids a
// half-close/EOF forgery.
//
// It replaced #1603's WriteBypassRevocation, which took no mode and emitted
// "default" alone. That writer's safety property was "no escalation reachable from
// this surface", and #2066 ENDS it deliberately rather than eroding it: the
// allow-list now admits the escalation, because a re-escalation is the transition
// that ticket routes in band. What this surface still guarantees is narrower and
// exact — no mode claude will not parse reaches the child, refused by
// non-membership so every unanticipated spelling is refused with it. Authorisation
// was never this function's job and is now the only stop the escalation has: see
// permissionModeAllowed for where it is made.
//
// The two refusals, in the order they are checked:
//
//   - mode outside permissionModeAllowed → ErrUnsupportedPermissionMode, bare.
//   - w == nil (no live child) → ErrNoLiveChild.
//
// The allow-list check comes FIRST, and the order is a contract rather than an
// accident. Both orderings write zero bytes, so this is not about the wire; it is
// about what the caller is told. Reversed, a caller naming a mode claude cannot
// parse while no child was bound would get back the RETRYABLE ErrNoLiveChild and
// could reasonably retry forever against a request that can never succeed. A
// vocabulary refusal is permanent and must read as permanent whatever the child is
// doing.
//
// requestID must be a LOCALLY-MINTED id, and Runner.nextControlID is the only
// source that satisfies it. There are TWO in-repo callers, not one:
// (*Runner).SetPermissionMode for a live child's posture change, and spawnAndWait
// for the spawn-time write — both mint off that counter, so the contract holds at
// each. (The count matters beyond bookkeeping: #2066's argument that the escalation
// is reachable only from the routing path is an argument about this caller set, so
// a reader auditing it needs the set to be right.) The structured encoding makes a
// hostile id non-catastrophic rather than merely unlikely, but the contract is the
// primary defence and the escaping the backstop.
//
// A marshal failure (not reachable for two strings, defensive) and a write failure
// (e.g. EPIPE when the pipe closed mid-teardown) are returned wrapped, never
// mis-reported as either refusal. No wrap carries the mode: an operator's value
// must not reach the daemon log through an error return (see
// ErrUnsupportedPermissionMode).
//
// The ack is not read HERE, but it IS read (#2064), and the distinction matters to
// anyone reasoning about the request_id. This function still writes and returns; the
// correlation lives one layer up, in the parser's noteControlAck, which matches the id
// against Runner.PostureGate and either opens the gate or records claude's refusal.
// Two consequences bind a caller: the id must stay locally minted and unique (see
// above), and a caller that reuses one would hand a stale ack the power to open a gate
// it does not belong to. Claude's own per-model refusal of auto (#2041) lands on that
// same path and is no longer discarded — it is what marks the gate refused.
func WritePermissionMode(w io.Writer, requestID, mode string) error {
	if !permissionModeAllowed(mode) {
		return ErrUnsupportedPermissionMode
	}
	if w == nil {
		return ErrNoLiveChild
	}
	env, err := marshalPermissionModeEnvelope(requestID, mode)
	if err != nil {
		return fmt.Errorf("streamsup: marshal permission mode: %w", err)
	}
	if _, err := w.Write(env); err != nil {
		return fmt.Errorf("streamsup: write permission mode: %w", err)
	}
	return nil
}

// marshalInitializeEnvelope returns the single newline-terminated initialize
// control line, the request that makes claude report what the session knows about
// itself — the model list (identifiers, display names, supported reasoning-effort
// levels) and the slash-command list. #1763 captured this line live against claude
// 2.1.239 across three arms, and all three agree field for field; the literal here
// is that line with the locally-minted request_id substituted for the capture's
// own probe id.
//
// The measurement is also why controlRequestInner needs no new field: the accepted
// request object carries subtype and nothing else, so Mode stays at its zero value
// and omitempty drops it. Like its two siblings every field but the request_id is
// a fixed literal, so the line has no injection surface of its own; the appended
// '\n' is the sole raw newline, making the envelope one physical line by
// construction (structured encoding, never string concatenation).
func marshalInitializeEnvelope(requestID string) ([]byte, error) {
	env := controlRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   controlRequestInner{Subtype: "initialize"},
	}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// WriteInitialize writes one initialize control_request line onto w, the child's
// held-open stdin (from Runner.Stdin), asking the live child to report what it
// knows about itself without a respawn. It mirrors WritePermissionMode minus the
// allow-list gate (this subtype has no caller-supplied field to gate): nil-check
// first, marshal, one Write, never close w — the io.Writer type
// structurally forbids a half-close/EOF forgery, which on a stream-json child
// would mean "no more input" and take the live session down without killing it.
//
// requestID must be a LOCALLY-MINTED id — Runner.nextControlID is the only source
// that satisfies this, and (*Runner).RequestInitialize is the only in-repo caller.
// The structured encoding makes a hostile id non-catastrophic rather than merely
// unlikely (json.Marshal escapes every metacharacter, so no id can open a second
// physical line or rewrite the fixed subtype), but the contract is the primary
// defence and the escaping the backstop.
//
// A nil w means no live child: WriteInitialize returns ErrNoLiveChild and writes
// nothing (checked first, so no panic and no partial write). A marshal failure
// (not reachable with fixed literals, defensive) and a write failure (e.g. EPIPE
// when the pipe closed mid-teardown) are returned wrapped — never mis-reported as
// the retryable ErrNoLiveChild.
//
// The control_response ack is not read here: this function writes the line and stops.
// Its ack is not ignored by the daemon, though, and the difference from
// WritePermissionMode's is worth stating — the parser's noteControlAck sees BOTH acks,
// off one counter, and refuses this one by id alone. That refusal is the whole reason
// the two spawn-time writes must never share an id: an initialize ack that matched
// would open a posture gate it never answered.
func WriteInitialize(w io.Writer, requestID string) error {
	if w == nil {
		return ErrNoLiveChild
	}
	env, err := marshalInitializeEnvelope(requestID)
	if err != nil {
		return fmt.Errorf("streamsup: marshal initialize: %w", err)
	}
	if _, err := w.Write(env); err != nil {
		return fmt.Errorf("streamsup: write initialize: %w", err)
	}
	return nil
}

// WriteTurn writes one user-turn stream-json envelope for prompt onto w, the
// child's held-open stdin (from Runner.Stdin). It writes exactly once and never
// closes w — holding stdin open for the next turn is the whole point of this
// path, and the io.Writer type structurally forbids a half-close/EOF forgery.
//
// ctx carries the turncommit gate on the queue-driven delivery path. WriteTurn
// claims it after the nil-writer check and before marshalling — mirroring
// supervisor.deliverViaSession on the PTY path. A false claim means the queued
// head was dropped during the wait for claude to go ready, so WriteTurn returns
// turncommit.ErrDropped and writes zero bytes: a dropped turn must never reach
// the live session. A nil gate (the non-queue paths, e.g. a direct single-turn
// send) delivers unconditionally.
//
// A nil w means no live child: WriteTurn returns ErrNoLiveChild and writes
// nothing, and does so BEFORE the gate is claimed — a head that cannot yet be
// written must stay droppable, so the retryable ErrNoLiveChild wins over a false
// gate. A write failure (e.g. EPIPE when the pipe closed mid-teardown) is
// returned wrapped; a closed-pipe write returns an error rather than panicking,
// so the teardown race (Stdin() captures the handle, teardown closes it, then
// WriteTurn writes) surfaces as the returned error, never a panic or false ack.
//
// The caller writes turn N+1 by calling WriteTurn(ctx, runner.Stdin(), next)
// again on the same handle — no re-open, no per-turn stdin lifecycle.
func WriteTurn(ctx context.Context, w io.Writer, prompt []byte) error {
	if w == nil {
		return ErrNoLiveChild
	}
	// The queue-driven delivery path carries a commit gate on ctx (#487, #1093).
	// It is claimed here — after WaitReady in the caller, before the envelope is
	// marshalled or written — to CLAIM the queued head for writing. A false claim
	// means the head was dropped during the ready-wait, so abort without writing a
	// single byte: a dropped message must never be injected into the live session.
	// A nil gate (the non-queue paths) writes unconditionally. Mirrors
	// supervisor.deliverViaSession's claim on the PTY path; ErrDropped is returned
	// bare so the queue can key drop-handling on errors.Is.
	if gate := turncommit.From(ctx); gate != nil && !gate() {
		return turncommit.ErrDropped
	}
	env, err := marshalTurnEnvelope(prompt)
	if err != nil {
		return fmt.Errorf("streamsup: marshal turn: %w", err)
	}
	if _, err := w.Write(env); err != nil {
		return fmt.Errorf("streamsup: write turn: %w", err)
	}
	return nil
}
