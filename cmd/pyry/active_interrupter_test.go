package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// --- #2103 activeInterrupter composition ---
//
// activeInterrupter had NO unit test before #2103: dispatch_arms_test.go covers
// interruptRunner, the pure dispatcher, and says so in its own header, and
// interrupt_routing_test.go — still cited in the e2e's flow comment — went with
// #1348's deletions. It gets one here because the id it resolves is now
// client-supplied, which turns its arm selection into the ticket's whole security
// surface: every reject row is one of these arms, and each is far cheaper to pin
// here than through a daemon.
//
// Both seams are plain func fields, so the fakes are the seams themselves.
// conversations.ValidID is NOT injected: it is a pure function of a string, so
// feeding a malformed id exercises the real validator rather than a stand-in that
// could disagree with it.

const (
	interrupterConvA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" // the cursor's conversation
	interrupterConvB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" // the conversation the frame names
)

// interruptingRunner is the minimal runner interruptRunner recognises: it exposes
// Interrupt and nothing else, so it takes the armInterrupt arm. It counts calls,
// which is what proves the actuation reached THIS runner rather than merely being
// computed, and returns an injectable error so the no-live-child row can drive the
// production sentinel through the real arm.
//
// childPID carries liveness in State() exactly as streamRunner does — zero until a
// spawned child publishes its pid. It exists so the no-live-child row can offer a
// faithful production shape: a bound conversation whose runner is real and whose
// child has never come up. #2099's twin needed a State() probe for that shape;
// this seam deliberately does not, and the row that pins the difference would be
// worthless against a double that could not express it.
type interruptingRunner struct {
	baseRunner
	childPID int
	err      error
	calls    int
}

func (r *interruptingRunner) State() sessions.State {
	if r.childPID == 0 {
		return sessions.State{}
	}
	return sessions.State{Phase: sessions.PhaseRunning, ChildPID: r.childPID}
}

func (r *interruptingRunner) Interrupt() error {
	r.calls++
	return r.err
}

// liveInterruptingRunner is a bound conversation whose child is up — the ordinary
// production shape for a conversation mid-turn.
func liveInterruptingRunner() *interruptingRunner {
	return &interruptingRunner{childPID: 4242}
}

// interrupterProbe captures what each seam was ASKED, so a test can assert on the
// question rather than only on the answer. Which id reached resolveRunner is the
// single most load-bearing observation in this file: it is the difference between
// stopping the conversation the client named and stopping the cursor's. cursorReads
// is the other half — on a named frame the cursor must not even be consulted, which
// is the structural form of "the cursor does not move".
type interrupterProbe struct {
	resolvedWith []string
	cursorReads  int
	logs         bytes.Buffer
}

