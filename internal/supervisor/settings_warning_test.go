package supervisor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// discardLog is a logger that swallows output; used where the record is not
// under assertion.
func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// settingsWarningErr is a stand-in for the *UnexpectedModalError WaitReady
// returns when claude's Settings Warning (ModalClassUnknown + selection shape)
// is up at startup idle.
func settingsWarningErr() error {
	return &tuidriver.UnexpectedModalError{Class: tuidriver.ModalClassUnknown}
}

// TestWaitReadyAutoContinue_HappyPath is the AC1+AC3 seam: a Settings Warning at
// startup is auto-answered Continue and readiness then proceeds (returns nil, so
// the queued turn is delivered rather than wedging). WaitReady first surfaces the
// unexpected modal, the detector confirms it is the Settings Warning, the answer
// clears it, and the re-gate returns ready.
func TestWaitReadyAutoContinue_HappyPath(t *testing.T) {
	t.Parallel()

	answered := false
	waitCalls := 0
	answerCalls := 0
	d := waitReadyDeps{
		waitReady: func(context.Context) error {
			waitCalls++
			if waitCalls == 1 {
				return settingsWarningErr()
			}
			return nil
		},
		isSettingsWarning: func() bool { return !answered }, // clears once answered
		answerContinue:    func() error { answerCalls++; answered = true; return nil },
		log:               discardLog(),
		dismissTimeout:    200 * time.Millisecond,
		dismissPoll:       time.Millisecond,
	}

	if err := waitReadyAutoContinue(context.Background(), d); err != nil {
		t.Fatalf("waitReadyAutoContinue = %v, want nil (turn proceeds)", err)
	}
	if answerCalls != 1 {
		t.Errorf("answerContinue called %d times, want exactly 1", answerCalls)
	}
	if waitCalls != 2 {
		t.Errorf("waitReady called %d times, want 2 (initial + re-gate)", waitCalls)
	}
}

// TestWaitReadyAutoContinue_OtherModalPassthrough covers the AC1 negative /
// AC4-adjacent contract: an unexpected modal that is NOT the Settings Warning
// (a trust-forward / future consent gate / MCP dialog) is returned unchanged and
// no keystroke is sent — we never blindly type "1" into an unrecognized dialog.
func TestWaitReadyAutoContinue_OtherModalPassthrough(t *testing.T) {
	t.Parallel()

	other := &tuidriver.UnexpectedModalError{Class: tuidriver.ModalClassPermission}
	answerCalls := 0
	d := waitReadyDeps{
		waitReady:         func(context.Context) error { return other },
		isSettingsWarning: func() bool { return false }, // not the Settings Warning
		answerContinue:    func() error { answerCalls++; return nil },
		log:               discardLog(),
	}

	err := waitReadyAutoContinue(context.Background(), d)
	if !errors.Is(err, other) {
		t.Errorf("err = %v, want the modal error returned unchanged", err)
	}
	if answerCalls != 0 {
		t.Errorf("answerContinue called %d times, want 0 (no keystroke into an unrecognized dialog)", answerCalls)
	}
}

// TestWaitReadyAutoContinue_NonModalErrorPassthrough proves a non-modal readiness
// error (ctx timeout, ProcessExited) passes straight through — fail-loud
// preserved, no keystroke.
func TestWaitReadyAutoContinue_NonModalErrorPassthrough(t *testing.T) {
	t.Parallel()

	answerCalls := 0
	d := waitReadyDeps{
		waitReady:         func(context.Context) error { return context.DeadlineExceeded },
		isSettingsWarning: func() bool { t.Error("isSettingsWarning consulted on non-modal error"); return false },
		answerContinue:    func() error { answerCalls++; return nil },
		log:               discardLog(),
	}

	err := waitReadyAutoContinue(context.Background(), d)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want errors.Is(context.DeadlineExceeded)", err)
	}
	if answerCalls != 0 {
		t.Errorf("answerContinue called %d times, want 0", answerCalls)
	}
}

// TestWaitReadyAutoContinue_ReadyPassthrough proves the common case — WaitReady
// returns nil (idle, no modal) — short-circuits with no detector or keystroke.
func TestWaitReadyAutoContinue_ReadyPassthrough(t *testing.T) {
	t.Parallel()

	d := waitReadyDeps{
		waitReady:         func(context.Context) error { return nil },
		isSettingsWarning: func() bool { t.Error("isSettingsWarning consulted on a ready screen"); return false },
		answerContinue:    func() error { t.Error("answerContinue called on a ready screen"); return nil },
		log:               discardLog(),
	}

	if err := waitReadyAutoContinue(context.Background(), d); err != nil {
		t.Errorf("waitReadyAutoContinue = %v, want nil", err)
	}
}

// TestWaitReadyAutoContinue_KeystrokeErrorWraps covers a PTY write error from the
// answer keystroke: it wraps with the stable "auto-continue settings warning:"
// prefix and preserves the underlying error for errors.Is.
func TestWaitReadyAutoContinue_KeystrokeErrorWraps(t *testing.T) {
	t.Parallel()

	boom := errors.New("pty closed")
	d := waitReadyDeps{
		waitReady:         func(context.Context) error { return settingsWarningErr() },
		isSettingsWarning: func() bool { return true },
		answerContinue:    func() error { return boom },
		log:               discardLog(),
	}

	err := waitReadyAutoContinue(context.Background(), d)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want errors.Is(err, boom)", err)
	}
	if err == nil || !strings.Contains(err.Error(), "auto-continue settings warning:") {
		t.Errorf("err = %v, want the auto-continue wrap prefix", err)
	}
}

