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
// set_permission_mode only; without the tag every interrupt line would grow a
// "mode":"" field it has no business carrying, and marshalInterruptEnvelope's
// output would stop matching the line claude has been sent since #1120.
// TestMarshalInterruptEnvelope's byte-exact want is what holds this.
type controlRequestInner struct {
	Subtype string `json:"subtype"`        // "interrupt" | "set_permission_mode"
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

// marshalBypassRevocationEnvelope returns the single newline-terminated
// set_permission_mode control line that drops a running child's bypass posture.
// #1595 measured this line live against claude 2.1.220: the child acked it with a
// success control_response, its next init line reported permissionMode default,
// and its behaviour on the following turn matched a default-launched control
// child exactly — all without a respawn.
//
// Like marshalInterruptEnvelope, every field but the locally-minted request_id is
// a fixed literal, so it has no injection surface of its own; the appended '\n' is
// the sole raw newline, making the envelope one physical line by construction.
//
// The emitted mode is fixed at default and is deliberately NOT a parameter. The
// opposite direction is a privilege escalation reachable over the daemon's own
// stdin, so it is absent from this surface rather than one argument away — and
// claude refuses it anyway: #1595 drove a request for the bypassPermissions mode
// on a child launched without --dangerously-skip-permissions and got back a
// refusal naming that missing flag. Re-granting bypass stays on the respawn path.
func marshalBypassRevocationEnvelope(requestID string) ([]byte, error) {
	env := controlRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request: controlRequestInner{
			Subtype: "set_permission_mode",
			Mode:    "default",
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// WriteBypassRevocation writes one set_permission_mode control_request line onto
// w, the child's held-open stdin (from Runner.Stdin), dropping the live child's
// bypass posture without killing it. It mirrors WriteInterrupt exactly: nil-check
// first, marshal, one Write, never close w.
//
// requestID must be a LOCALLY-MINTED id — Runner.nextControlID is the only source
// that satisfies this, and (*Runner).RevokeBypass is the only in-repo caller. The
// structured encoding makes a hostile id non-catastrophic rather than merely
// unlikely (json.Marshal escapes every metacharacter, so no id can open a second
// physical line or rewrite the fixed mode), but the contract is the primary
// defence and the escaping the backstop.
//
// A nil w means no live child: WriteBypassRevocation returns ErrNoLiveChild and
// writes nothing (checked first, so no panic and no partial write). A marshal
// failure (not reachable with fixed literals, defensive) and a write failure (e.g.
// EPIPE when the pipe closed mid-teardown) are returned wrapped.
//
// The control_response ack is not read here — nothing correlates the request_id,
// and reading it would need a stdout tap this slice has no consumer for. The
// parser already consumes the ack content-free (#1500), so the reply is handled
// without being interpreted.
func WriteBypassRevocation(w io.Writer, requestID string) error {
	if w == nil {
		return ErrNoLiveChild
	}
	env, err := marshalBypassRevocationEnvelope(requestID)
	if err != nil {
		return fmt.Errorf("streamsup: marshal bypass revocation: %w", err)
	}
	if _, err := w.Write(env); err != nil {
		return fmt.Errorf("streamsup: write bypass revocation: %w", err)
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
