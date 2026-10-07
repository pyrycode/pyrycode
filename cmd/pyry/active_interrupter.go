package main

import (
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// interruptArm names which actuation interruptRunner dispatched to. The constant
// VALUES are operator-facing: activeInterrupter.SendEsc logs them verbatim as a
// record's arm field (#1193), so they are part of the record contract, not an
// internal detail.
type interruptArm string

const (
	armInterrupt interruptArm = "interrupt" // sessions.Runner.Interrupt()
)

// interruptRunner actuates a runner's interrupt through sessions.Runner.Interrupt.
// Interrupt has been ON the interface since #2592, so there is no inert arm: a
// runner that cannot interrupt does not compile, rather than silently dropping the
// frame as the type assertion this replaced did.
//
// It returns the arm it dispatched to alongside the method's error, so the caller —
// the only scope holding the conversation id — can record which arm ran (#1193).
// The dispatcher itself stays pure: no logger, no ambient state. The arm is an
// OBSERVABILITY value and nothing may branch on it beyond selecting a record.
func interruptRunner(r sessions.Runner) (interruptArm, error) {
	return armInterrupt, r.Interrupt()
}

// activeInterrupter satisfies relay.Interrupter by routing an inbound interrupt to
// the runner bound to the conversation the frame NAMES (#2103), falling back to the
// ACTIVE conversation when it names none — replacing the former Interrupter: w.sup
// wiring that mis-delivered every interrupt to the bootstrap supervisor regardless
// of which conversation's turn was running (#1121). The two seams are injected (not
// raw *Pool/*Registry) so active_interrupter_test.go can drive the composition with
// fakes; production wires currentConv: active.CurrentConversation and resolveRunner
// over resolveBoundRunner(convReg, pool, …), and #2103 changed neither field, only
// how SendEsc picks the id it passes.
type activeInterrupter struct {
	currentConv   func() string
	resolveRunner func(convID string) (sessions.Runner, bool)

	// log records which arm an inbound interrupt took (#1192). Optional: nil
	// falls back to slog.Default() via logger().
	log *slog.Logger
}

// logger returns a's logger, falling back to slog.Default() when unset.
// activeInterrupter is a constructor-less bag of injected seams built as a
// named-field literal, so an omitted field is a reachable state — and a nil
// *slog.Logger panics on first use, which on this remotely-driven relay path
// would be a latent crash on a rarely-hit inert arm. Falling back to the default
// logger (not a discard handler) keeps an unwired literal's record visible.
func (a activeInterrupter) logger() *slog.Logger {
	if a.log == nil {
		return slog.Default()
	}
	return a.log
}

// SendEsc interrupts the runner bound to the conversation the frame NAMES, or —
// when it names none — the one the daemon's cursor points at (#1121, widened by
// #2103). It keeps the relay seam's method name (relay.Interrupter.SendEsc) even
// though the actuation is a per-runner interrupt, not literally an Esc — the seam
// doc already abstracts SendEsc as "claude's own interrupt" (#1121 seam decision),
// so renaming would churn the whole internal/relay package for no behavioural gain.
//
// conversationID is UNTRUSTED: it is the string a paired client put on the wire,
// and internal/relay forwards it unjudged because it can neither shape-check nor
// resolve it (relay.Interrupter's own doc block states that division). This method
// is the trust boundary, and the order below is what discharges it.
//
// The empty-string branch runs BEFORE conversations.ValidID, not after:
// ValidID("") is false by that function's own doc, so reversing the two would
// refuse every un-upgraded client's bare frame — the only shape interrupt had from
// #707 until #2103 — and silently break backward compatibility. From there every
// ambiguous state (no active conversation, a non-canonical named id, an
// unknown/unbound binding) is inert (nil), never actuating the wrong child; the
// actuation's own error propagates for handleInterrupt to Warn-log and tolerate
// (best-effort contract).
//
// THERE IS DELIBERATELY NO LIVENESS PROBE, and this is where #2103 parts from its
// twin rather than by oversight. activeSessionStarter.StartNewSession needs
// `named && State().ChildPID == 0` because RestartFresh on a childless runner
// rekeys the pool, persists sessions.json, rebinds the conversation and broadcasts
// a session_transition — observable damage with no session to show for it. This
// actuator has no such hazard: streamsup.WriteInterrupt checks its writer for nil
// FIRST and returns ErrNoLiveChild having written nothing, so a conversation with
// no live child is inert by construction on the named and cursor paths alike.
// Adding the probe would introduce a refusal the bare path does not have today,
// which is the pre-#2103 behaviour AC-2 exists to preserve. Generalises: a
// blocker's late fix is not automatically the twin's requirement — trace the twin's
// actuation to its own write site before copying a guard across.
// EVERY arm records which one it took, at Info so the records are visible at the
// daemon's default level (#1192, #1193) — the inert ones and the actuation.
// Combined with handleInterrupt's
// own records that closes the route: an interrupt reaching it always leaves at
// least one v2.interrupt.* record, so on a wired daemon an empty log means the
// frame never arrived. The dispatched record is written when the arm RETURNS —
// under #1193's direction choice the arm identity IS interruptRunner's return
// value — so an actuation that blocked forever would leave none; both actuations
// are single small writes and no such hang has been observed. The records identify
// the CONVERSATION: resolveBoundRunner never surfaces the bound session id to this
// caller.
//
// The cursor is never WRITTEN here, and on a named frame it is not even READ; only
// sessionRouter.Route stamps it. Interrupting a named conversation therefore leaves
// the active conversation exactly where it was.
func (a activeInterrupter) SendEsc(conversationID string) error {
	convID := conversationID
	switch {
	case convID == "":
		// Nothing named: the pre-#2103 path, verbatim. The cursor's own id is
		// daemon-authored and is deliberately NOT shape-checked — doing so would
		// change behaviour on the path this branch exists to preserve.
		convID = a.currentConv()
		if convID == "" {
			// No conversation id to carry — the record's information is its existence:
			// the frame reached SendEsc and nothing was active.
			a.logger().Info("relay: v2 interrupt inert; no active conversation",
				"event", "v2.interrupt.no_active_conv")
			return nil
		}
	case !conversations.ValidID(convID):
		// The ONE arm where an arbitrary client-chosen string reaches a log call, so
		// the ONE that needs boundedConvID: every arm below logs an id that has
		// passed ValidID and is provably 36 bytes. Refused before the registry is
		// touched, so a non-canonical string never becomes a lookup key.
		a.logger().Info("relay: v2 interrupt inert; named conversation id is not canonical",
			"event", "v2.interrupt.invalid_conv_id",
			"conversation_id", boundedConvID(convID))
		return nil
	}
	r, ok := a.resolveRunner(convID)
	if !ok {
		// One record for the unknown id and the known-but-unbound one alike:
		// resolveBoundRunner refuses both identically, so this caller structurally
		// cannot distinguish them — which is also what keeps the frame from
		// answering "does this conversation exist?" to a client that gets no reply.
		a.logger().Info("relay: v2 interrupt inert; conversation has no bound runner",
			"event", "v2.interrupt.no_bound_runner",
			"conversation_id", convID)
		return nil
	}
	arm, err := interruptRunner(r)
	// Emitted even when the arm returned an error: this records WHICH arm was
	// dispatched to, not that the child quiesced (hence dispatched, not actuated).
	// handleInterrupt's v2.interrupt.keystroke_err carries the error but not the
	// arm, so on a failed actuation the PAIR is what names the failing actuation —
	// suppressing this record on error would delete that. The error itself is NOT
	// repeated here: keystroke_err already carries it, and logging a wrapped
	// supervisor/streamsup sentinel twice under two correlation keys widens the
	// record surface for no diagnostic gain. arm is logged as a plain string, never
	// the named
	// type: slog renders a named string type through the Any path, and relay's
	// TextHandler and cmd/pyry's JSONHandler do not agree on how that renders.
	a.logger().Info("relay: v2 interrupt dispatched",
		"event", "v2.interrupt.dispatched",
		"conversation_id", convID,
		"arm", string(arm))
	return err
}