// TestWaitReadyAutoContinue_DialogStuckAfterAnswer proves the load-bearing
// dismissal poll: if the dialog never clears within the bound, the gate returns a
// loud retryable error, answers exactly once, and never reaches the re-gate.
func TestWaitReadyAutoContinue_DialogStuckAfterAnswer(t *testing.T) {
	t.Parallel()

	waitCalls := 0
	answerCalls := 0
	d := waitReadyDeps{
		waitReady:         func(context.Context) error { waitCalls++; return settingsWarningErr() },
		isSettingsWarning: func() bool { return true }, // never clears
		answerContinue:    func() error { answerCalls++; return nil },
		log:               discardLog(),
		dismissTimeout:    20 * time.Millisecond,
		dismissPoll:       time.Millisecond,
	}

	err := waitReadyAutoContinue(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "dialog still present") {
		t.Errorf("err = %v, want a 'dialog still present' error", err)
	}
	if !errors.As(err, new(*tuidriver.UnexpectedModalError)) {
		t.Errorf("err = %v, want the original modal error preserved as the cause", err)
	}
	if answerCalls != 1 {
		t.Errorf("answerContinue called %d times, want exactly 1", answerCalls)
	}
	if waitCalls != 1 {
		t.Errorf("waitReady called %d times, want 1 (re-gate not reached after a stuck dialog)", waitCalls)
	}
}

// TestWaitReadyAutoContinue_LogsWarning covers AC2: the auto-continue emits a
// structured slog warning carrying the workdir so the operator can find and fix
// the skipped settings. It is a log, not a client-facing modal.
func TestWaitReadyAutoContinue_LogsWarning(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	answered := false
	waitCalls := 0
	d := waitReadyDeps{
		waitReady: func(context.Context) error {
			waitCalls++
			if waitCalls == 1 {
				return settingsWarningErr()
			}
			return nil
		},
		isSettingsWarning: func() bool { return !answered },
		answerContinue:    func() error { answered = true; return nil },
		log:               slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})),
		workDir:           "/tmp/wd994",
		dismissTimeout:    200 * time.Millisecond,
		dismissPoll:       time.Millisecond,
	}

	if err := waitReadyAutoContinue(context.Background(), d); err != nil {
		t.Fatalf("waitReadyAutoContinue = %v, want nil", err)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("log = %q, want a WARN record", out)
	}
	if !strings.Contains(out, "auto-continuing") {
		t.Errorf("log = %q, want the auto-continue message", out)
	}
	if !strings.Contains(out, "workdir=/tmp/wd994") {
		t.Errorf("log = %q, want the workdir field", out)
	}
}

// TestSupervisor_ReadyDeps_WiresSeams pins the production wiring: readyDeps binds
// answerContinue to keystrokeFn(sess, keyAnswer, "1") — Continue is claude's
// option 1 — and isSettingsWarning to settingsWarningFn over the captured session.
func TestSupervisor_ReadyDeps_WiresSeams(t *testing.T) {
	t.Parallel()

	sup, err := New(helperConfig("exit0"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &tuidriver.Session{}

	var gotKey modalKey
	var gotArg string
	keystrokeCalls := 0
	sup.keystrokeFn = func(gotSess *tuidriver.Session, k modalKey, choice string) error {
		keystrokeCalls++
		if gotSess != sess {
			t.Errorf("keystrokeFn got session %p, want the captured %p", gotSess, sess)
		}
		gotKey, gotArg = k, choice
		return nil
	}
	detectCalls := 0
	sup.settingsWarningFn = func(gotSess *tuidriver.Session) bool {
		detectCalls++
		if gotSess != sess {
			t.Errorf("settingsWarningFn got session %p, want the captured %p", gotSess, sess)
		}
		return true
	}

	d := sup.readyDeps(sess)

	if !d.isSettingsWarning() {
		t.Error("isSettingsWarning() = false, want true (fake detector)")
	}
	if detectCalls != 1 {
		t.Errorf("settingsWarningFn called %d times, want 1", detectCalls)
	}
	if err := d.answerContinue(); err != nil {
		t.Fatalf("answerContinue = %v, want nil", err)
	}
	if keystrokeCalls != 1 {
		t.Errorf("keystrokeFn called %d times, want 1", keystrokeCalls)
	}
	if gotKey != keyAnswer || gotArg != "1" {
		t.Errorf("keystroke = (%v, %q), want (keyAnswer, \"1\")", gotKey, gotArg)
	}
}

// TestSupervisor_NewWiresSettingsWarningFn proves New installs the production
// detector as the default settingsWarningFn seam (immutable-post-New, like
// keystrokeFn).
func TestSupervisor_NewWiresSettingsWarningFn(t *testing.T) {
	t.Parallel()

	sup, err := New(helperConfig("exit0"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sup.settingsWarningFn == nil {
		t.Fatal("settingsWarningFn is nil after New, want the production detector")
	}
}
