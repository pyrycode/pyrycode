package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// settingsWarningAnchor is the sole claude-screen literal in internal/supervisor
// (elsewhere all substrate knowledge stays inside tui-driver; see the SECURITY
// note at ScreenSnapshot). It is a deliberate, contained exception:
//
//   - substrate-guard stays green: the guard bans the fixed set of tokens
//     tui-driver OWNS (trust modal, spinner, paste chip, network failure).
//     tui-driver owns nothing about the Settings Warning — no ModalClass, no
//     handling — so an anchor for a dialog it doesn't handle is not the
//     re-coupling the guard exists to prevent.
//   - It refines an already-trusted structural signal, never stands alone: the
//     anchor is consulted ONLY after WaitReady already returned an
//     *UnexpectedModalError, i.e. tui-driver already structurally confirmed a
//     genuine blocking selection dialog at pre-first-prompt idle (#224). This
//     adds a content co-signal (the title) to a structural signal — belt and
//     suspenders of different fabric, across the layer boundary.
//
// The architecturally ideal owner is tui-driver (a ModalClassSettingsWarning
// with a structural co-signal); that is a cross-repo release + go.mod bump #994
// deliberately avoids. See the spec's § Substrate boundary.
const settingsWarningAnchor = "Settings Warning" // dialog title, from the #988 live observation

// settingsWarningDismissTimeout / settingsWarningDismissPoll bound the post-answer
// dismissal poll. Answer("1") only writes the keystroke — the dialog stays
// rendered (and reads as idle) for a few hundred ms until claude re-renders — so
// the gate must wait for it to actually clear before re-gating readiness. Values
// mirror tui-driver's DefaultAnswerConfirmTimeout / answerConfirmPoll; comfortably
// inside the daemon's multi-second per-turn readiness budget. Tests shrink them
// via waitReadyDeps fields.
const (
	settingsWarningDismissTimeout = 2 * time.Second
	settingsWarningDismissPoll    = 150 * time.Millisecond
)

// detectSettingsWarning is the production settingsWarningFn: it renders the live
// snapshot and reports whether the Settings Warning title is present. Render+match
// in one expression — no named rendered var — mirroring ScreenSnapshot, so no
// rendered-text variable is stored. Like sendModalKeystroke it nil-derefs a
// zero-value Session's PTY, so it is overridden in tests.
func detectSettingsWarning(sess *tuidriver.Session) bool {
	return strings.Contains(tuidriver.Render(sess.Snapshot(), 0, 0), settingsWarningAnchor)
}

// waitReadyDeps are the seams waitReadyAutoContinue drives. deliverViaSession
// wires the real Session methods via (*Supervisor).readyDeps; tests inject fakes
// to script readiness, detection, and the answer keystroke with no live claude
// and no screen literal (the anchor stays in production detectSettingsWarning) —
// the same "seam one level above the screen" pattern as deliverGrowthDeps.
// dismissTimeout/dismissPoll are fields rather than package vars so parallel
// -race tests can shrink them without sharing mutable global state.
type waitReadyDeps struct {
	waitReady         func(ctx context.Context) error // wraps readyForDelivery(ctx, Session.WaitReady) — keeps the #988 trust-modal gate in front
	isSettingsWarning func() bool                     // wraps settingsWarningFn(sess)
	answerContinue    func() error                    // wraps keystrokeFn(sess, keyAnswer, "1")
	log               *slog.Logger
	workDir           string
	dismissTimeout    time.Duration // post-answer dismissal-poll bound; 0 → settingsWarningDismissTimeout
	dismissPoll       time.Duration // dismissal poll cadence;          0 → settingsWarningDismissPoll
}

// waitReadyAutoContinue gates readiness, auto-answering Continue past claude's
// informational Settings Warning startup dialog exactly once (#994). The malformed
// settings live in the operator's own host config (a trusted party), so continuing
// past the warning accepts claude's documented default — not a remote-consent
// decision — which is why this dialog is auto-continued locally rather than
// forwarded like the trust dialog.
//
// Returns nil once claude is ready; returns the underlying error unchanged for
// every non-Settings-Warning outcome so fail-loud is preserved for ctx
// cancel/timeout, *ProcessExitedError, the pending trust modal (#988), and every
// OTHER unexpected modal (a future consent gate, the MCP-enablement dialog). We
// never blindly type "1" into an unrecognized dialog: the keystroke fires only
// when tui-driver's structural *UnexpectedModalError coincides with the content
// co-signal isSettingsWarning().
//
// The auto-continue is attempted at most once per delivery — no recursion: after
// answering and confirming the dialog cleared, readiness is re-gated a single
// time, so a NEW unexpected modal that pops after Continue surfaces its own error
// (fail-loud, bounded).
func waitReadyAutoContinue(ctx context.Context, d waitReadyDeps) error {
	err := d.waitReady(ctx)
	if err == nil {
		return nil // common case: ready, no modal.
	}
	var modalErr *tuidriver.UnexpectedModalError
	if !errors.As(err, &modalErr) || !d.isSettingsWarning() {
		return err // ctx/exit error, trust-modal-pending, or a different unexpected modal → fail-loud, no keystroke.
	}

	log := d.log
	if log == nil {
		log = slog.Default()
	}
	log.Warn("supervisor: auto-continuing claude Settings Warning at startup", "workdir", d.workDir)

	if kerr := d.answerContinue(); kerr != nil {
		return fmt.Errorf("auto-continue settings warning: %w", kerr)
	}
	if !settingsWarningDismissed(ctx, d) {
		return fmt.Errorf("auto-continue settings warning: dialog still present after answering: %w", err)
	}
	return d.waitReady(ctx) // re-gate once; ready → nil, a new modal → its error.
}

// settingsWarningDismissed polls until the Settings Warning is no longer detected
// (the dismissal signal), the timeout elapses, or ctx is cancelled. It only
// observes — reusing the same detector seam — never writes. Load-bearing: without
// it an immediate re-WaitReady would re-catch the still-rendered dialog and fail
// loud. Mirrors tui-driver's modalDismissed poll structure.
func settingsWarningDismissed(ctx context.Context, d waitReadyDeps) bool {
	timeout := d.dismissTimeout
	if timeout <= 0 {
		timeout = settingsWarningDismissTimeout
	}
	poll := d.dismissPoll
	if poll <= 0 {
		poll = settingsWarningDismissPoll
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tk := time.NewTicker(poll)
	defer tk.Stop()
	for {
		if !d.isSettingsWarning() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-tk.C:
		}
	}
}

// readyDeps builds the production waitReadyDeps for a captured session. The
// keystroke and detection actuate on the captured sess (not a re-capture), the
// same discipline as deliverViaSession. waitReady wraps readyForDelivery so the
// #988 trust-modal gate runs in front of the auto-continue: a pending trust modal
// yields ErrTrustModalPending (not an *UnexpectedModalError), so it passes through
// waitReadyAutoContinue unchanged and stays fail-loud. Timeout/poll are left zero
// (package defaults apply).
func (s *Supervisor) readyDeps(sess *tuidriver.Session) waitReadyDeps {
	return waitReadyDeps{
		waitReady:         func(ctx context.Context) error { return readyForDelivery(ctx, sess.WaitReady) },
		isSettingsWarning: func() bool { return s.settingsWarningFn(sess) },
		answerContinue:    func() error { return s.keystrokeFn(sess, keyAnswer, "1") },
		log:               s.log,
		workDir:           s.cfg.WorkDir,
	}
}
