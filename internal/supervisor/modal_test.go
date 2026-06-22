package supervisor

import (
	"errors"
	"strings"
	"testing"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// TestSupervisor_ModalKeystroke_DispatchesAbstractVerb covers AC-1/AC-4 (the
// correct call fires): each verb method delegates the right (modalKey, choice)
// to the keystrokeFn seam and returns nil. The seam records what it receives, so
// no live claude is needed to prove the abstract dispatch.
func TestSupervisor_ModalKeystroke_DispatchesAbstractVerb(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		call    func(*Supervisor) error
		wantKey modalKey
		wantArg string
	}{
		{"accept trust", func(s *Supervisor) error { return s.AcceptTrust() }, keyAcceptTrust, ""},
		{"answer", func(s *Supervisor) error { return s.Answer("2") }, keyAnswer, "2"},
		{"send esc", func(s *Supervisor) error { return s.SendEsc() }, keyEsc, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sup, err := New(helperConfig("exit0"))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			sup.setSession(&tuidriver.Session{})

			var gotKey modalKey
			var gotArg string
			called := false
			sup.keystrokeFn = func(_ *tuidriver.Session, k modalKey, choice string) error {
				called = true
				gotKey = k
				gotArg = choice
				return nil
			}

			if err := tt.call(sup); err != nil {
				t.Fatalf("%s = %v, want nil", tt.name, err)
			}
			if !called {
				t.Fatal("keystrokeFn was not called")
			}
			if gotKey != tt.wantKey {
				t.Errorf("modalKey = %v, want %v", gotKey, tt.wantKey)
			}
			if gotArg != tt.wantArg {
				t.Errorf("choice = %q, want %q", gotArg, tt.wantArg)
			}
		})
	}
}

// TestSupervisor_ModalKeystroke_NoLiveSessionFailsLoud covers AC-3/AC-4 (sends
// nothing): with no session registered, each verb returns ErrNoLiveSession
// wrapped with its per-verb prefix and never invokes keystrokeFn.
func TestSupervisor_ModalKeystroke_NoLiveSessionFailsLoud(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		call       func(*Supervisor) error
		wantPrefix string
	}{
		{"accept trust", func(s *Supervisor) error { return s.AcceptTrust() }, "supervisor: accept trust:"},
		{"answer", func(s *Supervisor) error { return s.Answer("2") }, "supervisor: answer:"},
		{"send esc", func(s *Supervisor) error { return s.SendEsc() }, "supervisor: send esc:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sup, err := New(helperConfig("exit0"))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			// Deliberately no setSession: no live session is registered.

			called := false
			sup.keystrokeFn = func(_ *tuidriver.Session, _ modalKey, _ string) error {
				called = true
				return nil
			}

			err = tt.call(sup)
			if !errors.Is(err, ErrNoLiveSession) {
				t.Errorf("err = %v, want errors.Is(err, ErrNoLiveSession)", err)
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantPrefix) {
				t.Errorf("err = %v, want prefix %q", err, tt.wantPrefix)
			}
			if called {
				t.Error("keystrokeFn called on no-live-session path, want no keystroke written")
			}
		})
	}
}

// TestSupervisor_ModalKeystroke_KeystrokeErrorFailsLoud covers a PTY write error
// from the seam: the verb wraps it with the stable prefix and preserves it for
// errors.Is. Mirrors TestSupervisor_WriteUserTurn_DeliverErrorFailsLoud.
func TestSupervisor_ModalKeystroke_KeystrokeErrorFailsLoud(t *testing.T) {
	t.Parallel()

	sup, err := New(helperConfig("exit0"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sup.setSession(&tuidriver.Session{})
	boom := errors.New("pty closed")
	sup.keystrokeFn = func(_ *tuidriver.Session, _ modalKey, _ string) error { return boom }

	err = sup.AcceptTrust()
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want errors.Is(err, boom)", err)
	}
	if err == nil || !strings.Contains(err.Error(), "supervisor: accept trust:") {
		t.Errorf("err = %v, want the wrap prefix", err)
	}
}

// TestSupervisor_SendModalKeystroke_UnknownKeyReturnsError covers the
// programmer-bug guard: an unknown modalKey returns an error rather than
// panicking. The default branch never touches the (zero-value) session, so this
// is safe with a nil PTY.
func TestSupervisor_SendModalKeystroke_UnknownKeyReturnsError(t *testing.T) {
	t.Parallel()

	if err := sendModalKeystroke(&tuidriver.Session{}, modalKey(99), ""); err == nil {
		t.Fatal("sendModalKeystroke(modalKey(99)) = nil, want non-nil error")
	}
}
