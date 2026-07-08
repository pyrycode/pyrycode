package supervisor

import (
	"fmt"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// modalKey identifies one abstract keystroke intent, one level up from
// tui-driver's concrete key methods. Most resolve a modal (AcceptTrust/Answer/
// SendEsc); keyStartNewSession drives the "/clear" slash command, so the enum is
// an abstract keystroke intent, not strictly modal-resolution. It is unexported
// on purpose: the exported surface is the verb methods, not the enum.
type modalKey int

const (
	keyAcceptTrust     modalKey = iota // → Session.AcceptTrust()
	keyAnswer                          // → Session.Answer(choice)
	keyEsc                             // → Session.SendEsc()
	keyStartNewSession                 // → "/clear" via ClearInputLine + TypePrompt
)

// String renders the verb for the error-wrap prefix ("supervisor: <verb>: …").
func (k modalKey) String() string {
	switch k {
	case keyAcceptTrust:
		return "accept trust"
	case keyAnswer:
		return "answer"
	case keyEsc:
		return "send esc"
	case keyStartNewSession:
		return "start new session"
	default:
		return fmt.Sprintf("modalKey(%d)", int(k))
	}
}

// AcceptTrust sends claude's trust-folder accept keystroke ("1\r") to the live
// session. Answer and SendEsc are siblings. These three turn an abstract modal
// choice into the matching tui-driver keystroke so higher layers (the gated
// remote answer, cancel, deny-on-timeout legs) can resolve a modal without
// importing tui-driver or reaching into the child PTY directly. The primitive
// carries no trust decision — it routes whatever keystroke it is told to; the
// authorization (per-device gate, nonce validation, idempotency, option_id →
// choice mapping) lives entirely in the consumer.
//
// Unlike WriteUserTurn, these take no context.Context: a keystroke is a single
// non-blocking pty.Write with nothing to cancel (WriteUserTurn blocks for
// seconds on WaitReady + DeliverPrompt). A ctx here would advertise a
// cancellation contract the method cannot honor, so it is deliberately absent —
// do not cargo-cult one from WriteUserTurn.
//
// Returns ErrNoLiveSession (wrapped) when no claude child is attached, and
// writes no keystroke in that case. A PTY write error from a session torn down
// mid-write surfaces as a loud wrapped error, never a crash (see sendModalKey).
func (s *Supervisor) AcceptTrust() error {
	return s.sendModalKey(keyAcceptTrust, "")
}

// Answer sends modal choice (the literal option token claude renders, e.g. "1",
// "2", "y") followed by the commit. See AcceptTrust for the shared contract.
func (s *Supervisor) Answer(choice string) error {
	return s.sendModalKey(keyAnswer, choice)
}

// SendEsc sends a single ESC to dismiss a modal or cancel an in-flight turn.
// See AcceptTrust for the shared contract.
func (s *Supervisor) SendEsc() error {
	return s.sendModalKey(keyEsc, "")
}

// StartNewSession drives claude's "/clear" slash command into the live session,
// resetting the running conversation to a fresh session. Unlike the modal verbs
// it resolves no modal: it types a fixed slash command (see sendModalKeystroke
// for the exact ClearInputLine + TypePrompt sequence). It carries no trust
// decision and accepts no caller-supplied text — the "/clear" literal is fixed
// in code; any authorization lives in the future consumer. See AcceptTrust for
// the shared contract (no ctx, wrapped ErrNoLiveSession when detached, loud
// wrapped error on PTY failure).
func (s *Supervisor) StartNewSession() error {
	return s.sendModalKey(keyStartNewSession, "")
}

// sendModalKey captures the live Session under sessMu, releases the lock, then
// actuates the keystroke on the captured pointer — the same capture-then-release
// discipline as WriteUserTurn, so no lock is held across the PTY write. A
// concurrent setSession(nil)+Close racing the captured pointer is safe: the
// write lands in tui-driver's teardown-safe PTY-error path (no panic), surfacing
// as a loud wrapped error rather than a crash or a false success.
//
// A nil captured session returns ErrNoLiveSession without invoking keystrokeFn
// (writes nothing). Both the no-session and keystroke-error returns are wrapped
// with a stable "supervisor: <verb>:" prefix preserving the underlying error for
// errors.Is.
func (s *Supervisor) sendModalKey(k modalKey, choice string) error {
	s.sessMu.Lock()
	sess := s.sess
	s.sessMu.Unlock()

	if sess == nil {
		return fmt.Errorf("supervisor: %s: %w", k, ErrNoLiveSession)
	}
	if err := s.keystrokeFn(sess, k, choice); err != nil {
		return fmt.Errorf("supervisor: %s: %w", k, err)
	}
	return nil
}

// sendModalKeystroke is the production keystrokeFn: it routes one abstract
// modalKey to the matching tui-driver call on the captured session. It is the
// unexported-injection seam mirroring deliverViaSession — overridden only in
// tests, because the real tui-driver calls nil-deref the PTY on a zero-value
// Session, so verb dispatch cannot otherwise be unit-tested without a live
// claude.
func sendModalKeystroke(sess *tuidriver.Session, k modalKey, choice string) error {
	switch k {
	case keyAcceptTrust:
		return sess.AcceptTrust()
	case keyAnswer:
		return sess.Answer(choice)
	case keyEsc:
		return sess.SendEsc()
	case keyStartNewSession:
		// Drive claude's "/clear" (start a new session). ClearInputLine (Ctrl-U)
		// first so "/" lands at column 0: the slash-picker only opens when "/" is
		// the first character of the input line, so any drafted text would render
		// "/clear" as literal text ("hello/clear") and open no picker. TypePrompt
		// then types "/clear" byte-by-byte — each byte opens then filters the
		// slash-picker — settles, and commits with a trailing "\r" (Enter runs the
		// highlighted /clear entry). TypePrompt, not SendKeys or a paste: only
		// genuine byte-spaced typing makes claude register input and open the
		// picker; a bulk/paste write is treated as prompt text and opens nothing.
		if err := sess.ClearInputLine(); err != nil {
			return err
		}
		return sess.TypePrompt("/clear")
	default:
		// Unreachable from the exported API (the three verb methods pass only
		// the constants above). A programmer bug if hit — return loudly rather
		// than panic, per CODING-STYLE (panic is for unreachable code only).
		return fmt.Errorf("unknown modal key %d", int(k))
	}
}