// newInterrupter builds an activeInterrupter over the probe. bound maps a
// conversation id to the runner it resolves to; any id absent from it resolves as
// unknown — which is also how an UNBOUND one resolves, exactly as
// resolveBoundRunner refuses both identically and returns (nil, false).
func (p *interrupterProbe) newInterrupter(cursor string, bound map[string]sessions.Runner) activeInterrupter {
	return activeInterrupter{
		currentConv: func() string {
			p.cursorReads++
			return cursor
		},
		resolveRunner: func(convID string) (sessions.Runner, bool) {
			p.resolvedWith = append(p.resolvedWith, convID)
			r, ok := bound[convID]
			if !ok || r == nil {
				return nil, false
			}
			return r, true
		},
		log: slog.New(slog.NewJSONHandler(&p.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

// records decodes the probe's log buffer into one map per record, so an assertion
// can name a field instead of substring-matching a rendered line.
func (p *interrupterProbe) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(p.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log record %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// requireOneRecord asserts the probe captured exactly one record carrying event,
// and returns it. Exactly one, because every arm of SendEsc emits precisely one:
// that is the #1192/#1193 closure an operator relies on ("a wired daemon leaving no
// v2.interrupt.* record means the frame never arrived"), and a duplicate would make
// the record count meaningless as a diagnostic.
func requireOneRecord(t *testing.T, p *interrupterProbe, event string) map[string]any {
	t.Helper()
	recs := p.records(t)
	if len(recs) != 1 {
		t.Fatalf("captured %d records, want exactly 1:\n%s", len(recs), p.logs.String())
	}
	if got := recs[0]["event"]; got != event {
		t.Fatalf("record event = %v, want %q:\n%s", got, event, p.logs.String())
	}
	return recs[0]
}

// TestActiveInterrupter_NamedConversationInterruptsThatOne is AC-1 at the
// composition level: with the cursor parked on A and the frame naming B, the seam
// is asked about B and B alone, B's runner is actuated, and A's is untouched.
//
// The cursor is deliberately set to a DIFFERENT bound conversation rather than left
// empty. Against an empty cursor a regression that ignored the named id would
// resolve "" and land inert, which reads as a pass; against A it would interrupt
// A's own runner, which is the actual defect and reddens here.
//
// The cursor read count is the other assertion, and it is the structural form of
// AC-1's "the cursor does not move": a named frame must not consult the cursor at
// all, so no code path exists on which it could be written. Nothing in this file
// writes it — only sessionRouter.Route does — so a call count of zero is the
// strongest statement this level can make.
func TestActiveInterrupter_NamedConversationInterruptsThatOne(t *testing.T) {
	t.Parallel()

	runnerA, runnerB := liveInterruptingRunner(), liveInterruptingRunner()
	p := &interrupterProbe{}
	a := p.newInterrupter(interrupterConvA, map[string]sessions.Runner{
		interrupterConvA: runnerA,
		interrupterConvB: runnerB,
	})

	if err := a.SendEsc(interrupterConvB); err != nil {
		t.Fatalf("SendEsc(%s) = %v, want nil", interrupterConvB, err)
	}

	if got, want := p.resolvedWith, []string{interrupterConvB}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("resolveRunner asked about %q, want %q — the named conversation and no other", got, want)
	}
	if runnerB.calls != 1 {
		t.Errorf("the NAMED conversation's runner was interrupted %d times, want 1", runnerB.calls)
	}
	if runnerA.calls != 0 {
		t.Errorf("the CURSOR's runner was interrupted %d times, want 0 — this is the #2103 defect", runnerA.calls)
	}
	if p.cursorReads != 0 {
		t.Errorf("the cursor was read %d times on a named frame, want 0", p.cursorReads)
	}

	rec := requireOneRecord(t, p, "v2.interrupt.dispatched")
	if got := rec["conversation_id"]; got != interrupterConvB {
		t.Errorf("record conversation_id = %v, want %q — the record must name the conversation acted on", got, interrupterConvB)
	}
	if got := rec["arm"]; got != string(armInterrupt) {
		t.Errorf("record arm = %v, want %q", got, armInterrupt)
	}
}

// TestActiveInterrupter_UnnamedFollowsTheCursor is AC-2: a frame naming nothing
// behaves exactly as it did before #2103 — the cursor's conversation is interrupted
// — which is what keeps mobile's current bare frame working.
//
// The cursor's own id is deliberately NOT shape-checked, and this test would not
// notice if it were, since the cursor here is canonical. The row that pins the
// ordering is the empty-cursor one in TestActiveInterrupter_InertArms:
// ValidID("") is false, so a shape check placed above the empty-string branch
// would refuse every un-upgraded client's frame.
func TestActiveInterrupter_UnnamedFollowsTheCursor(t *testing.T) {
	t.Parallel()

	runnerA, runnerB := liveInterruptingRunner(), liveInterruptingRunner()
	p := &interrupterProbe{}
	a := p.newInterrupter(interrupterConvA, map[string]sessions.Runner{
		interrupterConvA: runnerA,
		interrupterConvB: runnerB,
	})

	if err := a.SendEsc(""); err != nil {
		t.Fatalf("SendEsc(\"\") = %v, want nil", err)
	}

	if len(p.resolvedWith) != 1 || p.resolvedWith[0] != interrupterConvA {
		t.Errorf("resolveRunner asked about %q, want the cursor's %q", p.resolvedWith, interrupterConvA)
	}
	if runnerA.calls != 1 {
		t.Errorf("the cursor's runner was interrupted %d times, want 1", runnerA.calls)
	}
	if runnerB.calls != 0 {
		t.Errorf("a non-cursor runner was interrupted %d times, want 0", runnerB.calls)
	}
	rec := requireOneRecord(t, p, "v2.interrupt.dispatched")
	if got := rec["conversation_id"]; got != interrupterConvA {
		t.Errorf("record conversation_id = %v, want %q", got, interrupterConvA)
	}
}

// TestActiveInterrupter_InertArms is AC-3's reject set: every ambiguous state takes
// no actuation, emits no error, and leaves exactly one record naming the arm.
//
// wantResolved is asserted as well as the actuation count because an inert return
// alone cannot distinguish "refused before the registry" from "asked the registry
// and it said no". That distinction is the whole point of the shape check: a string
// that is not a canonical conversation id must never reach a lookup at all.
func TestActiveInterrupter_InertArms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		cursor       string
		named        string
		bound        map[string]sessions.Runner
		runner       *interruptingRunner // nil when the row binds no interruptible runner
		wantEvent    string
		wantResolved []string
	}{
		{
			name:      "bare frame with no active conversation",
			cursor:    "",
			named:     "",
			wantEvent: "v2.interrupt.no_active_conv",
			// The registry is never touched: there is no id to look up. An empty id
			// reaching resolveRunner would be resolveBoundRunner's guard away from
			// Pool.Lookup(""), which returns the BOOTSTRAP session (#678).
			wantResolved: nil,
		},
		{
			name:         "named id is not a canonical conversation id",
			cursor:       interrupterConvA,
			named:        "not-a-uuid",
			wantEvent:    "v2.interrupt.invalid_conv_id",
			wantResolved: nil,
		},
		{
			name:         "named id looks canonical but nothing is bound to it",
			cursor:       interrupterConvA,
			named:        interrupterConvB,
			wantEvent:    "v2.interrupt.no_bound_runner",
			wantResolved: []string{interrupterConvB},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := &interrupterProbe{}
			a := p.newInterrupter(tc.cursor, tc.bound)

			if err := a.SendEsc(tc.named); err != nil {
				t.Fatalf("SendEsc(%q) = %v, want nil (every inert arm returns nil)", tc.named, err)
			}
			if len(p.resolvedWith) != len(tc.wantResolved) {
				t.Fatalf("resolveRunner asked about %q, want %q", p.resolvedWith, tc.wantResolved)
			}
			for i, want := range tc.wantResolved {
				if p.resolvedWith[i] != want {
					t.Errorf("resolveRunner call %d asked about %q, want %q", i, p.resolvedWith[i], want)
				}
			}
			requireOneRecord(t, p, tc.wantEvent)
		})
	}
}

// TestActiveInterrupter_BoundRunnerWithNoInterruptMethod is the fourth reject row,
// separated from the table because it needs a runner shape the others do not: a
// bound, resolvable runner that exposes no Interrupt method at all, so
// interruptRunner returns armNone. No actuation beats wrong actuation — the only
// other runner to try would be the bootstrap session's, which is the #678 isolation
// break resolveBoundRunner's guard exists to prevent.
func TestActiveInterrupter_BoundRunnerWithNoInterruptMethod(t *testing.T) {
	t.Parallel()

	p := &interrupterProbe{}
	a := p.newInterrupter(interrupterConvA, map[string]sessions.Runner{
		interrupterConvB: baseRunner{},
	})

	if err := a.SendEsc(interrupterConvB); err != nil {
		t.Fatalf("SendEsc = %v, want nil (armNone always pairs with a nil error)", err)
	}
	rec := requireOneRecord(t, p, "v2.interrupt.no_actuator")
	if got := rec["conversation_id"]; got != interrupterConvB {
		t.Errorf("record conversation_id = %v, want %q", got, interrupterConvB)
	}
}

// TestActiveInterrupter_NoLiveChildIsAttemptedNotGuarded is the pin that keeps
// #2099's liveness probe OUT of this seam, and it is the one row a later reader is
// most likely to "fix" by copying the twin.
//
// #2099 needed `named && runner.State().ChildPID == 0` because RestartFresh on a
// childless runner is observable damage with nothing to show for it: it rekeys the
// pool, persists sessions.json, rebinds the conversation and broadcasts a
// session_transition for a chat that never had a turn. Interrupt's actuator has no
// such hazard — streamsup.WriteInterrupt checks its writer for nil FIRST and
// returns ErrNoLiveChild having written nothing — so a named conversation with no
// live child is inert BY CONSTRUCTION, on the named and cursor paths alike.
//
// Adding the probe here would therefore buy nothing and cost the compatibility
// promise: it would introduce a refusal the bare path does not have today, which is
// exactly the cursor-vs-named disagreement #2099 recorded as its own accepted cost.
// The runner offered here reports ChildPID 0 AND returns the production sentinel,
// so a state probe inserted above the actuation reddens this row on the call count
// while a genuine no-live-child refusal keeps it green.
func TestActiveInterrupter_NoLiveChildIsAttemptedNotGuarded(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		named string
	}{
		{"named frame", interrupterConvB},
		{"bare frame follows the cursor", ""},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// childPID 0 is the never-spawned shape #2085 made ordinary: since then
			// create_conversation binds a session without spawning its child.
			runner := &interruptingRunner{err: streamsup.ErrNoLiveChild}
			cursor := interrupterConvB
			p := &interrupterProbe{}
			a := p.newInterrupter(cursor, map[string]sessions.Runner{interrupterConvB: runner})

			err := a.SendEsc(tc.named)
			if !errors.Is(err, streamsup.ErrNoLiveChild) {
				t.Errorf("SendEsc = %v, want the actuation's own %v propagated for handleInterrupt to tolerate",
					err, streamsup.ErrNoLiveChild)
			}
			if runner.calls != 1 {
				t.Fatalf("Interrupt called %d times, want 1: the actuation must be ATTEMPTED. A liveness "+
					"guard copied from #2099's activeSessionStarter would short-circuit here and, applied "+
					"to the bare frame, would change the pre-#2103 behaviour AC-2 exists to preserve", runner.calls)
			}
			// The dispatched record is emitted even though the arm returned an error:
			// it names WHICH arm ran, not that the child quiesced, and handleInterrupt's
			// keystroke_err carries the error but not the arm.
			requireOneRecord(t, p, "v2.interrupt.dispatched")
		})
	}
}

