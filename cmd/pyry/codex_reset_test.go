package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// TestCodexReset_WritesHandoffNote: new_session on a Codex conversation whose
// app-server is live runs the wrap-up turn on the fake Codex, stores its reply as
// the handoff note, and reports wrapping_up, then restarting with the note
// written, then the falling edge (#2663). The runner is built by the production
// factory, so the capture is the one it chains into the Codex sink.
func TestCodexReset_WritesHandoffNote(t *testing.T) {
	factory := newCodexRunnerFactory(codexHarness{
		bin:  fakeCodexBin(t),
		home: filepath.Join(t.TempDir(), "codex-home"),
		sink: newStreamTurnSink(0, nil),
	})
	built, err := factory(sessions.RunnerConfig{SessionID: resetSessionA, Harness: harnessCodex, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("factory = %v", err)
	}
	h := &codexHarnessT{r: built.(*codexRunner)}
	h.run(t)
	h.bound(t, nil)

	notes := newFakeNotes("")
	bcast := newResettingBcast(interactiveConns()...)
	starter := activeSessionStarter{
		resolveBound: func(convID string) (sessions.Runner, sessions.SessionID, string, bool) {
			return h.r, resetSessionA, "", convID == resetConvA
		},
		rotate: func(old sessions.SessionID) (sessions.SessionID, error) {
			return "fresh-" + old, nil
		},
		reset: &conversationReset{
			base: context.Background(),
			resolve: func(convID string) (resetTarget, bool) {
				return resetTarget{runner: h.r, write: h.r.WriteUserTurn}, convID == resetConvA
			},
			notes:    notes,
			deadline: 10 * time.Second,
			log:      quietLogger(),
		},
		resetting: attachedEmitter(bcast),
		log:       quietLogger(),
	}

	outcome := make(chan error, 1)
	starter.StartNewSessionLate(resetConvA, func(err error) { outcome <- err })
	select {
	case err := <-outcome:
		if err != nil {
			t.Fatalf("new_session outcome = %v, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("new_session outcome never arrived")
	}
	// The falling edge is deferred past the outcome, so wait for it.
	deadline := time.Now().Add(5 * time.Second)
	for bcast.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if got := notes.stored(resetConvA); got != "fakecodex reply" {
		t.Errorf("stored handoff note = %q, want the wrap-up reply %q", got, "fakecodex reply")
	}
	assertEdges(t, bcast.recorded(),
		edge{active: true, phase: protocol.ResetPhaseWrappingUp, handoff: protocol.ResetHandoffPending},
		edge{active: true, phase: protocol.ResetPhaseRestarting, handoff: protocol.ResetHandoffWritten},
		edge{active: false, phase: "", handoff: ""},
	)
}