// TestActiveInterrupter_BoundsLoggedConversationID is the log-hygiene pin. The
// shape-check-failure arm is the ONLY place an arbitrary client-chosen string
// reaches a log call on this path — every arm past conversations.ValidID logs a
// string that is provably 36 bytes — and it is bounded upstream only by the
// application-envelope byte cap, which a paired client may fill at will.
//
// Both halves of boundedConvID matter and both are asserted: the truncation, and
// the elision marker without which a cut id reads as a complete one and sends a
// reader chasing a conversation that was never named.
func TestActiveInterrupter_BoundsLoggedConversationID(t *testing.T) {
	t.Parallel()

	hostile := strings.Repeat("z", maxLoggedConvID*4)
	p := &interrupterProbe{}
	a := p.newInterrupter(interrupterConvA, nil)

	if err := a.SendEsc(hostile); err != nil {
		t.Fatalf("SendEsc = %v, want nil", err)
	}

	rec := requireOneRecord(t, p, "v2.interrupt.invalid_conv_id")
	got, _ := rec["conversation_id"].(string)
	if got == hostile {
		t.Fatalf("record carried the raw %d-byte id verbatim; it must be bounded", len(hostile))
	}
	if len(got) > maxLoggedConvID+len("…(truncated)") {
		t.Errorf("record conversation_id is %d bytes, want at most %d + the elision marker",
			len(got), maxLoggedConvID)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("record conversation_id = %q, want an elision marker so a cut id does not read as a whole one", got)
	}
}
